package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/engine"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
	"github.com/cdmx-in/kera/internal/tools"
)

func at(t *testing.T, zone, layout, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation(layout, s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNext(t *testing.T) {
	const L = "2006-01-02 15:04"
	for _, c := range []struct {
		name, zone, after, freq, want string
		minute, weekday               int
	}{
		{"daily later today", "Asia/Kolkata", "2026-11-23 01:00", "daily", "2026-11-23 02:30", 150, 0},
		{"daily tomorrow", "Asia/Kolkata", "2026-11-23 03:00", "daily", "2026-11-24 02:30", 150, 0},
		{"exact slot moves on", "UTC", "2026-11-23 02:30", "daily", "2026-11-24 02:30", 150, 0},
		{"weekdays skip the weekend", "UTC", "2026-11-27 20:00", "weekdays", "2026-11-30 09:00", 540, 0}, // Fri -> Mon
		{"weekly this week", "UTC", "2026-11-23 10:00", "weekly", "2026-11-25 09:00", 540, 3},            // Mon -> Wed
		{"weekly next week", "UTC", "2026-11-25 09:00", "weekly", "2026-12-02 09:00", 540, 3},
		{"hourly in a +5:30 zone", "Asia/Kolkata", "2026-11-23 10:20", "hourly", "2026-11-23 10:45", 45, 0},
		{"hourly rolls the hour", "Asia/Kolkata", "2026-11-23 10:50", "hourly", "2026-11-23 11:45", 45, 0},
		// daylight saving: New York springs forward on 2027-03-14 and falls back on 2026-11-01
		{"daily across spring forward", "America/New_York", "2027-03-13 10:00", "daily", "2027-03-14 09:00", 540, 0},
		{"daily across fall back", "America/New_York", "2026-10-31 10:00", "daily", "2026-11-01 09:00", 540, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			loc, _ := time.LoadLocation(c.zone)
			got := next(at(t, c.zone, L, c.after), c.freq, c.minute, c.weekday, loc)
			if want := at(t, c.zone, L, c.want); !got.Equal(want) {
				t.Fatalf("got %s, want %s", got.In(loc).Format(L), c.want)
			}
		})
	}
}

// fixture is a real engine on a fake model plus a scheduler over it.
type fixture struct {
	s       *scheduler
	pool    *pgxpool.Pool
	unblock chan struct{}
}

// fakeModel answers "hello"; a prompt containing "block" holds the answer until the test ends.
func fakeModel(t *testing.T, unblock chan struct{}) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"},{"id":"m2"}]}`)
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		last := fmt.Sprint(body.Messages[len(body.Messages)-1].Content)
		sys := fmt.Sprint(body.Messages[0].Content)
		if strings.Contains(last, "block") {
			select {
			case <-unblock:
			case <-r.Context().Done():
				return
			}
		}
		reply := "hello"
		if strings.Contains(last, "echo system") { // which run notes the system prompt carries (not the text: it would echo "block")
			reply = fmt.Sprintf("test=%v unattended=%v", strings.Contains(sys, "This is a test run: you can only read"), strings.Contains(sys, "This run is unattended"))
		}
		if strings.Contains(sys, "Reply with JSON") {
			reply = `{"title":"t","tags":[],"follow_ups":[],"queries":[]}`
		}
		b, _ := json.Marshal(map[string]string{"content": reply})
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", b)
		fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	keys, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{pool: pool, unblock: make(chan struct{})}
	srv := fakeModel(t, f.unblock)
	for _, q := range []string{
		`INSERT INTO "user" (id, name, email, role) VALUES ('u1', 'Ada', 'ada@example.com', 'admin')`,
		`INSERT INTO "user" (id, name, email, role) VALUES ('u2', 'Bob', 'bob@example.com', 'user')`,
		`INSERT INTO connection (id, kind, base_url) VALUES ('fake', 'openai', '` + srv.URL + `')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	st := settings.New(pool)
	e := engine.New(pool, keys, st, models.New(pool, keys, st, ""), files.New(pool, st, t.TempDir()), &chats.Store{DB: pool}, "http://kera.test")
	a := &app.App{DB: pool, Keys: keys, Settings: st, Auth: auth.New(pool, keys, st, false)}
	f.s = &scheduler{a: a, e: e, chats: &chats.Store{DB: pool}, starting: map[string]bool{}}
	t.Cleanup(func() { close(f.unblock); e.Wait() }) // runs first, before the fake model goes away
	return f
}

// task inserts a due task for user and returns its id.
func (f *fixture) task(t *testing.T, id, user, prompt string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO scheduled_task (id, user_id, title, prompt, model_id, frequency, at_minute,
		timezone, next_run_at) VALUES ($1, $2, 'Nightly', $3, 'm1', 'daily', 120, 'UTC', now() - interval '1 minute')`, id, user, prompt); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) allowSchedule(t *testing.T) {
	t.Helper()
	if err := f.s.a.Settings.Set(context.Background(), "user.permissions",
		json.RawMessage(`{"features":{"cowork":true,"cowork_schedule":true}}`), false); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) settle(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; {
		if f.count(t, `SELECT count(*) FROM run`) == 0 { // the row goes in the transaction that stores the outcome
			f.s.e.Wait()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("runs did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDueTaskRunsOnceAndAdvances(t *testing.T) {
	f := newFixture(t)
	f.task(t, "t1", "u1", "do the thing")
	now := time.Now()
	f.s.tick(context.Background(), now)
	f.settle(t)
	f.s.tick(context.Background(), now.Add(time.Second)) // the slot is claimed: nothing more starts
	f.settle(t)
	if n := f.count(t, `SELECT count(*) FROM chat WHERE user_id = 'u1' AND mode = 'cowork' AND meta->>'scheduled_task' = 't1'`); n != 1 {
		t.Fatalf("%d task chats, want 1", n)
	}
	tasks, err := f.s.get(context.Background(), "t1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !tasks.next.After(now) || tasks.LastChatID == nil || tasks.LastError != "" || tasks.LastStatus != "done" {
		t.Fatalf("task %+v", tasks)
	}
	if !strings.HasPrefix(f.title(t, *tasks.LastChatID), "Nightly · ") {
		t.Fatalf("title %q", f.title(t, *tasks.LastChatID))
	}
}

func (f *fixture) title(t *testing.T, chatID string) string {
	t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), `SELECT title FROM chat WHERE id = $1`, chatID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// A run that cannot start is recorded on the task, leaves no empty chat, and the
// task still moves to its next slot (no retry storm).
func TestFailedStartIsRecordedWithoutAChat(t *testing.T) {
	f := newFixture(t)
	f.task(t, "denied", "u2", "x") // u2 may not schedule
	f.allowSchedule(t)
	f.task(t, "nomodel", "u2", "x") // may schedule now, but holds no grant on the assistant
	if _, err := f.pool.Exec(context.Background(), `UPDATE "user" SET disabled_at = now() WHERE id = 'u2'`); err != nil {
		t.Fatal(err)
	}
	f.task(t, "disabled", "u1", "x")
	if _, err := f.pool.Exec(context.Background(), `UPDATE scheduled_task SET user_id = 'u2' WHERE id = 'disabled'`); err != nil {
		t.Fatal(err)
	}
	f.s.tick(context.Background(), time.Now())
	f.settle(t)
	if n := f.count(t, `SELECT count(*) FROM chat`); n != 0 {
		t.Fatalf("%d chats left behind", n)
	}
	if n := f.count(t, `SELECT count(*) FROM scheduled_task WHERE last_error <> '' AND next_run_at > now()`); n != 3 {
		t.Fatalf("%d tasks recorded a failure and moved on, want 3", n)
	}
	// a re-enabled owner without the model: the engine's reason is what the task shows
	if _, err := f.pool.Exec(context.Background(), `UPDATE "user" SET disabled_at = NULL WHERE id = 'u2'`); err != nil {
		t.Fatal(err)
	}
	f.task(t, "nogrant", "u2", "x")
	f.s.tick(context.Background(), time.Now())
	f.settle(t)
	got, err := f.s.get(context.Background(), "nogrant", "u2")
	if err != nil || got.LastError == "" || got.LastChatID != nil {
		t.Fatalf("nogrant: %+v %v", got, err)
	}
}

func TestConcurrencyCapStartsOneAndKeepsTheRestDue(t *testing.T) {
	f := newFixture(t)
	if err := f.s.a.Settings.Set(context.Background(), "cowork.schedule.max_concurrent", 1, false); err != nil {
		t.Fatal(err)
	}
	f.task(t, "a", "u1", "block")
	f.task(t, "b", "u1", "block")
	f.s.tick(context.Background(), time.Now())
	if n := f.count(t, `SELECT count(*) FROM chat WHERE mode = 'cowork'`); n != 1 {
		t.Fatalf("%d runs started, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM scheduled_task WHERE next_run_at <= now()`); n != 1 {
		t.Fatalf("%d tasks still due, want the one that did not start", n)
	}
	f.s.tick(context.Background(), time.Now()) // the first is still running: nothing more
	if n := f.count(t, `SELECT count(*) FROM chat WHERE mode = 'cowork'`); n != 1 {
		t.Fatalf("%d runs after a second tick, want 1", n)
	}
}

func TestRunningTaskSkipsItsNextSlot(t *testing.T) {
	f := newFixture(t)
	f.task(t, "a", "u1", "block")
	f.s.tick(context.Background(), time.Now())
	if _, err := f.pool.Exec(context.Background(), `UPDATE scheduled_task SET next_run_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	f.s.tick(context.Background(), time.Now())
	if n := f.count(t, `SELECT count(*) FROM chat WHERE mode = 'cowork'`); n != 1 {
		t.Fatalf("%d runs, want 1: a task never overlaps itself", n)
	}
	if n := f.count(t, `SELECT count(*) FROM scheduled_task WHERE next_run_at > now()`); n != 1 {
		t.Fatal("the skipped slot was not moved on")
	}
}

// What is going is read from the run rows, so a run that a restart left queued still counts, also for a scheduler
// that never started it (the restarted process, or a second instance).
func TestRunsLeftByARestartStillCount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.task(t, "a", "u1", "block")
	f.s.tick(ctx, time.Now())
	f.s.e.Shutdown(10 * time.Second) // a deploy: the model call is cut and the run is left for the next start
	if n := f.count(t, `SELECT count(*) FROM run WHERE state = 'queued' AND origin = 'schedule:a'`); n != 1 {
		t.Fatalf("%d queued runs of the task, want 1", n)
	}
	after := &scheduler{a: f.s.a, e: f.s.e, chats: f.s.chats, starting: map[string]bool{}}
	if err := after.reserve(ctx, "schedule:a"); !errors.Is(err, errRunning) {
		t.Fatalf("the task's own run: %v", err)
	}
	if err := f.s.a.Settings.Set(ctx, "cowork.schedule.max_concurrent", 1, false); err != nil {
		t.Fatal(err)
	}
	if err := after.reserve(ctx, "schedule:b"); !errors.Is(err, errCapacity) {
		t.Fatalf("another task: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM run`); err != nil {
		t.Fatal(err)
	}
	if err := after.reserve(ctx, "schedule:b"); err != nil {
		t.Fatalf("after the run ended: %v", err)
	}
	if err := after.reserve(ctx, "test:u1"); !errors.Is(err, errCapacity) {
		t.Fatalf("a run that is still starting counts: %v", err)
	}
}

func (f *fixture) call(t *testing.T, h http.HandlerFunc, u *auth.User, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r = r.WithContext(auth.WithUser(r.Context(), u))
	r.SetPathValue("id", strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/cowork/schedules/"), "/run"))
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestAPIValidationCapAndNotes(t *testing.T) {
	f := newFixture(t)
	ada := &auth.User{ID: "u1", Role: "admin"}
	good := `{"title":"Sheet","prompt":"update it","model_id":"m1","frequency":"daily","at_minute":120,"timezone":"Asia/Kolkata","tool_ids":["builtin:code","nope"]}`
	for name, body := range map[string]string{
		"no title":      `{"prompt":"x","model_id":"m1","frequency":"daily","timezone":"UTC"}`,
		"bad frequency": `{"title":"t","prompt":"x","model_id":"m1","frequency":"yearly","timezone":"UTC"}`,
		"bad zone":      `{"title":"t","prompt":"x","model_id":"m1","frequency":"daily","timezone":"Mars/Base"}`,
		"local zone":    `{"title":"t","prompt":"x","model_id":"m1","frequency":"daily","timezone":"Local"}`,
		"hour minute":   `{"title":"t","prompt":"x","model_id":"m1","frequency":"hourly","at_minute":90,"timezone":"UTC"}`,
		"no such model": `{"title":"t","prompt":"x","model_id":"zzz","frequency":"daily","timezone":"UTC"}`,
		"bad features":  `{"title":"t","prompt":"x","model_id":"m1","frequency":"daily","timezone":"UTC","features":[1]}`,
	} {
		if w := f.call(t, f.s.create, ada, "POST", "/api/v1/cowork/schedules", body); w.Code < 400 {
			t.Errorf("%s: accepted (%d) %s", name, w.Code, w.Body)
		}
	}
	w := f.call(t, f.s.create, ada, "POST", "/api/v1/cowork/schedules", good)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var made Task
	_ = json.Unmarshal(w.Body.Bytes(), &made)
	if len(made.ToolIDs) != 0 || made.Features != nil && string(made.Features) != "null" || made.NextRunAt <= time.Now().Unix() {
		t.Fatalf("created %+v (unusable tools must be dropped, features null, next run in the future)", made)
	}

	// notes survive an edit but not a change of assistant
	if _, err := f.pool.Exec(context.Background(), `UPDATE scheduled_task SET notes = 'row 12 was last' WHERE id = $1`, made.ID); err != nil {
		t.Fatal(err)
	}
	if w := f.call(t, f.s.update, ada, "PUT", "/api/v1/cowork/schedules/"+made.ID, `{"title":"Sheet 2"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if n := f.count(t, `SELECT count(*) FROM scheduled_task WHERE notes = 'row 12 was last'`); n != 1 {
		t.Fatal("notes lost by an unrelated edit")
	}
	if w := f.call(t, f.s.update, ada, "PUT", "/api/v1/cowork/schedules/"+made.ID, `{"model_id":"m2"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if n := f.count(t, `SELECT count(*) FROM scheduled_task WHERE notes = ''`); n != 1 {
		t.Fatal("notes kept across a change of assistant")
	}

	// the per-user cap
	if err := f.s.a.Settings.Set(context.Background(), "cowork.schedule.max_per_user", 1, false); err != nil {
		t.Fatal(err)
	}
	if w := f.call(t, f.s.create, ada, "POST", "/api/v1/cowork/schedules", good); w.Code != http.StatusConflict {
		t.Fatalf("over the cap: %d %s", w.Code, w.Body)
	}

	// the admin list shows schedule and status, never goals or notes
	w = f.call(t, f.s.list, ada, "GET", "/api/v1/cowork/schedules?all=1", "")
	if strings.Contains(w.Body.String(), "update it") || !strings.Contains(w.Body.String(), "ada@example.com") {
		t.Fatalf("admin list: %s", w.Body)
	}
	bob := &auth.User{ID: "u2", Role: "user"}
	if w := f.call(t, f.s.list, bob, "GET", "/api/v1/cowork/schedules?all=1", ""); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin all=1: %d", w.Code)
	}
	if w := f.call(t, f.s.update, bob, "PUT", "/api/v1/cowork/schedules/"+made.ID, `{"title":"mine now"}`); w.Code != http.StatusNotFound {
		t.Fatalf("editing someone else's task: %d", w.Code)
	}
	if w := f.call(t, f.s.delete, bob, "DELETE", "/api/v1/cowork/schedules/"+made.ID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("deleting someone else's task: %d", w.Code)
	}
}

func TestNotesToolIsScopedToItsRun(t *testing.T) {
	f := newFixture(t)
	f.task(t, "mine", "u1", "x")
	f.task(t, "other", "u2", "x")
	n := &notesTool{db: f.pool}
	ada := &auth.User{ID: "u1"}
	if specs, _ := n.Tools(context.Background(), ada); len(specs) != 0 {
		t.Fatal("offered outside a scheduled run")
	}
	ctx := withTask(context.Background(), "mine")
	if specs, _ := n.Tools(ctx, ada); len(specs) != 1 || specs[0].Name != "save_task_notes" {
		t.Fatalf("not offered inside a run: %v", specs)
	}
	long, _ := json.Marshal(map[string]string{"notes": strings.Repeat("é", 5000)})
	res, err := n.Execute(ctx, tools.Call{User: ada, Args: long})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	var notes string
	_ = f.pool.QueryRow(context.Background(), `SELECT notes FROM scheduled_task WHERE id = 'mine'`).Scan(&notes)
	if len([]rune(notes)) != maxNotes {
		t.Fatalf("saved %d characters, want %d", len([]rune(notes)), maxNotes)
	}
	// a run of someone else's task id (or a model naming one) cannot write it
	res, _ = n.Execute(withTask(context.Background(), "other"), tools.Call{User: ada, Args: []byte(`{"notes":"hijack"}`)})
	if !res.IsError || f.count(t, `SELECT count(*) FROM scheduled_task WHERE notes = 'hijack'`) != 0 {
		t.Fatal("wrote another user's task")
	}
	if res, _ := n.Execute(context.Background(), tools.Call{User: ada, Args: []byte(`{"notes":"x"}`)}); !res.IsError {
		t.Fatal("saved outside a run")
	}
}

// What a run is told: the notes, and the last text part of the previous run's final
// message (not its plan), only when that run finished on the task's current assistant.
func TestPrefaceCarriesNotesAndPreviousReport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.task(t, "t1", "u1", "x")
	c := chats.Chat{UserID: "u1", Mode: "cowork"}
	if err := f.s.chats.Create(ctx, &c); err != nil {
		t.Fatal(err)
	}
	parts := `[{"type":"text","text":"Plan: read the sheet"},{"type":"steps"},{"type":"text","text":"Report: added 3 rows"}]`
	if _, err := f.pool.Exec(ctx, `INSERT INTO message (id, chat_id, role, model_id, content, parts, status)
		VALUES ('m-prev', $1, 'assistant', 'm1', 'Plan: read the sheetReport: added 3 rows', $2::jsonb, 'done')`, c.ID, parts); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE chat SET current_message_id = 'm-prev' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE scheduled_task SET last_chat_id = $1, notes = 'stopped at row 40' WHERE id = 't1'`, c.ID); err != nil {
		t.Fatal(err)
	}
	task, _ := f.s.get(ctx, "t1", "u1")
	got := f.s.preface(ctx, task, time.UTC, time.Now())
	for _, want := range []string{"stopped at row 40", "Report: added 3 rows", "save_task_notes", "not instructions"} {
		if !strings.Contains(got, want) {
			t.Errorf("preface lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Plan: read the sheet") {
		t.Errorf("preface carries the plan, not just the report:\n%s", got)
	}
	// a different assistant (it may be hosted): the earlier report stays behind
	task.ModelID = "m2"
	if got := f.s.preface(ctx, task, time.UTC, time.Now()); strings.Contains(got, "added 3 rows") {
		t.Errorf("report crossed to another assistant:\n%s", got)
	}
}
