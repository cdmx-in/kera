package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/ids"
)

type groupOut struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Permissions json.RawMessage `json:"permissions"`
	MemberCount int             `json:"member_count"`
	CreatedAt   int64           `json:"created_at"`
	UpdatedAt   int64           `json:"updated_at"`
	UserIDs     *[]string       `json:"user_ids,omitempty"` // set on single-group reads
}

const groupSelect = `SELECT g.id, g.name, g.description, g.permissions,
	(SELECT count(*) FROM group_member m WHERE m.group_id = g.id),
	extract(epoch FROM g.created_at)::bigint, extract(epoch FROM g.updated_at)::bigint
	FROM "group" g `

func scanGroup(row pgx.Row) (groupOut, error) {
	var g groupOut
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.Permissions, &g.MemberCount, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

func (h *handler) listGroups(w http.ResponseWriter, r *http.Request) {
	rows, err := h.a.DB.Query(r.Context(), groupSelect+`ORDER BY lower(g.name)`)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list groups: %w", err))
		return
	}
	defer rows.Close()
	out := []groupOut{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out)
}

// group returns one group with its member ids.
func (h *handler) group(ctx context.Context, id string) (groupOut, error) {
	g, err := scanGroup(h.a.DB.QueryRow(ctx, groupSelect+`WHERE g.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return g, api.Errf(http.StatusNotFound, "not_found", "Group not found")
	}
	if err != nil {
		return g, fmt.Errorf("load group: %w", err)
	}
	rows, err := h.a.DB.Query(ctx, `SELECT user_id FROM group_member WHERE group_id = $1 ORDER BY user_id`, id)
	if err != nil {
		return g, fmt.Errorf("load members: %w", err)
	}
	members, err := pgx.CollectRows(rows, pgx.RowTo[string])
	members = nonNil(members)
	g.UserIDs = &members
	return g, err
}

func (h *handler) writeGroup(w http.ResponseWriter, r *http.Request, id string) {
	g, err := h.group(r.Context(), id)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, g)
}

type groupForm struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Permissions json.RawMessage `json:"permissions"`
}

func (f *groupForm) validate() error {
	if f.Name != nil {
		*f.Name = strings.TrimSpace(*f.Name)
		if *f.Name == "" {
			return api.Errf(http.StatusBadRequest, "bad_name", "Group name cannot be empty")
		}
	}
	if len(f.Permissions) > 0 && string(f.Permissions) != "null" {
		var m map[string]any
		if json.Unmarshal(f.Permissions, &m) != nil {
			return api.Errf(http.StatusBadRequest, "bad_permissions", "permissions must be a JSON object")
		}
	} else {
		f.Permissions = nil
	}
	return nil
}

func dupName(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return api.Errf(http.StatusConflict, "duplicate", "A group with that name already exists")
	}
	return err
}

func (h *handler) createGroup(w http.ResponseWriter, r *http.Request) {
	var f groupForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if f.Name == nil {
		f.Name = new(string)
	}
	if err := f.validate(); err != nil {
		api.Fail(w, r, err)
		return
	}
	perms := f.Permissions
	if perms == nil {
		perms = json.RawMessage(`{}`)
	}
	desc := ""
	if f.Description != nil {
		desc = *f.Description
	}
	id := ids.New()
	_, err := h.a.DB.Exec(r.Context(), `INSERT INTO "group" (id, name, description, permissions) VALUES ($1, $2, $3, $4)`,
		id, *f.Name, desc, perms)
	if err != nil {
		api.Fail(w, r, dupName(fmt.Errorf("create group: %w", err)))
		return
	}
	h.writeGroup(w, r, id)
}

func (h *handler) getGroup(w http.ResponseWriter, r *http.Request) {
	h.writeGroup(w, r, r.PathValue("id"))
}

func (h *handler) updateGroup(w http.ResponseWriter, r *http.Request) {
	var f groupForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := f.validate(); err != nil {
		api.Fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	tag, err := h.a.DB.Exec(r.Context(), `UPDATE "group" SET name = COALESCE($2, name), description = COALESCE($3, description),
		permissions = COALESCE($4, permissions), updated_at = now() WHERE id = $1`, id, f.Name, f.Description, f.Permissions)
	if err != nil {
		api.Fail(w, r, dupName(fmt.Errorf("update group: %w", err)))
		return
	}
	if tag.RowsAffected() == 0 {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "Group not found"))
		return
	}
	h.writeGroup(w, r, id)
}

func (h *handler) addMembers(w http.ResponseWriter, r *http.Request) {
	h.changeMembers(w, r, `INSERT INTO group_member (group_id, user_id)
		SELECT $1, u.id FROM "user" u WHERE u.id = ANY($2) ON CONFLICT DO NOTHING`)
}

func (h *handler) removeMembers(w http.ResponseWriter, r *http.Request) {
	h.changeMembers(w, r, `DELETE FROM group_member WHERE group_id = $1 AND user_id = ANY($2)`)
}

// changeMembers runs q with ($1 group id, $2 user ids); unknown user ids are ignored.
func (h *handler) changeMembers(w http.ResponseWriter, r *http.Request, q string) {
	var body struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if _, err := h.group(r.Context(), id); err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := h.a.DB.Exec(r.Context(), q, id, nonNil(body.UserIDs)); err != nil {
		api.Fail(w, r, fmt.Errorf("change members: %w", err))
		return
	}
	_, _ = h.a.DB.Exec(r.Context(), `UPDATE "group" SET updated_at = now() WHERE id = $1`, id)
	h.writeGroup(w, r, id)
}

func (h *handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	tx, err := h.a.DB.Begin(ctx)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM "group" WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() > 0 {
		// Grants point at groups by id without a foreign key; drop them too.
		_, err = tx.Exec(ctx, `DELETE FROM access_grant WHERE principal_type = 'group' AND principal_id = $1`, id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("delete group: %w", err))
		return
	}
	if tag.RowsAffected() == 0 {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "Group not found"))
		return
	}
	api.JSON(w, http.StatusOK, true)
}

var resourceTypes = map[string]bool{access.Model: true, access.ToolServer: true, access.Knowledge: true,
	access.Prompt: true, access.Chat: true, access.Folder: true}

func grantTarget(r *http.Request) (typ, id string, err error) {
	typ, id = r.PathValue("type"), r.PathValue("id")
	if !resourceTypes[typ] {
		return "", "", api.Errf(http.StatusBadRequest, "bad_type", "Unknown resource type "+typ)
	}
	return typ, id, nil
}

func (h *handler) getGrants(w http.ResponseWriter, r *http.Request) {
	typ, id, err := grantTarget(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	rows, err := h.a.DB.Query(r.Context(), `SELECT principal_type || ':' || principal_id, permission FROM access_grant
		WHERE resource_type = $1 AND resource_id = $2 ORDER BY principal_type, principal_id, permission`, typ, id)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list grants: %w", err))
		return
	}
	grants, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (access.Grant, error) {
		var g access.Grant
		return g, row.Scan(&g.Principal, &g.Permission)
	})
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list grants: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, nonNil(grants))
}

// putGrants replaces every grant of the resource, including ones `kera apply`
// created: an admin edit takes the resource out of managed control (the
// new rows are managed=false, so a later apply adds its rows back next to
// them rather than deleting them).
func (h *handler) putGrants(w http.ResponseWriter, r *http.Request) {
	typ, id, err := grantTarget(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var grants []access.Grant
	if err := api.Decode(r, &grants); err != nil {
		api.Fail(w, r, err)
		return
	}
	for _, g := range grants {
		if _, _, err := access.ParsePrincipal(g.Principal); err != nil {
			api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_principal", err.Error()))
			return
		}
		if g.Permission != access.Read && g.Permission != access.Write {
			api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_permission", "permission must be read or write"))
			return
		}
	}
	if err := access.Replace(r.Context(), h.a.DB, typ, id, grants, false); err != nil {
		api.Fail(w, r, fmt.Errorf("replace grants: %w", err))
		return
	}
	h.getGrants(w, r)
}
