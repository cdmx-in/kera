package pipeline

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sessResp struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Run    int64  `json:"run"`
	Error  string `json:"error"`
	Cells  []cellOut
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// startNotebook starts a runner's interactive loop and a session on it; it returns the session once it is ready.
func (e *tenv) startNotebook(base, entry string, files map[string]string) (*runner, sessResp) {
	e.t.Helper()
	rn, _ := e.newRunnerFor(e.runner("nb"))
	ctx, cancel := contextWithCancel(e.t)
	go rn.interactive(ctx)
	e.t.Cleanup(cancel)
	waitFor(e.t, "the runner to be seen", func() bool { return e.s.runnerOnline(context.Background()) })
	var s sessResp
	e.call(e.admin, "POST", base+"/sessions", map[string]any{"files": files, "entry": entry}, http.StatusCreated, &s)
	waitFor(e.t, "the session to be ready", func() bool {
		e.call(e.admin, "GET", base+"/sessions/"+s.ID, nil, http.StatusOK, &s)
		return s.Status == "ready"
	})
	return rn, s
}

// runCell queues a cell and waits for it to end.
func (e *tenv) runCell(base string, s sessResp, code string) cellOut {
	e.t.Helper()
	var q struct{ Cell int64 }
	e.call(e.admin, "POST", base+"/sessions/"+s.ID+"/cells", map[string]any{"code": code}, http.StatusCreated, &q)
	var out cellOut
	waitFor(e.t, "the cell to end", func() bool {
		var cur sessResp
		e.call(e.admin, "GET", base+"/sessions/"+s.ID, nil, http.StatusOK, &cur)
		for _, c := range cur.Cells {
			if c.ID == q.Cell && c.Status != "queued" && c.Status != "running" {
				out = c
				return true
			}
		}
		return false
	})
	return out
}

func TestNotebookBashSessionKeepsStateAndIsADryRun(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "API_TOKEN", "value": "supersecret-value-123"}, http.StatusOK, nil)
	_, s := e.startNotebook(base, "run.sh", map[string]string{"run.sh": "echo hi\n", "lib.sh": "greet() { echo \"hello $1\"; }\n"})

	c1 := e.runCell(base, s, "X=hello\ncd /tmp\necho first")
	if c1.Status != "ok" || !strings.Contains(c1.Output, "first") {
		t.Fatalf("cell 1: %+v", c1)
	}
	c2 := e.runCell(base, s, "echo \"$X $PWD $KERA_TEST\"\n. \"$KERA_SCRIPT_DIR/lib.sh\"\ngreet world\necho \"token=$API_TOKEN\"\necho \"${KERA_MARK}step build\"\nprintf 'no newline'")
	if c2.Status != "ok" || !strings.Contains(c2.Output, "hello /tmp 1") || !strings.Contains(c2.Output, "hello world") {
		t.Fatalf("state must persist between cells: %+v", c2)
	}
	if strings.Contains(c2.Output, "supersecret-value-123") || !strings.Contains(c2.Output, "token=***") {
		t.Fatalf("secret in output: %q", c2.Output)
	}
	if !strings.Contains(c2.Output, "▸ step: build") || !strings.HasSuffix(strings.TrimRight(c2.Output, "\n"), "no newline") {
		t.Fatalf("marker or partial line: %q", c2.Output)
	}
	if c3 := e.runCell(base, s, "echo nope >&2; false"); c3.Status != "failed" || c3.ExitCode == nil || *c3.ExitCode != 1 || !strings.Contains(c3.Output, "nope") {
		t.Fatalf("failing cell: %+v", c3)
	}

	// behind the session: a dry run with the test trigger, which is not the pipeline's "last run" and does not block a real one
	var rv runOut
	e.call(e.admin, "GET", base+"/runs/"+itoa(s.Run), nil, http.StatusOK, &rv)
	if !rv.DryRun || rv.Trigger != "test" || rv.Status != "running" {
		t.Fatalf("session run: %+v", rv)
	}
	var list struct{ Pipelines []pipelineOut }
	code, raw := e.do(e.admin, "GET", "/api/v1/admin/pipelines", nil)
	_ = list
	if code != http.StatusOK || strings.Contains(string(raw), `"trigger":"test"`) {
		t.Fatalf("a notebook run must not be the pipeline's last run: %d", code)
	}
	e.call(e.admin, "POST", base+"/run", nil, http.StatusOK, nil) // a real run can be queued while the session is open
	c4 := e.runCell(base, s, `echo "run=$KERA_RUN pipeline=$KERA_PIPELINE"`)
	if !strings.Contains(c4.Output, "run="+itoa(s.Run)+" pipeline=docs") {
		t.Fatalf("env of the session: %q", c4.Output)
	}

	// closing: the process ends, the dry run is finished, the scratch folder is gone
	e.call(e.admin, "DELETE", base+"/sessions/"+s.ID, nil, http.StatusOK, nil)
	waitFor(t, "the session to close", func() bool {
		e.call(e.admin, "GET", base+"/sessions/"+s.ID, nil, http.StatusOK, &s)
		return s.Status == "closed"
	})
	e.call(e.admin, "POST", base+"/sessions/"+s.ID+"/cells", map[string]any{"code": "echo x"}, http.StatusConflict, nil)
	if n := e.count(`SELECT count(*) FROM pipeline_run WHERE id = $1 AND client_done`, s.Run); n != 1 {
		t.Fatalf("the dry run behind the session is not finished")
	}
}

func TestNotebookPythonSessionStateTracebackAndStop(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	_, s := e.startNotebook(base, "job.py", map[string]string{"job.py": "print(1)\n", "helper.py": "def twice(x):\n    return 2 * x\n"})
	if c := e.runCell(base, s, "a = 41\nprint('set')"); c.Status != "ok" {
		t.Fatalf("cell 1: %+v", c)
	}
	if c := e.runCell(base, s, "from helper import twice\nprint(twice(a + 1))"); c.Status != "ok" || !strings.Contains(c.Output, "84") {
		t.Fatalf("state and imports from the script folder: %+v", c)
	}
	bad := e.runCell(base, s, "print('before')\n1 / 0")
	if bad.Status != "failed" || !strings.Contains(bad.Output, "ZeroDivisionError") || !strings.Contains(bad.Output, "before") {
		t.Fatalf("traceback: %+v", bad)
	}
	if c := e.runCell(base, s, "print(a)"); !strings.Contains(c.Output, "41") {
		t.Fatalf("state must survive a failed cell: %+v", c)
	}
	// stop: the cell is interrupted, the state stays
	var q struct{ Cell int64 }
	e.call(e.admin, "POST", base+"/sessions/"+s.ID+"/cells", map[string]any{"code": "import time\nprint('sleeping', flush=True)\ntime.sleep(60)"}, http.StatusCreated, &q)
	waitFor(t, "the cell to print", func() bool {
		var cur sessResp
		e.call(e.admin, "GET", base+"/sessions/"+s.ID, nil, http.StatusOK, &cur)
		for _, c := range cur.Cells {
			if c.ID == q.Cell && strings.Contains(c.Output, "sleeping") {
				return true
			}
		}
		return false
	})
	e.call(e.admin, "POST", base+"/sessions/"+s.ID+"/cells/"+itoa(q.Cell)+"/stop", nil, http.StatusOK, nil)
	waitFor(t, "the cell to stop", func() bool {
		var cur sessResp
		e.call(e.admin, "GET", base+"/sessions/"+s.ID, nil, http.StatusOK, &cur)
		for _, c := range cur.Cells {
			if c.ID == q.Cell {
				return c.Status == "stopped"
			}
		}
		return false
	})
	if c := e.runCell(base, s, "print(a)"); !strings.Contains(c.Output, "41") {
		t.Fatalf("a stopped Python cell must keep the session state: %+v", c)
	}
}

func TestNotebookStopRestartsAShellAndIdleSessionsClose(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	_, s := e.startNotebook(base, "run.sh", map[string]string{"run.sh": "echo hi\n"})
	e.runCell(base, s, "KEEP=1")
	var q struct{ Cell int64 }
	e.call(e.admin, "POST", base+"/sessions/"+s.ID+"/cells", map[string]any{"code": "echo started\nsleep 60"}, http.StatusCreated, &q)
	waitFor(t, "the cell to run", func() bool {
		var cur sessResp
		e.call(e.admin, "GET", base+"/sessions/"+s.ID, nil, http.StatusOK, &cur)
		return len(cur.Cells) > 0 && strings.Contains(cur.Cells[len(cur.Cells)-1].Output, "started")
	})
	e.call(e.admin, "POST", base+"/sessions/"+s.ID+"/cells/"+itoa(q.Cell)+"/stop", nil, http.StatusOK, nil)
	waitFor(t, "the cell to stop", func() bool {
		var cur sessResp
		e.call(e.admin, "GET", base+"/sessions/"+s.ID, nil, http.StatusOK, &cur)
		return cur.Cells[len(cur.Cells)-1].Status == "stopped"
	})
	if c := e.runCell(base, s, `echo "keep=[${KEEP:-}]"`); c.Status != "ok" || !strings.Contains(c.Output, "keep=[]") {
		t.Fatalf("a stopped shell restarts without its variables: %+v", c)
	}
	e.clk.add(sessionIdle + time.Minute)
	e.s.sessionTick(t.Context(), e.clk.now())
	waitFor(t, "the idle session to close", func() bool {
		e.call(e.admin, "GET", base+"/sessions/"+s.ID, nil, http.StatusOK, &s)
		return s.Status == "closed"
	})
}

func TestChecksFindSyntaxProblemsAndNeedARunner(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	body := map[string]any{"files": map[string]string{"run.sh": "echo ok\nif then\nfi\n", "tool.py": "x = (1,\nprint(x)\n", "fine.sh": "echo fine\n"}, "entry": "run.sh"}
	e.call(e.admin, "POST", base+"/check", body, http.StatusConflict, nil) // no runner has connected yet
	rn, _ := e.newRunnerFor(e.runner("nb"))
	ctx, cancel := contextWithCancel(t)
	go rn.interactive(ctx)
	t.Cleanup(cancel)
	waitFor(t, "the runner to be seen", func() bool { return e.s.runnerOnline(t.Context()) })
	var res checkResult
	e.call(e.admin, "POST", base+"/check", body, http.StatusOK, &res)
	var bash, py int
	for _, p := range res.Problems {
		switch {
		case p.File == "run.sh" && p.Severity == "error" && p.Line >= 2:
			bash++
		case p.File == "tool.py" && p.Severity == "error" && p.Line >= 1:
			py++
		case p.File == "fine.sh" && p.Severity == "error":
			t.Fatalf("a clean file has an error: %+v", p)
		}
	}
	if bash == 0 || py == 0 || len(res.Tools) == 0 {
		t.Fatalf("problems not found: %+v", res)
	}
	e.call(e.admin, "POST", base+"/check", map[string]any{"files": map[string]string{"a.sh": "echo ok\n"}, "entry": "a.sh"}, http.StatusOK, &res)
	for _, p := range res.Problems {
		if p.Severity == "error" {
			t.Fatalf("a clean script reported an error: %+v", p)
		}
	}
	e.call(e.user, "POST", base+"/check", body, http.StatusForbidden, nil)
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
