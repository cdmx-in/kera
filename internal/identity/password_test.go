package identity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/settings"
)

// Passwords used below. They are long enough to pass the policy and never
// appear anywhere they should not: TestNothingSecretIsLogged looks for them.
const (
	pwAnn  = "ann-first-password"
	pwNew  = "ann-second-password"
	pwBob  = "bob-first-password"
	pwEve  = "eve-wrong-guess"
	pwRoot = "root-bootstrap-password"
)

// community is the stack a stranger gets from the compose file: no OAuth
// provider, so password sign-in is on by default.
func community(t *testing.T) *env {
	t.Helper()
	e := setup(t)
	if _, err := e.a.DB.Exec(context.Background(), `DELETE FROM oauth_provider`); err != nil {
		t.Fatal(err)
	}
	return e
}

// logs captures what the server logs, as JSON lines.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.Write(p) }
func (l *logs) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }

func captureLogs(t *testing.T) *logs {
	l := &logs{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return l
}

type reply struct {
	Code int
	Body []byte
	Head http.Header
	Resp *http.Response
}

func (r reply) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
}

// send posts JSON as the web app does. from is the client address a reverse
// proxy would report (the test server is on loopback, which is trusted).
func (e *env) send(method, path, cred, from string, body any) reply {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}
	if from != "" {
		req.Header.Set("X-Forwarded-For", from)
	}
	resp, err := noFollow().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, b, resp.Header, resp}
}

func (e *env) signinAs(email, password, from string) reply {
	return e.send("POST", "/api/v1/auths/signin", "", from, map[string]string{"email": email, "password": password})
}

// withPassword inserts a user with a password and returns the id.
func (e *env) withPassword(role, email, password string) string {
	e.t.Helper()
	id, _ := e.mkUser(role, email)
	hash, err := auth.HashPassword(context.Background(), password)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.a.DB.Exec(context.Background(), `INSERT INTO auth_password (user_id, hash) VALUES ($1, $2)`, id, hash); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) hasPassword(id string) bool {
	var n int
	_ = e.a.DB.QueryRow(context.Background(), `SELECT count(*) FROM auth_password WHERE user_id = $1`, id).Scan(&n)
	return n == 1
}

func TestSigninDefaultRule(t *testing.T) {
	ctx := context.Background()
	e := setup(t) // has an OAuth login provider
	e.withPassword("user", "ann@example.com", pwAnn)
	providers := func() (list []map[string]string, form bool) {
		var out struct {
			Providers []map[string]string `json:"providers"`
			LoginForm bool                `json:"login_form"`
		}
		e.ok("GET", "/api/v1/auths/providers", "", nil, &out)
		return out.Providers, out.LoginForm
	}

	// SSO deployment, no setting: no form, and the endpoint refuses (not just hides).
	if list, form := providers(); form || len(list) != 1 {
		t.Fatalf("with a login provider and no setting: login_form=%v providers=%v", form, list)
	}
	if r := e.signinAs("ann@example.com", pwAnn, ""); r.Code != http.StatusForbidden || !strings.Contains(string(r.Body), "password_disabled") || len(r.Resp.Cookies()) != 0 {
		t.Fatalf("sign-in on an SSO-only install: %d %s", r.Code, r.Body)
	}

	// A provider that is not a login provider (a tool server's OAuth) does not count.
	if _, err := e.a.DB.Exec(ctx, `UPDATE oauth_provider SET options = '{}'`); err != nil {
		t.Fatal(err)
	}
	if _, form := providers(); !form {
		t.Fatal("a provider without login=true switched the form off")
	}
	if _, err := e.a.DB.Exec(ctx, `UPDATE oauth_provider SET options = '{"login": true}'`); err != nil {
		t.Fatal(err)
	}

	// Explicitly on: the form shows beside the provider and the endpoint works.
	if err := e.a.Settings.Set(ctx, "auth.password.enabled", true, false); err != nil {
		t.Fatal(err)
	}
	if list, form := providers(); !form || len(list) != 1 {
		t.Fatalf("explicitly on: login_form=%v providers=%v", form, list)
	}
	if r := e.signinAs("ann@example.com", pwAnn, ""); r.Code != http.StatusOK {
		t.Fatalf("explicitly on: %d %s", r.Code, r.Body)
	}

	// Explicitly off wins over everything, and refuses at once.
	if err := e.a.Settings.Set(ctx, "auth.password.enabled", false, false); err != nil {
		t.Fatal(err)
	}
	if _, form := providers(); form {
		t.Fatal("explicitly off: login_form is true")
	}
	if r := e.signinAs("ann@example.com", pwAnn, ""); r.Code != http.StatusForbidden || len(r.Resp.Cookies()) != 0 {
		t.Fatalf("explicitly off: %d %s", r.Code, r.Body)
	}

	// Back to automatic (the setting reset): no provider means on, a provider means off.
	if _, err := e.a.DB.Exec(ctx, `DELETE FROM config WHERE key = 'auth.password.enabled'`); err != nil {
		t.Fatal(err)
	}
	e.a.Settings.Invalidate()
	if _, form := providers(); form {
		t.Fatal("automatic with a provider: login_form is true")
	}
	if _, err := e.a.DB.Exec(ctx, `DELETE FROM oauth_provider`); err != nil {
		t.Fatal(err)
	}
	if list, form := providers(); !form || len(list) != 0 {
		t.Fatalf("community install: login_form=%v providers=%v", form, list)
	}
	if r := e.signinAs("ann@example.com", pwAnn, ""); r.Code != http.StatusOK {
		t.Fatalf("community install: %d %s", r.Code, r.Body)
	}

	// A value that is not a switch fails closed.
	if _, err := e.a.DB.Exec(ctx, `INSERT INTO config (key, value) VALUES ('auth.password.enabled', '"yes"')`); err != nil {
		t.Fatal(err)
	}
	e.a.Settings.Invalidate()
	if r := e.signinAs("ann@example.com", pwAnn, ""); r.Code != http.StatusForbidden {
		t.Fatalf("garbage setting: %d %s", r.Code, r.Body)
	}
}

func TestSignin(t *testing.T) {
	e := community(t)
	ctx := context.Background()
	annID := e.withPassword("user", "ann@example.com", pwAnn)
	e.mkUser("user", "bob@example.com") // no password
	carl := e.withPassword("user", "carl@example.com", pwBob)
	e.withPassword("user", "dora@example.com", pwBob)
	if _, err := e.a.DB.Exec(ctx, `UPDATE "user" SET disabled_at = now() WHERE id = $1`, carl); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.DB.Exec(ctx, `UPDATE "user" SET role = 'pending' WHERE email = 'dora@example.com'`); err != nil {
		t.Fatal(err)
	}

	// Every failure is the same answer: status, body, no cookie, no token.
	var first reply
	for i, c := range []struct{ name, email, pw string }{
		{"wrong password", "ann@example.com", pwEve},
		{"unknown user", "nobody@example.com", pwAnn},
		{"user without a password", "bob@example.com", pwAnn},
		{"disabled user, right password", "carl@example.com", pwBob},
		{"pending user, right password", "dora@example.com", pwBob},
		{"password with a trailing space", "ann@example.com", pwAnn + " "},
		{"empty password", "ann@example.com", ""},
		{"empty email", "", pwAnn},
		{"huge password", "ann@example.com", strings.Repeat("x", 5000)},
		{"NUL in the email", "ann\x00@example.com", pwAnn},
	} {
		r := e.signinAs(c.email, c.pw, fmt.Sprintf("203.0.113.%d", 100+i)) // a different address each time: the limiter stays out of it
		if i == 0 {
			first = r
		}
		if r.Code != http.StatusUnauthorized || !bytes.Equal(r.Body, first.Body) || len(r.Resp.Cookies()) != 0 {
			t.Errorf("%s: %d %s (cookies %v), want the answer of %q: %d %s", c.name, r.Code, r.Body, r.Resp.Cookies(), "wrong password", first.Code, first.Body)
		}
	}
	var fail struct{ Detail, Code string }
	first.json(t, &fail)
	if fail.Detail != "Invalid email or password" || fail.Code != "invalid_credentials" {
		t.Fatalf("failure body %+v", fail)
	}

	// A form post is not a sign-in (a page on another site can send one).
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/auths/signin", strings.NewReader(`{"email":"ann@example.com","password":"`+pwAnn+`"}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain body: %d", resp.StatusCode)
	}

	// The good sign-in: same cookie and token as the OAuth callback, and the body the clients read.
	r := e.signinAs(" Ann@Example.com ", pwAnn, "")
	if r.Code != http.StatusOK {
		t.Fatalf("sign-in: %d %s", r.Code, r.Body)
	}
	var got struct {
		Token     string `json:"token"`
		TokenType string `json:"token_type"`
		ExpiresAt int64  `json:"expires_at"`
		ID, Email string
		Role      string
	}
	var raw map[string]json.RawMessage
	r.json(t, &raw)
	r.json(t, &got)
	for _, k := range []string{"token", "token_type", "expires_at", "id", "email", "name", "role", "profile_image_url", "permissions", "groups"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("sign-in body lacks %q: %s", k, r.Body)
		}
	}
	if got.ID != annID || got.Email != "ann@example.com" || got.TokenType != "Bearer" || got.Role != "user" || got.Token == "" {
		t.Fatalf("sign-in body %+v", got)
	}
	ttl := int64(e.a.Auth.SessionTTL(ctx) / time.Second)
	if d := got.ExpiresAt - time.Now().Unix(); d < ttl-5 || d > ttl+5 {
		t.Errorf("expires_at is %ds away, want the session TTL %ds", d, ttl)
	}
	var cookie *http.Cookie
	for _, c := range r.Resp.Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != got.Token || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || int64(cookie.MaxAge) != ttl {
		t.Fatalf("session cookie %+v", cookie)
	}
	if strings.Contains(string(r.Body), "argon2") || strings.Contains(string(r.Body), pwAnn) {
		t.Fatal("the sign-in body carries the password or its hash")
	}
	var meBody me
	e.ok("GET", "/api/v1/auths/", got.Token, nil, &meBody)
	if meBody.ID != annID {
		t.Fatalf("the sign-in token does not work: %+v", meBody)
	}
}

func TestSigninRehashesStaleHashes(t *testing.T) {
	e := community(t)
	ctx := context.Background()
	id, _ := e.mkUser("user", "ann@example.com")
	stale := staleHash(t, pwAnn) // made with one lane, today's are two
	if _, err := e.a.DB.Exec(ctx, `INSERT INTO auth_password (user_id, hash) VALUES ($1, $2)`, id, stale); err != nil {
		t.Fatal(err)
	}
	if r := e.signinAs("ann@example.com", pwAnn, ""); r.Code != http.StatusOK {
		t.Fatalf("sign-in with a stale hash: %d %s", r.Code, r.Body)
	}
	var now string
	if err := e.a.DB.QueryRow(ctx, `SELECT hash FROM auth_password WHERE user_id = $1`, id).Scan(&now); err != nil {
		t.Fatal(err)
	}
	if now == stale || !strings.HasPrefix(now, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash after sign-in: %q", now)
	}
	if match, rehash, err := auth.VerifyPassword(ctx, pwAnn, now); err != nil || !match || rehash {
		t.Fatalf("new hash: match=%v rehash=%v err=%v", match, rehash, err)
	}
	// A wrong password never rewrites anything.
	if _, err := e.a.DB.Exec(ctx, `UPDATE auth_password SET hash = $2 WHERE user_id = $1`, id, stale); err != nil {
		t.Fatal(err)
	}
	e.signinAs("ann@example.com", pwEve, "")
	var after string
	_ = e.a.DB.QueryRow(ctx, `SELECT hash FROM auth_password WHERE user_id = $1`, id).Scan(&after)
	if after != stale {
		t.Fatal("a failed sign-in rewrote the hash")
	}
}

func TestSigninRateLimit(t *testing.T) {
	e := community(t)
	e.withPassword("user", "ann@example.com", pwAnn)
	_, admin := e.mkUser("admin", "root@example.com")

	// Five wrong tries for one email from one address ...
	for i := range pairFree {
		if r := e.signinAs("ann@example.com", pwEve, "203.0.113.10"); r.Code != http.StatusUnauthorized {
			t.Fatalf("try %d: %d %s", i+1, r.Code, r.Body)
		}
	}
	// ... then even the right password from that address is refused, with a way to know when to come back.
	r := e.signinAs("ann@example.com", pwAnn, "203.0.113.10")
	if r.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong tries: %d %s", pairFree, r.Code, r.Body)
	}
	var body struct {
		Detail, Code string
		RetryAfter   int `json:"retry_after"`
	}
	r.json(t, &body)
	if r.Head.Get("Retry-After") != "30" || body.RetryAfter != 30 || body.Code != "rate_limited" || body.Detail != "Too many attempts. Try again in 30 seconds." {
		t.Fatalf("429: header %q body %+v", r.Head.Get("Retry-After"), body)
	}
	if len(r.Resp.Cookies()) != 0 {
		t.Fatal("a locked sign-in set a cookie")
	}
	// Someone else is not locked out by it, and an unknown email is locked the same way (no tell).
	e.withPassword("user", "bob@example.com", pwBob)
	if r := e.signinAs("bob@example.com", pwBob, "203.0.113.10"); r.Code != http.StatusOK {
		t.Fatalf("another user from the same address: %d %s", r.Code, r.Body)
	}
	for range pairFree {
		e.signinAs("ghost@example.com", pwEve, "203.0.113.97")
	}
	if g := e.signinAs("ghost@example.com", pwEve, "203.0.113.97"); g.Code != http.StatusTooManyRequests || g.Head.Get("Retry-After") != "30" {
		t.Fatalf("unknown email after %d tries: %d (%q)", pairFree, g.Code, g.Head.Get("Retry-After"))
	}
	// An admin setting a new password clears the lock for every address (and ends their sessions).
	annID := e.idOf("ann@example.com")
	if r := e.send("POST", "/api/v1/users/"+annID+"/password", admin, "", map[string]string{"password": pwNew}); r.Code != http.StatusOK {
		t.Fatalf("admin set password: %d %s", r.Code, r.Body)
	}
	if r := e.signinAs("ann@example.com", pwNew, "203.0.113.10"); r.Code != http.StatusOK {
		t.Fatalf("after the admin reset, from the locked address: %d %s", r.Code, r.Body)
	}
}

// Strangers who know an email can burn their own tries on it, not the owner's: the strict
// count is per email and address together.
func TestSigninOwnerIsNotLockedOutByStrangers(t *testing.T) {
	e := community(t)
	e.withPassword("admin", "root@example.com", pwRoot)
	for i := range 4 { // four attackers, each spending its whole allowance and being locked
		for range pairFree {
			e.signinAs("root@example.com", pwEve, fmt.Sprintf("198.51.100.%d", 10+i))
		}
		if r := e.signinAs("root@example.com", pwEve, fmt.Sprintf("198.51.100.%d", 10+i)); r.Code != http.StatusTooManyRequests {
			t.Fatalf("attacker %d not locked: %d", i, r.Code)
		}
	}
	// 20 failed tries so far, under the email-wide ceiling: the owner, from their own address, signs in ...
	if r := e.signinAs("root@example.com", pwRoot, "203.0.113.1"); r.Code != http.StatusOK {
		t.Fatalf("owner after four strangers: %d %s", r.Code, r.Body)
	}
	// ... and again, since their success cleared their pair and gave back its try.
	for range 3 {
		if r := e.signinAs("root@example.com", pwRoot, "203.0.113.1"); r.Code != http.StatusOK {
			t.Fatalf("owner signing in again: %d %s", r.Code, r.Body)
		}
	}
	// The owner's own typos are forgiven at their address the same way a success clears the pair.
	for range pairFree - 1 {
		e.signinAs("root@example.com", pwEve, "203.0.113.1")
	}
	if r := e.signinAs("root@example.com", pwRoot, "203.0.113.1"); r.Code != http.StatusOK {
		t.Fatalf("owner after typos: %d %s", r.Code, r.Body)
	}
	if r := e.signinAs("root@example.com", pwEve, "203.0.113.1"); r.Code != http.StatusUnauthorized {
		t.Fatalf("a good sign-in did not clear the pair: %d", r.Code)
	}
}

// A guesser with many addresses still runs out: the email-wide ceiling is loose, not absent.
func TestSigninEmailWideCeiling(t *testing.T) {
	e := community(t)
	e.withPassword("user", "ann@example.com", pwAnn)
	for i := range emailFree {
		if r := e.signinAs("ann@example.com", pwEve, fmt.Sprintf("198.51.%d.%d", i/200, i%200+1)); r.Code != http.StatusUnauthorized {
			t.Fatalf("try %d from its own address: %d %s", i+1, r.Code, r.Body)
		}
	}
	r := e.signinAs("ann@example.com", pwEve, "203.0.113.200") // a fresh address: only the email-wide count can refuse it
	if r.Code != http.StatusTooManyRequests || r.Head.Get("Retry-After") != "30" {
		t.Fatalf("after %d tries from %d addresses: %d %s", emailFree, emailFree, r.Code, r.Body)
	}
	// The owner's successes do not wipe what the guesser spent.
	if r := e.signinAs("ann@example.com", pwAnn, "203.0.113.201"); r.Code != http.StatusTooManyRequests {
		t.Fatalf("the owner during the lock: %d", r.Code)
	}
}

// Parallel guesses meet the locks too: the try is counted before it is judged.
func TestSigninParallelGuessesAreLimited(t *testing.T) {
	e := community(t)
	e.withPassword("user", "ann@example.com", pwAnn)
	run := func(n int, from func(i int) string) map[int]int {
		var wg sync.WaitGroup
		var mu sync.Mutex
		codes := map[int]int{}
		for i := range n {
			wg.Go(func() {
				r := e.signinAs("ann@example.com", fmt.Sprintf("guess-number-%d", i), from(i))
				mu.Lock()
				codes[r.Code]++
				mu.Unlock()
			})
		}
		wg.Wait()
		return codes
	}
	// 40 at once from one address: exactly the pair allowance reaches the password check.
	if got := run(40, func(int) string { return "198.51.100.1" }); got[http.StatusUnauthorized] != pairFree || got[http.StatusTooManyRequests] != 40-pairFree {
		t.Fatalf("one address: %v", got)
	}
	// 60 at once from 60 addresses: the email-wide ceiling stops it (the five already spent above count).
	got := run(60, func(i int) string { return fmt.Sprintf("198.51.101.%d", i+1) })
	if got[http.StatusUnauthorized] != emailFree-pairFree || got[http.StatusTooManyRequests] != 60-(emailFree-pairFree) {
		t.Fatalf("many addresses: %v", got)
	}
}

func (e *env) idOf(email string) string {
	e.t.Helper()
	var id string
	if err := e.a.DB.QueryRow(context.Background(), `SELECT id FROM "user" WHERE email = $1`, email).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestSigninRateLimitPerAddress(t *testing.T) {
	e := community(t)
	e.withPassword("user", "ann@example.com", pwAnn)
	// Spraying many emails from one address runs into the address lock, not the email locks.
	for i := range ipFree {
		if r := e.signinAs(fmt.Sprintf("user%d@example.com", i), pwEve, "198.51.100.7"); r.Code != http.StatusUnauthorized {
			t.Fatalf("try %d: %d %s", i+1, r.Code, r.Body)
		}
	}
	if r := e.signinAs("ann@example.com", pwAnn, "198.51.100.7"); r.Code != http.StatusTooManyRequests || r.Head.Get("Retry-After") == "" {
		t.Fatalf("after %d tries from one address: %d %s", ipFree, r.Code, r.Body)
	}
	// Another address is untouched, and so is a client that forges a header: the
	// server is on loopback here, so its X-Forwarded-For is believed, as a proxy's is.
	if r := e.signinAs("ann@example.com", pwAnn, "198.51.100.8"); r.Code != http.StatusOK {
		t.Fatalf("another address: %d %s", r.Code, r.Body)
	}
	// Good sign-ins from a busy address do not lock it.
	for range ipFree + 5 {
		if r := e.signinAs("ann@example.com", pwAnn, "198.51.100.9"); r.Code != http.StatusOK {
			t.Fatalf("a busy address: %d %s", r.Code, r.Body)
		}
	}
}

func TestChangePassword(t *testing.T) {
	e := community(t)
	ctx := context.Background()
	annID := e.withPassword("user", "ann@example.com", pwAnn)
	sessionOf := func() string {
		var out struct{ Token string }
		e.signinAs("ann@example.com", pwAnn, "").json(t, &out)
		return out.Token
	}
	a, b := sessionOf(), sessionOf() // two browsers
	change := func(cred string, body map[string]string) reply {
		return e.send("POST", "/api/v1/auths/update/password", cred, "", body)
	}

	if r := e.send("POST", "/api/v1/auths/update/password", "", "", map[string]string{"password": pwAnn, "new_password": pwNew}); r.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", r.Code)
	}
	for name, c := range map[string]struct {
		body  map[string]string
		field string
	}{
		"wrong current password": {map[string]string{"password": pwEve, "new_password": pwNew}, "password"},
		"too short":              {map[string]string{"password": pwAnn, "new_password": "short"}, "new_password"},
		"the email address":      {map[string]string{"password": pwAnn, "new_password": "ann@example.com"}, "new_password"},
		"too long":               {map[string]string{"password": pwAnn, "new_password": strings.Repeat("x", 2000)}, "new_password"},
		"the same password":      {map[string]string{"password": pwAnn, "new_password": pwAnn}, "new_password"},
	} {
		r := change(a, c.body)
		var f struct{ Field string }
		r.json(t, &f)
		if r.Code != http.StatusBadRequest || f.Field != c.field {
			t.Errorf("%s: %d %s, want 400 on field %q", name, r.Code, r.Body, c.field)
		}
	}
	// Session a is still fine after refused changes (a 400 must not sign the user out: the web app signs out on a 401).
	e.ok("GET", "/api/v1/auths/", a, nil, nil)

	r := change(a, map[string]string{"password": pwAnn, "new_password": pwNew})
	if r.Code != http.StatusOK {
		t.Fatalf("change: %d %s", r.Code, r.Body)
	}
	var fresh struct {
		Status    bool
		Token     string `json:"token"`
		TokenType string `json:"token_type"`
		ExpiresAt int64  `json:"expires_at"`
	}
	r.json(t, &fresh)
	if !fresh.Status || fresh.Token == "" || fresh.TokenType != "Bearer" || fresh.ExpiresAt == 0 || len(r.Resp.Cookies()) != 1 || r.Resp.Cookies()[0].Value != fresh.Token {
		t.Fatalf("change reply %s", r.Body)
	}
	e.ok("GET", "/api/v1/auths/", fresh.Token, nil, nil) // the session that changed it goes on
	e.status(http.StatusUnauthorized, "GET", "/api/v1/auths/", a, nil)
	e.status(http.StatusUnauthorized, "GET", "/api/v1/auths/", b, nil)
	// and the refreshed token of the surviving session keeps the epoch
	var again me
	e.ok("GET", "/api/v1/auths/", fresh.Token, nil, &again)
	e.ok("GET", "/api/v1/auths/", again.Token, nil, nil)

	if r := e.signinAs("ann@example.com", pwAnn, ""); r.Code != http.StatusUnauthorized {
		t.Fatalf("old password still works: %d", r.Code)
	}
	if r := e.signinAs("ann@example.com", pwNew, ""); r.Code != http.StatusOK {
		t.Fatalf("new password: %d %s", r.Code, r.Body)
	}

	// An API key cannot change a password, and a user without one is told to ask.
	if _, err := e.a.DB.Exec(ctx, `UPDATE "user" SET role = 'admin' WHERE id = $1`, annID); err != nil {
		t.Fatal(err)
	}
	_, key, err := CreateAPIKey(ctx, e.a.DB, annID, "k")
	if err != nil {
		t.Fatal(err)
	}
	if r := change(key, map[string]string{"password": pwNew, "new_password": pwAnn}); r.Code != http.StatusForbidden {
		t.Fatalf("api key: %d %s", r.Code, r.Body)
	}
	_, bob := e.mkUser("user", "bob@example.com")
	if r := change(bob, map[string]string{"password": pwBob, "new_password": pwAnn}); r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), "no_password") {
		t.Fatalf("user without a password: %d %s", r.Code, r.Body)
	}

	// Guessing the current password is limited like sign-in is.
	cur := fresh.Token
	for i := range changeFree {
		if r := change(cur, map[string]string{"password": pwEve, "new_password": pwAnn}); r.Code != http.StatusBadRequest {
			t.Fatalf("guess %d: %d %s", i+1, r.Code, r.Body)
		}
	}
	r = change(cur, map[string]string{"password": pwNew, "new_password": pwAnn})
	if r.Code != http.StatusTooManyRequests || r.Head.Get("Retry-After") == "" {
		t.Fatalf("after %d wrong guesses: %d %s", changeFree, r.Code, r.Body)
	}
}

func TestChangePasswordNeedsPasswordSignIn(t *testing.T) {
	e := community(t)
	e.withPassword("user", "ann@example.com", pwAnn)
	var s struct{ Token string }
	e.signinAs("ann@example.com", pwAnn, "").json(t, &s)
	if err := e.a.Settings.Set(context.Background(), "auth.password.enabled", false, false); err != nil {
		t.Fatal(err)
	}
	if r := e.send("POST", "/api/v1/auths/update/password", s.Token, "", map[string]string{"password": pwAnn, "new_password": pwNew}); r.Code != http.StatusForbidden {
		t.Fatalf("with password sign-in off: %d %s", r.Code, r.Body)
	}
	var m map[string]any
	e.ok("GET", "/api/v1/auths/", s.Token, nil, &m)
	if m["has_password"] != true || m["password_login"] != false {
		t.Fatalf("me says has_password=%v password_login=%v", m["has_password"], m["password_login"])
	}
}

func TestAdminSetPasswordAndAddUser(t *testing.T) {
	e := community(t)
	adminID, admin := e.mkUser("admin", "root@example.com")
	bobID := e.withPassword("user", "bob@example.com", pwBob)
	var bobSession struct{ Token string }
	e.signinAs("bob@example.com", pwBob, "").json(t, &bobSession)
	_, annTok := e.mkUser("user", "ann@example.com")

	set := func(cred, id, pw string) reply {
		return e.send("POST", "/api/v1/users/"+id+"/password", cred, "", map[string]string{"password": pw})
	}
	// Only admins.
	if r := set(annTok, bobID, pwNew); r.Code != http.StatusForbidden {
		t.Fatalf("a user setting another's password: %d %s", r.Code, r.Body)
	}
	if r := set(bobSession.Token, bobID, pwNew); r.Code != http.StatusForbidden {
		t.Fatalf("a user setting their own through the admin route: %d %s", r.Code, r.Body)
	}
	if r := set("", bobID, pwNew); r.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", r.Code)
	}
	if e.signinAs("bob@example.com", pwNew, "").Code == http.StatusOK {
		t.Fatal("a refused request changed the password")
	}
	// An admin cannot use it on themselves (that is Settings > Account, with the current password).
	if r := set(admin, adminID, pwNew); r.Code != http.StatusForbidden || !strings.Contains(string(r.Body), "own_password") {
		t.Fatalf("own password: %d %s", r.Code, r.Body)
	}
	if r := set(admin, "no-such-id", pwNew); r.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", r.Code)
	}
	for name, pw := range map[string]string{"short": "short", "email": "bob@example.com", "long": strings.Repeat("x", 3000)} {
		var f struct{ Field string }
		r := set(admin, bobID, pw)
		r.json(t, &f)
		if r.Code != http.StatusBadRequest || f.Field != "password" {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}

	r := set(admin, bobID, pwNew)
	if r.Code != http.StatusOK || strings.TrimSpace(string(r.Body)) != `{"status":true}` {
		t.Fatalf("set: %d %s", r.Code, r.Body)
	}
	e.status(http.StatusUnauthorized, "GET", "/api/v1/auths/", bobSession.Token, nil) // signed out by the reset
	if e.signinAs("bob@example.com", pwBob, "").Code != http.StatusUnauthorized || e.signinAs("bob@example.com", pwNew, "").Code != http.StatusOK {
		t.Fatal("the new password does not replace the old one")
	}

	// The admin list says who has a password, and never shows one.
	var list struct {
		Users []map[string]any `json:"users"`
	}
	e.ok("GET", "/api/v1/users/", admin, nil, &list)
	for _, u := range list.Users {
		if _, leak := u["hash"]; leak || u["has_password"] != (u["email"] == "bob@example.com") {
			t.Errorf("user row %v", u)
		}
	}
	if code, b := e.do("GET", "/api/v1/users/"+bobID, admin, nil); code != 200 || strings.Contains(string(b), "argon2") {
		t.Fatalf("get user: %d %s", code, b)
	}

	// Add user.
	add := func(cred string, body map[string]string) reply {
		return e.send("POST", "/api/v1/auths/add", cred, "", body)
	}
	if r := add(annTok, map[string]string{"email": "cy@example.com", "password": pwNew}); r.Code != http.StatusForbidden {
		t.Fatalf("a user adding a user: %d", r.Code)
	}
	r = add(admin, map[string]string{"email": " Cy@Example.com ", "name": "Cy Twombly", "role": "admin", "password": pwNew})
	var cy map[string]any
	r.json(t, &cy)
	if r.Code != http.StatusOK || cy["email"] != "cy@example.com" || cy["name"] != "Cy Twombly" || cy["role"] != "admin" || cy["has_password"] != true || strings.Contains(string(r.Body), "argon2") {
		t.Fatalf("add: %d %s", r.Code, r.Body)
	}
	if e.signinAs("cy@example.com", pwNew, "").Code != http.StatusOK {
		t.Fatal("the new user cannot sign in with the initial password")
	}
	if r := add(admin, map[string]string{"email": "CY@example.com", "password": pwNew}); r.Code != http.StatusConflict {
		t.Fatalf("duplicate email: %d %s", r.Code, r.Body)
	}
	for name, body := range map[string]map[string]string{
		"no email":       {"password": pwNew},
		"not an email":   {"email": "cy", "password": pwNew},
		"display name":   {"email": "Cy <cy2@example.com>", "password": pwNew},
		"two addresses":  {"email": "a@example.com, b@example.com", "password": pwNew},
		"bad role":       {"email": "d@example.com", "role": "owner", "password": pwNew},
		"weak password":  {"email": "e@example.com", "password": "short"},
		"password=email": {"email": "f@example.com", "password": "f@example.com"},
	} {
		if r := add(admin, body); r.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	// No password: a user who can only sign in through SSO; the form refuses them like any stranger.
	r = add(admin, map[string]string{"email": "sso@example.com"})
	var sso map[string]any
	r.json(t, &sso)
	if r.Code != http.StatusOK || sso["role"] != "user" || sso["name"] != "sso" || sso["has_password"] != false {
		t.Fatalf("add without a password: %d %s", r.Code, r.Body)
	}
	if e.signinAs("sso@example.com", pwNew, "").Code != http.StatusUnauthorized {
		t.Fatal("a user without a password signed in")
	}
}

// Nothing a request or a log line shows is a password or a hash.
func TestNothingSecretIsLogged(t *testing.T) {
	l := captureLogs(t)
	e := community(t)
	_, admin := e.mkUser("admin", "root@example.com")
	var all bytes.Buffer
	see := func(r reply) reply { all.Write(r.Body); all.WriteString(fmt.Sprint(r.Head)); return r }

	see(e.send("POST", "/api/v1/auths/add", admin, "", map[string]string{"email": "ann@example.com", "password": pwAnn}))
	for range pairFree + 1 {
		see(e.signinAs("ann@example.com", pwEve, ""))
	}
	see(e.signinAs("nobody@example.com", pwEve, "203.0.113.5"))
	annID := e.idOf("ann@example.com")
	see(e.send("POST", "/api/v1/users/"+annID+"/password", admin, "", map[string]string{"password": pwNew}))
	var s struct{ Token string }
	see(e.signinAs("ann@example.com", pwNew, "")).json(t, &s)
	see(e.send("POST", "/api/v1/auths/update/password", s.Token, "", map[string]string{"password": pwEve, "new_password": pwAnn}))
	see(e.send("POST", "/api/v1/auths/update/password", s.Token, "", map[string]string{"password": pwNew, "new_password": pwAnn}))
	see(e.send("GET", "/api/v1/users/"+annID, admin, "", nil))

	t.Logf("log output:\n%s", l.String())
	if !strings.Contains(l.String(), "password sign-in failed") || !strings.Contains(l.String(), "nobody@example.com") || !strings.Contains(l.String(), "203.0.113.5") {
		t.Fatal("failed attempts are not logged with email and address")
	}
	for _, secret := range []string{pwAnn, pwNew, pwEve, "argon2"} {
		if strings.Contains(l.String(), secret) {
			t.Errorf("the log contains %q", secret)
		}
		if strings.Contains(all.String(), secret) {
			t.Errorf("a response contains %q", secret)
		}
	}
}

// staleHash is a valid argon2id hash of password made with one lane instead of two.
func staleHash(t *testing.T, password string) string {
	t.Helper()
	salt := []byte("0123456789abcdef")
	sum := argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum))
}

// signinWith posts a sign-in with arbitrary request headers (a proxy's, or a client's own).
func (e *env) signinWith(email, password string, hdr map[string]string) reply {
	e.t.Helper()
	b, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/auths/signin", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := noFollow().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, body, resp.Header, resp}
}

// Behind Caddy, nginx or Traefik on this host, a client's own CF-Connecting-IP is passed through
// untouched. It must not choose the address the limiter counts: the proxy's X-Forwarded-For does.
func TestSigninAddressIgnoresClientSentCFHeader(t *testing.T) {
	e := community(t)
	e.withPassword("user", "ann@example.com", pwAnn)

	// Spraying emails with a new forged CF-Connecting-IP per try still meets the address limit.
	var refused int
	for i := range ipFree + 5 {
		r := e.signinWith(fmt.Sprintf("spray%d@example.com", i), pwEve, map[string]string{
			"X-Forwarded-For": "203.0.113.5", "CF-Connecting-IP": fmt.Sprintf("198.51.100.%d", i+1)})
		if r.Code == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused != 5 {
		t.Fatalf("%d of %d tries were refused, want the last 5: a forged CF-Connecting-IP chose the address", refused, ipFree+5)
	}

	// Nor can it aim a lock at someone else's address: the forger burns its own.
	for i := range ipFree + 2 {
		e.signinWith(fmt.Sprintf("aim%d@example.com", i), pwEve, map[string]string{
			"X-Forwarded-For": "203.0.113.7", "CF-Connecting-IP": "192.0.2.50"})
	}
	if r := e.signinWith("ann@example.com", pwAnn, map[string]string{"X-Forwarded-For": "192.0.2.50"}); r.Code != http.StatusOK {
		t.Fatalf("a bystander at the address the forger named: %d %s", r.Code, r.Body)
	}
}

// An admin's API key can list and manage things, but it cannot set a password or make an
// admin: a leaked key would otherwise become a way to sign in as anyone.
func TestAPIKeyCannotSetPasswordsOrAddAdmins(t *testing.T) {
	e := community(t)
	_, admin := e.mkUser("admin", "root@example.com")
	annID := e.withPassword("user", "ann@example.com", pwAnn)
	var key struct{ Key string }
	e.ok("POST", "/api/v1/auths/api_keys", admin, map[string]string{"name": "ci"}, &key)
	e.ok("GET", "/api/v1/users/", key.Key, nil, nil) // the key is a working admin credential

	refused := func(what string, r reply) {
		t.Helper()
		if r.Code != http.StatusForbidden || !strings.Contains(string(r.Body), "session_required") {
			t.Errorf("%s with an API key: %d %s", what, r.Code, r.Body)
		}
	}
	refused("set a user's password", e.send("POST", "/api/v1/users/"+annID+"/password", key.Key, "", map[string]string{"password": "attacker-chosen-pw"}))
	if e.signinAs("ann@example.com", "attacker-chosen-pw", "").Code == http.StatusOK || e.signinAs("ann@example.com", pwAnn, "").Code != http.StatusOK {
		t.Fatal("the refused request changed Ann's password")
	}
	refused("add a user with a password", e.send("POST", "/api/v1/auths/add", key.Key, "", map[string]string{"email": "evil@example.com", "password": "attacker-chosen-pw"}))
	refused("add an admin", e.send("POST", "/api/v1/auths/add", key.Key, "", map[string]string{"email": "evil@example.com", "role": "admin"}))
	refused("change a password", e.send("POST", "/api/v1/auths/update/password", key.Key, "", map[string]string{"password": pwAnn, "new_password": "yyyyyyyyyyyy"}))
	if e.count(`SELECT count(*) FROM "user" WHERE email = 'evil@example.com'`) != 0 {
		t.Fatal("a refused add left a user behind")
	}
	// A plain user without a password (for SSO) is still fine through a key; the same call with a session works too.
	if r := e.send("POST", "/api/v1/auths/add", key.Key, "", map[string]string{"email": "sso@example.com"}); r.Code != http.StatusOK {
		t.Fatalf("add a user without a password through a key: %d %s", r.Code, r.Body)
	}
	if r := e.send("POST", "/api/v1/users/"+annID+"/password", admin, "", map[string]string{"password": pwNew}); r.Code != http.StatusOK {
		t.Fatalf("set password with a session: %d %s", r.Code, r.Body)
	}
}

// An SSO deployment whose login provider is removed later does not get a password form: saving the
// provider pinned the setting (settings.PinPasswordSignInOff, which the admin API and kera apply call).
// A fresh install with no provider still has the form on (TestSigninDefaultRule).
func TestRemovingTheLoginProviderLeavesPasswordSignInOff(t *testing.T) {
	e := setup(t) // an OAuth login provider
	ctx := context.Background()
	e.withPassword("user", "ann@example.com", pwAnn)
	if err := settings.PinPasswordSignInOff(ctx, e.a.DB); err != nil {
		t.Fatal(err)
	}
	e.a.Settings.Invalidate()
	if _, err := e.a.DB.Exec(ctx, `DELETE FROM oauth_provider`); err != nil {
		t.Fatal(err)
	}
	var out struct {
		LoginForm bool `json:"login_form"`
	}
	e.ok("GET", "/api/v1/auths/providers", "", nil, &out)
	if out.LoginForm {
		t.Fatal("the password form switched itself on when the provider was removed")
	}
	if r := e.signinAs("ann@example.com", pwAnn, ""); r.Code != http.StatusForbidden {
		t.Fatalf("sign-in after the provider was removed: %d %s", r.Code, r.Body)
	}
}

// Someone who types their password into the email box must not find it in the server log: only
// strings that pass the plain-address check are logged, others become a placeholder, in the
// failure line and in the lock line the limiter writes.
func TestPasswordTypedInTheEmailBoxIsNotLogged(t *testing.T) {
	l := captureLogs(t)
	e := community(t)
	e.withPassword("user", "ann@example.com", pwAnn)
	const typed = "my-very-secret-pass1"
	for range pairFree + 1 { // the last one is refused by the lock it set
		e.signinAs(typed, "whatever-else-1", "203.0.113.9")
	}
	e.signinAs(" "+strings.ToUpper(typed)+" ", "whatever-else-1", "203.0.113.9")
	e.signinAs("Ann@Example.com", pwEve, "203.0.113.10") // a real address is still named, lower-cased
	got := l.String()
	if strings.Contains(strings.ToLower(got), typed) {
		t.Fatalf("the log contains what was typed into the email box:\n%s", got)
	}
	for _, want := range []string{notAnEmail, `"msg":"sign-in locked"`, "203.0.113.9", `"email":"ann@example.com"`} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
}

func TestLoggable(t *testing.T) {
	for in, want := range map[string]string{
		"ann@example.com":      "ann@example.com",
		"hunter2hunter2":       notAnEmail,
		"my password is x@y z": notAnEmail,
		"":                     notAnEmail,
		"a@":                   notAnEmail,
		"Ann <ann@x.com>":      notAnEmail,
	} {
		if got := loggable(in); got != want {
			t.Errorf("loggable(%q) = %q, want %q", in, got, want)
		}
	}
}
