package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/provider"
)

var (
	regMu sync.Mutex
	regs  = map[*app.App]*Registry{}
)

// For returns the app's registry, creating it once. The chat engine and RAG
// call this to share the discovery cache.
func For(a *app.App) *Registry {
	regMu.Lock()
	defer regMu.Unlock()
	if r := regs[a]; r != nil {
		return r
	}
	r := New(a.DB, a.Keys, a.Settings, a.Env.OllamaURL)
	regs[a] = r
	return r
}

func init() { app.Register("models", Mount) }

// Mount registers the model and connection routes.
func Mount(a *app.App) error {
	h := &handlers{r: For(a)}
	// CSRF: auth.Credential ignores the session cookie on cross-origin writes.
	user := func(f http.HandlerFunc) http.Handler { return a.Auth.Required(f) }
	admin := func(f http.HandlerFunc) http.Handler { return a.Auth.Admin(f) }
	for _, p := range []string{"/api/models", "/api/v1/models", "/v1/models"} {
		a.Server.Handle("GET "+p, user(h.list))
	}
	a.Server.Handle("GET /api/v1/models/{$}", user(h.rows))
	a.Server.Handle("GET /api/v1/models/model", user(h.get))
	a.Server.Handle("POST /api/v1/models/create", user(h.create))
	a.Server.Handle("POST /api/v1/models/model/update", user(h.update))
	a.Server.Handle("POST /api/v1/models/model/toggle", user(h.toggle))
	a.Server.Handle("DELETE /api/v1/models/model/delete", user(h.delete))
	a.Server.Handle("POST /api/v1/models/model/delete", user(h.delete))
	a.Server.Handle("GET /api/v1/configs/connections", admin(h.connections))
	a.Server.Handle("POST /api/v1/configs/connections", admin(h.saveConnections))
	a.Server.Handle("POST /api/v1/configs/connections/{id}/verify", admin(h.verify))
	a.Server.Handle("DELETE /api/v1/configs/connections/{id}", admin(h.deleteConnection))
	return nil
}

type handlers struct{ r *Registry }

// list serves the OpenAI-style model list. API-key callers (and
// ?include_hidden=true) also get hidden models.
func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	ms, err := h.r.List(r.Context(), u, u.ViaAPIKey || r.URL.Query().Get("include_hidden") == "true")
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	data := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		data = append(data, listEntry(m))
	}
	api.JSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func listEntry(m *Resolved) map[string]any {
	owned, ctype := "openai", "external"
	if m.Local {
		owned, ctype = "ollama", "local"
	}
	sugg := m.Suggestions
	if len(sugg) == 0 {
		sugg = json.RawMessage(`[]`)
	}
	toolIDs := m.Tools.IDs()
	builtins := m.Tools.Builtins
	if builtins == nil {
		builtins = map[string]bool{}
	}
	knowledge := orEmpty(m.Knowledge)
	return map[string]any{
		"id": m.ID, "name": m.Name, "object": "model", "created": m.Created.Unix(),
		"owned_by": owned, "connection_type": ctype, "local": m.Local, "hidden": m.Hidden,
		"features": enabledFeatures(m.Features), "defaultFeatures": orEmpty(m.DefaultFeatures),
		"toolIds": toolIDs, "suggestionPrompts": sugg, "profileImageUrl": m.ImageURL,
		"capabilities": m.Capabilities,
		"info": map[string]any{
			"id": m.ID, "base_model_id": m.BaseModelID, "name": m.Name,
			"meta": map[string]any{"description": m.Description, "capabilities": m.Capabilities, "hidden": m.Hidden,
				"suggestion_prompts": sugg, "knowledge": knowledge, "toolIds": toolIDs, "builtinTools": builtins,
				"profile_image_url": m.ImageURL},
			"params": m.Params,
		},
	}
}

// enabledFeatures lists feature ids whose value is not false, empty or null
// ({"web_search": "native", "memory": false} -> ["web_search"]).
func enabledFeatures(f map[string]any) []string {
	out := []string{}
	for k, v := range f {
		switch x := v.(type) {
		case nil:
		case bool:
			if x {
				out = append(out, k)
			}
		case string:
			if x != "" && x != "false" && x != "off" {
				out = append(out, k)
			}
		default:
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// rows lists model rows: all for admins, the ones a user owns or may write otherwise.
func (h *handlers) rows(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	rs, err := loadRows(ctx, h.r.DB, "")
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	writable, all, err := access.IDs(ctx, h.r.DB, u, access.Model, access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out := []map[string]any{}
	for i := range rs {
		if all || slices.Contains(writable, rs[i].ID) || owns(u, &rs[i]) {
			out = append(out, rowJSON(&rs[i]))
		}
	}
	api.JSON(w, http.StatusOK, out)
}

func owns(u *auth.User, rw *row) bool { return rw.OwnerID != nil && *rw.OwnerID == u.ID }

func (h *handlers) can(ctx context.Context, u *auth.User, rw *row, perm string) (bool, error) {
	if owns(u, rw) {
		return true, nil
	}
	return access.Can(ctx, h.r.DB, u, access.Model, rw.ID, perm)
}

// lookup returns the row for id; a discovered model without a row gets an
// unsaved row (so it can be edited), visible when the user may see the model.
func (h *handlers) lookup(ctx context.Context, u *auth.User, id, perm string) (*row, error) {
	if id == "" {
		return nil, api.Errf(http.StatusBadRequest, "missing_id", "id is required")
	}
	rw, err := loadRow(ctx, h.r.DB, id)
	if err != nil {
		return nil, err
	}
	if rw == nil {
		m, err := h.r.Resolve(ctx, u, id)
		if errors.Is(err, ErrForbidden) {
			err = ErrNotFound // do not reveal models the caller cannot see
		}
		if err != nil {
			return nil, err
		}
		if perm == access.Write {
			if err := denied(access.Can(ctx, h.r.DB, u, access.Model, id, access.Write)); err != nil {
				return nil, err
			}
		}
		return rowFromModel(m), nil
	}
	if err := h.check(ctx, u, rw, perm); err != nil {
		return nil, err
	}
	return rw, nil
}

// check returns ErrNotFound when u may not read rw, ErrForbidden when u may
// read but not hold perm.
func (h *handlers) check(ctx context.Context, u *auth.User, rw *row, perm string) error {
	for _, p := range []string{access.Read, perm} {
		ok, err := h.can(ctx, u, rw, p)
		if err != nil {
			return err
		}
		if !ok && p == access.Read {
			return ErrNotFound
		}
		if !ok {
			return ErrForbidden
		}
	}
	return nil
}

// denied turns an access check into ErrForbidden (or its own error).
func denied(ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

func modelID(r *http.Request) string { return r.URL.Query().Get("id") }

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	rw, err := h.lookup(r.Context(), auth.FromContext(r.Context()), modelID(r), access.Read)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, rowJSON(rw))
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	if !u.Can("workspace.models") {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "forbidden", "Permission required: workspace.models"))
		return
	}
	var f form
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	f.ID = strings.TrimSpace(f.ID)
	if f.ID == "" {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "missing_id", "id is required"))
		return
	}
	existing, err := loadRow(ctx, h.r.DB, f.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if existing == nil && !u.IsAdmin() {
		// A row with a discovered model's id overrides it for everyone; only
		// admins (or write-grant holders, via update) may do that.
		if _, err := h.r.Resolve(ctx, u, f.ID); !errors.Is(err, ErrNotFound) {
			existing = &row{}
		}
	}
	if existing != nil {
		api.Fail(w, r, api.Errf(http.StatusConflict, "model_exists", "a model with this id already exists"))
		return
	}
	rw := &row{ID: f.ID, Name: f.ID, Active: true, OwnerID: &u.ID}
	if err := h.write(ctx, u, rw, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, rowJSON(rw))
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	rw, err := h.lookup(ctx, u, modelID(r), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var f form
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := h.write(ctx, u, rw, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, rowJSON(rw))
}

// write applies f, saves and re-reads the row (for timestamps). A non-admin
// may only build a preset on a model they can use.
func (h *handlers) write(ctx context.Context, u *auth.User, rw *row, f *form) error {
	before := rw.BaseModelID
	if err := f.apply(rw); err != nil {
		return err
	}
	if b := rw.BaseModelID; b != nil && !u.IsAdmin() && (before == nil || *before != *b) {
		if _, err := h.r.Resolve(ctx, u, *b); errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return api.Errf(http.StatusBadRequest, "bad_base_model", "base model not found")
		} else if err != nil {
			return err
		}
	}
	if err := saveRow(ctx, h.r.DB, rw); err != nil {
		return err
	}
	saved, err := loadRow(ctx, h.r.DB, rw.ID)
	if err != nil {
		return err
	}
	*rw = *saved
	return nil
}

func (h *handlers) toggle(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	rw, err := h.lookup(ctx, u, modelID(r), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	active := !rw.Active
	if err := h.write(ctx, u, rw, &form{IsActive: &active}); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, rowJSON(rw))
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	id := modelID(r)
	if id == "" && r.Method == http.MethodPost {
		var f form
		if err := api.Decode(r, &f); err != nil {
			api.Fail(w, r, err)
			return
		}
		id = f.ID
	}
	rw, err := loadRow(ctx, h.r.DB, id)
	if err == nil && rw == nil {
		err = ErrNotFound
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := h.check(ctx, u, rw, access.Write); err != nil {
		api.Fail(w, r, err)
		return
	}
	// One statement, so the row and its grants go together.
	if _, err := h.r.DB.Exec(ctx, `WITH m AS (DELETE FROM model WHERE id = $1)
		DELETE FROM access_grant WHERE resource_type = 'model' AND resource_id = $1`, id); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, true)
}

// Connections admin ---------------------------------------------------------------

type connJSON struct {
	Connection
	HasKey bool `json:"has_key"`
}

type connForm struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	BaseURL  string            `json:"base_url"`
	APIKey   *string           `json:"api_key"` // absent keeps the stored key, "" clears it
	Prefix   string            `json:"prefix"`
	ModelIDs []string          `json:"model_ids"`
	Enabled  *bool             `json:"enabled"`
	Headers  map[string]string `json:"headers"`
	Options  json.RawMessage   `json:"options"`
	Sort     int               `json:"sort"`
}

func (h *handlers) connections(w http.ResponseWriter, r *http.Request) {
	conns, err := h.r.Connections(r.Context())
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out := make([]connJSON, 0, len(conns))
	for _, c := range conns {
		c.ModelIDs = orEmpty(c.ModelIDs)
		if c.Headers == nil {
			c.Headers = map[string]string{}
		}
		out = append(out, connJSON{Connection: c, HasKey: c.APIKey != ""})
	}
	api.JSON(w, http.StatusOK, map[string]any{"connections": out})
}

func (f *connForm) validate() error {
	if f.ID == "" {
		f.ID = ids.New()
	}
	switch f.Kind {
	case provider.KindOllama, provider.KindOpenAI, provider.KindResponses:
	default:
		return api.Errf(http.StatusBadRequest, "bad_kind", "kind must be ollama, openai or responses")
	}
	u, err := url.Parse(f.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return api.Errf(http.StatusBadRequest, "bad_url", "base_url must be an http(s) URL")
	}
	f.BaseURL = strings.TrimRight(f.BaseURL, "/")
	if strings.ContainsAny(f.Prefix, " .") {
		return api.Errf(http.StatusBadRequest, "bad_prefix", "prefix must not contain spaces or dots")
	}
	if len(f.Options) == 0 || string(f.Options) == "null" {
		f.Options = json.RawMessage(`{}`)
	}
	var o connOptions
	if err := json.Unmarshal(f.Options, &o); err != nil {
		return api.Errf(http.StatusBadRequest, "bad_options", "options: "+err.Error())
	}
	if f.ModelIDs == nil {
		f.ModelIDs = []string{}
	}
	if f.Headers == nil {
		f.Headers = map[string]string{}
	}
	return nil
}

// saveConnections upserts {"connections": [...]}; connections not listed are kept.
func (h *handlers) saveConnections(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Connections []connForm `json:"connections"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	tx, err := h.r.DB.Begin(ctx)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	for i := range body.Connections {
		f := &body.Connections[i]
		if err := f.validate(); err != nil {
			api.Fail(w, r, err)
			return
		}
		enabled := f.Enabled == nil || *f.Enabled
		if _, err := tx.Exec(ctx, `INSERT INTO connection (id, kind, base_url, prefix, model_ids, enabled, headers, options, sort)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind, base_url = EXCLUDED.base_url,
			prefix = EXCLUDED.prefix, model_ids = EXCLUDED.model_ids, enabled = EXCLUDED.enabled, headers = EXCLUDED.headers,
			options = EXCLUDED.options, sort = EXCLUDED.sort`,
			f.ID, f.Kind, f.BaseURL, f.Prefix, f.ModelIDs, enabled, f.Headers, f.Options, f.Sort); err != nil {
			api.Fail(w, r, err)
			return
		}
		if f.APIKey != nil {
			enc, err := h.r.Keys.EncryptString(*f.APIKey)
			if err != nil {
				api.Fail(w, r, err)
				return
			}
			if _, err := tx.Exec(ctx, `UPDATE connection SET api_key_enc = $2 WHERE id = $1`, f.ID, enc); err != nil {
				api.Fail(w, r, err)
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.r.Invalidate()
	h.connections(w, r)
}

func (h *handlers) deleteConnection(w http.ResponseWriter, r *http.Request) {
	tag, err := h.r.DB.Exec(r.Context(), `DELETE FROM connection WHERE id = $1`, r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "connection_not_found", "connection not found"))
		return
	}
	h.r.Invalidate(r.PathValue("id"))
	api.JSON(w, http.StatusOK, true)
}

// verify lists a stored connection's models without the cache.
func (h *handlers) verify(w http.ResponseWriter, r *http.Request) {
	cl, err := h.r.ConnectionClient(r.Context(), r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ms, err := cl.ListModels(ctx)
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusBadGateway, "connection_failed", err.Error()))
		return
	}
	h.r.Invalidate(r.PathValue("id"))
	api.JSON(w, http.StatusOK, map[string]any{"ok": true, "models": orEmpty(ms)})
}
