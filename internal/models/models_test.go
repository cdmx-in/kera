package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

// fakeServers stands up an Ollama server (qwen, llava) and an OpenAI server
// (gpt-a, gpt-b) and records the last chat request headers.
type fakeServers struct {
	ollama, openai *httptest.Server
	tagCalls       atomic.Int32
	lastHeader     atomic.Value // http.Header
}

func newFakes(t *testing.T) *fakeServers {
	f := &fakeServers{}
	f.ollama = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			f.tagCalls.Add(1)
			io.WriteString(w, `{"models":[{"name":"qwen","model":"qwen"},{"name":"llava","model":"llava"}]}`)
		case "/api/chat":
			f.lastHeader.Store(r.Header.Clone())
			io.WriteString(w, `{"message":{"content":"hi"},"done":true,"done_reason":"stop"}`+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	f.openai = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			http.Error(w, `{"error":"bad key"}`, 401)
			return
		}
		io.WriteString(w, `{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`)
	}))
	t.Cleanup(f.ollama.Close)
	t.Cleanup(f.openai.Close)
	return f
}

type env struct {
	db     *pgxpool.Pool
	keys   *secret.Keys
	st     *settings.Store
	reg    *Registry
	srv    http.Handler
	fakes  *fakeServers
	tokens map[string]string // user id -> bearer
}

func setup(t *testing.T) *env {
	t.Helper()
	db := testdb.New(t)
	ctx := context.Background()
	keys, err := secret.Derive(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(db)
	srv, err := api.New(config.Env{}, db)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{DB: db, Keys: keys, Settings: st, Server: srv, Auth: auth.New(db, keys, st, false)}
	if err := Mount(a); err != nil {
		t.Fatal(err)
	}
	e := &env{db: db, keys: keys, st: st, reg: For(a), srv: srv.Handler(), fakes: newFakes(t), tokens: map[string]string{}}
	for _, u := range [][3]string{{"admin", "admin@example.com", "admin"}, {"alice", "alice@example.com", "user"}, {"bob", "bob@example.com", "user"}} {
		mustExec(t, db, `INSERT INTO "user" (id, name, email, role) VALUES ($1, $1, $2, $3)`, u[0], u[1], u[2])
		tok, err := auth.IssueToken(keys, u[0], time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		e.tokens[u[0]] = tok
	}
	// bob uses an API key
	key, hash, prefix := auth.NewAPIKey()
	mustExec(t, db, `INSERT INTO api_key (id, user_id, key_hash, prefix) VALUES ('k1', 'bob', $1, $2)`, hash, prefix)
	e.tokens["bob-key"] = key
	if err := st.Set(ctx, "user.permissions", map[string]any{"features": map[string]any{"api_keys": true}}, false); err != nil {
		t.Fatal(err)
	}
	enc, err := keys.EncryptString("upstream-key")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO connection (id, kind, base_url, sort, options) VALUES ('local', 'ollama', $1, 0,
		'{"forward_user_headers": {"email": "X-User-Email", "role": "X-User-Role"}}')`, e.fakes.ollama.URL)
	mustExec(t, db, `INSERT INTO connection (id, kind, base_url, api_key_enc, prefix, model_ids, sort, headers)
		VALUES ('web', 'responses', $1, $2, 'web', '{gpt-a}', 1, '{"X-Static": "yes"}')`, e.fakes.openai.URL+"/v1", enc)
	mustExec(t, db, `INSERT INTO connection (id, kind, base_url, enabled) VALUES ('off', 'openai', 'http://127.0.0.1:1', false)`)
	// rows: hidden base override, a preset for everyone, a group-only preset
	mustExec(t, db, `INSERT INTO model (id, connection_id, name, hidden, params) VALUES ('qwen', 'local', 'Qwen', true, '{"num_ctx": 65536}')`)
	mustExec(t, db, `INSERT INTO model (id, base_model_id, name, system_prompt, description, tools, knowledge, suggestions, features)
		VALUES ('kera', 'web.gpt-a', 'Kera', 'You are Kera.', 'KB. Built 2026-01-01.',
		'{"servers": ["kb"], "builtins": {"ask_user": false, "execute_code": true}}', '[{"id": "k1", "mode": "full"}]',
		'[{"title": ["Hi"], "content": "Hello"}]', '{"web_search": "native", "memory": false}')`)
	mustExec(t, db, `INSERT INTO model (id, base_model_id, name) VALUES ('finance', 'qwen', 'Finance Desk')`)
	must(t, access.Replace(ctx, db, access.Model, "kera", []access.Grant{{Principal: "*"}}, false))
	must(t, access.Replace(ctx, db, access.Model, "qwen", []access.Grant{{Principal: "user:bob"}}, false))
	must(t, access.Replace(ctx, db, access.Model, "finance", []access.Grant{{Principal: "user:alice", Permission: access.Write}}, false))
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustExec(t *testing.T, db *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) call(t *testing.T, who, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if who != "" {
		req.Header.Set("Authorization", "Bearer "+e.tokens[who])
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (e *env) json(t *testing.T, who, method, path string, body any, want int) map[string]any {
	t.Helper()
	code, b := e.call(t, who, method, path, body)
	if code != want {
		t.Fatalf("%s %s as %s: %d %s", method, path, who, code, b)
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func idList(data any) string {
	var out []string
	for _, m := range data.([]any) {
		out = append(out, m.(map[string]any)["id"].(string))
	}
	return strings.Join(out, ",")
}

func TestRegistryVisibility(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	admin, _ := e.reg.authUser(ctx, "admin")
	alice, _ := e.reg.authUser(ctx, "alice")
	bob, _ := e.reg.authUser(ctx, "bob")
	list := func(u *auth.User, hidden bool) string {
		t.Helper()
		ms, err := e.reg.List(ctx, u, hidden)
		must(t, err)
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return strings.Join(out, ",")
	}
	// admin: everything visible, sorted by name; web.gpt-b filtered by model_ids, "off" not queried
	if got := list(admin, true); got != "finance,kera,llava,qwen,web.gpt-a" {
		t.Fatalf("admin hidden: %s", got)
	}
	if got := list(admin, false); got != "finance,kera,llava,web.gpt-a" {
		t.Fatalf("admin: %s", got)
	}
	if got := list(alice, false); got != "finance,kera" {
		t.Fatalf("alice: %s", got)
	}
	if got := list(bob, false); got != "kera" {
		t.Fatalf("bob: %s", got)
	}
	if got := list(bob, true); got != "kera,qwen" {
		t.Fatalf("bob hidden: %s", got)
	}
	must(t, e.st.Set(ctx, "models.base_public", true, false))
	must(t, e.st.Set(ctx, "site.model_order", []string{"kera", "web.gpt-a"}, false))
	if got := list(bob, false); got != "kera,web.gpt-a,llava" {
		t.Fatalf("bob base_public: %s", got)
	}
	if n := e.fakes.tagCalls.Load(); n != 1 {
		t.Fatalf("discovery not cached: %d tag calls", n)
	}

	// Resolve
	m, err := e.reg.Resolve(ctx, alice, "finance")
	must(t, err)
	if m.ProviderModel != "qwen" || m.Params["num_ctx"] != 65536.0 || !m.Local || m.Connection.ID != "local" {
		t.Fatalf("finance %+v", m)
	}
	if m.UserHeaders.Get("X-User-Email") != "alice@example.com" || m.UserHeaders.Get("X-User-Role") != "user" || len(m.UserHeaders) != 2 {
		t.Fatalf("user headers %v", m.UserHeaders)
	}
	k, err := e.reg.Resolve(ctx, bob, "kera")
	must(t, err)
	if k.Connection.APIKey != "upstream-key" || k.ProviderModel != "gpt-a" || k.Local || k.SystemPrompt != "You are Kera." ||
		len(k.Knowledge) != 1 || k.Knowledge[0].Mode != "full" || k.Tools.Servers[0] != "kb" || len(k.UserHeaders) != 0 {
		t.Fatalf("kera %+v", k)
	}
	if _, err := e.reg.Resolve(ctx, bob, "qwen"); err != nil {
		t.Fatalf("hidden model must resolve: %v", err)
	}
	if _, err := e.reg.Resolve(ctx, bob, "finance"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("finance for bob: %v", err)
	}
	if _, err := e.reg.Resolve(ctx, admin, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	// Client carries connection + user headers
	cl, err := e.reg.Client(m)
	must(t, err)
	for _, err := range cl.Stream(ctx, provider.Request{Model: m.ProviderModel}) {
		must(t, err)
	}
	h := e.fakes.lastHeader.Load().(http.Header)
	if h.Get("X-User-Email") != "alice@example.com" {
		t.Fatalf("forwarded headers %v", h)
	}
}

func TestImplicitOllama(t *testing.T) {
	db := testdb.New(t)
	keys, _ := secret.Derive(strings.Repeat("s", 32))
	f := newFakes(t)
	r := New(db, keys, settings.New(db), f.ollama.URL)
	ms, err := r.List(context.Background(), &auth.User{ID: "root", Role: "admin"}, false)
	must(t, err)
	if len(ms) != 2 || ms[0].Connection.ID != "ollama" || !ms[0].Connection.Implicit {
		t.Fatalf("%+v", ms)
	}
}

func TestModelRoutes(t *testing.T) {
	e := setup(t)

	// list shape (admin)
	out := e.json(t, "admin", "GET", "/api/models", nil, 200)
	if idList(out["data"]) != "finance,kera,llava,web.gpt-a" {
		t.Fatalf("admin list %s", idList(out["data"]))
	}
	var kera map[string]any
	for _, m := range out["data"].([]any) {
		if m.(map[string]any)["id"] == "kera" {
			kera = m.(map[string]any)
		}
	}
	meta := kera["info"].(map[string]any)["meta"].(map[string]any)
	if kera["owned_by"] != "openai" || kera["connection_type"] != "external" || kera["local"] != false ||
		strings.Join(toStrings(kera["features"]), ",") != "web_search" ||
		strings.Join(toStrings(kera["toolIds"]), ",") != "server:kb,builtin:execute_code" ||
		meta["description"] != "KB. Built 2026-01-01." || kera["object"] != "model" {
		t.Fatalf("kera entry %v", kera)
	}
	if _, has := kera["info"].(map[string]any)["params"].(map[string]any)["system"]; has {
		t.Fatal("system prompt leaked in /api/models")
	}
	// hidden: API key and include_hidden see it, the UI list does not
	if got := idList(e.json(t, "bob", "GET", "/api/v1/models", nil, 200)["data"]); got != "kera" {
		t.Fatalf("bob ui %s", got)
	}
	if got := idList(e.json(t, "bob-key", "GET", "/v1/models", nil, 200)["data"]); got != "kera,qwen" {
		t.Fatalf("bob key %s", got)
	}
	if got := idList(e.json(t, "bob", "GET", "/api/models?include_hidden=true", nil, 200)["data"]); got != "kera,qwen" {
		t.Fatalf("bob include_hidden %s", got)
	}
	if code, _ := e.call(t, "", "GET", "/api/models", nil); code != 401 {
		t.Fatalf("anonymous %d", code)
	}

	// row GET + stamp-style round trip of meta.description
	m := e.json(t, "admin", "GET", "/api/v1/models/model?id=kera", nil, 200)
	if m["params"].(map[string]any)["system"] != "You are Kera." || m["base_model_id"] != "web.gpt-a" {
		t.Fatalf("row %v", m)
	}
	m["meta"].(map[string]any)["description"] = "KB. Updated 2026-11-04."
	upd := e.json(t, "admin", "POST", "/api/v1/models/model/update?id=kera", m, 200)
	um := upd["meta"].(map[string]any)
	if um["description"] != "KB. Updated 2026-11-04." || upd["params"].(map[string]any)["system"] != "You are Kera." ||
		strings.Join(toStrings(um["toolIds"]), ",") != "server:kb,builtin:execute_code" ||
		um["builtinTools"].(map[string]any)["ask_user"] != false || len(um["knowledge"].([]any)) != 1 {
		t.Fatalf("after update %v", upd)
	}
	// partial update leaves the rest alone
	e.json(t, "admin", "POST", "/api/v1/models/model/update?id=kera", map[string]any{"name": "Kera 2"}, 200)
	k, err := e.reg.Resolve(context.Background(), &auth.User{Role: "admin"}, "kera")
	must(t, err)
	if k.Name != "Kera 2" || k.Description != "KB. Updated 2026-11-04." || k.SystemPrompt != "You are Kera." {
		t.Fatalf("resolved %+v", k)
	}

	// write access: alice may edit finance (write grant) but not kera (read grant)
	e.json(t, "alice", "POST", "/api/v1/models/model/update?id=finance", map[string]any{"meta": map[string]any{"description": "d"}}, 200)
	e.json(t, "alice", "POST", "/api/v1/models/model/update?id=kera", map[string]any{"name": "x"}, 403)
	e.json(t, "alice", "GET", "/api/v1/models/model?id=qwen", nil, 404)
	e.json(t, "alice", "POST", "/api/v1/models/create", map[string]any{"id": "mine"}, 403)
	e.json(t, "admin", "POST", "/api/v1/models/model/update?id=kera", map[string]any{"meta": map[string]any{"fileContext": "bogus"}}, 400)

	// a discovered model without a row: GET synthesises it, update creates the row
	e.json(t, "admin", "GET", "/api/v1/models/model?id=llava", nil, 200)
	e.json(t, "admin", "POST", "/api/v1/models/model/update?id=llava", map[string]any{"meta": map[string]any{"hidden": true}}, 200)
	if got := idList(e.json(t, "admin", "GET", "/api/models", nil, 200)["data"]); got != "finance,kera,web.gpt-a" {
		t.Fatalf("after hiding llava %s", got)
	}

	// create, toggle, delete
	created := e.json(t, "admin", "POST", "/api/v1/models/create", map[string]any{"id": "helper", "name": "Helper",
		"base_model_id": "qwen", "params": map[string]any{"system": "Help.", "temperature": 0.3},
		"meta": map[string]any{"toolIds": []string{"builtin:search_web"}}}, 200)
	if created["user_id"] != "admin" || created["is_active"] != true {
		t.Fatalf("created %v", created)
	}
	e.json(t, "admin", "POST", "/api/v1/models/create", map[string]any{"id": "helper"}, 409)
	h, err := e.reg.Resolve(context.Background(), &auth.User{Role: "admin"}, "helper")
	must(t, err)
	if h.SystemPrompt != "Help." || h.Params["num_ctx"] != 65536.0 || h.Params["temperature"] != 0.3 || !h.Tools.Builtins["search_web"] {
		t.Fatalf("helper %+v", h)
	}
	if e.json(t, "admin", "POST", "/api/v1/models/model/toggle?id=helper", nil, 200)["is_active"] != false {
		t.Fatal("toggle")
	}
	if _, err := e.reg.Resolve(context.Background(), &auth.User{Role: "admin"}, "helper"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inactive resolves: %v", err)
	}
	rows := e.call
	code, b := rows(t, "admin", "GET", "/api/v1/models/", nil)
	if code != 200 || !strings.Contains(string(b), `"helper"`) {
		t.Fatalf("rows %d %s", code, b)
	}
	code, b = rows(t, "alice", "GET", "/api/v1/models/", nil)
	if code != 200 || !strings.Contains(string(b), `"finance"`) || strings.Contains(string(b), `"kera"`) {
		t.Fatalf("alice rows %d %s", code, b)
	}
	e.json(t, "alice", "DELETE", "/api/v1/models/model/delete?id=helper", nil, 404) // cannot see it
	if code, _ := e.call(t, "admin", "DELETE", "/api/v1/models/model/delete?id=helper", nil); code != 200 {
		t.Fatalf("delete %d", code)
	}
	e.json(t, "admin", "GET", "/api/v1/models/model?id=helper", nil, 404)
}

func TestConnectionRoutes(t *testing.T) {
	e := setup(t)
	out := e.json(t, "admin", "GET", "/api/v1/configs/connections", nil, 200)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "upstream-key") || !strings.Contains(string(raw), `"has_key":true`) {
		t.Fatalf("connections %s", raw)
	}
	e.json(t, "alice", "GET", "/api/v1/configs/connections", nil, 403)

	// upsert without api_key keeps the stored key; a new connection gets one
	e.json(t, "admin", "POST", "/api/v1/configs/connections", map[string]any{"connections": []map[string]any{
		{"id": "web", "kind": "responses", "base_url": e.fakes.openai.URL + "/", "prefix": "web", "model_ids": []string{}},
		{"id": "extra", "kind": "openai", "base_url": e.fakes.openai.URL, "api_key": "upstream-key", "prefix": "x"},
	}}, 200)
	c, err := e.reg.Connection(context.Background(), "web")
	must(t, err)
	if c.APIKey != "upstream-key" || c.BaseURL != e.fakes.openai.URL || len(c.ModelIDs) != 0 {
		t.Fatalf("web %+v", c.BaseURL)
	}
	v := e.json(t, "admin", "POST", "/api/v1/configs/connections/extra/verify", nil, 200)
	if strings.Join(toStrings(v["models"]), ",") != "gpt-a,gpt-b" {
		t.Fatalf("verify %v", v)
	}
	// model_ids cleared and the cache invalidated: gpt-b now listed for web too
	if got := idList(e.json(t, "admin", "GET", "/api/models", nil, 200)["data"]); !strings.Contains(got, "web.gpt-b") || !strings.Contains(got, "x.gpt-a") {
		t.Fatalf("after upsert %s", got)
	}
	// clearing the key breaks verify with a 502
	e.json(t, "admin", "POST", "/api/v1/configs/connections", map[string]any{"connections": []map[string]any{
		{"id": "extra", "kind": "openai", "base_url": e.fakes.openai.URL, "api_key": ""}}}, 200)
	e.json(t, "admin", "POST", "/api/v1/configs/connections/extra/verify", nil, 502)
	e.json(t, "admin", "POST", "/api/v1/configs/connections", map[string]any{"connections": []map[string]any{
		{"id": "bad", "kind": "gemini", "base_url": "http://x"}}}, 400)
	e.json(t, "admin", "DELETE", "/api/v1/configs/connections/extra", nil, 200)
	e.json(t, "admin", "DELETE", "/api/v1/configs/connections/extra", nil, 404)
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// authUser loads a user the way the auth middleware does (test helper).
func (r *Registry) authUser(ctx context.Context, id string) (*auth.User, error) {
	return auth.New(r.DB, r.Keys, r.Settings, false).LoadUser(ctx, id)
}

func TestModelWriteGuards(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	must(t, e.st.Set(ctx, "user.permissions", map[string]any{"features": map[string]any{"api_keys": true},
		"workspace": map[string]any{"models": true}}, false))

	// a user may not shadow a discovered model or an invisible one with a new row
	e.json(t, "alice", "POST", "/api/v1/models/create", map[string]any{"id": "llava", "params": map[string]any{"system": "pwned"}}, 409)
	// presets only over models the user can use
	e.json(t, "alice", "POST", "/api/v1/models/create", map[string]any{"id": "mine", "base_model_id": "qwen"}, 400)
	e.json(t, "alice", "POST", "/api/v1/models/create", map[string]any{"id": "mine", "base_model_id": "kera"}, 200)
	e.json(t, "alice", "POST", "/api/v1/models/model/update?id=mine", map[string]any{"base_model_id": "web.gpt-a"}, 400)
	// admins may create an override row for a discovered model
	e.json(t, "admin", "POST", "/api/v1/models/create", map[string]any{"id": "llava", "name": "LLaVA"}, 200)

	// 404, not 403, for models the caller cannot see
	e.json(t, "alice", "POST", "/api/v1/models/model/update?id=qwen", map[string]any{"name": "x"}, 404)
	e.json(t, "alice", "POST", "/api/v1/models/model/toggle?id=qwen", nil, 404)
	e.json(t, "alice", "DELETE", "/api/v1/models/model/delete?id=qwen", nil, 404)
	e.json(t, "alice", "DELETE", "/api/v1/models/model/delete?id=kera", nil, 403)

	// cookie-authenticated cross-site writes are refused; same-origin ones pass
	post := func(site string) int {
		req := httptest.NewRequest("POST", "/api/v1/models/model/toggle?id=llava", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: e.tokens["admin"]})
		req.Header.Set("Sec-Fetch-Site", site)
		rec := httptest.NewRecorder()
		e.srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post("cross-site"); code != http.StatusUnauthorized { // cookie ignored cross-site (auth.Credential)
		t.Fatalf("cross-site toggle %d", code)
	}
	if code := post("same-origin"); code != 200 {
		t.Fatalf("same-origin toggle %d", code)
	}

	// the list carries builtinTools in info.meta
	for _, m := range e.json(t, "admin", "GET", "/api/models", nil, 200)["data"].([]any) {
		if mm := m.(map[string]any); mm["id"] == "kera" {
			bt := mm["info"].(map[string]any)["meta"].(map[string]any)["builtinTools"].(map[string]any)
			if bt["execute_code"] != true || bt["ask_user"] != false {
				t.Fatalf("builtinTools %v", bt)
			}
		}
	}
}

// A connection whose stored key cannot be decrypted (secret key changed) is
// skipped with key_error; the other connections' models stay listed.
func TestUndecryptableConnectionKey(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	mustExec(t, e.db, `INSERT INTO connection (id, kind, base_url, api_key_enc, sort) VALUES ('broken', 'openai', 'http://127.0.0.1:1', '\x0102030405060708090a0b0c0d0e0f10', 9)`)
	admin, _ := e.reg.authUser(ctx, "admin")
	ms, err := e.reg.List(ctx, admin, true)
	if err != nil || len(ms) == 0 {
		t.Fatalf("list with a broken connection: %d models, %v", len(ms), err)
	}
	conns, err := e.reg.Connections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range conns {
		if (c.ID == "broken") != (c.KeyError != "") {
			t.Fatalf("connection %s key_error=%q", c.ID, c.KeyError)
		}
	}
}

// Embedding and rerank models (by server report or by name) stay out of chat
// lists, show their kind with ?kinds=all, and resolve with that kind.
func TestModelKinds(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	keys, _ := secret.Derive(strings.Repeat("s", 32))
	ol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"model":"qwen"},{"model":"all-minilm"},{"model":"bge-reranker-v2-m3"}]}`)
		case "/api/show":
			var b struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			caps := map[string]string{"qwen": `["completion","tools"]`, "all-minilm": `["embedding"]`, "bge-reranker-v2-m3": `["completion"]`}[b.Model]
			io.WriteString(w, `{"capabilities":`+caps+`}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ol.Close()
	oa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"gpt-a"},{"id":"cohere-rerank"},{"id":"small-vectors"}]}`)
		case "/v1/model/info":
			io.WriteString(w, `{"data":[{"model_name":"gpt-a","model_info":{"mode":"chat"}},
				{"model_name":"cohere-rerank","model_info":{"mode":"rerank"}},{"model_name":"small-vectors","model_info":{"mode":"embedding"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer oa.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"text-embedding-3-small"},{"id":"gpt-b"}]}`)
			return
		}
		http.NotFound(w, r) // no /model/info: names decide
	}))
	defer plain.Close()
	mustExec(t, db, `INSERT INTO connection (id, kind, base_url, sort) VALUES ('ol', 'ollama', $1, 0)`, ol.URL)
	mustExec(t, db, `INSERT INTO connection (id, kind, base_url, sort) VALUES ('lite', 'openai', $1, 1)`, oa.URL)
	mustExec(t, db, `INSERT INTO connection (id, kind, base_url, prefix, sort) VALUES ('plain', 'openai', $1, 'p', 2)`, plain.URL)
	r := New(db, keys, settings.New(db), "")
	ms, err := r.List(ctx, &auth.User{ID: "root", Role: "admin"}, true)
	must(t, err)
	got := map[string]string{}
	for _, m := range ms {
		got[m.ID] = m.Kind
	}
	want := map[string]string{"qwen": "", "all-minilm": "embedding", "bge-reranker-v2-m3": "rerank", "gpt-a": "",
		"cohere-rerank": "rerank", "small-vectors": "embedding", "p.text-embedding-3-small": "embedding", "p.gpt-b": ""}
	for id, k := range want {
		if g, ok := got[id]; !ok || g != k {
			t.Errorf("%s: kind %q (listed %v), want %q", id, g, ok, k)
		}
	}
	if m, err := r.Resolve(ctx, &auth.User{ID: "root", Role: "admin"}, "all-minilm"); err != nil || m.Kind != "embedding" {
		t.Fatalf("resolve %+v %v", m, err)
	}
	if modelKind("Rerank-English", nil) != "rerank" || modelKind("nomic-embed-text", nil) != "embedding" || modelKind("qwen3", nil) != "" {
		t.Fatal("name heuristics")
	}
}

func TestModelListHidesNonChatKinds(t *testing.T) {
	e := setup(t)
	e.reg.mu.Lock()
	e.reg.cache["local"] = discovery{key: "ollama|" + e.fakes.ollama.URL, ids: []string{"qwen", "llava", "mxbai-embed-large"},
		kinds: map[string]string{"mxbai-embed-large": "embedding"}, at: time.Now()}
	e.reg.mu.Unlock()
	if got := idList(e.json(t, "admin", "GET", "/api/models", nil, 200)["data"]); got != "finance,kera,llava,web.gpt-a" {
		t.Fatalf("chat list %s", got)
	}
	out := e.json(t, "admin", "GET", "/api/models?include_hidden=true&kinds=all", nil, 200)
	kinds := map[string]any{}
	for _, m := range out["data"].([]any) {
		kinds[m.(map[string]any)["id"].(string)] = m.(map[string]any)["kind"]
	}
	if kinds["mxbai-embed-large"] != "embedding" || kinds["kera"] != "chat" {
		t.Fatalf("kinds %v", kinds)
	}
}
