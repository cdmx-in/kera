package gworkspace

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/connectors"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

func wsUser(id, email, name, dept string, suspended bool) User {
	u := User{ID: id, PrimaryEmail: email, Suspended: suspended}
	u.Name.FullName = name
	if dept != "" {
		u.Organizations = append(u.Organizations, struct {
			Department string `json:"department"`
			Primary    bool   `json:"primary"`
		}{dept, true})
	}
	return u
}

type fixture struct {
	t    *testing.T
	db   *pgxpool.Pool
	opt  Options
	ids  map[string]string // email -> user id
	auth *auth.Authenticator
}

func newFixture(t *testing.T) *fixture {
	pool := testdb.New(t)
	keys, _ := secret.Derive(strings.Repeat("k", 32))
	f := &fixture{t: t, db: pool, ids: map[string]string{},
		opt:  Options{Provider: "google", DefaultRole: "user", DeleteAfter: 30 * 24 * time.Hour, Now: time.Now(), Exclude: map[string]bool{"bot@ex.com": true}},
		auth: auth.New(pool, keys, settings.New(pool), false)}
	return f
}

func (f *fixture) mkUser(email, role, sub string) string {
	f.t.Helper()
	id := ids.New()
	var s *string
	if sub != "" {
		s = &sub
	}
	f.exec(`INSERT INTO "user" (id, name, email, role, oauth_sub) VALUES ($1, $2, $3, $4, $5)`, id, email, email, role, s)
	f.ids[email] = id
	return id
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(context.Background(), q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

// sync runs one committed sync and returns its report.
func (f *fixture) sync(users ...User) (*connectors.Report, error) {
	f.t.Helper()
	ctx := context.Background()
	tx, err := f.db.Begin(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	rep := connectors.NewReport()
	if err := Sync(ctx, tx, users, f.opt, rep); err != nil {
		return rep, err
	}
	return rep, tx.Commit(ctx)
}

func (f *fixture) mustSync(users ...User) *connectors.Report {
	f.t.Helper()
	rep, err := f.sync(users...)
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func (f *fixture) scalar(q string, args ...any) string {
	f.t.Helper()
	var s string
	if err := f.db.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return s
}

func (f *fixture) disabled(email string) bool {
	return f.scalar(`SELECT (disabled_at IS NOT NULL)::text FROM "user" WHERE lower(email) = $1`, email) == "true"
}

func (f *fixture) groups(email string) string {
	return f.scalar(`SELECT coalesce(string_agg(g.name || ':' || m.origin, ',' ORDER BY g.name), '') FROM group_member m
		JOIN "group" g ON g.id = m.group_id JOIN "user" u ON u.id = m.user_id WHERE lower(u.email) = $1`, email)
}

func has(rep *connectors.Report, action, target string) bool {
	for _, c := range rep.Changes {
		if c.Action == action && c.Target == target {
			return true
		}
	}
	return false
}

func TestSyncLifecycle(t *testing.T) {
	f := newFixture(t)
	f.mkUser("boss@ex.com", "admin", "")
	f.mkUser("ann@ex.com", "admin", "google@ann")
	f.mkUser("bot@ex.com", "user", "")
	f.mkUser("old@ex.com", "user", "google@old")
	f.mkUser("guest@other.com", "user", "")
	f.mkUser("imp@ex.com", "user", "imp") // bare sub, as the Open WebUI import stored it
	f.exec(`INSERT INTO "group" (id, name) VALUES ('sales', 'Sales')`)
	f.exec(`INSERT INTO group_member (group_id, user_id) VALUES ('sales', $1)`, f.ids["boss@ex.com"])

	boss := wsUser("b", "Boss@ex.com", "Big Boss", "Engineering", false)
	ann := wsUser("ann", "ann@ex.com", "Ann", "", false)
	amy := wsUser("a", "amy@ex.com", "Amy Adams", "Engineering", false)
	sue := wsUser("s", "sue@ex.com", "Sue", "Engineering", true)
	imp := wsUser("imp", "imp@ex.com", "imp@ex.com", "Engineering", false)

	// Run 1: link by email, create, skip suspended newcomers, departments, missing user disabled.
	rep := f.mustSync(boss, ann, amy, sue, imp)
	if !has(rep, "link", "imp@ex.com") || f.scalar(`SELECT oauth_sub FROM "user" WHERE email = 'imp@ex.com'`) != "google@imp" || f.groups("imp@ex.com") != "Engineering:gworkspace" {
		t.Errorf("bare imported sub not linked: %+v %v", rep.Changes, rep.Warnings)
	}
	for _, want := range [][2]string{{"link", "boss@ex.com"}, {"create", "amy@ex.com"}, {"group", "Engineering"},
		{"join", "boss@ex.com"}, {"join", "amy@ex.com"}, {"disable", "old@ex.com"}, {"update", "boss@ex.com"}} {
		if !has(rep, want[0], want[1]) {
			t.Errorf("run 1: no %s %s in %+v", want[0], want[1], rep.Changes)
		}
	}
	if got := f.scalar(`SELECT count(*)::text FROM "user" WHERE email = 'sue@ex.com'`); got != "0" {
		t.Errorf("suspended newcomer created")
	}
	if got := f.scalar(`SELECT role || ' ' || origin || ' ' || oauth_sub || ' ' || name FROM "user" WHERE email = 'amy@ex.com'`); got != "user gworkspace google@a Amy Adams" {
		t.Errorf("amy = %q", got)
	}
	if got := f.groups("boss@ex.com"); got != "Engineering:gworkspace,Sales:kera" {
		t.Errorf("boss groups = %q", got)
	}
	if !f.disabled("old@ex.com") || f.disabled("bot@ex.com") || f.disabled("guest@other.com") || f.disabled("boss@ex.com") {
		t.Errorf("disabled flags wrong")
	}
	if _, err := f.auth.LoadUser(context.Background(), f.ids["old@ex.com"]); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("disabled user loads: %v", err)
	}

	// Run 2: same input changes nothing.
	if rep := f.mustSync(boss, ann, amy, sue, imp); len(rep.Changes) != 0 {
		t.Errorf("run 2 not idempotent: %+v", rep.Changes)
	}

	// Run 3: amy suspended and moved to Sales (existing group reused); manual Sales membership of boss kept.
	amy2 := wsUser("a", "amy@ex.com", "Amy Adams", "Sales", true)
	boss2 := wsUser("b", "boss@ex.com", "Big Boss", "", false)
	rep = f.mustSync(boss2, ann, amy2)
	if !f.disabled("amy@ex.com") || !has(rep, "disable", "amy@ex.com") {
		t.Errorf("amy not disabled: %+v", rep.Changes)
	}
	if got := f.groups("amy@ex.com"); got != "Sales:gworkspace" {
		t.Errorf("amy groups = %q", got)
	}
	if got := f.groups("boss@ex.com"); got != "Sales:kera" {
		t.Errorf("boss groups after leaving Engineering = %q", got)
	}
	if got := f.scalar(`SELECT count(*)::text FROM "group" WHERE name = 'Sales'`); got != "1" {
		t.Errorf("Sales duplicated")
	}

	// Run 4: amy active again -> enabled; old reappears, suspended -> stays disabled, countdown cleared.
	amy3 := wsUser("a", "amy@ex.com", "Amy Adams", "Sales", false)
	old := wsUser("old", "old@ex.com", "Old", "", true)
	rep = f.mustSync(boss2, ann, amy3, old)
	if f.disabled("amy@ex.com") || !has(rep, "enable", "amy@ex.com") {
		t.Errorf("amy not enabled: %+v", rep.Changes)
	}
	if !f.disabled("old@ex.com") || f.scalar(`SELECT info::text FROM "user" WHERE email = 'old@ex.com'`) != "{}" {
		t.Errorf("old: countdown not cleared")
	}

	// Run 5: old gone again; 31 days later it is deleted. Email renames follow the directory id.
	f.mustSync(boss2, ann, amy3)
	f.opt.Now = f.opt.Now.Add(31 * 24 * time.Hour)
	amy4 := wsUser("a", "amy.adams@ex.com", "Amy Adams", "Sales", false)
	rep = f.mustSync(boss2, ann, amy4)
	if !has(rep, "delete", "old@ex.com") || f.scalar(`SELECT count(*)::text FROM "user" WHERE email = 'old@ex.com'`) != "0" {
		t.Errorf("old not deleted: %+v", rep.Changes)
	}
	if f.scalar(`SELECT email FROM "user" WHERE oauth_sub = 'google@a'`) != "amy.adams@ex.com" {
		t.Errorf("amy email not renamed: %+v", rep.Changes)
	}
}

func TestSyncGuards(t *testing.T) {
	f := newFixture(t)
	f.mkUser("boss@ex.com", "admin", "google@b")
	for i := range 7 {
		f.mkUser(string(rune('a'+i))+"@ex.com", "user", "")
	}
	if _, err := f.sync(); err == nil {
		t.Fatal("empty listing accepted")
	}
	// 7 of 8 users missing from a partial listing: above max(5, 10%), nothing changes.
	if _, err := f.sync(wsUser("b", "boss@ex.com", "Boss", "", false)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("safety cap: %v", err)
	}
	if f.disabled("a@ex.com") {
		t.Fatal("cap run changed data")
	}
	// The only admin is never disabled, even when suspended.
	rep, err := f.sync(wsUser("b", "boss@ex.com", "Boss", "", true), wsUser("x", "a@ex.com", "A", "", false),
		wsUser("y", "b@ex.com", "B", "", false), wsUser("z", "c@ex.com", "C", "", false))
	if err != nil {
		t.Fatal(err)
	}
	if f.disabled("boss@ex.com") || len(rep.Warnings) == 0 {
		t.Errorf("last admin disabled; warnings %v", rep.Warnings)
	}
}

// A user past the delete countdown who still owns a pipeline is kept, disabled,
// with a warning; the rest of the run (other users leaving, new hires) goes through.
// DeleteUser would answer 409 and the failed statement would roll the whole run back.
func TestSyncKeepsAPipelineOwner(t *testing.T) {
	f := newFixture(t)
	f.mkUser("boss@ex.com", "admin", "")
	owner := f.mkUser("owner@ex.com", "user", "google@owner")
	f.mkUser("leaver@ex.com", "user", "google@leaver")
	f.exec(`INSERT INTO pipeline (id, name, user_id) VALUES ('p1', 'docs-sync', $1)`, owner)
	boss := wsUser("b", "boss@ex.com", "Boss", "", false)
	leaver := wsUser("l", "leaver@ex.com", "Leaver", "", false)

	f.mustSync(boss, leaver) // the owner is missing: disabled, the countdown starts
	if !f.disabled("owner@ex.com") {
		t.Fatal("the missing owner was not disabled")
	}
	f.opt.Now = f.opt.Now.Add(31 * 24 * time.Hour)
	rep, err := f.sync(boss, wsUser("n", "newbie@ex.com", "Newbie", "", false)) // the countdown is over; leaver just left
	if err != nil {
		t.Fatalf("a pipeline owner must not fail the run: %v", err)
	}
	if f.scalar(`SELECT count(*)::text FROM "user" WHERE email = 'owner@ex.com'`) != "1" || !f.disabled("owner@ex.com") {
		t.Error("the owner was deleted or enabled")
	}
	if !f.disabled("leaver@ex.com") || f.scalar(`SELECT count(*)::text FROM "user" WHERE email = 'newbie@ex.com'`) != "1" {
		t.Errorf("the rest of the run was rolled back: %+v", rep.Changes)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "owner@ex.com owns a pipeline") {
		t.Errorf("warnings %v", rep.Warnings)
	}
	// once the owner changes, the next run deletes the user
	f.exec(`UPDATE pipeline SET user_id = $1`, f.ids["boss@ex.com"])
	if rep = f.mustSync(boss); !has(rep, "delete", "owner@ex.com") {
		t.Errorf("owner not deleted after the pipeline changed hands: %+v", rep.Changes)
	}
}

// TestRun drives the connector end to end: settings, service-account token,
// paged directory listing, dry run, then a real run.
func TestRun(t *testing.T) {
	pool := testdb.New(t)
	keys, _ := secret.Derive(strings.Repeat("k", 32))
	st := settings.New(pool)
	a := &app.App{DB: pool, Keys: keys, Settings: st}
	ctx := context.Background()

	pk, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(pk)
	var gotSubject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			parts := strings.Split(r.Form.Get("assertion"), ".")
			if len(parts) == 3 {
				claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
				var c struct{ Sub string }
				_ = json.Unmarshal(claims, &c)
				gotSubject = c.Sub
			}
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
		case "/users":
			if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("customer") != "my_customer" {
				http.Error(w, "denied", http.StatusForbidden)
				return
			}
			if r.URL.Query().Get("pageToken") == "" {
				_, _ = w.Write([]byte(`{"users":[{"id":"1","primaryEmail":"one@ex.com","name":{"fullName":"One"},"organizations":[{"department":"Ops"}]}],"nextPageToken":"p2"}`))
				return
			}
			_, _ = w.Write([]byte(`{"users":[{"id":"2","primaryEmail":"two@ex.com","name":{"fullName":"Two"}}]}`))
		}
	}))
	defer srv.Close()
	directoryURL = srv.URL + "/users"

	key, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "sa@proj.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": srv.URL + "/token"})
	sealed, err := apply.SealSecret(keys, string(key))
	if err != nil {
		t.Fatal(err)
	}
	c := connectors.Connector{Name: Name, Title: "Google Workspace", Run: run}
	if _, err := connectors.Run(ctx, a, c, "cli", true); err == nil {
		t.Fatal("ran without credentials")
	}
	for k, v := range map[string]any{"connectors.google_workspace.credentials": sealed, "connectors.google_workspace.admin_email": "admin@ex.com"} {
		if err := st.Set(ctx, k, v, false); err != nil {
			t.Fatal(err)
		}
	}

	// The service account's own address is the classic mistake (Google answers 400 Invalid Input).
	_ = st.Set(ctx, "connectors.google_workspace.admin_email", "sa@proj.iam.gserviceaccount.com", false)
	if _, err := connectors.Run(ctx, a, c, "cli", true); err == nil || !strings.Contains(err.Error(), "not the service account") {
		t.Errorf("service-account admin email: %v", err)
	}
	_ = st.Set(ctx, "connectors.google_workspace.admin_email", "admin@ex.com", false)
	if err := directoryError(400, []byte(`{"error":{"code":400,"message":"Invalid Input"}}`)); !strings.Contains(err.Error(), "400 Invalid Input. Admin to act as") {
		t.Errorf("directoryError = %v", err)
	}

	rep, err := connectors.Run(ctx, a, c, "cli", true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts["create"] != 2 || rep.Counts["join"] != 1 {
		t.Errorf("dry run report %+v", rep.Counts)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM "user"`).Scan(&n)
	if n != 0 {
		t.Fatalf("dry run wrote %d users", n)
	}
	if gotSubject != "admin@ex.com" {
		t.Errorf("JWT impersonates %q", gotSubject)
	}

	if _, err := connectors.Run(ctx, a, c, "cli", false); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE origin = 'gworkspace'`).Scan(&n)
	if n != 2 {
		t.Errorf("real run created %d users", n)
	}
	// the dry runs recorded nothing; the real one recorded both accounts with the integration as the actor
	var events, other int
	var leaks bool
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action = 'user.create' AND actor_kind = 'service' AND actor_label = 'Google Workspace'
			AND actor_id IS NULL AND target_type = 'user' AND meta->>'integration' = 'google_workspace' AND target_label IN ('one@ex.com', 'two@ex.com')),
		count(*) FILTER (WHERE action <> 'user.create'),
		coalesce(bool_or(row_to_json(a)::text ~* 'PRIVATE KEY|Bearer|assertion|client_email'), false) FROM audit_event a`).Scan(&events, &other, &leaks); err != nil {
		t.Fatal(err)
	}
	if events != 2 || other != 0 || leaks {
		t.Errorf("audit: %d user.create events by the integration, %d other events, credentials in a row: %v", events, other, leaks)
	}
	var runs, failed int
	_ = pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE error <> '') FROM connector_run WHERE finished_at IS NOT NULL`).Scan(&runs, &failed)
	if runs != 4 || failed != 2 {
		t.Errorf("connector_run rows: %d finished, %d failed", runs, failed)
	}
}
