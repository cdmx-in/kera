package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"iter"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/identity"
	"github.com/cdmx-in/kera/internal/knowledge"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

// TestMain lets the test binary act as the kera command: the end-to-end test
// runs `kera pipeline push` as the child of `kera pipeline run` by starting itself.
func TestMain(m *testing.M) {
	if os.Getenv("KERA_PIPELINE_TEST_CLI") == "1" {
		err := Command(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
		var ee *ExitError
		switch {
		case errors.As(err, &ee):
			os.Exit(ee.Code)
		case err != nil:
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// stubEmbed is a bag-of-words embedder: each word lands in a hashed bucket.
type stubEmbed struct{}

func (stubEmbed) ConnectionClient(context.Context, string) (provider.Client, error) {
	return stubEmbed{}, nil
}
func (stubEmbed) ListModels(context.Context) ([]string, error) { return nil, nil }
func (stubEmbed) Stream(context.Context, provider.Request) iter.Seq2[provider.Event, error] {
	return func(func(provider.Event, error) bool) {}
}

func (stubEmbed) Embed(_ context.Context, _ string, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 16)
		v[0] = 0.01
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(strings.Trim(w, ".,?!#")))
			v[h.Sum32()%16]++
		}
		var n float64
		for _, x := range v {
			n += float64(x * x)
		}
		for j := range v {
			v[j] /= float32(math.Sqrt(n))
		}
		out[i] = v
	}
	return out, nil
}

// clock is the service's time; tests move it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type tenv struct {
	t        *testing.T
	pool     *pgxpool.Pool
	s        *Service
	k        *knowledge.Service
	url      string
	clk      *clock
	admin    string // session token of the admin "root"
	user     string // session token of the user "alice"
	adminKey string // an sk- API key of the admin
}

// newEnv builds a server with the pipeline routes. With index the ingest worker
// runs; without it files stay queued (tests move the queue with k.Step).
func newEnv(t *testing.T, index bool) *tenv {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	keys, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(pool)
	cfg := config.Env{DataDir: t.TempDir(), PublicURL: "http://kera.example.com"}
	srv, err := api.New(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Env: cfg, DB: pool, Keys: keys, Settings: st, Server: srv, Auth: auth.New(pool, keys, st, false)}
	for k, v := range map[string]any{"rag.embedding.connection": "ollama", "rag.embedding.model": "stub", "rag.chunk_size": 200, "rag.chunk_overlap": 20} {
		if err := st.Set(ctx, k, v, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := files.Mount(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.For(a).Close)
	k := knowledge.New(pool, st, files.For(a), nil, stubEmbed{})
	files.For(a).OnProcessed(func(context.Context, files.File) { k.Wake() })
	if index {
		if err := k.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(k.Close)
	s := New(a, k)
	clk := &clock{t: time.Date(2026, 3, 10, 6, 0, 0, 0, time.UTC)}
	s.now = clk.now
	s.routes()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &tenv{t: t, pool: pool, s: s, k: k, url: ts.URL, clk: clk}
	for _, u := range []struct{ id, role string }{{"root", "admin"}, {"alice", "user"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email, role) VALUES ($1, $1 || '@example.com', $2)`, u.id, u.role); err != nil {
			t.Fatal(err)
		}
		tok, err := auth.IssueToken(keys, u.id, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if u.id == "root" {
			e.admin = tok
		} else {
			e.user = tok
		}
	}
	key, hash, prefix := auth.NewAPIKey()
	if _, err := pool.Exec(ctx, `INSERT INTO api_key (id, user_id, key_hash, prefix) VALUES ('k1', 'root', $1, $2)`, hash, prefix); err != nil {
		t.Fatal(err)
	}
	e.adminKey = key
	return e
}

func (e *tenv) do(tok, method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, e.url+path, rd)
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

func (e *tenv) call(tok, method, path string, body any, want int, out any) {
	e.t.Helper()
	code, b := e.do(tok, method, path, body)
	if code != want {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, code, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

func (e *tenv) count(q string, args ...any) (n int) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// mkPipe creates a pipeline (owner root) with new collections and returns its token and the collection ids.
func (e *tenv) mkPipe(name string, colls ...string) (pipe, []string) {
	e.t.Helper()
	var out struct {
		Pipeline pipelineOut
		Token    string
	}
	e.call(e.admin, "POST", "/api/v1/admin/pipelines", map[string]any{"name": name, "new_collections": colls}, http.StatusCreated, &out)
	var kids []string
	for _, t := range out.Pipeline.Targets {
		kids = append(kids, t.ID)
	}
	return pipe{e: e, name: name, tok: out.Token}, kids
}

// pipe is a pipeline seen through its token.
type pipe struct {
	e         *tenv
	name, tok string
}

func (p pipe) path(rest string) string { return "/api/v1/pipelines/" + p.name + rest }

func (p pipe) start(in startIn) int64 {
	p.e.t.Helper()
	var out startOut
	p.e.call(p.tok, "POST", p.path("/runs"), in, http.StatusCreated, &out)
	return out.Run
}

func (p pipe) steps(run int64, items ...stepIn) {
	p.e.t.Helper()
	p.e.call(p.tok, "POST", p.path(fmt.Sprintf("/runs/%d/steps", run)), map[string]any{"steps": items}, http.StatusOK, nil)
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (p pipe) sync(run int64, coll string, content map[string]string, complete bool) planOut {
	p.e.t.Helper()
	files := []knowledge.SyncFile{}
	for n, c := range content {
		files = append(files, knowledge.SyncFile{Name: n, SHA256: sum(c), Size: int64(len(c))})
	}
	var out planOut
	p.e.call(p.tok, "POST", p.path(fmt.Sprintf("/runs/%d/sync", run)), map[string]any{"collection": coll, "files": files, "complete": complete}, http.StatusOK, &out)
	return out
}

func (p pipe) put(run int64, coll, name, content string) {
	p.e.t.Helper()
	q := fmt.Sprintf("/runs/%d/files?collection=%s&name=%s&sha256=%s", run, coll, name, sum(content))
	p.e.call(p.tok, "PUT", p.path(strings.ReplaceAll(q, " ", "%20")), []byte(content), http.StatusOK, nil)
}

func (p pipe) finish(run int64, errText string) runOut {
	p.e.t.Helper()
	var out runOut
	p.e.call(p.tok, "POST", p.path(fmt.Sprintf("/runs/%d/finish", run)), map[string]any{"error": errText}, http.StatusOK, &out)
	return out
}

func (p pipe) get(run int64) runOut {
	p.e.t.Helper()
	var out runOut
	p.e.call(p.tok, "GET", p.path(fmt.Sprintf("/runs/%d", run)), nil, http.StatusOK, &out)
	return out
}

// closed polls the run (which settles it) until it is closed.
func (p pipe) closed(run int64) runOut {
	p.e.t.Helper()
	for i := 0; i < 600; i++ {
		if rv := p.get(run); rv.FinishedAt != nil {
			return rv
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.e.t.Fatalf("run %d did not close", run)
	return runOut{}
}

// stepOf finds a step of a run view by name (and kind).
func stepOf(rv runOut, kera bool, name string) stepOut {
	if rv.Steps != nil {
		for _, st := range *rv.Steps {
			if st.Kera == kera && st.Name == name {
				return st
			}
		}
	}
	return stepOut{Status: "(missing)"}
}

// logOf reads one step's log through the admin route.
func (e *tenv) logOf(pname string, run int64, seq int) stepOut {
	e.t.Helper()
	var st stepOut
	e.call(e.admin, "GET", fmt.Sprintf("/api/v1/admin/pipelines/%s/runs/%d/steps/%d", pname, run, seq), nil, http.StatusOK, &st)
	return st
}

// ---- pure functions ---------------------------------------------------------------

func TestReport(t *testing.T) {
	now := time.Date(2026, 3, 10, 6, 0, 0, 0, time.UTC)
	step := func(status string) *step { return &step{Status: status, Counts: map[string]int64{}} }
	for _, c := range []struct {
		name         string
		from         string
		in           stepIn
		status, errs string
	}{
		{"pending starts when written to", "pending", stepIn{Log: "x"}, "running", ""},
		{"declared twice changes nothing", "running", stepIn{Status: "pending"}, "running", ""},
		{"running closes ok", "running", stepIn{Status: "ok"}, "ok", ""},
		{"closed step ignores running", "ok", stepIn{Status: "running"}, "ok", ""},
		{"closed step ignores ok", "failed", stepIn{Status: "ok"}, "failed", ""},
		{"ok escalates to warning", "ok", stepIn{Status: "warning", Error: "slow"}, "warning", "slow"},
		{"warning escalates to failed", "warning", stepIn{Status: "failed", Error: "boom"}, "failed", "boom"},
		{"failed does not drop to warning", "failed", stepIn{Status: "warning", Error: "late"}, "failed", "late"},
		{"skipped from pending", "pending", stepIn{Status: "skipped"}, "skipped", ""},
		{"unknown status is ignored", "running", stepIn{Status: "bogus"}, "running", ""},
	} {
		st := step(c.from)
		st.report(c.in, now, nil)
		if st.Status != c.status || st.Error != c.errs {
			t.Errorf("%s: got %q / %q, want %q / %q", c.name, st.Status, st.Error, c.status, c.errs)
		}
	}
	st := step("running")
	st.report(stepIn{Status: "failed", Error: "first"}, now, nil)
	st.report(stepIn{Error: "second"}, now, nil)
	st.report(stepIn{Error: "second"}, now, nil)
	if st.Error != "first; second" {
		t.Errorf("errors join once: %q", st.Error)
	}
	st = step("running")
	for i := 0; i < 30; i++ {
		st.report(stepIn{Counts: map[string]int64{fmt.Sprintf("k%d", i): int64(i)}}, now, nil)
	}
	st.report(stepIn{Counts: map[string]int64{"bad key!": 1}}, now, nil)
	if len(st.Counts) != maxCounts {
		t.Errorf("counts kept: %d, want %d", len(st.Counts), maxCounts)
	}
}

func TestAddLog(t *testing.T) {
	now := time.Date(2026, 3, 10, 6, 7, 8, 0, time.UTC)
	st := &step{Status: "running"}
	st.addLog("one\n\n  \ntwo\r\n", now, nil)
	if st.Log != "06:07:08 one\n06:07:08 two\n" {
		t.Errorf("log %q", st.Log)
	}
	// the step keeps the tail
	st = &step{Status: "running"}
	line := strings.Repeat("x", 1000)
	for i := 0; i < 400; i++ {
		st.addLog(fmt.Sprintf("%03d %s", i, line), now, nil)
	}
	if len(st.Log) > maxStepLog || !st.LogDropped || !strings.Contains(st.Log, "399 xxx") || strings.Contains(st.Log, " 000 x") {
		t.Errorf("tail: len %d dropped %v", len(st.Log), st.LogDropped)
	}
	if !strings.HasPrefix(st.Log, "06:07:08 ") {
		t.Errorf("the tail starts mid-line: %q", st.Log[:20])
	}
	// the run budget drops whole reports
	budget := 100
	st = &step{Status: "running"}
	st.addLog(strings.Repeat("y", 200), now, &budget)
	if st.Log != "" || !st.LogDropped || budget != 100 {
		t.Errorf("budget: log %d dropped %v budget %d", len(st.Log), st.LogDropped, budget)
	}
}

func TestIsLate(t *testing.T) {
	created := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	good := time.Date(2026, 3, 10, 6, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		every int
		last  *time.Time
		now   time.Time
		want  bool
	}{
		{"no expectation", 0, &good, good.AddDate(1, 0, 0), false},
		{"on time", 60, &good, good.Add(74 * time.Minute), false},
		{"slack is 15 minutes for short intervals", 60, &good, good.Add(76 * time.Minute), true},
		{"slack is 10 percent for long ones", 1440, &good, good.Add(1440*time.Minute + 143*time.Minute), false},
		{"past 10 percent", 1440, &good, good.Add(1440*time.Minute + 145*time.Minute), true},
		{"never run counts from creation", 60, nil, created.Add(74 * time.Minute), false},
		{"never run and overdue", 60, nil, created.Add(76 * time.Minute), true},
	} {
		if got := isLate(c.every, c.last, created, c.now); got != c.want {
			t.Errorf("%s: late = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHuman(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "1 min", 5 * time.Minute: "5 min", 2 * time.Hour: "2 h", 50 * time.Hour: "2 days"} {
		if got := human(d); got != want {
			t.Errorf("human(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestComposeError(t *testing.T) {
	steps := []step{
		{Name: "fetch", Status: "failed", Error: "kept the old docs."},
		{Name: "index", Status: "ok"},
		{Name: "Product docs", Kera: true, Status: "failed", Error: "1 of 4 files could not be indexed: scan.pdf: no text."},
	}
	if got, want := composeError("", "", steps), "Failed at step fetch: kept the old docs. Product docs: 1 of 4 files could not be indexed: scan.pdf: no text."; got != want {
		t.Errorf("composed:\n got %q\nwant %q", got, want)
	}
	if got := composeError("", "the script said so", steps[:1]); got != "the script said so" {
		t.Errorf("finish error wins over failed steps: %q", got)
	}
	if got := composeError("Closed by an admin.", "x", steps[:1]); got != "Closed by an admin." {
		t.Errorf("forced reason wins: %q", got)
	}
}

// ---- token scope --------------------------------------------------------------------

func TestTokenScope(t *testing.T) {
	e := newEnv(t, true)
	a, kidsA := e.mkPipe("docs-sync", "Product docs")
	b, _ := e.mkPipe("other-sync", "Other docs")
	run := a.start(startIn{})

	t.Run("a token of another pipeline is refused", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{"POST", a.path("/runs")}, {"GET", a.path(fmt.Sprintf("/runs/%d", run))}, {"GET", a.path("/health")},
			{"POST", a.path(fmt.Sprintf("/runs/%d/steps", run))}, {"POST", a.path(fmt.Sprintf("/runs/%d/finish", run))},
		} {
			if code, _ := e.do(b.tok, tc.method, tc.path, map[string]any{}); code != http.StatusUnauthorized {
				t.Errorf("%s %s with another pipeline's token: %d", tc.method, tc.path, code)
			}
		}
	})

	t.Run("a collection that is not a target", func(t *testing.T) {
		for _, coll := range []string{"Other docs", "no such collection", ""} {
			code, body := e.do(a.tok, "POST", a.path(fmt.Sprintf("/runs/%d/sync", run)), map[string]any{"collection": coll, "files": []any{}, "complete": true})
			if code != http.StatusForbidden || !strings.Contains(string(body), "not_a_target") {
				t.Errorf("sync into %q: %d %s", coll, code, body)
			}
			if code, _ := e.do(a.tok, "PUT", a.path(fmt.Sprintf("/runs/%d/files?collection=%s&name=a.md&sha256=%s", run, strings.ReplaceAll(coll, " ", "%20"), sum("x"))), []byte("x")); code != http.StatusForbidden {
				t.Errorf("put into %q: %d", coll, code)
			}
		}
		if n := e.count(`SELECT count(*) FROM knowledge_file`); n != 0 {
			t.Errorf("%d rows were written", n)
		}
		// the same collection by id is fine
		a.sync(run, kidsA[0], map[string]string{}, true)
	})

	t.Run("a pipeline token is no user credential", func(t *testing.T) {
		for _, path := range []string{"/api/v1/admin/pipelines", "/api/v1/files/", "/api/v1/admin/pipelines/docs-sync/runs"} {
			if code, _ := e.do(a.tok, "GET", path, nil); code != http.StatusUnauthorized {
				t.Errorf("GET %s with a pipeline token: %d", path, code)
			}
		}
	})

	t.Run("no user or admin credential works on a token route", func(t *testing.T) {
		for name, tok := range map[string]string{"admin session": e.admin, "user session": e.user, "admin api key": e.adminKey, "none": "", "wrong prefix": "sk-" + strings.Repeat("a", 40), "unknown kp token": "kp-" + strings.Repeat("a", 40)} {
			for _, path := range []string{a.path("/runs"), a.path("/health"), a.path(fmt.Sprintf("/runs/%d", run))} {
				method := "GET"
				if strings.HasSuffix(path, "/runs") {
					method = "POST"
				}
				if code, _ := e.do(tok, method, path, map[string]any{}); code != http.StatusUnauthorized {
					t.Errorf("%s: %s %s: %d", name, method, path, code)
				}
			}
		}
	})

	t.Run("a run of another pipeline is not found", func(t *testing.T) {
		runB := b.start(startIn{})
		if code, _ := e.do(a.tok, "GET", a.path(fmt.Sprintf("/runs/%d", runB)), nil); code != http.StatusNotFound {
			t.Errorf("another pipeline's run: %d", code)
		}
	})

	t.Run("a rotated token replaces the old one", func(t *testing.T) {
		var out struct{ Token string }
		e.call(e.admin, "POST", "/api/v1/admin/pipelines/docs-sync/token", nil, http.StatusOK, &out)
		if !strings.HasPrefix(out.Token, "kp-") || len(out.Token) != 43 {
			t.Fatalf("token shape %q", out.Token)
		}
		if code, _ := e.do(a.tok, "GET", a.path("/health"), nil); code != http.StatusUnauthorized {
			t.Errorf("old token: %d", code)
		}
		if code, _ := e.do(out.Token, "GET", a.path("/health"), nil); code != http.StatusOK && code != http.StatusServiceUnavailable {
			t.Errorf("new token: %d", code)
		}
		a.tok = out.Token
	})

	t.Run("a disabled pipeline answers 403", func(t *testing.T) {
		e.call(e.admin, "PUT", "/api/v1/admin/pipelines/docs-sync", map[string]any{"enabled": false}, http.StatusOK, nil)
		for _, path := range []string{a.path("/health"), a.path(fmt.Sprintf("/runs/%d", run))} {
			if code, _ := e.do(a.tok, "GET", path, nil); code != http.StatusForbidden {
				t.Errorf("GET %s: %d", path, code)
			}
		}
		if code, _ := e.do(a.tok, "POST", a.path("/runs"), startIn{}); code != http.StatusForbidden {
			t.Errorf("start: %d", code)
		}
	})

	t.Run("a user cannot use the admin routes", func(t *testing.T) {
		if code, _ := e.do(e.user, "GET", "/api/v1/admin/pipelines", nil); code != http.StatusForbidden {
			t.Errorf("user on admin route: %d", code)
		}
	})

	if n := e.count(`SELECT count(*) FROM pipeline_run`); n != 2 {
		t.Errorf("%d runs; only the two started with a good token should exist", n)
	}
}

func TestOneOpenRun(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	run := p.start(startIn{Trigger: "push"})
	var held struct {
		Code, Detail, Trigger string
		Run                   int64
	}
	e.call(p.tok, "POST", p.path("/runs"), startIn{}, http.StatusConflict, &held)
	if held.Code != "run_active" || held.Run != run || held.Trigger != "push" || !strings.Contains(held.Detail, "already has a run in progress") {
		t.Errorf("409 body: %+v", held)
	}
	p.finish(run, "")
	if second := p.start(startIn{}); second == run {
		t.Error("a new run reused the number")
	}
	// reports and uploads to a closed run are refused
	if code, _ := e.do(p.tok, "POST", p.path(fmt.Sprintf("/runs/%d/steps", run)), map[string]any{"steps": []stepIn{{Name: "late"}}}); code != http.StatusConflict {
		t.Errorf("steps on a closed run: %d", code)
	}
}

func TestScriptCannotWriteKeraStep(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	run := p.start(startIn{Steps: []string{"fetch"}})
	p.steps(run, stepIn{Name: kids[0], Status: "failed", Error: "forged"}, stepIn{Name: "Product docs", Status: "ok", Counts: map[string]int64{"indexed": 99}})
	rv := p.get(run)
	node := stepOf(rv, true, "Product docs")
	if node.Status != "pending" || node.Error != "" || node.Counts["indexed"] != 0 || node.Key != kids[0] {
		t.Errorf("the collection step was touched: %+v", node)
	}
	if stepOf(rv, false, kids[0]).Status != "failed" || stepOf(rv, false, "Product docs").Status != "ok" {
		t.Errorf("a script step with a collection's name is an ordinary script step: %+v", *rv.Steps)
	}
}

func TestStepLimitsAndMasking(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	run := p.start(startIn{})

	t.Run("masking and control characters", func(t *testing.T) {
		pw, tok := "Hunter"+"2xYz99", "kp-"+strings.Repeat("Ab3", 14)
		p.steps(run, stepIn{Name: "fetch", Log: "connect with password=" + pw + "\n\x1b[31mred\x1b[0m and\x07 bell\ntoken " + tok + "\nplain line", Error: "failed with password: " + pw})
		st := e.logOf("docs-sync", run, 1)
		if st.Log == nil || strings.Contains(*st.Log, pw) || strings.Contains(*st.Log, tok) || strings.Contains(*st.Log, "\x1b") || strings.Contains(*st.Log, "\x07") ||
			!strings.Contains(*st.Log, "[REDACTED") || !strings.Contains(*st.Log, "red and bell") || !strings.Contains(*st.Log, "06:00:00 plain line") {
			t.Errorf("log: %q", *st.Log)
		}
		if strings.Contains(st.Error, pw) || !strings.Contains(st.Error, "[REDACTED") {
			t.Errorf("error: %q", st.Error)
		}
	})

	t.Run("the error is capped", func(t *testing.T) {
		p.steps(run, stepIn{Name: "long", Status: "failed", Error: strings.Repeat("é", 5000)})
		if n := len([]rune(stepOf(p.get(run), false, "long").Error)); n != maxErrorLen {
			t.Errorf("error length %d", n)
		}
	})

	t.Run("one step keeps its tail", func(t *testing.T) {
		line := strings.Repeat("z", 1000)
		for i := 0; i < 20; i++ {
			var sb strings.Builder
			for j := 0; j < 20; j++ {
				fmt.Fprintf(&sb, "line-%04d %s\n", i*20+j, line)
			}
			p.steps(run, stepIn{Name: "chatty", Log: sb.String()})
		}
		seq := stepOf(p.get(run), false, "chatty").Seq
		st := e.logOf("docs-sync", run, seq)
		if len(*st.Log) > maxStepLog || !st.LogDropped || !strings.Contains(*st.Log, "line-0399") || strings.Contains(*st.Log, "line-0000") {
			t.Errorf("tail: %d bytes, dropped %v", len(*st.Log), st.LogDropped)
		}
	})

	t.Run("a run keeps 4 MiB of log", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			p.steps(run, stepIn{Name: fmt.Sprintf("big-%02d", i), Log: "start"})
		}
		// fill the allowance directly: posting 4 MiB through the masking rules is slow under -race
		if _, err := e.pool.Exec(context.Background(), `UPDATE pipeline_step SET log = repeat(repeat('v', 99) || E'\n', 2100) WHERE run_id = $1 AND name LIKE 'big-%'`, run); err != nil {
			t.Fatal(err)
		}
		before := e.count(`SELECT coalesce(sum(octet_length(log)), 0)::int FROM pipeline_step WHERE run_id = $1`, run)
		if before <= maxRunLog {
			t.Fatalf("setup: %d bytes", before)
		}
		p.steps(run, stepIn{Name: "big-00", Log: "one more line"})
		if after := e.count(`SELECT coalesce(sum(octet_length(log)), 0)::int FROM pipeline_step WHERE run_id = $1`, run); after != before {
			t.Errorf("the run log grew past the allowance: %d -> %d", before, after)
		}
		if !stepOf(p.get(run), false, "big-00").LogDropped {
			t.Error("nothing says the log was cut")
		}
	})

	t.Run("a run holds 100 script steps", func(t *testing.T) {
		run := finishAndRestart(p, run)
		var items []stepIn
		for i := 0; i < 120; i++ {
			items = append(items, stepIn{Name: fmt.Sprintf("s%03d", i)})
		}
		var out struct{ Ignored int }
		e.call(p.tok, "POST", p.path(fmt.Sprintf("/runs/%d/steps", run)), map[string]any{"steps": items}, http.StatusOK, &out)
		if out.Ignored != 20 || e.count(`SELECT count(*) FROM pipeline_step WHERE run_id = $1 AND NOT kera`, run) != maxSteps {
			t.Errorf("ignored %d, stored %d", out.Ignored, e.count(`SELECT count(*) FROM pipeline_step WHERE run_id = $1 AND NOT kera`, run))
		}
	})

	t.Run("a report is at most 1 MiB", func(t *testing.T) {
		run := finishAndRestart(p, 0)
		big := map[string]any{"steps": []stepIn{{Name: "x", Log: strings.Repeat("a\n", 600<<10)}}}
		if code, _ := e.do(p.tok, "POST", p.path(fmt.Sprintf("/runs/%d/steps", run)), big); code != http.StatusRequestEntityTooLarge {
			t.Errorf("1.2 MiB report: %d", code)
		}
	})

	t.Run("bad step reports are ignored, not fatal", func(t *testing.T) {
		run := finishAndRestart(p, 0)
		var out struct{ Ignored int }
		e.call(p.tok, "POST", p.path(fmt.Sprintf("/runs/%d/steps", run)), map[string]any{"steps": []stepIn{{Name: ""}, {Name: strings.Repeat("n", 300)}, {Name: "a", Status: "bogus"}, {Name: "good", Status: "running"}}}, http.StatusOK, &out)
		if out.Ignored != 3 {
			t.Errorf("ignored %d", out.Ignored)
		}
	})
}

// finishAndRestart closes the pipeline's open run and opens a new one.
func finishAndRestart(p pipe, open int64) int64 {
	p.e.t.Helper()
	if open == 0 {
		open = int64(p.e.count(`SELECT max(id) FROM pipeline_run`))
	}
	p.finish(open, "")
	return p.start(startIn{})
}

// ---- lifecycle -----------------------------------------------------------------------

var docs = map[string]string{
	"fox.md":  "# Fox\nThe quick brown fox jumps over the lazy dog.",
	"jugs.md": "# Jugs\nPack my box with five dozen liquor jugs.",
	"tea.md":  "# Tea\nBrew the leaves for three minutes in hot water.",
}

// push is a script's whole run for one collection: start, plan, upload, finish; it returns the closed run.
func (p pipe) push(coll string, content map[string]string, steps ...stepIn) (int64, runOut) {
	p.e.t.Helper()
	run := p.start(startIn{Steps: []string{"fetch", "kera"}})
	p.steps(run, stepIn{Name: "fetch", Status: "ok"}, stepIn{Name: "kera", Status: "running"})
	plan := p.sync(run, coll, content, true)
	for _, n := range plan.Upload {
		p.put(run, coll, n, content[n])
	}
	p.sync(run, coll, content, true)
	p.steps(run, append(steps, stepIn{Name: "kera", Status: "ok"})...)
	p.finish(run, "")
	return run, p.closed(run)
}

func TestLifecycleOK(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")

	run, rv := p.push("Product docs", docs)
	if rv.Status != "ok" || rv.Error != "" || !rv.ClientDone {
		t.Fatalf("first run: %s %q", rv.Status, rv.Error)
	}
	node := stepOf(rv, true, "Product docs")
	if node.Status != "ok" || node.Counts["listed"] != 3 || node.Counts["uploaded"] != 3 || node.Counts["indexed"] != 3 || node.Counts["failed"] != 0 || node.Key != kids[0] {
		t.Errorf("node: %+v", node)
	}
	if rv.Counts["uploaded"] != 3 || stepOf(rv, false, "fetch").Status != "ok" || stepOf(rv, false, "kera").Status != "ok" {
		t.Errorf("run: %+v steps %+v", rv.Counts, *rv.Steps)
	}
	log := *e.logOf("docs-sync", run, node.Seq).Log
	for _, want := range []string{"+ fox.md", "+ jugs.md", "+ tea.md"} {
		if !strings.Contains(log, want) {
			t.Errorf("collection log lacks %q: %q", want, log)
		}
	}
	if n := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND replaces IS NULL AND status = 'completed'`, kids[0]); n != 3 {
		t.Errorf("%d files live", n)
	}
	if rv.StartedAt.IsZero() || rv.FinishedAt == nil || !strings.HasSuffix(fmt.Sprint(rv.StartedAt.Location()), "UTC") {
		t.Errorf("times: %v %v", rv.StartedAt, rv.FinishedAt)
	}

	// nothing changed: nothing is uploaded
	_, rv = p.push("Product docs", docs)
	if node = stepOf(rv, true, "Product docs"); rv.Status != "ok" || node.Counts["uploaded"] != 0 || node.Counts["unchanged"] != 3 {
		t.Errorf("rerun: %s %+v", rv.Status, node.Counts)
	}

	// one file changes, one goes away
	changed := map[string]string{"fox.md": "# Fox\nThe slow red fox naps under the old tree.", "jugs.md": docs["jugs.md"]}
	run, rv = p.push("Product docs", changed)
	node = stepOf(rv, true, "Product docs")
	if rv.Status != "ok" || node.Counts["uploaded"] != 1 || node.Counts["removed"] != 1 || node.Counts["unchanged"] != 1 {
		t.Errorf("third run: %s %q %+v", rv.Status, rv.Error, node.Counts)
	}
	log = *e.logOf("docs-sync", run, node.Seq).Log
	if !strings.Contains(log, "~ fox.md") || !strings.Contains(log, "- tea.md") {
		t.Errorf("log %q", log)
	}
	if n := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0]); n != 2 {
		t.Errorf("%d rows after the third run", n)
	}
	var live string
	if err := e.pool.QueryRow(context.Background(), `SELECT f.sha256 FROM knowledge_file kf JOIN file f ON f.id = kf.file_id WHERE kf.knowledge_id = $1 AND f.filename = 'fox.md'`, kids[0]).Scan(&live); err != nil || live != sum(changed["fox.md"]) {
		t.Errorf("the live fox.md is not the new version: %v", err)
	}
	if rv.Counts["removed"] != 1 {
		t.Errorf("run counts %+v", rv.Counts)
	}
}

func TestFailedScriptStepSkipsPrune(t *testing.T) {
	for name, finishErr := range map[string]string{"a failed step": "", "a finish error": "the fetcher crashed"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, true)
			p, kids := e.mkPipe("docs-sync", "Product docs")
			p.push("Product docs", docs)

			run := p.start(startIn{Steps: []string{"fetch", "kera"}})
			if finishErr == "" {
				p.steps(run, stepIn{Name: "fetch", Status: "failed", Error: "kept the previous docs"})
			}
			p.sync(run, "Product docs", map[string]string{"fox.md": docs["fox.md"]}, true)
			p.finish(run, finishErr)
			rv := p.closed(run)
			if rv.Status != "failed" {
				t.Fatalf("status %s", rv.Status)
			}
			if finishErr == "" && !strings.HasPrefix(rv.Error, "Failed at step fetch: kept the previous docs.") {
				t.Errorf("error %q", rv.Error)
			}
			if finishErr != "" && rv.Error != finishErr {
				t.Errorf("error %q", rv.Error)
			}
			if n := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0]); n != 3 {
				t.Errorf("%d files left; prune must not run after a failure", n)
			}
			node := stepOf(rv, true, "Product docs")
			if node.Counts["removed"] != 0 || !strings.Contains(*e.logOf("docs-sync", run, node.Seq).Log, "prune skipped: the script reported a failure") {
				t.Errorf("node %+v", node)
			}
			if stepOf(rv, false, "kera").Status != "skipped" {
				t.Errorf("a step that never ran is skipped: %+v", *rv.Steps)
			}
		})
	}
}

func TestPartialListingDoesNotPrune(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	p.push("Product docs", docs)
	run := p.start(startIn{})
	p.sync(run, "Product docs", map[string]string{"fox.md": docs["fox.md"]}, false)
	p.finish(run, "")
	if rv := p.closed(run); rv.Status != "ok" {
		t.Errorf("status %s", rv.Status)
	}
	if n := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0]); n != 3 {
		t.Errorf("%d files; a partial listing must not prune", n)
	}
}

func TestRefusedPruneAndAllowOnce(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	four := map[string]string{"a.md": "# A\nalpha text", "b.md": "# B\nbravo text", "c.md": "# C\ncharlie text", "d.md": "# D\ndelta text"}
	p.push("Product docs", four)

	// the listing is empty: the removal of everything is refused, the run is a warning
	run, rv := p.push("Product docs", map[string]string{})
	if rv.Status != "warning" || !strings.Contains(rv.Error, "refused") || e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0]) != 4 {
		t.Fatalf("refused prune: %s %q", rv.Status, rv.Error)
	}
	node := stepOf(rv, true, "Product docs")
	if node.Status != "warning" || node.Counts["refused"] != 4 || !strings.Contains(*e.logOf("docs-sync", run, node.Seq).Log, "prune refused") {
		t.Errorf("node %+v", node)
	}

	// an admin allows it once; the next ok run removes, and clears the flag
	e.call(e.admin, "POST", "/api/v1/admin/pipelines/docs-sync/once", map[string]any{"mass_delete": true}, http.StatusOK, nil)
	_, rv = p.push("Product docs", map[string]string{"a.md": four["a.md"]})
	if rv.Status != "ok" || e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0]) != 1 {
		t.Errorf("allowed prune: %s %q", rv.Status, rv.Error)
	}
	var out pipelineOut
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync", nil, http.StatusOK, &out)
	if out.AllowMassDelete {
		t.Error("the allow flag is still on")
	}
}

func TestAdoptOnce(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	// a file somebody added by hand
	f, err := files.For(e.s.a).Create(context.Background(), "root", "fox.md", "", strings.NewReader(docs["fox.md"]), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO knowledge_file (knowledge_id, file_id, status, origin) VALUES ($1, $2, 'completed', 'kera')`, kids[0], f.ID); err != nil {
		t.Fatal(err)
	}
	e.call(e.admin, "POST", "/api/v1/admin/pipelines/docs-sync/once", map[string]any{"adopt": true}, http.StatusOK, nil)

	run := p.start(startIn{DryRun: true})
	plan := p.sync(run, "Product docs", docs, true)
	if len(plan.Adopted) != 1 || len(plan.Upload) != 2 {
		t.Fatalf("dry plan: %+v", plan)
	}
	var origin string
	_ = e.pool.QueryRow(context.Background(), `SELECT origin FROM knowledge_file WHERE file_id = $1`, f.ID).Scan(&origin)
	if origin != "kera" {
		t.Errorf("a dry run adopted the file: origin %q", origin)
	}
	if code, body := e.do(p.tok, "PUT", p.path(fmt.Sprintf("/runs/%d/files?collection=%s&name=jugs.md&sha256=%s", run, "Product%20docs", sum(docs["jugs.md"]))), []byte(docs["jugs.md"])); code != http.StatusBadRequest || !strings.Contains(string(body), "dry_run") {
		t.Errorf("put in a dry run: %d %s", code, body)
	}
	p.finish(run, "")
	rv := p.closed(run)
	if rv.Status != "ok" || !rv.DryRun {
		t.Errorf("dry run: %s dry=%v", rv.Status, rv.DryRun)
	}
	var flags pipelineOut
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync", nil, http.StatusOK, &flags)
	if !flags.Adopt {
		t.Error("a dry run cleared the adopt flag")
	}

	_, rv = p.push("Product docs", docs)
	node := stepOf(rv, true, "Product docs")
	if rv.Status != "ok" || node.Counts["adopted"] != 1 || node.Counts["uploaded"] != 2 {
		t.Errorf("adopt run: %s %+v", rv.Status, node.Counts)
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT origin FROM knowledge_file WHERE file_id = $1`, f.ID).Scan(&origin)
	if origin != knowledge.SyncOrigin(flags.ID) {
		t.Errorf("origin %q after adopt", origin)
	}
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync", nil, http.StatusOK, &flags)
	if flags.Adopt {
		t.Error("the adopt flag survived an ok run")
	}
}

func TestBrokenFileFailsTheRun(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs", "Notes")
	content := map[string]string{"fox.md": docs["fox.md"], "broken.pdf": "not really a pdf"}
	run, rv := p.push("Product docs", content)
	if rv.Status != "failed" {
		t.Fatalf("status %s: %q", rv.Status, rv.Error)
	}
	node := stepOf(rv, true, "Product docs")
	if node.Status != "failed" || node.Counts["failed"] != 1 || node.Counts["indexed"] != 1 || !strings.Contains(node.Error, "broken.pdf") {
		t.Errorf("node %+v", node)
	}
	if !strings.Contains(rv.Error, "Product docs: 1 of 2 files could not be indexed: broken.pdf") {
		t.Errorf("run error %q", rv.Error)
	}
	if !strings.Contains(*e.logOf("docs-sync", run, node.Seq).Log, "FAILED broken.pdf") {
		t.Error("the failure is not in the log")
	}
	// the other collection never synced: skipped, not red
	if n := stepOf(rv, true, "Notes"); n.Status != "skipped" || n.Key != kids[1] {
		t.Errorf("untouched collection: %+v", n)
	}
	// the good file is live; the broken one still has a row that says failed
	if n := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND status = 'completed'`, kids[0]); n != 1 {
		t.Errorf("%d live files", n)
	}
}

// ---- the prune guard, one-shot flags, refused and unplanned uploads --------------------

// numbered makes n small documents named prefix00.md ... whose text differs.
func numbered(prefix string, n int) map[string]string {
	out := map[string]string{}
	for i := 0; i < n; i++ {
		out[fmt.Sprintf("%s%02d.md", prefix, i)] = fmt.Sprintf("# %s %d\nThe %s text number %d.", prefix, i, prefix, i)
	}
	return out
}

// A folder that replaces most of the collection is refused, even when it has as
// many files as the collection did: the files it uploads must not dilute the guard.
func TestWholesaleReplacementIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		keep, add int // old files kept in the listing, new files added
		stale     int64
	}{"ten other names": {0, 10, 10}, "six of ten replaced": {4, 6, 6}} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, true)
			p, kids := e.mkPipe("docs-sync", "Product docs")
			old := numbered("old", 10)
			p.push("Product docs", old)

			next := numbered("new", c.add)
			for i := 0; i < c.keep; i++ {
				n := fmt.Sprintf("old%02d.md", i)
				next[n] = old[n]
			}
			run, rv := p.push("Product docs", next)
			node := stepOf(rv, true, "Product docs")
			if rv.Status != "warning" || !strings.Contains(rv.Error, "refused") || node.Counts["refused"] != c.stale || node.Counts["removed"] != 0 {
				t.Fatalf("run %d: %s %q %+v", run, rv.Status, rv.Error, node.Counts)
			}
			if n := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND replaces IS NULL`, kids[0]); n != 10+c.add {
				t.Errorf("%d live files; the old ten must stay next to the %d new ones", n, c.add)
			}
		})
	}
}

func (e *tenv) flags() (adopt, mass bool) {
	e.t.Helper()
	var out pipelineOut
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync", nil, http.StatusOK, &out)
	return out.Adopt, out.AllowMassDelete
}

// "Allow removing once" is spent by the run that used it, whatever that run's
// verdict: a bad PDF turns the run red after the files are gone, and the next
// run (an empty, unmounted source) must be refused again.
func TestAllowOnceIsSpentByAFailedRun(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	p.push("Product docs", numbered("f", 12))
	e.call(e.admin, "POST", "/api/v1/admin/pipelines/docs-sync/once", map[string]any{"mass_delete": true}, http.StatusOK, nil)

	_, rv := p.push("Product docs", map[string]string{"f00.md": numbered("f", 12)["f00.md"], "broken.pdf": "not really a pdf"})
	if rv.Status != "failed" || stepOf(rv, true, "Product docs").Counts["removed"] != 11 {
		t.Fatalf("day 2: %s %q %+v", rv.Status, rv.Error, stepOf(rv, true, "Product docs").Counts)
	}
	if _, mass := e.flags(); mass {
		t.Error("the allow flag survived the run that used it")
	}
	before := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0])
	_, rv = p.push("Product docs", map[string]string{})
	if rv.Status != "warning" || !strings.Contains(rv.Error, "refused") || e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0]) != before {
		t.Errorf("day 3, an empty folder: %s %q", rv.Status, rv.Error)
	}
}

// Adopt is spent once a run adopted a file; a flag that no prune or adoption
// used stays for the next run.
func TestAdoptIsSpentByAFailedRunThatAdopted(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	f, err := files.For(e.s.a).Create(context.Background(), "root", "fox.md", "", strings.NewReader(docs["fox.md"]), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO knowledge_file (knowledge_id, file_id, status, origin) VALUES ($1, $2, 'completed', 'kera')`, kids[0], f.ID); err != nil {
		t.Fatal(err)
	}
	e.call(e.admin, "POST", "/api/v1/admin/pipelines/docs-sync/once", map[string]any{"adopt": true, "mass_delete": true}, http.StatusOK, nil)

	run := p.start(startIn{Steps: []string{"fetch", "kera"}})
	p.steps(run, stepIn{Name: "fetch", Status: "failed", Error: "kept the previous docs"})
	plan := p.sync(run, "Product docs", docs, true)
	for _, n := range plan.Upload {
		p.put(run, "Product docs", n, docs[n])
	}
	p.finish(run, "")
	if rv := p.closed(run); rv.Status != "failed" || stepOf(rv, true, "Product docs").Counts["adopted"] != 1 {
		t.Fatalf("run: %s %q", rv.Status, rv.Error)
	}
	adopt, mass := e.flags()
	if adopt {
		t.Error("the adopt flag survived a run that adopted")
	}
	if !mass {
		t.Error("the allow flag was spent by a run that removed nothing (the script failed, so no prune ran)")
	}
}

// An upload the server refuses for the file's own sake (over the size limit)
// makes the collection step, and so the run, fail; the old version stays live.
func TestRefusedUploadFailsTheRun(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	p.push("Product docs", docs)
	if err := e.s.a.Settings.Set(context.Background(), "files.max_mb", 1, false); err != nil {
		t.Fatal(err)
	}
	big := "# Fox\n" + strings.Repeat("x", 1<<20)
	content := map[string]string{"fox.md": big, "jugs.md": docs["jugs.md"], "tea.md": docs["tea.md"]}

	run := p.start(startIn{})
	plan := p.sync(run, "Product docs", content, true)
	if len(plan.Upload) != 1 || plan.Upload[0] != "fox.md" {
		t.Fatalf("plan %+v", plan)
	}
	code, body := e.do(p.tok, "PUT", p.path(fmt.Sprintf("/runs/%d/files?collection=Product%%20docs&name=fox.md&sha256=%s", run, sum(big))), []byte(big))
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("put: %d %s", code, body)
	}
	p.sync(run, "Product docs", content, true)
	p.finish(run, "")
	rv := p.closed(run)
	node := stepOf(rv, true, "Product docs")
	if rv.Status != "failed" || node.Status != "failed" || node.Counts["rejected"] != 1 || !strings.Contains(rv.Error, "refused on upload") {
		t.Fatalf("run: %s %q node %s %+v", rv.Status, rv.Error, node.Status, node.Counts)
	}
	if log := *e.logOf("docs-sync", run, node.Seq).Log; !strings.Contains(log, "FAILED fox.md: ") || !strings.Contains(log, "larger than the upload limit") {
		t.Errorf("the refusal is not in the log: %q", log)
	}
	var live string
	if err := e.pool.QueryRow(context.Background(), `SELECT f.sha256 FROM knowledge_file kf JOIN file f ON f.id = kf.file_id WHERE kf.knowledge_id = $1 AND f.filename = 'fox.md'`, kids[0]).Scan(&live); err != nil || live != sum(docs["fox.md"]) {
		t.Errorf("the old fox.md is not the live one: %v", err)
	}
}

// A file the plans did not ask for is refused: a looping or leaked token cannot
// store an unbounded number of files through a run.
func TestUploadMustBePlanned(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	put := func(run int64, name string) int {
		code, _ := e.do(p.tok, "PUT", p.path(fmt.Sprintf("/runs/%d/files?collection=Product%%20docs&name=%s&sha256=%s", run, name, sum(name))), []byte(name))
		return code
	}
	run := p.start(startIn{})
	if code := put(run, "early.md"); code != http.StatusConflict {
		t.Errorf("an upload before any listing: %d", code)
	}
	p.sync(run, "Product docs", map[string]string{"fox.md": "fox.md"}, true)
	ok := 0
	for i := 0; i < 15; i++ {
		name := "fox.md"
		if i > 0 {
			name = fmt.Sprintf("extra%02d.md", i)
		}
		if put(run, name) == http.StatusOK {
			ok++
		}
	}
	if ok != 11 { // the one listed file and ten more of slack for a retried upload
		t.Errorf("%d uploads accepted for a listing of one file", ok)
	}
	if n := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0]); n != ok {
		t.Errorf("%d rows for %d accepted uploads", n, ok)
	}
}

// The reason of a failed file is the extractor's own text: it is masked, stripped
// of control characters and cut like every other error text.
func TestFailureTextIsMaskedAndCapped(t *testing.T) {
	st := knowledge.SyncStatus{Failed: 2, Failures: []knowledge.SyncFailure{{Name: "a.pdf",
		Error: "bad header password: Hunter22Hunter \x1b[31mred\x1b[0m " + strings.Repeat("x", 10000)}}}
	got := failureText(st, 5)
	if strings.Contains(got, "Hunter22Hunter") || strings.Contains(got, "\x1b") || utf8.RuneCountInString(got) > maxErrorLen {
		t.Errorf("failure text: %d runes %q", utf8.RuneCountInString(got), got[:80])
	}
	if !strings.HasPrefix(got, "2 of 5 files could not be indexed: a.pdf: bad header password:") {
		t.Errorf("failure text starts %q", got[:60])
	}
}

// ---- ticker --------------------------------------------------------------------------

func TestTickIdle(t *testing.T) {
	e := newEnv(t, false)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	run := p.start(startIn{Steps: []string{"fetch", "index", "publish"}})
	p.steps(run, stepIn{Name: "fetch", Status: "ok"}, stepIn{Name: "index", Log: "working"})

	e.clk.add(29 * time.Minute)
	e.s.tick(context.Background(), e.clk.now())
	if rv := p.get(run); rv.FinishedAt != nil {
		t.Fatal("closed after 29 idle minutes")
	}
	p.steps(run) // a heartbeat starts the idle time again
	e.clk.add(29 * time.Minute)
	e.s.tick(context.Background(), e.clk.now())
	if rv := p.get(run); rv.FinishedAt != nil {
		t.Fatal("a heartbeat did not count")
	}
	e.clk.add(2 * time.Minute)
	e.s.tick(context.Background(), e.clk.now())
	rv := p.get(run)
	if rv.FinishedAt == nil || rv.Status != "failed" || rv.Error != "The script stopped reporting at 06:29 UTC." {
		t.Fatalf("idle run: %s %q", rv.Status, rv.Error)
	}
	idx := stepOf(rv, false, "index")
	if stepOf(rv, false, "fetch").Status != "ok" || idx.Status != "failed" || idx.Error != rv.Error ||
		stepOf(rv, false, "publish").Status != "skipped" || stepOf(rv, true, "Product docs").Status != "skipped" {
		t.Errorf("which node is red: %+v", *rv.Steps)
	}
	// the pipeline can start again
	p.start(startIn{})
}

func TestTickScriptCap(t *testing.T) {
	e := newEnv(t, false)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	run := p.start(startIn{Steps: []string{"loop"}})
	p.steps(run, stepIn{Name: "loop"})
	for i := 0; i < 9; i++ { // alive every 20 minutes, up to exactly 3 hours
		e.clk.add(20 * time.Minute)
		p.steps(run)
		e.s.tick(context.Background(), e.clk.now())
		if p.get(run).FinishedAt != nil {
			t.Fatalf("closed after %d minutes", (i+1)*20)
		}
	}
	e.clk.add(time.Minute)
	p.steps(run)
	e.s.tick(context.Background(), e.clk.now())
	rv := p.get(run)
	if rv.Status != "failed" || !strings.Contains(rv.Error, "still running after 3 hours") || stepOf(rv, false, "loop").Status != "failed" {
		t.Errorf("capped run: %s %q", rv.Status, rv.Error)
	}
}

func TestTickIndexingStall(t *testing.T) {
	e := newEnv(t, false) // nothing indexes
	p, _ := e.mkPipe("docs-sync", "Product docs", "Notes")
	run := p.start(startIn{Steps: []string{"fetch"}})
	p.steps(run, stepIn{Name: "fetch", Status: "ok"})
	plan := p.sync(run, "Product docs", map[string]string{"fox.md": docs["fox.md"]}, true)
	p.put(run, "Product docs", plan.Upload[0], docs["fox.md"])
	p.sync(run, "Notes", map[string]string{}, true)
	p.finish(run, "")

	rv := p.get(run)
	if rv.FinishedAt != nil || stepOf(rv, true, "Product docs").Counts["waiting"] != 1 || stepOf(rv, true, "Notes").Status != "ok" {
		t.Fatalf("while waiting: closed=%v %+v", rv.FinishedAt != nil, *rv.Steps)
	}
	e.clk.add(29 * time.Minute)
	e.s.tick(context.Background(), e.clk.now())
	if p.get(run).FinishedAt != nil {
		t.Fatal("closed before 30 minutes without progress")
	}
	e.clk.add(2 * time.Minute)
	e.s.tick(context.Background(), e.clk.now())
	rv = p.get(run)
	node := stepOf(rv, true, "Product docs")
	if rv.Status != "failed" || node.Status != "failed" || !strings.Contains(node.Error, "1 files still waiting to be indexed: fox.md") ||
		stepOf(rv, true, "Notes").Status != "ok" || stepOf(rv, false, "fetch").Status != "ok" || !strings.Contains(rv.Error, "Indexing made no progress for 30 minutes") ||
		!strings.Contains(rv.Error, "fox.md") {
		t.Errorf("stalled run: %s %q %+v", rv.Status, rv.Error, *rv.Steps)
	}
	if !strings.Contains(*e.logOf("docs-sync", run, node.Seq).Log, "still waiting") {
		t.Error("the waiting files are not in the log")
	}
}

func TestTickProgressKeepsTheRunOpen(t *testing.T) {
	e := newEnv(t, false)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	run := p.start(startIn{})
	two := map[string]string{"fox.md": docs["fox.md"], "tea.md": docs["tea.md"]}
	plan := p.sync(run, "Product docs", two, true)
	for _, n := range plan.Upload {
		p.put(run, "Product docs", n, two[n])
	}
	p.finish(run, "")
	for _, n := range plan.Upload { // wait for text extraction
		var fid string
		_ = e.pool.QueryRow(context.Background(), `SELECT file_id FROM knowledge_file kf JOIN file f ON f.id = kf.file_id WHERE f.filename = $1`, n).Scan(&fid)
		if _, err := files.For(e.s.a).Wait(context.Background(), fid, 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	e.clk.add(25 * time.Minute)
	if did, err := e.k.Step(context.Background()); err != nil || !did { // one file is indexed
		t.Fatal(did, err)
	}
	e.s.tick(context.Background(), e.clk.now()) // progress: the idle time starts over
	e.clk.add(25 * time.Minute)
	e.s.tick(context.Background(), e.clk.now())
	if p.get(run).FinishedAt != nil {
		t.Fatal("the run was closed 25 minutes after the last progress")
	}
	if _, err := e.k.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.s.tick(context.Background(), e.clk.now())
	if rv := p.get(run); rv.FinishedAt == nil || rv.Status != "ok" {
		t.Errorf("after the last file: %s", rv.Status)
	}
}

// ---- lateness, health, flags -----------------------------------------------------------

func TestLateAndHealth(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	e.call(e.admin, "PUT", "/api/v1/admin/pipelines/docs-sync", map[string]any{"every_minutes": 60}, http.StatusOK, nil)
	health := func(want int) health {
		t.Helper()
		var h health
		e.call(p.tok, "GET", p.path("/health"), nil, want, &h)
		return h
	}
	late := func() bool {
		t.Helper()
		var out pipelineOut
		e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync", nil, http.StatusOK, &out)
		return out.Late
	}

	if h := health(http.StatusOK); h.Status != "never_run" || h.LastOK != nil {
		t.Errorf("fresh pipeline: %+v", h)
	}
	e.clk.add(76 * time.Minute)
	if h := health(http.StatusServiceUnavailable); h.Status != "late" || !strings.Contains(h.Detail, "expected every 60 min") || !late() {
		t.Errorf("never run and overdue: %+v", h)
	}

	run := p.start(startIn{})
	p.sync(run, "Product docs", map[string]string{}, true) // syncs the one target
	p.finish(run, "")
	if rv := p.closed(run); rv.Status != "ok" {
		t.Fatal(rv.Status)
	}
	t0 := e.clk.now()
	if h := health(http.StatusOK); h.Status != "ok" || h.LastOK == nil || !h.LastOK.Equal(t0) || late() {
		t.Errorf("after a good run: %+v", h)
	}

	// a run that syncs no collection does not reset the clock
	e.clk.add(50 * time.Minute)
	run = p.start(startIn{})
	p.steps(run, stepIn{Name: "only-a-script", Status: "ok"})
	p.finish(run, "")
	if rv := p.closed(run); rv.Status != "ok" {
		t.Fatal(rv.Status)
	}
	e.clk.add(24 * time.Minute) // 74 minutes after the last good run: on time
	if late() {
		t.Error("late at 74 minutes")
	}
	e.clk.add(2 * time.Minute) // 76 minutes
	if h := health(http.StatusServiceUnavailable); h.Status != "late" || !late() || !h.LastOK.Equal(t0) {
		t.Errorf("a run that synced nothing reset the clock: %+v", h)
	}
	// a dry run does not either
	run = p.start(startIn{DryRun: true})
	p.sync(run, "Product docs", map[string]string{}, true)
	p.finish(run, "")
	p.closed(run)
	if !late() {
		t.Error("a dry run reset the clock")
	}

	// a real sync does
	run = p.start(startIn{})
	p.sync(run, "Product docs", map[string]string{}, true)
	p.finish(run, "")
	p.closed(run)
	if late() {
		t.Error("late after a good run")
	}
	health(http.StatusOK)

	// the last closed run failed: 503 with its error
	run = p.start(startIn{})
	p.finish(run, "the fetcher crashed")
	p.closed(run)
	if h := health(http.StatusServiceUnavailable); h.Status != "failed" || !strings.Contains(h.Detail, "the fetcher crashed") || h.LastOK == nil {
		t.Errorf("after a failed run: %+v", h)
	}
	// a warning is still healthy
	run = p.start(startIn{})
	p.steps(run, stepIn{Name: "scan", Status: "warning", Error: "leftovers"})
	p.sync(run, "Product docs", map[string]string{}, true)
	p.finish(run, "")
	if rv := p.closed(run); rv.Status != "warning" {
		t.Fatal(rv.Status)
	}
	if h := health(http.StatusOK); h.Status != "warning" {
		t.Errorf("after a warning: %+v", h)
	}
	// an admin may not read the health URL
	if code, _ := e.do(e.admin, "GET", p.path("/health"), nil); code != http.StatusUnauthorized {
		t.Errorf("admin on health: %d", code)
	}
}

func TestOneShotFlagsClearOnOK(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	flags := func() (adopt, mass bool) {
		var out pipelineOut
		e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync", nil, http.StatusOK, &out)
		return out.Adopt, out.AllowMassDelete
	}
	e.call(e.admin, "POST", "/api/v1/admin/pipelines/docs-sync/once", map[string]any{"adopt": true, "mass_delete": true}, http.StatusOK, nil)
	if a, m := flags(); !a || !m {
		t.Fatal("flags not set")
	}
	run := p.start(startIn{})
	p.finish(run, "boom")
	p.closed(run)
	if a, m := flags(); !a || !m {
		t.Error("a failed run cleared the flags")
	}
	run = p.start(startIn{DryRun: true})
	p.finish(run, "")
	p.closed(run)
	if a, m := flags(); !a || !m {
		t.Error("a dry run cleared the flags")
	}
	run = p.start(startIn{})
	p.finish(run, "")
	if rv := p.closed(run); rv.Status != "ok" {
		t.Fatal(rv.Status)
	}
	if a, m := flags(); a || m {
		t.Error("an ok run left the flags on")
	}
}

// ---- admin ---------------------------------------------------------------------------

func TestAdminCreateValidation(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	c1, _ := e.k.CreateFor(ctx, "root", "Same", "")
	c2, _ := e.k.CreateFor(ctx, "root", "Same", "")
	c3, _ := e.k.CreateFor(ctx, "root", "Unique", "")
	for name, tc := range map[string]struct {
		body map[string]any
		code string
	}{
		"bad name":                {map[string]any{"name": "Docs Sync"}, "bad_name"},
		"empty name":              {map[string]any{"name": ""}, "bad_name"},
		"two new collections":     {map[string]any{"name": "a", "new_collections": []string{"X", "X"}}, "duplicate_target_name"},
		"existing and new":        {map[string]any{"name": "a", "targets": []string{c3.ID}, "new_collections": []string{"Unique"}}, "duplicate_target_name"},
		"two collections by id":   {map[string]any{"name": "a", "targets": []string{c1.ID, c2.ID}}, "duplicate_target_name"},
		"ambiguous name":          {map[string]any{"name": "a", "targets": []string{"Same"}}, "ambiguous_collection"},
		"unknown collection":      {map[string]any{"name": "a", "targets": []string{"nothing"}}, "unknown_collection"},
		"negative expected every": {map[string]any{"name": "a", "every_minutes": -1}, "bad_every_minutes"},
	} {
		code, body := e.do(e.admin, "POST", "/api/v1/admin/pipelines", tc.body)
		if code != http.StatusBadRequest || !strings.Contains(string(body), tc.code) {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if e.count(`SELECT count(*) FROM pipeline`) != 0 || e.count(`SELECT count(*) FROM knowledge WHERE name IN ('X', 'a')`) != 0 {
		t.Error("a rejected form left rows behind")
	}
	// by name when unambiguous, by id otherwise; a duplicate pipeline name is refused
	var out struct{ Pipeline pipelineOut }
	e.call(e.admin, "POST", "/api/v1/admin/pipelines", map[string]any{"name": "ok", "targets": []string{"Unique", c1.ID}}, http.StatusCreated, &out)
	if len(out.Pipeline.Targets) != 2 || out.Pipeline.Targets[0].ID != c3.ID || out.Pipeline.Owner.ID != "root" || out.Pipeline.TokenPrefix == "" {
		t.Errorf("created: %+v", out.Pipeline)
	}
	if code, _ := e.do(e.admin, "POST", "/api/v1/admin/pipelines", map[string]any{"name": "ok"}); code != http.StatusBadRequest {
		t.Errorf("duplicate name: %d", code)
	}
	// an update that makes two targets share a name is refused as well
	if code, _ := e.do(e.admin, "PUT", "/api/v1/admin/pipelines/ok", map[string]any{"targets": []string{c1.ID, c2.ID}}); code != http.StatusBadRequest {
		t.Errorf("update with a duplicate: %d", code)
	}
}

func TestAdminRunsAndSteps(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	var ids []int64
	for i := 0; i < 3; i++ {
		run := p.start(startIn{Steps: []string{"fetch"}})
		p.steps(run, stepIn{Name: "fetch", Status: "ok", Log: fmt.Sprintf("run %d fetched", i)})
		p.finish(run, "")
		p.closed(run)
		ids = append(ids, run)
	}
	var page struct {
		Runs []runOut
		More bool
	}
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync/runs?limit=2", nil, http.StatusOK, &page)
	if len(page.Runs) != 2 || !page.More || page.Runs[0].ID != ids[2] || page.Runs[1].ID != ids[1] || page.Runs[0].Steps != nil {
		t.Errorf("first page: %+v", page)
	}
	e.call(e.admin, "GET", fmt.Sprintf("/api/v1/admin/pipelines/docs-sync/runs?limit=2&before=%d", ids[1]), nil, http.StatusOK, &page)
	if len(page.Runs) != 1 || page.More || page.Runs[0].ID != ids[0] {
		t.Errorf("second page: %+v", page)
	}
	var one runOut
	e.call(e.admin, "GET", fmt.Sprintf("/api/v1/admin/pipelines/docs-sync/runs/%d", ids[0]), nil, http.StatusOK, &one)
	if one.Steps == nil || len(*one.Steps) != 2 || (*one.Steps)[0].Log != nil || one.Pipeline != "docs-sync" {
		t.Errorf("run view: %+v", one)
	}
	st := e.logOf("docs-sync", ids[0], 1)
	if st.Log == nil || !strings.Contains(*st.Log, "run 0 fetched") {
		t.Errorf("step log: %+v", st)
	}
	for _, path := range []string{"/runs/999999", "/runs/x", fmt.Sprintf("/runs/%d/steps/77", ids[0]), "/runs/" + fmt.Sprint(ids[0]) + "/steps/x"} {
		if code, _ := e.do(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync"+path, nil); code != http.StatusNotFound {
			t.Errorf("GET %s: %d", path, code)
		}
	}
	// another pipeline's run is not reachable through this name
	e.mkPipe("other-sync", "Other docs")
	if code, _ := e.do(e.admin, "GET", fmt.Sprintf("/api/v1/admin/pipelines/other-sync/runs/%d", ids[0]), nil); code != http.StatusNotFound {
		t.Errorf("a run through another pipeline: %d", code)
	}
}

func TestAdminCloseStuckRun(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	run := p.start(startIn{Steps: []string{"fetch", "next"}})
	p.steps(run, stepIn{Name: "fetch"})
	e.clk.add(7 * time.Minute)
	var rv runOut
	e.call(e.admin, "POST", fmt.Sprintf("/api/v1/admin/pipelines/docs-sync/runs/%d/close", run), nil, http.StatusOK, &rv)
	if rv.Status != "failed" || rv.Error != "Closed by an admin at 06:07 UTC." || stepOf(rv, false, "fetch").Status != "failed" || stepOf(rv, false, "next").Status != "skipped" {
		t.Errorf("closed run: %s %q %+v", rv.Status, rv.Error, *rv.Steps)
	}
	e.call(e.admin, "POST", fmt.Sprintf("/api/v1/admin/pipelines/docs-sync/runs/%d/close", run), nil, http.StatusOK, &rv) // closing twice is fine
	p.start(startIn{})
}

func TestRunsAreTrimmed(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync")
	var first int64
	for i := 0; i < maxRuns+5; i++ {
		run := p.start(startIn{})
		if i == 0 {
			first = run
		}
		p.finish(run, "")
	}
	if n := e.count(`SELECT count(*) FROM pipeline_run`); n != maxRuns {
		t.Errorf("%d runs kept, want %d", n, maxRuns)
	}
	if e.count(`SELECT count(*) FROM pipeline_run WHERE id = $1`, first) != 0 || e.count(`SELECT count(*) FROM pipeline_step WHERE run_id = $1`, first) != 0 {
		t.Error("the oldest run is still there")
	}
}

func TestDeleteHandsFilesBack(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	p.push("Product docs", docs)
	e.call(e.admin, "DELETE", "/api/v1/admin/pipelines/docs-sync", nil, http.StatusOK, nil)
	if e.count(`SELECT count(*) FROM pipeline`) != 0 || e.count(`SELECT count(*) FROM pipeline_run`) != 0 {
		t.Error("the pipeline or its runs are still there")
	}
	if e.count(`SELECT count(*) FROM knowledge WHERE id = $1`, kids[0]) != 1 || e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND origin = 'kera'`, kids[0]) != 3 {
		t.Error("the collection or its files were removed; they must stay, owned by no pipeline")
	}
	if code, _ := e.do(p.tok, "GET", p.path("/health"), nil); code != http.StatusUnauthorized {
		t.Errorf("token of a deleted pipeline: %d", code)
	}
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync", nil, http.StatusNotFound, nil)
}

func TestOwnerDeleteAndChange(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	p, kids := e.mkPipe("docs-sync", "Product docs")
	other, err := e.k.CreateFor(ctx, "alice", "Alice notes", "")
	if err != nil {
		t.Fatal(err)
	}
	e.call(e.admin, "PUT", "/api/v1/admin/pipelines/docs-sync", map[string]any{"targets": []string{kids[0], other.ID}}, http.StatusOK, nil)
	p.push("Product docs", docs)

	var ae *api.Error
	if found, err := identity.DeleteUser(ctx, e.pool, "root"); found || !errors.As(err, &ae) || ae.Status != http.StatusConflict || ae.Code != "pipeline_owner" {
		t.Fatalf("deleting the owner: found=%v err=%v", found, err)
	}
	if code, _ := e.do(e.admin, "PUT", "/api/v1/admin/pipelines/docs-sync", map[string]any{"owner_id": "nobody"}); code != http.StatusBadRequest {
		t.Errorf("unknown new owner: %d", code)
	}

	var out pipelineOut
	e.call(e.admin, "PUT", "/api/v1/admin/pipelines/docs-sync", map[string]any{"owner_id": "alice"}, http.StatusOK, &out)
	if out.Owner.ID != "alice" {
		t.Errorf("owner %+v", out.Owner)
	}
	if n := e.count(`SELECT count(*) FROM file f JOIN knowledge_file kf ON kf.file_id = f.id WHERE kf.knowledge_id = $1 AND f.user_id = 'alice'`, kids[0]); n != 3 {
		t.Errorf("%d of 3 files moved to the new owner", n)
	}
	if e.count(`SELECT count(*) FROM knowledge WHERE id = $1 AND user_id = 'alice'`, kids[0]) != 1 {
		t.Error("the collection the old owner held did not move")
	}
	if e.count(`SELECT count(*) FROM knowledge WHERE id = $1 AND user_id = 'alice'`, other.ID) != 1 {
		t.Error("a collection of the new owner changed hands")
	}
	// new uploads belong to the new owner, and the old owner can go now
	p.push("Product docs", map[string]string{"new.md": "# New\nfresh text here"})
	if n := e.count(`SELECT count(*) FROM file f JOIN knowledge_file kf ON kf.file_id = f.id WHERE kf.knowledge_id = $1 AND f.user_id = 'root'`, kids[0]); n != 0 {
		t.Errorf("%d files still belong to the old owner", n)
	}
	if found, err := identity.DeleteUser(ctx, e.pool, "root"); !found || err != nil {
		t.Errorf("deleting the former owner: found=%v err=%v", found, err)
	}
	if found, err := identity.DeleteUser(ctx, e.pool, "alice"); found || !errors.As(err, &ae) || ae.Code != "pipeline_owner" {
		t.Errorf("deleting the new owner: found=%v err=%v", found, err)
	}
}

func TestFinishTwiceAndUnknownRun(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	run := p.start(startIn{})
	first := p.finish(run, "")
	second := p.finish(run, "ignored")
	if first.Status != "ok" || second.Status != "ok" || second.Error != "" {
		t.Errorf("finishing twice: %q %q (%q)", first.Status, second.Status, second.Error)
	}
	if code, _ := e.do(p.tok, "GET", p.path("/runs/424242"), nil); code != http.StatusNotFound {
		t.Errorf("unknown run: %d", code)
	}
	if code, _ := e.do(p.tok, "GET", p.path("/runs/abc"), nil); code != http.StatusNotFound {
		t.Errorf("bad run id: %d", code)
	}
}

func TestStartReturnsLinkAndCollections(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs", "Notes")
	var out struct {
		Run         int64
		URL         string
		Collections []map[string]string
		DryRun      bool `json:"dry_run"`
	}
	e.call(p.tok, "POST", p.path("/runs"), startIn{Steps: []string{"a", "b", "a"}, DryRun: true}, http.StatusCreated, &out)
	if out.URL != fmt.Sprintf("http://kera.example.com/admin/pipelines/docs-sync/%d", out.Run) || len(out.Collections) != 2 || out.Collections[1]["id"] != kids[1] || !out.DryRun {
		t.Errorf("start answer: %+v", out)
	}
	rv := p.get(out.Run)
	if rv.Status != "running" || len(*rv.Steps) != 4 || (*rv.Steps)[0].Name != "a" || (*rv.Steps)[1].Name != "b" || (*rv.Steps)[2].Seq != keraSeqBase || (*rv.Steps)[3].Status != "pending" {
		t.Errorf("fresh run: %+v", *rv.Steps)
	}
}
