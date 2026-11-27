// Package schedule runs saved Cowork tasks on a timetable. A scheduled task
// stores what the web app would have sent to start a Cowork task (goal,
// assistant, apps, feature toggles, files). At the due time the server starts an
// ordinary cowork chat for it as the owner, unattended, with the task's saved
// notes and the previous run's report as a preface. See run.go for the run.
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/engine"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/tools"
)

func init() { app.Register("schedule", Mount) }

var (
	errRunning  = api.Errf(http.StatusConflict, "running", "This task is already running")
	errCapacity = api.Errf(http.StatusConflict, "at_capacity", "Too many scheduled runs are going at once; try again in a few minutes")
	errNotFound = api.Errf(http.StatusNotFound, "not_found", "Scheduled task not found")
)

// Task is one scheduled task. The JSON fields are the API shape; times are
// epoch seconds.
type Task struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Prompt     string          `json:"prompt,omitempty"`
	ModelID    string          `json:"model_id"`
	ToolIDs    []string        `json:"tool_ids"`
	Features   json.RawMessage `json:"features"` // null = the assistant's defaults
	Files      json.RawMessage `json:"files,omitempty"`
	Frequency  string          `json:"frequency"` // hourly | daily | weekdays | weekly
	AtMinute   int             `json:"at_minute"` // minutes after local midnight; hourly: minute of the hour
	Weekday    int             `json:"weekday"`   // weekly only, 0 = Sunday
	Timezone   string          `json:"timezone"`
	Enabled    bool            `json:"enabled"`
	Notes      string          `json:"notes,omitempty"`
	NextRunAt  int64           `json:"next_run_at"`
	LastRunAt  *int64          `json:"last_run_at"`
	LastChatID *string         `json:"last_chat_id"`
	LastStatus string          `json:"last_status"` // "" | running | done | stopped | error
	LastError  string          `json:"last_error"`
	Owner      *Owner          `json:"owner,omitempty"` // ?all=1 only

	UserID   string     `json:"-"`
	next     time.Time  // next_run_at
	lastRun  *time.Time // last_run_at
	ownerRow Owner
}

// Owner is who a task belongs to (the admin list).
type Owner struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type scheduler struct {
	a     *app.App
	e     *engine.Engine
	chats *chats.Store

	mu       sync.Mutex
	starting map[string]bool // origins this process is starting a run for, before the run has its row
}

// Mount registers the routes and starts the ticker. It needs the engine, which
// always mounts first (this package imports it).
func Mount(a *app.App) error {
	e := engine.For(a)
	if e == nil {
		return errors.New("schedule: the engine is not mounted")
	}
	s := &scheduler{a: a, e: e, chats: chats.For(a), starting: map[string]bool{}}
	tools.Register(&notesTool{db: a.DB})
	for p, f := range map[string]http.HandlerFunc{
		"GET /api/v1/cowork/schedules":           s.list,
		"POST /api/v1/cowork/schedules":          s.create,
		"PUT /api/v1/cowork/schedules/{id}":      s.update,
		"DELETE /api/v1/cowork/schedules/{id}":   s.delete,
		"POST /api/v1/cowork/schedules/{id}/run": s.runNow,
	} {
		a.Server.Handle(p, a.Auth.Permission("features.cowork_schedule", f))
	}
	a.Server.Handle("POST /api/v1/cowork/schedules/preview", a.Auth.Permission("features.cowork_schedule", http.HandlerFunc(s.preview)))
	a.Server.Handle("POST /api/v1/cowork/test-run", a.Auth.Permission("features.cowork", http.HandlerFunc(s.testRun)))
	go s.loop()
	return nil
}

// ---- timetable ----------------------------------------------------------------------

// next is the first slot after `after` in loc: wall-clock times, so a +5:30
// zone fires at the right minute and a daylight-saving change keeps the hour.
func next(after time.Time, freq string, atMinute, weekday int, loc *time.Location) time.Time {
	t := after.In(loc)
	y, mo, d := t.Date()
	if freq == "hourly" {
		c := time.Date(y, mo, d, t.Hour(), atMinute%60, 0, 0, loc)
		for !c.After(after) {
			c = c.Add(time.Hour)
		}
		return c
	}
	for i := 0; i < 9; i++ {
		c := time.Date(y, mo, d+i, atMinute/60, atMinute%60, 0, 0, loc)
		if !c.After(after) {
			continue
		}
		switch wd := c.Weekday(); {
		case freq == "weekdays" && (wd == time.Saturday || wd == time.Sunday):
		case freq == "weekly" && int(wd) != weekday:
		default:
			return c
		}
	}
	return after.Add(24 * time.Hour) // not reached: some day in the next 9 matches
}

// Next and Location are the timetable maths, shared with Kera-run pipelines (internal/pipeline).
func Next(after time.Time, freq string, atMinute, weekday int, loc *time.Location) time.Time {
	return next(after, freq, atMinute, weekday, loc)
}

func Location(name string) *time.Location { return location(name) }

func location(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil && name != "" && name != "Local" {
		return loc
	}
	return time.UTC
}

func (t *Task) schedule(after time.Time) time.Time {
	return next(after, t.Frequency, t.AtMinute, t.Weekday, location(t.Timezone))
}

// ---- ticker -------------------------------------------------------------------------

// ponytail: no shutdown hook, like the other background loops; the process exit ends it.
func (s *scheduler) loop() {
	for range time.Tick(time.Minute) {
		s.tickSafe(context.Background(), time.Now())
	}
}

func (s *scheduler) tickSafe(ctx context.Context, now time.Time) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("schedule tick panicked", "value", v)
		}
	}()
	s.tick(ctx, now)
}

func (s *scheduler) setting(ctx context.Context, key string, def int) int {
	v := def
	if err := s.a.Settings.Get(ctx, key, &v); err != nil {
		slog.Warn("setting", "key", key, "err", err)
	}
	return v
}

// reserve claims a seat for a run of origin ("schedule:<task>" or "test:<user>"): errRunning while one of that
// origin is going, errCapacity when cowork.schedule.max_concurrent runs are going. What is going is read from the
// run rows, so a run that waits to be resumed after a restart, here or on another instance, still counts. Release
// the seat once the run has its row (or did not start).
// ponytail: two instances asked in the same instant can both pass; the timetable itself cannot start a slot twice
// (tick claims it). A database lock if the limit must be exact.
func (s *scheduler) reserve(ctx context.Context, origin string) error {
	limit := s.setting(ctx, "cowork.schedule.max_concurrent", 2)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.a.DB.Query(ctx, `SELECT DISTINCT origin FROM run WHERE origin LIKE $1 OR origin LIKE $2`,
		scheduleOrigin+"%", testOrigin+"%")
	if err != nil {
		return fmt.Errorf("list running scheduled tasks: %w", err)
	}
	going, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list running scheduled tasks: %w", err)
	}
	for o := range s.starting {
		if !slices.Contains(going, o) {
			going = append(going, o)
		}
	}
	if slices.Contains(going, origin) {
		return errRunning
	}
	if len(going) >= limit {
		return errCapacity
	}
	s.starting[origin] = true
	return nil
}

func (s *scheduler) release(origin string) {
	s.mu.Lock()
	delete(s.starting, origin)
	s.mu.Unlock()
}

// tick starts the due tasks. A task is claimed by moving its next_run_at before
// it starts, so a slot is never run twice. A slot missed while the server was down
// runs once now, not once per missed slot; a task still running from its last
// slot skips this one.
func (s *scheduler) tick(ctx context.Context, now time.Time) {
	rows, err := s.a.DB.Query(ctx, selectTask+` WHERE t.enabled AND t.next_run_at <= $1 ORDER BY t.next_run_at LIMIT 50`, now)
	if err != nil {
		slog.Error("schedule: list due tasks", "err", err)
		return
	}
	due, err := scanTasks(rows)
	if err != nil {
		slog.Error("schedule: read due tasks", "err", err)
		return
	}
	for _, t := range due {
		reserved := s.reserve(ctx, scheduleOrigin+t.ID)
		if errors.Is(reserved, errCapacity) {
			return // the rest stay due and go first on the next tick
		}
		if reserved != nil && !errors.Is(reserved, errRunning) {
			slog.Error("schedule: reserve", "task", t.ID, "err", reserved)
			return // stays due
		}
		claim, err := s.a.DB.Exec(ctx, `UPDATE scheduled_task SET next_run_at = $3 WHERE id = $1 AND next_run_at = $2`,
			t.ID, t.next, t.schedule(now))
		if err != nil || claim.RowsAffected() == 0 {
			if reserved == nil {
				s.release(scheduleOrigin + t.ID)
			}
			continue
		}
		if reserved != nil {
			continue // still running from the last slot: this one is skipped
		}
		if _, err := s.run(ctx, t, now); err != nil {
			slog.Warn("scheduled task not started", "task", t.ID, "err", err)
		}
	}
}

// ---- storage ------------------------------------------------------------------------

const selectTask = `SELECT t.id, t.user_id, u.name, u.email, t.title, t.prompt, t.model_id, t.tool_ids, t.features, t.files,
	t.frequency, t.at_minute, t.weekday, t.timezone, t.enabled, t.notes, t.next_run_at, t.last_run_at, t.last_chat_id,
	coalesce(CASE WHEN m.status = 'streaming' THEN 'running' ELSE m.status END, ''),
	coalesce(nullif(t.last_error, ''), CASE WHEN m.status = 'error' THEN m.error END, '')
	FROM scheduled_task t JOIN "user" u ON u.id = t.user_id
	LEFT JOIN chat c ON c.id = t.last_chat_id
	LEFT JOIN message m ON m.id = c.current_message_id AND m.chat_id = c.id`

func scanTasks(rows pgx.Rows) ([]*Task, error) {
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		t := &Task{}
		if err := rows.Scan(&t.ID, &t.UserID, &t.ownerRow.Name, &t.ownerRow.Email, &t.Title, &t.Prompt, &t.ModelID,
			&t.ToolIDs, &t.Features, &t.Files, &t.Frequency, &t.AtMinute, &t.Weekday, &t.Timezone, &t.Enabled, &t.Notes,
			&t.next, &t.lastRun, &t.LastChatID, &t.LastStatus, &t.LastError); err != nil {
			return nil, fmt.Errorf("scan scheduled task: %w", err)
		}
		t.ownerRow.ID = t.UserID
		t.NextRunAt = t.next.Unix()
		if t.lastRun != nil {
			n := t.lastRun.Unix()
			t.LastRunAt = &n
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// get returns a task owned by userID.
func (s *scheduler) get(ctx context.Context, id, userID string) (*Task, error) {
	rows, err := s.a.DB.Query(ctx, selectTask+` WHERE t.id = $1 AND t.user_id = $2`, id, userID)
	if err != nil {
		return nil, fmt.Errorf("get scheduled task: %w", err)
	}
	ts, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, errNotFound
	}
	return ts[0], nil
}

// ---- API ----------------------------------------------------------------------------

// form is a create or update body; absent fields keep the stored value.
type form struct {
	Title     *string         `json:"title"`
	Prompt    *string         `json:"prompt"`
	ModelID   *string         `json:"model_id"`
	ToolIDs   *[]string       `json:"tool_ids"`
	Features  json.RawMessage `json:"features"`
	Files     json.RawMessage `json:"files"`
	Frequency *string         `json:"frequency"`
	AtMinute  *int            `json:"at_minute"`
	Weekday   *int            `json:"weekday"`
	Timezone  *string         `json:"timezone"`
	Enabled   *bool           `json:"enabled"`
	Notes     *string         `json:"notes"`
}

func (f *form) apply(t *Task) {
	for dst, v := range map[*string]*string{&t.Title: f.Title, &t.Prompt: f.Prompt, &t.ModelID: f.ModelID,
		&t.Frequency: f.Frequency, &t.Timezone: f.Timezone} {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	for dst, v := range map[*int]*int{&t.AtMinute: f.AtMinute, &t.Weekday: f.Weekday} {
		if v != nil {
			*dst = *v
		}
	}
	if f.ToolIDs != nil {
		t.ToolIDs = *f.ToolIDs
	}
	if f.Features != nil {
		t.Features = f.Features
	}
	if f.Files != nil {
		t.Files = f.Files
	}
	if f.Enabled != nil {
		t.Enabled = *f.Enabled
	}
	if f.Notes != nil {
		t.Notes = cut(*f.Notes, maxNotes)
	}
}

// invalid is a field error for the form.
func invalid(field, msg string) error { return api.FieldErr(field, "invalid", msg) }

// checkGoal checks what the task does (instruction, assistant, switches, files) and cleans Features and
// Files into storable form. A scheduled task and a test run share it.
func checkGoal(t *Task) error {
	switch {
	case t.Prompt == "" || len(t.Prompt) > 50000:
		return invalid("prompt", "Write what the assistant should do (up to 50,000 characters)")
	case t.ModelID == "":
		return invalid("model_id", "Choose an assistant")
	}
	if len(t.Features) == 0 || string(t.Features) == "null" {
		t.Features = nil
	} else if err := json.Unmarshal(t.Features, &map[string]bool{}); err != nil {
		return invalid("features", "Features must be an object of true or false values")
	}
	if len(t.Files) == 0 || string(t.Files) == "null" {
		t.Files = json.RawMessage(`[]`)
	} else if err := json.Unmarshal(t.Files, &[]map[string]any{}); err != nil {
		return invalid("files", "Files must be a list")
	}
	return nil
}

// checkTiming checks the timetable fields. The preview shares it with saving.
func checkTiming(t *Task) error {
	switch {
	case !slices.Contains([]string{"hourly", "daily", "weekdays", "weekly"}, t.Frequency):
		return invalid("frequency", "Frequency must be hourly, daily, weekdays or weekly")
	case t.AtMinute < 0 || t.AtMinute > 1439 || (t.Frequency == "hourly" && t.AtMinute > 59):
		return invalid("at_minute", "That time of day is not valid")
	case t.Weekday < 0 || t.Weekday > 6:
		return invalid("weekday", "Weekday must be 0 (Sunday) to 6 (Saturday)")
	case t.Timezone == "" || t.Timezone == "Local":
		return invalid("timezone", "Time zone is required")
	}
	if _, err := time.LoadLocation(t.Timezone); err != nil {
		return invalid("timezone", "Unknown time zone")
	}
	return nil
}

// validate checks a task as it would be stored and keeps only the tool servers
// that work for the owner today (so a saved task never asks for an app that is
// not connected).
func (s *scheduler) validate(ctx context.Context, u *auth.User, t *Task) error {
	if t.Title == "" || len([]rune(t.Title)) > 120 {
		return invalid("title", "Give the task a name of up to 120 characters")
	}
	if err := checkGoal(t); err != nil {
		return err
	}
	if err := checkTiming(t); err != nil {
		return err
	}
	usable, err := s.e.UsableServers(ctx, u, t.ModelID, cleanIDs(t.ToolIDs))
	if err != nil {
		return err
	}
	t.ToolIDs = usable
	return nil
}

// cleanIDs turns the composer's tool ids ("server:<id>", bare ids, built-ins) into bare server ids.
func cleanIDs(in []string) []string {
	out := []string{}
	for _, id := range in {
		id = strings.TrimPrefix(id, "server:")
		if id != "" && !strings.HasPrefix(id, "builtin:") && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

const maxNotes = 4000

// cut truncates s to n runes.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *scheduler) list(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	all := r.URL.Query().Get("all") == "1"
	if all && !u.IsAdmin() {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "forbidden", "Admins only"))
		return
	}
	rows, err := s.a.DB.Query(ctx, selectTask+` WHERE ($1 OR t.user_id = $2) ORDER BY t.created_at, t.id`, all, u.ID)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list scheduled tasks: %w", err))
		return
	}
	ts, err := scanTasks(rows)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if all { // schedule and status only: never other people's goals, notes or files
		for _, t := range ts {
			o := t.ownerRow
			t.Owner, t.Prompt, t.Notes, t.Files = &o, "", "", nil
		}
	}
	api.JSON(w, http.StatusOK, ts)
}

func (s *scheduler) create(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	var f form
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	var n int
	if err := s.a.DB.QueryRow(ctx, `SELECT count(*) FROM scheduled_task WHERE user_id = $1`, u.ID).Scan(&n); err != nil {
		api.Fail(w, r, fmt.Errorf("count scheduled tasks: %w", err))
		return
	}
	if n >= s.setting(ctx, "cowork.schedule.max_per_user", 5) {
		api.Fail(w, r, api.Errf(http.StatusConflict, "limit", "You have reached the limit of scheduled tasks; delete one first"))
		return
	}
	t := &Task{ID: ids.New(), UserID: u.ID, Enabled: true}
	f.apply(t)
	t.Notes = "" // a new task has no notes yet
	if err := s.validate(ctx, u, t); err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := s.a.DB.Exec(ctx, `INSERT INTO scheduled_task (id, user_id, title, prompt, model_id, tool_ids, features, files,
		frequency, at_minute, weekday, timezone, enabled, next_run_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		t.ID, u.ID, t.Title, t.Prompt, t.ModelID, t.ToolIDs, nullJSON(t.Features), []byte(t.Files),
		t.Frequency, t.AtMinute, t.Weekday, t.Timezone, t.Enabled, t.schedule(time.Now())); err != nil {
		api.Fail(w, r, fmt.Errorf("insert scheduled task: %w", err))
		return
	}
	s.respond(w, r, t.ID, u.ID)
}

func (s *scheduler) update(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	t, err := s.get(ctx, r.PathValue("id"), u.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var f form
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	old := *t
	f.apply(t)
	if t.ModelID != old.ModelID && f.Notes == nil {
		t.Notes = "" // notes written under one assistant never go to another (it may be hosted)
	}
	if err := s.validate(ctx, u, t); err != nil {
		api.Fail(w, r, err)
		return
	}
	nextRun := t.next
	if t.Frequency != old.Frequency || t.AtMinute != old.AtMinute || t.Weekday != old.Weekday ||
		t.Timezone != old.Timezone || (t.Enabled && !old.Enabled) {
		nextRun = t.schedule(time.Now())
	}
	if _, err := s.a.DB.Exec(ctx, `UPDATE scheduled_task SET title = $3, prompt = $4, model_id = $5, tool_ids = $6, features = $7,
		files = $8, frequency = $9, at_minute = $10, weekday = $11, timezone = $12, enabled = $13, notes = $14, next_run_at = $15,
		updated_at = now() WHERE id = $1 AND user_id = $2`,
		t.ID, u.ID, t.Title, t.Prompt, t.ModelID, t.ToolIDs, nullJSON(t.Features), []byte(t.Files),
		t.Frequency, t.AtMinute, t.Weekday, t.Timezone, t.Enabled, t.Notes, nextRun); err != nil {
		api.Fail(w, r, fmt.Errorf("update scheduled task: %w", err))
		return
	}
	s.respond(w, r, t.ID, u.ID)
}

func (s *scheduler) respond(w http.ResponseWriter, r *http.Request, id, userID string) {
	t, err := s.get(r.Context(), id, userID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, t)
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func (s *scheduler) delete(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	tag, err := s.a.DB.Exec(r.Context(), `DELETE FROM scheduled_task WHERE id = $1 AND user_id = $2`, r.PathValue("id"), u.ID)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("delete scheduled task: %w", err))
		return
	}
	if tag.RowsAffected() == 0 {
		api.Fail(w, r, errNotFound)
		return
	}
	api.JSON(w, http.StatusOK, true)
}

// runNow starts the task now, outside its timetable (its next slot is unchanged).
func (s *scheduler) runNow(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	t, err := s.get(ctx, r.PathValue("id"), u.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := s.reserve(ctx, scheduleOrigin+t.ID); err != nil {
		api.Fail(w, r, err)
		return
	}
	chatID, err := s.run(ctx, t, time.Now())
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusConflict, "not_started", err.Error()))
		return
	}
	api.JSON(w, http.StatusOK, map[string]string{"chat_id": chatID})
}
