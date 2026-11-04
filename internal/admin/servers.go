package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/apply"
)

// Rows saved here become managed=false: the UI now owns them, although a
// later `kera apply` that names the same id adopts them again. Secrets are
// write-only: an absent key keeps the stored one, "" clears it. Readers
// (tool providers, hooks, OAuth) query these tables on use, so no cache needs
// invalidating.

func checkURL(field, s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return api.Errf(http.StatusBadRequest, "bad_url", field+" must be an http(s) URL")
	}
	return nil
}

func checkID(id string) error {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, " /") {
		return api.Errf(http.StatusBadRequest, "bad_id", "id is required and must not contain spaces or slashes")
	}
	return nil
}

func oneOf(field, v string, allowed ...string) error {
	if !slices.Contains(allowed, v) {
		return api.Errf(http.StatusBadRequest, "bad_"+field, fmt.Sprintf("%s must be one of %s", field, strings.Join(allowed, ", ")))
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func notFound(what string) error {
	return api.Errf(http.StatusNotFound, "not_found", what+" not found")
}

// deleteRow deletes one row by id with an optional extra statement in the
// same transaction (e.g. the row's grants).
func (h *handler) deleteRow(ctx context.Context, table, id, what string, extra string) error {
	tx, err := h.a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete %s: %w", what, err)
	}
	if tag.RowsAffected() == 0 {
		return notFound(what)
	}
	if extra != "" {
		if _, err := tx.Exec(ctx, extra, id); err != nil {
			return fmt.Errorf("delete %s: %w", what, err)
		}
	}
	return tx.Commit(ctx)
}

// ---- tool servers ----------------------------------------------------------------

type toolAuthOut struct {
	Type     string `json:"type"`
	Header   string `json:"header"`
	Provider string `json:"provider"`
	HasKey   bool   `json:"has_key"`
}

type toolServer struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	URL               string          `json:"url"`
	SpecPath          string          `json:"spec_path"`
	Auth              toolAuthOut     `json:"auth"`
	Enabled           bool            `json:"enabled"`
	NameFilter        []string        `json:"name_filter"`
	DisabledFunctions []string        `json:"disabled_functions"`
	MaxResultChars    int             `json:"max_result_chars"`
	AutoEnable        bool            `json:"auto_enable_when_connected"`
	Display           json.RawMessage `json:"display"`
	Sort              int             `json:"sort"`
	Managed           bool            `json:"managed"`
}

const toolCols = `id, kind, name, description, url, spec_path, auth, enabled, name_filter, disabled_functions,
	max_result_chars, auto_enable_when_connected, display, sort, managed`

func scanTool(row pgx.CollectableRow) (toolServer, error) {
	var t toolServer
	var auth []byte
	err := row.Scan(&t.ID, &t.Kind, &t.Name, &t.Description, &t.URL, &t.SpecPath, &auth, &t.Enabled, &t.NameFilter,
		&t.DisabledFunctions, &t.MaxResultChars, &t.AutoEnable, &t.Display, &t.Sort, &t.Managed)
	var a apply.ToolAuthColumn
	_ = json.Unmarshal(auth, &a)
	t.Auth = toolAuthOut{Type: a.Type, Header: a.Header, Provider: a.Provider, HasKey: a.KeyEnc != ""}
	if t.Auth.Type == "" {
		t.Auth.Type = "none"
	}
	return t, err
}

func (h *handler) listToolServers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.a.DB.Query(r.Context(), `SELECT `+toolCols+` FROM tool_server ORDER BY sort, id`)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list tool servers: %w", err))
		return
	}
	out, err := pgx.CollectRows(rows, scanTool)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list tool servers: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, nonNil(out))
}

type toolForm struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	SpecPath    string `json:"spec_path"`
	Auth        struct {
		Type     string  `json:"type"`
		Header   string  `json:"header"`
		Provider string  `json:"provider"`
		Key      *string `json:"key"`
	} `json:"auth"`
	Enabled           *bool           `json:"enabled"`
	NameFilter        []string        `json:"name_filter"`
	DisabledFunctions []string        `json:"disabled_functions"`
	MaxResultChars    int             `json:"max_result_chars"`
	AutoEnable        bool            `json:"auto_enable_when_connected"`
	Display           json.RawMessage `json:"display"` // absent keeps the stored value
	Sort              int             `json:"sort"`
}

func (f *toolForm) validate() error {
	f.ID, f.URL = strings.TrimSpace(f.ID), strings.TrimRight(strings.TrimSpace(f.URL), "/")
	if f.Auth.Type == "" {
		f.Auth.Type = "none"
	}
	errs := []error{checkID(f.ID), oneOf("kind", f.Kind, "openapi", "mcp"), checkURL("url", f.URL),
		oneOf("auth_type", f.Auth.Type, "none", "bearer", "header", "user_oauth")}
	if f.Auth.Type == "header" && strings.TrimSpace(f.Auth.Header) == "" {
		errs = append(errs, api.Errf(http.StatusBadRequest, "bad_auth", "auth.header is required for header auth"))
	}
	if f.Auth.Type == "user_oauth" && f.Auth.Provider == "" {
		errs = append(errs, api.Errf(http.StatusBadRequest, "bad_auth", "auth.provider is required for user_oauth auth"))
	}
	if f.MaxResultChars < 0 {
		errs = append(errs, api.Errf(http.StatusBadRequest, "bad_max_result_chars", "max_result_chars must be 0 or more"))
	}
	if len(f.Display) > 0 && string(f.Display) != "null" && jsonType(f.Display) != "object" {
		errs = append(errs, api.Errf(http.StatusBadRequest, "bad_display", "display must be an object"))
	}
	if f.SpecPath == "" && f.Kind == "openapi" {
		f.SpecPath = "openapi.json"
	}
	return firstErr(errs)
}

func firstErr(errs []error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// saveToolServer upserts one server by id.
func (h *handler) saveToolServer(w http.ResponseWriter, r *http.Request) {
	var f toolForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := f.validate(); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := h.writeToolServer(r.Context(), &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	rows, err := h.a.DB.Query(r.Context(), `SELECT `+toolCols+` FROM tool_server WHERE id = $1`, f.ID)
	if err == nil {
		var t toolServer
		if t, err = pgx.CollectExactlyOneRow(rows, scanTool); err == nil {
			api.JSON(w, http.StatusOK, t)
			return
		}
	}
	api.Fail(w, r, err)
}

func (h *handler) writeToolServer(ctx context.Context, f *toolForm) error {
	tx, err := h.a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var stored []byte
	var display json.RawMessage
	err = tx.QueryRow(ctx, `SELECT auth, display FROM tool_server WHERE id = $1 FOR UPDATE`, f.ID).Scan(&stored, &display)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load tool server: %w", err)
	}
	var old apply.ToolAuthColumn
	_ = json.Unmarshal(stored, &old)
	a := apply.ToolAuthColumn{Type: f.Auth.Type}
	switch f.Auth.Type {
	case "bearer", "header":
		a.Header, a.KeyEnc = strings.TrimSpace(f.Auth.Header), old.KeyEnc
		if f.Auth.Type == "bearer" {
			a.Header = ""
		}
		if f.Auth.Key != nil {
			if a.KeyEnc, err = apply.SealToolKey(h.a.Keys, *f.Auth.Key); err != nil {
				return err
			}
		}
		if a.KeyEnc == "" {
			return api.Errf(http.StatusBadRequest, "bad_auth", "auth.key is required for "+f.Auth.Type+" auth")
		}
	case "user_oauth":
		a.Provider = f.Auth.Provider
		// optional service key header sent next to the user's token
		if a.Header = strings.TrimSpace(f.Auth.Header); a.Header != "" {
			a.KeyEnc = old.KeyEnc
			if f.Auth.Key != nil {
				if a.KeyEnc, err = apply.SealToolKey(h.a.Keys, *f.Auth.Key); err != nil {
					return err
				}
			}
			if a.KeyEnc == "" {
				return api.Errf(http.StatusBadRequest, "bad_auth", "auth.key is required with auth.header")
			}
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM oauth_provider WHERE id = $1)`, a.Provider).Scan(&ok); err != nil {
			return fmt.Errorf("check provider: %w", err)
		}
		if !ok {
			return api.Errf(http.StatusBadRequest, "bad_auth", "unknown OAuth provider "+a.Provider)
		}
	}
	if len(f.Display) > 0 && string(f.Display) != "null" {
		display = f.Display
	}
	if display == nil {
		display = json.RawMessage(`{}`)
	}
	authJSON, _ := json.Marshal(a)
	_, err = tx.Exec(ctx, `INSERT INTO tool_server (id, kind, name, description, url, spec_path, auth, enabled, name_filter,
		disabled_functions, max_result_chars, auto_enable_when_connected, display, sort, managed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, false)
		ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind, name = EXCLUDED.name, description = EXCLUDED.description,
		url = EXCLUDED.url, spec_path = EXCLUDED.spec_path, auth = EXCLUDED.auth, enabled = EXCLUDED.enabled,
		name_filter = EXCLUDED.name_filter, disabled_functions = EXCLUDED.disabled_functions,
		max_result_chars = EXCLUDED.max_result_chars, auto_enable_when_connected = EXCLUDED.auto_enable_when_connected,
		display = EXCLUDED.display, sort = EXCLUDED.sort, managed = false`,
		f.ID, f.Kind, strings.TrimSpace(f.Name), f.Description, f.URL, f.SpecPath, authJSON, f.Enabled == nil || *f.Enabled,
		nonNil(f.NameFilter), nonNil(f.DisabledFunctions), f.MaxResultChars, f.AutoEnable, display, f.Sort)
	if err != nil {
		return fmt.Errorf("save tool server: %w", err)
	}
	return tx.Commit(ctx)
}

func (h *handler) deleteToolServer(w http.ResponseWriter, r *http.Request) {
	err := h.deleteRow(r.Context(), "tool_server", r.PathValue("id"), "tool server",
		`DELETE FROM access_grant WHERE resource_type = '`+access.ToolServer+`' AND resource_id = $1`)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, true)
}

// ---- hook servers ----------------------------------------------------------------

type hookServer struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	URL          string          `json:"url"`
	HasKey       bool            `json:"has_key"`
	Events       []string        `json:"events"`
	Priority     int             `json:"priority"`
	Scope        string          `json:"scope"`
	ModelIDs     []string        `json:"model_ids"`
	Config       json.RawMessage `json:"config"`
	ForwardOAuth []string        `json:"forward_oauth"`
	TimeoutMS    int             `json:"timeout_ms"`
	OnError      string          `json:"on_error"`
	Enabled      bool            `json:"enabled"`
	Managed      bool            `json:"managed"`
}

const hookCols = `id, name, url, key_enc IS NOT NULL, events, priority, scope, model_ids, config, forward_oauth,
	timeout_ms, on_error, enabled, managed`

func scanHook(row pgx.CollectableRow) (hookServer, error) {
	var x hookServer
	err := row.Scan(&x.ID, &x.Name, &x.URL, &x.HasKey, &x.Events, &x.Priority, &x.Scope, &x.ModelIDs, &x.Config,
		&x.ForwardOAuth, &x.TimeoutMS, &x.OnError, &x.Enabled, &x.Managed)
	return x, err
}

func (h *handler) listHooks(w http.ResponseWriter, r *http.Request) {
	rows, err := h.a.DB.Query(r.Context(), `SELECT `+hookCols+` FROM hook_server ORDER BY priority, id`)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list hook servers: %w", err))
		return
	}
	out, err := pgx.CollectRows(rows, scanHook)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list hook servers: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, nonNil(out))
}

type hookForm struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	URL          string          `json:"url"`
	Key          *string         `json:"key"`
	Events       []string        `json:"events"`
	Priority     int             `json:"priority"`
	Scope        string          `json:"scope"`
	ModelIDs     []string        `json:"model_ids"`
	Config       json.RawMessage `json:"config"`
	ForwardOAuth []string        `json:"forward_oauth"`
	TimeoutMS    int             `json:"timeout_ms"`
	OnError      string          `json:"on_error"`
	Enabled      *bool           `json:"enabled"`
}

func (f *hookForm) validate() error {
	f.ID, f.URL = strings.TrimSpace(f.ID), strings.TrimSpace(f.URL)
	if len(f.Events) == 0 {
		f.Events = []string{"inlet"}
	}
	if f.Scope == "" {
		f.Scope = "models"
	}
	if f.OnError == "" {
		f.OnError = "skip"
	}
	if f.TimeoutMS == 0 {
		f.TimeoutMS = 10000
	}
	if len(f.Config) == 0 || string(f.Config) == "null" {
		f.Config = json.RawMessage(`{}`)
	}
	errs := []error{checkID(f.ID), checkURL("url", f.URL), oneOf("scope", f.Scope, "global", "models"),
		oneOf("on_error", f.OnError, "skip", "abort")}
	for _, e := range f.Events {
		errs = append(errs, oneOf("events", e, "inlet", "outlet"))
	}
	if jsonType(f.Config) != "object" {
		errs = append(errs, api.Errf(http.StatusBadRequest, "bad_config", "config must be an object"))
	}
	if f.TimeoutMS < 0 {
		errs = append(errs, api.Errf(http.StatusBadRequest, "bad_timeout", "timeout_ms must be positive"))
	}
	return firstErr(errs)
}

func (h *handler) saveHook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var f hookForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := f.validate(); err != nil {
		api.Fail(w, r, err)
		return
	}
	_, err := h.a.DB.Exec(ctx, `INSERT INTO hook_server (id, name, url, events, priority, scope, model_ids, config,
		forward_oauth, timeout_ms, on_error, enabled, managed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, false)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, url = EXCLUDED.url, events = EXCLUDED.events,
		priority = EXCLUDED.priority, scope = EXCLUDED.scope, model_ids = EXCLUDED.model_ids, config = EXCLUDED.config,
		forward_oauth = EXCLUDED.forward_oauth, timeout_ms = EXCLUDED.timeout_ms, on_error = EXCLUDED.on_error,
		enabled = EXCLUDED.enabled, managed = false`,
		f.ID, strings.TrimSpace(f.Name), f.URL, f.Events, f.Priority, f.Scope, nonNil(f.ModelIDs), f.Config,
		nonNil(f.ForwardOAuth), f.TimeoutMS, f.OnError, f.Enabled == nil || *f.Enabled)
	if err == nil && f.Key != nil {
		var enc []byte
		if enc, err = h.a.Keys.EncryptString(*f.Key); err == nil {
			_, err = h.a.DB.Exec(ctx, `UPDATE hook_server SET key_enc = $2 WHERE id = $1`, f.ID, enc)
		}
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("save hook server: %w", err))
		return
	}
	rows, err := h.a.DB.Query(ctx, `SELECT `+hookCols+` FROM hook_server WHERE id = $1`, f.ID)
	if err == nil {
		var x hookServer
		if x, err = pgx.CollectExactlyOneRow(rows, scanHook); err == nil {
			api.JSON(w, http.StatusOK, x)
			return
		}
	}
	api.Fail(w, r, err)
}

func (h *handler) deleteHook(w http.ResponseWriter, r *http.Request) {
	if err := h.deleteRow(r.Context(), "hook_server", r.PathValue("id"), "hook server", ""); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, true)
}

// ---- OAuth providers ----------------------------------------------------------------

type provider struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Login                 bool   `json:"login"`
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	ClientID              string `json:"client_id"`
	HasSecret             bool   `json:"has_secret"`
	Scope                 string `json:"scope"`
	PKCE                  bool   `json:"pkce"`
	SendResource          bool   `json:"send_resource"`
	Managed               bool   `json:"managed"`
	RedirectURI           string `json:"redirect_uri"`
}

type providerOptions struct {
	Login bool   `json:"login"`
	Name  string `json:"name"`
}

const providerCols = `id, issuer, authorization_endpoint, token_endpoint, revocation_endpoint, userinfo_endpoint,
	client_id, client_secret_enc IS NOT NULL, scope, pkce, send_resource, options, managed`

func (h *handler) scanProvider(row pgx.CollectableRow) (provider, error) {
	var p provider
	var opts []byte
	err := row.Scan(&p.ID, &p.Issuer, &p.AuthorizationEndpoint, &p.TokenEndpoint, &p.RevocationEndpoint,
		&p.UserinfoEndpoint, &p.ClientID, &p.HasSecret, &p.Scope, &p.PKCE, &p.SendResource, &opts, &p.Managed)
	var o providerOptions
	_ = json.Unmarshal(opts, &o)
	p.Login, p.Name = o.Login, o.Name
	p.RedirectURI = h.redirectURI(p.ID, p.Login)
	return p, err
}

// redirectURI is what the provider's console must list: login providers
// return to /oauth/<id>/callback, tool-server providers to
// /oauth/clients/<id>/callback.
func (h *handler) redirectURI(id string, login bool) string {
	base := strings.TrimRight(h.a.Env.PublicURL, "/")
	if login {
		return base + "/oauth/" + url.PathEscape(id) + "/callback"
	}
	return base + "/oauth/clients/" + url.PathEscape(id) + "/callback"
}

func (h *handler) listProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.a.DB.Query(r.Context(), `SELECT `+providerCols+` FROM oauth_provider ORDER BY id`)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list oauth providers: %w", err))
		return
	}
	out, err := pgx.CollectRows(rows, h.scanProvider)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list oauth providers: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, nonNil(out))
}

type providerForm struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	Login                 bool    `json:"login"`
	Issuer                string  `json:"issuer"`
	AuthorizationEndpoint string  `json:"authorization_endpoint"`
	TokenEndpoint         string  `json:"token_endpoint"`
	RevocationEndpoint    string  `json:"revocation_endpoint"`
	UserinfoEndpoint      string  `json:"userinfo_endpoint"`
	ClientID              string  `json:"client_id"`
	ClientSecret          *string `json:"client_secret"`
	Scope                 string  `json:"scope"`
	PKCE                  *bool   `json:"pkce"`
	SendResource          bool    `json:"send_resource"`
}

func (f *providerForm) validate() error {
	f.ID, f.ClientID = strings.TrimSpace(f.ID), strings.TrimSpace(f.ClientID)
	errs := []error{checkID(f.ID)}
	if f.ClientID == "" {
		errs = append(errs, api.Errf(http.StatusBadRequest, "bad_client_id", "client_id is required"))
	}
	for field, v := range map[string]string{"issuer": f.Issuer, "authorization_endpoint": f.AuthorizationEndpoint,
		"token_endpoint": f.TokenEndpoint, "revocation_endpoint": f.RevocationEndpoint, "userinfo_endpoint": f.UserinfoEndpoint} {
		if v != "" {
			errs = append(errs, checkURL(field, v))
		}
	}
	// Login providers may leave endpoints empty: sign-in fills Google's (the
	// row `kera apply` writes for auth.oauth.google has none).
	if !f.Login && f.Issuer == "" && (f.AuthorizationEndpoint == "" || f.TokenEndpoint == "") {
		errs = append(errs, api.Errf(http.StatusBadRequest, "bad_endpoints", "set issuer, or the authorization and token endpoints"))
	}
	return firstErr(errs)
}

func (h *handler) saveProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var f providerForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := f.validate(); err != nil {
		api.Fail(w, r, err)
		return
	}
	// options may hold keys this form does not know; merge into them.
	opts, _ := json.Marshal(providerOptions{Login: f.Login, Name: strings.TrimSpace(f.Name)})
	_, err := h.a.DB.Exec(ctx, `INSERT INTO oauth_provider (id, issuer, authorization_endpoint, token_endpoint,
		revocation_endpoint, userinfo_endpoint, client_id, scope, pkce, send_resource, options, managed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false)
		ON CONFLICT (id) DO UPDATE SET issuer = EXCLUDED.issuer, authorization_endpoint = EXCLUDED.authorization_endpoint,
		token_endpoint = EXCLUDED.token_endpoint, revocation_endpoint = EXCLUDED.revocation_endpoint,
		userinfo_endpoint = EXCLUDED.userinfo_endpoint, client_id = EXCLUDED.client_id, scope = EXCLUDED.scope,
		pkce = EXCLUDED.pkce, send_resource = EXCLUDED.send_resource,
		options = oauth_provider.options || EXCLUDED.options, managed = false`,
		f.ID, f.Issuer, f.AuthorizationEndpoint, f.TokenEndpoint, f.RevocationEndpoint, f.UserinfoEndpoint, f.ClientID,
		f.Scope, f.PKCE == nil || *f.PKCE, f.SendResource, opts)
	if err == nil && f.ClientSecret != nil {
		var enc []byte
		if enc, err = h.a.Keys.EncryptString(*f.ClientSecret); err == nil {
			_, err = h.a.DB.Exec(ctx, `UPDATE oauth_provider SET client_secret_enc = $2 WHERE id = $1`, f.ID, enc)
		}
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("save oauth provider: %w", err))
		return
	}
	rows, err := h.a.DB.Query(ctx, `SELECT `+providerCols+` FROM oauth_provider WHERE id = $1`, f.ID)
	if err == nil {
		var p provider
		if p, err = pgx.CollectExactlyOneRow(rows, h.scanProvider); err == nil {
			api.JSON(w, http.StatusOK, p)
			return
		}
	}
	api.Fail(w, r, err)
}

// deleteProvider refuses while a tool server signs in through the provider.
func (h *handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var users []string
	rows, err := h.a.DB.Query(ctx, `SELECT id FROM tool_server WHERE auth->>'provider' = $1 ORDER BY id`, id)
	if err == nil {
		users, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("check provider use: %w", err))
		return
	}
	if len(users) > 0 {
		api.Fail(w, r, api.Errf(http.StatusConflict, "in_use", "Used by tool server "+strings.Join(users, ", ")))
		return
	}
	if err := h.deleteRow(ctx, "oauth_provider", id, "OAuth provider", ""); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, true)
}
