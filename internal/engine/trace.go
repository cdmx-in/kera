package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/provider"
)

// The run trace records what each model round, tool call, retrieval and
// compaction of a run cost: sizes, counts, timings and flags. It never holds
// prompt text, tool arguments or tool results (not even a preview), and an
// error is a short code, so there is nothing to redact. Steps are collected in
// memory and written once, when the run finishes; a failure to write is logged
// and never fails the run.

// Step kinds (match the run_step.kind CHECK constraint).
const (
	stepModel      = "model"
	stepTool       = "tool"
	stepRetrieval  = "retrieval"
	stepCompaction = "compaction"
)

// maxTraceSteps bounds the memory and rows of one run.
// ponytail: later steps of a very long run are counted but not kept; raise it or sample if that matters.
const maxTraceSteps = 2000

// traceStep is one run_step row.
type traceStep struct {
	Kind, Name  string
	Round       int
	Started     time.Time
	Duration    time.Duration
	Prompt      *int
	Cached      *int
	Completion  *int
	Finish, Err string
	Detail      map[string]any
}

// runTrace is a run's collected steps.
type runTrace struct {
	mu        sync.Mutex
	on        bool
	steps     []traceStep
	dropped   int
	toolChars int // size of the tool definitions offered, measured once
}

func (t *runTrace) enable(on bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.on = on
}

func (t *runTrace) add(s traceStep) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.on {
		return
	}
	if len(t.steps) >= maxTraceSteps {
		t.dropped++
		return
	}
	t.steps = append(t.steps, s)
}

func (t *runTrace) take() ([]traceStep, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, d := t.steps, t.dropped
	t.steps, t.dropped = nil, 0
	return s, d
}

func intp(n int) *int { return &n }

// modelTrace says what is special about a model call: which try it was, whether it will be repeated, and
// whether it is the closing report at the round cap.
type modelTrace struct {
	attempt int
	retry   bool
	final   bool
}

// traceModel records one model call (one try of it). a.use is what the server reported (nil when it reported nothing).
func (x *exec) traceModel(round int, req provider.Request, started time.Time, a attempt, mt modelTrace) {
	use, textChars, calls, finish, err := a.use, a.textChars, len(a.calls), a.finish, a.err
	t := &x.run.trace
	t.mu.Lock()
	if t.toolChars == 0 && len(req.Tools) > 0 {
		b, _ := json.Marshal(req.Tools)
		t.toolChars = len(b)
	}
	toolChars := t.toolChars
	t.mu.Unlock()
	promptChars := 0
	for _, m := range req.Messages {
		promptChars += utf8.RuneCountInString(m.Content)
		for _, c := range m.ToolCalls {
			promptChars += len(c.Arguments)
		}
	}
	s := traceStep{Kind: stepModel, Name: x.m.ProviderModel, Round: round, Started: started, Duration: time.Since(started), Finish: finish,
		Detail: map[string]any{"messages": len(req.Messages), "prompt_chars": promptChars, "tools_offered": len(req.Tools),
			"tool_def_chars": toolChars, "text_chars": textChars, "tool_calls": calls, "truncated": finish == "length"}}
	if use != nil {
		s.Prompt, s.Completion, s.Cached = intp(use.PromptTokens), intp(use.CompletionTokens), use.CachedTokens
	}
	if mt.attempt > 1 {
		s.Detail["attempt"] = mt.attempt
	}
	if mt.retry {
		s.Detail["will_retry"] = true
	}
	if mt.final { // the stop reason of the run is the cap, not what the model said about this call
		s.Detail["final_report"], s.Detail["provider_finish"] = true, finish
		s.Finish = "round_cap"
	}
	if err != nil {
		s.Err = traceError(err, s.Detail)
	}
	t.add(s)
}

// traceError is the short code of err; an HTTP status goes to detail. Never the message: it can echo a request.
func traceError(err error, detail map[string]any) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var he *provider.HTTPError
	if errors.As(err, &he) {
		detail["http_status"] = he.Status
	}
	code, _ := classify(err)
	return code
}

// saveTrace writes the run's steps in one batch (one implicit transaction).
func (e *Engine) saveTrace(x *exec) {
	defer func() {
		if v := recover(); v != nil {
			slog.Warn("run trace panic", "run", x.run.ID, "value", v)
		}
	}()
	steps, dropped := x.run.trace.take()
	if len(steps) == 0 {
		return
	}
	if last := &steps[len(steps)-1]; dropped > 0 {
		if last.Detail == nil {
			last.Detail = map[string]any{}
		}
		last.Detail["steps_dropped_after"] = dropped // ponytail: noted on the last kept step
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := &pgx.Batch{}
	base := (x.run.attempt - 1) * maxTraceSteps // after the steps of the attempts before a restart
	for i, s := range steps {
		detail, err := json.Marshal(s.Detail)
		if err != nil || s.Detail == nil {
			detail = []byte("{}")
		}
		b.Queue(`INSERT INTO run_step (run_id, message_id, user_id, model_id, seq, round, kind, name, started_at, duration_ms,
			prompt_tokens, cached_tokens, completion_tokens, finish, error, detail)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			x.run.ID, x.run.MessageID, x.u.ID, x.m.ID, base+i, s.Round, s.Kind, s.Name, s.Started, s.Duration.Milliseconds(),
			s.Prompt, s.Cached, s.Completion, s.Finish, s.Err, detail)
	}
	res := e.DB.SendBatch(ctx, b)
	if err := res.Close(); err != nil {
		slog.Warn("write run trace", "run", x.run.ID, "err", err)
	}
}

// purgeTrace deletes trace rows older than trace.retention_days (at least one day).
func (e *Engine) purgeTrace(ctx context.Context) (int64, error) {
	days := 30
	e.setting(ctx, "trace.retention_days", &days)
	tag, err := e.DB.Exec(ctx, `DELETE FROM run_step WHERE started_at < now() - make_interval(days => $1)`, max(days, 1))
	return tag.RowsAffected(), err
}
