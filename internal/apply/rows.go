package apply

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/ids"
)

// row is one current resource, normalised like the desired side.
type row struct {
	id      string
	v       any
	managed bool
}

// ---- settings ----------------------------------------------------------------

func (r *run) readSettings() ([]row, error) {
	secrets := settingKeys()
	rows, err := r.tx.Query(r.ctx, `SELECT key, value, managed FROM config ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var key string
		var raw []byte
		var managed bool
		if err := rows.Scan(&key, &raw, &managed); err != nil {
			return nil, err
		}
		s := setting{Secret: secrets[key]}
		if s.Secret {
			if plain, err := openSetting(r.keys, raw); err == nil {
				s.JSON, _ = canonical(plain)
			}
		} else if s.JSON, err = canonicalJSON(raw); err != nil {
			return nil, fmt.Errorf("config %s: %w", key, err)
		}
		out = append(out, row{id: key, v: s.JSON, managed: managed})
	}
	return out, rows.Err()
}

func (r *run) writeSetting(key string, s setting) error {
	val := s.JSON
	if s.Secret {
		var err error
		if val, err = sealSetting(r.keys, s.JSON); err != nil {
			return fmt.Errorf("encrypt %s: %w", key, err)
		}
	}
	_, err := r.tx.Exec(r.ctx, `INSERT INTO config (key, value, managed, updated_at) VALUES ($1, $2, true, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, managed = true, updated_at = now()`, key, val)
	return wrap(err, "write config %s", key)
}

func (r *run) deleteSetting(key string) error {
	_, err := r.tx.Exec(r.ctx, `DELETE FROM config WHERE key = $1 AND managed`, key)
	return wrap(err, "delete config %s", key)
}

// ---- groups --------------------------------------------------------------------

func normGroup(g Group) Group {
	g.Permissions = normPerms(g.Permissions)
	return g
}

func normPerms(p Permissions) Permissions {
	out := Permissions{}
	for cat, leaves := range p {
		if len(leaves) > 0 {
			out[cat] = leaves
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// permsFrom keeps the two-level boolean leaves of a stored permissions object
// (rows imported from elsewhere may carry other shapes).
func permsFrom(raw []byte) Permissions {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	out := Permissions{}
	for cat, v := range m {
		leaves, _ := v.(map[string]any)
		for k, b := range leaves {
			if b, ok := b.(bool); ok {
				if out[cat] == nil {
					out[cat] = map[string]bool{}
				}
				out[cat][k] = b
			}
		}
	}
	return out
}

func (r *run) readGroups() ([]row, error) {
	rows, err := r.tx.Query(r.ctx, `SELECT name, description, permissions, managed FROM "group" ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read groups: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var g Group
		var raw []byte
		var managed bool
		if err := rows.Scan(&g.Name, &g.Description, &raw, &managed); err != nil {
			return nil, err
		}
		g.Permissions = permsFrom(raw)
		out = append(out, row{id: g.Name, v: normGroup(g), managed: managed})
	}
	return out, rows.Err()
}

func (r *run) writeGroup(g Group) error {
	_, err := r.tx.Exec(r.ctx, `INSERT INTO "group" (id, name, description, permissions, managed) VALUES ($1, $2, $3, $4, true)
		ON CONFLICT (name) DO UPDATE SET description = EXCLUDED.description, permissions = EXCLUDED.permissions,
		managed = true, updated_at = now()`, ids.New(), g.Name, g.Description, jsonCol(g.Permissions, "{}"))
	return wrap(err, "write group %s", g.Name)
}

// deleteGroup removes a managed group, unless people are still in it:
// memberships are UI-managed and apply never removes them.
func (r *run) deleteGroup(name string) error {
	var id string
	var members int
	err := r.tx.QueryRow(r.ctx, `SELECT g.id, (SELECT count(*) FROM group_member m WHERE m.group_id = g.id)
		FROM "group" g WHERE g.name = $1 AND g.managed`, name).Scan(&id, &members)
	if err != nil {
		return wrap(err, "find group %s", name)
	}
	if members > 0 {
		r.res.Warnings = append(r.res.Warnings, fmt.Sprintf("group %s not deleted: it still has %d members", name, members))
		return errKept
	}
	if _, err := r.tx.Exec(r.ctx, `DELETE FROM access_grant WHERE principal_type = 'group' AND principal_id = $1`, id); err != nil {
		return wrap(err, "delete grants of group %s", name)
	}
	_, err = r.tx.Exec(r.ctx, `DELETE FROM "group" WHERE id = $1`, id)
	return wrap(err, "delete group %s", name)
}

// errKept tells reconcile that a prune candidate was deliberately kept.
var errKept = errors.New("kept")

func (r *run) loadGroups() error {
	r.groupID, r.groupName = map[string]string{}, map[string]string{}
	rows, err := r.tx.Query(r.ctx, `SELECT id, name FROM "group"`)
	if err != nil {
		return fmt.Errorf("read groups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		r.groupID[name], r.groupName[id] = id, name
	}
	return rows.Err()
}

// ---- grants --------------------------------------------------------------------

// normGrants resolves group ids to names, fills the default permission and
// sorts, so the file and the database compare equal.
func (r *run) normGrants(line int, what string, gs []access.Grant) []access.Grant {
	var out []access.Grant
	for _, g := range gs {
		ptype, pid, err := access.ParsePrincipal(g.Principal)
		if err != nil {
			continue // reported by validate
		}
		if ptype == "group" {
			if _, ok := r.groupID[pid]; !ok {
				name, ok := r.groupName[pid]
				if !ok {
					r.errs.add(line, "%s: unknown group %q (declare it under groups or create it first)", what, pid)
					continue
				}
				pid = name
			}
		}
		perm := g.Permission
		if perm == "" {
			perm = access.Read
		}
		out = append(out, access.Grant{Principal: ptype + ":" + pid, Permission: perm})
	}
	return sortGrants(out)
}

func sortGrants(gs []access.Grant) []access.Grant {
	slices.SortFunc(gs, func(a, b access.Grant) int {
		return strings.Compare(a.Principal+" "+a.Permission, b.Principal+" "+b.Permission)
	})
	return slices.Compact(gs)
}

// readGrants returns managed grants per resource id, groups shown by name.
func (r *run) readGrants(resType string) (map[string][]access.Grant, error) {
	rows, err := r.tx.Query(r.ctx, `SELECT resource_id, principal_type, principal_id, permission FROM access_grant
		WHERE resource_type = $1 AND managed`, resType)
	if err != nil {
		return nil, fmt.Errorf("read %s grants: %w", resType, err)
	}
	defer rows.Close()
	out := map[string][]access.Grant{}
	for rows.Next() {
		var id, ptype, pid, perm string
		if err := rows.Scan(&id, &ptype, &pid, &perm); err != nil {
			return nil, err
		}
		if name, ok := r.groupName[pid]; ok && ptype == "group" {
			pid = name
		}
		out[id] = append(out[id], access.Grant{Principal: ptype + ":" + pid, Permission: perm})
	}
	for id := range out {
		out[id] = sortGrants(out[id])
	}
	return out, rows.Err()
}

// setGrants replaces the managed grants of one resource (hand-made grants stay).
// ponytail: same rule as access.Replace, inlined because Replace opens its own
// transaction; give Replace a pgx.Tx parameter if a third caller appears.
func (r *run) setGrants(resType, resID string, gs []access.Grant) error {
	if _, err := r.tx.Exec(r.ctx, `DELETE FROM access_grant WHERE resource_type = $1 AND resource_id = $2 AND managed`,
		resType, resID); err != nil {
		return wrap(err, "clear grants of %s %s", resType, resID)
	}
	for _, g := range gs {
		ptype, pid, _ := access.ParsePrincipal(g.Principal)
		if ptype == "group" {
			pid = r.groupID[pid]
		}
		if _, err := r.tx.Exec(r.ctx, `INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission, managed)
			VALUES ($1, $2, $3, $4, $5, $6, true)
			ON CONFLICT (resource_type, resource_id, principal_type, principal_id, permission) DO UPDATE SET managed = true`,
			ids.New(), resType, resID, ptype, pid, g.Permission); err != nil {
			return wrap(err, "grant %s %s", resType, resID)
		}
	}
	return nil
}

func (r *run) dropGrants(resType, resID string) error {
	_, err := r.tx.Exec(r.ctx, `DELETE FROM access_grant WHERE resource_type = $1 AND resource_id = $2`, resType, resID)
	return wrap(err, "delete grants of %s %s", resType, resID)
}

// ---- oauth providers -----------------------------------------------------------------

type providerOptions struct {
	Login bool   `json:"login,omitempty"`
	Name  string `json:"name,omitempty"`
}

func normProvider(p OAuthProvider) OAuthProvider {
	p.PKCE = nilIfTrue(p.PKCE)
	return p
}

func (r *run) readProviders() ([]row, error) {
	rows, err := r.tx.Query(r.ctx, `SELECT id, issuer, authorization_endpoint, token_endpoint, revocation_endpoint,
		userinfo_endpoint, client_id, client_secret_enc, scope, pkce, send_resource, options, managed FROM oauth_provider ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read oauth providers: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var p OAuthProvider
		var enc, opts []byte
		var pkce, managed bool
		if err := rows.Scan(&p.ID, &p.Issuer, &p.AuthorizationEndpoint, &p.TokenEndpoint, &p.RevocationEndpoint,
			&p.UserinfoEndpoint, &p.ClientID, &enc, &p.Scope, &pkce, &p.SendResource, &opts, &managed); err != nil {
			return nil, err
		}
		p.ClientSecret = r.openStored("oauth provider "+p.ID+" client secret", enc)
		var o providerOptions
		_ = json.Unmarshal(opts, &o)
		p.Login, p.Name, p.PKCE = o.Login, o.Name, &pkce
		out = append(out, row{id: p.ID, v: normProvider(p), managed: managed})
	}
	return out, rows.Err()
}

func (r *run) writeProvider(p OAuthProvider) error {
	enc, err := r.keys.EncryptString(p.ClientSecret)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(r.ctx, `INSERT INTO oauth_provider (id, issuer, authorization_endpoint, token_endpoint, revocation_endpoint,
		userinfo_endpoint, client_id, client_secret_enc, scope, pkce, send_resource, options, managed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, true)
		ON CONFLICT (id) DO UPDATE SET issuer = EXCLUDED.issuer, authorization_endpoint = EXCLUDED.authorization_endpoint,
		token_endpoint = EXCLUDED.token_endpoint, revocation_endpoint = EXCLUDED.revocation_endpoint,
		userinfo_endpoint = EXCLUDED.userinfo_endpoint, client_id = EXCLUDED.client_id, client_secret_enc = EXCLUDED.client_secret_enc,
		scope = EXCLUDED.scope, pkce = EXCLUDED.pkce, send_resource = EXCLUDED.send_resource, options = EXCLUDED.options, managed = true`,
		p.ID, p.Issuer, p.AuthorizationEndpoint, p.TokenEndpoint, p.RevocationEndpoint, p.UserinfoEndpoint, p.ClientID, enc,
		p.Scope, p.PKCE == nil || *p.PKCE, p.SendResource, jsonCol(providerOptions{Login: p.Login, Name: p.Name}, "{}"))
	return wrap(err, "write oauth provider %s", p.ID)
}

// ---- connections ---------------------------------------------------------------------

type connOptions struct {
	WebSearch          *WebSearchOpts  `json:"web_search,omitempty"`
	ForwardUserHeaders *ForwardHeaders `json:"forward_user_headers,omitempty"`
}

func normConnection(c Connection) Connection {
	c.Enabled = nilIfTrue(c.Enabled)
	if c.WebSearch != nil && *c.WebSearch == (WebSearchOpts{}) {
		c.WebSearch = nil
	}
	if c.ForwardUserHeaders != nil && *c.ForwardUserHeaders == (ForwardHeaders{}) {
		c.ForwardUserHeaders = nil
	}
	return c
}

func (r *run) readConnections() ([]row, error) {
	rows, err := r.tx.Query(r.ctx, `SELECT id, kind, base_url, api_key_enc, prefix, model_ids, enabled, headers, options, managed
		FROM connection ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read connections: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var c Connection
		var enc, headers, opts []byte
		var enabled, managed bool
		if err := rows.Scan(&c.ID, &c.Kind, &c.URL, &enc, &c.Prefix, &c.Models, &enabled, &headers, &opts, &managed); err != nil {
			return nil, err
		}
		c.APIKey = r.openStored("connection "+c.ID+" api key", enc)
		_ = json.Unmarshal(headers, &c.Headers)
		var o connOptions
		_ = json.Unmarshal(opts, &o)
		c.WebSearch, c.ForwardUserHeaders, c.Enabled = o.WebSearch, o.ForwardUserHeaders, &enabled
		out = append(out, row{id: c.ID, v: normConnection(c), managed: managed})
	}
	return out, rows.Err()
}

func (r *run) writeConnection(c Connection) error {
	enc, err := r.keys.EncryptString(c.APIKey)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(r.ctx, `INSERT INTO connection (id, kind, base_url, api_key_enc, prefix, model_ids, enabled, headers, options, managed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, true)
		ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind, base_url = EXCLUDED.base_url, api_key_enc = EXCLUDED.api_key_enc,
		prefix = EXCLUDED.prefix, model_ids = EXCLUDED.model_ids, enabled = EXCLUDED.enabled, headers = EXCLUDED.headers,
		options = EXCLUDED.options, managed = true`,
		c.ID, c.Kind, c.URL, enc, c.Prefix, strs(c.Models), c.Enabled == nil || *c.Enabled, jsonCol(c.Headers, "{}"),
		jsonCol(connOptions{WebSearch: c.WebSearch, ForwardUserHeaders: c.ForwardUserHeaders}, "{}"))
	return wrap(err, "write connection %s", c.ID)
}

// ---- tool servers --------------------------------------------------------------------

type authCol struct {
	Type     string `json:"type"`
	KeyEnc   string `json:"key_enc,omitempty"`
	Header   string `json:"header,omitempty"`
	Provider string `json:"provider,omitempty"`
}

func normToolServer(t ToolServer) ToolServer {
	t.Enabled = nilIfTrue(t.Enabled)
	if t.Kind == "openapi" && t.Spec == "openapi.json" {
		t.Spec = ""
	}
	if t.Auth != nil && *t.Auth == (ToolAuth{Type: "none"}) {
		t.Auth = nil
	}
	if t.Display != nil && t.Display.Label == "" && t.Display.Icon == "" && t.Display.Kind == "" &&
		t.Display.Plural == "" && len(t.Display.Tools) == 0 {
		t.Display = nil
	}
	return t
}

func (r *run) readToolServers() ([]row, error) {
	grants, err := r.readGrants(access.ToolServer)
	if err != nil {
		return nil, err
	}
	rows, err := r.tx.Query(r.ctx, `SELECT id, kind, name, description, url, spec_path, auth, enabled, name_filter,
		disabled_functions, max_result_chars, auto_enable_when_connected, display, managed FROM tool_server ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read tool servers: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var t ToolServer
		var auth, display []byte
		var enabled, managed bool
		if err := rows.Scan(&t.ID, &t.Kind, &t.Name, &t.Description, &t.URL, &t.Spec, &auth, &enabled, &t.Filter,
			&t.Disable, &t.MaxResultChars, &t.AutoEnableWhenConnected, &display, &managed); err != nil {
			return nil, err
		}
		var a authCol
		_ = json.Unmarshal(auth, &a)
		t.Auth = &ToolAuth{Type: a.Type, Header: a.Header, Provider: a.Provider}
		if a.KeyEnc != "" {
			raw, derr := base64.StdEncoding.DecodeString(a.KeyEnc)
			if derr != nil {
				raw = []byte{0} // corrupt: treated as undecryptable below
			}
			t.Auth.Key = r.openStored("tool server "+t.ID+" key", raw)
		}
		_ = json.Unmarshal(display, &t.Display)
		t.Enabled, t.Grants = &enabled, grants[t.ID]
		out = append(out, row{id: t.ID, v: normToolServer(t), managed: managed})
	}
	return out, rows.Err()
}

func (r *run) writeToolServer(t ToolServer) error {
	a := authCol{Type: "none"}
	if t.Auth != nil {
		a = authCol{Type: t.Auth.Type, Header: t.Auth.Header, Provider: t.Auth.Provider}
		var err error
		if a.KeyEnc, err = r.sealB64(t.Auth.Key); err != nil {
			return err
		}
	}
	spec := t.Spec
	if spec == "" && t.Kind == "openapi" {
		spec = "openapi.json"
	}
	_, err := r.tx.Exec(r.ctx, `INSERT INTO tool_server (id, kind, name, description, url, spec_path, auth, enabled, name_filter,
		disabled_functions, max_result_chars, auto_enable_when_connected, display, managed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, true)
		ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind, name = EXCLUDED.name, description = EXCLUDED.description,
		url = EXCLUDED.url, spec_path = EXCLUDED.spec_path, auth = EXCLUDED.auth, enabled = EXCLUDED.enabled,
		name_filter = EXCLUDED.name_filter, disabled_functions = EXCLUDED.disabled_functions,
		max_result_chars = EXCLUDED.max_result_chars, auto_enable_when_connected = EXCLUDED.auto_enable_when_connected,
		display = EXCLUDED.display, managed = true`,
		t.ID, t.Kind, t.Name, t.Description, t.URL, spec, jsonCol(a, "{}"), t.Enabled == nil || *t.Enabled, strs(t.Filter),
		strs(t.Disable), t.MaxResultChars, t.AutoEnableWhenConnected, jsonCol(t.Display, "{}"))
	if err != nil {
		return wrap(err, "write tool server %s", t.ID)
	}
	return r.setGrants(access.ToolServer, t.ID, t.Grants)
}

// ---- hook servers --------------------------------------------------------------------

func normHook(h HookServer) HookServer {
	h.Enabled = nilIfTrue(h.Enabled)
	if slices.Equal(h.Events, []string{"inlet"}) {
		h.Events = nil
	}
	if h.Scope == "models" {
		h.Scope = ""
	}
	if h.TimeoutMS == 10000 {
		h.TimeoutMS = 0
	}
	if h.OnError == "skip" {
		h.OnError = ""
	}
	return h
}

func (r *run) readHooks() ([]row, error) {
	rows, err := r.tx.Query(r.ctx, `SELECT id, name, url, key_enc, events, priority, scope, model_ids, config, forward_oauth,
		timeout_ms, on_error, enabled, managed FROM hook_server ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read hook servers: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var h HookServer
		var enc, cfg []byte
		var enabled, managed bool
		if err := rows.Scan(&h.ID, &h.Name, &h.URL, &enc, &h.Events, &h.Priority, &h.Scope, &h.ModelIDs, &cfg,
			&h.ForwardOAuth, &h.TimeoutMS, &h.OnError, &enabled, &managed); err != nil {
			return nil, err
		}
		h.Key = r.openStored("hook server "+h.ID+" key", enc)
		_ = json.Unmarshal(cfg, &h.Config)
		h.Enabled = &enabled
		out = append(out, row{id: h.ID, v: normHook(h), managed: managed})
	}
	return out, rows.Err()
}

func (r *run) writeHook(h HookServer) error {
	enc, err := r.keys.EncryptString(h.Key)
	if err != nil {
		return err
	}
	events, scope, timeout, onErr := h.Events, h.Scope, h.TimeoutMS, h.OnError
	if len(events) == 0 {
		events = []string{"inlet"}
	}
	if scope == "" {
		scope = "models"
	}
	if timeout == 0 {
		timeout = 10000
	}
	if onErr == "" {
		onErr = "skip"
	}
	_, err = r.tx.Exec(r.ctx, `INSERT INTO hook_server (id, name, url, key_enc, events, priority, scope, model_ids, config,
		forward_oauth, timeout_ms, on_error, enabled, managed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, true)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, url = EXCLUDED.url, key_enc = EXCLUDED.key_enc,
		events = EXCLUDED.events, priority = EXCLUDED.priority, scope = EXCLUDED.scope, model_ids = EXCLUDED.model_ids,
		config = EXCLUDED.config, forward_oauth = EXCLUDED.forward_oauth, timeout_ms = EXCLUDED.timeout_ms,
		on_error = EXCLUDED.on_error, enabled = EXCLUDED.enabled, managed = true`,
		h.ID, h.Name, h.URL, enc, events, h.Priority, scope, strs(h.ModelIDs), jsonCol(h.Config, "{}"),
		strs(h.ForwardOAuth), timeout, onErr, h.Enabled == nil || *h.Enabled)
	return wrap(err, "write hook server %s", h.ID)
}

// ---- models --------------------------------------------------------------------------

func normModel(m Model) Model {
	m.Active = nilIfTrue(m.Active)
	if m.FileContext == "tools" {
		m.FileContext = ""
	}
	if m.Features != nil && *m.Features == (ModelFeatures{}) {
		m.Features = nil
	}
	if m.Tools != nil && len(m.Tools.Servers) == 0 && len(m.Tools.Builtins) == 0 {
		m.Tools = nil
	}
	for i := range m.Knowledge {
		if m.Knowledge[i].Mode == "retrieve" {
			m.Knowledge[i].Mode = ""
		}
	}
	return m
}

func (r *run) readModels() ([]row, error) {
	grants, err := r.readGrants(access.Model)
	if err != nil {
		return nil, err
	}
	rows, err := r.tx.Query(r.ctx, `SELECT id, coalesce(connection_id, ''), coalesce(base_model_id, ''), name, description,
		system_prompt, params, capabilities, features, default_features, file_context, tools, knowledge, hooks, suggestions,
		hidden, active, image_url, managed FROM model ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var m Model
		var params, caps, feats, tools, know, sugg []byte
		var active, managed bool
		if err := rows.Scan(&m.ID, &m.Connection, &m.Base, &m.Name, &m.Description, &m.Prompt, &params, &caps, &feats,
			&m.DefaultFeatures, &m.FileContext, &tools, &know, &m.Hooks, &sugg, &m.Hidden, &active, &m.Image, &managed); err != nil {
			return nil, err
		}
		for _, c := range []struct {
			raw []byte
			dst any
		}{{params, &m.Params}, {caps, &m.Capabilities}, {feats, &m.Features}, {tools, &m.Tools}, {know, &m.Knowledge}, {sugg, &m.Suggestions}} {
			if err := json.Unmarshal(c.raw, c.dst); err != nil {
				return nil, fmt.Errorf("model %s: %w", m.ID, err)
			}
		}
		m.Active, m.Grants = &active, grants[m.ID]
		out = append(out, row{id: m.ID, v: normModel(m), managed: managed})
	}
	return out, rows.Err()
}

func (r *run) writeModel(m Model) error {
	fc := m.FileContext
	if fc == "" {
		fc = "tools"
	}
	_, err := r.tx.Exec(r.ctx, `INSERT INTO model (id, connection_id, base_model_id, name, description, system_prompt, params,
		capabilities, features, default_features, file_context, tools, knowledge, hooks, suggestions, hidden, active, image_url, managed)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, true)
		ON CONFLICT (id) DO UPDATE SET connection_id = EXCLUDED.connection_id, base_model_id = EXCLUDED.base_model_id,
		name = EXCLUDED.name, description = EXCLUDED.description, system_prompt = EXCLUDED.system_prompt, params = EXCLUDED.params,
		capabilities = EXCLUDED.capabilities, features = EXCLUDED.features, default_features = EXCLUDED.default_features,
		file_context = EXCLUDED.file_context, tools = EXCLUDED.tools, knowledge = EXCLUDED.knowledge, hooks = EXCLUDED.hooks,
		suggestions = EXCLUDED.suggestions, hidden = EXCLUDED.hidden, active = EXCLUDED.active, image_url = EXCLUDED.image_url,
		managed = true, updated_at = now()`,
		m.ID, m.Connection, m.Base, m.Name, m.Description, m.Prompt, jsonCol(m.Params, "{}"), jsonCol(m.Capabilities, "{}"),
		jsonCol(m.Features, "{}"), strs(m.DefaultFeatures), fc, jsonCol(m.Tools, "{}"), jsonCol(m.Knowledge, "[]"),
		strs(m.Hooks), jsonCol(m.Suggestions, "[]"), m.Hidden, m.Active == nil || *m.Active, m.Image)
	if err != nil {
		return wrap(err, "write model %s", m.ID)
	}
	return r.setGrants(access.Model, m.ID, m.Grants)
}

// ---- knowledge grants ------------------------------------------------------------------

// knowledgeIDs maps collection names to ids; a name used twice is ambiguous.
func (r *run) knowledgeIDs() (map[string][]string, error) {
	rows, err := r.tx.Query(r.ctx, `SELECT name, id FROM knowledge ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("read knowledge: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var name, id string
		if err := rows.Scan(&name, &id); err != nil {
			return nil, err
		}
		out[name] = append(out[name], id)
	}
	return out, rows.Err()
}

func (r *run) readKnowledgeGrants() ([]row, error) {
	grants, err := r.readGrants(access.Knowledge)
	if err != nil {
		return nil, err
	}
	names, err := r.knowledgeIDs()
	if err != nil {
		return nil, err
	}
	var out []row
	for name, kids := range names {
		if len(kids) == 1 && len(grants[kids[0]]) > 0 {
			out = append(out, row{id: name, v: KnowledgeGrant{Name: name, Grants: grants[kids[0]]}, managed: true})
		}
	}
	slices.SortFunc(out, func(a, b row) int { return strings.Compare(a.id, b.id) })
	return out, nil
}

// ---- helpers --------------------------------------------------------------------------

func (r *run) sealB64(s string) (string, error) {
	b, err := r.keys.EncryptString(s)
	if err != nil || b == nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// staleSecret stands for a stored secret that no longer decrypts (the
// KERA_SECRET_KEY changed). It never equals a configured value, so the plan
// shows an update and apply re-encrypts the value from the file.
const staleSecret = "\x00undecryptable"

// openStored decrypts a stored secret for comparison; an undecryptable one
// becomes staleSecret with a warning instead of failing the whole apply.
func (r *run) openStored(what string, enc []byte) string {
	v, err := r.keys.DecryptString(enc)
	if err != nil {
		r.res.Warnings = append(r.res.Warnings, what+" cannot be decrypted with the current KERA_SECRET_KEY; it will be rewritten from the config")
		return staleSecret
	}
	return v
}

func (r *run) openB64(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return r.keys.DecryptString(b)
}

// jsonCol encodes v for a jsonb column; nil and empty values become empty.
func jsonCol(v any, empty string) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return empty
	}
	return string(b)
}

// strs keeps text[] columns NOT NULL.
func strs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nilIfTrue(b *bool) *bool {
	if b != nil && *b {
		return nil
	}
	return b
}

func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}
