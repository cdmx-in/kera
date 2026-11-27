package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/engine"
)

// previewRuns is how many upcoming runs the preview lists.
const previewRuns = 3

// preview answers POST /api/v1/cowork/schedules/preview: the next runs of a timetable that is not saved yet,
// from the same maths the ticker uses, so the page never repeats the time zone and daylight-saving rules.
func (s *scheduler) preview(w http.ResponseWriter, r *http.Request) {
	var f form
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	t := &Task{}
	f.apply(t)
	if err := checkTiming(t); err != nil {
		api.Fail(w, r, err)
		return
	}
	runs := make([]int64, 0, previewRuns)
	for at := time.Now(); len(runs) < previewRuns; {
		at = t.schedule(at)
		runs = append(runs, at.Unix())
	}
	api.JSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// testRun answers POST /api/v1/cowork/test-run: it runs a task that is not saved, once, now, the way a
// scheduled run goes (nobody answers questions) but read-only: only tools their server declares read-only
// are offered (engine.Request.ReadOnly), so a trial cannot write to a connected app. The run is an ordinary
// cowork chat marked meta.test_run, titled "Test · <name>". It does not touch any scheduled task, its notes
// or its timetable. A person has one test at a time; tests count against cowork.schedule.max_concurrent.
func (s *scheduler) testRun(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	var f form
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	t := &Task{UserID: u.ID}
	f.apply(t)
	if err := checkGoal(t); err != nil {
		api.Fail(w, r, err)
		return
	}
	if t.Title == "" {
		t.Title = cut(strings.SplitN(t.Prompt, "\n", 2)[0], 60)
	}
	origin := testOrigin + u.ID // tests share the concurrency limit with scheduled runs
	if err := s.reserve(ctx, origin); err != nil {
		if errors.Is(err, errRunning) {
			err = api.Errf(http.StatusConflict, "running", "A test run is already going. Wait for it to finish or stop it first")
		}
		api.Fail(w, r, err)
		return
	}
	defer s.release(origin)
	chatID, err := s.startTrial(ctx, u, t)
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusConflict, "not_started", err.Error()))
		return
	}
	api.JSON(w, http.StatusOK, map[string]string{"chat_id": chatID})
}

func (s *scheduler) startTrial(ctx context.Context, u *auth.User, t *Task) (string, error) {
	req := &engine.Request{Model: t.ModelID, Unattended: true, ReadOnly: true, ToolIDs: cleanIDs(t.ToolIDs), Origin: testOrigin + u.ID}
	if len(t.Features) > 0 {
		if err := json.Unmarshal(t.Features, &req.Features); err != nil {
			return "", fmt.Errorf("the feature switches are unreadable: %w", err)
		}
	}
	c := &chats.Chat{UserID: u.ID, ModelIDs: []string{t.ModelID}, Mode: "cowork", Title: "Test · " + t.Title,
		Meta: map[string]any{"test_run": true}}
	if err := s.chats.Create(ctx, c); err != nil {
		return "", err
	}
	req.ChatID = c.ID
	req.UserMessage = &engine.UserMessage{Content: t.Prompt, Files: t.Files}
	if _, err := s.e.Start(ctx, u, req); err != nil {
		if e := s.chats.Delete(ctx, u.ID, c.ID); e != nil {
			slog.Warn("delete the chat of a test run that did not start", "chat", c.ID, "err", e)
		}
		return "", err
	}
	return c.ID, nil
}
