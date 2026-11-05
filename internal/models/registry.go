// Package models is the model registry: connections to model servers, model
// discovery, model rows (base-model overrides and presets) merged into one
// list, per-user visibility, and resolution of a model id to everything the
// chat engine needs to call it.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/guard"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
)

const (
	discoveryTTL     = 30 * time.Second
	discoveryTimeout = 10 * time.Second
	maxPresetDepth   = 3
)

// Errors returned by Resolve; they are *api.Error, so handlers can api.Fail them.
var (
	ErrNotFound  = api.Errf(http.StatusNotFound, "model_not_found", "model not found")
	ErrForbidden = api.Errf(http.StatusForbidden, "model_forbidden", "you do not have access to this model")
	ErrNotChat   = api.Errf(http.StatusBadRequest, "model_not_chat", "this is an embedding or rerank model; it cannot chat")
)

// Connection is one model server.
type Connection struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"` // ollama | openai | responses
	BaseURL  string            `json:"base_url"`
	APIKey   string            `json:"-"` // decrypted; never serialised
	Prefix   string            `json:"prefix"`
	ModelIDs []string          `json:"model_ids"`
	Enabled  bool              `json:"enabled"`
	Headers  map[string]string `json:"headers"`
	Options  json.RawMessage   `json:"options"`
	Sort     int               `json:"sort"`
	Managed  bool              `json:"managed"`
	Implicit bool              `json:"implicit,omitempty"`  // from OLLAMA_BASE_URL, not stored
	KeyError string            `json:"key_error,omitempty"` // stored key cannot be decrypted (KERA_SECRET_KEY changed?)
}

type connOptions struct {
	ForwardUserHeaders map[string]string `json:"forward_user_headers"`
	WebSearch          json.RawMessage   `json:"web_search"`
}

func (c Connection) options() connOptions {
	var o connOptions
	if len(c.Options) > 0 {
		_ = json.Unmarshal(c.Options, &o)
	}
	return o
}

// WebSearch returns the provider-native web search options (Responses connections).
func (c Connection) WebSearch() json.RawMessage { return c.options().WebSearch }

// UserHeaders returns the headers that identify u to the model server, per
// options.forward_user_headers ({"email": "<header>", "name", "id", "role"}).
// None are sent when the option is absent.
func (c Connection) UserHeaders(u *auth.User) http.Header {
	h := http.Header{}
	if u == nil {
		return h
	}
	vals := map[string]string{"email": u.Email, "name": u.Name, "id": u.ID, "role": u.Role}
	for field, name := range c.options().ForwardUserHeaders {
		if v, ok := vals[field]; ok && name != "" && v != "" {
			h.Set(name, strings.NewReplacer("\r", " ", "\n", " ").Replace(v))
		}
	}
	return h
}

func (c Connection) modelID(providerID string) string {
	if c.Prefix == "" {
		return providerID
	}
	return c.Prefix + "." + providerID
}

// Tools is a model's tool configuration.
type Tools struct {
	Servers  []string        `json:"servers,omitempty"`
	Builtins map[string]bool `json:"builtins,omitempty"`
	Disabled []string        `json:"disabled,omitempty"`
}

// IDs renders tools as ["server:<id>", "builtin:<name>"] (enabled builtins only).
func (t Tools) IDs() []string {
	out := []string{}
	for _, s := range t.Servers {
		out = append(out, "server:"+s)
	}
	for _, b := range slices.Sorted(maps.Keys(t.Builtins)) {
		if t.Builtins[b] {
			out = append(out, "builtin:"+b)
		}
	}
	return out
}

// KnowledgeRef attaches a knowledge collection by id or by name (config
// files name collections); Mode is auto (unset), retrieve, full or tools
// (see engine.knowledgeMode).
type KnowledgeRef struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// Resolved is a model as the engine and the model list see it: a discovered
// base model with its override row applied, or a preset merged over its base.
type Resolved struct {
	ID              string
	Name            string
	Description     string
	BaseModelID     string // set for presets
	Connection      Connection
	ProviderModel   string // id on the model server (no prefix)
	SystemPrompt    string
	Params          map[string]any
	Capabilities    map[string]any
	Features        map[string]any
	DefaultFeatures []string
	Tools           Tools
	Knowledge       []KnowledgeRef
	FileContext     string // tools | full
	Hooks           []string
	Suggestions     json.RawMessage
	ImageURL        string
	Kind            string // "" for chat models, provider.ModelEmbedding or provider.ModelRerank
	Local           bool
	Hidden          bool
	Created         time.Time
	UserHeaders     http.Header // set by Resolve from the connection's forward_user_headers

	hasRow bool
	active bool
}

// Registry lists and resolves models.
type Registry struct {
	DB        *pgxpool.Pool
	Keys      *secret.Keys
	Settings  *settings.Store
	OllamaURL string       // implicit connection when the connection table is empty
	HTTP      *http.Client // for model servers; nil = http.DefaultClient

	mu    sync.Mutex
	cache map[string]discovery
}

type discovery struct {
	key   string // kind|base_url, so an edited connection is refetched
	ids   []string
	kinds map[string]string // provider id -> embedding | rerank (KindLister)
	err   error
	at    time.Time
}

// New returns a registry.
func New(db *pgxpool.Pool, keys *secret.Keys, st *settings.Store, ollamaURL string) *Registry {
	return &Registry{DB: db, Keys: keys, Settings: st, OllamaURL: ollamaURL, cache: map[string]discovery{}}
}

// Invalidate drops cached discovery results (all when no id is given).
func (r *Registry) Invalidate(ids ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(ids) == 0 {
		clear(r.cache)
	}
	for _, id := range ids {
		delete(r.cache, id)
	}
}

// Connections returns every connection (disabled ones included), ordered by
// sort then id, with API keys decrypted.
func (r *Registry) Connections(ctx context.Context) ([]Connection, error) {
	rows, err := r.DB.Query(ctx, `SELECT id, kind, base_url, api_key_enc, prefix, model_ids, enabled, headers, options, sort, managed
		FROM connection ORDER BY sort, id`)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		var c Connection
		var key []byte
		if err := rows.Scan(&c.ID, &c.Kind, &c.BaseURL, &key, &c.Prefix, &c.ModelIDs, &c.Enabled, &c.Headers, &c.Options, &c.Sort, &c.Managed); err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		if c.APIKey, err = r.Keys.DecryptString(key); err != nil {
			// One bad key must not hide every model: the connection is skipped
			// by discovery and shown to admins with the error.
			slog.Error("connection api key cannot be decrypted; re-enter it or restore KERA_SECRET_KEY", "connection", c.ID, "err", err)
			c.KeyError = "stored API key cannot be decrypted; re-enter it"
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 && r.OllamaURL != "" {
		out = append(out, Connection{ID: "ollama", Kind: provider.KindOllama, BaseURL: r.OllamaURL, Enabled: true,
			Options: json.RawMessage(`{}`), Implicit: true})
	}
	return out, nil
}

// Connection returns one connection by id.
func (r *Registry) Connection(ctx context.Context, id string) (Connection, error) {
	conns, err := r.Connections(ctx)
	if err != nil {
		return Connection{}, err
	}
	for _, c := range conns {
		if c.ID == id {
			return c, nil
		}
	}
	return Connection{}, api.Errf(http.StatusNotFound, "connection_not_found", "connection not found")
}

// ConnectionClient returns a client for a connection id (embeddings, tasks).
func (r *Registry) ConnectionClient(ctx context.Context, id string) (provider.Client, error) {
	c, err := r.Connection(ctx, id)
	if err != nil {
		return nil, err
	}
	cl, err := r.client(c, nil)
	if err != nil {
		return nil, err
	}
	return r.guarded(cl, c, c.Kind == provider.KindOllama, nil, c.ID), nil
}

// Client returns a client for m; connection headers and the user headers
// from Resolve are sent with every request.
func (r *Registry) Client(m *Resolved) (provider.Client, error) {
	cl, err := r.client(m.Connection, m.UserHeaders)
	if err != nil {
		return nil, err
	}
	return r.guarded(cl, m.Connection, m.Local, m.Features, m.ID), nil
}

// guarded wraps cl with the guardrails policy: the guardrails.* settings
// overlaid by the model's features.guardrails map. Every provider client is
// built here, so chat, tasks, compaction, memory and embeddings are covered.
func (r *Registry) guarded(cl provider.Client, c Connection, local bool, features map[string]any, modelID string) provider.Client {
	if r.Settings == nil || r.DB == nil {
		return cl
	}
	p := r.policy(features)
	var mod guard.Moderator
	if c.Kind != provider.KindOllama && p.ModerationModel != "" {
		mod = guard.OpenAIModerator(r.HTTP, c.BaseURL, c.APIKey, p.ModerationModel)
	}
	return guard.Wrap(cl, p, local, mod, r.audit(modelID))
}

func (r *Registry) policy(features map[string]any) guard.Policy {
	ctx := context.Background()
	get := func(key string, dst any) { _ = r.Settings.Get(ctx, key, dst) }
	var p guard.Policy
	get("guardrails.pii", &p.PII)
	get("guardrails.output_pii", &p.OutputPII)
	get("guardrails.injection", &p.Injection)
	get("guardrails.moderation", &p.Moderation)
	get("guardrails.moderation.model", &p.ModerationModel)
	get("guardrails.blocklist", &p.Blocklist)
	p.LocalExempt = r.Settings.Bool(ctx, "guardrails.local_exempt")
	if o, _ := features["guardrails"].(map[string]any); o != nil { // per-model override, same leaf names
		mode := func(k string, dst *guard.Mode) {
			if v, ok := o[k].(string); ok {
				*dst = guard.Mode(v)
			}
		}
		mode("pii", &p.PII)
		mode("output_pii", &p.OutputPII)
		mode("injection", &p.Injection)
		mode("moderation", &p.Moderation)
		if v, ok := o["local_exempt"].(bool); ok {
			p.LocalExempt = v
		}
		if v, ok := o["blocklist"].([]any); ok {
			p.Blocklist = nil
			for _, x := range v {
				if s, ok := x.(string); ok {
					p.Blocklist = append(p.Blocklist, s)
				}
			}
		}
	}
	return p
}

// audit writes guardrail events for modelID; the user comes from the call's
// context. Inserts run off the request path and only log failures.
func (r *Registry) audit(modelID string) guard.Audit {
	return func(ctx context.Context, evs []guard.Event) {
		var uid *string
		via := false
		if u := auth.FromContext(ctx); u != nil {
			uid, via = &u.ID, u.ViaAPIKey
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			for _, e := range evs {
				if _, err := r.DB.Exec(ctx, `INSERT INTO guardrail_event (user_id, via_api_key, model_id, rule, action, count) VALUES ($1, $2, $3, $4, $5, $6)`,
					uid, via, modelID, e.Rule, e.Action, e.Count); err != nil {
					slog.Error("guardrail audit", "err", err)
					return
				}
			}
		}()
	}
}

func (r *Registry) client(c Connection, extra http.Header) (provider.Client, error) {
	h := http.Header{}
	for k, v := range c.Headers {
		h.Set(k, v)
	}
	for k, vs := range extra {
		h[k] = vs
	}
	return provider.New(provider.Config{Kind: c.Kind, BaseURL: c.BaseURL, APIKey: c.APIKey, Headers: h, HTTP: r.HTTP})
}

// discover lists a connection's models and their kinds, cached for
// discoveryTTL (failures too, so a dead server does not stall every model list).
func (r *Registry) discover(ctx context.Context, c Connection) ([]string, map[string]string, time.Time, error) {
	key := c.Kind + "|" + c.BaseURL
	r.mu.Lock()
	d, ok := r.cache[c.ID]
	r.mu.Unlock()
	if ok && d.key == key && time.Since(d.at) < discoveryTTL {
		return d.ids, d.kinds, d.at, d.err
	}
	var kinds map[string]string
	cl, err := r.client(c, nil)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
		d.ids, err = cl.ListModels(ctx)
		if kl, ok := cl.(provider.KindLister); ok && err == nil {
			// ponytail: asked on every refresh (one /api/show per Ollama model);
			// cache per model id if a server has hundreds of models.
			kinds, _ = kl.ModelKinds(ctx, d.ids)
		}
		cancel()
	}
	d = discovery{key: key, ids: d.ids, kinds: kinds, err: err, at: time.Now()}
	if err != nil {
		d.ids = nil
		slog.Warn("model discovery failed", "connection", c.ID, "err", err)
	}
	r.mu.Lock()
	r.cache[c.ID] = d
	r.mu.Unlock()
	return d.ids, d.kinds, d.at, d.err
}

// modelKind is what the server reported, else a guess from the name: chat
// lists must not offer embedding or rerank models, and servers without a
// kind API (or Ollama rerankers, which claim "completion") are common.
func modelKind(id string, reported map[string]string) string {
	if k := reported[id]; k != "" {
		return k
	}
	n := strings.ToLower(id)
	switch {
	case strings.Contains(n, "rerank"):
		return provider.ModelRerank
	case strings.Contains(n, "embed"):
		return provider.ModelEmbedding
	}
	return ""
}

type baseModel struct {
	conn       Connection
	providerID string
	kind       string
	at         time.Time
}

// bases discovers every enabled connection's models in parallel, in
// connection order. A failing connection is skipped.
func (r *Registry) bases(ctx context.Context) ([]baseModel, error) {
	conns, err := r.Connections(ctx)
	if err != nil {
		return nil, err
	}
	found := make([][]baseModel, len(conns))
	var wg sync.WaitGroup
	for i, c := range conns {
		if !c.Enabled || c.KeyError != "" {
			continue
		}
		wg.Go(func() {
			ids, kinds, at, err := r.discover(ctx, c)
			if err != nil {
				return
			}
			for _, id := range ids {
				if len(c.ModelIDs) == 0 || slices.Contains(c.ModelIDs, id) || slices.Contains(c.ModelIDs, c.modelID(id)) {
					found[i] = append(found[i], baseModel{conn: c, providerID: id, kind: modelKind(id, kinds), at: at})
				}
			}
		})
	}
	wg.Wait()
	return slices.Concat(found...), nil
}

// all returns every model (inactive ones included), keyed by id.
func (r *Registry) all(ctx context.Context) (map[string]*Resolved, error) {
	bs, err := r.bases(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := loadRows(ctx, r.DB, "")
	if err != nil {
		return nil, err
	}
	return merge(bs, rows), nil
}

// merge applies model rows to discovered models. A row without base_model_id
// overrides the discovered model with the same id; a row with base_model_id
// is a preset over another model (up to maxPresetDepth presets deep).
func merge(bs []baseModel, rows []row) map[string]*Resolved {
	out := map[string]*Resolved{}
	for _, b := range bs {
		id := b.conn.modelID(b.providerID)
		if _, dup := out[id]; dup {
			continue // first connection wins
		}
		out[id] = &Resolved{ID: id, Name: id, Connection: b.conn, ProviderModel: b.providerID, Kind: b.kind,
			Local: b.conn.Kind == provider.KindOllama, Created: b.at, FileContext: "tools", active: true,
			Params: map[string]any{}, Capabilities: map[string]any{}, Features: map[string]any{}}
	}
	presets := map[string]*row{}
	for i := range rows {
		rw := &rows[i]
		if rw.BaseModelID != nil && *rw.BaseModelID != "" {
			presets[rw.ID] = rw
		} else if m := out[rw.ID]; m != nil {
			rw.applyTo(m)
		}
		// ponytail: an override row for a model no connection currently offers is
		// skipped, so the model disappears while its server is down.
	}
	derived := map[string]*Resolved{}
	for id, rw := range presets {
		chain := []*row{rw}
		cur := *rw.BaseModelID
		for presets[cur] != nil && len(chain) <= maxPresetDepth {
			chain = append(chain, presets[cur])
			cur = *presets[cur].BaseModelID
		}
		base := out[cur]
		if base == nil || len(chain) > maxPresetDepth {
			continue // missing base, too deep, or a cycle
		}
		m := base
		for i := len(chain) - 1; i >= 0; i-- {
			m = chain[i].presetOf(m)
		}
		derived[id] = m
	}
	maps.Copy(out, derived)
	return out
}

// List returns the models u may use, sorted by site.model_order then name.
// Hidden models are left out unless includeHidden.
func (r *Registry) List(ctx context.Context, u *auth.User, includeHidden bool) ([]*Resolved, error) {
	all, err := r.all(ctx)
	if err != nil {
		return nil, err
	}
	canSee, err := r.visibility(ctx, u)
	if err != nil {
		return nil, err
	}
	var out []*Resolved
	for _, m := range all {
		if m.active && (includeHidden || !m.Hidden) && canSee(m) {
			out = append(out, m)
		}
	}
	var order []string
	_ = r.Settings.Get(ctx, "site.model_order", &order)
	sortModels(out, order)
	return out, nil
}

// Resolve returns model id for u: ErrNotFound when it does not exist or is
// inactive, ErrForbidden when u may not use it. Hidden models resolve.
func (r *Registry) Resolve(ctx context.Context, u *auth.User, id string) (*Resolved, error) {
	all, err := r.all(ctx)
	if err != nil {
		return nil, err
	}
	m := all[id]
	if m == nil || !m.active {
		return nil, ErrNotFound
	}
	canSee, err := r.visibility(ctx, u)
	if err != nil {
		return nil, err
	}
	if !canSee(m) {
		return nil, ErrForbidden
	}
	out := *m
	out.UserHeaders = m.Connection.UserHeaders(u)
	return &out, nil
}

// visibility: admins see everything; users see models they hold a read grant
// on, plus base models without a row when models.base_public is set.
func (r *Registry) visibility(ctx context.Context, u *auth.User) (func(*Resolved) bool, error) {
	if u.IsAdmin() {
		return func(*Resolved) bool { return true }, nil
	}
	ids, _, err := access.IDs(ctx, r.DB, u, access.Model, access.Read)
	if err != nil {
		return nil, err
	}
	granted := map[string]bool{}
	for _, id := range ids {
		granted[id] = true
	}
	basePublic := r.Settings.Bool(ctx, "models.base_public")
	return func(m *Resolved) bool { return granted[m.ID] || (!m.hasRow && basePublic) }, nil
}

func sortModels(ms []*Resolved, order []string) {
	rank := map[string]int{}
	for i, id := range order {
		rank[id] = i
	}
	slices.SortStableFunc(ms, func(a, b *Resolved) int {
		ra, oka := rank[a.ID]
		rb, okb := rank[b.ID]
		switch {
		case oka && okb:
			return ra - rb
		case oka:
			return -1
		case okb:
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}
