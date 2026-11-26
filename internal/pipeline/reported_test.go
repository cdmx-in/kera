package pipeline

import (
	"net/http"
	"strings"
	"testing"
)

// The reported steps are what runs really sent, in run order, each from the newest run that got to it, with the
// code location the script gave.
func TestReportedSteps(t *testing.T) {
	e := newEnv(t, false)
	p, _ := e.mkPipe("outside")
	other, _ := e.mkPipe("other")
	url := "/api/v1/admin/pipelines/outside/reported-steps"
	type reply struct {
		Runs  int
		Steps []reportedStep
	}
	var got reply
	e.call(e.admin, "GET", url, nil, http.StatusOK, &got)
	if got.Runs != 0 || got.Steps == nil || len(got.Steps) != 0 {
		t.Fatalf("a pipeline that never ran: %+v", got)
	}
	finish := func(run int64) {
		e.call(p.tok, "POST", p.path("/runs/")+itoa(run)+"/finish", map[string]any{}, http.StatusOK, nil)
	}
	full := p.start(startIn{Steps: []string{"filter", "context", "analyze", "deliver", "cleanup"}})
	p.steps(full, stepIn{Name: "filter", Status: "running", CodeAt: "worker.py:10 process()"})
	p.steps(full, stepIn{Name: "filter", Status: "ok"}, stepIn{Name: "context", Status: "running", CodeAt: "  worker.py:20 process()\x00  "})
	p.steps(full, stepIn{Name: "context", CodeAt: "worker.py:99 later()", Counts: map[string]int64{"items": 4}}) // the first location stays
	p.steps(full, stepIn{Name: "analyze", Status: "running"}, stepIn{Name: "deliver", Status: "running", CodeAt: strings.Repeat("x", 900)})
	finish(full)
	short := p.start(startIn{Steps: []string{"filter", "deliver"}}) // a shorter path: deliver is its second step
	p.steps(short, stepIn{Name: "filter", Status: "running"}, stepIn{Name: "deliver", Status: "running", CodeAt: "worker.py:30 process()"})
	finish(short)

	e.call(e.admin, "GET", url, nil, http.StatusOK, &got)
	var names []string
	by := map[string]reportedStep{}
	for _, s := range got.Steps {
		names = append(names, s.Name)
		by[s.Name] = s
	}
	if got.Runs != 2 || strings.Join(names, " ") != "filter context analyze deliver cleanup" {
		t.Fatalf("runs %d, order %v", got.Runs, names)
	}
	if c := by["context"]; c.Run != full || c.CodeAt != "worker.py:20 process()" || c.Counts["items"] != 4 {
		t.Fatalf("context: %+v", c)
	}
	if d := by["deliver"]; d.Run != short || d.CodeAt != "worker.py:30 process()" {
		t.Fatalf("deliver comes from the newest run: %+v", d)
	}
	if f := by["filter"]; f.Run != short || f.CodeAt != "" { // the newest run sent none: nothing is carried over from an older one
		t.Fatalf("filter: %+v", f)
	}
	if c := by["cleanup"]; c.Status != "skipped" {
		t.Fatalf("a declared step that never ran: %+v", c)
	}
	if n := e.count(`SELECT max(length(code_at)) FROM pipeline_step`); n > maxCodeAt {
		t.Fatalf("a code location of %d characters was stored", n)
	}
	var run runOut
	e.call(e.admin, "GET", "/api/v1/admin/pipelines/outside/runs/"+itoa(full), nil, http.StatusOK, &run)
	if (*run.Steps)[1].CodeAt != "worker.py:20 process()" {
		t.Fatalf("the run view carries the location: %+v", (*run.Steps)[1])
	}
	for _, tok := range []string{"", p.tok, other.tok, e.user} {
		code, _ := e.do(tok, "GET", url, nil)
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("a non-admin got %d", code)
		}
	}
}
