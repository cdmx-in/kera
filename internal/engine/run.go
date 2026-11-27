package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/guard"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/retrieval"
	"github.com/cdmx-in/kera/internal/sandbox"
)

// unattendedUnit is the unit of cowork.schedule.max_minutes (tests shorten it).
var unattendedUnit = time.Minute

// exec is the working state of one run.
type exec struct {
	u           *auth.User
	m           *models.Resolved
	req         *Request
	run         *Run
	chat        *chats.Chat // nil when nothing is persisted
	parentID    string      // the message the answer replies to (persisted chats)
	interactive bool        // native event stream (a person is watching)
	noChat      bool        // API call without a chat
	profile     userProfile
	started     time.Time
	limit       *time.Timer // unattended runs: stops the run when the time limit passes
	durable     bool        // a restart resumes this run: it checkpoints its state (durable.go)
	resumed     bool        // this attempt continues an earlier one
	ck          *checkpoint // where a resumed run goes on from; nil starts the conversation from the beginning
	missing     []string    // requested tool servers that did not join (assembleTools)
	withheld    []string    // read-only runs: functions left out because their server does not declare them read-only

	turns       []turn
	fileIDs     []string // readable files attached anywhere in the conversation
	collections []string // knowledge collections attached to the request
	system      []string // system prompt sections, in order
	data        string   // retrieved passages as a reference message before the newest user turn (rag.placement = data); never stored
	features    map[string]bool
	cites       cites // citation numbers of the run (cite.go)

	providerTools  []json.RawMessage
	disableServers bool
	disabledNames  map[string]bool
	params         map[string]any
}

// cowork reports a task run: the chat's mode decides, so follow-ups cannot forget it.
func (x *exec) cowork() bool { return x.chat != nil && x.chat.Mode == "cowork" }

// turn is one conversation message with the files attached to it.
type turn struct {
	ID      string
	msg     provider.Message
	files   []string
	summary string // compaction summary ending at this message
}

// Start resolves the model, records the user turn and an assistant
// placeholder (persisted chats), registers the run and starts it.
func (e *Engine) Start(ctx context.Context, u *auth.User, req *Request) (*Run, error) {
	m, err := e.Models.Resolve(ctx, u, req.Model)
	if err != nil {
		return nil, err
	}
	if m.Kind != "" {
		return nil, models.ErrNotChat
	}
	x := &exec{u: u, m: m, req: req, interactive: req.StreamFormat == "events", started: time.Now(),
		disabledNames: map[string]bool{}}
	isTemp := req.Temporary || strings.HasPrefix(req.ChatID, "temp:")
	if isTemp && !u.Can("features.temporary_chat") {
		return nil, errTempForbidden
	}
	if req.ChatID != "" && !isTemp {
		c, err := e.Chats.Get(ctx, u.ID, req.ChatID)
		if err != nil {
			return nil, err
		}
		x.chat = &c
	}
	if x.cowork() && !u.Can("features.cowork") {
		return nil, errCoworkForbidden
	}
	x.noChat = req.ChatID == "" && !req.Temporary
	// client-executed tools hand the loop to the caller, and a run without a stored chat has nowhere to go on in
	x.durable = x.chat != nil && len(req.Tools) == 0 && e.Settings.Bool(ctx, "runs.resume")
	if x.chat != nil && x.chat.Locality != nil && *x.chat.Locality == "local" && !m.Local && e.Settings.Bool(ctx, "chat.local_lock") {
		return nil, errLocalLock
	}
	msgID := req.ID
	if msgID == "" {
		msgID = ids.New()
	}
	chatID := ""
	switch {
	case x.chat != nil:
		chatID = x.chat.ID
	case strings.HasPrefix(req.ChatID, "temp:"):
		chatID = req.ChatID // later turns of a temporary chat keep its id (answers and cancel use it)
	}
	run := newRun(chatID, msgID, u.ID, m.ID, x.chat != nil)
	run.unattended = req.Unattended
	run.resumes = x.durable
	x.run = run
	if err := e.runs.add(run); err != nil {
		return nil, err
	}
	// the cancel func is in place before the row, which the heartbeat acts on
	runCtx, cancel := e.runContext(context.WithoutCancel(ctx), x)
	if x.chat != nil {
		if err := e.prepareMessages(ctx, x); err != nil {
			cancel(err)
			run.done("done")
			return nil, err
		}
		if x.durable {
			// the stored message changes only together with the checkpoint, apart from the open questions
			run.persist = func() { e.savePending(x) }
		} else {
			run.persist = func() { e.save(x) }
		}
	}
	if req.Unattended {
		x.timeLimit(e.unattendedLimit(ctx), cancel)
	}
	e.wg.Add(1)
	go e.execute(runCtx, x)
	return run, nil
}

// runContext is the context a run works under: it outlives the request, carries what tools, retrieval and the
// guardrails read from it, and is cancelled with the reason the run stops for.
func (e *Engine) runContext(parent context.Context, x *exec) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	if x.m != nil && x.m.Local {
		// knowledge (and tools that honour it) keep a local run's documents
		// away from external services such as a hosted reranker
		ctx = retrieval.WithLocalRun(ctx)
	}
	ctx = guard.WithNotifier(ctx, x.run.guardNotice)
	ctx = auth.WithUser(ctx, x.u) // the guardrail audit names the user, also after the request that started the run is gone
	if x.req.Origin != "" {
		ctx = WithOrigin(ctx, x.req.Origin)
	}
	x.run.setCancel(cancel)
	return ctx, cancel
}

// unattendedLimit is how long a run nobody watches may take.
func (e *Engine) unattendedLimit(ctx context.Context) time.Duration {
	minutes := 120
	e.setting(ctx, "cowork.schedule.max_minutes", &minutes)
	return time.Duration(max(minutes, 1)) * unattendedUnit
}

// timeLimit stops the run after d. The run context drops the caller's deadline and tool calls have no client
// timeout, so one hung tool would hold a scheduler seat forever.
func (x *exec) timeLimit(d time.Duration, cancel context.CancelCauseFunc) {
	x.limit = time.AfterFunc(d, func() { cancel(errTimeLimit) })
}

// prepareMessages stores the user turn and the assistant placeholder.
func (e *Engine) prepareMessages(ctx context.Context, x *exec) error {
	c, req, msgID := x.chat, x.req, x.run.MessageID
	// before anything is written: a run on another instance owns the message just as one here does
	if busy, err := e.busy(ctx, msgID); err != nil {
		return err
	} else if busy {
		return errBusy
	}
	switch {
	case req.UserMessage != nil:
		um := req.UserMessage
		if um.ID == "" {
			um.ID = ids.New()
		}
		var parent *string
		if um.ParentID != nil && *um.ParentID != "" {
			parent = um.ParentID
		}
		if err := e.Chats.InsertMessage(ctx, &chats.Message{ID: um.ID, ChatID: c.ID, ParentID: parent, Role: "user",
			Content: um.Content, Files: um.Files}); err != nil {
			return err
		}
		x.parentID = um.ID
		// A chat link grants read access (files.CanRead), so link only files
		// the user can already read; anything else is someone else's file id.
		var readable []string
		for _, id := range fileIDsOf(um.Files) {
			if ok, err := e.Files.CanRead(ctx, x.u, id); err == nil && ok {
				readable = append(readable, id)
			}
		}
		if err := e.Chats.LinkFiles(ctx, c.ID, um.ID, readable); err != nil {
			return err
		}
	case req.ParentID != "":
		if _, err := e.Chats.Message(ctx, c.ID, req.ParentID); err != nil {
			return err
		}
		x.parentID = req.ParentID
	default:
		if old, err := e.Chats.Message(ctx, c.ID, msgID); err == nil && old.Role == "assistant" {
			if old.ParentID != nil {
				x.parentID = *old.ParentID
			}
			break
		}
		// an API caller continuing a chat: its last user message becomes the new turn
		um := chats.Message{ID: ids.New(), ChatID: c.ID, ParentID: c.CurrentMessageID, Role: "user"}
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				um.Content, _ = contentOf(req.Messages[i].Content)
				break
			}
		}
		if err := e.Chats.InsertMessage(ctx, &um); err != nil {
			return err
		}
		x.parentID = um.ID
	}
	var parent *string
	if x.parentID != "" {
		parent = &x.parentID
	}
	if err := e.Chats.InsertMessage(ctx, &chats.Message{ID: msgID, ChatID: c.ID, ParentID: parent, Role: "assistant",
		ModelID: x.m.ID, Status: "streaming"}); err != nil {
		return err
	}
	// the row first: it decides who may write the message, and a message marked as being written without one is an
	// orphan to the janitor
	if err := e.insertRun(ctx, x); err != nil {
		return err
	}
	if err := e.startMessage(ctx, x); err != nil {
		e.dropRun(x)
		return err
	}
	return nil
}

// startMessage marks the assistant message as being written and makes it the chat's current one.
func (e *Engine) startMessage(ctx context.Context, x *exec) error {
	c, msgID := x.chat, x.run.MessageID
	if _, err := e.DB.Exec(ctx, `UPDATE message SET status = 'streaming', model_id = $2, error = '', updated_at = now()
		WHERE id = $1`, msgID, x.m.ID); err != nil {
		return fmt.Errorf("reset message: %w", err)
	}
	if err := e.Chats.SetCurrent(ctx, c.ID, msgID); err != nil {
		return err
	}
	return e.Chats.AddModel(ctx, c.ID, x.m.ID)
}

// execute is the run's goroutine.
func (e *Engine) execute(ctx context.Context, x *exec) {
	defer e.wg.Done()
	if x.limit != nil {
		defer x.limit.Stop()
	}
	defer func() {
		if v := recover(); v != nil {
			slog.Error("run panic", "run", x.run.ID, "value", v)
			x.run.fail("internal", "internal error")
			e.end(x, "done", false)
		}
	}()
	err := e.process(ctx, x)
	status := "done"
	var later *tryLater
	switch {
	case err == nil:
	case errors.Is(err, errLostLease) || errors.Is(context.Cause(ctx), errLostLease):
		e.abandon(x)
		return
	case errors.As(err, &later) && ctx.Err() == nil && e.deferRun(x):
		return
	case errors.Is(err, errCancelled) || errors.Is(context.Cause(ctx), errCancelled):
		status = "stopped"
	case x.durable && (errors.Is(err, errShutdown) || errors.Is(context.Cause(ctx), errShutdown)):
		e.releaseRun(x)
		return
	case errors.Is(context.Cause(ctx), errShutdown):
		x.run.fail("interrupted", interruptedMsg)
	case errors.Is(context.Cause(ctx), errTimeLimit):
		x.run.fail("timeout", timeLimitMsg)
	default:
		code, msg := classify(err)
		slog.Warn("run failed", "run", x.run.ID, "model", x.m.ID, "code", code, "err", err)
		x.run.fail(code, msg)
	}
	e.finish(x, status, err == nil)
}

// process runs the pipeline up to the final answer. A run that goes on from a checkpoint keeps the conversation the
// checkpoint holds: the inlet hooks, the context and the compaction are not run again.
func (e *Engine) process(ctx context.Context, x *exec) error {
	x.run.trace.enable(e.Settings.Bool(ctx, "trace.enable"))
	x.profile = e.profile(ctx, x.u)
	ck := x.ck
	if ck == nil {
		x.features = e.features(ctx, x)
		x.params = map[string]any{}
		for k, v := range x.m.Params {
			x.params[k] = v
		}
		for k, v := range x.req.Params {
			x.params[k] = v
		}
	}
	// also for a checkpoint: the outlet hooks, the title and the user's links read the turns
	if err := e.loadTurns(ctx, x); err != nil {
		return err
	}
	x.run.setUserLinks(x.turns) // before inlet hooks: what the user typed, not what a hook added
	if p := strings.TrimSpace(x.req.Preface); p != "" {
		// after setUserLinks: its URLs are no consent; first, so the goal stays the last user turn
		x.turns = append([]turn{{msg: provider.Message{Role: "user", Content: p}}}, x.turns...)
	}
	if ck == nil {
		if err := e.runHooks(ctx, x, "inlet"); err != nil {
			return err
		}
		if err := e.buildContext(ctx, x); err != nil {
			return err
		}
	} else {
		ck.restore(x)
	}
	table, specs, err := e.assembleTools(ctx, x)
	if err != nil {
		return err
	}
	if len(x.withheld) > 0 && ck == nil { // the first line of the answer, so the reader of a test sees what was left out
		x.run.delta(withheldNote(x.withheld) + "\n\n")
	}
	if x.req.Unattended && len(x.missing) > 0 {
		// Nobody can fix it mid-run, and running without the app would only produce a wrong report.
		err := api.Errf(http.StatusConflict, "tool_unavailable", "These tools could not be used for this run (not connected, expired, not allowed for this assistant, or not available): "+strings.Join(x.missing, ", "))
		if x.resumed { // a tool server restarting together with this one gets a while to come back
			return &tryLater{err}
		}
		return err
	}
	if x.resumed {
		e.forgetDeferred(x.run.ID) // it is back: a later interruption gets the whole patience again
	}
	if ck == nil {
		if t, ok := table["execute_code"]; ok && t.provider.ID() == sandbox.ID {
			x.system = append(x.system, sandbox.Prompt(ctx, e.Settings))
		}
		if err := e.compact(ctx, x, x.overheadTokens(specs)); err != nil { // after the prompt and the tools exist, so they count
			return err
		}
	}
	if err := e.loop(ctx, x, table, specs); err != nil {
		return err
	}
	return e.runHooks(ctx, x, "outlet")
}

// finish emits usage and background-task results, closes the run and persists.
func (e *Engine) finish(x *exec, status string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// an owner that was replaced must not report the run as done to anyone
	err := e.hold(ctx, x)
	if errors.Is(err, errLostLease) {
		e.abandon(x)
		return
	}
	x.run.emitUsage()
	// the extras are skipped or cut while shutting down: saving the finished answer matters more
	if ok && status == "done" && !e.draining() {
		taskCtx, stop := e.cutOnDrain(ctx)
		e.backgroundTasks(taskCtx, x)
		stop()
	}
	if e.end(x, status, ok) && ok && status == "done" && !e.draining() {
		e.maybeReviewMemory(x)
	}
}

// end closes the run, persists its outcome and then tells the streams it is done, so a reload right after done shows
// the final state. It reports whether the outcome was written by this instance as the run's owner.
func (e *Engine) end(x *exec, status string, ok bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := x.run
	run.settle(status)
	run.mu.Lock()
	u := &usageRow{userID: x.u.ID, modelID: x.m.ID, usage: run.usage, duration: time.Since(x.started)}
	run.mu.Unlock()
	var err error
	if x.chat == nil {
		err = insertUsage(ctx, e.DB, *u)
	} else {
		u.chatID, u.msgID = &x.chat.ID, &run.MessageID
		run.saveMu.Lock()
		err = e.endRun(ctx, x, run.progress(), ok, u)
		run.saveMu.Unlock()
	}
	if errors.Is(err, errLostLease) {
		e.abandon(x)
		return false
	}
	if err != nil { // the row stays; its lease expires and the run is taken over
		slog.Error("end run", "run", run.ID, "err", err)
	}
	run.close()
	e.saveTrace(x)
	return err == nil
}

// save persists the message state (persisted chats only). A run that lost its lease is stopped.
func (e *Engine) save(x *exec) {
	if x.chat == nil {
		return
	}
	run := x.run
	// saves come from the run and from answer handlers; holding saveMu across
	// the read and the write keeps an older state from landing after a newer one
	run.saveMu.Lock()
	defer run.saveMu.Unlock()
	p := run.progress()
	p.Fence = e.fence(x)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch err := e.Chats.SaveProgress(ctx, run.MessageID, p); {
	case errors.Is(err, chats.ErrFenced):
		if !run.Finished() {
			run.stop(errLostLease)
		}
	case err != nil:
		slog.Error("save message", "run", run.ID, "err", err)
	}
}

// savePending writes only the open questions of a run that can be resumed. The rest of its stored message changes
// together with the checkpoint, so that a resumed run finds the message exactly as its checkpoint left it.
func (e *Engine) savePending(x *exec) {
	run := x.run
	run.saveMu.Lock()
	defer run.saveMu.Unlock()
	run.mu.Lock()
	pending := mustMarshal(run.state.PendingCalls, "[]")
	run.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tag, err := e.DB.Exec(ctx, `UPDATE message SET pending_calls = $2, updated_at = now() WHERE id = $1
		AND EXISTS (SELECT 1 FROM run WHERE id = $3 AND lease_owner = $4 AND attempt = $5 FOR SHARE)`,
		run.MessageID, pending, run.ID, e.instance, run.attempt)
	switch {
	case err != nil:
		slog.Error("save the open questions", "run", run.ID, "err", err)
	case tag.RowsAffected() == 0 && !run.Finished():
		run.stop(errLostLease)
	}
}

// progress is the run's message state as it is stored.
func (r *Run) progress() chats.Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := chats.Progress{Content: r.state.Text(), Parts: json.RawMessage(mustMarshal(r.state.Parts, "[]")),
		Files: json.RawMessage(mustMarshal(r.state.Files, "[]")), Sources: json.RawMessage(mustMarshal(r.state.Sources, "[]")),
		PendingCalls: json.RawMessage(mustMarshal(r.state.PendingCalls, "[]")),
		FollowUps:    slices.Clone(r.state.FollowUps), Status: r.state.Status, Error: r.state.Error}
	if r.state.Usage != nil {
		u := *r.state.Usage
		p.Usage = &u
	}
	return p
}

// classify turns an error into the code and text the user sees.
func classify(err error) (string, string) {
	var ae *api.Error
	var he *provider.HTTPError
	switch {
	case errors.As(err, &ae):
		return ae.Code, ae.Message
	case provider.IsContextOverflow(err):
		return "context_length_exceeded", "The conversation does not fit the model's context window and nothing more could be shortened. " +
			"Start a new chat, attach less, or use a model with a larger window."
	case errors.As(err, &he):
		msg := fmt.Sprintf("The model server returned %d %s", he.Status, http.StatusText(he.Status))
		if he.Body != "" {
			msg += ": " + clip(he.Body, 300)
		}
		return "provider_error", msg
	case errors.Is(err, provider.ErrIdleTimeout):
		return "timeout", "The model stopped responding."
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "The request took too long."
	}
	return "error", clip(err.Error(), 300)
}

// ---- conversation -------------------------------------------------------------------

// loadTurns builds the conversation: the stored branch for the web app's
// persisted chats, otherwise the caller's messages.
func (e *Engine) loadTurns(ctx context.Context, x *exec) error {
	req := x.req
	if x.chat != nil && x.parentID != "" && (req.UserMessage != nil || len(req.Messages) == 0) {
		branch, err := e.Chats.Branch(ctx, x.chat.ID, x.parentID)
		if err != nil {
			return err
		}
		digest, local := e.Settings.Bool(ctx, "chat.context.digest"), x.m != nil && x.m.Local
		for _, m := range branch {
			if m.Role == "assistant" && m.Content == "" {
				continue // failed or empty replies add nothing
			}
			content := m.Content
			if m.Role == "assistant" && digest {
				if d := stepDigest(m.Parts, local); d != "" {
					content += "\n\n" + d
				}
			}
			x.turns = append(x.turns, turn{ID: m.ID, msg: provider.Message{Role: m.Role, Content: content},
				files: fileIDsOf(m.Files), summary: m.ContextSummary})
			x.collections = append(x.collections, collectionIDsOf(m.Files)...)
		}
	} else {
		pms := providerMessages(req.Messages)
		for i, pm := range pms {
			t := turn{msg: pm}
			for _, f := range req.Messages[i].Files {
				if f.Type == "collection" {
					x.collections = append(x.collections, f.ID)
				} else if f.ID != "" {
					t.files = append(t.files, f.ID)
				}
			}
			x.turns = append(x.turns, t)
		}
	}
	for _, f := range req.Files {
		switch {
		case f.ID == "":
		case f.Type == "collection":
			x.collections = append(x.collections, f.ID)
		default:
			if i := lastUser(x.turns); i >= 0 && !containsStr(x.turns[i].files, f.ID) {
				x.turns[i].files = append(x.turns[i].files, f.ID)
			}
		}
	}
	// a compaction summary replaces everything up to the message it ends at
	for i := len(x.turns) - 1; i >= 0; i-- {
		if s := x.turns[i].summary; s != "" {
			rest := append([]turn{{msg: provider.Message{Role: "system", Content: summaryBlock(s)}}}, x.turns[i+1:]...)
			x.turns = rest
			break
		}
	}
	if lastUser(x.turns) < 0 {
		return api.Errf(http.StatusBadRequest, "no_user_message", "the conversation has no user message")
	}
	return nil
}

func lastUser(ts []turn) int {
	for i := len(ts) - 1; i >= 0; i-- {
		if ts[i].msg.Role == "user" {
			return i
		}
	}
	return -1
}

// lastUserText is the newest user message's text.
func (x *exec) lastUserText() string {
	if i := lastUser(x.turns); i >= 0 {
		return x.turns[i].msg.Content
	}
	return ""
}

// fileIDsOf reads file ids from a stored files list ({id, type} objects).
func fileIDsOf(raw json.RawMessage) []string { return refIDs(raw, false) }

func collectionIDsOf(raw json.RawMessage) []string { return refIDs(raw, true) }

func refIDs(raw json.RawMessage, collections bool) []string {
	var list []map[string]any
	_ = json.Unmarshal(raw, &list)
	var out []string
	for _, f := range list {
		id, _ := f["id"].(string)
		if id == "" {
			continue
		}
		if (f["type"] == "collection") == collections {
			out = append(out, id)
		}
	}
	return out
}

func summaryBlock(s string) string {
	return "Summary of the earlier part of this conversation (older messages were condensed):\n\n" + s
}

// features resolves which optional features run: the request's toggles
// (defaults for callers that send none), what the model supports and what
// the user may use.
func (e *Engine) features(ctx context.Context, x *exec) map[string]bool {
	out := map[string]bool{}
	for _, id := range []string{"web_search", "code_interpreter", "memory"} {
		v, set := x.req.Features[id]
		on := v
		switch {
		case set:
		case id == "memory":
			on = true // unless switched off, memory follows the user's own setting
		case x.req.Features == nil:
			on = containsStr(x.m.DefaultFeatures, id)
		}
		// memory follows the user's own setting unless the model turns it off
		modelOK := supported(x.m.Features[id])
		if id == "memory" {
			modelOK = !switchedOff(x.m.Features[id])
		}
		out[id] = on && x.u.Can("features."+id) && modelOK
	}
	return out
}

// switchedOff reports a model feature value that says no: false, "false" or "off".
func switchedOff(v any) bool {
	switch x := v.(type) {
	case bool:
		return !x
	case string:
		return x == "false" || x == "off"
	}
	return false
}

// supported reads a model feature value ({"web_search": "native"}, true, "false").
func supported(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != "" && x != "false" && x != "off"
	}
	return v != nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
