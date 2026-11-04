package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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

// compactionThreshold: the model's params.compact_token_threshold (or
// compaction_threshold) wins over chat.compaction.threshold.
func (e *Engine) compactionThreshold(ctx context.Context, x *exec) int {
	for _, k := range []string{"compact_token_threshold", "compaction_threshold"} {
		switch v := x.m.Params[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
	}
	n := 120000
	e.setting(ctx, "chat.compaction.threshold", &n)
	return n
}

// splitForCompaction returns how many leading turns to summarise: all but
// the newest retention percent, never the last user message, and only when
// the conversation is over the threshold and at least 4 turns long.
func splitForCompaction(ts []turn, threshold, retention int) int {
	if threshold <= 0 || len(ts) < 4 || estimateTokens(ts) <= threshold {
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
func (e *Engine) compact(ctx context.Context, x *exec) error {
	if x.chat == nil || !e.Settings.Bool(ctx, "chat.compaction.enable") {
		return nil
	}
	retention := 30
	e.setting(ctx, "chat.compaction.retention", &retention)
	cut := splitForCompaction(x.turns, e.compactionThreshold(ctx, x), retention)
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
	before := estimateTokens(x.turns)
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
		x.run.compaction("done", "", 0, 0)
		return nil
	}
	summary = strings.TrimSpace(summary)
	if err := e.Chats.SetContextSummary(ctx, x.chat.ID, lastID, summary); err != nil {
		return err
	}
	x.turns = append([]turn{{msg: provider.Message{Role: "system", Content: summaryBlock(summary)}}}, x.turns[cut:]...)
	x.run.compaction("done", summary, before, estimateTokens(x.turns))
	return nil
}
