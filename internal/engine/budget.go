package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cdmx-in/kera/internal/provider"
)

// Context management inside a run: the loop knows how full the model's window is, clears old
// tool results when it fills up and, when that is not enough, summarises the run so far. See
// docs/context-management.md.

// compactKeepRounds is how many of the newest rounds a mid-run summary leaves untouched.
// ponytail: a constant; make it a setting if a model needs more recent detail.
const compactKeepRounds = 2

// overflowAt is the share of the window at which a reported prompt size is treated as "the
// server probably cut the prompt": Ollama drops the oldest messages and still answers.
const overflowAt = 0.97

// runBudget is what the loop knows about the model's window during one run.
type runBudget struct {
	manage     bool    // size tracking, clearing and mid-run compaction are on
	window     int     // tokens
	source     string  // where the window came from: context_window, num_ctx or setting
	clearAt    float64 // share of the window above which old tool results are cleared
	clearTo    float64 // share the clearing stops at
	keepRounds int     // newest rounds whose results are never cleared
	compact    bool    // summarise when clearing is not enough
	spillAt    int     // characters above which a tool result becomes a file (0 = never)
	toolChars  int     // size of the tool definitions
	base       int     // index in the message list where this run's own messages start

	lastPrompt int // prompt tokens the server reported for the last call (or the estimate after a rewrite)
	lastLen    int // number of messages that call carried

	mu      sync.Mutex
	spilled map[string]string // tool call id -> "file name (id ...)" of the spilled full text
}

// newBudget reads the settings and the model's window. base is len(msgs) when the loop starts.
func (e *Engine) newBudget(ctx context.Context, x *exec, toolChars, base int, hasFileTool bool) *runBudget {
	b := &runBudget{manage: e.Settings.Bool(ctx, "chat.context.enable"), clearAt: 0.7, clearTo: 0.5, keepRounds: 4,
		compact: e.Settings.Bool(ctx, "chat.context.compact"), toolChars: toolChars, base: base, spilled: map[string]string{}}
	b.window, b.source = e.contextWindow(ctx, x)
	e.setting(ctx, "chat.context.clear_at", &b.clearAt)
	e.setting(ctx, "chat.context.clear_to", &b.clearTo)
	e.setting(ctx, "chat.context.keep_rounds", &b.keepRounds)
	b.keepRounds = max(b.keepRounds, 1)
	b.manage = b.manage && b.window > 0 && b.clearAt > 0 && b.clearTo < b.clearAt
	if hasFileTool { // a spilled result is read back with view_file
		spill := 0
		e.setting(ctx, "tools.spill_chars", &spill)
		switch {
		case spill > 0:
			b.spillAt = spill
		case spill == 0 && b.window > 0:
			b.spillAt = max(8000, b.window/2) // one eighth of the window, in characters
		}
	}
	return b
}

// contextWindow is the model's window in tokens: params.context_window, else params.num_ctx
// (the request's parameters win over the preset's), else a setting: chat.context.window_local
// for a local model, chat.compaction.threshold for a hosted one.
func (e *Engine) contextWindow(ctx context.Context, x *exec) (int, string) {
	for _, k := range []string{"context_window", "num_ctx"} {
		if n := intParam(x.params[k]); n > 0 {
			return n, k
		}
	}
	if x.m.Local {
		n := 16000
		e.setting(ctx, "chat.context.window_local", &n)
		return n, "setting"
	}
	n := 120000
	e.setting(ctx, "chat.compaction.threshold", &n)
	return n, "setting"
}

func intParam(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// msgChars counts what a message adds to the prompt, in characters.
func msgChars(m provider.Message) int {
	n := len(m.Content)
	for _, c := range m.ToolCalls {
		n += len(c.Name) + len(c.Arguments)
	}
	return n
}

// charsTokens is the chars/4 estimate with a few tokens per message for the framing.
// ponytail: no tokenizer; JSON and non-English text run closer to 3 characters per token, so the thresholds leave headroom.
func charsTokens(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		n += msgChars(m)/4 + 4
	}
	return n
}

// size estimates the prompt tokens of msgs: what the server reported for the last call plus an
// estimate for the messages added since, or an estimate of everything before the first report
// and after a rewrite. A reported size under half the estimate is not trusted (a server that
// counts only the uncached part of the prompt).
func (b *runBudget) size(msgs []provider.Message) int {
	all := charsTokens(msgs) + b.toolChars/4
	if b.lastPrompt == 0 || b.lastLen > len(msgs) {
		return all
	}
	n := b.lastPrompt + charsTokens(msgs[b.lastLen:])
	if n*2 < all {
		return all
	}
	return n
}

// rebase records an estimate as the new baseline after the messages were rewritten.
func (b *runBudget) rebase(tokens, msgs int) { b.lastPrompt, b.lastLen = tokens, msgs }

// observe takes the prompt size a call reported and notes a suspected overflow in the trace.
func (b *runBudget) observe(x *exec, round, msgs, prompt int) {
	b.lastPrompt, b.lastLen = prompt, msgs
	if b.window <= 0 || float64(prompt) < overflowAt*float64(b.window) {
		return
	}
	slog.Warn("prompt at the context window, the server may have cut it", "run", x.run.ID, "model", x.m.ID, "prompt_tokens", prompt, "window", b.window)
	x.run.trace.add(traceStep{Kind: stepCompaction, Name: "overflow_suspected", Round: round, Started: time.Now(),
		Detail: map[string]any{"prompt_tokens": prompt, "window": b.window, "window_source": b.source}})
}

// span is one round of the run: an assistant message with tool calls and what follows it up to the next such message.
type span struct{ from, to int }

// rounds lists the run's own rounds, oldest first.
func rounds(msgs []provider.Message, base int) []span {
	var out []span
	for i := base; i < len(msgs); i++ {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			if n := len(out); n > 0 {
				out[n-1].to = i
			}
			out = append(out, span{from: i})
		}
	}
	if n := len(out); n > 0 {
		out[n-1].to = len(msgs)
	}
	return out
}

const clearedPrefix = "[cleared to save context"

// clearedNote replaces a tool result that was cleared.
func (b *runBudget) clearedNote(m provider.Message, chars int) string {
	note := fmt.Sprintf("%s: %s returned %d characters]", clearedPrefix, m.Name, chars)
	b.mu.Lock()
	f := b.spilled[m.ToolCallID]
	b.mu.Unlock()
	if f != "" {
		note += fmt.Sprintf(" The full text is in file %s; read it with view_file or grep_chat_files.", f)
	}
	return note
}

// fit keeps the next request under the window: when the estimate passes clear_at it clears old tool
// results down to clear_to; when that is not enough it summarises the run so far. The first
// messages (system, earlier turns, the user's request) are never touched.
func (e *Engine) fit(ctx context.Context, x *exec, b *runBudget, msgs []provider.Message, round int) []provider.Message {
	if !b.manage {
		return msgs
	}
	limit := int(b.clearAt * float64(b.window))
	if b.size(msgs) <= limit {
		return msgs
	}
	msgs = e.clearOld(x, b, msgs, round, b.keepRounds)
	if b.size(msgs) <= limit || !b.compact {
		return msgs
	}
	return e.compactRun(ctx, x, b, msgs, round)
}

// clearOld replaces the content of tool messages older than the newest keep rounds, oldest first,
// until the estimate is down to clear_to of the window. Every tool call keeps its result message.
func (e *Engine) clearOld(x *exec, b *runBudget, msgs []provider.Message, round, keep int) []provider.Message {
	rs := rounds(msgs, b.base)
	if len(rs) <= keep {
		return msgs
	}
	started := time.Now()
	before := b.size(msgs)
	size, target := before, int(b.clearTo*float64(b.window))
	out := append([]provider.Message(nil), msgs...)
	cleared := 0
	for _, r := range rs[:len(rs)-keep] {
		for i := r.from; i < r.to && size > target; i++ {
			m := out[i]
			if m.Role != "tool" || strings.HasPrefix(m.Content, clearedPrefix) {
				continue
			}
			note := b.clearedNote(m, utf8.RuneCountInString(m.Content))
			if len(note) >= len(m.Content) {
				continue
			}
			size -= (len(m.Content) - len(note)) / 4
			out[i].Content = note
			cleared++
		}
	}
	if cleared == 0 {
		return msgs
	}
	b.rebase(size, len(out))
	x.run.trace.add(traceStep{Kind: stepCompaction, Name: "clear", Round: round, Started: started, Duration: time.Since(started),
		Detail: map[string]any{"results_cleared": cleared, "tokens_before": before, "tokens_after": size, "window": b.window, "window_source": b.source}})
	return out
}

// runSummaryPrompt is the existing summary prompt with what a run in progress needs kept.
const runSummaryPrompt = summaryPrompt + " The conversation is the work done so far on the user's latest request: " +
	"also keep what was found, what was already tried, what is still to do and the names of the files that were read."

// compactRun summarises the run's messages before the newest compactKeepRounds rounds with the task model
// and puts the summary in their place. When the summary fails it clears harder instead and goes on.
func (e *Engine) compactRun(ctx context.Context, x *exec, b *runBudget, msgs []provider.Message, round int) []provider.Message {
	rs := rounds(msgs, b.base)
	if len(rs) <= compactKeepRounds {
		return msgs
	}
	cut := rs[len(rs)-compactKeepRounds].from
	started := time.Now()
	before := b.size(msgs)
	x.run.compaction("running", "", 0, 0)
	summary, err := e.summariseRun(ctx, x, b, msgs[b.base:cut])
	step := traceStep{Kind: stepCompaction, Name: "summarise_run", Round: round, Started: started,
		Detail: map[string]any{"messages_summarised": cut - b.base, "tokens_before": before, "window": b.window, "window_source": b.source}}
	if err != nil {
		slog.Warn("mid-run compaction failed, clearing instead", "run", x.run.ID, "err", err)
		step.Err, step.Duration = "compaction_failed", time.Since(started)
		x.run.compaction("done", "", 0, 0)
		out := e.clearOld(x, b, msgs, round, compactKeepRounds)
		step.Detail["tokens_after"] = b.size(out)
		x.run.trace.add(step)
		return out
	}
	files := b.filesIn(msgs[b.base:cut])
	out := append(append([]provider.Message(nil), msgs[:b.base]...), provider.Message{Role: "user", Data: true, Content: runSummaryBlock(summary, files)})
	out = append(out, msgs[cut:]...)
	after := charsTokens(out) + b.toolChars/4
	b.rebase(after, len(out))
	step.Duration = time.Since(started)
	step.Detail["tokens_after"] = after
	x.run.trace.add(step)
	x.run.compaction("done", summary, before, after)
	return out
}

// summariseRun asks the task model for notes on the run's messages.
func (e *Engine) summariseRun(ctx context.Context, x *exec, b *runBudget, msgs []provider.Message) (string, error) {
	tm, err := e.taskModel(ctx, x)
	if err != nil {
		return "", err
	}
	summary, _, err := e.complete(ctx, tm, runSummaryPrompt, runTranscript(msgs, b.window*2))
	if err != nil {
		return "", fmt.Errorf("summarise run: %w", err)
	}
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("summarise run: empty summary")
	}
	return strings.TrimSpace(summary), nil
}

// runTranscript renders messages for the summary call, each one clipped, at most limit characters (the newest kept).
func runTranscript(msgs []provider.Message, limit int) string {
	per := min(max(limit/max(len(msgs), 1), 300), 2000)
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "### %s\n%s\n", m.Role, clip(m.Content, per))
		for _, c := range m.ToolCalls {
			fmt.Fprintf(&b, "call %s %s\n", c.Name, clip(string(c.Arguments), 300))
		}
		b.WriteString("\n")
	}
	s := b.String()
	if len(s) > limit {
		cut := len(s) - limit
		for cut < len(s) && s[cut]&0xC0 == 0x80 {
			cut++
		}
		s = "[earlier part left out]\n" + s[cut:]
	}
	return s
}

// filesIn lists the spilled files whose results are among msgs, so the summary still points to them.
func (b *runBudget) filesIn(msgs []provider.Message) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, m := range msgs {
		if f := b.spilled[m.ToolCallID]; m.Role == "tool" && f != "" && !containsStr(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func runSummaryBlock(summary string, files []string) string {
	s := "Summary of the work done so far in this run (the earlier tool calls and results were condensed to save space):\n\n" + summary
	if len(files) > 0 {
		s += "\n\nFull tool results are stored in these files; read them with view_file or grep_chat_files: " + strings.Join(files, ", ")
	}
	return s
}
