// Package prompts is the prompt library: saved prompts a user inserts in the
// composer by typing /command. Routes follow the Open WebUI shapes.
package prompts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

func init() { app.Register("prompts", Mount) }

var (
	errNotFound  = api.Errf(http.StatusNotFound, "not_found", "Prompt not found")
	errForbidden = api.Errf(http.StatusForbidden, "forbidden", "You do not have write access to this prompt")
	errTaken     = api.Errf(http.StatusConflict, "command_taken", "You already have a prompt with this command")
	commandRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// Prompt is one prompt row.
type Prompt struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Command   string    `json:"command"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

type handler struct{ db *pgxpool.Pool }

// Mount registers /api/v1/prompts.
func Mount(a *app.App) error {
	h := &handler{db: a.DB}
	for p, f := range map[string]http.HandlerFunc{
		"GET /api/v1/prompts/{$}":               h.list,
		"GET /api/v1/prompts":                   h.list,
		"POST /api/v1/prompts/create":           h.create,
		"GET /api/v1/prompts/id/{id}":           h.get,
		"POST /api/v1/prompts/id/{id}/update":   h.update,
		"DELETE /api/v1/prompts/id/{id}/delete": h.delete,
		"GET /api/v1/prompts/command/{command}": h.byCommand,
		"GET /api/v1/prompts/id/{id}/grants":    h.grants,
		"PUT /api/v1/prompts/id/{id}/grants":    h.setGrants,
	} {
		a.Server.Handle(p, a.Auth.Required(f)) // CSRF: see auth.Credential
	}
	return nil
}

const cols = `id, user_id, command, title, content, created_at, updated_at`

func scan(row pgx.Row) (*Prompt, error) {
	p := &Prompt{}
	err := row.Scan(&p.ID, &p.UserID, &p.Command, &p.Title, &p.Content, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan prompt: %w", err)
	}
	return p, nil
}

// json is the Open WebUI-compatible shape (name = title) plus write_access.
func (p *Prompt) json(write bool) map[string]any {
	return map[string]any{"id": p.ID, "user_id": p.UserID, "command": p.Command, "title": p.Title, "name": p.Title,
		"content": p.Content, "created_at": p.CreatedAt.Unix(), "updated_at": p.UpdatedAt.Unix(),
		"timestamp": p.UpdatedAt.Unix(), "write_access": write}
}

// can: owner and admins always; others need a grant.
func (h *handler) can(ctx context.Context, u *auth.User, p *Prompt, perm string) (bool, error) {
	if p.UserID == u.ID {
		return true, nil
	}
	return access.Can(ctx, h.db, u, access.Prompt, p.ID, perm)
}

// load returns the prompt when u holds perm: 404 when u cannot read it, 403 otherwise.
func (h *handler) load(ctx context.Context, u *auth.User, id, perm string) (*Prompt, error) {
	p, err := scan(h.db.QueryRow(ctx, `SELECT `+cols+` FROM prompt WHERE id = $1`, id))
	if err != nil {
		return nil, err
	}
	for _, x := range []string{access.Read, perm} {
		ok, err := h.can(ctx, u, p, x)
		if err != nil {
			return nil, err
		}
		if !ok && x == access.Read {
			return nil, errNotFound
		}
		if !ok {
			return nil, errForbidden
		}
	}
	return p, nil
}

// visible lists prompts u may read: own first, then shared, by command.
func (h *handler) visible(ctx context.Context, u *auth.User) ([]*Prompt, []string, bool, error) {
	readable, all, err := access.IDs(ctx, h.db, u, access.Prompt, access.Read)
	if err != nil {
		return nil, nil, false, err
	}
	writable, _, err := access.IDs(ctx, h.db, u, access.Prompt, access.Write)
	if err != nil {
		return nil, nil, false, err
	}
	if readable == nil {
		readable = []string{}
	}
	rows, err := h.db.Query(ctx, `SELECT `+cols+` FROM prompt WHERE $1 OR user_id = $2 OR id = ANY($3)
		ORDER BY user_id <> $2, command, id`, all, u.ID, readable)
	if err != nil {
		return nil, nil, false, fmt.Errorf("list prompts: %w", err)
	}
	defer rows.Close()
	var out []*Prompt
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, nil, false, err
		}
		out = append(out, p)
	}
	return out, writable, all, rows.Err()
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	ps, writable, all, err := h.visible(r.Context(), u)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.json(all || p.UserID == u.ID || slices.Contains(writable, p.ID)))
	}
	api.JSON(w, http.StatusOK, out)
}

type form struct {
	Command *string `json:"command"`
	Title   *string `json:"title"`
	Name    *string `json:"name"` // Open WebUI calls the title name
	Content *string `json:"content"`
}

// normalize validates the fields present; the command loses a leading "/".
func (f *form) normalize() error {
	if f.Title == nil {
		f.Title = f.Name
	}
	if f.Command != nil {
		c := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(*f.Command), "/"))
		if !commandRe.MatchString(c) {
			return api.Errf(http.StatusBadRequest, "bad_command", "command must be 1-64 letters, digits, - or _")
		}
		f.Command = &c
	}
	if f.Title != nil {
		t := strings.TrimSpace(*f.Title)
		f.Title = &t
	}
	return nil
}

func taken(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return errTaken
	}
	return err
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	if !u.Can("workspace.prompts") {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "forbidden", "Permission required: workspace.prompts"))
		return
	}
	var f form
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := f.normalize(); err != nil {
		api.Fail(w, r, err)
		return
	}
	if f.Command == nil || f.Content == nil {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_request", "command and content are required"))
		return
	}
	title := *f.Command
	if f.Title != nil && *f.Title != "" {
		title = *f.Title
	}
	p, err := scan(h.db.QueryRow(ctx, `INSERT INTO prompt (id, user_id, command, title, content) VALUES ($1, $2, $3, $4, $5)
		RETURNING `+cols, ids.New(), u.ID, *f.Command, title, *f.Content))
	if err != nil {
		api.Fail(w, r, taken(err))
		return
	}
	api.JSON(w, http.StatusOK, p.json(true))
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	p, err := h.load(ctx, u, r.PathValue("id"), access.Read)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	write, err := h.can(ctx, u, p, access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, p.json(write))
}

// byCommand finds a prompt by command: the caller's own first, then a shared one.
func (h *handler) byCommand(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	c := strings.ToLower(strings.TrimPrefix(r.PathValue("command"), "/"))
	ps, writable, all, err := h.visible(ctx, u)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	for _, p := range ps {
		if p.Command == c {
			api.JSON(w, http.StatusOK, p.json(all || p.UserID == u.ID || slices.Contains(writable, p.ID)))
			return
		}
	}
	api.Fail(w, r, errNotFound)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	p, err := h.load(ctx, u, r.PathValue("id"), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var f form
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := f.normalize(); err != nil {
		api.Fail(w, r, err)
		return
	}
	for dst, v := range map[*string]*string{&p.Command: f.Command, &p.Title: f.Title, &p.Content: f.Content} {
		if v != nil {
			*dst = *v
		}
	}
	p, err = scan(h.db.QueryRow(ctx, `UPDATE prompt SET command = $2, title = $3, content = $4, updated_at = now()
		WHERE id = $1 RETURNING `+cols, p.ID, p.Command, p.Title, p.Content))
	if err != nil {
		api.Fail(w, r, taken(err))
		return
	}
	api.JSON(w, http.StatusOK, p.json(true))
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	p, err := h.load(ctx, u, r.PathValue("id"), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := h.db.Exec(ctx, `WITH d AS (DELETE FROM prompt WHERE id = $1)
		DELETE FROM access_grant WHERE resource_type = 'prompt' AND resource_id = $1`, p.ID); err != nil {
		api.Fail(w, r, fmt.Errorf("delete prompt: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, true)
}

func (h *handler) grants(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	p, err := h.load(ctx, u, r.PathValue("id"), access.Read)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	rows, err := h.db.Query(ctx, `SELECT principal_type || ':' || principal_id, permission FROM access_grant
		WHERE resource_type = 'prompt' AND resource_id = $1 ORDER BY 1, 2`, p.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	gs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (access.Grant, error) {
		var g access.Grant
		return g, row.Scan(&g.Principal, &g.Permission)
	})
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if gs == nil {
		gs = []access.Grant{}
	}
	api.JSON(w, http.StatusOK, gs)
}

func (h *handler) setGrants(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	p, err := h.load(ctx, u, r.PathValue("id"), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var gs []access.Grant
	if err := api.Decode(r, &gs); err != nil {
		api.Fail(w, r, err)
		return
	}
	for _, g := range gs {
		if _, _, err := access.ParsePrincipal(g.Principal); err != nil {
			api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_principal", err.Error()))
			return
		}
		if g.Permission != "" && g.Permission != access.Read && g.Permission != access.Write {
			api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_permission", "permission must be read or write"))
			return
		}
	}
	if err := access.Replace(ctx, h.db, access.Prompt, p.ID, gs, false); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.grants(w, r)
}
