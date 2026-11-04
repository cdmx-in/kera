package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// result is probe.Result as the console reads it.
type result struct {
	OK     bool     `json:"ok"`
	MS     int64    `json:"ms"`
	Error  string   `json:"error"`
	Note   string   `json:"note"`
	Via    string   `json:"via"`
	Status int      `json:"status"`
	Tools  []string `json:"tools"`
	Checks []struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	} `json:"checks"`
}

func (e *env) probe(path string, body any) result {
	e.t.Helper()
	var r result
	e.ok("POST", path, body, &r)
	return r
}

// fieldErr wants a 400 naming field.
func (e *env) fieldErr(field, path string, body any) {
	e.t.Helper()
	code, b := e.do("POST", path, e.admin, body)
	var got struct{ Field string }
	_ = json.Unmarshal(b, &got)
	if code != http.StatusBadRequest || got.Field != field {
		e.t.Errorf("POST %s %v = %d %s, want 400 on %s", path, body, code, b, field)
	}
}

const openAPISpec = `{"openapi":"3.1.0","paths":{
	"/search":{"post":{"operationId":"search_kb","requestBody":{"content":{"application/json":{"schema":{"type":"object"}}}}}},
	"/item/{id}":{"get":{"operationId":"get_item","parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}]}}}}`

// fakeMCP answers initialize, the initialized notification and tools/list.
func fakeMCP(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		var res string
		switch m.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			res = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}`
		case "tools/list":
			res = `{"tools":[{"name":"echo","inputSchema":{"type":"object"}},{"name":"sum","inputSchema":{"type":"object"}}]}`
		default:
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"result":`+res+`}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestToolServerDraftTest(t *testing.T) {
	e := setup(t)
	const path = "/api/v1/configs/tool_servers/test"
	spec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tool-key" {
			http.Error(w, "echo "+r.Header.Get("Authorization"), http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/openapi.json" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, openAPISpec)
	}))
	defer spec.Close()
	draft := map[string]any{"id": "kb", "kind": "openapi", "url": spec.URL + "/api", "spec_path": "openapi.json",
		"auth": map[string]any{"type": "bearer", "key": "tool-key"}}
	if r := e.probe(path, draft); !r.OK || strings.Join(r.Tools, ",") != "get_item,search_kb" {
		t.Fatalf("openapi = %+v", r)
	}
	// saved key is used when none is typed; a wrong typed key is masked in the error
	e.ok("POST", "/api/v1/configs/tool_servers", draft, nil)
	draft["auth"] = map[string]any{"type": "bearer"}
	draft["disabled_functions"] = []string{"get_item"}
	if r := e.probe(path, draft); !r.OK || strings.Join(r.Tools, ",") != "search_kb" {
		t.Fatalf("stored key = %+v", r)
	}
	draft["auth"] = map[string]any{"type": "bearer", "key": "wrong-key"}
	if r := e.probe(path, draft); r.OK || !strings.Contains(r.Error, "401") || strings.Contains(r.Error, "wrong-key") {
		t.Fatalf("wrong key = %+v", r)
	}

	mcp := fakeMCP(t)
	if r := e.probe(path, map[string]any{"kind": "mcp", "url": mcp.URL}); !r.OK || strings.Join(r.Tools, ",") != "echo,sum" {
		t.Fatalf("mcp = %+v", r)
	}
	// user_oauth before the admin connected: reachability only
	e.exec(`INSERT INTO oauth_provider (id, client_id, authorization_endpoint, token_endpoint) VALUES ('p', 'c', 'https://a/x', 'https://a/y')`)
	if r := e.probe(path, map[string]any{"kind": "mcp", "url": mcp.URL, "auth": map[string]any{"type": "user_oauth", "provider": "p"}}); !r.OK ||
		!strings.Contains(r.Note, "Connect") || len(r.Tools) != 0 {
		t.Fatalf("user_oauth = %+v", r)
	}
	if r := e.probe(path, map[string]any{"kind": "mcp", "url": "http://169.254.169.254/mcp"}); r.OK || !strings.Contains(r.Error, "not allowed") {
		t.Fatalf("metadata = %+v", r)
	}
	var n int
	_ = e.a.DB.QueryRow(t.Context(), `SELECT count(*) FROM tool_server`).Scan(&n)
	if n != 1 {
		t.Fatalf("tests saved rows: %d", n)
	}

	// field errors (test and save share them)
	base := func(kv ...any) map[string]any {
		m := map[string]any{"id": "new", "kind": "openapi", "url": "http://x"}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	for _, p := range []string{path, "/api/v1/configs/tool_servers"} {
		e.fieldErr("url", p, base("url", "http://x "))
		e.fieldErr("spec_path", p, base("spec_path", "https://elsewhere/openapi.json"))
		e.fieldErr("max_result_chars", p, base("max_result_chars", -1))
		e.fieldErr("max_result_chars", p, base("max_result_chars", "many"))
		e.fieldErr("auth.header", p, base("auth", map[string]any{"type": "header", "header": "X Key", "key": "k"}))
		e.fieldErr("auth.provider", p, base("auth", map[string]any{"type": "user_oauth"}))
		e.fieldErr("auth.key", p, base("id", "new2", "auth", map[string]any{"type": "bearer"}))
	}
	e.fieldErr("id", "/api/v1/configs/tool_servers", base("id", "New Server"))
	e.fieldErr("auth.provider", "/api/v1/configs/tool_servers", base("auth", map[string]any{"type": "user_oauth", "provider": "ghost"}))
}

func TestHookDraftTest(t *testing.T) {
	e := setup(t)
	const path = "/api/v1/configs/hook_servers/test"
	var mu sync.Mutex
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		mu.Unlock()
		if r.URL.Path == "/hooks/inlet" && strings.Contains(string(b), `"test":true`) {
			io.WriteString(w, `{}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer hook.Close()
	e.ok("POST", "/api/v1/configs/hook_servers", map[string]any{"id": "h", "url": hook.URL + "/hooks", "key": "hk-1", "scope": "global"}, nil)
	r := e.probe(path, map[string]any{"id": "h", "url": hook.URL + "/hooks", "scope": "global"})
	if !r.OK || r.Status != 200 || !strings.Contains(r.Via, "/inlet") {
		t.Fatalf("hook = %+v", r)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "GET /hooks/health Bearer hk-1") || !strings.HasPrefix(got[1], "POST /hooks/inlet Bearer hk-1") {
		t.Fatalf("calls = %q", got)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.Error(w, "no", 500)
		}
	}))
	defer health.Close()
	if r := e.probe(path, map[string]any{"url": health.URL, "scope": "global"}); !r.OK || r.Via != "GET /health" {
		t.Fatalf("health = %+v", r)
	}
	if r := e.probe(path, map[string]any{"url": hook.URL + "/other", "scope": "global", "events": []string{"outlet"}}); r.OK || r.Status != 404 {
		t.Fatalf("missing = %+v", r)
	}
	if r := e.probe(path, map[string]any{"url": "http://169.254.169.254", "scope": "global"}); r.OK || !strings.Contains(r.Error, "not allowed") {
		t.Fatalf("metadata = %+v", r)
	}
	for _, p := range []string{path, "/api/v1/configs/hook_servers"} {
		ok := map[string]any{"id": "h2", "url": "http://h", "scope": "global"}
		with := func(k string, v any) map[string]any {
			m := map[string]any{}
			for a, b := range ok {
				m[a] = b
			}
			m[k] = v
			return m
		}
		e.fieldErr("events", p, with("events", []string{}))
		e.fieldErr("events", p, with("events", []string{"stream"}))
		e.fieldErr("priority", p, with("priority", 1.5))
		e.fieldErr("timeout_ms", p, with("timeout_ms", 500))
		e.fieldErr("timeout_ms", p, with("timeout_ms", 120001))
		e.fieldErr("model_ids", p, with("scope", "models"))
		e.fieldErr("config", p, with("config", []int{1}))
	}
}

func TestProviderDraftTest(t *testing.T) {
	e := setup(t)
	const path = "/api/v1/configs/oauth_providers/test"
	var mu sync.Mutex
	var seen []string
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.URL.String()+" "+string(b)+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			io.WriteString(w, `{"issuer":"`+idp.URL+`","authorization_endpoint":"`+idp.URL+`/auth","token_endpoint":"`+idp.URL+`/token"}`)
		case "/auth":
			http.Error(w, "missing client_id", 400)
		default:
			http.Error(w, "method", 405)
		}
	}))
	defer idp.Close()
	e.ok("POST", "/api/v1/configs/oauth_providers", map[string]any{"id": "idp", "client_id": "c", "client_secret": "cs-secret",
		"authorization_endpoint": idp.URL + "/auth", "token_endpoint": idp.URL + "/token"}, nil)
	draft := map[string]any{"id": "idp", "client_id": "c", "issuer": idp.URL, "authorization_endpoint": idp.URL + "/auth", "token_endpoint": idp.URL + "/token"}
	r := e.probe(path, draft)
	if !r.OK || len(r.Checks) != 4 {
		t.Fatalf("idp = %+v", r)
	}
	draft["token_endpoint"] = idp.URL + "/oauth/token"
	r = e.probe(path, draft)
	if r.OK || r.Checks[1].OK || !strings.Contains(r.Checks[1].Detail, "/token") || !r.Checks[3].OK {
		t.Fatalf("mismatch = %+v", r)
	}
	for _, s := range seen {
		if strings.Contains(s, "cs-secret") {
			t.Fatalf("secret sent: %s", s)
		}
	}
	// no issuer: only the endpoints; a dead endpoint fails its check
	r = e.probe(path, map[string]any{"client_id": "c", "authorization_endpoint": idp.URL + "/auth", "token_endpoint": "http://127.0.0.1:1/token"})
	if r.OK || len(r.Checks) != 2 || !r.Checks[0].OK || r.Checks[1].OK {
		t.Fatalf("dead token endpoint = %+v", r)
	}
	for _, p := range []string{path, "/api/v1/configs/oauth_providers"} {
		e.fieldErr("client_id", p, map[string]any{"id": "x", "authorization_endpoint": "https://a/b", "token_endpoint": "https://a/c"})
		e.fieldErr("token_endpoint", p, map[string]any{"id": "x", "client_id": "c", "authorization_endpoint": "https://a/b"})
		e.fieldErr("token_endpoint", p, map[string]any{"id": "x", "client_id": "c", "login": true, "authorization_endpoint": "https://a/b"})
		e.fieldErr("scope", p, map[string]any{"id": "x", "client_id": "c", "login": true, "scope": "openid,email"})
		e.fieldErr("scope", p, map[string]any{"id": "x", "client_id": "c", "login": true, "scope": "profile"})
		e.fieldErr("issuer", p, map[string]any{"id": "x", "client_id": "c", "login": true, "issuer": "accounts.example.com"})
	}
	e.fieldErr("id", "/api/v1/configs/oauth_providers", map[string]any{"id": "Google Login", "client_id": "c", "login": true})
}
