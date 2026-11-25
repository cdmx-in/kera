package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/audit"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

type env struct {
	t     *testing.T
	a     *app.App
	srv   *httptest.Server
	admin string // admin token
	user  string // non-admin token
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	keys, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Env{PublicURL: "https://chat.example.com"}
	s, err := api.New(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(pool)
	a := &app.App{Env: cfg, DB: pool, Keys: keys, Settings: st, Server: s, Auth: auth.New(pool, keys, st, false)}
	if err := Mount(a); err != nil {
		t.Fatal(err)
	}
	if err := audit.Mount(a); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	e := &env{t: t, a: a, srv: srv}
	_, e.admin = e.mkUser("admin", "boss@example.com")
	_, e.user = e.mkUser("user", "pat@example.com")
	return e
}

func (e *env) mkUser(role, email string) (string, string) {
	e.t.Helper()
	id := ids.New()
	if _, err := e.a.DB.Exec(context.Background(), `INSERT INTO "user" (id, name, email, role) VALUES ($1, $2, $3, $4)`,
		id, strings.Split(email, "@")[0], email, role); err != nil {
		e.t.Fatal(err)
	}
	tok, _ := auth.IssueToken(e.a.Keys, id, time.Hour)
	return id, tok
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.a.DB.Exec(context.Background(), q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) do(method, path, tok string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// ok calls as admin, wants 200 and decodes into out.
func (e *env) ok(method, path string, body, out any) {
	e.t.Helper()
	code, b := e.do(method, path, e.admin, body)
	if code != http.StatusOK {
		e.t.Fatalf("%s %s = %d %s", method, path, code, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

// fails calls as admin and wants status with error code.
func (e *env) fails(status int, code, method, path string, body any) {
	e.t.Helper()
	got, b := e.do(method, path, e.admin, body)
	var errBody struct{ Code string }
	_ = json.Unmarshal(b, &errBody)
	if got != status || errBody.Code != code {
		e.t.Fatalf("%s %s = %d %s, want %d %s", method, path, got, b, status, code)
	}
}

func TestAdminOnly(t *testing.T) {
	e := setup(t)
	routes := [][2]string{
		{"GET", "/api/v1/configs/settings"}, {"POST", "/api/v1/configs/settings"}, {"GET", "/api/v1/configs/extractor/status"},
		{"GET", "/api/v1/configs/tool_servers"}, {"POST", "/api/v1/configs/tool_servers"}, {"DELETE", "/api/v1/configs/tool_servers/x"},
		{"GET", "/api/v1/configs/hook_servers"}, {"POST", "/api/v1/configs/hook_servers"}, {"DELETE", "/api/v1/configs/hook_servers/x"},
		{"GET", "/api/v1/configs/oauth_providers"}, {"POST", "/api/v1/configs/oauth_providers"}, {"DELETE", "/api/v1/configs/oauth_providers/x"},
		{"POST", "/api/v1/configs/tool_servers/test"}, {"POST", "/api/v1/configs/hook_servers/test"}, {"POST", "/api/v1/configs/oauth_providers/test"},
		{"GET", "/api/v1/analytics/summary"}, {"GET", "/api/v1/analytics/daily"}, {"GET", "/api/v1/analytics/models"},
		{"GET", "/api/v1/analytics/users"}, {"GET", "/api/v1/analytics/spend?start_date=2026-01-01&end_date=2026-01-31"},
	}
	for _, rt := range routes {
		if code, b := e.do(rt[0], rt[1], e.user, map[string]any{}); code != http.StatusForbidden {
			t.Errorf("%s %s as user = %d %s, want 403", rt[0], rt[1], code, b)
		}
		if code, _ := e.do(rt[0], rt[1], "", map[string]any{}); code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous = %d, want 401", rt[0], rt[1], code)
		}
	}
}

func findSetting(t *testing.T, list []settingOut, key string) settingOut {
	t.Helper()
	for _, s := range list {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("setting %s not listed", key)
	return settingOut{}
}

func TestSettings(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var list []settingOut
	e.ok("GET", "/api/v1/configs/settings", nil, &list)
	if s := findSetting(t, list, "chat.max_tool_iterations"); string(s.Value) != "30" || string(s.Default) != "30" || s.Set || s.Managed {
		t.Fatalf("default setting = %+v", s)
	}
	// apply-only keys are listed too; secret ones without a value.
	if s := findSetting(t, list, "spend.source.dsn"); !s.Secret || s.Value != nil || s.Set {
		t.Fatalf("secret setting = %+v", s)
	}

	e.fails(400, "unknown_key", "POST", "/api/v1/configs/settings", map[string]any{"nope.key": 1})
	e.fails(400, "bad_type", "POST", "/api/v1/configs/settings", map[string]any{"chat.local_lock": "yes"})
	e.fails(400, "bad_type", "POST", "/api/v1/configs/settings", map[string]any{"site.default_models": "kera"})
	e.fails(400, "bad_type", "POST", "/api/v1/configs/settings", map[string]any{"chat.max_tool_iterations": 2.5})
	e.fails(400, "bad_type", "POST", "/api/v1/configs/settings", map[string]any{"spend.source.dsn": 5})
	// A rejected batch writes nothing.
	e.fails(400, "bad_type", "POST", "/api/v1/configs/settings", map[string]any{"chat.local_lock": true, "rag.top_k": "x"})
	if e.a.Settings.Bool(ctx, "chat.local_lock") {
		t.Fatal("partial batch was written")
	}

	// A key kera apply owns keeps its managed flag after a UI edit.
	e.exec(`INSERT INTO config (key, value, managed) VALUES ('rag.top_k', '10', true)`)
	_ = e.a.Settings.Get(ctx, "rag.top_k", new(int)) // warm the cache: the write must invalidate it
	e.ok("POST", "/api/v1/configs/settings", map[string]any{
		"rag.top_k": 12, "rag.relevance_threshold": 0.25, "chat.local_lock": true,
		"site.default_models": []string{"kera"}, "spend.source.dsn": "postgres://u:hunter2@db/x",
	}, &list)
	if s := findSetting(t, list, "rag.top_k"); string(s.Value) != "12" || !s.Managed || !s.Set {
		t.Fatalf("rag.top_k = %+v", s)
	}
	if s := findSetting(t, list, "rag.relevance_threshold"); string(s.Value) != "0.25" || s.Managed {
		t.Fatalf("rag.relevance_threshold = %+v", s)
	}
	var topK int
	if err := e.a.Settings.Get(ctx, "rag.top_k", &topK); err != nil || topK != 12 {
		t.Fatalf("store sees rag.top_k = %d (%v)", topK, err)
	}
	if !e.a.Settings.Bool(ctx, "chat.local_lock") {
		t.Fatal("chat.local_lock not written")
	}

	// Secrets: stored like apply stores them, never returned.
	s := findSetting(t, list, "spend.source.dsn")
	if !s.Set || s.Value != nil {
		t.Fatalf("secret after write = %+v", s)
	}
	_, b := e.do("GET", "/api/v1/configs/settings", e.admin, nil)
	if bytes.Contains(b, []byte("hunter2")) {
		t.Fatal("secret value returned")
	}
	if dsn, err := apply.ReadSecret(ctx, e.a.Settings, e.a.Keys, "spend.source.dsn"); err != nil || dsn != "postgres://u:hunter2@db/x" {
		t.Fatalf("ReadSecret = %q, %v", dsn, err)
	}

	// ui.brand is checked with apply's brand rules.
	for _, bad := range []map[string]any{
		{"accent": map[string]any{"base": "red}</style>"}},
		{"defaultTheme": "neon"},
		{"splash": map[string]any{"style": "spin"}},
		{"logo": map[string]any{"light": "javascript:alert(1)"}},
		{"nmae": "typo"},
	} {
		e.fails(400, "invalid_brand", "POST", "/api/v1/configs/settings", map[string]any{"ui.brand": bad})
	}
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"ui.brand": map[string]any{
		"name": "Acme", "accent": map[string]any{"base": "#123456", "bright": "oklch(0.7 0.1 200)"},
		"logo": map[string]any{"light": "/brand/logo-1a2b3c4d.png"}, "grey": map[string]any{"hue": 260, "chroma": 0.02},
		"login": map[string]any{"style": "plain"}, "defaultTheme": "dark",
	}}, &list)
	var brand apply.Brand
	if err := e.a.Settings.Get(ctx, "ui.brand", &brand); err != nil || brand.Name != "Acme" || brand.Grey.Hue != 260 {
		t.Fatalf("ui.brand = %+v (%v)", brand, err)
	}

	// null resets to the default; "" clears a secret.
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"rag.top_k": nil, "spend.source.dsn": ""}, &list)
	if s := findSetting(t, list, "rag.top_k"); string(s.Value) != "30" || s.Set {
		t.Fatalf("reset rag.top_k = %+v", s)
	}
	if s := findSetting(t, list, "spend.source.dsn"); s.Set {
		t.Fatalf("cleared secret still set: %+v", s)
	}
}

func TestToolServers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	base := map[string]any{"id": "kb", "kind": "openapi", "name": "KB", "url": "http://kb:8781/"}
	with := func(kv ...any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	e.fails(400, "bad_kind", "POST", "/api/v1/configs/tool_servers", with("kind", "soap"))
	e.fails(400, "bad_url", "POST", "/api/v1/configs/tool_servers", with("url", "ftp://x"))
	e.fails(400, "bad_auth", "POST", "/api/v1/configs/tool_servers", with("auth", map[string]any{"type": "bearer"}))
	e.fails(400, "bad_auth", "POST", "/api/v1/configs/tool_servers", with("auth", map[string]any{"type": "user_oauth", "provider": "ghost"}))

	// A managed row edited in the UI becomes unmanaged.
	e.exec(`INSERT INTO tool_server (id, kind, url, managed, display) VALUES ('kb', 'openapi', 'http://old', true, '{"label":"KB facts"}')`)
	var ts toolServer
	e.ok("POST", "/api/v1/configs/tool_servers", with("auth", map[string]any{"type": "bearer", "key": "tok-123"},
		"name_filter", []string{"search_*"}, "max_result_chars", 4000), &ts)
	if ts.Managed || !ts.Auth.HasKey || ts.Auth.Type != "bearer" || ts.URL != "http://kb:8781" || ts.SpecPath != "openapi.json" ||
		ts.MaxResultChars != 4000 || len(ts.NameFilter) != 1 || !ts.Enabled || !strings.Contains(string(ts.Display), "KB facts") {
		t.Fatalf("saved = %+v display=%s", ts, ts.Display)
	}
	key := func() string {
		var a apply.ToolAuthColumn
		if err := e.a.DB.QueryRow(ctx, `SELECT auth FROM tool_server WHERE id = 'kb'`).Scan(&a); err != nil {
			t.Fatal(err)
		}
		raw, _ := base64.StdEncoding.DecodeString(a.KeyEnc)
		k, err := e.a.Keys.DecryptString(raw)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	if key() != "tok-123" {
		t.Fatal("key not stored like apply stores it")
	}
	// Absent key keeps it; the list never shows it.
	e.ok("POST", "/api/v1/configs/tool_servers", with("auth", map[string]any{"type": "bearer"}, "enabled", false), &ts)
	if key() != "tok-123" || ts.Enabled {
		t.Fatalf("update lost the key or enabled: %+v", ts)
	}
	_, b := e.do("GET", "/api/v1/configs/tool_servers", e.admin, nil)
	if bytes.Contains(b, []byte("tok-123")) || bytes.Contains(b, []byte("key_enc")) {
		t.Fatalf("list leaks the key: %s", b)
	}

	// user_oauth with a known provider; delete drops the grants.
	e.exec(`INSERT INTO oauth_provider (id, client_id) VALUES ('sheets', 'c')`)
	e.ok("POST", "/api/v1/configs/tool_servers", with("kind", "mcp", "auth", map[string]any{"type": "user_oauth", "provider": "sheets", "key": "ignored"}), &ts)
	if ts.Auth.Provider != "sheets" || ts.Auth.HasKey {
		t.Fatalf("oauth server = %+v", ts)
	}
	e.fails(409, "in_use", "DELETE", "/api/v1/configs/oauth_providers/sheets", nil)
	e.exec(`INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission)
		VALUES ('g1', 'tool_server', 'kb', 'user', '*', 'read')`)
	e.ok("DELETE", "/api/v1/configs/tool_servers/kb", nil, nil)
	var n int
	_ = e.a.DB.QueryRow(ctx, `SELECT count(*) FROM access_grant`).Scan(&n)
	if n != 0 {
		t.Fatal("grants survived the delete")
	}
	e.fails(404, "not_found", "DELETE", "/api/v1/configs/tool_servers/kb", nil)
}

func TestHooksAndProviders(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.fails(400, "bad_events", "POST", "/api/v1/configs/hook_servers", map[string]any{"id": "h", "url": "http://h", "events": []string{"stream"}})
	var hk hookServer
	e.ok("POST", "/api/v1/configs/hook_servers", map[string]any{"id": "h", "url": "http://h", "key": "hk-1", "events": []string{"outlet"}, "scope": "global"}, &hk)
	if !hk.HasKey || hk.Scope != "global" || hk.OnError != "skip" || hk.TimeoutMS != 10000 || string(hk.Config) != "{}" {
		t.Fatalf("hook = %+v", hk)
	}
	e.ok("POST", "/api/v1/configs/hook_servers", map[string]any{"id": "h", "url": "http://h2", "scope": "global"}, &hk)
	if !hk.HasKey || hk.URL != "http://h2" {
		t.Fatalf("hook update lost the key: %+v", hk)
	}
	e.ok("POST", "/api/v1/configs/hook_servers", map[string]any{"id": "h", "url": "http://h2", "scope": "global", "key": ""}, &hk)
	if hk.HasKey {
		t.Fatal("empty key did not clear")
	}

	e.fails(400, "bad_client_id", "POST", "/api/v1/configs/oauth_providers", map[string]any{"id": "g", "issuer": "https://accounts.example.com"})
	e.exec(`INSERT INTO oauth_provider (id, client_id, options, managed) VALUES ('google', 'old', '{"login": true, "extra": 1}', true)`)
	var p provider
	e.fails(400, "bad_endpoints", "POST", "/api/v1/configs/oauth_providers", map[string]any{"id": "tool", "client_id": "c"})
	// The Google login row apply writes has no endpoints; editing it must work.
	e.ok("POST", "/api/v1/configs/oauth_providers", map[string]any{"id": "google", "login": true,
		"client_id": "cid", "client_secret": "cs-secret"}, &p)
	if p.Managed || !p.HasSecret || !p.PKCE || p.RedirectURI != "https://chat.example.com/oauth/google/callback" {
		t.Fatalf("provider = %+v", p)
	}
	var opts map[string]any
	var enc []byte
	_ = e.a.DB.QueryRow(ctx, `SELECT options, client_secret_enc FROM oauth_provider WHERE id = 'google'`).Scan(&opts, &enc)
	if sec, _ := e.a.Keys.DecryptString(enc); sec != "cs-secret" || opts["extra"] != float64(1) {
		t.Fatalf("stored secret %q options %v", sec, opts)
	}
	e.ok("POST", "/api/v1/configs/oauth_providers", map[string]any{"id": "mcp:sheets", "authorization_endpoint": "https://a.example.com/auth",
		"token_endpoint": "https://a.example.com/token", "client_id": "c2"}, &p)
	if p.RedirectURI != "https://chat.example.com/oauth/clients/mcp:sheets/callback" || p.HasSecret {
		t.Fatalf("tool provider = %+v", p)
	}
	_, b := e.do("GET", "/api/v1/configs/oauth_providers", e.admin, nil)
	if bytes.Contains(b, []byte("cs-secret")) {
		t.Fatal("secret listed")
	}
	e.ok("DELETE", "/api/v1/configs/oauth_providers/mcp:sheets", nil, nil)
}

func TestAnalytics(t *testing.T) {
	e := setup(t)
	alice, _ := e.mkUser("user", "alice@example.com")
	bob, _ := e.mkUser("user", "bob@example.com")
	e.exec(`INSERT INTO chat (id, user_id) VALUES ('c1', $1), ('c2', $1), ('c3', $2)`, alice, bob)
	msg := func(id, chat, role, model, at, usage string) {
		e.exec(`INSERT INTO message (id, chat_id, role, model_id, created_at, usage) VALUES ($1, $2, $3, $4, $5, $6)`,
			id, chat, role, model, at, usage)
	}
	// Imported history: tokens only in message.usage (OpenAI and Ollama names).
	msg("u1", "c1", "user", "", "2026-03-01T10:00:00Z", `{}`)
	msg("a1", "c1", "assistant", "kera", "2026-03-01T10:00:05Z", `{"prompt_tokens": 100, "completion_tokens": 10}`)
	msg("a2", "c2", "assistant", "local", "2026-03-01T23:59:00Z", `{"prompt_eval_count": 50, "eval_count": 5}`)
	// New reply with a usage row: the row wins over message.usage (no double count).
	msg("a3", "c3", "assistant", "kera", "2026-03-03T08:00:00Z", `{"prompt_tokens": 999}`)
	e.exec(`INSERT INTO usage (user_id, model_id, chat_id, message_id, prompt_tokens, completion_tokens, created_at)
		VALUES ($1, 'kera', 'c3', 'a3', 200, 20, '2026-03-03T08:00:01Z'), ($1, 'kera', 'c3', 'a3', 300, 30, '2026-03-03T08:00:02Z')`, bob)
	// API call without a stored message.
	e.exec(`INSERT INTO usage (user_id, model_id, prompt_tokens, completion_tokens, created_at)
		VALUES ($1, 'local', 7, 3, '2026-03-02T12:00:00Z')`, alice)
	// Outside the range.
	msg("a4", "c1", "assistant", "kera", "2026-02-28T23:59:59Z", `{"prompt_tokens": 1}`)
	msg("a5", "c1", "assistant", "kera", "2026-03-04T00:00:00Z", `{"prompt_tokens": 1}`)

	q := "?start_date=2026-03-01&end_date=2026-03-03"
	var s summary
	e.ok("GET", "/api/v1/analytics/summary"+q, nil, &s)
	want := summary{StartDate: "2026-03-01", EndDate: "2026-03-03", UsersTotal: 4, ActiveUsers: 2, Messages: 4, Chats: 3, Models: 2,
		PromptTokens: 100 + 50 + 500 + 7, CompletionTokens: 10 + 5 + 50 + 3}
	if s != want {
		t.Fatalf("summary = %+v\nwant      %+v", s, want)
	}
	var days []dayRow
	e.ok("GET", "/api/v1/analytics/daily"+q, nil, &days)
	wantDays := []dayRow{{"2026-03-01", 2, 1, 150, 15}, {"2026-03-02", 1, 1, 7, 3}, {"2026-03-03", 1, 1, 500, 50}}
	if len(days) != 3 || days[0] != wantDays[0] || days[1] != wantDays[1] || days[2] != wantDays[2] {
		t.Fatalf("daily = %+v", days)
	}
	var models []modelRow
	e.ok("GET", "/api/v1/analytics/models"+q, nil, &models)
	if len(models) != 2 || models[0] != (modelRow{"kera", 2, 2, 2, 600, 60}) || models[1] != (modelRow{"local", 2, 1, 1, 57, 8}) {
		t.Fatalf("models = %+v", models)
	}
	var users []userRow
	e.ok("GET", "/api/v1/analytics/users"+q, nil, &users)
	if len(users) != 2 || users[0].Email != "alice@example.com" || users[0].Messages != 3 || users[0].Chats != 2 ||
		users[1].Email != "bob@example.com" || users[1].PromptTokens != 500 {
		t.Fatalf("users = %+v", users)
	}
	e.fails(400, "bad_range", "GET", "/api/v1/analytics/summary?start_date=2026-03-05&end_date=2026-03-01", nil)
	e.fails(400, "bad_range", "GET", "/api/v1/analytics/daily?start_date=2025-01-01&end_date=2026-03-01", nil)
	e.ok("GET", "/api/v1/analytics/daily", nil, &days) // default: last 30 days
	if len(days) != 30 {
		t.Fatalf("default range has %d days", len(days))
	}
}

func TestSpend(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := "/api/v1/analytics/spend?start_date=2026-03-01&end_date=2026-03-31"
	var none struct {
		Configured *bool `json:"configured"`
		Rows       []spendRow
		Users      []spendUser
	}
	e.ok("GET", q, nil, &none) // no source: an empty state (200), not an error that reads as an outage
	if none.Configured == nil || *none.Configured || none.Rows == nil || none.Users == nil {
		t.Fatalf("unconfigured spend = %+v", none)
	}
	e.fails(400, "bad_range", "GET", "/api/v1/analytics/spend", nil)

	// A second scratch database stands in for LiteLLM's.
	lite := testdb.New(t)
	if _, err := lite.Exec(ctx, `CREATE TABLE "LiteLLM_SpendLogs" (request_id text PRIMARY KEY, spend float8 NOT NULL DEFAULT 0,
		prompt_tokens int NOT NULL DEFAULT 0, completion_tokens int NOT NULL DEFAULT 0, "startTime" timestamp(3) NOT NULL,
		model text NOT NULL DEFAULT '', end_user text);
		INSERT INTO "LiteLLM_SpendLogs" VALUES
		('r1', 1.5, 100, 10, '2026-03-02 09:00', 'gpt', 'boss@example.com'),
		('r2', 0.25, 50, 5, '2026-03-02 23:59:59.999', 'gpt', 'boss@example.com'),
		('r3', 0.01, 30, 0, '2026-03-31 12:00', 'embed', NULL),
		('r4', 9, 1, 1, '2026-04-01 00:00', 'gpt', 'boss@example.com'),
		('r5', 9, 1, 1, '2026-02-28 23:59:59', 'gpt', 'boss@example.com')`); err != nil {
		t.Fatal(err)
	}
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"spend.source.kind": "litellm_postgres",
		"spend.source.dsn": lite.Config().ConnString()}, nil)

	e.ok("POST", "/api/v1/configs/settings", map[string]any{"spend.source.dsn": "postgres://x:y@127.0.0.1:1/z?connect_timeout=2"}, nil)
	e.fails(424, "spend_source_failed", "GET", q, nil) // unreachable source: not a 502/503 outage
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"spend.source.dsn": lite.Config().ConnString()}, nil)

	var out struct {
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Rows      []spendRow
		Users     []spendUser
	}
	e.ok("GET", q, nil, &out)
	if out.StartDate != "2026-03-01" || out.EndDate != "2026-03-31" || len(out.Users) != 2 || out.Users[0].Email != "boss@example.com" ||
		out.Users[0].LastActiveAt == 0 {
		t.Fatalf("spend = %+v", out)
	}
	got := map[string]spendRow{}
	for _, r := range out.Rows {
		got[r.EndUser+"|"+r.Model+"|"+r.Day] = r
	}
	if len(got) != 2 || got["boss@example.com|gpt|2026-03-02"] != (spendRow{"boss@example.com", "gpt", "2026-03-02", 1.75, 150, 15, 2}) ||
		got["|embed|2026-03-31"] != (spendRow{"", "embed", "2026-03-31", 0.01, 30, 0, 1}) {
		t.Fatalf("rows = %+v", out.Rows)
	}
	// The pool is read-only.
	p, err := (&handler{a: e.a}).spend.get(ctx, lite.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Exec(ctx, `DELETE FROM "LiteLLM_SpendLogs"`); err == nil {
		t.Fatal("spend pool can write")
	}
}

// auth.password.enabled has no value until an admin sets one (then the sign-in
// form follows the OAuth providers); it takes true or false and nothing else.
func TestPasswordSwitchSetting(t *testing.T) {
	e := setup(t)
	var list []settingOut
	e.ok("GET", "/api/v1/configs/settings", nil, &list)
	if s := findSetting(t, list, "auth.password.enabled"); string(s.Value) != "null" || string(s.Default) != "null" || s.Set {
		t.Fatalf("unset switch = %+v", s)
	}
	for _, bad := range []any{"yes", 1, []bool{true}, map[string]any{}} {
		e.fails(400, "bad_type", "POST", "/api/v1/configs/settings", map[string]any{"auth.password.enabled": bad})
	}
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"auth.password.enabled": false}, &list)
	if s := findSetting(t, list, "auth.password.enabled"); string(s.Value) != "false" || !s.Set {
		t.Fatalf("switch off = %+v", s)
	}
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"auth.password.enabled": true}, &list)
	if s := findSetting(t, list, "auth.password.enabled"); string(s.Value) != "true" {
		t.Fatalf("switch on = %+v", s)
	}
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"auth.password.enabled": nil}, &list)
	if s := findSetting(t, list, "auth.password.enabled"); string(s.Value) != "null" || s.Set {
		t.Fatalf("switch reset = %+v", s)
	}
}

// Saving an OAuth login provider pins password sign-in off while the setting has no value, so
// removing the provider later does not switch the password form on by itself; a tool-server
// provider (no login) and a value an admin set are left alone.
func TestSavingALoginProviderPinsPasswordSignInOff(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	value := func() string {
		var v string
		err := e.a.DB.QueryRow(ctx, `SELECT value::text FROM config WHERE key = 'auth.password.enabled'`).Scan(&v)
		if err != nil {
			return "(unset)"
		}
		return v
	}
	provider := func(id string, login bool) map[string]any {
		return map[string]any{"id": id, "login": login, "client_id": "c", "client_secret": "s",
			"authorization_endpoint": "https://idp.example.com/auth", "token_endpoint": "https://idp.example.com/token"}
	}

	e.ok("POST", "/api/v1/configs/oauth_providers", provider("mcp:sheets", false), nil)
	if v := value(); v != "(unset)" {
		t.Fatalf("a tool-server provider changed the setting: %s", v)
	}
	e.ok("POST", "/api/v1/configs/oauth_providers", provider("sso", true), nil)
	if v := value(); v != "false" {
		t.Fatalf("after saving a login provider: %s, want false", v)
	}
	e.ok("DELETE", "/api/v1/configs/oauth_providers/sso", nil, nil)
	if v := value(); v != "false" {
		t.Fatalf("after deleting the provider: %s, the form must stay off", v)
	}

	// An admin's explicit choice survives saving a provider.
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"auth.password.enabled": true}, nil)
	e.ok("POST", "/api/v1/configs/oauth_providers", provider("sso", true), nil)
	if v := value(); v != "true" {
		t.Fatalf("saving a provider overwrote an explicit true: %s", v)
	}
	// Reset to automatic, then a provider pins again.
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"auth.password.enabled": nil}, nil)
	e.ok("POST", "/api/v1/configs/oauth_providers", provider("sso", true), nil)
	if v := value(); v != "false" {
		t.Fatalf("after reset and save: %s", v)
	}
}
