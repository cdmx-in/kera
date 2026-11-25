package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/audit"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

// ident is what the fake provider reports for a code.
type ident struct {
	Sub, Email, Name, Picture string
	Verified                  any
}

// fakeIdP is a minimal OAuth 2.0 provider: /token checks the client secret
// and the PKCE verifier, /userinfo returns the identity bound to the code.
type fakeIdP struct {
	*httptest.Server
	mu         sync.Mutex
	challenges map[string]string // code -> S256 challenge
	idents     map[string]ident  // code -> identity
}

func newFakeIdP(t *testing.T) *fakeIdP {
	f := &fakeIdP{challenges: map[string]string{}, idents: map[string]ident{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, sec, ok := r.BasicAuth()
		if !ok {
			id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		code := r.PostForm.Get("code")
		f.mu.Lock()
		ch, known := f.challenges[code]
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if id != "client-1" || sec != "s3cret" || !known || base64.RawURLEncoding.EncodeToString(sum[:]) != ch {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at-`+code+`","token_type":"Bearer","expires_in":3600}`)
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at-")
		f.mu.Lock()
		u, ok := f.idents[code]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		api.JSON(w, http.StatusOK, map[string]any{"sub": u.Sub, "email": u.Email, "email_verified": u.Verified,
			"name": u.Name, "picture": u.Picture})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

type env struct {
	t   *testing.T
	a   *app.App
	h   *handler
	srv *httptest.Server
	idp *fakeIdP
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	keys, err := secret.Derive(strings.Repeat("t", 32))
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
	if err := Mount(a); err != nil {
		t.Fatal(err)
	}
	if err := audit.Mount(a); err != nil {
		t.Fatal(err)
	}
	root = s.Handler()
	e := &env{t: t, a: a, h: &handler{a: a}, srv: srv, idp: newFakeIdP(t)}
	enc, _ := keys.EncryptString("s3cret")
	_, err = pool.Exec(context.Background(), `INSERT INTO oauth_provider (id, authorization_endpoint, token_endpoint,
		userinfo_endpoint, client_id, client_secret_enc, options) VALUES ('google', $1, $2, $3, 'client-1', $4, '{"login": true}')`,
		e.idp.URL+"/authorize", e.idp.URL+"/token", e.idp.URL+"/userinfo", enc)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func noFollow() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// signIn runs login + callback for who and returns the callback response.
func (e *env) signIn(who ident, next string) *http.Response {
	e.t.Helper()
	c := noFollow()
	resp, err := c.Get(e.srv.URL + "/oauth/google/login?next=" + url.QueryEscape(next))
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	code := ids.New()
	e.idp.mu.Lock()
	e.idp.challenges[code] = q.Get("code_challenge")
	e.idp.idents[code] = who
	e.idp.mu.Unlock()
	resp, err = c.Get(e.srv.URL + "/oauth/google/callback?code=" + code + "&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// tokenFrom extracts the session token from a successful callback redirect.
func (e *env) tokenFrom(resp *http.Response) (token, next string) {
	e.t.Helper()
	loc := resp.Header.Get("Location")
	frag, ok := strings.CutPrefix(loc, "/auth#")
	if !ok {
		e.t.Fatalf("callback redirect %q, want /auth#token=...", loc)
	}
	v, _ := url.ParseQuery(frag)
	return v.Get("token"), v.Get("next")
}

func (e *env) do(method, path, cred string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}
	resp, err := noFollow().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *env) ok(method, path, cred string, body, out any) {
	e.t.Helper()
	code, b := e.do(method, path, cred, body)
	if code != http.StatusOK {
		e.t.Fatalf("%s %s = %d %s", method, path, code, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

func (e *env) status(want int, method, path, cred string, body any) {
	e.t.Helper()
	if code, b := e.do(method, path, cred, body); code != want {
		e.t.Fatalf("%s %s = %d %s, want %d", method, path, code, b, want)
	}
}

// mkUser inserts a user directly and returns its id and a session token.
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

type me struct {
	ID, Email, Name, Role string
	ProfileImageURL       string `json:"profile_image_url"`
	Groups                []string
	Permissions           map[string]any
	Token                 string
}

func TestOAuthSignIn(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	// Login redirect carries the client, callback and PKCE parameters.
	c := noFollow()
	resp, err := c.Get(e.srv.URL + "/oauth/google/login?next=/c/abc")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc.String(), e.idp.URL+"/authorize") ||
		q.Get("client_id") != "client-1" || q.Get("redirect_uri") != e.srv.URL+"/oauth/google/callback" ||
		q.Get("response_type") != "code" || q.Get("scope") != "openid email profile" || q.Get("state") == "" ||
		q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
		t.Fatalf("login redirect %d %s", resp.StatusCode, loc)
	}

	// First user ever becomes admin; the session works as bearer and cookie.
	resp = e.signIn(ident{Sub: "a1", Email: "Ann@Example.com", Name: "Ann", Picture: "https://img/a", Verified: true}, "/c/abc")
	tok, next := e.tokenFrom(resp)
	if next != "/c/abc" {
		t.Fatalf("next = %q", next)
	}
	var cookie bool
	for _, ck := range resp.Cookies() {
		cookie = cookie || (ck.Name == auth.CookieName && ck.Value == tok && ck.HttpOnly)
	}
	if !cookie {
		t.Fatal("no session cookie")
	}
	var ann me
	e.ok("GET", "/api/v1/auths/", tok, nil, &ann)
	if ann.Role != "admin" || ann.Email != "ann@example.com" || ann.ProfileImageURL != "https://img/a" || ann.Token == "" {
		t.Fatalf("first user %+v", ann)
	}

	// Second user gets the default role; a returning user keeps their id and
	// gets a refreshed name and picture.
	tokB, _ := e.tokenFrom(e.signIn(ident{Sub: "b1", Email: "bob@example.com", Name: "Bob", Verified: "true"}, ""))
	var bob me
	e.ok("GET", "/api/v1/auths/", tokB, nil, &bob)
	if bob.Role != "user" {
		t.Fatalf("second user role %q", bob.Role)
	}
	tokB2, _ := e.tokenFrom(e.signIn(ident{Sub: "b1", Email: "bob@example.com", Name: "Robert", Picture: "https://img/b", Verified: true}, ""))
	var bob2 me
	e.ok("GET", "/api/v1/auths/", tokB2, nil, &bob2)
	if bob2.ID != bob.ID || bob2.Name != "Robert" || bob2.ProfileImageURL != "https://img/b" {
		t.Fatalf("returning user %+v", bob2)
	}

	// Merge by email links an imported account to the provider identity.
	carolID, _ := e.mkUser("user", "carol@example.com")
	tokC, _ := e.tokenFrom(e.signIn(ident{Sub: "c1", Email: "CAROL@example.com", Name: "Carol", Verified: true}, ""))
	var carol me
	e.ok("GET", "/api/v1/auths/", tokC, nil, &carol)
	var sub string
	_ = e.a.DB.QueryRow(ctx, `SELECT oauth_sub FROM "user" WHERE id = $1`, carolID).Scan(&sub)
	if carol.ID != carolID || sub != "google@c1" {
		t.Fatalf("merge: id %s want %s, sub %q", carol.ID, carolID, sub)
	}

	failCode := func(resp *http.Response) string {
		loc := resp.Header.Get("Location")
		code, ok := strings.CutPrefix(loc, "/auth?error=")
		if !ok {
			t.Fatalf("redirect %q, want an error", loc)
		}
		return code
	}
	if got := failCode(e.signIn(ident{Sub: "d1", Email: "dan@example.com", Verified: false}, "")); got != "email_unverified" {
		t.Fatalf("unverified: %s", got)
	}
	if got := failCode(e.signIn(ident{Sub: "d1", Email: "dan@example.com"}, "")); got != "email_unverified" {
		t.Fatalf("missing email_verified: %s", got)
	}
	_ = e.a.Settings.Set(ctx, "auth.oauth.allowed_domains", []string{"example.com"}, false)
	if got := failCode(e.signIn(ident{Sub: "e1", Email: "eve@other.org", Verified: true}, "")); got != "domain_not_allowed" {
		t.Fatalf("domain: %s", got)
	}
	_ = e.a.Settings.Set(ctx, "auth.oauth.merge_by_email", false, false)
	e.mkUser("user", "fay@example.com")
	if got := failCode(e.signIn(ident{Sub: "f1", Email: "fay@example.com", Verified: true}, "")); got != "email_taken" {
		t.Fatalf("merge off: %s", got)
	}
	_ = e.a.Settings.Set(ctx, "auth.signup.oauth", false, false)
	if got := failCode(e.signIn(ident{Sub: "g1", Email: "gus@example.com", Verified: true}, "")); got != "signup_disabled" {
		t.Fatalf("signup off: %s", got)
	}
	// Existing users still sign in with sign-up off.
	e.tokenFrom(e.signIn(ident{Sub: "a1", Email: "ann@example.com", Verified: true}, ""))

	// Open redirects collapse to "/".
	for _, bad := range []string{"//evil.example", "https://evil.example/x", "/\\evil.example", "javascript:alert(1)"} {
		if _, next := e.tokenFrom(e.signIn(ident{Sub: "a1", Email: "ann@example.com", Verified: true}, bad)); next != "/" {
			t.Fatalf("next %q kept as %q", bad, next)
		}
	}

	// A callback without the flow cookie, or with another state, is rejected.
	c = noFollow()
	resp, _ = c.Get(e.srv.URL + "/oauth/google/callback?code=x&state=y")
	resp.Body.Close()
	if got := failCode(resp); got != "invalid_state" {
		t.Fatalf("no cookie: %s", got)
	}
	resp, _ = c.Get(e.srv.URL + "/oauth/google/login")
	resp.Body.Close()
	resp, _ = c.Get(e.srv.URL + "/oauth/google/callback?code=x&state=forged")
	resp.Body.Close()
	if got := failCode(resp); got != "invalid_state" {
		t.Fatalf("forged state: %s", got)
	}
	resp, _ = noFollow().Get(e.srv.URL + "/oauth/nope/login")
	resp.Body.Close()
	if got := failCode(resp); got != "unknown_provider" {
		t.Fatalf("unknown provider: %s", got)
	}

	var prov struct {
		Providers []map[string]string
		LoginForm bool `json:"login_form"`
	}
	e.ok("GET", "/api/v1/auths/providers", "", nil, &prov)
	if len(prov.Providers) != 1 || prov.Providers[0]["id"] != "google" || prov.Providers[0]["name"] != "Google" || prov.LoginForm {
		t.Fatalf("providers %+v", prov)
	}
}

func TestFirstUserRace(t *testing.T) {
	e := setup(t)
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := e.h.resolveUser(context.Background(), "google",
				Userinfo{Sub: string(rune('a' + i)), Email: string(rune('a'+i)) + "@example.com"})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var admins int
	_ = e.a.DB.QueryRow(context.Background(), `SELECT count(*) FROM "user" WHERE role = 'admin'`).Scan(&admins)
	if admins != 1 {
		t.Fatalf("%d admins", admins)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"": "/", "/": "/", "/c/1?x=2#y": "/c/1?x=2#y", "//x.y": "/",
		"http://x.y": "/", "x": "/", "/a\\b": "/", "/\r\nLocation: x": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAPIKeys(t *testing.T) {
	e := setup(t)
	_, admin := e.mkUser("admin", "root@example.com")
	_, user := e.mkUser("user", "u@example.com")

	// Off by default for users.
	e.status(http.StatusForbidden, "POST", "/api/v1/auths/api_keys", user, map[string]string{"name": "x"})
	var perms map[string]map[string]any
	e.ok("GET", "/api/v1/users/default/permissions", admin, nil, &perms)
	perms["features"]["api_keys"] = true
	e.ok("POST", "/api/v1/users/default/permissions", admin, perms, nil)

	var created struct{ ID, Name, Key string }
	e.ok("POST", "/api/v1/auths/api_keys", user, map[string]string{"name": "browser"}, &created)
	if !strings.HasPrefix(created.Key, "sk-") || created.Name != "browser" {
		t.Fatalf("created %+v", created)
	}
	var who me
	e.ok("GET", "/api/v1/auths/", created.Key, nil, &who)
	if who.Email != "u@example.com" || who.Token != "" {
		t.Fatalf("via key %+v", who)
	}
	var list []APIKey
	e.ok("GET", "/api/v1/auths/api_keys", user, nil, &list)
	if len(list) != 1 || list[0].Prefix != created.Key[:10] || list[0].LastUsedAt == nil {
		t.Fatalf("list %+v", list)
	}
	if strings.Contains(mustJSON(list), created.Key) {
		t.Fatal("list leaks the key")
	}

	// Turning the permission off disables existing keys.
	perms["features"]["api_keys"] = false
	e.ok("POST", "/api/v1/users/default/permissions", admin, perms, nil)
	e.status(http.StatusUnauthorized, "GET", "/api/v1/auths/", created.Key, nil)
	perms["features"]["api_keys"] = true
	e.ok("POST", "/api/v1/users/default/permissions", admin, perms, nil)
	e.status(http.StatusOK, "GET", "/api/v1/auths/", created.Key, nil)

	// Compat single-key endpoints.
	var ck struct {
		APIKey string `json:"api_key"`
	}
	e.ok("POST", "/api/v1/auths/api_key", user, nil, &ck)
	first := ck.APIKey
	e.ok("POST", "/api/v1/auths/api_key", user, nil, &ck)
	e.status(http.StatusUnauthorized, "GET", "/api/v1/auths/", first, nil) // rotated
	e.status(http.StatusOK, "GET", "/api/v1/auths/", created.Key, nil)     // named key untouched
	e.ok("GET", "/api/v1/auths/api_key", user, nil, &ck)
	if !strings.HasSuffix(ck.APIKey, "...") || strings.Contains(ck.APIKey, created.Key) {
		t.Fatalf("compat get %q", ck.APIKey)
	}

	e.status(http.StatusNotFound, "DELETE", "/api/v1/auths/api_keys/"+created.ID, admin, nil) // not admin's key
	e.ok("DELETE", "/api/v1/auths/api_keys/"+created.ID, user, nil, nil)
	e.status(http.StatusUnauthorized, "GET", "/api/v1/auths/", created.Key, nil)
	e.ok("DELETE", "/api/v1/auths/api_key", user, nil, nil)
	e.status(http.StatusNotFound, "GET", "/api/v1/auths/api_key", user, nil)
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestAdminOnly(t *testing.T) {
	e := setup(t)
	uid, user := e.mkUser("user", "u@example.com")
	for _, r := range [][2]string{{"GET", "/api/v1/users/"}, {"GET", "/api/v1/users/" + uid}, {"POST", "/api/v1/users/" + uid + "/update"},
		{"DELETE", "/api/v1/users/" + uid}, {"GET", "/api/v1/users/default/permissions"}, {"POST", "/api/v1/users/default/permissions"},
		{"GET", "/api/v1/groups/"}, {"POST", "/api/v1/groups/create"}, {"GET", "/api/v1/groups/id/x"},
		{"DELETE", "/api/v1/groups/id/x/delete"}, {"GET", "/api/v1/grants/model/m"}, {"PUT", "/api/v1/grants/model/m"}} {
		e.status(http.StatusForbidden, r[0], r[1], user, map[string]any{})
		e.status(http.StatusUnauthorized, r[0], r[1], "", map[string]any{})
	}
}

func TestUsersAdmin(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	adminID, admin := e.mkUser("admin", "root@example.com")
	uid, user := e.mkUser("user", "zed@example.com")
	_, _, _ = CreateAPIKey(ctx, e.a.DB, uid, "k")

	var page struct {
		Users []userOut
		Total int
	}
	e.ok("GET", "/api/v1/users/?query=ZED", admin, nil, &page)
	if page.Total != 1 || len(page.Users) != 1 || page.Users[0].ID != uid || page.Users[0].APIKeyCount != 1 ||
		string(page.Users[0].Groups) != "[]" || page.Users[0].CreatedAt == 0 {
		t.Fatalf("users %+v", page)
	}
	e.ok("GET", "/api/v1/users/?page=2", admin, nil, &page)
	if page.Total != 2 || len(page.Users) != 0 {
		t.Fatalf("page 2 %+v", page)
	}
	// Sorting: opposite orders reverse the list; an unknown key falls back to signup order, not an error.
	var asc, desc, bogus struct{ Users []userOut }
	e.ok("GET", "/api/v1/users/?sort=email&order=asc", admin, nil, &asc)
	e.ok("GET", "/api/v1/users/?sort=email&order=desc", admin, nil, &desc)
	e.ok("GET", "/api/v1/users/?sort=nope%3B+DROP", admin, nil, &bogus)
	if len(asc.Users) != 2 || asc.Users[0].ID != desc.Users[1].ID || asc.Users[0].Email > asc.Users[1].Email || len(bogus.Users) != 2 {
		t.Fatalf("sort asc %+v desc %+v bogus %+v", asc, desc, bogus)
	}

	// ?size= pages with a stable order (created_at, id) and a bounded size.
	all := []string{adminID, uid}
	for i := range 23 {
		id, _ := e.mkUser("user", fmt.Sprintf("p%02d@example.com", i))
		all = append(all, id)
	}
	_, _ = e.a.DB.Exec(ctx, `UPDATE "user" SET created_at = '2026-01-01'`) // ties fall back to id
	slices.Sort(all)
	var got []string
	for p := 1; p <= 3; p++ {
		e.ok("GET", fmt.Sprintf("/api/v1/users/?size=10&page=%d", p), admin, nil, &page)
		if page.Total != 25 {
			t.Fatalf("size=10 page %d total %d", p, page.Total)
		}
		for _, u := range page.Users {
			got = append(got, u.ID)
		}
	}
	if !slices.Equal(got, all) {
		t.Fatalf("paged order %v, want %v", got, all)
	}
	for size, want := range map[string]int{"1": 10, "-5": 10, "x": 25, "": 25, "500": 25} {
		e.ok("GET", "/api/v1/users/?size="+size, admin, nil, &page)
		if len(page.Users) != want {
			t.Fatalf("size=%q: %d users, want %d", size, len(page.Users), want)
		}
	}
	if s := pageSize(httptest.NewRequest("GET", "/?size=500", nil)); s != maxUsersPerPage {
		t.Fatalf("size=500 -> %d", s)
	}

	var u userOut
	e.ok("POST", "/api/v1/users/"+uid+"/update", admin, map[string]string{"name": "Zed", "role": "admin"}, &u)
	if u.Name != "Zed" || u.Role != "admin" {
		t.Fatalf("update %+v", u)
	}
	e.status(http.StatusForbidden, "POST", "/api/v1/users/"+adminID+"/update", admin, map[string]string{"role": "user"})
	e.status(http.StatusBadRequest, "POST", "/api/v1/users/"+uid+"/update", admin, map[string]string{"role": "root"})
	e.ok("POST", "/api/v1/users/"+uid+"/update", admin, map[string]string{"role": "user"}, &u)
	e.status(http.StatusNotFound, "POST", "/api/v1/users/nope/update", admin, map[string]string{"name": "x"})

	// The last admin cannot be demoted, whoever asks.
	err := e.h.applyUserUpdate(ctx, "someone-else", adminID, nil, ptr("user"))
	if ae, ok := err.(*api.Error); !ok || ae.Code != "last_admin" {
		t.Fatalf("last admin: %v", err)
	}

	// Own settings merge shallowly.
	var s map[string]any
	e.ok("POST", "/api/v1/users/user/settings/update", user, map[string]any{"ui": map[string]any{"theme": "dark"}}, &s)
	e.ok("POST", "/api/v1/users/user/settings/update", user, map[string]any{"lang": "en"}, &s)
	e.ok("GET", "/api/v1/users/user/settings", user, nil, &s)
	if s["lang"] != "en" || s["ui"].(map[string]any)["theme"] != "dark" {
		t.Fatalf("settings %v", s)
	}
	e.status(http.StatusBadRequest, "POST", "/api/v1/users/user/settings/update", user, []int{1})

	// Avatars: generated SVG, or a redirect to the stored picture.
	code, body := e.do("GET", "/api/v1/users/"+uid+"/profile/image", user, nil)
	if code != http.StatusOK || !strings.Contains(string(body), ">Z</text>") {
		t.Fatalf("avatar %d %s", code, body)
	}
	_, _ = e.a.DB.Exec(ctx, `UPDATE "user" SET profile_image_url = 'https://img.example/z.png' WHERE id = $1`, uid)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/users/"+uid+"/profile/image", nil)
	req.Header.Set("Authorization", "Bearer "+user)
	resp, _ := noFollow().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://img.example/z.png" {
		t.Fatalf("avatar redirect %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	e.status(http.StatusForbidden, "DELETE", "/api/v1/users/"+adminID, admin, nil)
	_, _ = e.a.DB.Exec(ctx, `INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission)
		VALUES ('g-zed', 'model', 'm', 'user', $1, 'read')`, uid)
	e.ok("DELETE", "/api/v1/users/"+uid, admin, nil, nil)
	e.status(http.StatusNotFound, "GET", "/api/v1/users/"+uid, admin, nil)
	var grants int
	_ = e.a.DB.QueryRow(ctx, `SELECT count(*) FROM access_grant WHERE principal_id = $1`, uid).Scan(&grants)
	if grants != 0 {
		t.Fatal("deleting a user left their grants")
	}
	e.status(http.StatusUnauthorized, "GET", "/api/v1/auths/", user, nil)
}

func ptr[T any](v T) *T { return &v }

func TestGroupsAndGrants(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, admin := e.mkUser("admin", "root@example.com")
	uid, user := e.mkUser("user", "u@example.com")

	var g groupOut
	e.ok("POST", "/api/v1/groups/create", admin, map[string]any{"name": "Finance", "description": "money",
		"permissions": map[string]any{"features": map[string]any{"api_keys": true}}}, &g)
	e.status(http.StatusConflict, "POST", "/api/v1/groups/create", admin, map[string]any{"name": "Finance"})
	e.status(http.StatusBadRequest, "POST", "/api/v1/groups/create", admin, map[string]any{"name": " "})

	// Membership: unknown ids are ignored; group permissions add to defaults.
	e.status(http.StatusForbidden, "POST", "/api/v1/auths/api_keys", user, map[string]string{})
	e.ok("POST", "/api/v1/groups/id/"+g.ID+"/users/add", admin, map[string]any{"user_ids": []string{uid, "ghost"}}, &g)
	if g.UserIDs == nil || len(*g.UserIDs) != 1 || (*g.UserIDs)[0] != uid || g.MemberCount != 1 {
		t.Fatalf("add %+v", g)
	}
	var who me
	e.ok("GET", "/api/v1/auths/", user, nil, &who)
	if len(who.Groups) != 1 || who.Groups[0] != g.ID {
		t.Fatalf("groups %+v", who.Groups)
	}
	e.ok("POST", "/api/v1/auths/api_keys", user, map[string]string{"name": "via group"}, nil)

	var list []groupOut
	e.ok("GET", "/api/v1/groups/", admin, nil, &list)
	if len(list) != 1 || list[0].MemberCount != 1 || list[0].Description != "money" || string(list[0].MemberPreview) != `["`+uid+`"]` {
		t.Fatalf("list %+v", list)
	}
	var page struct{ Users []userOut }
	e.ok("GET", "/api/v1/users/?query=u@", admin, nil, &page)
	if !strings.Contains(string(page.Users[0].Groups), `"Finance"`) {
		t.Fatalf("user groups %s", page.Users[0].Groups)
	}

	e.ok("POST", "/api/v1/groups/id/"+g.ID+"/update", admin, map[string]any{"name": "Finance Team"}, &g)
	if g.Name != "Finance Team" || g.Description != "money" || !strings.Contains(string(g.Permissions), "api_keys") {
		t.Fatalf("update %+v", g)
	}
	e.ok("POST", "/api/v1/groups/id/"+g.ID+"/users/remove", admin, map[string]any{"user_ids": []string{uid}}, &g)
	if g.UserIDs == nil || len(*g.UserIDs) != 0 {
		t.Fatalf("remove %+v", g)
	}
	e.status(http.StatusNotFound, "GET", "/api/v1/groups/id/nope", admin, nil)

	// Grants: an admin edit replaces every grant, managed ones included.
	_, _ = e.a.DB.Exec(ctx, `INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission, managed)
		VALUES ($1, 'model', 'hf.co/org/m:7b', 'user', 'old', 'read', true)`, ids.New())
	var grants []map[string]string
	put := []map[string]string{{"principal": "*", "permission": "read"}, {"principal": "group:" + g.ID, "permission": "write"}}
	e.ok("PUT", "/api/v1/grants/model/hf.co/org/m:7b", admin, put, &grants)
	if mustJSON(grants) != `[{"permission":"write","principal":"group:`+g.ID+`"},{"permission":"read","principal":"user:*"}]` {
		t.Fatalf("grants %s", mustJSON(grants))
	}
	var managed int
	_ = e.a.DB.QueryRow(ctx, `SELECT count(*) FROM access_grant WHERE managed`).Scan(&managed)
	if managed != 0 {
		t.Fatalf("%d managed grants left", managed)
	}
	e.status(http.StatusBadRequest, "PUT", "/api/v1/grants/model/m", admin, []map[string]string{{"principal": "team:x", "permission": "read"}})
	e.status(http.StatusBadRequest, "PUT", "/api/v1/grants/model/m", admin, []map[string]string{{"principal": "*", "permission": "own"}})
	e.status(http.StatusBadRequest, "GET", "/api/v1/grants/planet/m", admin, nil)

	// Deleting a group drops its grants.
	e.ok("DELETE", "/api/v1/groups/id/"+g.ID+"/delete", admin, nil, nil)
	e.ok("GET", "/api/v1/grants/model/hf.co/org/m:7b", admin, nil, &grants)
	if len(grants) != 1 || grants[0]["principal"] != "user:*" {
		t.Fatalf("after delete %v", grants)
	}
	e.status(http.StatusNotFound, "DELETE", "/api/v1/groups/id/"+g.ID+"/delete", admin, nil)
}

func TestSignoutAndCookie(t *testing.T) {
	e := setup(t)
	_, tok := e.mkUser("user", "u@example.com")
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/auths/", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var refreshed bool
	for _, c := range resp.Cookies() {
		refreshed = refreshed || (c.Name == auth.CookieName && c.Value != "" && c.MaxAge > 0)
	}
	if resp.StatusCode != http.StatusOK || !refreshed {
		t.Fatalf("cookie session %d refreshed=%v", resp.StatusCode, refreshed)
	}
	for _, method := range []string{"POST", "GET"} {
		req, _ = http.NewRequest(method, e.srv.URL+"/api/v1/auths/signout", nil)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		var cleared bool
		for _, c := range resp.Cookies() {
			cleared = cleared || (c.Name == auth.CookieName && c.MaxAge < 0)
		}
		if !cleared {
			t.Fatalf("%s signout kept the cookie", method)
		}
	}
}

// A state-changing request authenticated only by the cookie must come from
// our own origin; a Bearer credential is not sent by browsers on their own.
func TestCookieCSRF(t *testing.T) {
	e := setup(t)
	_, tok := e.mkUser("user", "u@example.com")
	post := func(site string, cookie, bearer bool) int {
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/users/user/settings/update", strings.NewReader(`{"a":1}`))
		req.Header.Set("Sec-Fetch-Site", site)
		if cookie {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
		}
		if bearer {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		site           string
		cookie, bearer bool
		want           int
	}{
		{"same-origin", true, false, http.StatusOK},
		{"cross-site", true, false, http.StatusUnauthorized},
		{"same-site", true, false, http.StatusUnauthorized},
		{"cross-site", false, true, http.StatusOK},
	} {
		if got := post(c.site, c.cookie, c.bearer); got != c.want {
			t.Errorf("%+v: status %d", c, got)
		}
	}
}

func TestCLI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.mkUser("user", "ann@example.com")
	var out, warn bytes.Buffer
	if err := UserCmd(ctx, e.a.DB, []string{"promote", "ANN@example.com"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if err := UserCmd(ctx, e.a.DB, []string{"promote", "nobody@example.com"}, strings.NewReader(""), &out); err == nil {
		t.Fatal("promoted a missing user")
	}
	out.Reset()
	if err := UserCmd(ctx, e.a.DB, []string{"list"}, strings.NewReader(""), &out); err != nil || !strings.Contains(out.String(), "ann@example.com  admin") {
		t.Fatalf("list: %v %q", err, out.String())
	}
	out.Reset()
	if err := UserCmd(ctx, e.a.DB, []string{"add", "--name", "Bo Lee", "Bo@Example.com"}, strings.NewReader(""), &out); err != nil || !strings.Contains(out.String(), "bo@example.com as user") {
		t.Fatalf("add: %v %q", err, out.String())
	}
	if err := UserCmd(ctx, e.a.DB, []string{"add", "bo@example.com"}, strings.NewReader(""), &out); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("add twice: %v", err)
	}
	if err := UserCmd(ctx, e.a.DB, []string{"add", "--admin", "root@example.com"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	var role, uname string
	if err := e.a.DB.QueryRow(ctx, `SELECT role, name FROM "user" WHERE email = 'root@example.com'`).Scan(&role, &uname); err != nil || role != "admin" || uname != "root" {
		t.Fatalf("admin user: %v %q %q", err, role, uname)
	}
	out.Reset()
	if err := APIKeyCmd(ctx, e.a.DB, []string{"create", "--user", "ann@example.com", "--name", "cli"}, &out, &warn); err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(out.String())
	if !strings.HasPrefix(key, "sk-") || warn.Len() != 0 {
		t.Fatalf("create: %q warn %q", key, warn.String())
	}
	e.status(http.StatusOK, "GET", "/api/v1/auths/", key, nil)
	out.Reset()
	if err := APIKeyCmd(ctx, e.a.DB, []string{"list", "--user", "ann@example.com"}, &out, &warn); err != nil ||
		!strings.Contains(out.String(), key[:10]) || strings.Contains(out.String(), key) {
		t.Fatalf("list: %v %q", err, out.String())
	}
	e.mkUser("user", "bob@example.com")
	out.Reset()
	if err := APIKeyCmd(ctx, e.a.DB, []string{"create", "--user", "bob@example.com"}, &out, &warn); err != nil ||
		!strings.Contains(warn.String(), "features.api_keys") {
		t.Fatalf("permission warning: %v %q", err, warn.String())
	}
}
