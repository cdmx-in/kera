package audit

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

type tenv struct {
	t                  *testing.T
	a                  *app.App
	url                string
	adminID, userID    string
	admin, user, akey  string // session tokens and an admin API key
	adminMail, usrMail string
}

// setup mounts audit with a few routes of its own: an admin route the catalogue does not know, one it does,
// and a user route that records twice.
func setup(t *testing.T) *tenv {
	t.Helper()
	pool := testdb.New(t)
	keys, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Env{PublicURL: "http://kera.example.com"}
	srv, err := api.New(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(pool)
	a := &app.App{Env: cfg, DB: pool, Keys: keys, Settings: st, Server: srv, Auth: auth.New(pool, keys, st, false)}
	if err := Mount(a); err != nil {
		t.Fatal(err)
	}
	poke := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("say") != "" {
			Record(r.Context(), Event{TargetLabel: "Thing one", Meta: map[string]any{"password": "hunter2-value", "changed": []string{"name"}}})
		}
		status, _ := strconv.Atoi(r.URL.Query().Get("status"))
		w.WriteHeader(max(status, http.StatusOK))
	}
	a.Server.Handle("POST /api/v1/test/things/{id}/parts/{part}", a.Auth.Admin(http.HandlerFunc(poke)))
	a.Server.Handle("GET /api/v1/test/things/{id}/parts/{part}", a.Auth.Admin(http.HandlerFunc(poke)))
	a.Server.Handle("DELETE /api/v1/admin/secrets/{name}", a.Auth.Admin(http.HandlerFunc(poke)))
	a.Server.Handle("POST /api/v1/test/mine", a.Auth.Required(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Record(r.Context(), Event{Action: "chat.share", TargetID: "c1"})
		Record(r.Context(), Event{Action: "chat.unshare", TargetID: "c1"})
	})))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &tenv{t: t, a: a, url: ts.URL, adminMail: "boss@example.com", usrMail: "pat@example.com"}
	e.adminID, e.admin = e.mkUser("admin", e.adminMail)
	e.userID, e.user = e.mkUser("user", e.usrMail)
	key, hash, prefix := auth.NewAPIKey()
	e.exec(`INSERT INTO api_key (id, user_id, key_hash, prefix) VALUES ('k1', $1, $2, $3)`, e.adminID, hash, prefix)
	e.akey = key
	return e
}

func (e *tenv) mkUser(role, email string) (string, string) {
	e.t.Helper()
	id := ids.New()
	e.exec(`INSERT INTO "user" (id, name, email, role) VALUES ($1, $2, $3, $4)`, id, strings.Split(email, "@")[0], email, role)
	tok, err := auth.IssueToken(e.a.Keys, id, time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	return id, tok
}

func (e *tenv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.a.DB.Exec(context.Background(), q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *tenv) do(method, path, tok string, hdr ...string) (int, []byte, http.Header) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.url+path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// rows returns every stored event, oldest first, as the API shapes them.
func (e *tenv) rows() []Row {
	e.t.Helper()
	h := &handler{a: e.a}
	out, err := h.query(context.Background(), "", nil, 1000)
	if err != nil {
		e.t.Fatal(err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func meta(t *testing.T, r Row) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Meta, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAdminHook(t *testing.T) {
	e := setup(t)
	path := "/api/v1/test/things/t-1/parts/p-2"

	if code, _, _ := e.do("GET", path, e.admin); code != 200 {
		t.Fatalf("GET = %d", code)
	}
	if n := len(e.rows()); n != 0 {
		t.Fatalf("a GET left %d events", n)
	}

	e.do("POST", path, e.admin, "User-Agent", "audit-test/1", "X-Forwarded-For", "203.0.113.5")
	got := e.rows()
	if len(got) != 1 {
		t.Fatalf("one POST left %d events", len(got))
	}
	r := got[0]
	if r.Action != "admin.request" || r.ActorID != e.adminID || r.ActorLabel != e.adminMail || r.ActorKind != KindUser ||
		r.TargetID != "t-1" || r.Outcome != OK || r.UserAgent != "audit-test/1" || r.IP != "203.0.113.5" {
		t.Fatalf("generic event: %+v", r)
	}
	if m := meta(t, r); m["part"] != "p-2" || m["method"] != "POST" || m["route"] != "/api/v1/test/things/{id}/parts/{part}" {
		t.Fatalf("generic meta: %v", m)
	}

	e.do("POST", path, e.user) // a non-admin: refused, recorded as denied with who tried
	e.do("POST", path+"?status=409", e.admin)
	e.do("POST", path+"?say=1", e.akey)
	e.do("DELETE", "/api/v1/admin/secrets/read-token", e.admin)
	e.do("POST", path, "") // nobody signed in: not recorded (401 before the hook)
	got = e.rows()
	if len(got) != 5 {
		t.Fatalf("%d events, want 5: %+v", len(got), got)
	}
	if d := got[1]; d.Outcome != Denied || d.ActorID != e.userID || d.Action != "admin.request" {
		t.Errorf("refused call: %+v", d)
	}
	if f := got[2]; f.Outcome != Failed {
		t.Errorf("409: %+v", f)
	}
	said := got[3] // the handler's own Record replaces the route's event, filled in from the route
	if said.ActorKind != KindAPIKey || said.TargetLabel != "Thing one" || said.TargetID != "t-1" || said.Action != "admin.request" {
		t.Errorf("explicit event: %+v", said)
	}
	if m := meta(t, said); m["password"] != nil || fmt.Sprint(m["changed"]) != "[name]" || m["part"] != "p-2" {
		t.Errorf("explicit meta: %v", m)
	}
	if strings.Contains(string(said.Meta), "hunter2") {
		t.Errorf("a password value reached the log: %s", said.Meta)
	}
	if c := got[4]; c.Action != "secret.delete" || c.TargetType != "secret" || c.TargetID != "read-token" {
		t.Errorf("catalogued route: %+v", c)
	}
}

func TestRecordOutsideAdminRoutes(t *testing.T) {
	e := setup(t)
	e.do("POST", "/api/v1/test/mine", e.user)
	got := e.rows()
	if len(got) != 2 || got[0].Action != "chat.share" || got[1].Action != "chat.unshare" || got[0].ActorID != e.userID || got[0].TargetType != "chat" {
		t.Fatalf("user route events: %+v", got)
	}
}

// seed stores events with chosen times, oldest first.
func (e *tenv) seed() {
	e.t.Helper()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	add := func(minutes int, actorID, label, action, target, outcome, meta string) {
		var id *string
		if actorID != "" {
			id = &actorID
		}
		e.exec(`INSERT INTO audit_event (id, at, actor_id, actor_label, actor_kind, action, target_type, target_id, target_label, outcome, meta)
			VALUES ($1, $2, $3, $4, 'user', $5, split_part($5, '.', 1), $6, $6, $7, $8)`,
			ids.New(), base.Add(time.Duration(minutes)*time.Minute), id, label, action, target, outcome, meta)
	}
	add(0, e.adminID, e.adminMail, "auth.sign_in", "", "ok", `{"method": "password"}`)
	add(1, "", "email#1a2b3c4d", "auth.sign_in_failed", "", "denied", `{"reason": "no such user"}`)
	add(2, e.adminID, e.adminMail, "secret.update", "read-token", "ok", `{"changed": ["value"]}`)
	add(3, e.adminID, e.adminMail, "settings.update", "", "ok", `{"settings": ["site.name"]}`)
	add(60*24, e.userID, e.usrMail, "chat.share", "c-9", "ok", `{}`)
	add(60*24+1, e.adminID, e.adminMail, "user.role_change", e.userID, "failed", `{"from": "user", "to": "admin"}`)
	add(60*24*3, e.adminID, e.adminMail, "pipeline.run", "nightly", "ok", `{}`)
}

func (e *tenv) list(query string) ([]Row, string) {
	e.t.Helper()
	code, b, _ := e.do("GET", "/api/v1/admin/audit?"+query, e.admin)
	if code != 200 {
		e.t.Fatalf("list %s = %d %s", query, code, b)
	}
	var out struct {
		Events []Row  `json:"events"`
		Next   string `json:"next"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		e.t.Fatal(err)
	}
	return out.Events, out.Next
}

func actionsOf(rows []Row) string {
	var s []string
	for _, r := range rows {
		s = append(s, r.Action)
	}
	return strings.Join(s, ",")
}

func TestListFilters(t *testing.T) {
	e := setup(t)
	e.seed()
	for _, c := range []struct{ query, want string }{
		{"", "pipeline.run,user.role_change,chat.share,settings.update,secret.update,auth.sign_in_failed,auth.sign_in"},
		{"actor=" + e.userID, "chat.share"},
		{"actor=PAT@", "chat.share"},
		{"action=auth.", "auth.sign_in_failed,auth.sign_in"},
		{"action=auth.,secret.", "secret.update,auth.sign_in_failed,auth.sign_in"},
		{"action=auth.sign_in_", "auth.sign_in_failed"}, // _ is a literal, not a wildcard
		{"target=read-token", "secret.update"},
		{"outcome=denied,failed", "user.role_change,auth.sign_in_failed"},
		{"from=2026-05-02&to=2026-05-02", "user.role_change,chat.share"},
		{"from=2026-05-01T12:02:00Z&to=2026-05-01", "settings.update,secret.update"},
		{"q=site.name", "settings.update"},
		{"q=NIGHTLY", "pipeline.run"},
		{"q=100%25", ""},
		{"actor=" + e.adminID + "&outcome=ok&action=secret.", "secret.update"},
	} {
		got, _ := e.list(c.query)
		if a := actionsOf(got); a != c.want {
			t.Errorf("%s: got %s, want %s", c.query, a, c.want)
		}
	}
}

// The actor filter (and the Users page's Activity link, which passes the user id) also returns the failed
// sign-ins against that user, which are stored with an anonymous actor; nothing else leaks in.
func TestActorFilterIncludesFailedSignIns(t *testing.T) {
	e := setup(t)
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	n := 0
	add := func(actorID, label, action, targetID, targetLabel string) {
		n++
		var actor *string
		if actorID != "" {
			actor = &actorID
		}
		kind := "anonymous"
		if actor != nil {
			kind = "user"
		}
		e.exec(`INSERT INTO audit_event (id, at, actor_id, actor_label, actor_kind, action, target_type, target_id, target_label, outcome)
			VALUES ($1, $2, $3, $4, $5, $6, 'user', $7, $8, 'denied')`, ids.New(), at.Add(time.Duration(n)*time.Minute), actor, label, kind, action, targetID, targetLabel)
	}
	add("", e.usrMail, "auth.sign_in_failed", e.userID, e.usrMail)           // password: the account is the target (id)
	add("", "PAT@EXAMPLE.COM", "auth.sign_in_failed", "", "PAT@EXAMPLE.COM") // OAuth: only the attempted email is known
	add("", e.adminMail, "auth.sign_in_failed", e.adminID, e.adminMail)      // another account
	add("", "email#1a2b3c4d", "auth.sign_in_failed", "", "email#1a2b3c4d")   // no such account
	add(e.adminID, e.adminMail, "user.role_change", e.userID, e.usrMail)     // an admin acts on the user: not the user's activity
	add(e.userID, e.usrMail, "chat.share", "c-1", "")
	add("", "", "auth.sign_in_failed", "", "") // nothing known
	for _, c := range []struct{ query, want string }{
		{"actor=" + e.userID, "chat.share,auth.sign_in_failed,auth.sign_in_failed"},
		{"actor=" + e.userID + "&action=auth.", "auth.sign_in_failed,auth.sign_in_failed"},
		{"actor=" + e.usrMail, "chat.share,auth.sign_in_failed,auth.sign_in_failed"},
		{"actor=" + e.adminID, "user.role_change,auth.sign_in_failed"},
		{"actor=no-such-id", ""},
	} {
		got, _ := e.list(c.query)
		if a := actionsOf(got); a != c.want {
			t.Errorf("%s: got %s, want %s", c.query, a, c.want)
		}
	}
	// stored as before: the failed sign-ins still have no actor
	got, _ := e.list("actor=" + e.userID + "&action=auth.")
	for _, r := range got {
		if r.ActorID != "" || r.ActorKind != "anonymous" {
			t.Errorf("stored actor changed: %+v", r)
		}
	}
}

func TestListPages(t *testing.T) {
	e := setup(t)
	e.seed()
	// two events at the same instant: the id breaks the tie, so paging neither skips nor repeats one
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	e.exec(`INSERT INTO audit_event (id, at, actor_kind, action) VALUES ($1, $2, 'system', 'config.apply')`, ids.New(), at)
	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		rows, next := e.list("limit=3&cursor=" + url.QueryEscape(cursor))
		for _, r := range rows {
			seen = append(seen, r.ID)
		}
		if next == "" {
			break
		}
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		cursor = next
	}
	all, _ := e.list("limit=200")
	if len(seen) != 8 || len(all) != 8 {
		t.Fatalf("paged %d events, listed %d, want 8", len(seen), len(all))
	}
	for i := range all {
		if all[i].ID != seen[i] {
			t.Fatalf("page order differs from the full list at %d", i)
		}
	}
	for _, bad := range []string{"cursor=nope", "from=yesterday"} {
		if code, _, _ := e.do("GET", "/api/v1/admin/audit?"+bad, e.admin); code != 400 {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	for _, p := range []string{"/api/v1/admin/audit", "/api/v1/admin/audit/export"} {
		if code, _, _ := e.do("GET", p, e.user); code != 403 {
			t.Errorf("non-admin %s = %d, want 403", p, code)
		}
	}
}

func TestExport(t *testing.T) {
	e := setup(t)
	e.seed()
	// a label a spreadsheet would run as a formula, and values that must never be written
	ctx := With(context.Background(), e.a.DB, KindSystem, "=HYPERLINK(\"http://evil.example\")")
	Record(ctx, Event{Action: "config.apply", Meta: map[string]any{"api_key": "sk-leak-1234567890", "note": "kp-leak-0987654321"}})
	code, b, hdr := e.do("GET", "/api/v1/admin/audit/export?outcome=ok", e.admin)
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") || !strings.Contains(hdr.Get("Content-Disposition"), "attachment") {
		t.Fatalf("export = %d %v", code, hdr)
	}
	recs, err := csv.NewReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1+6 || recs[0][0] != "at" || recs[0][4] != "action" {
		t.Fatalf("csv has %d records (want header + 6 ok events): %v", len(recs), recs)
	}
	if !strings.Contains(string(b), `'=HYPERLINK`) {
		t.Errorf("a formula-like cell was not neutralised:\n%s", b)
	}
	for _, leak := range []string{"sk-leak", "kp-leak", "hunter2"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("export contains %q", leak)
		}
	}
	last := e.rows()
	if r := last[len(last)-1]; r.Action != "audit.export" || r.ActorID != e.adminID || meta(t, r)["rows"] != float64(6) {
		t.Errorf("the export was not recorded: %+v %s", r, r.Meta)
	}
}

func TestPurge(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for _, days := range []int{400, 30, 1} {
		e.exec(`INSERT INTO audit_event (id, at, actor_kind, action) VALUES ($1, now() - make_interval(days => $2), 'system', 'config.apply')`, ids.New(), days)
	}
	count := func() int {
		var n int
		_ = e.a.DB.QueryRow(ctx, `SELECT count(*) FROM audit_event`).Scan(&n)
		return n
	}
	if n, err := purge(ctx, e.a); err != nil || n != 1 || count() != 2 {
		t.Fatalf("default 365 days: purged %d (%v), %d left", n, err, count())
	}
	if err := e.a.Settings.Set(ctx, "audit.retention_days", 0, false); err != nil {
		t.Fatal(err)
	}
	if n, err := purge(ctx, e.a); err != nil || n != 0 || count() != 2 {
		t.Fatalf("0 keeps everything: purged %d (%v)", n, err)
	}
	if err := e.a.Settings.Set(ctx, "audit.retention_days", 7, false); err != nil {
		t.Fatal(err)
	}
	if n, err := purge(ctx, e.a); err != nil || n != 1 || count() != 1 {
		t.Fatalf("7 days: purged %d (%v), %d left", n, err, count())
	}
}
