package pipeline

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

// newRunnerFor makes a runner that talks to the test server and keeps its workspace in a temp dir.
func (e *tenv) newRunnerFor(token string) (*runner, *bytes.Buffer) {
	var out bytes.Buffer
	rn := &runner{url: e.url, token: token, work: e.t.TempDir(), host: "testbox", tm: timing{flush: 50 * time.Millisecond, beat: time.Second, poll: 20 * time.Millisecond, hang: 5 * time.Second},
		wait: time.Second, cancelEvery: 100 * time.Millisecond, out: &out, errw: &out, hc: &http.Client{Timeout: 10 * time.Second}}
	return rn, &out
}

func (e *tenv) saveScript(base, entry string, files map[string]string, args string) {
	e.t.Helper()
	e.call(e.admin, "PUT", base+"/script", map[string]any{"files": files, "entry": entry, "args": args}, http.StatusCreated, nil)
}

func (e *tenv) runNow(base string) runOut {
	e.t.Helper()
	var rv runOut
	e.call(e.admin, "POST", base+"/run", nil, http.StatusOK, &rv)
	return rv
}

func (e *tenv) adminRunOut(base string, id int64) runOut {
	e.t.Helper()
	var rv runOut
	e.call(e.admin, "GET", base+"/runs/"+itoa(id), nil, http.StatusOK, &rv)
	return rv
}

func stepByName(r runOut, name string) *stepOut {
	if r.Steps != nil {
		for i := range *r.Steps {
			if (*r.Steps)[i].Name == name {
				return &(*r.Steps)[i]
			}
		}
	}
	return nil
}

func TestRunnerRunsAScriptWithStepsCountsAndMaskedSecrets(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "MY_TOKEN", "value": "supersecret-value-123"}, http.StatusOK, nil)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "GREETING", "value": "hello-there", "secret": false}, http.StatusOK, nil)
	e.saveScript(base, "run.sh", map[string]string{"run.sh": `#!/bin/bash
echo "${KERA_MARK}steps fetch build"
echo "${KERA_MARK}step fetch"
echo "greeting=$GREETING arg=$1 pipeline=$KERA_PIPELINE"
echo "my token is $MY_TOKEN"
echo "runner token leak? [${KERA_RUNNER_TOKEN:-none}] cwd=$(basename "$PWD")"
echo "${KERA_MARK}count files=7"
echo "${KERA_MARK}step build"
cat "$KERA_SCRIPT_DIR/lib/helper.txt"
`, "lib/helper.txt": "from-a-second-file\n"}, "alpha")
	rv := e.runNow(base)
	rn, out := e.newRunnerFor(e.runner("r1"))
	t.Setenv("KERA_RUNNER_TOKEN", "must-not-reach-the-script")
	if ran, err := rn.once(t.Context()); err != nil || !ran {
		t.Fatalf("once: ran=%v err=%v\n%s", ran, err, out)
	}
	got := e.adminRunOut(base, rv.ID)
	if got.Status != "ok" || got.Queued || got.Source["host"] != "testbox" {
		t.Fatalf("run: %+v\n%s", got, out)
	}
	fetch, build := stepByName(got, "fetch"), stepByName(got, "build")
	if fetch == nil || build == nil || fetch.Status != "ok" || build.Status != "ok" || fetch.Counts["files"] != 7 {
		t.Fatalf("steps: %+v %+v\n%s", fetch, build, out)
	}
	var log struct{ Log string }
	e.call(e.admin, "GET", base+"/runs/"+itoa(rv.ID)+"/steps/"+itoa(int64(fetch.Seq)), nil, http.StatusOK, &log)
	if !strings.Contains(log.Log, "greeting=hello-there arg=alpha pipeline=docs") || !strings.Contains(log.Log, "cwd=docs") {
		t.Fatalf("log: %q", log.Log)
	}
	if strings.Contains(log.Log, "supersecret-value-123") || !strings.Contains(log.Log, "my token is ***") {
		t.Fatalf("the secret reached the log: %q", log.Log)
	}
	if strings.Contains(log.Log, "must-not-reach") || !strings.Contains(log.Log, "[none]") {
		t.Fatalf("the runner's own environment reached the script: %q", log.Log)
	}
	var blog struct{ Log string }
	e.call(e.admin, "GET", base+"/runs/"+itoa(rv.ID)+"/steps/"+itoa(int64(build.Seq)), nil, http.StatusOK, &blog)
	if !strings.Contains(blog.Log, "from-a-second-file") {
		t.Fatalf("second file missing: %q", blog.Log)
	}
	if strings.Contains(out.String(), "supersecret-value-123") {
		t.Fatalf("the secret reached the runner's own output: %s", out)
	}
	if ran, _ := rn.once(t.Context()); ran {
		t.Fatal("nothing was queued, yet a job ran")
	}
}

func TestRunnerReportsAFailingScriptAndABadEntry(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	rn, out := e.newRunnerFor(e.runner("r1"))

	e.saveScript(base, "run.sh", map[string]string{"run.sh": "echo \"${KERA_MARK}step work\"\necho oops >&2\nexit 3\n"}, "")
	rv := e.runNow(base)
	if _, err := rn.once(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := e.adminRunOut(base, rv.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "work") || !strings.Contains(got.Error, "3") {
		t.Fatalf("failing script: %+v\n%s", got, out)
	}

	e.saveScript(base, "run.rb", map[string]string{"run.rb": "puts 1"}, "")
	rv = e.runNow(base)
	if _, err := rn.once(t.Context()); err != nil {
		t.Fatal(err)
	}
	got = e.adminRunOut(base, rv.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, ".sh or .py") {
		t.Fatalf("bad entry: %+v", got)
	}
}

func TestRunnerStopsACancelledScriptAndItsChildren(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	e.saveScript(base, "run.sh", map[string]string{"run.sh": "echo \"${KERA_MARK}step wait\"\nsleep 120 &\nsleep 120\n"}, "")
	rv := e.runNow(base)
	rn, out := e.newRunnerFor(e.runner("r1"))
	done := make(chan error, 1)
	go func() { _, err := rn.once(t.Context()); done <- err }()
	deadline := time.Now().Add(10 * time.Second)
	for e.count(`SELECT count(*) FROM pipeline_run WHERE id = $1 AND claimed_at IS NOT NULL`, rv.ID) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("never claimed\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	e.call(e.admin, "POST", base+"/runs/"+itoa(rv.ID)+"/cancel", nil, http.StatusOK, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the runner did not stop the script\n%s", out)
	}
	got := e.adminRunOut(base, rv.ID)
	if got.Status != "failed" || got.Error != "Cancelled by an admin." {
		t.Fatalf("cancelled run: %+v", got)
	}
}

func TestRunnerTokenRefusedIsAnError(t *testing.T) {
	e := newEnv(t, false)
	rn, _ := e.newRunnerFor("kr-wrong")
	if _, err := rn.once(t.Context()); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a wrong runner token must be reported: %v", err)
	}
}

func TestRunnerRunsAPythonEntryAndKeepsTheWorkspaceBetweenRuns(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	e.saveScript(base, "job.py", map[string]string{"job.py": `import os
n = int(open("state.txt").read()) + 1 if os.path.exists("state.txt") else 1
open("state.txt", "w").write(str(n))
print(os.environ["KERA_MARK"] + "count runs=%d" % n)
print("script dir has job.py:", os.path.exists(os.path.join(os.environ["KERA_SCRIPT_DIR"], "job.py")))
`}, "")
	rn, out := e.newRunnerFor(e.runner("r1"))
	var last runOut
	for i := 1; i <= 2; i++ {
		last = e.runNow(base)
		if _, err := rn.once(t.Context()); err != nil {
			t.Fatal(err)
		}
		last = e.adminRunOut(base, last.ID)
		if last.Status != "ok" {
			t.Fatalf("run %d: %+v\n%s", i, last, out)
		}
	}
	st := stepByName(last, "start")
	if st == nil || st.Counts["runs"] != 2 { // the workspace (state.txt) survived the first run
		t.Fatalf("workspace state lost between runs: %+v\n%s", st, out)
	}
}

func TestNotebookScriptKeepsLineNumbersAndDeclaresSteps(t *testing.T) {
	code := "echo setup\n#%% fetch data\necho 1\n#%%\necho 2\n#%%   build\n"
	got := notebookScript(code, "bash", "::kera-x::")
	lines := strings.Split(got, "\n")
	if len(lines) != len(strings.Split(code, "\n")) {
		t.Fatalf("line count changed: %q", got)
	}
	if !strings.Contains(lines[1], "::kera-x::steps fetch-data cell-2 build") || !strings.Contains(lines[1], "::kera-x::step fetch-data") ||
		!strings.Contains(lines[3], "::kera-x::step cell-2") || !strings.Contains(lines[5], "::kera-x::step build") || strings.Contains(lines[3], "steps") {
		t.Fatalf("headers: %q", got)
	}
	if lines[0] != "echo setup" || lines[2] != "echo 1" {
		t.Fatalf("code lines must not change: %q", got)
	}
	py := notebookScript("x = 1\n#%% one\nprint(x)\n", "python", "::kera-x::")
	if !strings.Contains(py, `print("::kera-x::steps one", flush=True); print("::kera-x::step one", flush=True)`) {
		t.Fatalf("python header: %q", py)
	}
	if notebookScript("echo hi\n", "bash", "m") != "echo hi\n" || notebookScript(code, "", "m") != code {
		t.Fatal("a script without headers, or a language we cannot mark, must be unchanged")
	}
	// end to end: a notebook script reports its cells as steps
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	e.saveScript(base, "run.sh", map[string]string{"run.sh": "#%% first\necho one\n#%% second\necho two\n"}, "")
	rv := e.runNow(base)
	rn, out := e.newRunnerFor(e.runner("r1"))
	if _, err := rn.once(t.Context()); err != nil {
		t.Fatal(err)
	}
	got2 := e.adminRunOut(base, rv.ID)
	if got2.Status != "ok" || stepByName(got2, "first") == nil || stepByName(got2, "second") == nil || stepByName(got2, "second").Status != "ok" {
		t.Fatalf("notebook steps: %+v\n%s", got2, out)
	}
}

func TestRequirementsGiveAShellScriptThePythonEnvironment(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	// a comment-only requirements.txt: the environment is made without needing the network
	e.saveScript(base, "run.sh", map[string]string{"run.sh": "echo \"venv=$(dirname \"$(command -v python3)\")\"\n", "requirements.txt": "# nothing to install\n"}, "")
	rv := e.runNow(base)
	rn, out := e.newRunnerFor(e.runner("r1"))
	if _, err := rn.once(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := e.adminRunOut(base, rv.ID)
	st := stepByName(got, "start")
	var log struct{ Log string }
	if st != nil {
		e.call(e.admin, "GET", base+"/runs/"+itoa(rv.ID)+"/steps/"+itoa(int64(st.Seq)), nil, http.StatusOK, &log)
	}
	if got.Status != "ok" || !strings.Contains(log.Log, ".venvs/") {
		t.Fatalf("python3 in a shell script must come from the cached environment: %+v\n%q\n%s", got, log.Log, out)
	}
}
