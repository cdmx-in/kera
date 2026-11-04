package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
)

const usersPerPage = 30

type userOut struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Email           string          `json:"email"`
	Role            string          `json:"role"`
	ProfileImageURL string          `json:"profile_image_url"`
	LastActiveAt    int64           `json:"last_active_at"`
	CreatedAt       int64           `json:"created_at"`
	Groups          json.RawMessage `json:"groups"` // [{id, name}]
	APIKeyCount     int             `json:"api_key_count"`
}

const userSelect = `SELECT u.id, u.name, u.email, u.role, u.profile_image_url,
	extract(epoch FROM u.last_active_at)::bigint, extract(epoch FROM u.created_at)::bigint,
	COALESCE((SELECT json_agg(json_build_object('id', g.id, 'name', g.name) ORDER BY g.name)
		FROM group_member m JOIN "group" g ON g.id = m.group_id WHERE m.user_id = u.id), '[]'),
	(SELECT count(*) FROM api_key k WHERE k.user_id = u.id)
	FROM "user" u `

func scanUser(row pgx.Row) (userOut, error) {
	var u userOut
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.ProfileImageURL, &u.LastActiveAt, &u.CreatedAt, &u.Groups, &u.APIKeyCount)
	return u, err
}

func (h *handler) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("query"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	const match = `($1 = '' OR strpos(lower(u.name), lower($1)) > 0 OR strpos(lower(u.email), lower($1)) > 0)`
	var total int
	if err := h.a.DB.QueryRow(ctx, `SELECT count(*) FROM "user" u WHERE `+match, q).Scan(&total); err != nil {
		api.Fail(w, r, fmt.Errorf("count users: %w", err))
		return
	}
	rows, err := h.a.DB.Query(ctx, userSelect+`WHERE `+match+` ORDER BY u.created_at, u.id LIMIT $2 OFFSET $3`,
		q, usersPerPage, (page-1)*usersPerPage)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list users: %w", err))
		return
	}
	defer rows.Close()
	users := []userOut{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"users": users, "total": total})
}

func (h *handler) loadUser(ctx context.Context, id string) (userOut, error) {
	u, err := scanUser(h.a.DB.QueryRow(ctx, userSelect+`WHERE u.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, api.Errf(http.StatusNotFound, "not_found", "User not found")
	}
	if err != nil {
		return u, fmt.Errorf("load user: %w", err)
	}
	return u, nil
}

func (h *handler) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.loadUser(r.Context(), r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, u)
}

func (h *handler) updateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name *string `json:"name"`
		Role *string `json:"role"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	me := auth.FromContext(r.Context())
	if err := h.applyUserUpdate(r.Context(), me.ID, id, body.Name, body.Role); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.getUser(w, r)
}

func (h *handler) applyUserUpdate(ctx context.Context, meID, id string, name, role *string) error {
	tx, err := h.a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// One lock for every role change, so two admins demoting each other
	// at the same moment cannot leave zero admins.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kera.roles'))`); err != nil {
		return fmt.Errorf("role lock: %w", err)
	}
	var current string
	err = tx.QueryRow(ctx, `SELECT role FROM "user" WHERE id = $1`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.Errf(http.StatusNotFound, "not_found", "User not found")
	}
	if err != nil {
		return fmt.Errorf("load user: %w", err)
	}
	if role != nil && *role != current {
		if err := checkRoleChange(ctx, tx, meID, id, current, *role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE "user" SET role = $2, updated_at = now() WHERE id = $1`, id, *role); err != nil {
			return fmt.Errorf("update role: %w", err)
		}
	}
	if name != nil {
		n := strings.TrimSpace(*name)
		if n == "" {
			return api.Errf(http.StatusBadRequest, "bad_name", "Name cannot be empty")
		}
		if _, err := tx.Exec(ctx, `UPDATE "user" SET name = $2, updated_at = now() WHERE id = $1`, id, n); err != nil {
			return fmt.Errorf("update name: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func checkRoleChange(ctx context.Context, tx pgx.Tx, meID, id, current, next string) error {
	if next != "admin" && next != "user" && next != "pending" {
		return api.Errf(http.StatusBadRequest, "bad_role", "Role must be admin, user or pending")
	}
	if id == meID {
		return api.Errf(http.StatusForbidden, "own_role", "You cannot change your own role")
	}
	if current != "admin" {
		return nil
	}
	var admins int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE role = 'admin'`).Scan(&admins); err != nil {
		return fmt.Errorf("count admins: %w", err)
	}
	if admins <= 1 {
		return api.Errf(http.StatusConflict, "last_admin", "Cannot demote the last admin")
	}
	return nil
}

func (h *handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == auth.FromContext(r.Context()).ID {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "self_delete", "You cannot delete yourself"))
		return
	}
	// The caller is an admin and not the target, so an admin always remains.
	tag, err := h.a.DB.Exec(r.Context(), `DELETE FROM "user" WHERE id = $1`, id)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("delete user: %w", err))
		return
	}
	if tag.RowsAffected() == 0 {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "User not found"))
		return
	}
	api.JSON(w, http.StatusOK, true)
}

func (h *handler) getDefaultPerms(w http.ResponseWriter, r *http.Request) {
	v, err := h.a.Settings.Raw(r.Context(), "user.permissions")
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, v)
}

func (h *handler) setDefaultPerms(w http.ResponseWriter, r *http.Request) {
	v, err := decodeObject(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := h.a.Settings.Set(r.Context(), "user.permissions", v, false); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, v)
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	var s json.RawMessage
	err := h.a.DB.QueryRow(r.Context(), `SELECT settings FROM "user" WHERE id = $1`, auth.FromContext(r.Context()).ID).Scan(&s)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("read settings: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, s)
}

// updateSettings merges the body's top-level keys into the user's settings.
func (h *handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	patch, err := decodeObject(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var s json.RawMessage
	err = h.a.DB.QueryRow(r.Context(), `UPDATE "user" SET settings = settings || $2::jsonb, updated_at = now()
		WHERE id = $1 RETURNING settings`, auth.FromContext(r.Context()).ID, patch).Scan(&s)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("update settings: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, s)
}

// profileImage redirects to an http(s) picture, decodes a data: URL, or
// draws an initial-letter avatar.
func (h *handler) profileImage(w http.ResponseWriter, r *http.Request) {
	var id, name, email, img string
	err := h.a.DB.QueryRow(r.Context(), `SELECT id, name, email, profile_image_url FROM "user" WHERE id = $1`, r.PathValue("id")).
		Scan(&id, &name, &email, &img)
	if errors.Is(err, pgx.ErrNoRows) {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "User not found"))
		return
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("load user: %w", err))
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if strings.HasPrefix(img, "https://") || strings.HasPrefix(img, "http://") {
		http.Redirect(w, r, img, http.StatusFound)
		return
	}
	if ct, data, ok := decodeDataURL(img); ok {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(initialAvatar(id, name, email)))
}

// decodeDataURL accepts base64 data: URLs of raster images only (an SVG
// served from our origin could run script).
func decodeDataURL(s string) (contentType string, data []byte, ok bool) {
	meta, payload, found := strings.Cut(strings.TrimPrefix(s, "data:"), ",")
	if !found || !strings.HasPrefix(s, "data:") || !strings.HasSuffix(meta, ";base64") {
		return "", nil, false
	}
	ct := strings.TrimSuffix(meta, ";base64")
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", nil, false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	return ct, data, err == nil
}

func initialAvatar(id, name, email string) string {
	label := strings.TrimSpace(name)
	if label == "" {
		label = email
	}
	r, _ := utf8.DecodeRuneInString(label)
	if r == utf8.RuneError {
		r = '?'
	}
	f := fnv.New32a()
	f.Write([]byte(id))
	hue := f.Sum32() % 360
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="96" height="96" viewBox="0 0 96 96">`+
		`<rect width="96" height="96" fill="hsl(%d,45%%,45%%)"/>`+
		`<text x="48" y="50" dy=".35em" text-anchor="middle" font-family="system-ui,sans-serif" font-size="44" fill="#fff">%s</text></svg>`,
		hue, html.EscapeString(string(unicode.ToUpper(r))))
}
