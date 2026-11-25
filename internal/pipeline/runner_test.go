package pipeline

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type scriptResp struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

// kmode makes a Kera-run pipeline with a script and a daily timetable at 06:30 UTC; it returns the admin paths.
func (e *tenv) kmode(name string) (pipe, string) {
	e.t.Helper()
	p, _ := e.mkPipe(name)
	base := "/api/v1/admin/pipelines/" + name
	e.call(e.admin, "PUT", base+"/script", map[string]any{"files": map[string]string{"run.sh": "echo hi\n"}, "entry": "run.sh"}, http.StatusCreated, nil)
	e.call(e.admin, "PUT", base+"/schedule", map[string]any{"run_mode": "kera", "scheduled": true, "frequency": "daily", "at_minute": 6*60 + 30, "timezone": "UTC"}, http.StatusOK, nil)
	return p, base
}

func (e *tenv) runner(name string) string {
	e.t.Helper()
	var out struct{ Token string }
	e.call(e.admin, "POST", "/api/v1/admin/pipeline-runners", map[string]any{"name": name}, http.StatusCreated, &out)
	return out.Token
}

func (e *tenv) claim(tok string) (int, job) {
	e.t.Helper()
	var j job
	code, b := e.do(tok, "POST", "/api/v1/runner/claim", map[string]any{"version": "test", "host": "box"})
	if code == http.StatusOK {
		e.call(tok, "POST", "/api/v1/runner/claim", nil, http.StatusNoContent, nil) // nothing else is queued
		_ = b
	}
	return code, j
}

func TestKeraRunTimetableQueuesOnceAndRunnerClaims(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	tok := e.runner("r1")
	ctx := t.Context()

	e.s.tick(ctx, e.clk.now()) // 06:00: not due yet
	if n := e.count(`SELECT count(*) FROM pipeline_run`); n != 0 {
		t.Fatalf("queued before its slot: %d runs", n)
	}
	e.clk.set(time.Date(2026, 3, 10, 6, 31, 0, 0, time.UTC))
	e.s.tick(ctx, e.clk.now())
	e.s.tick(ctx, e.clk.now()) // a second tick (another process) must not queue it again
	if n := e.count(`SELECT count(*) FROM pipeline_run WHERE queued AND trigger = 'schedule' AND script_version = 1`); n != 1 {
		t.Fatalf("slot queued %d times", n)
	}

	var j job
	e.call(tok, "POST", "/api/v1/runner/claim", map[string]any{"version": "v9", "host": "box"}, http.StatusOK, &j)
	if j.Pipeline != "docs" || j.Entry != "run.sh" || j.Files["run.sh"] != "echo hi\n" || !strings.HasPrefix(j.Token, "kj-") {
		t.Fatalf("job: %+v", j)
	}
	e.call(tok, "POST", "/api/v1/runner/claim", nil, http.StatusNoContent, nil) // already taken
	if n := e.count(`SELECT count(*) FROM pipeline_run WHERE NOT queued AND claimed_at IS NOT NULL AND runner_id IS NOT NULL`); n != 1 {
		t.Fatalf("claimed runs: %d", n)
	}

	// the job token works on this run's routes, and only there
	runPath := "/api/v1/pipelines/docs/runs/" + itoa(j.Run)
	e.call(j.Token, "POST", runPath+"/steps", map[string]any{"steps": []map[string]any{{"name": "go", "log": "hello\n"}}}, http.StatusOK, nil)
	var rv runOut
	e.call(j.Token, "GET", runPath, nil, http.StatusOK, &rv)
	if rv.Version != 1 || rv.Queued || rv.Source["client"] == "" {
		t.Fatalf("run: %+v", rv)
	}
	e.call(j.Token, "GET", "/api/v1/pipelines/docs/runs/"+itoa(j.Run+1), nil, http.StatusUnauthorized, nil)
	other, _ := e.mkPipe("other")
	_ = other
	e.call(j.Token, "GET", "/api/v1/pipelines/other/runs/"+itoa(j.Run), nil, http.StatusUnauthorized, nil)
	e.call(j.Token+"0", "GET", runPath, nil, http.StatusUnauthorized, nil)

	// once the run is closed the job token stops working
	e.call(j.Token, "POST", runPath+"/finish", map[string]any{}, http.StatusOK, nil)
	e.s.tick(ctx, e.clk.now())
	e.call(j.Token, "GET", runPath, nil, http.StatusUnauthorized, nil)
	_ = base
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestSlotDueWhileARunIsOpenIsSkipped(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	var rv runOut
	e.call(e.admin, "POST", base+"/run", nil, http.StatusOK, &rv) // a run is open (queued)
	e.clk.set(time.Date(2026, 3, 10, 6, 31, 0, 0, time.UTC))
	e.s.tick(t.Context(), e.clk.now()) // the 06:30 slot is due now
	if n := e.count(`SELECT count(*) FROM pipeline_run`); n != 1 {
		t.Fatalf("a slot queued a second run while one was open: %d runs", n)
	}
	var next time.Time
	if err := e.pool.QueryRow(t.Context(), `SELECT next_run_at FROM pipeline WHERE name = 'docs'`).Scan(&next); err != nil || !next.After(e.clk.now()) {
		t.Fatalf("the skipped slot must still move the timetable on: %v %v", next, err)
	}
}

func TestRunnerQueueExpiresWithoutRunnerAndCancelQueued(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	var rv runOut
	e.call(e.admin, "POST", base+"/run", nil, http.StatusOK, &rv) // Run now
	if !rv.Queued || rv.Trigger != "manual" {
		t.Fatalf("run now: %+v", rv)
	}
	e.call(e.admin, "POST", base+"/run", nil, http.StatusConflict, nil) // one open run at a time
	e.clk.add(11 * time.Minute)
	e.s.tick(t.Context(), e.clk.now())
	var after runOut
	e.call(e.admin, "GET", base+"/runs/"+itoa(rv.ID), nil, http.StatusOK, &after)
	if after.Status != "failed" || !strings.Contains(after.Error, "No runner picked this up") {
		t.Fatalf("expired queued run: %+v", after)
	}

	e.call(e.admin, "POST", base+"/run", nil, http.StatusOK, &rv)
	e.call(e.admin, "POST", base+"/runs/"+itoa(rv.ID)+"/cancel", nil, http.StatusOK, &after)
	if after.Status != "failed" || !strings.Contains(after.Error, "Cancelled by") {
		t.Fatalf("cancelled queued run: %+v", after)
	}
}

func TestCancelRunningRunIsAskedOfTheRunner(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	tok := e.runner("r1")
	var rv runOut
	e.call(e.admin, "POST", base+"/run", nil, http.StatusOK, &rv)
	var j job
	e.call(tok, "POST", "/api/v1/runner/claim", map[string]any{}, http.StatusOK, &j)
	e.call(e.admin, "POST", base+"/runs/"+itoa(rv.ID)+"/cancel", nil, http.StatusOK, &rv)
	if rv.Status != "running" || !rv.Cancelling {
		t.Fatalf("a running run is asked to stop, not closed: %+v", rv)
	}
	var seen runOut
	e.call(j.Token, "GET", "/api/v1/pipelines/docs/runs/"+itoa(j.Run), nil, http.StatusOK, &seen)
	if !seen.Cancelling {
		t.Fatalf("the runner cannot see the cancel request: %+v", seen)
	}
}

func TestScriptVersionsEnvAndSecrets(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	// a second save is version 2 and the claim of a queued run uses the version it was queued with
	e.call(e.admin, "PUT", base+"/script", map[string]any{"files": map[string]string{"run.sh": "echo v2\n", "lib/x.py": "print(1)\n"}, "entry": "run.sh", "note": "second"}, http.StatusCreated, nil)
	var list scriptListOut
	e.call(e.admin, "GET", base+"/script", nil, http.StatusOK, &list)
	if list.Current.Version != 2 || len(list.Versions) != 2 || list.Current.Files["lib/x.py"] == "" {
		t.Fatalf("versions: %+v", list)
	}
	var old scriptOut
	e.call(e.admin, "GET", base+"/script/1", nil, http.StatusOK, &old)
	if old.Files["run.sh"] != "echo hi\n" {
		t.Fatalf("version 1: %+v", old)
	}
	for _, bad := range []map[string]any{
		{"files": map[string]string{"../evil": "x"}, "entry": "../evil"},
		{"files": map[string]string{"/abs": "x"}, "entry": "/abs"},
		{"files": map[string]string{"a.sh": "x"}, "entry": "missing.sh"},
		{"files": map[string]string{}, "entry": "a"},
		{"files": map[string]string{"a.sh": "x\x00"}, "entry": "a.sh"},
	} {
		e.call(e.admin, "PUT", base+"/script", bad, http.StatusBadRequest, nil)
	}
	e.call(e.user, "GET", base+"/script", nil, http.StatusForbidden, nil) // admin only

	// environment: a secret is never read back, a plain value is, reserved names are refused
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "API_TOKEN", "value": "s3cr3t-value"}, http.StatusOK, nil)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "REGION", "value": "eu", "secret": false}, http.StatusOK, nil)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "KERA_RUN", "value": "1"}, http.StatusBadRequest, nil)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "KERA_API_KEY", "value": "a-key-the-script-needs"}, http.StatusOK, nil) // only the names the runner sets are reserved
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "1BAD", "value": "1"}, http.StatusBadRequest, nil)
	code, raw := e.do(e.admin, "GET", base+"/env", nil)
	if code != http.StatusOK || strings.Contains(string(raw), "s3cr3t-value") || !strings.Contains(string(raw), `"eu"`) {
		t.Fatalf("env listing leaks or hides: %d %s", code, raw)
	}
	var stored []byte
	if err := e.pool.QueryRow(t.Context(), `SELECT value_enc FROM pipeline_env WHERE name = 'API_TOKEN'`).Scan(&stored); err != nil || strings.Contains(string(stored), "s3cr3t-value") {
		t.Fatalf("secret stored in the clear: %v", err)
	}

	// the runner gets the decrypted environment with the job
	tok := e.runner("r1")
	e.call(e.admin, "POST", base+"/run", nil, http.StatusOK, nil)
	var j job
	e.call(tok, "POST", "/api/v1/runner/claim", map[string]any{}, http.StatusOK, &j)
	if j.Env["API_TOKEN"] != "s3cr3t-value" || j.Env["REGION"] != "eu" || j.Version != 2 {
		t.Fatalf("job env/version: %+v", j)
	}
	e.call(e.admin, "DELETE", base+"/env/REGION", nil, http.StatusOK, nil)
}

func TestRunnerAuthAndModes(t *testing.T) {
	e := newEnv(t, false)
	e.call("kr-nope", "POST", "/api/v1/runner/claim", nil, http.StatusUnauthorized, nil)
	e.call(e.admin, "POST", "/api/v1/runner/claim", nil, http.StatusUnauthorized, nil) // a session is not a runner
	p, _ := e.mkPipe("ext")
	e.call(p.tok, "POST", "/api/v1/runner/claim", nil, http.StatusUnauthorized, nil) // nor is a pipeline token
	base := "/api/v1/admin/pipelines/ext"
	e.call(e.admin, "POST", base+"/run", nil, http.StatusConflict, nil)                                                         // external pipeline: nothing to run
	e.call(e.admin, "PUT", base+"/schedule", map[string]any{"run_mode": "kera", "scheduled": true}, http.StatusBadRequest, nil) // no script yet
	e.call(e.admin, "PUT", base+"/schedule", map[string]any{"timezone": "Not/AZone"}, http.StatusBadRequest, nil)
	e.call(e.admin, "PUT", base+"/schedule", map[string]any{"frequency": "yearly"}, http.StatusBadRequest, nil)
	// a request body cannot open a queued or scheduled run
	code, _ := e.do(p.tok, "POST", p.path("/runs"), map[string]any{"trigger": "schedule"})
	if code != http.StatusBadRequest {
		t.Fatalf("a script may not claim the schedule trigger: %d", code)
	}
	// a rotated runner token stops the old one
	var created struct {
		Runner runnerOut
		Token  string
	}
	e.call(e.admin, "POST", "/api/v1/admin/pipeline-runners", map[string]any{"name": "r2"}, http.StatusCreated, &created)
	e.call(created.Token, "POST", "/api/v1/runner/claim", nil, http.StatusNoContent, nil)
	var rot struct{ Token string }
	e.call(e.admin, "POST", "/api/v1/admin/pipeline-runners/"+created.Runner.ID+"/token", nil, http.StatusOK, &rot)
	e.call(created.Token, "POST", "/api/v1/runner/claim", nil, http.StatusUnauthorized, nil)
	e.call(rot.Token, "POST", "/api/v1/runner/claim", nil, http.StatusNoContent, nil)
	var list struct{ Runners []runnerOut }
	e.call(e.admin, "GET", "/api/v1/admin/pipeline-runners", nil, http.StatusOK, &list)
	if len(list.Runners) != 1 || !list.Runners[0].Online {
		t.Fatalf("runners: %+v", list.Runners)
	}
}

func TestMissedSlotRunsOnceAfterDowntime(t *testing.T) {
	e := newEnv(t, false)
	e.kmode("docs")
	e.clk.set(time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)) // four slots missed
	e.s.tick(t.Context(), e.clk.now())
	e.s.tick(t.Context(), e.clk.now())
	if n := e.count(`SELECT count(*) FROM pipeline_run WHERE trigger = 'schedule'`); n != 1 {
		t.Fatalf("missed slots queued %d runs, want 1", n)
	}
	var next time.Time
	if err := e.pool.QueryRow(t.Context(), `SELECT next_run_at FROM pipeline WHERE name = 'docs'`).Scan(&next); err != nil || !next.After(e.clk.now()) {
		t.Fatalf("next slot not moved on: %v %v", next, err)
	}
}
