package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/tools"
)

// askTimeout bounds how long a tool waits for the user by default.
var askTimeout = 30 * time.Minute

// coworkAskTimeout is the default wait in a task, where the user is often away.
// ponytail: an in-memory wait; a restart withdraws the question (the start-up
// sweep marks the message as failed). Persist pending asks if tasks must survive restarts.
var coworkAskTimeout = 12 * time.Hour

// keepToolImages is how many tool image turns (screenshots) stay in the
// conversation; older ones are blanked to bound tokens.
// ponytail: a constant; make it a setting if desktop tasks need more history.
const keepToolImages = 3

// toolImageTypes are the image types every vision API accepts; a tool image
// of another type (SVG, BMP, TIFF, HEIC...) is not sent to the model.
var toolImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// toolEntry is one function the model may call this run.
type toolEntry struct {
	spec     tools.Spec
	provider tools.Provider
	server   string // tool server id ("" for built-ins)
}

// builtinFeature names the request feature a built-in set needs.
var builtinFeature = map[string]string{
	"builtin:code":   "code_interpreter",
	"builtin:web":    "web_search",
	"builtin:memory": "memory",
}

// builtinAllowed decides whether a built-in set joins this run.
func (e *Engine) builtinAllowed(ctx context.Context, x *exec, id string) bool {
	short := strings.TrimPrefix(id, "builtin:")
	if v, ok := x.m.Tools.Builtins[short]; ok && !v {
		return false
	}
	switch id {
	case "builtin:files":
		return len(x.fileIDs) > 0 && x.m.FileContext != "full"
	case "builtin:web":
		return x.features["web_search"] && fmt.Sprint(x.m.Features["web_search"]) != "native"
	case "builtin:memory":
		if !e.Settings.Bool(ctx, "chat.memory.enable") || !x.profile.Memory || !x.features["memory"] {
			return false
		}
		return x.m.Local || !e.Settings.Bool(ctx, "chat.memory.local_only")
	case "builtin:knowledge":
		return len(x.m.Knowledge) > 0 || len(x.collections) > 0
	}
	if f, ok := builtinFeature[id]; ok {
		return x.features[f]
	}
	return true
}

// serverIDs is the run's tool servers: the request's tool_ids when given
// ("server:<id>" or a bare id; "builtin:*" entries are ignored), else the model's.
func (x *exec) serverIDs() []string {
	if x.req.ToolIDs == nil {
		return x.m.Tools.Servers
	}
	var out []string
	for _, id := range x.req.ToolIDs {
		if strings.HasPrefix(id, "builtin:") {
			continue
		}
		id = strings.TrimPrefix(id, "server:")
		if id != "" && !containsStr(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// assembleTools lists the functions for this run. Later providers shadow
// earlier ones with the same function name: built-ins first, then servers
// in order (so a server's fetch_url replaces the built-in). Passthrough
// requests (the caller sent its own tools) get only those.
func (e *Engine) assembleTools(ctx context.Context, x *exec) (map[string]toolEntry, []provider.Tool, error) {
	if len(x.req.Tools) > 0 {
		return nil, clientTools(x.req.Tools), nil
	}
	all, err := tools.All(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list tool providers: %w", err)
	}
	var order []tools.Provider
	for _, p := range all {
		if strings.HasPrefix(p.ID(), "builtin:") && e.builtinAllowed(ctx, x, p.ID()) {
			order = append(order, p)
		}
	}
	if !x.disableServers {
		for _, id := range x.serverIDs() {
			for _, p := range all {
				if p.ID() == id && !strings.HasPrefix(id, "builtin:") {
					if s, ok := p.(interface{ LocalOnly() bool }); ok && s.LocalOnly() && !x.m.Local {
						slog.Debug("local-only tool server skipped for a hosted model", "server", id, "model", x.m.ID)
						continue // its data (mail, documents) must never reach a hosted provider
					}
					if s, ok := p.(interface{ ModelOnly() bool }); ok && s.ModelOnly() && !containsStr(x.m.Tools.Servers, id) {
						slog.Warn("model-only tool server refused: the model does not list it", "server", id, "model", x.m.ID)
						continue // confidential tools stay inside the assistants built for them
					}
					order = append(order, p)
				}
			}
		}
	}
	table := map[string]toolEntry{}
	var names []string
	listed := map[string]bool{} // requested servers that offered at least one function
	x.withheld = nil
	add := func(p tools.Provider) {
		specs, err := p.Tools(ctx, x.u)
		if err != nil {
			slog.Warn("tool provider unavailable", "provider", p.ID(), "err", err)
			return
		}
		server := ""
		if !strings.HasPrefix(p.ID(), "builtin:") {
			server = p.ID()
			listed[server] = len(specs) > 0
		}
		for _, s := range specs {
			if e.toolDisabled(x, s.Name) {
				continue
			}
			if x.req.ReadOnly && !s.ReadOnly && !testSets[p.ID()] {
				x.withheld = append(x.withheld, s.Name)
				continue
			}
			if _, dup := table[s.Name]; !dup {
				names = append(names, s.Name)
			}
			table[s.Name] = toolEntry{spec: s, provider: p, server: server}
		}
	}
	for _, p := range order {
		add(p)
	}
	// A result too long for the prompt is stored as a file the model reads back with view_file, so the
	// file tools join whenever the run has tools that can return long text (not only the core set).
	if v, set := x.m.Tools.Builtins["files"]; !set || v {
		if _, has := table["view_file"]; !has && e.spillOn(ctx) && hasWorkTools(table) {
			for _, p := range all {
				if p.ID() == "builtin:files" {
					add(p)
				}
			}
		}
	}
	// A server the run asked for but that did not join (not connected, local-only on
	// a hosted model, hook-disabled, listing failed) is normally dropped silently;
	// unattended runs fail on it, so x.missing records it.
	x.missing = nil
	for _, id := range x.serverIDs() {
		if !strings.HasPrefix(id, "builtin:") && !listed[id] {
			x.missing = append(x.missing, id)
		}
	}
	specs := make([]provider.Tool, 0, len(names))
	for _, n := range names {
		s := table[n].spec
		params := s.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		specs = append(specs, provider.Tool{Name: s.Name, Description: s.Description, Parameters: params})
	}
	return table, specs, nil
}

// UsableServers returns those of toolIDs ("server:<id>" or bare ids; built-in
// ids are ignored) that would join a run of model for u right now, so a saved
// task keeps only what works today. It errors when u may not use the model.
func (e *Engine) UsableServers(ctx context.Context, u *auth.User, model string, toolIDs []string) ([]string, error) {
	m, err := e.Models.Resolve(ctx, u, model)
	if err != nil {
		return nil, err
	}
	if m.Kind != "" {
		return nil, models.ErrNotChat
	}
	x := &exec{u: u, m: m, req: &Request{ToolIDs: append([]string{}, toolIDs...)}, disabledNames: map[string]bool{}}
	if _, _, err := e.assembleTools(ctx, x); err != nil {
		return nil, err
	}
	out := []string{}
	for _, id := range x.serverIDs() {
		if !strings.HasPrefix(id, "builtin:") && !slices.Contains(x.missing, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// spillOn reports whether long tool results may be stored as files (tools.spill_chars is not negative).
func (e *Engine) spillOn(ctx context.Context) bool {
	n := 0
	e.setting(ctx, "tools.spill_chars", &n)
	return n >= 0
}

// hasWorkTools reports whether the run has a tool besides the core set (time, questions, chat search).
func hasWorkTools(table map[string]toolEntry) bool {
	for _, t := range table {
		if t.provider.ID() != "builtin:core" {
			return true
		}
	}
	return false
}

func (e *Engine) toolDisabled(x *exec, name string) bool {
	if v, ok := x.m.Tools.Builtins[name]; ok && !v {
		return true
	}
	if name == "ask_user" && !x.interactive {
		return true // nobody is there to answer
	}
	return containsStr(x.m.Tools.Disabled, name) || x.disabledNames[name]
}

// finalReportNote is added at the round cap, in place of one more round of tool calls.
const finalReportNote = "[The limit on tool rounds was reached. Do not call any more tools. Write the final report now: what was done, " +
	"what was found, and what is still open or could not be done.]"

// loop calls the model and runs tool calls until it answers without one.
func (e *Engine) loop(ctx context.Context, x *exec, table map[string]toolEntry, specs []provider.Tool) error {
	client, err := e.Models.Client(x.m)
	if err != nil {
		return err
	}
	maxIter, key := 30, "chat.max_tool_iterations"
	if x.cowork() {
		maxIter, key = 100, "cowork.max_tool_iterations"
	}
	e.setting(ctx, key, &maxIter)
	maxIter = max(maxIter, 1)
	msgs := x.messages()
	toolJSON, _ := json.Marshal(specs)
	_, hasView := table["view_file"]
	b := e.newBudget(ctx, x, len(toolJSON), len(msgs), hasView)
	st := e.newLoopState(ctx)
	var imageTurns []int // indexes in msgs of the user turns carrying tool images
	passthrough := len(x.req.Tools) > 0
	for round := 0; ; round++ {
		x.run.mu.Lock()
		x.run.roundText = ""
		x.run.mu.Unlock()
		msgs = e.fit(ctx, x, b, msgs, round)
		req := provider.Request{Model: x.m.ProviderModel, Messages: msgs, Tools: specs, Params: x.params, ProviderTools: x.providerTools}
		final := !passthrough && round >= maxIter
		if final { // the cap: one last call without tools that asks for the report
			x.run.status(StatusEvent{ID: "limit", Action: "tool_call", Description: fmt.Sprintf("Stopped after %d tool rounds", maxIter), Done: true})
			req.Messages = append(slices.Clone(msgs), provider.Message{Role: "user", Content: finalReportNote})
			req.Tools, req.ProviderTools = nil, nil
		}
		calls, finish, err := e.callModel(ctx, x, client, req, round, b, final)
		if err != nil {
			if final && ctx.Err() == nil { // the work is done and saved; only the closing report is missing
				slog.Warn("final report call failed", "run", x.run.ID, "err", err)
				return nil
			}
			return err
		}
		x.run.mu.Lock()
		x.run.finish = finish
		x.run.mu.Unlock()
		if len(calls) == 0 || final {
			return nil
		}
		if passthrough {
			x.run.mu.Lock()
			x.run.passthrough, x.run.finish = calls, "tool_calls"
			x.run.mu.Unlock()
			for _, c := range calls {
				x.run.toolCall(c.ID, c.Name, "client", c.Arguments)
			}
			return nil
		}
		x.run.mu.Lock()
		text := x.run.roundText
		x.run.mu.Unlock()
		msgs = append(msgs, provider.Message{Role: "assistant", Content: text, ToolCalls: calls})
		b.callChars = b.callShare(msgs, len(calls)) // the round's results share what is left of the window
		results := e.runCalls(ctx, x, table, b, st, calls, round)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		var shots []provider.Image
		for i, c := range calls {
			msgs = append(msgs, provider.Message{Role: "tool", ToolCallID: c.ID, Name: c.Name, Content: results[i].content})
			shots = append(shots, results[i].images...)
		}
		if len(shots) > 0 { // tool messages cannot carry images in every API; a user turn can
			// ponytail: only the round's last image is sent (earlier ones are stale
			// screens of the same desktop); keep more per round if a tool returns
			// several distinct images the model must compare.
			msgs = append(msgs, provider.Message{Role: "user", Content: "[Screenshots returned by the tool calls above. Continue the task.]", Images: shots[len(shots)-1:]})
			imageTurns = append(imageTurns, len(msgs)-1)
			if len(imageTurns) > keepToolImages {
				old := &msgs[imageTurns[0]]
				old.Images, old.Content = nil, "[earlier screenshot removed]"
				imageTurns = imageTurns[1:]
			}
		}
		x.run.snapshot()
		e.save(x)
	}
}

// retryDelay is the wait before retry n (0-based) of a model call; a variable so tests need not wait.
var retryDelay = func(n int) time.Duration { return time.Second << n }

// retryable reports whether a model call that failed before any event is worth repeating: the server was
// busy or failing (429, 5xx), unreachable, or went quiet. A refusal (4xx, a blocked prompt) would fail again, and so
// does a prompt that is too long for the window, even when the server answers it with a 500: the loop makes it
// shorter first (see overflowRetry).
func retryable(err error) bool {
	if provider.IsContextOverflow(err) {
		return false
	}
	var he *provider.HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests || he.Status >= 500
	}
	if errors.Is(err, provider.ErrIdleTimeout) {
		return true
	}
	var ne net.Error
	return !errors.Is(err, context.DeadlineExceeded) && (errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF))
}

// attempt is what one streamed model call produced.
type attempt struct {
	calls     []provider.ToolCall
	finish    string
	use       *provider.Usage
	textChars int
	got       bool // at least one event arrived
	err       error
}

// callModel streams one model call into the run and returns its tool calls. A call that fails before
// any event arrived (nothing shown, no side effect) is repeated up to chat.model_retries times when
// the error is 429, 5xx, a connection error or the idle timeout. round is the loop round, for the
// trace; final marks the call at the round cap.
func (e *Engine) callModel(ctx context.Context, x *exec, client provider.Client, req provider.Request, round int, b *runBudget, final bool) ([]provider.ToolCall, string, error) {
	retries := 2
	e.setting(ctx, "chat.model_retries", &retries)
	for try := 0; ; try++ {
		started := time.Now()
		a := e.streamOnce(ctx, x, client, req)
		again := a.err != nil && !a.got && try < retries && ctx.Err() == nil && retryable(a.err)
		x.traceModel(round, req, started, a, modelTrace{attempt: try + 1, retry: again, final: final})
		if a.use != nil {
			b.observe(x, round, req.Messages, a.use.PromptTokens)
		}
		if !again {
			return a.calls, a.finish, a.err
		}
		slog.Warn("model call failed before any output, retrying", "run", x.run.ID, "model", x.m.ID, "attempt", try+1, "err", a.err)
		select {
		case <-time.After(retryDelay(try)):
		case <-ctx.Done():
			return nil, "", context.Cause(ctx)
		}
	}
}

// streamOnce makes one model call.
func (e *Engine) streamOnce(ctx context.Context, x *exec, client provider.Client, req provider.Request) (a attempt) {
	a.finish = "stop"
	for ev, serr := range client.Stream(ctx, req) {
		if serr != nil {
			a.err = serr
			if c := context.Cause(ctx); c != nil {
				a.err = c
			}
			a.calls, a.finish = nil, ""
			return a
		}
		a.got = true
		switch ev.Kind {
		case provider.EventText:
			a.textChars += utf8.RuneCountInString(ev.Text)
			x.run.delta(ev.Text)
		case provider.EventReasoning:
			x.run.reasoningDelta(ev.Text)
		case provider.EventToolCall:
			a.calls = append(a.calls, ev.ToolCall)
		case provider.EventCitation:
			x.run.sources([]Source{{URL: ev.Citation.URL, Title: ev.Citation.Title, Name: ev.Citation.Title}})
		case provider.EventUsage:
			x.run.addUsage(ev.Usage)
			u := ev.Usage
			a.use = &u
		case provider.EventDone:
			if ev.FinishReason != "" {
				a.finish = ev.FinishReason
			}
		}
	}
	x.run.reasoningDone()
	if len(a.calls) > 0 {
		a.finish = "tool_calls"
	}
	return a
}

// loopState is what the rounds of one run share: the repeat guard and the limits.
type loopState struct {
	repeatLimit int  // the same call this many times in a row is not run again (0 = off)
	parallel    bool // run read-only calls of one round at once
	lastKey     string
	lastCount   int
}

func (e *Engine) newLoopState(ctx context.Context) *loopState {
	st := &loopState{repeatLimit: 3, parallel: e.Settings.Bool(ctx, "chat.parallel_tools")}
	e.setting(ctx, "chat.repeat_limit", &st.repeatLimit)
	return st
}

// repeated counts c against the calls before it and returns the text that replaces its result when
// it is the repeatLimit-th identical call in a row ("" = run it).
// ponytail: compares the raw argument text, so the same arguments in another key order pass; normalise if models do that.
func (st *loopState) repeated(c provider.ToolCall) string {
	key := c.Name + "\x00" + string(c.Arguments)
	if key == st.lastKey {
		st.lastCount++
	} else {
		st.lastKey, st.lastCount = key, 1
	}
	if st.repeatLimit <= 0 || st.lastCount < st.repeatLimit {
		return ""
	}
	return fmt.Sprintf("Not run: %s was already called with these exact arguments %d times in a row. Repeating it will not give a different result. "+
		"Try different arguments or another approach, or answer with what you already have.", c.Name, st.lastCount-1)
}

// readOnlySets are the built-in sets whose calls never change anything, so one round's calls may run together.
var readOnlySets = map[string]bool{"builtin:web": true, "builtin:knowledge": true, "builtin:files": true}

// testSets are the built-in sets a read-only run keeps: none of them changes anything outside the run
// (the code set runs in the sandbox). Memory is left out on purpose: it would change what is remembered.
// ponytail: a tool server's tool is kept only when the server declares it read-only (Spec.ReadOnly), so a server
// that annotates nothing offers nothing to a test; add a per-server list in its config if that proves too strict.
var testSets = map[string]bool{"builtin:core": true, "builtin:files": true, "builtin:knowledge": true, "builtin:web": true, "builtin:skills": true, "builtin:code": true}

// withheldNote tells the reader of a test run which tools were not offered.
func withheldNote(names []string) string {
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	more := ""
	if len(names) > 8 {
		more = fmt.Sprintf(" and %d more", len(names)-8)
		names = names[:8]
	}
	return fmt.Sprintf("Test run: tools that may change things are not available (%s%s)", strings.Join(names, ", "), more)
}

// maxParallel bounds the calls of one round that run at once.
// ponytail: a constant; make it a setting if a deployment's search or file backends want more.
const maxParallel = 4

// callResult is what one call returns to the loop.
type callResult struct {
	content string
	images  []provider.Image
}

// runCalls runs one round's calls and returns their results in call order. The calls run at once only
// when there are several and every one belongs to a read-only built-in set; anything else stays serial.
func (e *Engine) runCalls(ctx context.Context, x *exec, table map[string]toolEntry, b *runBudget, st *loopState, calls []provider.ToolCall, round int) []callResult {
	out := make([]callResult, len(calls))
	skip := make([]string, len(calls))
	for i, c := range calls {
		skip[i] = st.repeated(c)
	}
	one := func(i int, c provider.ToolCall) {
		content, imgs := e.runTool(ctx, x, table, b, c, round, skip[i])
		out[i] = callResult{content, imgs}
	}
	if !st.parallel || len(calls) < 2 || !allReadOnly(table, calls) {
		for i := range calls {
			if ctx.Err() != nil {
				break
			}
			one(i, e.announce(x, table, calls[i]))
		}
		return out
	}
	x.run.mu.Lock()
	first := len(x.run.tools)
	x.run.mu.Unlock()
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallel)
	for i := range calls {
		wg.Add(1)
		sem <- struct{}{}
		c := e.announce(x, table, calls[i]) // here, not in the goroutine: goroutines start in any order
		go func() {
			defer func() {
				if v := recover(); v != nil { // a goroutine's panic would end the server, not just this run
					slog.Error("tool panic", "run", x.run.ID, "tool", calls[i].Name, "value", v)
					out[i] = callResult{content: "Error: the tool crashed."}
				}
				<-sem
				wg.Done()
			}()
			one(i, c)
		}()
	}
	wg.Wait()
	// the calls were recorded as they finished; the response lists them in the order the model made them
	x.run.mu.Lock()
	slices.SortStableFunc(x.run.tools[first:], func(a, b toolRecord) int { return cmp.Compare(callIndex(calls, a.ID), callIndex(calls, b.ID)) })
	x.run.mu.Unlock()
	return out
}

func callIndex(calls []provider.ToolCall, id string) int {
	return slices.IndexFunc(calls, func(c provider.ToolCall) bool { return c.ID == id })
}

func allReadOnly(table map[string]toolEntry, calls []provider.ToolCall) bool {
	for _, c := range calls {
		t, ok := table[c.Name]
		if !ok || !readOnlySets[t.provider.ID()] {
			return false
		}
	}
	return true
}

// announce shows a call to the user as a step and returns it with its unset arguments dropped, ready for runTool.
func (e *Engine) announce(x *exec, table map[string]toolEntry, c provider.ToolCall) provider.ToolCall {
	entry, ok := table[c.Name]
	if ok {
		c.Arguments = dropUnsetArgs(entry.spec.Parameters, c.Arguments)
	}
	x.run.toolCall(c.ID, c.Name, entry.server, c.Arguments)
	return c
}

// runTool executes one announced call and returns what the model reads back: the text
// and, for a vision model, the result's images.
// skip, when set, is the result instead of running the call (the repeat guard).
func (e *Engine) runTool(ctx context.Context, x *exec, table map[string]toolEntry, b *runBudget, c provider.ToolCall, round int, skip string) (string, []provider.Image) {
	started := time.Now()
	entry, ok := table[c.Name]
	var res tools.Result
	switch {
	case !ok:
		res = tools.Result{Content: fmt.Sprintf("Error: there is no tool named %q.", c.Name), IsError: true}
	case skip != "":
		res = tools.Result{Content: skip, IsError: true}
	default:
		var err error
		call := e.toolCall(x, c)
		call.MaxChars = b.callChars
		res, err = entry.provider.Execute(ctx, call)
		if err != nil {
			if ctx.Err() != nil {
				res = tools.Result{Content: "Cancelled.", IsError: true}
			} else {
				slog.Warn("tool failed", "tool", c.Name, "server", entry.server, "err", err)
				res = tools.Result{Content: "Error: " + err.Error(), IsError: true}
			}
		}
	}
	spillText, spillFile, spilledOut := e.spill(ctx, x, b, c, res, round)
	if spilledOut {
		res.Inputs = append(res.Inputs, spillFile.id) // an input of the step: the model reads it, the answer does not carry it
	}
	shown := res.Content // what the model reads of the result
	if spilledOut {
		shown = spillText
	}
	capped := b.callChars > 0 && len(shown) > b.callChars+capSlack // more than this call's share of the window
	if capped {
		shown = cutResult(shown, b.callChars)
	}
	x.run.trace.add(traceStep{Kind: stepTool, Name: c.Name, Round: round, Started: started, Duration: time.Since(started),
		Detail: map[string]any{"server": entry.server, "args_chars": len(c.Arguments), "result_chars": utf8.RuneCountInString(res.Content),
			"is_error": res.IsError, "truncated": res.Truncated || capped, "files": len(res.Files), "images": len(res.Images), "known": ok,
			"spilled": spilledOut, "repeat_refused": skip != "", "capped": capped, "cap_chars": b.callChars}})
	refs := e.fileRefs(ctx, res.Files)
	x.run.files(refs)
	var inputs []FileRef
	for _, id := range res.Inputs {
		if ok, err := e.Files.CanRead(ctx, x.u, id); err == nil && ok {
			inputs = append(inputs, e.fileRefs(ctx, []string{id})...)
		}
	}
	// later tool calls in this turn can open them (e.g. execute_code edits a .pptx a Drive tool just fetched)
	for _, r := range append(slices.Clone(refs), inputs...) {
		x.addFile(r.ID)
	}
	x.run.sources(toolSources(res.Sources))
	for _, p := range res.Parts {
		x.run.block(p)
	}
	// Tool images need vision set explicitly: a base model without a
	// capabilities row may be text-only, and its provider would reject the run.
	vision := x.m.Capabilities["vision"] == true
	stepFiles := append(slices.Clone(refs), inputs...) // inputs: on the step only, not attached to the answer
	var imgs []provider.Image
	var notes []string // why images were not shown
	for _, id := range res.Images {
		if ok, err := e.Files.CanRead(ctx, x.u, id); err != nil || !ok {
			continue
		}
		f, err := e.Files.Get(ctx, id)
		if err != nil || !strings.HasPrefix(f.ContentType, "image/") {
			continue
		}
		if !slices.ContainsFunc(stepFiles, func(r FileRef) bool { return r.ID == id }) {
			stepFiles = append(stepFiles, refOf(f)) // on the step only, not attached to the answer
		}
		note := ""
		switch {
		case !vision:
			note = "this model has no vision"
		case !toolImageTypes[f.ContentType]:
			note = f.ContentType + " is not a supported image type"
		case f.Size > maxImageBytes:
			note = fmt.Sprintf("%s is larger than %d MB", f.Filename, maxImageBytes>>20)
		default:
			if img, ok := e.image(ctx, f); ok {
				imgs = append(imgs, img)
			} else {
				note = f.Filename + " could not be read"
			}
		}
		if note != "" && !slices.Contains(notes, note) {
			notes = append(notes, note)
		}
	}
	x.run.toolResult(c.ID, clip(res.Content, 2000), stepFiles, res.IsError)
	x.run.mu.Lock()
	x.run.tools = append(x.run.tools, toolRecord{ID: c.ID, Name: c.Name, Args: c.Arguments, Result: clip(res.Content, 20000), Error: res.IsError})
	x.run.mu.Unlock()
	content := shown
	if content == "" && len(refs) > 0 {
		var names []string
		for _, f := range refs {
			names = append(names, f.Name)
		}
		content = "Files created and shown to the user: " + strings.Join(names, ", ")
	}
	if len(notes) > 0 {
		content = strings.TrimSpace(content + "\n(image not shown: " + strings.Join(notes, "; ") + ")")
	}
	return content, imgs
}

// addFile makes a file readable by the run's later tool calls (calls of one round may run together).
func (x *exec) addFile(id string) {
	x.run.mu.Lock()
	defer x.run.mu.Unlock()
	if !slices.Contains(x.fileIDs, id) {
		x.fileIDs = append(x.fileIDs, id)
	}
}

// readableFiles is a copy of the files the run's tool calls may open.
func (x *exec) readableFiles() []string {
	x.run.mu.Lock()
	defer x.run.mu.Unlock()
	return slices.Clone(x.fileIDs)
}

// toolCall builds the provider-facing Call.
func (e *Engine) toolCall(x *exec, c provider.ToolCall) tools.Call {
	run := x.run
	chatID := run.ChatID
	if x.noChat {
		chatID = ""
	}
	tok, _ := signRunToken(e.Keys, run.ID, run.UserID, time.Now().Add(15*time.Minute))
	return tools.Call{ID: c.ID, Name: c.Name, Args: c.Arguments, User: x.u, ModelID: x.m.ID, ChatID: chatID,
		MessageID: run.MessageID, RunID: run.ID, Task: x.cowork(), FileIDs: x.readableFiles(), Cite: x.cites.number,
		CallbackURL: e.callbackURL(run.ID), CallbackToken: tok,
		Emit: func(ev tools.Event) { e.applyEvent(run, c.ID, ev.Type, ev.Data) },
		Ask: func(ctx context.Context, r tools.AskRequest) (json.RawMessage, error) {
			timeout := askTimeout
			if x.cowork() {
				timeout = coworkAskTimeout
			}
			if r.TimeoutMS > 0 {
				timeout = time.Duration(r.TimeoutMS) * time.Millisecond
			}
			return run.ask(ctx, pendingFor(r), timeout)
		}}
}

// pendingFor maps a tool's question to the card the web app shows.
func pendingFor(r tools.AskRequest) PendingCall {
	if r.Kind == "confirm" {
		return PendingCall{Kind: "confirm", Title: r.Title, Message: r.Message}
	}
	q := r.Questions
	if len(q) == 0 || string(q) == "null" {
		q, _ = json.Marshal([]map[string]any{{"question": coalesce(r.Message, r.Title), "header": r.Title,
			"options": []any{}, "allowOther": true}})
	}
	return PendingCall{Kind: "ask_user", Questions: q}
}

// applyEvent handles a progress event from a tool, hook or callback.
// Only status, sources, files and block are accepted.
func (e *Engine) applyEvent(run *Run, callID, typ string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	switch typ {
	case "status":
		var s StatusEvent
		if json.Unmarshal(b, &s) == nil {
			if s.CallID == "" {
				s.CallID = callID
			}
			run.status(s)
		}
	case "sources":
		var wrapped struct {
			Sources []tools.Source `json:"sources"`
		}
		var list []tools.Source
		if json.Unmarshal(b, &list) != nil && json.Unmarshal(b, &wrapped) == nil {
			list = wrapped.Sources
		}
		run.sources(toolSources(list))
	case "files":
		var ids []string
		if json.Unmarshal(b, &ids) != nil {
			var wrapped struct {
				Files []struct {
					ID string `json:"id"`
				} `json:"files"`
			}
			_ = json.Unmarshal(b, &wrapped)
			for _, f := range wrapped.Files {
				ids = append(ids, f.ID)
			}
		}
		run.files(e.fileRefs(context.Background(), ids))
	case "block":
		var w struct {
			Block json.RawMessage `json:"block"`
		}
		if json.Unmarshal(b, &w) == nil && len(w.Block) > 0 {
			run.block(w.Block)
		} else {
			run.block(b)
		}
	}
}

func toolSources(in []tools.Source) []Source {
	out := make([]Source, 0, len(in))
	for _, s := range in {
		src := Source{Name: s.Name, URL: s.URL, FileID: s.FileID, Page: s.Page, Snippet: s.Snippet}
		if s.Cite > 0 {
			src.ID = strconv.Itoa(s.Cite)
		}
		out = append(out, src)
	}
	return out
}

// fileRefs loads display info for file ids (unknown ids are skipped).
func (e *Engine) fileRefs(ctx context.Context, ids []string) []FileRef {
	var out []FileRef
	for _, id := range ids {
		f, err := e.Files.Get(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, refOf(f))
	}
	return out
}

func refOf(f files.File) FileRef {
	t := "file"
	if strings.HasPrefix(f.ContentType, "image/") {
		t = "image"
	}
	return FileRef{ID: f.ID, Name: f.Filename, Size: f.Size, ContentType: f.ContentType, Type: t}
}

// dropUnsetArgs removes placeholder values ("", 0, false, [], {}) the model put
// in optional parameters whose schema default is null, i.e. "not set". Some
// models fill every optional field; for a tool like format_sheet_range an empty
// dropdown_values fails the call and merge_cells: false unmerges the range.
// ponytail: an intentional false/0 on such a parameter is dropped too (e.g.
// unmerge via merge_cells: false); give the schema a non-null default to keep it.
func dropUnsetArgs(schema, args json.RawMessage) json.RawMessage {
	var sc struct {
		Properties map[string]map[string]json.RawMessage `json:"properties"`
		Required   []string                              `json:"required"`
	}
	var a map[string]json.RawMessage
	if json.Unmarshal(schema, &sc) != nil || json.Unmarshal(args, &a) != nil || len(a) == 0 {
		return args
	}
	dropped := false
	for k, v := range a {
		d, ok := sc.Properties[k]["default"]
		if !ok || string(d) != "null" || slices.Contains(sc.Required, k) {
			continue
		}
		switch strings.TrimSpace(string(v)) {
		case `""`, "0", "false", "[]", "{}", "null":
			delete(a, k)
			dropped = true
		}
	}
	if !dropped {
		return args
	}
	out, err := json.Marshal(a)
	if err != nil {
		return args
	}
	return out
}
