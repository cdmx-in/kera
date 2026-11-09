package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/guard"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/provider"
)

// retention is how long a finished run stays replayable.
const retention = 10 * time.Minute

// event is one entry of a run's log. Raw is the encoded data line (payload
// plus type, seq and messageId), frozen at emit time.
type event struct {
	Seq  int64
	Type string
	Data map[string]any // the payload; values are never mutated after emit
	Raw  []byte
}

// toolRecord is one server-side tool call, for the OpenAI-compatible kera.tool_calls.
type toolRecord struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"arguments"`
	Result string          `json:"result"`
	Error  bool            `json:"error,omitempty"`
}

// Run is one completion: its message state, its event log and its pending
// questions. It lives in memory, detached from the HTTP request that started it.
// ponytail: single-instance registry; a second instance would need PG LISTEN/NOTIFY.
type Run struct {
	ID        string
	ChatID    string // "temp:<run id>" when nothing is persisted
	MessageID string
	UserID    string
	ModelID   string
	Persisted bool
	Created   time.Time

	mu         sync.Mutex
	state      msgState
	events     []event
	wake       chan struct{}
	finished   bool
	finishedAt time.Time
	cancel     context.CancelCauseFunc
	waiters    map[string]chan json.RawMessage

	// for the OpenAI-compatible adapter
	roundText   string
	reasoning   string
	passthrough []provider.ToolCall
	tools       []toolRecord
	usage       Usage
	finish      string   // stop | tool_calls | length
	uploads     int      // files stored through the files callback
	userLinks   []string // URLs the user typed in this conversation (the links callback)

	checkpoint func()     // persists the state (set by the engine; may be nil)
	saveMu     sync.Mutex // orders saves: state read + write happen together
}

func newRun(chatID, messageID, userID, modelID string, persisted bool) *Run {
	r := &Run{ID: ids.New(), ChatID: chatID, MessageID: messageID, UserID: userID, ModelID: modelID,
		Persisted: persisted, Created: time.Now(), wake: make(chan struct{}), waiters: map[string]chan json.RawMessage{},
		state: msgState{Status: "streaming"}}
	if r.ChatID == "" {
		r.ChatID = "temp:" + r.ID
	}
	if r.MessageID == "" {
		r.MessageID = ids.New()
	}
	return r
}

// emitLocked appends an event; r.mu must be held.
func (r *Run) emitLocked(typ string, data map[string]any) {
	if r.finished {
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	seq := int64(len(r.events) + 1)
	payload := make(map[string]any, len(data)+4)
	for k, v := range data {
		payload[k] = v
	}
	payload["type"], payload["seq"], payload["messageId"] = typ, seq, r.MessageID
	if seq == 1 && !r.Persisted {
		payload["chatId"] = r.ChatID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw, _ = json.Marshal(map[string]any{"type": "error", "seq": seq, "messageId": r.MessageID,
			"code": "internal", "message": "event encoding failed"})
	}
	r.events = append(r.events, event{Seq: seq, Type: typ, Data: data, Raw: raw})
	if typ == "done" {
		r.finished, r.finishedAt = true, time.Now()
	}
	close(r.wake)
	r.wake = make(chan struct{})
}

func (r *Run) emit(typ string, data map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitLocked(typ, data)
}

// next returns the events after seq, a channel closed on the next append,
// and whether the run has finished.
func (r *Run) next(after int64) ([]event, <-chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if after < 0 {
		after = 0
	}
	var out []event
	if after < int64(len(r.events)) {
		out = slices.Clone(r.events[after:])
	}
	return out, r.wake, r.finished
}

// Seq is the number of events so far.
func (r *Run) Seq() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(len(r.events))
}

// Finished reports whether done was emitted.
func (r *Run) Finished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}

// Live returns the state for chat JSON overlays.
func (r *Run) Live() chats.Live {
	r.mu.Lock()
	defer r.mu.Unlock()
	return chats.Live{Parts: json.RawMessage(mustMarshal(r.state.Parts, "[]")), Files: json.RawMessage(mustMarshal(r.state.Files, "[]")),
		Sources: json.RawMessage(mustMarshal(r.state.Sources, "[]")), PendingCalls: json.RawMessage(mustMarshal(r.state.PendingCalls, "[]")),
		Status: r.state.Status, Seq: int64(len(r.events))}
}

// snapshotLocked is the full-state checkpoint payload.
func (r *Run) snapshotLocked() map[string]any {
	return map[string]any{
		"parts": json.RawMessage(mustMarshal(r.state.Parts, "[]")), "files": json.RawMessage(mustMarshal(r.state.Files, "[]")),
		"sources": json.RawMessage(mustMarshal(r.state.Sources, "[]")), "pendingCalls": json.RawMessage(mustMarshal(r.state.PendingCalls, "[]")),
		"status": r.state.Status,
	}
}

func (r *Run) snapshot() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitLocked("snapshot", r.snapshotLocked())
}

func mustMarshal(v any, empty string) []byte {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return []byte(empty)
	}
	return b
}

// ---- typed emitters (state + event) ------------------------------------------------

func (r *Run) delta(text string) {
	if text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.delta(text, time.Now())
	r.roundText += text
	r.emitLocked("delta", map[string]any{"text": text})
}

func (r *Run) reasoningDelta(text string) {
	if text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.reasoning(text, time.Now())
	r.reasoning += text
	r.emitLocked("reasoning", map[string]any{"text": text})
}

func (r *Run) reasoningDone() {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.state.last()
	if p == nil || p.Type != "reasoning" || p.Done {
		return
	}
	r.state.closeReasoning(time.Now())
	r.emitLocked("reasoning", map[string]any{"done": true, "durationMs": p.DurationMs})
}

func (r *Run) toolCall(callID, name, server string, args json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var a any = args
	if !json.Valid(args) {
		a = string(args)
	}
	r.state.toolCall(&Step{CallID: callID, Name: name, Server: server, Args: a, Status: "running", StatusLines: []StatusLine{}}, time.Now())
	r.emitLocked("tool_call", map[string]any{"callId": callID, "name": name, "server": server, "args": a})
}

func (r *Run) toolResult(callID, preview string, files []FileRef, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := "ok"
	if failed {
		status = "error"
	}
	if st := r.state.step(callID); st != nil {
		st.Status, st.Output = "done", preview
		if failed {
			st.Status = "error"
		}
		if len(files) > 0 {
			st.Files = slices.Clone(files)
		}
		for i := range st.StatusLines {
			st.StatusLines[i].Done = true
		}
	}
	data := map[string]any{"callId": callID, "status": status, "preview": preview}
	if len(files) > 0 {
		data["files"] = slices.Clone(files)
	}
	r.emitLocked("tool_result", data)
}

// StatusEvent is a progress update (also what tools and callbacks send).
type StatusEvent struct {
	ID          string   `json:"id,omitempty"`
	Action      string   `json:"action,omitempty"`
	Description string   `json:"description"`
	Done        bool     `json:"done"`
	Hidden      bool     `json:"hidden,omitempty"`
	Query       string   `json:"query,omitempty"`
	URLs        []string `json:"urls,omitempty"`
	Count       int      `json:"count,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Server      string   `json:"server,omitempty"`
	CallID      string   `json:"callId,omitempty"`
}

func (r *Run) status(s StatusEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.status(s.CallID, StatusLine{ID: s.ID, Action: s.Action, Description: s.Description, Done: s.Done}, s.URLs, s.Hidden)
	data := map[string]any{"description": s.Description, "done": s.Done}
	for k, v := range map[string]string{"id": s.ID, "action": s.Action, "query": s.Query, "tool": s.Tool, "server": s.Server, "callId": s.CallID} {
		if v != "" {
			data[k] = v
		}
	}
	if s.Hidden {
		data["hidden"] = true
	}
	if len(s.URLs) > 0 {
		data["urls"] = slices.Clone(s.URLs)
	}
	if s.Count > 0 {
		data["count"] = s.Count
	}
	r.emitLocked("status", data)
}

func (r *Run) sources(src []Source) {
	if len(src) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.addSources(src)
	r.emitLocked("sources", map[string]any{"sources": slices.Clone(src)})
}

func (r *Run) files(fs []FileRef) {
	if len(fs) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.addFiles(fs)
	r.emitLocked("files", map[string]any{"files": slices.Clone(fs)})
}

func (r *Run) block(b json.RawMessage) {
	if !json.Valid(b) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.closeReasoning(time.Now())
	r.state.Parts = append(r.state.Parts, &Part{Type: "block", Block: slices.Clone(b)})
	r.emitLocked("block", map[string]any{"block": json.RawMessage(slices.Clone(b))})
}

// guardNotice tells the user what the guardrails redacted (log-only and
// blocked events need no note: nothing changed, or the error says why).
func (r *Run) guardNotice(evs []guard.Event) {
	var in, out []guard.Event
	for _, e := range evs {
		if e.Action != string(guard.Redact) {
			continue
		}
		if strings.HasPrefix(e.Rule, "output.") {
			out = append(out, e)
		} else {
			in = append(in, e)
		}
	}
	for _, n := range []struct {
		evs  []guard.Event
		text string
	}{{in, "Removed %s before sending this to the model."}, {out, "Masked %s in the reply."}} {
		if d := guard.Describe(n.evs); d != "" {
			b, _ := json.Marshal(map[string]any{"type": "notice", "kind": "guardrail", "text": fmt.Sprintf(n.text, d)})
			r.block(b)
		}
	}
}

func (r *Run) compaction(state, summary string, before, after int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.compaction(state, summary, before, after)
	data := map[string]any{"state": state}
	if summary != "" {
		data["summary"] = summary
	}
	if before > 0 {
		data["before"], data["after"] = before, after
	}
	r.emitLocked("compaction", data)
}

func (r *Run) title(t string) { r.emit("title", map[string]any{"title": t}) }

func (r *Run) followUps(list []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.FollowUps = slices.Clone(list)
	r.emitLocked("follow_ups", map[string]any{"follow_ups": slices.Clone(list)})
}

func (r *Run) addUsage(u provider.Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage.PromptTokens += u.PromptTokens
	r.usage.CompletionTokens += u.CompletionTokens
}

func (r *Run) emitUsage() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.usage.PromptTokens == 0 && r.usage.CompletionTokens == 0 {
		return
	}
	u := r.usage
	r.state.Usage = &u
	r.emitLocked("usage", map[string]any{"usage": u})
}

// fail records an error; done follows separately.
func (r *Run) fail(code, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Status == "streaming" {
		r.state.Status, r.state.Error = "error", msg
	}
	r.emitLocked("error", map[string]any{"code": code, "message": msg})
}

// done closes the log. status is done or stopped.
func (r *Run) done(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.finish(status, time.Now())
	for id, ch := range r.waiters {
		close(ch)
		delete(r.waiters, id)
	}
	r.state.PendingCalls = nil
	data := map[string]any{}
	if r.state.Status == "stopped" {
		data["status"] = "stopped"
	}
	r.emitLocked("done", data)
}

// ---- questions ----------------------------------------------------------------------

// ErrNoAnswer means the question was withdrawn (timeout, cancel, run end).
var ErrNoAnswer = errors.New("no answer")

// ask shows a question card and waits for its answer.
func (r *Run) ask(ctx context.Context, pc PendingCall, timeout time.Duration) (json.RawMessage, error) {
	if pc.CallID == "" {
		pc.CallID = "ask_" + ids.New()
	}
	ch := make(chan json.RawMessage, 1)
	r.mu.Lock()
	if r.finished {
		r.mu.Unlock()
		return nil, ErrNoAnswer
	}
	r.waiters[pc.CallID] = ch
	r.state.PendingCalls = append(r.state.PendingCalls, pc)
	cp := r.checkpoint
	r.mu.Unlock()
	// persist before the card goes out, so a reload right after it shows the card
	if cp != nil {
		cp()
	}
	r.mu.Lock()
	data := map[string]any{"callId": pc.CallID}
	if pc.Kind == "confirm" {
		data["title"], data["message"] = pc.Title, pc.Message
		if pc.ConfirmLabel != "" {
			data["confirmLabel"] = pc.ConfirmLabel
		}
		if pc.CancelLabel != "" {
			data["cancelLabel"] = pc.CancelLabel
		}
	} else {
		data["questions"] = pc.Questions
	}
	r.emitLocked(pc.Kind, data)
	r.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v, ok := <-ch:
		if !ok {
			return nil, ErrNoAnswer
		}
		return v, nil
	case <-ctx.Done():
	case <-timer.C:
	}
	r.withdraw(pc.CallID)
	return nil, fmt.Errorf("%w: %v", ErrNoAnswer, context.Cause(ctx))
}

// withdraw removes an unanswered question and persists that, so the stored
// pending_calls (read by the Cowork list) clear while the run goes on.
func (r *Run) withdraw(callID string) {
	r.mu.Lock()
	if _, ok := r.waiters[callID]; !ok {
		r.mu.Unlock()
		return
	}
	delete(r.waiters, callID)
	r.removePendingLocked(callID)
	r.emitLocked("snapshot", r.snapshotLocked())
	cp := r.checkpoint
	r.mu.Unlock()
	if cp != nil {
		cp()
	}
}

func (r *Run) removePendingLocked(callID string) {
	r.state.PendingCalls = slices.DeleteFunc(r.state.PendingCalls, func(p PendingCall) bool { return p.CallID == callID })
}

// ErrNoSuchCall means the call id is not waiting for an answer.
var ErrNoSuchCall = errors.New("no such question")

// answer delivers a value to a waiting question.
func (r *Run) answer(callID string, value json.RawMessage) error {
	r.mu.Lock()
	ch, ok := r.waiters[callID]
	if !ok {
		r.mu.Unlock()
		return ErrNoSuchCall
	}
	delete(r.waiters, callID)
	r.removePendingLocked(callID)
	r.emitLocked("snapshot", r.snapshotLocked())
	cp := r.checkpoint
	r.mu.Unlock()
	ch <- value
	if cp != nil {
		cp()
	}
	return nil
}

// ---- registry ------------------------------------------------------------------------

type registry struct {
	mu    sync.Mutex
	byMsg map[string]*Run
	byID  map[string]*Run
}

func newRegistry() *registry {
	return &registry{byMsg: map[string]*Run{}, byID: map[string]*Run{}}
}

func (g *registry) add(r *Run) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()
	if old := g.byMsg[r.MessageID]; old != nil && !old.Finished() {
		return errBusy
	}
	g.byMsg[r.MessageID] = r
	g.byID[r.ID] = r
	return nil
}

func (g *registry) byMessage(id string) *Run {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.byMsg[id]
}

func (g *registry) run(id string) *Run {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.byID[id]
}

// forChat lists the runs of a chat (for reattaching).
func (g *registry) forChat(chatID string) []*Run {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*Run
	for _, r := range g.byID {
		if r.ChatID == chatID {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b *Run) int { return a.Created.Compare(b.Created) })
	return out
}

func (g *registry) sweepLocked() {
	now := time.Now()
	for id, r := range g.byID {
		r.mu.Lock()
		old := r.finished && now.Sub(r.finishedAt) > retention
		r.mu.Unlock()
		if old {
			delete(g.byID, id)
			if g.byMsg[r.MessageID] == r {
				delete(g.byMsg, r.MessageID)
			}
		}
	}
}

func (g *registry) sweep() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()
}
