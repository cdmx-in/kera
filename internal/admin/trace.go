package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
)

// The run trace (written by internal/engine/trace.go): sizes and timings of
// each model round and tool call of a chat or task run. No text is stored.

type traceRun struct {
	RunID            string    `json:"run_id"`
	MessageID        string    `json:"message_id"`
	UserID           string    `json:"user_id"`
	UserName         string    `json:"user_name"`
	UserEmail        string    `json:"user_email"`
	ModelID          string    `json:"model_id"`
	StartedAt        time.Time `json:"started_at"`
	DurationMs       int64     `json:"duration_ms"`
	Rounds           int       `json:"rounds"`
	ToolCalls        int       `json:"tool_calls"`
	ToolErrors       int       `json:"tool_errors"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CachedTokens     int64     `json:"cached_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	Failed           bool      `json:"failed"`
}

// traceRuns lists the newest traced runs (?days=7&limit=100).
func (h *handler) traceRuns(w http.ResponseWriter, r *http.Request) {
	since, err := guardDays(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	limit := 100
	if n, _ := strconv.Atoi(r.URL.Query().Get("limit")); n > 0 && n <= 500 {
		limit = n
	}
	rows, err := h.a.DB.Query(r.Context(), `SELECT s.run_id, s.message_id, s.user_id, COALESCE(u.name, ''), COALESCE(u.email, ''), s.model_id,
		min(s.started_at), COALESCE(extract(epoch FROM max(s.started_at + s.duration_ms * interval '1 millisecond') - min(s.started_at)) * 1000, 0)::bigint,
		count(*) FILTER (WHERE s.kind = 'model')::int, count(*) FILTER (WHERE s.kind = 'tool')::int,
		count(*) FILTER (WHERE s.kind = 'tool' AND s.detail->>'is_error' = 'true')::int,
		COALESCE(sum(s.prompt_tokens), 0)::bigint, COALESCE(sum(s.cached_tokens), 0)::bigint, COALESCE(sum(s.completion_tokens), 0)::bigint,
		bool_or(s.error <> '')
		FROM run_step s LEFT JOIN "user" u ON u.id = s.user_id WHERE s.started_at >= $1
		GROUP BY s.run_id, s.message_id, s.user_id, u.name, u.email, s.model_id ORDER BY min(s.started_at) DESC LIMIT $2`, since, limit)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("trace runs: %w", err))
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (traceRun, error) {
		var t traceRun
		return t, row.Scan(&t.RunID, &t.MessageID, &t.UserID, &t.UserName, &t.UserEmail, &t.ModelID, &t.StartedAt, &t.DurationMs,
			&t.Rounds, &t.ToolCalls, &t.ToolErrors, &t.PromptTokens, &t.CachedTokens, &t.CompletionTokens, &t.Failed)
	})
	if err != nil {
		api.Fail(w, r, fmt.Errorf("trace runs: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, nonNil(out))
}

type traceStep struct {
	Seq              int             `json:"seq"`
	Round            int             `json:"round"`
	Kind             string          `json:"kind"`
	Name             string          `json:"name"`
	StartedAt        time.Time       `json:"started_at"`
	DurationMs       int             `json:"duration_ms"`
	PromptTokens     *int            `json:"prompt_tokens"`
	CachedTokens     *int            `json:"cached_tokens"`
	CompletionTokens *int            `json:"completion_tokens"`
	Finish           string          `json:"finish"`
	Error            string          `json:"error"`
	Detail           json.RawMessage `json:"detail"`
}

type traceDetail struct {
	RunID      string      `json:"run_id"`
	MessageID  string      `json:"message_id"`
	UserID     string      `json:"user_id"`
	UserName   string      `json:"user_name"`
	UserEmail  string      `json:"user_email"`
	ModelID    string      `json:"model_id"`
	StartedAt  time.Time   `json:"started_at"`
	DurationMs int64       `json:"duration_ms"`
	Steps      []traceStep `json:"steps"`
}

// traceSteps returns the steps of the newest run of a message (a regenerated
// answer keeps its message id; each attempt has its own run id).
func (h *handler) traceSteps(w http.ResponseWriter, r *http.Request) {
	msg := r.PathValue("message_id")
	var runID string
	err := h.a.DB.QueryRow(r.Context(), `SELECT run_id FROM run_step WHERE message_id = $1 ORDER BY started_at DESC, id DESC LIMIT 1`, msg).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "no trace for this message (it may be older than the retention, or from before tracing)"))
		return
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("trace steps: %w", err))
		return
	}
	rows, err := h.a.DB.Query(r.Context(), `SELECT seq, round, kind, name, started_at, duration_ms, prompt_tokens, cached_tokens, completion_tokens,
		finish, error, detail FROM run_step WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("trace steps: %w", err))
		return
	}
	steps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (traceStep, error) {
		var s traceStep
		return s, row.Scan(&s.Seq, &s.Round, &s.Kind, &s.Name, &s.StartedAt, &s.DurationMs, &s.PromptTokens, &s.CachedTokens, &s.CompletionTokens,
			&s.Finish, &s.Error, &s.Detail)
	})
	if err != nil {
		api.Fail(w, r, fmt.Errorf("trace steps: %w", err))
		return
	}
	out := traceDetail{Steps: nonNil(steps)}
	out.RunID, out.MessageID = runID, msg
	err = h.a.DB.QueryRow(r.Context(), `SELECT s.user_id, COALESCE(u.name, ''), COALESCE(u.email, ''), s.model_id FROM run_step s
		LEFT JOIN "user" u ON u.id = s.user_id WHERE s.run_id = $1 LIMIT 1`, runID).Scan(&out.UserID, &out.UserName, &out.UserEmail, &out.ModelID)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("trace steps: %w", err))
		return
	}
	out.StartedAt = steps[0].StartedAt
	for _, s := range steps {
		if end := s.StartedAt.Add(time.Duration(s.DurationMs) * time.Millisecond); end.Sub(out.StartedAt).Milliseconds() > out.DurationMs {
			out.DurationMs = end.Sub(out.StartedAt).Milliseconds()
		}
	}
	api.JSON(w, http.StatusOK, out)
}
