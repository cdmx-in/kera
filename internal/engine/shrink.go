package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/provider"
)

// The guard before a model call. fit (budget.go) clears and summarises at clear_at, which keeps a run
// healthy; guard is what makes sure the call that goes out fits the window, whatever the rounds before
// it did: it shortens what is there, oldest and largest first, and says so in the trace.

// errNothingToShrink is what afterOverflow returns when the messages cannot get any shorter.
var errNothingToShrink = errors.New("nothing left to shorten")

// guard brings the estimate under the ceiling before a model call: it clears the results of earlier rounds largest
// first (the newest round is the one the model has not read yet), then cuts the largest results of any round to a
// common length with a note, then summarises the run (unless fit just did). A prompt still over failShare of the
// window after that is not sent when the window is known; the error says why.
func (e *Engine) guard(ctx context.Context, x *exec, b *runBudget, msgs []provider.Message, round int, summarised bool) ([]provider.Message, error) {
	ceil := b.ceiling()
	if b.size(msgs) <= ceil {
		return msgs, nil
	}
	msgs = e.clearRecent(x, b, msgs, round, ceil)
	msgs = e.truncateLargest(x, b, msgs, round, ceil)
	if b.size(msgs) > ceil && b.compact && !summarised {
		msgs = e.compactRun(ctx, x, b, msgs, round)
		msgs = e.truncateLargest(x, b, msgs, round, ceil)
	}
	if n := b.size(msgs); n > int(failShare*float64(b.window)) {
		return msgs, e.tooLarge(x, b, round, n)
	}
	return msgs, nil
}

// tooLarge is the outcome of a prompt that nothing can shorten any more. With a window the model declared (context_window,
// num_ctx) the call would only fail, so the run ends with an error that says what to change. A window that is only the
// default is a guess (the model may have more), and one learned from a refusal is a guess too: the call goes out and the
// server's answer settles it.
func (e *Engine) tooLarge(x *exec, b *runBudget, round, tokens int) error {
	step := traceStep{Kind: stepCompaction, Name: "too_large", Round: round, Started: time.Now(),
		Detail: map[string]any{"tokens_before": tokens, "tokens_after": tokens, "window": b.window, "window_source": b.source}}
	if b.source == "setting" || b.source == "server" {
		slog.Warn("prompt over the assumed window, nothing left to shorten, sending it", "run", x.run.ID, "model", x.m.ID, "tokens", tokens, "window", b.window)
		step.Detail["sent"] = true
		x.run.trace.add(step)
		return nil
	}
	step.Err = "context_length_exceeded"
	x.run.trace.add(step)
	return api.Errf(http.StatusRequestEntityTooLarge, "context_length_exceeded", fmt.Sprintf(
		"The instructions, tools, attachments and your message take about %d tokens, more than the model's context window of %d, and nothing more can be shortened. "+
			"Use a model with a larger window, or shorten the message or the attached text.", tokens, b.window))
}

// afterOverflow shortens msgs after the server refused them as too long although the estimate said they fit. The
// window is taken as a quarter smaller than that estimate from now on (the model has fewer tokens than assumed, or this
// text counts higher, source "server"), and fit runs again with it. It returns errNothingToShrink when nothing got
// shorter, because the same prompt would fail again.
func (e *Engine) afterOverflow(ctx context.Context, x *exec, b *runBudget, msgs []provider.Message, round int, cause error) ([]provider.Message, error) {
	started := time.Now()
	before, was := b.size(msgs), b.window
	b.window, b.source = min(b.window, max(before*3/4, 1024)), "server"
	out, err := e.fit(ctx, x, b, msgs, round)
	after := b.size(out)
	detail := map[string]any{"tokens_before": before, "tokens_after": after, "window_before": was, "window_after": b.window}
	var he *provider.HTTPError
	if errors.As(cause, &he) {
		detail["http_status"] = he.Status
	}
	step := traceStep{Kind: stepCompaction, Name: "overflow_retry", Round: round, Started: started, Duration: time.Since(started), Detail: detail}
	switch {
	case err != nil:
		step.Err = "context_length_exceeded"
	case after >= before:
		step.Err, err = "nothing_to_shrink", errNothingToShrink
	}
	x.run.trace.add(step)
	if err != nil {
		return msgs, err
	}
	slog.Warn("the server refused the prompt as too long, shortened it and trying once more", "run", x.run.ID, "model", x.m.ID, "tokens_before", before, "tokens_after", after)
	return out, nil
}

// clearIdx replaces the content of the tool results at idx, in that order, by their cleared notes until the estimate is
// down to target. scope tells the trace which results were offered: "old" (older than the kept rounds) or "recent".
func (e *Engine) clearIdx(x *exec, b *runBudget, msgs []provider.Message, round int, idx []int, target int, scope string) []provider.Message {
	started := time.Now()
	before := b.size(msgs)
	size := before
	out := slices.Clone(msgs)
	cleared := 0
	for _, i := range idx {
		if size <= target {
			break
		}
		m := out[i]
		if m.Role != "tool" || strings.HasPrefix(m.Content, clearedPrefix) {
			continue
		}
		note := b.clearedNote(m, utf8.RuneCountInString(m.Content))
		if len(note) >= len(m.Content) {
			continue
		}
		size -= b.tokens(len(m.Content) - len(note))
		out[i].Content = note
		cleared++
	}
	if cleared == 0 {
		return msgs
	}
	b.rebase(size, len(out))
	x.run.trace.add(traceStep{Kind: stepCompaction, Name: "clear", Round: round, Started: started, Duration: time.Since(started),
		Detail: map[string]any{"results_cleared": cleared, "scope": scope, "tokens_before": before, "tokens_after": size, "window": b.window, "window_source": b.source}})
	return out
}

// clearRecent clears the results that clearOld kept for being recent, largest first, until the estimate is down to
// target: those of every round but the newest. Counting rounds protects nothing when a few rounds fill the window.
func (e *Engine) clearRecent(x *exec, b *runBudget, msgs []provider.Message, round, target int) []provider.Message {
	rs := rounds(msgs, b.base)
	if len(rs) < 2 || b.size(msgs) <= target {
		return msgs
	}
	var idx []int
	for _, r := range rs[:len(rs)-1] {
		for i := r.from; i < r.to; i++ {
			idx = append(idx, i)
		}
	}
	slices.SortStableFunc(idx, func(a, c int) int { return cmp.Compare(len(msgs[c].Content), len(msgs[a].Content)) })
	return e.clearIdx(x, b, msgs, round, idx, target, "recent")
}

// minKeep is the least a cut tool result keeps, note included.
const minKeep = 500

// truncateLargest cuts the largest tool results, whatever round they are from, to one common length (the longest
// first, so small results stay whole) until the estimate is down to target. Each cut result ends with a note saying
// how much was shown. The messages the run started with are never touched.
func (e *Engine) truncateLargest(x *exec, b *runBudget, msgs []provider.Message, round, target int) []provider.Message {
	before := b.size(msgs)
	if before <= target {
		return msgs
	}
	var idx, lens []int
	for i := b.base; i < len(msgs); i++ {
		if m := msgs[i]; m.Role == "tool" && !strings.HasPrefix(m.Content, clearedPrefix) && len(m.Content) > minKeep {
			idx = append(idx, i)
		}
	}
	slices.SortStableFunc(idx, func(a, c int) int { return cmp.Compare(len(msgs[c].Content), len(msgs[a].Content)) })
	for _, i := range idx {
		lens = append(lens, len(msgs[i].Content))
	}
	level := levelFor(lens, b.chars(before-target)+24*len(idx), minKeep) // 24 each: the note's digits and the rounding of the estimate
	started := time.Now()
	out := slices.Clone(msgs)
	size, cut := before, 0
	for _, i := range idx {
		if len(out[i].Content) <= level {
			break // sorted: the rest is shorter
		}
		short := cutResult(out[i].Content, level)
		size -= b.tokens(len(out[i].Content) - len(short))
		out[i].Content = short
		cut++
	}
	if cut == 0 {
		return msgs
	}
	b.rebase(size, len(out))
	x.run.trace.add(traceStep{Kind: stepCompaction, Name: "truncate", Round: round, Started: started, Duration: time.Since(started),
		Detail: map[string]any{"results_truncated": cut, "kept_chars": level, "tokens_before": before, "tokens_after": size, "window": b.window, "window_source": b.source}})
	return out
}

// levelFor is the length the longest of lens (sorted longest first) must be cut to so that need characters go, and
// never less than floor: the top k are cut to the same level, the others stay as they are.
func levelFor(lens []int, need, floor int) int {
	sum := 0
	for k, n := range lens {
		sum += n
		next := floor
		if k+1 < len(lens) {
			next = max(lens[k+1], floor)
		}
		if sum-(k+1)*next >= need {
			return max((sum-need)/(k+1), next)
		}
	}
	return floor
}
