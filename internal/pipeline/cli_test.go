package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/version"
)

func init() { retryDelay = time.Millisecond }

// syncBuf is a buffer several goroutines may write.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func exitOf(err error) int {
	var ee *ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.Code
	}
	return -1
}

// ---- marker parser -------------------------------------------------------------------

const testMark = "::kera-0123456789abcdef::"

// feed runs lines through a parser and returns what it reported and echoed.
func feed(lines ...string) (items []stepIn, echoed []string, p *parser) {
	p = newParser(testMark, func(in stepIn) { items = append(items, in) }, func(s string) { echoed = append(echoed, s) })
	for _, l := range lines {
		p.line(l)
	}
	return items, echoed, p
}

func TestParser(t *testing.T) {
	for _, c := range []struct {
		name  string
		lines []string
		want  []stepIn
		echo  []string
	}{
		{"lines before the first marker go to start",
			[]string{"hello", "world"},
			[]stepIn{{Name: "start", Status: "running"}, {Name: "start", Log: "hello\n"}, {Name: "start", Log: "world\n"}},
			[]string{"hello", "world"}},
		{"steps are declared pending and a step ends the previous one",
			[]string{testMark + "steps a b", testMark + "step a", "in a", testMark + "step b", "in b"},
			[]stepIn{{Name: "a", Status: "pending"}, {Name: "b", Status: "pending"}, {Name: "a", Status: "running"}, {Name: "a", Log: "in a\n"},
				{Name: "a", Status: "ok"}, {Name: "b", Status: "running"}, {Name: "b", Log: "in b\n"}},
			[]string{"[step] a", "in a", "[step] b", "in b"}},
		{"lines before the first step make an implicit start that comes before the declared steps",
			[]string{testMark + "steps a b", "hello", testMark + "step a"},
			[]stepIn{{Name: "start", Status: "running"}, {Name: "a", Status: "pending"}, {Name: "b", Status: "pending"}, {Name: "start", Log: "hello\n"},
				{Name: "start", Status: "ok"}, {Name: "a", Status: "running"}},
			[]string{"hello", "[step] a"}},
		{"steps declared while a step is open are reported at once",
			[]string{testMark + "step a", testMark + "steps b c"},
			[]stepIn{{Name: "a", Status: "running"}, {Name: "b", Status: "pending"}, {Name: "c", Status: "pending"}},
			[]string{"[step] a"}},
		{"warn, fail and skip mark the current step and the script goes on",
			[]string{testMark + "step a", testMark + "warn slow", testMark + "fail broken source", "more", testMark + "step b", testMark + "skip no changes"},
			[]stepIn{{Name: "a", Status: "running"}, {Name: "a", Status: "warning", Error: "slow"}, {Name: "a", Status: "failed", Error: "broken source"},
				{Name: "a", Log: "more\n"}, {Name: "b", Status: "running"}, {Name: "b", Status: "skipped", Error: "no changes"}},
			[]string{"[step] a", "[warn] slow", "[fail] broken source", "more", "[step] b", "[skip] no changes"}},
		{"re-entering a closed step is ignored",
			[]string{testMark + "step a", testMark + "step b", testMark + "step a", "still in b"},
			[]stepIn{{Name: "a", Status: "running"}, {Name: "a", Status: "ok"}, {Name: "b", Status: "running"}, {Name: "b", Log: "still in b\n"}},
			[]string{"[step] a", "[step] b", "still in b"}},
		{"a step that failed is not re-entered either",
			[]string{testMark + "step a", testMark + "fail x", testMark + "step b", testMark + "step a", "text"},
			[]stepIn{{Name: "a", Status: "running"}, {Name: "a", Status: "failed", Error: "x"}, {Name: "b", Status: "running"}, {Name: "b", Log: "text\n"}},
			[]string{"[step] a", "[fail] x", "[step] b", "text"}},
		{"count",
			[]string{testMark + "step a", testMark + "count files=12", testMark + "count bad", testMark + "count x=y"},
			[]stepIn{{Name: "a", Status: "running"}, {Name: "a", Counts: map[string]int64{"files": 12}},
				{Name: "a", Log: testMark + "count bad\n"}, {Name: "a", Log: testMark + "count x=y\n"}},
			[]string{"[step] a", testMark + "count bad", testMark + "count x=y"}},
		{"a forged marker without the nonce is plain log",
			[]string{testMark + "step a", "::kera-ffffffffffffffff::step evil", "::kera-ffffffffffffffff::fail forged", "::step also", testMark + "dance now"},
			[]stepIn{{Name: "a", Status: "running"}, {Name: "a", Log: "::kera-ffffffffffffffff::step evil\n"}, {Name: "a", Log: "::kera-ffffffffffffffff::fail forged\n"},
				{Name: "a", Log: "::step also\n"}, {Name: "a", Log: testMark + "dance now\n"}},
			[]string{"[step] a", "::kera-ffffffffffffffff::step evil", "::kera-ffffffffffffffff::fail forged", "::step also", testMark + "dance now"}},
		{"a marker in the middle of a line is plain log",
			[]string{testMark + "step a", "text " + testMark + "fail inline"},
			[]stepIn{{Name: "a", Status: "running"}, {Name: "a", Log: "text " + testMark + "fail inline\n"}},
			[]string{"[step] a", "text " + testMark + "fail inline"}},
		{"end closes the current step and later lines still belong to it",
			[]string{testMark + "step a", testMark + "end", "after"},
			[]stepIn{{Name: "a", Status: "running"}, {Name: "a", Status: "ok"}, {Name: "a", Log: "after\n"}},
			[]string{"[step] a", "after"}},
		{"a marker first opens start when no step is open",
			[]string{testMark + "warn early"},
			[]stepIn{{Name: "start", Status: "running"}, {Name: "start", Status: "warning", Error: "early"}},
			[]string{"[warn] early"}},
	} {
		items, echoed, _ := feed(c.lines...)
		if !equalItems(items, c.want) {
			t.Errorf("%s: reports\n got %+v\nwant %+v", c.name, items, c.want)
		}
		if !slices.Equal(echoed, c.echo) {
			t.Errorf("%s: echo\n got %q\nwant %q", c.name, echoed, c.echo)
		}
	}
}

// A script that declares its steps and then ends without a word still reports them, so the run can skip them.
func TestParserDeclaredOnlyIsReported(t *testing.T) {
	var items []stepIn
	p := newParser(testMark, func(in stepIn) { items = append(items, in) }, func(string) {})
	p.line(testMark + "steps a b")
	if len(items) != 0 {
		t.Fatalf("nothing is reported before something happens: %+v", items)
	}
	p.end(0)
	if want := []stepIn{{Name: "a", Status: "pending"}, {Name: "b", Status: "pending"}}; !equalItems(items, want) {
		t.Errorf("reports\n got %+v\nwant %+v", items, want)
	}
}

// Running is a stepIn literal helper: {Name: "b", Status: "running"} reads as status running.
func Running() (string, string) { return "", "" }

func equalItems(a, b []stepIn) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func TestParserExit(t *testing.T) {
	for _, c := range []struct {
		name    string
		lines   []string
		code    int
		runErr  string
		failed  []string // steps reported failed after the exit
		okSteps []string // steps reported ok by the exit
	}{
		{"success closes the current step", []string{testMark + "step a"}, 0, "", nil, []string{"a"}},
		{"no output and success", nil, 0, "", nil, nil},
		{"a non-zero exit fails the current step", []string{testMark + "step a", "oops"}, 3, "Failed at step a: exit code 3.", []string{"a"}, nil},
		{"no output and a non-zero exit fails start", nil, 7, "Failed at step start: exit code 7.", []string{"start"}, nil},
		{"a script that printed end keeps its failures for the exit: only the failed steps are blamed",
			[]string{testMark + "step fetch-a", testMark + "fail kept the old docs", testMark + "step kera", "uploaded", testMark + "end"}, 1,
			"Failed at step fetch-a: kept the old docs.", nil, nil},
		{"two failed steps and the deferred exit",
			[]string{testMark + "step a", testMark + "fail one", testMark + "step b", testMark + "fail two", testMark + "step c", testMark + "end"}, 1,
			"Failed at step a: one. Failed at step b: two.", nil, nil},
		{"a soft failure and then a crash in a later step: that step is blamed too",
			[]string{testMark + "step fetch-qas", testMark + "fail kept the previous QA docs", testMark + "step inventory"}, 1,
			"Failed at step fetch-qas: kept the previous QA docs. Failed at step inventory: exit code 1.", []string{"inventory"}, nil},
		{"a crash right after a warning is blamed on that step even when another step failed",
			[]string{testMark + "step a", testMark + "fail one", testMark + "step b", testMark + "warn hmm"}, 4,
			"Failed at step a: one. Failed at step b: exit code 4.", []string{"b"}, nil},
		{"the deferred exit after the failing step itself is not blamed twice",
			[]string{testMark + "step a", testMark + "fail one"}, 1,
			"Failed at step a: one.", nil, nil},
		{"a failed step with no text", []string{testMark + "step a", testMark + "fail"}, 2, "Failed at step a.", nil, nil},
		{"a warning step then a non-zero exit fails it", []string{testMark + "step a", testMark + "warn hmm"}, 5, "Failed at step a: exit code 5.", []string{"a"}, nil},
		{"a failed step and a zero exit leaves the run to the server", []string{testMark + "step a", testMark + "fail x"}, 0, "", nil, nil},
	} {
		var after []stepIn
		items, _, p := feed(c.lines...)
		p.sink = func(in stepIn) { after = append(after, in) }
		runErr := p.end(c.code)
		_ = items
		if runErr != c.runErr {
			t.Errorf("%s: run error %q, want %q", c.name, runErr, c.runErr)
		}
		var failed, ok []string
		for _, in := range after {
			switch in.Status {
			case "failed":
				failed = append(failed, in.Name)
			case "ok":
				ok = append(ok, in.Name)
			}
		}
		if !slices.Equal(failed, c.failed) || !slices.Equal(ok, c.okSteps) {
			t.Errorf("%s: after the exit failed %v ok %v, want failed %v ok %v", c.name, failed, ok, c.failed, c.okSteps)
		}
	}
}

func TestSemver(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.3", "v1.2.2", true}, {"v1.10.0", "v1.9.9", true}, {"v1.2.3", "v1.2.3", false}, {"v1.2.2", "v1.2.3", false},
		{"1.3.0-rc1", "v1.2.0", true}, {"dev", "v1.0.0", false}, {"v1.0.0", "dev", false}, {"", "", false},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// ---- the wrapper against a fake server -------------------------------------------------

type fakeKera struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	status  int // answer of a run start: 0 = 201
	version string
	failN   int // steps reports answered 503 before one succeeds
	reports [][]stepIn
	finish  []string
}

func newFake(t *testing.T) *fakeKera {
	f := &fakeKera{t: t, version: "dev"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKera) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer kp-test" {
		http.Error(w, `{"detail":"no","code":"unauthenticated"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/runs"):
		if f.status == http.StatusConflict {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"detail":"this pipeline already has a run in progress (run 5, started 2026-03-10 06:00, last report 06:01 UTC)","code":"run_active","run":5}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"run":7,"url":"http://kera.example.com/admin/pipelines/docs-sync/7","version":%q}`, f.version)
	case strings.HasSuffix(r.URL.Path, "/steps"):
		if f.failN > 0 {
			f.failN--
			http.Error(w, `{"detail":"busy","code":"x"}`, http.StatusServiceUnavailable)
			return
		}
		var in struct{ Steps []stepIn }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.reports = append(f.reports, in.Steps)
		fmt.Fprint(w, `{"ok":true}`)
	case strings.HasSuffix(r.URL.Path, "/finish"):
		var in struct{ Error string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.finish = append(f.finish, in.Error)
		now := time.Now().UTC()
		_ = json.NewEncoder(w).Encode(runOut{ID: 7, Status: "ok", FinishedAt: &now})
	default:
		http.NotFound(w, r)
	}
}

// agg is what the fake learned about one step.
type agg struct {
	status, err, log string
	counts           map[string]int64
}

func (f *fakeKera) steps() (map[string]*agg, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]*agg{}
	var order []string
	for _, rep := range f.reports {
		for _, in := range rep {
			a := out[in.Name]
			if a == nil {
				a = &agg{counts: map[string]int64{}}
				out[in.Name] = a
				order = append(order, in.Name)
			}
			if in.Status != "" {
				a.status = in.Status
			}
			if in.Error != "" {
				a.err = in.Error
			}
			a.log += in.Log
			for k, v := range in.Counts {
				a.counts[k] = v
			}
		}
	}
	return out, order
}

func (f *fakeKera) env() cliEnv {
	return cliEnv{URL: f.srv.URL, Pipeline: "docs-sync", Token: "kp-test", Environ: os.Environ()}
}

var fast = timing{flush: 20 * time.Millisecond, beat: 60 * time.Millisecond, poll: 10 * time.Millisecond}

func wrap(t *testing.T, e cliEnv, tm timing, script string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb syncBuf
	err = command(context.Background(), []string{"run", "--", "sh", "-c", script}, e, tm, &out, &errb)
	return out.String(), errb.String(), err
}

func TestWrapperReportsAndTees(t *testing.T) {
	f := newFake(t)
	f.version = "v99.0.0"
	defer func(v string) { version.Version = v }(version.Version)
	version.Version = "v1.0.0"
	script := `echo before
echo "${KERA_MARK}steps fetch kera"
echo "${KERA_MARK}step fetch"
echo "from stdout"
echo "from stderr" >&2
echo "${KERA_MARK}warn the source is slow"
echo "${KERA_MARK}step kera"
echo "${KERA_MARK}count files=3"
echo "::kera-forged::step evil"
echo "${KERA_MARK}fail kept the old docs"
test -n "$KERA_RUN" && echo "run is $KERA_RUN"
exit 4`
	stdout, stderr, err := wrap(t, f.env(), fast, script)
	if exitOf(err) != 4 {
		t.Fatalf("exit: %v", err)
	}
	for _, want := range []string{"from stdout", "from stderr", "before", "[step] fetch", "[warn] the source is slow", "[fail] kept the old docs", "::kera-forged::step evil", "run is 7",
		"run 7 http://kera.example.com/admin/pipelines/docs-sync/7", "run 7 ok"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "::kera-0") || strings.Contains(stdout, "count files") {
		t.Errorf("a marker line leaked into the output:\n%s", stdout)
	}
	if !strings.Contains(stderr, "server is version v99.0.0") {
		t.Errorf("no version warning: %q", stderr)
	}
	steps, order := f.steps()
	if !slices.Equal(order, []string{"start", "fetch", "kera"}) && !slices.Equal(order, []string{"fetch", "kera", "start"}) {
		t.Errorf("step order %v", order)
	}
	if steps["start"].status != "ok" || !strings.Contains(steps["start"].log, "before") {
		t.Errorf("start: %+v", steps["start"])
	}
	if s := steps["fetch"]; s.status != "warning" || s.err != "the source is slow" || !strings.Contains(s.log, "from stdout") || !strings.Contains(s.log, "from stderr") {
		t.Errorf("fetch: %+v", s)
	}
	if s := steps["kera"]; s.status != "failed" || s.err != "kept the old docs" || s.counts["files"] != 3 || !strings.Contains(s.log, "::kera-forged::step evil") {
		t.Errorf("kera: %+v", s)
	}
	if _, evil := steps["evil"]; evil {
		t.Error("a forged marker made a step")
	}
	if len(f.finish) != 1 || f.finish[0] != "Failed at step kera: kept the old docs." {
		t.Errorf("finish: %q", f.finish)
	}
}

func TestWrapperExitCodes(t *testing.T) {
	f := newFake(t)
	for _, c := range []struct {
		script string
		code   int
		finish string
	}{
		{`exit 0`, 0, ""},
		{`echo "${KERA_MARK}step a"; exit 3`, 3, "Failed at step a: exit code 3."},
		{`echo hi; exit 9`, 9, "Failed at step start: exit code 9."},
		{`kill -TERM $$`, 143, "Failed at step start: exit code 143."},
		// a soft failure and then a crash: the step the script died in is red too
		{`echo "${KERA_MARK}step fetch"; echo "${KERA_MARK}fail kept the old docs"; echo "${KERA_MARK}step kera"; exit 1`, 1, "Failed at step fetch: kept the old docs. Failed at step kera: exit code 1."},
		// a script that keeps its failures for the end says so: the step that did the work stays green
		{`echo "${KERA_MARK}step fetch"; echo "${KERA_MARK}fail kept the old docs"; echo "${KERA_MARK}step kera"; echo "${KERA_MARK}end"; exit 1`, 1, "Failed at step fetch: kept the old docs."},
	} {
		f.mu.Lock()
		f.reports, f.finish = nil, nil
		f.mu.Unlock()
		_, _, err := wrap(t, f.env(), fast, c.script)
		if exitOf(err) != c.code {
			t.Errorf("%q: exit %d, want %d", c.script, exitOf(err), c.code)
		}
		if len(f.finish) != 1 || f.finish[0] != c.finish {
			t.Errorf("%q: finish %q, want %q", c.script, f.finish, c.finish)
		}
	}
	steps, _ := f.steps()
	if steps["kera"].status != "ok" || steps["fetch"].status != "failed" {
		t.Errorf("the failing-at-the-end script: kera %+v fetch %+v", steps["kera"], steps["fetch"])
	}

	_, stderr, err := command2(t, f.env(), []string{"run", "--", "/no/such/command"})
	if exitOf(err) != 127 || !strings.Contains(stderr, "could not start") {
		t.Errorf("missing command: %v %q", err, stderr)
	}
	// the run error names a step "start": it must exist and be red
	steps, _ = f.steps()
	if st := steps["start"]; st == nil || st.status != "failed" || !strings.Contains(st.err, "could not start") {
		t.Errorf("no red start node for a command that did not start: %+v", st)
	}
}

func command2(t *testing.T, e cliEnv, args []string) (string, string, error) {
	t.Helper()
	var out, errb syncBuf
	err := command(context.Background(), args, e, fast, &out, &errb)
	return out.String(), errb.String(), err
}

func TestWrapperUnreachableRunsTheCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	e := cliEnv{URL: "http://127.0.0.1:1", Pipeline: "docs-sync", Token: "kp-test", Environ: os.Environ()}
	stdout, stderr, err := wrap(t, e, fast, `echo working; touch `+marker+`; echo "mark is ${KERA_MARK:+set}"; echo "run is ${KERA_RUN:-none}"; echo "${KERA_MARK}step a"; exit 3`)
	if exitOf(err) != 3 {
		t.Errorf("exit %v, want the command's 3", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("the command did not run")
	}
	for _, want := range []string{"working", "mark is set", "run is none", "[step] a"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "not reporting this run") {
		t.Errorf("no warning: %q", stderr)
	}
	// missing configuration is the same: warn, run
	_, stderr, err = wrap(t, cliEnv{Environ: os.Environ()}, fast, `exit 0`)
	if err != nil || !strings.Contains(stderr, "KERA_URL") {
		t.Errorf("no configuration: %v %q", err, stderr)
	}
	// a wrong token is not a reason to skip the work either
	e = cliEnv{URL: newFake(t).srv.URL, Pipeline: "docs-sync", Token: "kp-wrong", Environ: os.Environ()}
	if _, stderr, err = wrap(t, e, fast, `exit 2`); exitOf(err) != 2 || !strings.Contains(stderr, "not reporting") {
		t.Errorf("wrong token: %v %q", err, stderr)
	}
}

// A Kera that accepts connections and never answers (a proxy that swallows
// packets) must not hold the script back for minutes, at the start or at the end.
func TestWrapperBlackHoledKeraDoesNotStallTheScript(t *testing.T) {
	for name, startOK := range map[string]bool{"at the start": false, "after the start": true} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body) // the server notices a client that left only once the body is read
				if startOK && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/runs") {
					w.WriteHeader(http.StatusCreated)
					fmt.Fprint(w, `{"run":7,"url":"http://kera.example.com/admin/pipelines/docs-sync/7","version":"dev"}`)
					return
				}
				<-r.Context().Done() // never answers; ends when the client gives up
			}))
			defer srv.Close()
			e := cliEnv{URL: srv.URL, Pipeline: "docs-sync", Token: "kp-test", Environ: os.Environ()}
			tm := timing{flush: 20 * time.Millisecond, beat: time.Hour, poll: 10 * time.Millisecond, hang: 150 * time.Millisecond}
			done := make(chan error, 1)
			go func() { _, _, err := wrap(t, e, tm, `echo "${KERA_MARK}step a"; echo hello`); done <- err }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("the script's own exit code must come through: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the script was held back by a Kera that never answers")
			}
		})
	}
}

func TestWrapperBusyExits75(t *testing.T) {
	f := newFake(t)
	f.status = http.StatusConflict
	marker := filepath.Join(t.TempDir(), "ran")
	stdout, stderr, err := wrap(t, f.env(), fast, `touch `+marker)
	if exitOf(err) != 75 {
		t.Errorf("exit %v, want 75", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran although a run is open")
	}
	if !strings.Contains(stderr, "run 5") || stdout != "" {
		t.Errorf("who holds it: %q (stdout %q)", stderr, stdout)
	}
}

func TestWrapperBatching(t *testing.T) {
	t.Run("a long run sends several reports and heartbeats", func(t *testing.T) {
		f := newFake(t)
		_, _, err := wrap(t, f.env(), fast, `echo "${KERA_MARK}step a"; echo one; sleep 0.4; echo two`)
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		empty := 0
		for _, r := range f.reports {
			if len(r) == 0 {
				empty++
			}
		}
		if len(f.reports) < 3 || empty == 0 {
			t.Errorf("%d reports, %d heartbeats", len(f.reports), empty)
		}
	})
	t.Run("16 KiB are sent without waiting for the timer", func(t *testing.T) {
		f := newFake(t)
		slow := timing{flush: time.Hour, beat: time.Hour, poll: time.Millisecond}
		_, _, err := wrap(t, f.env(), slow, `echo "${KERA_MARK}step a"; i=0; while [ $i -lt 300 ]; do echo "line $i xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"; i=$((i+1)); done; sleep 0.3`)
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		n := len(f.reports)
		f.mu.Unlock()
		steps, _ := f.steps()
		if n < 2 || strings.Count(steps["a"].log, "\n") != 300 {
			t.Errorf("%d reports, %d lines", n, strings.Count(steps["a"].log, "\n"))
		}
	})
	t.Run("a report that cannot be delivered is kept and retried", func(t *testing.T) {
		f := newFake(t)
		f.failN = 5 // more than the three tries of one flush
		_, stderr, err := wrap(t, f.env(), fast, `echo "${KERA_MARK}step a"; echo kept-line; sleep 0.3`)
		if err != nil {
			t.Fatal(err)
		}
		steps, _ := f.steps()
		if !strings.Contains(steps["a"].log, "kept-line") || !strings.Contains(stderr, "unreachable") {
			t.Errorf("log %q stderr %q", steps["a"].log, stderr)
		}
	})
}

// ---- reading a folder ---------------------------------------------------------------

func TestScanDir(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "# A")
	write("sub/b.md", "# B")
	write("sub/deeper/c.txt", "see")
	write(".hidden.md", "# secret")
	write(".git/config.md", "# not me")
	write("pic.png", "\x89PNG")
	write("tool.exe", "MZ")
	write(strings.Repeat("n", 250)+".md", "# long")
	if err := os.Symlink(filepath.Join(dir, "a.md"), filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	list, skipped, err := scanDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range list {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"a.md", "sub--b.md", "sub--deeper--c.txt"}) {
		t.Errorf("files: %v", names)
	}
	if list[0].SHA != sum("# A") || list[0].Size != 3 {
		t.Errorf("hash %+v", list[0])
	}
	joined := strings.Join(skipped, "|")
	for _, want := range []string{".hidden.md (hidden)", ".git (hidden)", "link.md (symbolic link)", "pic.png (type not supported)", "tool.exe (type not supported)", "name not allowed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("skipped lacks %q: %v", want, skipped)
		}
	}
	if strings.Contains(joined, ".git/config") {
		t.Errorf("the contents of a skipped folder are listed: %v", skipped)
	}

	write("x/y.md", "# one")
	write("x--y.md", "# two")
	if _, _, err := scanDir(dir); err == nil || !strings.Contains(err.Error(), `"x--y.md"`) {
		t.Errorf("two files with one flat name: %v", err)
	}
	if _, _, err := scanDir(filepath.Join(dir, "a.md")); err == nil {
		t.Error("a file as --dir")
	}
	if _, _, err := scanDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing --dir")
	}
}

// --dir may be a symbolic link to the folder (a mounted volume often is).
func TestScanDirFollowsALinkedRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# A"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "docs")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	list, _, err := scanDir(link)
	if err != nil || len(list) != 1 || list[0].Name != "a.md" {
		t.Errorf("through a link: %+v %v", list, err)
	}
}

// ---- against the real server ---------------------------------------------------------------

func (e *tenv) cli(p pipe, extra ...string) cliEnv {
	return cliEnv{URL: e.url, Pipeline: p.name, Token: p.tok, Environ: append(os.Environ(), extra...)}
}

func writeFiles(t *testing.T, dir string, content map[string]string) {
	t.Helper()
	for n, c := range content {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func push(t *testing.T, e cliEnv, args ...string) (string, string, error) {
	t.Helper()
	var out, errb syncBuf
	err := command(context.Background(), append([]string{"push"}, args...), e, fast, &out, &errb)
	return out.String(), errb.String(), err
}

func TestPushOutsideARun(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs")
	dir := t.TempDir()
	writeFiles(t, dir, docs)
	ce := e.cli(p)

	// a dry run shows the plan and changes nothing
	stdout, _, err := push(t, ce, "--collection", "Product docs", "--dir", dir, "--dry-run")
	if err != nil || !strings.Contains(stdout, "3 files, 3 to upload") || !strings.Contains(stdout, "would upload: fox.md, jugs.md, tea.md") {
		t.Errorf("dry run: %v\n%s", err, stdout)
	}
	if e.count(`SELECT count(*) FROM knowledge_file`) != 0 {
		t.Error("a dry run wrote rows")
	}

	stdout, _, err = push(t, ce, "--collection", "Product docs", "--dir", dir)
	if err != nil || !strings.Contains(stdout, "3 uploaded, 3 indexed, 0 failed, 0 removed") || !strings.Contains(stdout, " ok") {
		t.Fatalf("push: %v\n%s", err, stdout)
	}
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND status = 'completed'`, kids[0]) != 3 {
		t.Error("the files are not live")
	}
	if e.count(`SELECT count(*) FROM pipeline_run WHERE trigger = 'push' AND status = 'ok'`) != 2 {
		t.Error("the push runs are not recorded")
	}

	// the same folder: nothing to do. A file removed: pruned. A file edited: replaced.
	stdout, _, err = push(t, ce, "--collection", "Product docs", "--dir", dir)
	if err != nil || !strings.Contains(stdout, "0 to upload, 3 unchanged") {
		t.Errorf("rerun: %v\n%s", err, stdout)
	}
	if err := os.Remove(filepath.Join(dir, "tea.md")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{"fox.md": "# Fox\nA different fox sleeps in the sun."})
	stdout, _, err = push(t, ce, "--collection", kids[0], "--dir", dir) // by id
	if err != nil || !strings.Contains(stdout, "1 to upload, 1 unchanged, adopt 0, remove 1") || !strings.Contains(stdout, "1 uploaded, 2 indexed, 0 failed, 1 removed") {
		t.Errorf("edit and remove: %v\n%s", err, stdout)
	}
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0]) != 2 {
		t.Error("the removed file is still in the collection")
	}

	// a broken file: the run is red and the command exits 1; the good file is live
	writeFiles(t, dir, map[string]string{"broken.pdf": "not a pdf"})
	stdout, _, err = push(t, ce, "--collection", "Product docs", "--dir", dir)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 1 || !strings.Contains(ee.Msg, "failed") || !strings.Contains(ee.Msg, "broken.pdf") || !strings.Contains(stdout, "1 failed") {
		t.Errorf("broken file: %v\n%s", err, stdout)
	}

	// a refused prune: the run is a warning, exit 1, nothing removed
	for _, n := range []string{"fox.md", "jugs.md", "broken.pdf"} {
		_ = os.Remove(filepath.Join(dir, n))
	}
	stdout, _, err = push(t, ce, "--collection", "Product docs", "--dir", dir)
	if exitOf(err) != 1 || !strings.Contains(stdout, "would be refused") || e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[0]) != 3 {
		t.Errorf("refused: %v\n%s", err, stdout)
	}

	// a collection the pipeline does not write to, and a wrong token
	if _, _, err = push(t, ce, "--collection", "Elsewhere", "--dir", dir); err == nil || !strings.Contains(err.Error(), "refused the listing") {
		t.Errorf("not a target: %v", err)
	}
	bad := ce
	bad.Token = "kp-" + strings.Repeat("0", 40)
	if _, _, err = push(t, bad, "--collection", "Product docs", "--dir", dir); err == nil {
		t.Error("a wrong token")
	}
}

// A file the server refuses on upload (here: over files.max_mb) is no silent
// skip: the run is red and the command says so.
func TestPushReportsARefusedUpload(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	dir := t.TempDir()
	writeFiles(t, dir, docs)
	ce := e.cli(p)
	if _, _, err := push(t, ce, "--collection", "Product docs", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	if err := e.s.a.Settings.Set(context.Background(), "files.max_mb", 1, false); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{"fox.md": "# Fox\n" + strings.Repeat("x", 1<<20)})
	stdout, _, err := push(t, ce, "--collection", "Product docs", "--dir", dir)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 1 || !strings.Contains(ee.Msg, "refused on upload") || !strings.Contains(stdout, "fox.md was not uploaded") || !strings.Contains(stdout, "0 uploaded") {
		t.Errorf("push: %v\n%s", err, stdout)
	}
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id IN (SELECT unnest(targets) FROM pipeline) AND status = 'completed'`) != 3 {
		t.Error("the old versions are not all live")
	}
}

func TestPushWhileARunIsOpenExits75(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	p.start(startIn{})
	dir := t.TempDir()
	writeFiles(t, dir, docs)
	_, stderr, err := push(t, e.cli(p), "--collection", "Product docs", "--dir", dir)
	if exitOf(err) != 75 || !strings.Contains(stderr, "already has a run in progress") {
		t.Errorf("push: %v %q", err, stderr)
	}
}

func TestPushUsageAndConfiguration(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	dir := t.TempDir()
	for name, args := range map[string][]string{"no collection": {"--dir", dir}, "no dir": {"--collection", "x"}, "stray argument": {"--collection", "x", "--dir", dir, "extra"}} {
		if _, _, err := push(t, e.cli(p), args...); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := push(t, cliEnv{}, "--collection", "x", "--dir", dir); err == nil || !strings.Contains(err.Error(), "KERA_URL") {
		t.Errorf("no configuration: %v", err)
	}
	if _, _, err := push(t, e.cli(p, "x=y"), "--collection", "x", "--dir", "/no/such/dir"); err == nil {
		t.Error("missing folder")
	}
	if err := command(context.Background(), nil, cliEnv{}, fast, &syncBuf{}, &syncBuf{}); err == nil {
		t.Error("no subcommand")
	}
	if err := command(context.Background(), []string{"jump"}, cliEnv{}, fast, &syncBuf{}, &syncBuf{}); err == nil {
		t.Error("unknown subcommand")
	}
}

func TestWrappedPush(t *testing.T) {
	e := newEnv(t, true)
	p, kids := e.mkPipe("docs-sync", "Product docs", "Notes")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"fox.md": docs["fox.md"], "broken.pdf": "not a pdf"})
	notes := t.TempDir()
	writeFiles(t, notes, map[string]string{"jugs.md": docs["jugs.md"]})

	// the wrapper runs this very test binary as the command (see TestMain)
	ce := e.cli(p, "KERA_PIPELINE_TEST_CLI=1", "KERA_URL="+e.url, "KERA_PIPELINE=docs-sync", "KERA_PIPELINE_TOKEN="+p.tok, "KERA_TEST_BIN="+os.Args[0])
	script := `set -e
echo "${KERA_MARK}steps fetch kera"
echo "${KERA_MARK}step fetch"
echo "fetched"
echo "${KERA_MARK}step kera"
"$KERA_TEST_BIN" push --collection "Product docs" --dir "` + dir + `"
"$KERA_TEST_BIN" push --collection Notes --dir "` + notes + `"
echo "done"`
	stdout, _, err := wrap(t, ce, fast, script)
	if err != nil {
		t.Fatalf("a push inside a run must exit 0 on file-level failures, got %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "done") || !strings.Contains(stdout, "Product docs: 2 files") || !strings.Contains(stdout, "Notes: 1 files") {
		t.Errorf("the second push did not run:\n%s", stdout)
	}
	var runs struct{ Runs []runOut }
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync/runs", nil, http.StatusOK, &runs)
	if len(runs.Runs) != 1 {
		t.Fatalf("%d runs; push inside a wrapped run must not open its own", len(runs.Runs))
	}
	rv := p.closed(runs.Runs[0].ID)
	if rv.Status != "failed" || !strings.Contains(rv.Error, "Product docs: 1 of 2 files could not be indexed: broken.pdf") {
		t.Errorf("run: %s %q", rv.Status, rv.Error)
	}
	if stepOf(rv, false, "fetch").Status != "ok" || stepOf(rv, false, "kera").Status != "ok" {
		t.Errorf("the failed file is on the collection, not on the script's step: %+v", *rv.Steps)
	}
	if n := stepOf(rv, true, "Product docs"); n.Status != "failed" || n.Counts["indexed"] != 1 || n.Key != kids[0] {
		t.Errorf("collection step: %+v", n)
	}
	if n := stepOf(rv, true, "Notes"); n.Status != "ok" || n.Counts["indexed"] != 1 {
		t.Errorf("the other collection must go on: %+v", n)
	}
	if !strings.Contains(stdout, "http://kera.example.com/admin/pipelines/docs-sync/") {
		t.Errorf("no run link:\n%s", stdout)
	}

	// the next day: a script step failed, so nothing is pruned; the removal is kept for a good run
	_ = os.Remove(filepath.Join(notes, "jugs.md"))
	writeFiles(t, notes, map[string]string{"tea.md": docs["tea.md"]})
	script = `echo "${KERA_MARK}step fetch"; echo "${KERA_MARK}fail the source was down"; echo "${KERA_MARK}step kera"
"$KERA_TEST_BIN" push --collection Notes --dir "` + notes + `"
echo "${KERA_MARK}end"
exit 1`
	_, _, err = wrap(t, ce, fast, script)
	if exitOf(err) != 1 {
		t.Fatalf("exit %v", err)
	}
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync/runs", nil, http.StatusOK, &runs)
	rv = p.closed(runs.Runs[0].ID)
	if rv.Status != "failed" || rv.Error != "Failed at step fetch: the source was down." || stepOf(rv, false, "kera").Status != "ok" {
		t.Errorf("second run: %s %q %+v", rv.Status, rv.Error, *rv.Steps)
	}
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, kids[1]) != 2 {
		t.Error("the file was pruned although the script reported a failure")
	}
}

func TestWrappedPushRefusals(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	dir := t.TempDir()
	writeFiles(t, dir, docs)
	run := p.start(startIn{})
	ce := e.cli(p)
	ce.Run, ce.Mark = fmt.Sprint(run), testMark

	// the server refuses the listing: reported as a failed step through a marker, exit 0
	stdout, _, err := push(t, ce, "--collection", "Elsewhere", "--dir", dir)
	if err != nil || !strings.Contains(stdout, testMark+"fail Elsewhere: Kera refused the listing") {
		t.Errorf("refused listing: %v\n%s", err, stdout)
	}
	// a broken connection or token is an error
	bad := ce
	bad.URL = "http://127.0.0.1:1"
	if _, _, err = push(t, bad, "--collection", "Product docs", "--dir", dir); err == nil {
		t.Error("an unreachable server must be an error")
	}
	bad = ce
	bad.Token = "kp-" + strings.Repeat("1", 40)
	if _, _, err = push(t, bad, "--collection", "Product docs", "--dir", dir); err == nil {
		t.Error("a wrong token must be an error")
	}
	if _, _, err = push(t, ce, "--collection", "Product docs", "--dir", dir, "--dry-run"); err == nil || !strings.Contains(err.Error(), "--dry-run") {
		t.Errorf("--dry-run inside a run: %v", err)
	}
	if _, _, err = push(t, cliEnv{URL: e.url, Pipeline: "docs-sync", Token: p.tok, Run: "abc"}, "--collection", "Product docs", "--dir", dir); err == nil {
		t.Error("a bad KERA_RUN")
	}
	if code, _ := e.do(p.tok, "GET", p.path(fmt.Sprintf("/runs/%d", run)), nil); code != http.StatusOK {
		t.Fatal(code)
	}
}

func TestWrapperAgainstTheServer(t *testing.T) {
	e := newEnv(t, true)
	p, _ := e.mkPipe("docs-sync", "Product docs")
	ce := e.cli(p)
	_, _, err := wrap(t, ce, fast, `echo "${KERA_MARK}steps a b c"; echo "${KERA_MARK}step a"; echo "fine"; echo "${KERA_MARK}step b"; w=Hunter; echo "password=${w}2xYz99"; echo "${KERA_MARK}fail bad source"; echo "${KERA_MARK}step c"; echo done; echo "${KERA_MARK}end"; exit 2`)
	if exitOf(err) != 2 {
		t.Fatalf("exit %v", err)
	}
	var runs struct{ Runs []runOut }
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/docs-sync/runs", nil, http.StatusOK, &runs)
	rv := p.closed(runs.Runs[0].ID)
	if rv.Status != "failed" || rv.Error != "Failed at step b: bad source." || stepOf(rv, false, "a").Status != "ok" || stepOf(rv, false, "b").Status != "failed" || stepOf(rv, false, "c").Status != "ok" {
		t.Errorf("run: %s %q %+v", rv.Status, rv.Error, *rv.Steps)
	}
	if st := e.logOf("docs-sync", rv.ID, stepOf(rv, false, "b").Seq); strings.Contains(*st.Log, "Hunter2xYz99") || !strings.Contains(*st.Log, "[REDACTED") {
		t.Errorf("the printed password is not masked: %q", *st.Log)
	}
	if stepOf(rv, true, "Product docs").Status != "skipped" {
		t.Errorf("a collection nobody synced is skipped: %+v", *rv.Steps)
	}
}
