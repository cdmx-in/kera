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
// Runs of stored chats also have a row (durable.go), which is what other instances and a restart see.
// ponytail: events stay in this process, so a browser on another instance gets snapshots instead of a token
// stream; LISTEN/NOTIFY and a chunk table if that matters.
type Run struct {
	ID        string
	ChatID    string // "temp:<run id>" when nothing is persisted
	MessageID string
	UserID    string
	ModelID   string
	Persisted bool
	Created   time.Time

	unattended bool // nobody can answer: ask fails at once
	// base is added to every event's sequence number, so the events of a resumed run come after those of the attempt
	// before it (set before the run is shared, never changed).
	base int64
	// attempt numbers the attempts of a run with a row; it fences their writes and callback tokens (1 for a new run).
	attempt int
	leased  bool  // the run has a row whose lease the heartbeat renews (under mu)
	resumes bool  // a restart resumes the run, so a shutdown lets it drain (under mu)
	gone    error // why the run left this instance without ending (under mu)

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
	trace       runTrace // per-step sizes and timings, written once at finish (trace.go)
	usage       Usage
	finish      string   // stop | tool_calls | length
	uploads     int      // files stored through the files callback
	userLinks   []string // URLs the user typed in this conversation (the links callback)

	persist func()     // writes the state to the message (set by the engine; may be nil)
	saveMu  sync.Mutex // orders saves: state read + write happen together
}

func newRun(chatID, messageID, userID, modelID string, persisted bool) *Run {
	r := &Run{ID: ids.New(), ChatID: chatID, MessageID: messageID, UserID: userID, ModelID: modelID,
		Persisted: persisted, Created: time.Now(), attempt: 1, wake: make(chan struct{}), waiters: map[string]chan json.RawMessage{},
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
	seq := r.base + int64(len(r.events)+1)
	payload := make(map[string]any, len(data)+4)
	for k, v := range data {
		payload[k] = v
	}
	payload["type"], payload["seq"], payload["messageId"] = typ, seq, r.MessageID
	if len(r.events) == 0 && !r.Persisted {
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
// and whether the run has finished. A seq from before this attempt gets them all.
func (r *Run) next(after int64) ([]event, <-chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	after = max(after-r.base, 0)
	var out []event
	if after < int64(len(r.events)) {
		out = slices.Clone(r.events[after:])
	}
	return out, r.wake, r.finished
}

// Seq is the sequence number of the newest event.
func (r *Run) Seq() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seqLocked()
}

func (r *Run) seqLocked() int64 { return r.base + int64(len(r.events)) }

// setCancel stores the function that stops the run.
func (r *Run) setCancel(f context.CancelCauseFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel = f
}

// stop cancels the run for the given reason.
func (r *Run) stop(why error) {
	r.mu.Lock()
	f := r.cancel
	r.mu.Unlock()
	if f != nil {
		f(why)
	}
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
		Status: r.state.Status, Seq: r.seqLocked()}
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

// settle gives the state its final status (done or stopped) and withdraws open questions.
func (r *Run) settle(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.finish(status, time.Now())
	r.closeWaitersLocked()
	r.state.PendingCalls = nil
}

// restart empties the message for an answer that starts again, and tells the streams.
func (r *Run) restart() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = msgState{Status: "streaming"}
	r.emitLocked("snapshot", r.snapshotLocked())
}

// close ends the event log with done.
func (r *Run) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	data := map[string]any{}
	if r.state.Status == "stopped" {
		data["status"] = "stopped"
	}
	r.emitLocked("done", data)
}

// done is settle and close, for a run that is not persisted.
func (r *Run) done(status string) {
	r.settle(status)
	r.close()
}

func (r *Run) closeWaitersLocked() {
	for id, ch := range r.waiters {
		close(ch)
		delete(r.waiters, id)
	}
}

// detach ends the log without done: the run goes on in another attempt, so a stream that closes here must be
// reopened, not taken as finished.
func (r *Run) detach(why error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.finished, r.finishedAt, r.gone = true, time.Now(), why
	r.closeWaitersLocked()
	close(r.wake)
	r.wake = make(chan struct{})
}

// detached is why the run left this instance without ending, or nil.
func (r *Run) detached() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gone
}

func (r *Run) setResumable(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resumes = v
}

func (r *Run) resumable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resumes
}

func (r *Run) setLeased() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leased = true
}

// ---- questions ----------------------------------------------------------------------

// ErrNoAnswer means the question was withdrawn (timeout, cancel, run end).
var ErrNoAnswer = errors.New("no answer")

// ask shows a question card and waits for its answer.
func (r *Run) ask(ctx context.Context, pc PendingCall, timeout time.Duration) (json.RawMessage, error) {
	if r.unattended { // before anything is registered, so no question is left pending
		return nil, fmt.Errorf("%w: nobody is watching this run", ErrNoAnswer)
	}
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
	persist := r.persist
	r.mu.Unlock()
	// persist before the card goes out, so a reload right after it shows the card
	if persist != nil {
		persist()
	}
	r.card(pc)
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

// card sends the event that shows a question.
func (r *Run) card(pc PendingCall) {
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
	r.emit(pc.Kind, data)
}

// addPending adds the question to the open ones, once.
func (r *Run) addPending(pc PendingCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removePendingLocked(pc.CallID)
	r.state.PendingCalls = append(r.state.PendingCalls, pc)
}

// dropPending closes the question's card.
func (r *Run) dropPending(callID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removePendingLocked(callID)
	r.emitLocked("snapshot", r.snapshotLocked())
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
	persist := r.persist
	r.mu.Unlock()
	if persist != nil {
		persist()
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
	persist := r.persist
	r.mu.Unlock()
	ch <- value
	if persist != nil {
		persist()
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

// replace registers r over any run of the same message and returns the unfinished one it displaced.
func (g *registry) replace(r *Run) *Run {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()
	old := g.byMsg[r.MessageID]
	g.byMsg[r.MessageID] = r
	g.byID[r.ID] = r
	if old != nil && !old.Finished() {
		return old
	}
	return nil
}

// remove forgets r (not an attempt that has replaced it).
func (g *registry) remove(r *Run) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byID[r.ID] == r {
		delete(g.byID, r.ID)
	}
	if g.byMsg[r.MessageID] == r {
		delete(g.byMsg, r.MessageID)
	}
}

// leased lists, by id, the unfinished runs whose row this instance renews.
func (g *registry) leased() map[string]*Run {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]*Run{}
	for id, r := range g.byID {
		r.mu.Lock()
		if r.leased && !r.finished {
			out[id] = r
		}
		r.mu.Unlock()
	}
	return out
}

// live lists the runs that have not finished.
func (g *registry) live() []*Run {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*Run
	for _, r := range g.byID {
		if !r.Finished() {
			out = append(out, r)
		}
	}
	return out
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
