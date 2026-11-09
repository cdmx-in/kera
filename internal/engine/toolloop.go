package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/files"
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
	for _, p := range order {
		specs, err := p.Tools(ctx, x.u)
		if err != nil {
			slog.Warn("tool provider unavailable", "provider", p.ID(), "err", err)
			continue
		}
		server := ""
		if !strings.HasPrefix(p.ID(), "builtin:") {
			server = p.ID()
		}
		for _, s := range specs {
			if e.toolDisabled(x, s.Name) {
				continue
			}
			if _, dup := table[s.Name]; !dup {
				names = append(names, s.Name)
			}
			table[s.Name] = toolEntry{spec: s, provider: p, server: server}
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

func (e *Engine) toolDisabled(x *exec, name string) bool {
	if v, ok := x.m.Tools.Builtins[name]; ok && !v {
		return true
	}
	if name == "ask_user" && !x.interactive {
		return true // nobody is there to answer
	}
	return containsStr(x.m.Tools.Disabled, name) || x.disabledNames[name]
}

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
	msgs := x.messages()
	var imageTurns []int // indexes in msgs of the user turns carrying tool images
	passthrough := len(x.req.Tools) > 0
	for round := 0; ; round++ {
		x.run.mu.Lock()
		x.run.roundText = ""
		x.run.mu.Unlock()
		calls, finish, err := e.callModel(ctx, x, client, provider.Request{Model: x.m.ProviderModel, Messages: msgs,
			Tools: specs, Params: x.params, ProviderTools: x.providerTools})
		if err != nil {
			return err
		}
		x.run.mu.Lock()
		x.run.finish = finish
		x.run.mu.Unlock()
		if len(calls) == 0 {
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
		if round >= maxIter {
			x.run.status(StatusEvent{ID: "limit", Action: "tool_call", Description: fmt.Sprintf("Stopped after %d tool rounds", maxIter), Done: true})
			return nil
		}
		x.run.mu.Lock()
		text := x.run.roundText
		x.run.mu.Unlock()
		msgs = append(msgs, provider.Message{Role: "assistant", Content: text, ToolCalls: calls})
		var shots []provider.Image
		for _, c := range calls {
			content, imgs := e.runTool(ctx, x, table, c)
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			msgs = append(msgs, provider.Message{Role: "tool", ToolCallID: c.ID, Name: c.Name, Content: content})
			shots = append(shots, imgs...)
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

// callModel streams one model call into the run and returns its tool calls.
func (e *Engine) callModel(ctx context.Context, x *exec, client provider.Client, req provider.Request) ([]provider.ToolCall, string, error) {
	var calls []provider.ToolCall
	finish := "stop"
	for ev, err := range client.Stream(ctx, req) {
		if err != nil {
			if c := context.Cause(ctx); c != nil {
				return nil, "", c
			}
			return nil, "", err
		}
		switch ev.Kind {
		case provider.EventText:
			x.run.delta(ev.Text)
		case provider.EventReasoning:
			x.run.reasoningDelta(ev.Text)
		case provider.EventToolCall:
			calls = append(calls, ev.ToolCall)
		case provider.EventCitation:
			x.run.sources([]Source{{URL: ev.Citation.URL, Title: ev.Citation.Title, Name: ev.Citation.Title}})
		case provider.EventUsage:
			x.run.addUsage(ev.Usage)
		case provider.EventDone:
			if ev.FinishReason != "" {
				finish = ev.FinishReason
			}
		}
	}
	x.run.reasoningDone()
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	return calls, finish, nil
}

// runTool executes one call and returns what the model reads back: the text
// and, for a vision model, the result's images.
func (e *Engine) runTool(ctx context.Context, x *exec, table map[string]toolEntry, c provider.ToolCall) (string, []provider.Image) {
	entry, ok := table[c.Name]
	if ok {
		c.Arguments = dropUnsetArgs(entry.spec.Parameters, c.Arguments)
	}
	x.run.toolCall(c.ID, c.Name, entry.server, c.Arguments)
	var res tools.Result
	if !ok {
		res = tools.Result{Content: fmt.Sprintf("Error: there is no tool named %q.", c.Name), IsError: true}
	} else {
		var err error
		res, err = entry.provider.Execute(ctx, e.toolCall(x, c))
		if err != nil {
			if ctx.Err() != nil {
				res = tools.Result{Content: "Cancelled.", IsError: true}
			} else {
				slog.Warn("tool failed", "tool", c.Name, "server", entry.server, "err", err)
				res = tools.Result{Content: "Error: " + err.Error(), IsError: true}
			}
		}
	}
	refs := e.fileRefs(ctx, res.Files)
	x.run.files(refs)
	// later tool calls in this turn can open them (e.g. execute_code edits a .pptx a Drive tool just fetched)
	for _, r := range refs {
		if !slices.Contains(x.fileIDs, r.ID) {
			x.fileIDs = append(x.fileIDs, r.ID)
		}
	}
	x.run.sources(toolSources(res.Sources))
	for _, p := range res.Parts {
		x.run.block(p)
	}
	// Tool images need vision set explicitly: a base model without a
	// capabilities row may be text-only, and its provider would reject the run.
	vision := x.m.Capabilities["vision"] == true
	stepFiles := refs
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
	content := res.Content
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

// toolCall builds the provider-facing Call.
func (e *Engine) toolCall(x *exec, c provider.ToolCall) tools.Call {
	run := x.run
	chatID := run.ChatID
	if x.noChat {
		chatID = ""
	}
	tok, _ := signRunToken(e.Keys, run.ID, run.UserID, time.Now().Add(15*time.Minute))
	return tools.Call{ID: c.ID, Name: c.Name, Args: c.Arguments, User: x.u, ModelID: x.m.ID, ChatID: chatID,
		MessageID: run.MessageID, RunID: run.ID, FileIDs: x.fileIDs,
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
		out = append(out, Source{Name: s.Name, URL: s.URL, FileID: s.FileID, Page: s.Page, Snippet: s.Snippet})
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
