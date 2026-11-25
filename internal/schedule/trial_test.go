package schedule

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/cowork"
)

func TestPreviewListsTheNextRunsOfAnUnsavedTimetable(t *testing.T) {
	f := newFixture(t)
	ada := &auth.User{ID: "u1", Role: "admin"}
	w := f.call(t, f.s.preview, ada, "POST", "/api/v1/cowork/schedules/preview",
		`{"frequency":"weekdays","at_minute":540,"timezone":"Asia/Kolkata"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	var out struct {
		Runs []int64 `json:"runs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Runs) != 3 {
		t.Fatalf("runs %v err %v", out.Runs, err)
	}
	loc, _ := time.LoadLocation("Asia/Kolkata")
	prev := time.Now()
	for _, sec := range out.Runs {
		at := time.Unix(sec, 0).In(loc)
		if !at.After(prev) || at.Hour() != 9 || at.Minute() != 0 || at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
			t.Fatalf("run %s after %s: want a later weekday at 09:00", at, prev)
		}
		prev = at
	}
	if n := f.count(t, `SELECT count(*) FROM scheduled_task`); n != 0 {
		t.Fatalf("%d tasks saved by a preview", n)
	}
	for name, body := range map[string]string{
		"bad zone":      `{"frequency":"daily","at_minute":0,"timezone":"Mars/Base"}`,
		"bad frequency": `{"frequency":"yearly","at_minute":0,"timezone":"UTC"}`,
		"hour minute":   `{"frequency":"hourly","at_minute":90,"timezone":"UTC"}`,
	} {
		if w := f.call(t, f.s.preview, ada, "POST", "/api/v1/cowork/schedules/preview", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

// A test run is read-only and unattended, is kept as a marked task, and leaves no schedule, notes or timetable behind.
func TestTestRunIsMarkedReadOnlyAndSchedulesNothing(t *testing.T) {
	f := newFixture(t)
	ada := &auth.User{ID: "u1", Role: "admin"}
	for name, body := range map[string]string{
		"no goal":  `{"title":"x","model_id":"m1"}`,
		"no model": `{"title":"x","prompt":"hi"}`,
		"features": `{"title":"x","prompt":"hi","model_id":"m1","features":[1]}`,
	} {
		if w := f.call(t, f.s.testRun, ada, "POST", "/api/v1/cowork/test-run", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	w := f.call(t, f.s.testRun, ada, "POST", "/api/v1/cowork/test-run", `{"title":"Morning digest","prompt":"echo system","model_id":"m1","tool_ids":["builtin:code"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("test run: %d %s", w.Code, w.Body)
	}
	var out struct {
		ChatID string `json:"chat_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	f.settle(t)

	var title, mode, content string
	if err := f.pool.QueryRow(context.Background(), `SELECT c.title, c.mode, m.content FROM chat c
		JOIN message m ON m.chat_id = c.id AND m.role = 'assistant' WHERE c.id = $1 AND c.meta->>'test_run' = 'true'`, out.ChatID).Scan(&title, &mode, &content); err != nil {
		t.Fatalf("the test chat is not marked: %v", err)
	}
	if title != "Test · Morning digest" || mode != "cowork" {
		t.Fatalf("chat %q in mode %q", title, mode)
	}
	if content != "test=true unattended=true" {
		t.Errorf("the model was told %q", content)
	}
	if n := f.count(t, `SELECT count(*) FROM scheduled_task`); n != 0 {
		t.Fatalf("%d scheduled tasks after a test run", n)
	}
	list, err := cowork.List(context.Background(), f.pool, "u1")
	if err != nil || len(list) != 1 || !list[0].Test {
		t.Fatalf("task list %+v err %v: the run should be listed as a test", list, err)
	}
}

func TestTestRunWaitsForTheLastOne(t *testing.T) {
	f := newFixture(t)
	ada := &auth.User{ID: "u1", Role: "admin"}
	body := `{"title":"slow","prompt":"block","model_id":"m1"}`
	if w := f.call(t, f.s.testRun, ada, "POST", "/api/v1/cowork/test-run", body); w.Code != http.StatusOK {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	if w := f.call(t, f.s.testRun, ada, "POST", "/api/v1/cowork/test-run", body); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already going") {
		t.Fatalf("second while the first runs: %d %s", w.Code, w.Body)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO "user" (id, name, email, role) VALUES ('u3', 'Cy', 'cy@example.com', 'admin')`); err != nil {
		t.Fatal(err)
	}
	bob := &auth.User{ID: "u3", Role: "admin"}
	if w := f.call(t, f.s.testRun, bob, "POST", "/api/v1/cowork/test-run", body); w.Code != http.StatusOK {
		t.Fatalf("another person's test is not blocked by it: %d %s", w.Code, w.Body)
	}
}
