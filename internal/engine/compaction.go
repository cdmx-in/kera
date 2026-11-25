package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/provider"
)

// estimateTokens is a conservative chars/4 estimate of the conversation.
// ponytail: no tokenizer; thresholds are set with headroom instead.
func estimateTokens(ts []turn) int {
	n := 0
	for _, t := range ts {
		n += len(t.msg.Content)/4 + 4
	}
	return n
}

// overheadTokens estimates what every request carries besides the turns: the system prompt and the tool definitions.
func (x *exec) overheadTokens(specs []provider.Tool) int {
	b, _ := json.Marshal(specs)
	return (len(strings.Join(x.system, "\n\n")) + len(x.data) + len(b)) / 4
}

// compactionThreshold: the model's params.compact_token_threshold (or compaction_threshold) wins; then,
// for a model whose window is known (params.context_window or num_ctx, or a local model's assumed
// window), chat.context.clear_at of that window, the point where the loop starts clearing too;
// else chat.compaction.threshold.
func (e *Engine) compactionThreshold(ctx context.Context, x *exec) int {
	for _, k := range []string{"compact_token_threshold", "compaction_threshold"} {
		if n := intParam(x.params[k]); n > 0 {
			return n
		}
	}
	if win, src := e.contextWindow(ctx, x); src != "setting" || x.m.Local {
		at := 0.7
		e.setting(ctx, "chat.context.clear_at", &at)
		return int(at * float64(win))
	}
	n := 120000
	e.setting(ctx, "chat.compaction.threshold", &n)
	return n
}

// splitForCompaction returns how many leading turns to summarise: all but
// the newest retention percent, never the last user message, and only when
// the conversation is over the threshold and at least 4 turns long.
// overhead is the estimated size of the system prompt and tool definitions, which count against the threshold.
func splitForCompaction(ts []turn, threshold, retention, overhead int) int {
	if threshold <= 0 || len(ts) < 4 || estimateTokens(ts)+overhead <= threshold {
		return 0
	}
	keep := max(len(ts)*retention/100, 1)
	cut := len(ts) - keep
	if last := lastUser(ts); cut > last {
		cut = last
	}
	if cut < 2 {
		return 0
	}
	return cut
}

// compact summarises older turns when the conversation is too long. The
// summary is stored on the last summarised message, so later turns start
// from it; the UI gets a compaction part (hidden unless the user opts in).
func (e *Engine) compact(ctx context.Context, x *exec, overhead int) error {
	if x.chat == nil || !e.Settings.Bool(ctx, "chat.compaction.enable") {
		return nil
	}
	retention := 30
	e.setting(ctx, "chat.compaction.retention", &retention)
	cut := splitForCompaction(x.turns, e.compactionThreshold(ctx, x), retention, overhead)
	if cut == 0 {
		return nil
	}
	lastID := ""
	for i := cut - 1; i >= 0 && lastID == ""; i-- {
		lastID = x.turns[i].ID
	}
	if lastID == "" {
		return nil // only synthetic turns (an earlier summary) to fold
	}
	before := estimateTokens(x.turns) + overhead
	started := time.Now()
	failed := ""
	defer func() {
		after := estimateTokens(x.turns) + overhead
		x.run.trace.add(traceStep{Kind: stepCompaction, Name: "summarise", Started: started, Duration: time.Since(started), Err: failed,
			Detail: map[string]any{"turns_summarised": cut, "tokens_before": before, "tokens_after": after}})
	}()
	x.run.compaction("running", "", 0, 0)
	var b strings.Builder
	for _, t := range x.turns[:cut] {
		fmt.Fprintf(&b, "### %s\n%s\n\n", t.msg.Role, t.msg.Content)
	}
	tm, err := e.taskModel(ctx, x)
	if err != nil {
		return err
	}
	summary, _, err := e.complete(ctx, tm, summaryPrompt, b.String())
	if err != nil || strings.TrimSpace(summary) == "" {
		slog.Warn("compaction failed, continuing with the full conversation", "err", err)
		failed = "compaction_failed"
		x.run.compaction("done", "", 0, 0)
		return nil
	}
	summary = strings.TrimSpace(summary)
	if err := e.Chats.SetContextSummary(ctx, x.chat.ID, lastID, summary); err != nil {
		return err
	}
	x.turns = append([]turn{{msg: provider.Message{Role: "system", Content: summaryBlock(summary)}}}, x.turns[cut:]...)
	x.run.compaction("done", summary, before, estimateTokens(x.turns)+overhead)
	return nil
}
