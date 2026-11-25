package audit

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/testdb"
)

func TestSecretKey(t *testing.T) {
	for _, k := range []string{"password", "new_password", "Passwords", "api_key", "apiKey", "keys", "token", "access_token",
		"client_secret", "secret", "value", "env_value", "values", "credentials", "cookie", "Authorization", "dsn",
		"content", "message", "prompt", "body", "text", "private_key"} {
		if !secretKey(k) {
			t.Errorf("%q should be dropped", k)
		}
	}
	for _, k := range []string{"secret_ref", "changed", "value_changed", "api_keys_revoked", "variable", "write_only",
		"settings", "users", "role", "from", "to", "method", "reason", "route", "var", "share_id", "file", "kinds", "grants"} {
		if secretKey(k) {
			t.Errorf("%q should stay", k)
		}
	}
}

func TestClean(t *testing.T) {
	long := strings.Repeat("é", 300)
	many := make([]string, 80)
	for i := range many {
		many[i] = "u"
	}
	got := clean(map[string]any{
		"password":   "hunter2",
		"changed":    []string{"value", "description"},
		"secret_ref": "read-token",
		"nested":     map[string]any{"token": "x", "ok": true, "deeper": map[string]any{"deepest": map[string]any{"x": 1}}},
		"leaked":     "sk-abcdefghijklmnop",
		"bearer":     []any{"Bearer abc", "fine"},
		"long":       long,
		"many":       many,
		"count":      int64(3),
		"fn":         func() {},
	}, 0)
	b, _ := json.Marshal(got)
	s := string(b)
	for _, bad := range []string{"hunter2", "sk-abcdefghijklmnop", "Bearer abc", `"token"`, `"fn"`, `"deepest"`} {
		if strings.Contains(s, bad) {
			t.Errorf("clean kept %q: %s", bad, s)
		}
	}
	for _, want := range []string{`"changed":["value","description"]`, `"secret_ref":"read-token"`, `"ok":true`, `"count":3`, `"[redacted]"`, `"fine"`} {
		if !strings.Contains(s, want) {
			t.Errorf("clean lost %s: %s", want, s)
		}
	}
	if l := got["long"].(string); len(l) > maxMetaStr || !strings.HasPrefix(long, l) {
		t.Errorf("long string not clipped on a character boundary: %d bytes", len(l))
	}
	if n := len(got["many"].([]any)); n != maxMetaLen {
		t.Errorf("list of 80 kept %d items, want %d", n, maxMetaLen)
	}
}

func TestLimiter(t *testing.T) {
	var l limiter
	now := time.Date(2026, 1, 1, 10, 0, 5, 0, time.UTC)
	n := 0
	for range perAddress + 10 {
		if l.allow("203.0.113.1", now) {
			n++
		}
	}
	if n != perAddress {
		t.Fatalf("one address got %d events in a minute, want %d", n, perAddress)
	}
	if !l.allow("203.0.113.2", now) {
		t.Fatal("another address was refused")
	}
	if !l.allow("203.0.113.1", now.Add(time.Minute)) {
		t.Fatal("the cap did not reset with the next minute")
	}
	total := 0
	for i := range perMinute + 50 {
		if l.allow(strings.Repeat("a", i%200+1), now.Add(2*time.Minute)) {
			total++
		}
	}
	if total != perMinute {
		t.Fatalf("%d events from many addresses in a minute, want the overall cap %d", total, perMinute)
	}
}

func TestOutcomeOf(t *testing.T) {
	for status, want := range map[int]string{0: OK, 200: OK, 201: OK, 204: OK, 302: OK, 400: Failed, 401: Denied, 403: Denied, 404: Failed, 409: Failed, 500: Failed} {
		if got := outcomeOf(status); got != want {
			t.Errorf("status %d = %s, want %s", status, got, want)
		}
	}
}

// Record must never fail or block its caller: no scope, no database, a closed database.
func TestRecordNeverFails(t *testing.T) {
	Record(context.Background(), Event{Action: "x.y"})
	Record(With(context.Background(), nil, KindSystem, "test"), Event{Action: "x.y"}) // nil pool: recovered
	pool := testdb.New(t)
	ctx := With(context.Background(), pool, KindSystem, "test")
	pool.Close()
	done := make(chan struct{})
	go func() { Record(ctx, Event{Action: "x.y"}); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Record blocked on a closed database")
	}
}

func TestRecordTruncates(t *testing.T) {
	pool := testdb.New(t)
	ctx := With(context.Background(), pool, KindSystem, "test")
	big := map[string]any{}
	for i := range 40 {
		big["k"+strings.Repeat("x", i)] = strings.Repeat("y", 190)
	}
	Record(ctx, Event{Action: strings.Repeat("a", 100), TargetLabel: strings.Repeat("ü", 400), Meta: big})
	var action, label string
	var meta map[string]any
	if err := pool.QueryRow(ctx, `SELECT action, target_label, meta FROM audit_event`).Scan(&action, &label, &meta); err != nil {
		t.Fatal(err)
	}
	if len(action) != 64 || len(label) > maxField || !strings.HasPrefix(strings.Repeat("ü", 400), label) {
		t.Errorf("not clipped: action %d bytes, label %d bytes", len(action), len(label))
	}
	if meta["truncated"] != true || len(meta) != 1 {
		t.Errorf("meta over %d bytes kept: %v", maxMeta, meta)
	}
}

// Failed and refused events are capped per address and minute; the request's address is the key.
func TestFailedEventsAreCapped(t *testing.T) {
	pool := testdb.New(t)
	l := &Log{db: pool}
	h := l.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Record(r.Context(), Event{Action: "auth.sign_in_failed", Outcome: Denied, ActorKind: KindAnonymous})
	}))
	send := func(addr string) {
		req := httptest.NewRequest("POST", "/x", nil)
		req.RemoteAddr = addr + ":1234"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	for range perAddress + 15 {
		send("203.0.113.7")
	}
	send("198.51.100.4")
	var n, other int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE ip = '203.0.113.7'), count(*) FILTER (WHERE ip = '198.51.100.4') FROM audit_event`).Scan(&n, &other); err != nil {
		t.Fatal(err)
	}
	if n != perAddress || other != 1 {
		t.Fatalf("stored %d events from the flooding address (want %d) and %d from another (want 1)", n, perAddress, other)
	}
}

// Every action the code can record has a sentence in the web app, and every route the catalogue names exists.
func TestActionsHaveSentences(t *testing.T) {
	web, err := os.ReadFile("../../web/src/features/admin/audit.ts")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{"admin.request": true}
	for _, a := range actions {
		used[a] = true
	}
	literal := regexp.MustCompile(`Action:\s*"([a-z_]+\.[a-z_]+)"`)
	mapped := regexp.MustCompile(`"[a-z]+":\s*"(user\.[a-z_]+)"`) // connectors.userActions
	var src []byte
	for _, dir := range []string{"../../internal", "../../cmd"} {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			b, err := os.ReadFile(p)
			if !strings.Contains(filepath.ToSlash(p), "internal/audit/") { // actions.go itself names every route
				src = append(src, b...)
			}
			for _, m := range literal.FindAllSubmatch(b, -1) {
				used[string(m[1])] = true
			}
			if strings.HasSuffix(p, "connectors.go") {
				for _, m := range mapped.FindAllSubmatch(b, -1) {
					used[string(m[1])] = true
				}
			}
			return err
		})
	}
	for _, a := range slices.Sorted(maps.Keys(used)) {
		if !strings.Contains(string(web), `'`+a+`'`) {
			t.Errorf("action %s has no sentence in web/src/features/admin/audit.ts", a)
		}
	}
	for pattern := range actions {
		if !strings.Contains(string(src), `"`+pattern+`"`) {
			t.Errorf("catalogue route %q is not registered anywhere (renamed?)", pattern)
		}
	}
	if len(used) < 60 {
		t.Fatalf("found only %d actions: the source scan is broken", len(used))
	}
}
