package pipeline

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/auth"
)

// builtinRow is the hash of the runner "builtin", and how many runners there are in all.
func (e *tenv) builtinRow() (hash string, runners int) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(), `SELECT coalesce(max(token_hash) FILTER (WHERE name = 'builtin'), ''), count(*) FROM pipeline_runner`).Scan(&hash, &runners); err != nil {
		e.t.Fatal(err)
	}
	return hash, runners
}

func TestBuiltinRunnerTokenFile(t *testing.T) {
	e := newEnv(t, false)
	ctx := t.Context()
	file := filepath.Join(t.TempDir(), "run", "kera-runner", "token") // the folder does not exist yet

	// first start: the folder, the file and the row are made, and the token works
	if err := e.s.ensureBuiltinRunner(ctx, file); err != nil {
		t.Fatal(err)
	}
	tok := readRunnerToken(file)
	if tok == "" {
		t.Fatal("no valid token in the file")
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("file mode: %v %v", st, err)
	}
	if h, n := e.builtinRow(); h != auth.HashAPIKey(tok) || n != 1 {
		t.Fatalf("row: hash ok %v, %d runners", h == auth.HashAPIKey(tok), n)
	}
	e.call(tok, "POST", "/api/v1/runner/claim", nil, http.StatusNoContent, nil)
	var list struct{ Runners []runnerOut }
	e.call(e.admin, "GET", "/api/v1/admin/pipeline-runners", nil, http.StatusOK, &list)
	if len(list.Runners) != 1 || list.Runners[0].Name != "builtin" || !list.Runners[0].Online {
		t.Fatalf("runners: %+v", list.Runners)
	}

	// second start: the same token, the same row
	var id string
	_ = e.pool.QueryRow(ctx, `SELECT id FROM pipeline_runner WHERE name = 'builtin'`).Scan(&id)
	if err := e.s.ensureBuiltinRunner(ctx, file); err != nil {
		t.Fatal(err)
	}
	var id2 string
	_ = e.pool.QueryRow(ctx, `SELECT id FROM pipeline_runner WHERE name = 'builtin'`).Scan(&id2)
	if readRunnerToken(file) != tok || id2 != id {
		t.Fatal("a second start changed the token or the row")
	}

	// a file that holds another valid token (restored from a backup, written by hand) rotates the row to it
	other, _, _, _ := newRunnerToken()
	if err := os.WriteFile(file, []byte(other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ensureBuiltinRunner(ctx, file); err != nil {
		t.Fatal(err)
	}
	if h, n := e.builtinRow(); h != auth.HashAPIKey(other) || n != 1 {
		t.Fatalf("after a changed file: %d runners", n)
	}
	e.call(tok, "POST", "/api/v1/runner/claim", nil, http.StatusUnauthorized, nil)
	e.call(other, "POST", "/api/v1/runner/claim", nil, http.StatusNoContent, nil)

	// anything else in the file is replaced by a fresh token
	for _, junk := range []string{"", "not a token", "kr-short", other[:20]} {
		if err := os.WriteFile(file, []byte(junk), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := e.s.ensureBuiltinRunner(ctx, file); err != nil {
			t.Fatal(err)
		}
		fresh := readRunnerToken(file)
		if h, n := e.builtinRow(); fresh == "" || fresh == other || h != auth.HashAPIKey(fresh) || n != 1 {
			t.Fatalf("junk %q: not replaced (%d runners)", junk, n)
		}
		other = fresh
	}

	// a deleted row comes back, with the token the file already has
	if _, err := e.pool.Exec(ctx, `DELETE FROM pipeline_runner`); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ensureBuiltinRunner(ctx, file); err != nil {
		t.Fatal(err)
	}
	if h, n := e.builtinRow(); h != auth.HashAPIKey(other) || n != 1 {
		t.Fatalf("after delete: %d runners", n)
	}

	// an unwritable place is an error, not a crash
	if err := e.s.ensureBuiltinRunner(ctx, filepath.Join(file, "x", "token")); err == nil {
		t.Fatal("no error for a file inside a file")
	}
}

func TestBuiltinRunnerSeveralStartsAtOnce(t *testing.T) {
	e := newEnv(t, false)
	file := filepath.Join(t.TempDir(), "token")
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- e.s.ensureBuiltinRunner(context.Background(), file) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	tok := readRunnerToken(file)
	if h, n := e.builtinRow(); tok == "" || h != auth.HashAPIKey(tok) || n != 1 {
		t.Fatalf("file and row disagree after concurrent starts (%d runners)", n)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(file), ".token-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

// Rotating the built-in runner from the page must reach the file the runner container reads.
func TestRotatingTheBuiltinRunnerUpdatesTheFile(t *testing.T) {
	e := newEnv(t, false)
	file := filepath.Join(t.TempDir(), "token")
	e.s.a.Env.RunnerTokenFile = file
	e.s.startBuiltinRunner(t.Context())
	old := readRunnerToken(file)
	var list struct{ Runners []runnerOut }
	e.call(e.admin, "GET", "/api/v1/admin/pipeline-runners", nil, http.StatusOK, &list)
	if old == "" || len(list.Runners) != 1 {
		t.Fatalf("not started: %q %+v", old, list.Runners)
	}
	var out struct{ Token string }
	e.call(e.admin, "POST", "/api/v1/admin/pipeline-runners/"+list.Runners[0].ID+"/token", nil, http.StatusOK, &out)
	if got := readRunnerToken(file); got != out.Token || got == old {
		t.Fatal("the file does not hold the rotated token")
	}
	e.call(out.Token, "POST", "/api/v1/runner/claim", nil, http.StatusNoContent, nil)

	// another runner's rotation leaves the file alone
	other := e.runner("laptop")
	_ = other
	e.call(e.admin, "GET", "/api/v1/admin/pipeline-runners", nil, http.StatusOK, &list)
	for _, r := range list.Runners {
		if r.Name == "laptop" {
			e.call(e.admin, "POST", "/api/v1/admin/pipeline-runners/"+r.ID+"/token", nil, http.StatusOK, nil)
		}
	}
	if readRunnerToken(file) != out.Token {
		t.Fatal("rotating another runner changed the file")
	}
}

func TestRunnerWaitsForTheTokenFile(t *testing.T) {
	var errw bytes.Buffer
	file := filepath.Join(t.TempDir(), "token")
	rn := &runner{tokenFile: file, errw: &errw}

	// missing file: it waits, says so, and stops when asked to
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if rn.waitForToken(ctx, 10*time.Millisecond) {
		t.Fatal("got a token from a file that does not exist")
	}
	if !strings.Contains(errw.String(), "waiting for the token in "+file) {
		t.Fatalf("no message: %q", errw.String())
	}

	// the file appears (and an empty one is not a token): it carries on with what is in it
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(50 * time.Millisecond); _ = os.WriteFile(file, []byte(" kr-abc \n"), 0o644) }()
	if !rn.waitForToken(context.Background(), 10*time.Millisecond) || rn.bearer() != "kr-abc" {
		t.Fatalf("token %q", rn.bearer())
	}
}

func TestRunnerReadsAgainAfterTheTokenIsRefused(t *testing.T) {
	e := newEnv(t, false)
	file := filepath.Join(t.TempDir(), "token")
	e.s.a.Env.RunnerTokenFile = file
	e.s.startBuiltinRunner(t.Context())
	rn, _ := e.newRunnerFor("")
	rn.tokenFile = file
	if !rn.waitForToken(t.Context(), 10*time.Millisecond) {
		t.Fatal("no token")
	}
	if j, err := rn.claim(t.Context()); err != nil || j != nil {
		t.Fatalf("first claim: %v %v", j, err)
	}

	// Kera rotates it (file and row): the runner's next claim is refused once, then it uses the new token
	var list struct{ Runners []runnerOut }
	e.call(e.admin, "GET", "/api/v1/admin/pipeline-runners", nil, http.StatusOK, &list)
	var out struct{ Token string }
	e.call(e.admin, "POST", "/api/v1/admin/pipeline-runners/"+list.Runners[0].ID+"/token", nil, http.StatusOK, &out)
	if _, err := rn.claim(t.Context()); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a rotated token was not refused: %v", err)
	}
	if rn.bearer() != out.Token {
		t.Fatal("the runner did not read the new token")
	}
	if j, err := rn.claim(t.Context()); err != nil || j != nil {
		t.Fatalf("claim with the new token: %v %v", j, err)
	}

	// a runner given its token in the environment has no file to go back to
	rn2, _ := e.newRunnerFor("kr-0000000000000000000000000000000000000000")
	if _, err := rn2.claim(t.Context()); err == nil || rn2.bearer() != "kr-0000000000000000000000000000000000000000" {
		t.Fatalf("env token: %v", err)
	}
}

func TestRunnerCommandTokenSettings(t *testing.T) {
	t.Setenv("KERA_URL", "http://127.0.0.1:1")
	t.Setenv("KERA_RUNNER_TOKEN", "")
	t.Setenv("KERA_RUNNER_TOKEN_FILE", "")
	var out bytes.Buffer
	if err := RunnerCommand(context.Background(), &out, &out); err == nil || !strings.Contains(err.Error(), "KERA_RUNNER_TOKEN_FILE") {
		t.Fatalf("no token and no file: %v", err)
	}
	// a file that never appears: the command waits and ends cleanly when it is stopped
	t.Setenv("KERA_RUNNER_TOKEN_FILE", filepath.Join(t.TempDir(), "token"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := RunnerCommand(ctx, &out, &out); err != nil {
		t.Fatalf("waiting for the file: %v", err)
	}
	if !strings.Contains(out.String(), "waiting for the token in") {
		t.Fatalf("no message: %q", out.String())
	}
}
