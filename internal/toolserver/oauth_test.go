package toolserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/oauthclient"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
	"github.com/cdmx-in/kera/internal/tools"
)

// fakeAS is an OAuth authorization server: code exchange with PKCE, refresh, RFC 7009 revoke.
type fakeAS struct {
	*httptest.Server
	redirect string

	mu           sync.Mutex
	challenges   map[string]string
	refreshes    int
	refreshFail  bool
	revokeStatus int
	revoked      []string
}

func newFakeAS(t *testing.T) *fakeAS {
	f := &fakeAS{challenges: map[string]string{}, revokeStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, sec, ok := r.BasicAuth()
		if !ok {
			id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		if id != "cid" || sec != "csecret" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			ch, known := f.challenges[r.PostForm.Get("code")]
			if !known || base64.RawURLEncoding.EncodeToString(sum[:]) != ch || r.PostForm.Get("redirect_uri") != f.redirect {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			// expires inside the 5-minute window, so the first use refreshes
			_, _ = io.WriteString(w, `{"access_token":"at1","refresh_token":"rt1","token_type":"Bearer","expires_in":60,"scope":"sheets"}`)
		case "refresh_token":
			f.refreshes++
			if f.refreshFail || r.PostForm.Get("refresh_token") != "rt1" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"at2","token_type":"Bearer","expires_in":3600}`)
		}
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.revoked = append(f.revoked, r.PostForm.Get("token"))
		w.WriteHeader(f.revokeStatus)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

type env struct {
	t   *testing.T
	a   *app.App
	srv *httptest.Server
	m   *Manager
	as  *fakeAS
	mcp *fakeMCP
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	keys, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	var root http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { root.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	cfg := config.Env{PublicURL: srv.URL}
	s, err := api.New(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(pool)
	a := &app.App{Env: cfg, DB: pool, Keys: keys, Settings: st, Server: s, Auth: auth.New(pool, keys, st, false)}
	if err := oauthclient.Mount(a); err != nil {
		t.Fatal(err)
	}
	m := NewManager(pool, keys, st, files.New(pool, st, t.TempDir()), oauthclient.For(a))
	h := &handler{m: m, all: m.Providers}
	s.Handle("GET /api/v1/tools/{$}", a.Auth.Required(http.HandlerFunc(h.list)))
	root = s.Handler()
	e := &env{t: t, a: a, srv: srv, m: m, as: newFakeAS(t), mcp: newFakeMCP(t)}
	e.as.redirect = srv.URL + "/oauth/clients/mcp:fake/callback"
	sec, _ := keys.EncryptString("csecret")
	e.exec(`INSERT INTO "user" (id, name, email, role) VALUES ('u1', 'Ana', 'ana@example.com', 'user'),
		('u2', 'Bo', 'bo@example.com', 'user'), ('adm', 'Root', 'root@example.com', 'admin')`)
	e.exec(`INSERT INTO oauth_provider (id, authorization_endpoint, token_endpoint, revocation_endpoint, client_id, client_secret_enc, scope)
		VALUES ('mcp:fake', $1, $2, $3, 'cid', $4, 'openid sheets')`, e.as.URL+"/authorize", e.as.URL+"/token", e.as.URL+"/revoke", sec)
	e.exec(`INSERT INTO tool_server (id, kind, name, url, auth, disabled_functions) VALUES
		('mcp:fake', 'mcp', 'Fake Sheets', $1, '{"type":"user_oauth","provider":"mcp:fake"}', '{hidden}')`, e.mcp.URL)
	e.exec(`INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission)
		VALUES ('g1', 'tool_server', 'mcp:fake', 'user', '*', 'read')`)
	_ = ctx
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.a.DB.Exec(context.Background(), q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) user(id string) *auth.User {
	u, err := e.a.Auth.LoadUser(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

// browser returns a client signed in as userID that does not follow redirects.
func (e *env) browser(userID string) *http.Client {
	jar, _ := cookiejar.New(nil)
	tok, err := auth.IssueToken(e.a.Keys, userID, time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	u, _ := url.Parse(e.srv.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: auth.CookieName, Value: tok, Path: "/"}})
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *env) get(c *http.Client, path string) *http.Response {
	e.t.Helper()
	resp, err := c.Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// connect runs authorize + callback for the browser and returns the callback response.
func (e *env) connect(c *http.Client, code string) *http.Response {
	e.t.Helper()
	resp := e.get(c, "/oauth/clients/mcp:fake/authorize?next="+url.QueryEscape("/?tools=mcp:fake"))
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		e.t.Fatalf("authorize: %d %v", resp.StatusCode, err)
	}
	q := loc.Query()
	e.as.mu.Lock()
	e.as.challenges[code] = q.Get("code_challenge")
	e.as.mu.Unlock()
	return e.get(c, "/oauth/clients/mcp:fake/callback?code="+code+"&state="+url.QueryEscape(q.Get("state")))
}

func (e *env) server(id string) *Server {
	e.t.Helper()
	e.m.mu.Lock()
	e.m.loadedAt = time.Time{} // re-read rows now
	e.m.mu.Unlock()
	ss, err := e.m.Servers(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, s := range ss {
		if s.ID() == id {
			return s
		}
	}
	e.t.Fatalf("no server %s", id)
	return nil
}

func (e *env) list(userID string) map[string]toolEntry {
	e.t.Helper()
	tok, _ := auth.IssueToken(e.a.Keys, userID, time.Hour)
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/tools/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		e.t.Fatalf("list: %v %v", resp, err)
	}
	defer resp.Body.Close()
	var list []toolEntry
	_ = json.NewDecoder(resp.Body).Decode(&list)
	out := map[string]toolEntry{}
	for _, x := range list {
		out[x.ID] = x
	}
	return out
}

func (e *env) disconnect(userID string) int {
	tok, _ := auth.IssueToken(e.a.Keys, userID, time.Hour)
	req, _ := http.NewRequest(http.MethodDelete, e.srv.URL+"/api/v1/oauth/clients/mcp:fake/session", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (e *env) tokenRows(userID string) int {
	var n int
	_ = e.a.DB.QueryRow(context.Background(), `SELECT count(*) FROM oauth_token WHERE user_id = $1`, userID).Scan(&n)
	return n
}

// connect -> tools appear -> call with the user's bearer -> refresh -> disconnect revokes.
func TestUserOAuthLifecycle(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.mcp.setToken("at2")
	ana := e.user("u1")
	s := e.server("mcp:fake")

	if specs, err := s.Tools(ctx, ana); err != nil || specs != nil {
		t.Fatalf("before connect: %v %v", specs, err)
	}
	if got := e.list("u1")["mcp:fake"]; got.Authenticated || got.AuthType != "user_oauth" || got.Provider != "mcp:fake" || len(got.Functions) != 0 {
		t.Fatalf("list before connect %+v", got)
	}
	res, _ := s.Execute(ctx, tools.Call{Name: "echo", User: ana, Emit: func(tools.Event) {}})
	if !res.IsError || !strings.Contains(res.Content, "connect Fake Sheets first") {
		t.Fatalf("execute before connect %+v", res)
	}

	// authorize redirect
	b := e.browser("u1")
	resp := e.get(b, "/oauth/clients/mcp:fake/authorize?next="+url.QueryEscape("/?tools=mcp:fake"))
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	if loc.Path != "/authorize" || q.Get("client_id") != "cid" || q.Get("redirect_uri") != e.as.redirect || q.Get("scope") != "openid sheets" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("response_type") != "code" ||
		q.Get("access_type") != "" {
		t.Fatalf("authorize URL %s", loc)
	}

	resp = e.connect(b, "code-1")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/?tools=mcp:fake" {
		t.Fatalf("callback %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if e.tokenRows("u1") != 1 {
		t.Fatal("token not stored")
	}
	var enc []byte
	_ = e.a.DB.QueryRow(ctx, `SELECT token_enc FROM oauth_token WHERE user_id = 'u1'`).Scan(&enc)
	if strings.Contains(string(enc), "at1") || strings.Contains(string(enc), "rt1") {
		t.Fatal("token stored in clear text")
	}

	// tools appear (the first use refreshes: at1 expires within 5 minutes)
	specs, err := s.Tools(ctx, ana)
	if err != nil || len(specs) != 4 {
		t.Fatalf("specs %v err %v", specs, err)
	}
	res, err = s.Execute(ctx, tools.Call{ID: "c1", Name: "echo", Args: json.RawMessage(`{"text":"hello"}`), User: ana, Emit: func(tools.Event) {}})
	if err != nil || res.IsError || res.Content != "hello" {
		t.Fatalf("execute %+v %v", res, err)
	}
	if _, _, h, _ := e.mcp.stats(); h.Get("Authorization") != "Bearer at2" {
		t.Fatalf("bearer %q", h.Get("Authorization"))
	}
	if e.as.refreshes != 1 {
		t.Fatalf("refreshes %d", e.as.refreshes)
	}
	res, _ = s.Execute(ctx, tools.Call{Name: "hidden", User: ana, Emit: func(tools.Event) {}})
	if !res.IsError || !strings.Contains(res.Content, "unknown tool") {
		t.Fatalf("disabled function callable: %+v", res)
	}
	got := e.list("u1")["mcp:fake"]
	if !got.Authenticated || got.ConnectedAt == 0 || strings.Join(got.Functions, ",") != "echo,ask,pic,fail" || got.Name != "Fake Sheets" {
		t.Fatalf("list after connect %+v", got)
	}
	if e.list("u2")["mcp:fake"].Authenticated {
		t.Fatal("another user sees the grant")
	}

	// images come back as files of the user
	res, _ = s.Execute(ctx, tools.Call{Name: "pic", User: ana, Emit: func(tools.Event) {}})
	if len(res.Files) != 1 {
		t.Fatalf("image not saved: %+v", res)
	}
	var owner string
	_ = e.a.DB.QueryRow(ctx, `SELECT user_id FROM file WHERE id = $1`, res.Files[0]).Scan(&owner)
	if owner != "u1" {
		t.Fatalf("file owner %q", owner)
	}

	// disconnect: a provider 5xx keeps the grant (409), success deletes it
	e.as.revokeStatus = http.StatusServiceUnavailable
	if code := e.disconnect("u1"); code != http.StatusConflict || e.tokenRows("u1") != 1 {
		t.Fatalf("revoke failure: %d rows %d", code, e.tokenRows("u1"))
	}
	e.as.revokeStatus = http.StatusOK
	if code := e.disconnect("u1"); code != http.StatusOK || e.tokenRows("u1") != 0 {
		t.Fatalf("disconnect: %d rows %d", code, e.tokenRows("u1"))
	}
	if strings.Join(e.as.revoked, ",") != "rt1,rt1" {
		t.Fatalf("revoked %v", e.as.revoked)
	}
	if specs, _ := s.Tools(ctx, ana); specs != nil {
		t.Fatal("tools still listed after disconnect (cache not dropped)")
	}
	if code := e.disconnect("u1"); code != http.StatusOK {
		t.Fatalf("second disconnect %d", code)
	}
}

func TestRefreshFailureDropsGrant(t *testing.T) {
	e := setup(t)
	e.as.refreshFail = true
	e.connect(e.browser("u1"), "code-2")
	if e.tokenRows("u1") != 1 {
		t.Fatal("not connected")
	}
	_, err := oauthclient.For(e.a).AccessToken(context.Background(), "u1", "mcp:fake")
	if !errors.Is(err, oauthclient.ErrNotConnected) || e.tokenRows("u1") != 0 {
		t.Fatalf("err %v rows %d", err, e.tokenRows("u1"))
	}
}

func TestCallbackChecks(t *testing.T) {
	e := setup(t)
	// not signed in -> sign-in page, coming back to authorize
	resp := e.get(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		"/oauth/clients/mcp:fake/authorize")
	if !strings.HasPrefix(resp.Header.Get("Location"), "/auth?redirect=%2Foauth%2Fclients%2Fmcp%3Afake%2Fauthorize") {
		t.Fatalf("anonymous authorize -> %s", resp.Header.Get("Location"))
	}
	// default return path and a state mismatch
	b := e.browser("u1")
	resp = e.get(b, "/oauth/clients/mcp:fake/authorize?next=https://evil.example/")
	if resp.StatusCode != http.StatusFound {
		t.Fatal(resp.StatusCode)
	}
	resp = e.get(b, "/oauth/clients/mcp:fake/callback?code=x&state=wrong")
	if loc := resp.Header.Get("Location"); loc != "/?oauth_error=invalid_state" || e.tokenRows("u1") != 0 {
		t.Fatalf("bad state -> %s", loc)
	}
	// the flow started by u1 cannot finish in u2's session
	resp = e.get(b, "/oauth/clients/mcp:fake/authorize")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	e.as.challenges["c3"] = loc.Query().Get("code_challenge")
	var flowCookie []*http.Cookie
	for _, c := range b.Jar.Cookies(mustURL(e.srv.URL + "/oauth/clients/")) {
		if c.Name != auth.CookieName {
			c.Path = "/oauth/clients/"
			flowCookie = append(flowCookie, c)
		}
	}
	other := e.browser("u2")
	other.Jar.SetCookies(mustURL(e.srv.URL+"/oauth/clients/"), flowCookie)
	resp = e.get(other, "/oauth/clients/mcp:fake/callback?code=c3&state="+url.QueryEscape(loc.Query().Get("state")))
	if !strings.Contains(resp.Header.Get("Location"), "oauth_error=session_mismatch") || e.tokenRows("u2")+e.tokenRows("u1") != 0 {
		t.Fatalf("mismatch -> %s", resp.Header.Get("Location"))
	}
	// and the right user completes it
	resp = e.connect(b, "c4")
	if resp.Header.Get("Location") != "/?tools=mcp:fake" {
		t.Fatalf("default next -> %s", resp.Header.Get("Location"))
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func TestGoogleAuthorizeURL(t *testing.T) {
	e := setup(t)
	e.exec(`INSERT INTO oauth_provider (id, authorization_endpoint, token_endpoint, revocation_endpoint, client_id, scope)
		VALUES ('mcp:google-sheets', 'https://accounts.google.com/o/oauth2/v2/auth', 'https://oauth2.googleapis.com/token',
		'https://oauth2.googleapis.com/revoke', 'gid.apps.googleusercontent.com',
		'openid https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/spreadsheets')`)
	resp := e.get(e.browser("u1"), "/oauth/clients/mcp:google-sheets/authorize?next=%2F%3Ftools%3Dmcp%3Agoogle-sheets")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	want := map[string]string{"redirect_uri": e.srv.URL + "/oauth/clients/mcp:google-sheets/callback", "access_type": "offline",
		"prompt": "consent", "code_challenge_method": "S256", "client_id": "gid.apps.googleusercontent.com",
		"scope": "openid https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/spreadsheets"}
	if loc.Host != "accounts.google.com" {
		t.Fatalf("host %s", loc.Host)
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if q.Get("resource") != "" {
		t.Error("resource sent although send_resource is off")
	}
}

func TestAccessAndCache(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.exec(`INSERT INTO "group" (id, name) VALUES ('grp', 'R&D')`)
	e.exec(`INSERT INTO tool_server (id, kind, name, url) VALUES ('search', 'mcp', 'Search', $1)`, e.mcp.URL)
	e.exec(`INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission)
		VALUES ('g2', 'tool_server', 'search', 'group', 'grp', 'read')`)
	s := e.server("search")
	if specs, _ := s.Tools(ctx, e.user("u1")); specs != nil {
		t.Fatal("user outside the group sees the tools")
	}
	if _, ok := e.list("u1")["search"]; ok {
		t.Fatal("listed for a user without access")
	}
	res, _ := s.Execute(ctx, tools.Call{Name: "echo", User: e.user("u1"), Emit: func(tools.Event) {}})
	if !res.IsError {
		t.Fatal("call allowed without access")
	}
	if got := e.list("adm")["search"]; !got.Authenticated || len(got.Functions) != 5 || got.Kind != "mcp" {
		t.Fatalf("admin list %+v", got)
	}
	e.exec(`INSERT INTO group_member (group_id, user_id) VALUES ('grp', 'u1')`)
	ana := e.user("u1")
	if specs, _ := s.Tools(ctx, ana); len(specs) != 5 {
		t.Fatalf("member sees %d tools", len(specs))
	}
	_, lists, _, _ := e.mcp.stats()
	for range 3 {
		_, _ = s.Tools(ctx, ana)
	}
	if _, again, _, _ := e.mcp.stats(); again != lists {
		t.Fatalf("tools/list not cached: %d -> %d", lists, again)
	}
	// a changed row gets a fresh server (and a fresh cache)
	e.exec(`UPDATE tool_server SET disabled_functions = '{fail}' WHERE id = 'search'`)
	if specs, _ := e.server("search").Tools(ctx, ana); len(specs) != 4 {
		t.Fatalf("row change not picked up: %d", len(specs))
	}
	e.exec(`UPDATE tool_server SET enabled = false WHERE id = 'search'`)
	e.m.mu.Lock()
	e.m.loadedAt = time.Time{}
	e.m.mu.Unlock()
	ps, _ := e.m.Providers(ctx)
	for _, p := range ps {
		if p.ID() == "search" {
			t.Fatal("disabled server still provided")
		}
	}
}
