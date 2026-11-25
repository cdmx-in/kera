package identity

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// auditRows lists the audit events as "action outcome actor_kind target_label meta", oldest first, and fails
// the test if any row, in any column, contains one of the secrets.
func (e *env) auditRows(secrets ...string) []string {
	e.t.Helper()
	rows, err := e.a.DB.Query(context.Background(), `SELECT action || ' ' || outcome || ' ' || actor_kind || ' ' || target_label || ' ' || meta::text,
		row_to_json(a)::text FROM audit_event a ORDER BY at, id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var short, whole string
		if err := rows.Scan(&short, &whole); err != nil {
			e.t.Fatal(err)
		}
		for _, s := range secrets {
			if strings.Contains(strings.ToLower(whole), strings.ToLower(s)) {
				e.t.Fatalf("an audit row contains %q: %s", s, whole)
			}
		}
		out = append(out, short)
	}
	return out
}

func TestAuditSignIn(t *testing.T) {
	e := community(t)
	annID := e.withPassword("user", "ann@example.com", pwAnn)
	typed := "my-very-secret-pass1" // a password typed into the email box

	e.signinAs("ann@example.com", pwEve, "203.0.113.9")
	e.signinAs(typed, pwEve, "203.0.113.9")
	var s struct{ Token string }
	e.signinAs("ann@example.com", pwAnn, "203.0.113.9").json(t, &s)
	e.send("POST", "/api/v1/auths/signout", s.Token, "", nil)
	e.send("POST", "/api/v1/auths/signout", "", "", nil) // nobody signed in: nothing to record

	got := e.auditRows(pwAnn, pwEve, typed)
	want := []string{
		`auth.sign_in_failed denied anonymous ann@example.com {"method": "password", "reason": "wrong password"}`,
		`auth.sign_in_failed denied anonymous ` + fingerprint(typed) + ` {"method": "password", "reason": "no such user"}`,
		`auth.sign_in ok user  {"method": "password"}`,
		`auth.sign_out ok user  {}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var target, ip, actor string
	if err := e.a.DB.QueryRow(context.Background(), `SELECT (SELECT target_id FROM audit_event WHERE action = 'auth.sign_in_failed' ORDER BY at LIMIT 1),
		(SELECT ip FROM audit_event WHERE action = 'auth.sign_in'), (SELECT actor_id FROM audit_event WHERE action = 'auth.sign_in')`).Scan(&target, &ip, &actor); err != nil {
		t.Fatal(err)
	}
	if target != annID || actor != annID || ip != "203.0.113.9" {
		t.Fatalf("target %q, actor %q, ip %q", target, actor, ip)
	}
}

// Tries refused by the sign-in lock (429) leave no event, so guessing cannot fill the log.
func TestAuditLockedSignInsAreNotRecorded(t *testing.T) {
	e := community(t)
	for range pairFree + 10 {
		e.signinAs("nobody@example.com", pwEve, "198.51.100.7")
	}
	if got := len(e.auditRows(pwEve)); got != pairFree {
		t.Fatalf("%d failed sign-ins recorded, want the %d the lock let through", got, pairFree)
	}
}

func TestAuditUserAdmin(t *testing.T) {
	e := setup(t)
	_, admin := e.mkUser("admin", "root@example.com")
	patID, _ := e.mkUser("user", "pat@example.com")
	e.ok("POST", "/api/v1/users/"+patID+"/update", admin, map[string]any{"role": "admin"}, nil)
	e.ok("POST", "/api/v1/users/"+patID+"/update", admin, map[string]any{"name": "Pat Q"}, nil)
	e.ok("POST", "/api/v1/users/"+patID+"/password", admin, map[string]any{"password": pwNew}, nil)
	e.ok("POST", "/api/v1/auths/add", admin, map[string]any{"email": "kim@example.com", "password": pwBob}, nil)
	var g struct{ ID string }
	e.ok("POST", "/api/v1/groups/create", admin, map[string]any{"name": "Finance"}, &g)
	e.ok("POST", "/api/v1/groups/id/"+g.ID+"/users/add", admin, map[string]any{"user_ids": []string{patID}}, nil)
	e.ok("DELETE", "/api/v1/groups/id/"+g.ID+"/delete", admin, nil, nil)
	var key struct{ ID, Key string }
	e.ok("POST", "/api/v1/auths/api_keys", admin, map[string]any{"name": "ci"}, &key)
	e.ok("DELETE", "/api/v1/auths/api_keys/"+key.ID, admin, nil, nil)
	e.ok("DELETE", "/api/v1/users/"+patID, admin, nil, nil)
	e.status(http.StatusUnauthorized, "DELETE", "/api/v1/users/"+patID, "", nil) // nobody signed in: refused before the audit hook
	got := e.auditRows(pwNew, pwBob, key.Key)
	want := []string{
		`user.role_change ok user pat@example.com {"to": "admin", "from": "user"}`,
		`user.rename ok user pat@example.com {"name_changed": true}`,
		`user.password_set ok user pat@example.com {"api_keys_revoked": false}`,
		`user.create ok user kim@example.com {"role": "user", "password_set": true}`,
		`group.create ok user Finance {}`,
		`group.member_add ok user Finance {"users": ["` + patID + `"]}`,
		`group.delete ok user Finance {}`,
		`apikey.create ok user ci {}`,
		`apikey.revoke ok user ci {}`,
		`user.delete ok user pat@example.com {}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The command line records as the "Kera command line" actor (kind system, no user id): user add, passwd,
// promote and apikey create, with names and flags only. The password and the key never reach the log.
func TestAuditCommandLine(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var out, warn bytes.Buffer
	user := func(stdin string, args ...string) {
		t.Helper()
		if err := UserCmd(ctx, e.a.DB, args, strings.NewReader(stdin), &out); err != nil {
			t.Fatalf("kera user %v: %v", args, err)
		}
	}
	user(pwAnn+"\n", "add", "--password-stdin", "Ann@Example.com")
	user("", "add", "--admin", "root@example.com")
	user(pwNew+"\n", "passwd", "--stdin", "--revoke-api-keys", "ann@example.com")
	user("", "promote", "ANN@example.com")
	out.Reset()
	if err := APIKeyCmd(ctx, e.a.DB, []string{"create", "--user", "ann@example.com", "--name", "ci"}, &out, &warn); err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(out.String())

	got := e.auditRows(pwAnn, pwNew, key)
	want := []string{
		`user.create ok system ann@example.com {"role": "user", "password_set": true}`,
		`user.create ok system root@example.com {"role": "admin", "password_set": false}`,
		`user.password_set ok system ann@example.com {"api_keys_revoked": true}`,
		`user.role_change ok system ann@example.com {"to": "admin"}`,
		`apikey.create ok system ci {"owner": "ann@example.com"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var n int
	var annID, createdFor string
	if err := e.a.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE actor_kind = 'system' AND actor_label = 'Kera command line' AND actor_id IS NULL),
		(SELECT id FROM "user" WHERE email = 'ann@example.com'),
		(SELECT target_id FROM audit_event WHERE action = 'user.create' ORDER BY at LIMIT 1) FROM audit_event`).Scan(&n, &annID, &createdFor); err != nil {
		t.Fatal(err)
	}
	if n != len(want) || createdFor != annID {
		t.Fatalf("%d of %d events by the command line actor; user.create target %q, user %q", n, len(want), createdFor, annID)
	}
}

// A sign-in with a provider records auth.sign_in (and user.create for a new account) as the user, and a
// refused or failed one as auth.sign_in_failed with an anonymous actor. No code, token or client secret is stored.
func TestAuditOAuthSignIn(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok, _ := e.tokenFrom(e.signIn(ident{Sub: "a1", Email: "Ann@Example.com", Name: "Ann", Verified: true}, ""))
	e.tokenFrom(e.signIn(ident{Sub: "a1", Email: "ann@example.com", Verified: true}, "")) // a returning user: no user.create
	e.signIn(ident{Sub: "d1", Email: "Dan@Example.com", Verified: false}, "")             // refused by the provider's answer
	_ = e.a.Settings.Set(ctx, "auth.oauth.allowed_domains", []string{"example.com"}, false)
	e.signIn(ident{Sub: "e1", Email: "eve@other.org", Verified: true}, "") // refused by the domain list

	// a code the provider does not know: the exchange fails (outcome failed, not denied)
	c := noFollow()
	resp, err := c.Get(e.srv.URL + "/oauth/google/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	resp, err = c.Get(e.srv.URL + "/oauth/google/callback?code=unknown-code-123&state=" + url.QueryEscape(loc.Query().Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// no login flow cookie at all
	resp, err = noFollow().Get(e.srv.URL + "/oauth/google/callback?code=unknown-code-456&state=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got := e.auditRows(tok, "s3cret", "unknown-code-123", "unknown-code-456")
	want := []string{
		`user.create ok user ann@example.com {"role": "admin", "method": "google"}`,
		`auth.sign_in ok user  {"method": "google"}`,
		`auth.sign_in ok user  {"method": "google"}`,
		`auth.sign_in_failed denied anonymous Dan@Example.com {"method": "google", "reason": "email_unverified"}`,
		`auth.sign_in_failed denied anonymous eve@other.org {"method": "google", "reason": "domain_not_allowed"}`,
		`auth.sign_in_failed failed anonymous  {"method": "google", "reason": "exchange_failed"}`,
		`auth.sign_in_failed denied anonymous  {"method": "google", "reason": "invalid_state"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var annID string
	var actors int
	if err := e.a.DB.QueryRow(ctx, `SELECT id, (SELECT count(*) FROM audit_event WHERE actor_id = u.id AND actor_label = 'ann@example.com'
		AND action IN ('auth.sign_in', 'user.create')) FROM "user" u WHERE email = 'ann@example.com'`).Scan(&annID, &actors); err != nil || actors != 3 {
		t.Fatalf("events with the user as actor: %d (%v)", actors, err)
	}
}
