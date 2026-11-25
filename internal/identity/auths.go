package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/audit"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

// me returns the current user. A browser session also gets a fresh token
// and cookie, so long-lived tabs (and cookie-authenticated iframes) stay in.
func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	out := h.meBody(r.Context(), u)
	if !u.ViaAPIKey {
		ttl := h.a.Auth.SessionTTL(r.Context())
		tok, err := auth.IssueSession(h.a.Keys, u.ID, u.SessionEpoch, ttl)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		h.a.Auth.SetSessionCookie(w, tok, ttl)
		out["token"], out["token_type"], out["expires_at"] = tok, "Bearer", time.Now().Add(ttl).Unix()
	}
	api.JSON(w, http.StatusOK, out)
}

// meBody is the user as GET /api/v1/auths/ and the sign-in routes show them.
// has_password and password_login tell the settings page whether to offer a
// password change.
func (h *handler) meBody(ctx context.Context, u *auth.User) map[string]any {
	var has bool
	if err := h.a.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_password WHERE user_id = $1)`, u.ID).Scan(&has); err != nil {
		slog.Warn("read has_password", "err", err)
	}
	return map[string]any{"id": u.ID, "email": u.Email, "name": u.Name, "role": u.Role,
		"profile_image_url": u.ProfileImageURL, "permissions": u.Permissions, "groups": nonNil(u.Groups),
		"has_password": has, "password_login": h.passwordEnabled(ctx)}
}

func (h *handler) signout(w http.ResponseWriter, r *http.Request) {
	if u, err := h.a.Auth.Resolve(r); err == nil && !u.ViaAPIKey { // nobody signed in: nothing worth a record
		audit.Record(auth.WithUser(r.Context(), u), audit.Event{Action: "auth.sign_out"})
	}
	h.a.Auth.ClearSessionCookie(w)
	api.JSON(w, http.StatusOK, map[string]any{"status": true})
}

// providers is public: the sign-in page lists these buttons.
func (h *handler) providers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.a.DB.Query(r.Context(), `SELECT id, options FROM oauth_provider WHERE options @> '{"login": true}' ORDER BY id`)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list providers: %w", err))
		return
	}
	defer rows.Close()
	list := []map[string]string{}
	for rows.Next() {
		var id string
		var opts map[string]any
		if err := rows.Scan(&id, &opts); err != nil {
			api.Fail(w, r, err)
			return
		}
		list = append(list, map[string]string{"id": id, "name": providerName(id, opts)})
	}
	if err := rows.Err(); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"providers": list, "login_form": h.passwordEnabled(r.Context())})
}

func providerName(id string, opts map[string]any) string {
	if n, _ := opts["name"].(string); n != "" {
		return n
	}
	if id == "" {
		return id
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// APIKey is an api_key row without its hash.
type APIKey struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt *int64 `json:"last_used_at"`
}

// CreateAPIKey stores a new key for userID and returns its id and the key,
// which is never stored or shown again.
func CreateAPIKey(ctx context.Context, db *pgxpool.Pool, userID, name string) (id, key string, err error) {
	if name == "" {
		name = "API key"
	}
	key, hash, prefix := auth.NewAPIKey()
	id = ids.New()
	_, err = db.Exec(ctx, `INSERT INTO api_key (id, user_id, key_hash, prefix, name) VALUES ($1, $2, $3, $4, $5)`,
		id, userID, hash, prefix, name)
	if err != nil {
		return "", "", fmt.Errorf("create api key: %w", err)
	}
	return id, key, nil
}

// ListAPIKeys returns userID's keys, newest first.
func ListAPIKeys(ctx context.Context, db *pgxpool.Pool, userID string) ([]APIKey, error) {
	rows, err := db.Query(ctx, `SELECT id, name, prefix, extract(epoch FROM created_at)::bigint,
		extract(epoch FROM last_used_at)::bigint
		FROM api_key WHERE user_id = $1 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	keys := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (h *handler) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := ListAPIKeys(r.Context(), h.a.DB, auth.FromContext(r.Context()).ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, keys)
}

func (h *handler) createKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	id, key, err := CreateAPIKey(r.Context(), h.a.DB, auth.FromContext(r.Context()).ID, body.Name)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	name := body.Name
	if name == "" {
		name = "API key"
	}
	audit.Record(r.Context(), audit.Event{Action: "apikey.create", TargetID: id, TargetLabel: name})
	api.JSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "key": key, "prefix": auth.KeyPrefix(key)})
}

func (h *handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	var name string
	err := h.a.DB.QueryRow(r.Context(), `DELETE FROM api_key WHERE id = $1 AND user_id = $2 RETURNING name`,
		r.PathValue("id"), auth.FromContext(r.Context()).ID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "API key not found"))
		return
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("delete api key: %w", err))
		return
	}
	audit.Record(r.Context(), audit.Event{Action: "apikey.revoke", TargetID: r.PathValue("id"), TargetLabel: name})
	api.JSON(w, http.StatusOK, true)
}

// Open WebUI clients know one key per user: GET shows the newest key's
// prefix, POST rotates the key it manages (named compatKeyName) and leaves
// named keys alone, DELETE removes every key of the user.
const compatKeyName = "API key (legacy endpoint)"

func (h *handler) compatGetKey(w http.ResponseWriter, r *http.Request) {
	keys, err := ListAPIKeys(r.Context(), h.a.DB, auth.FromContext(r.Context()).ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if len(keys) == 0 {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "API key not found"))
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"api_key": keys[0].Prefix + "..."})
}

func (h *handler) compatNewKey(w http.ResponseWriter, r *http.Request) {
	uid := auth.FromContext(r.Context()).ID
	if _, err := h.a.DB.Exec(r.Context(), `DELETE FROM api_key WHERE user_id = $1 AND name = $2`, uid, compatKeyName); err != nil {
		api.Fail(w, r, fmt.Errorf("rotate api key: %w", err))
		return
	}
	id, key, err := CreateAPIKey(r.Context(), h.a.DB, uid, compatKeyName)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	audit.Record(r.Context(), audit.Event{Action: "apikey.create", TargetID: id, TargetLabel: compatKeyName})
	api.JSON(w, http.StatusOK, map[string]any{"api_key": key})
}

func (h *handler) compatDeleteKeys(w http.ResponseWriter, r *http.Request) {
	tag, err := h.a.DB.Exec(r.Context(), `DELETE FROM api_key WHERE user_id = $1`, auth.FromContext(r.Context()).ID)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("delete api keys: %w", err))
		return
	}
	audit.Record(r.Context(), audit.Event{Action: "apikey.revoke", Meta: map[string]any{"count": tag.RowsAffected()}})
	api.JSON(w, http.StatusOK, true)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// decodeObject reads a JSON object body as raw JSON.
func decodeObject(r *http.Request) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := api.Decode(r, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, api.Errf(http.StatusBadRequest, "bad_json", "expected a JSON object")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, errors.New("re-encode body")
	}
	return b, nil
}
