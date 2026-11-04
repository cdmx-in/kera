package skills

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/tools"
)

func init() { app.Register("skills", Mount) }

// maxUpload caps an uploaded skills zip.
const maxUpload = 64 << 20

var errNotFound = api.Errf(http.StatusNotFound, "not_found", "Skill not found")

// Mount registers the tool set and the admin routes under /api/v1/skills.
func Mount(a *app.App) error {
	tools.Register(&Provider{DB: a.DB})
	h := &handler{db: a.DB}
	for p, f := range map[string]http.HandlerFunc{
		"GET /api/v1/skills/{$}":                    h.list,
		"GET /api/v1/skills":                        h.list,
		"POST /api/v1/skills/import":                h.importZip,
		"GET /api/v1/skills/id/{id}":                h.get,
		"POST /api/v1/skills/id/{id}/update":        h.update,
		"DELETE /api/v1/skills/id/{id}/delete":      h.delete,
		"GET /api/v1/skills/id/{id}/file/{path...}": h.file,
	} {
		a.Server.Handle(p, a.Auth.Admin(f))
	}
	return nil
}

type handler struct{ db *pgxpool.Pool }

func rowJSON(r Row) map[string]any {
	return map[string]any{"id": r.ID, "name": r.Name, "description": r.Description, "enabled": r.Enabled,
		"lazy": r.Lazy, "managed": r.Managed, "files": r.Files, "created_at": r.CreatedAt.Unix(), "updated_at": r.UpdatedAt.Unix()}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := All(r.Context(), h.db)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		out = append(out, rowJSON(s))
	}
	api.JSON(w, http.StatusOK, out)
}

func (h *handler) load(r *http.Request) (*Stored, error) {
	s, err := Get(r.Context(), h.db, r.PathValue("id"))
	if errors.Is(err, ErrNotFound) || err == nil && s.ID != r.PathValue("id") {
		return nil, errNotFound
	}
	return s, err
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	s, err := h.load(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	files := make([]string, 0, len(s.Resources))
	for k := range s.Resources {
		files = append(files, k)
	}
	slices.Sort(files)
	api.JSON(w, http.StatusOK, map[string]any{"id": s.ID, "name": s.Name, "description": s.Description,
		"content": s.Content, "files": files, "enabled": s.Enabled, "lazy": s.Lazy, "managed": s.Managed})
}

func (h *handler) file(w http.ResponseWriter, r *http.Request) {
	s, err := h.load(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	txt, ok := s.Resources[r.PathValue("path")]
	if !ok {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "File not found"))
		return
	}
	api.JSON(w, http.StatusOK, map[string]string{"path": r.PathValue("path"), "content": txt})
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	s, err := h.load(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var f struct {
		Enabled *bool `json:"enabled"`
		Lazy    *bool `json:"lazy"`
	}
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := h.db.Exec(r.Context(), `UPDATE skill SET enabled = coalesce($2, enabled), lazy = coalesce($3, lazy),
		updated_at = now() WHERE id = $1`, s.ID, f.Enabled, f.Lazy); err != nil {
		api.Fail(w, r, fmt.Errorf("update skill: %w", err))
		return
	}
	h.get(w, r)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	err := Delete(r.Context(), h.db, r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		err = errNotFound
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, true)
}

// importZip takes a multipart upload (field "file") of a skills zip.
func (h *handler) importZip(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	fh, hdr, err := r.FormFile("file")
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_request", "upload a .zip in the form field \"file\" (at most 64 MB)"))
		return
	}
	defer fh.Close()
	b, err := io.ReadAll(fh)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_zip", "not a zip file"))
		return
	}
	list, warns, err := Parse(z, strings.TrimSuffix(path.Base(hdr.Filename), ".zip"))
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_zip", err.Error()))
		return
	}
	if len(list) == 0 {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "no_skills", "the zip holds no SKILL.md"))
		return
	}
	res, err := Import(r.Context(), h.db, list, nil)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if warns == nil {
		warns = []string{}
	}
	api.JSON(w, http.StatusOK, map[string]any{"results": res, "warnings": warns})
}

// Result is the outcome of importing one skill.
type Result struct {
	Name    string `json:"name"`
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
}

// Import upserts every skill in one transaction (not managed) and adds the
// grants (principals as access.ParsePrincipal reads them, group ids) to each.
// Existing grants are kept.
func Import(ctx context.Context, db *pgxpool.Pool, list []Skill, grants []access.Grant) ([]Result, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var out []Result
	for _, s := range list {
		id, outcome, err := Upsert(ctx, tx, s, false)
		if err != nil {
			return nil, err
		}
		for _, g := range grants {
			ptype, pid, err := access.ParsePrincipal(g.Principal)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission)
				VALUES ($1, 'skill', $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
				ids.New(), id, ptype, pid, cmp.Or(g.Permission, access.Read)); err != nil {
				return nil, fmt.Errorf("grant skill %s: %w", s.Name, err)
			}
		}
		out = append(out, Result{Name: s.Name, ID: id, Outcome: outcome})
	}
	return out, tx.Commit(ctx)
}

// resolveGroup is for the CLI: "group:<name>" becomes "group:<id>".
func resolveGroup(ctx context.Context, db *pgxpool.Pool, p string) (string, error) {
	name, ok := strings.CutPrefix(p, "group:")
	if !ok {
		return p, nil
	}
	var id string
	err := db.QueryRow(ctx, `SELECT id FROM "group" WHERE id = $1 OR name = $1 ORDER BY id = $1 DESC LIMIT 1`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("unknown group %q", name)
	}
	return "group:" + id, err
}
