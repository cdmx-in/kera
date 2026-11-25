package admin

import (
	"net/http"
	"testing"
	"time"
)

func (e *env) step(run, msg string, seq int, kind, name string, at time.Time, ms int, prompt, cached, completion any, detail string) {
	e.t.Helper()
	e.exec(`INSERT INTO run_step (run_id, message_id, user_id, model_id, seq, round, kind, name, started_at, duration_ms,
		prompt_tokens, cached_tokens, completion_tokens, finish, error, detail)
		VALUES ($1, $2, (SELECT id FROM "user" WHERE email = 'pat@example.com'), 'm1', $3, 0, $4, $5, $6, $7, $8, $9, $10, '',
		CASE WHEN $4 = 'tool' AND $5 = 'broken' THEN 'x' ELSE '' END, $11::jsonb)`, run, msg, seq, kind, name, at, ms, prompt, cached, completion, detail)
}

func TestTraceRoutes(t *testing.T) {
	e := setup(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	// message a: an older attempt and a newer one (a regenerated answer keeps its message id)
	e.step("run-old", "a", 0, "model", "m", now.Add(-2*time.Hour), 100, 10, nil, 1, `{}`)
	e.step("run-new", "a", 0, "model", "m", now.Add(-time.Minute), 800, 100, 64, 20, `{"prompt_chars":400}`)
	e.step("run-new", "a", 1, "tool", "lookup", now.Add(-time.Minute+800*time.Millisecond), 300, nil, nil, nil, `{"result_chars":1200,"is_error":false}`)
	e.step("run-new", "a", 2, "tool", "broken", now.Add(-time.Minute+1100*time.Millisecond), 50, nil, nil, nil, `{"result_chars":30,"is_error":true}`)
	e.step("run-new", "a", 3, "model", "m", now.Add(-time.Minute+1200*time.Millisecond), 600, 150, nil, 40, `{}`)
	e.step("run-ancient", "z", 0, "model", "m", now.Add(-30*24*time.Hour), 10, 1, nil, 1, `{}`)

	for _, path := range []string{"/api/v1/admin/runs", "/api/v1/admin/runs/a/steps"} {
		if code, _ := e.do("GET", path, e.user, nil); code != http.StatusForbidden {
			t.Errorf("non-admin GET %s = %d", path, code)
		}
		if code, _ := e.do("GET", path, "", nil); code != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s = %d", path, code)
		}
	}

	var runs []traceRun
	e.ok("GET", "/api/v1/admin/runs", nil, &runs)
	if len(runs) != 2 || runs[0].RunID != "run-new" || runs[1].RunID != "run-old" { // the 30-day-old run is outside the default 7 days
		t.Fatalf("runs %+v", runs)
	}
	n := runs[0]
	if n.MessageID != "a" || n.UserEmail != "pat@example.com" || n.ModelID != "m1" || n.Rounds != 2 || n.ToolCalls != 2 || n.ToolErrors != 1 ||
		n.PromptTokens != 250 || n.CachedTokens != 64 || n.CompletionTokens != 60 || !n.Failed || n.DurationMs != 1800 {
		t.Fatalf("summary %+v", n)
	}
	if runs[1].Failed || runs[1].CachedTokens != 0 {
		t.Fatalf("old run %+v", runs[1])
	}
	e.ok("GET", "/api/v1/admin/runs?days=60&limit=1", nil, &runs)
	if len(runs) != 1 {
		t.Fatalf("limit: %d runs", len(runs))
	}
	e.ok("GET", "/api/v1/admin/runs?days=60", nil, &runs)
	if len(runs) != 3 {
		t.Fatalf("60 days: %d runs", len(runs))
	}
	e.fails(http.StatusBadRequest, "bad_range", "GET", "/api/v1/admin/runs?days=0", nil)

	var d struct {
		traceRun
		Steps []traceStep `json:"steps"`
	}
	e.ok("GET", "/api/v1/admin/runs/a/steps", nil, &d)
	if d.RunID != "run-new" || d.MessageID != "a" || d.UserEmail != "pat@example.com" || d.DurationMs != 1800 || len(d.Steps) != 4 {
		t.Fatalf("detail %+v", d)
	}
	s := d.Steps
	if s[0].Kind != "model" || *s[0].PromptTokens != 100 || *s[0].CachedTokens != 64 || s[1].Name != "lookup" || s[1].PromptTokens != nil ||
		string(s[1].Detail) != `{"is_error":false,"result_chars":1200}` || s[2].Error != "x" || !s[0].StartedAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("steps %+v", s)
	}
	e.fails(http.StatusNotFound, "not_found", "GET", "/api/v1/admin/runs/nope/steps", nil)
}
