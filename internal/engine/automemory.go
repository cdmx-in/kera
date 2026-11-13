package engine

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/cdmx-in/kera/internal/memory"
)

// Automatic memory: every chat.memory.review_every-th user message of a saved chat, a
// background call reads what the user wrote and stores lasting facts about them. The
// switches are the manual memory tools' own (builtinAllowed): the admin setting, the
// user's opt-in, features.memory, the model's memory flag and chat.memory.local_only.
const reviewPrompt = `You keep a short list of lasting facts about a user, so an assistant can help them better in later chats.
Read the user's recent messages. List new facts worth remembering for months: their role, team, tools, and standing preferences about how answers should look.
Write each as one short sentence about the user ("Works in the finance team."). Leave out anything that only matters for the current task, anything already known, guesses, and anything sensitive: passwords, keys, card or identity numbers, health, and details about other people.
If there is nothing, return an empty list. Reply with JSON only: {"facts": ["..."]}`

const (
	maxAutoFacts     = 2   // stored per review
	maxFactChars     = 300 // a longer "fact" is a paragraph of the current task
	maxMemoriesTotal = 200 // per user: reviews stop adding above this
	reviewWindow     = 6   // the user's latest messages the review reads
	knownShown       = 100 // memories listed to the model so it skips them
)

// maybeReviewMemory starts a review when this answer completes a review interval.
// It runs after the run is closed, so it never delays the answer.
func (e *Engine) maybeReviewMemory(x *exec) {
	if e.Memory == nil || x.chat == nil || !x.interactive || x.cowork() {
		return // temporary and API runs, bots and tasks are not conversations with the person
	}
	ctx := context.Background()
	every := 5
	e.setting(ctx, "chat.memory.review_every", &every)
	if every <= 0 || !e.builtinAllowed(ctx, x, "builtin:memory") {
		return
	}
	var n int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM message WHERE chat_id = $1 AND role = 'user'`, x.chat.ID).Scan(&n); err != nil || n == 0 || n%every != 0 {
		return
	}
	// ponytail: one review at a time, so reviews never queue on the GPU the chats share; a skipped
	// review is not retried, the next interval covers the same messages (the window is wider).
	if !e.reviewMu.TryLock() {
		return
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer e.reviewMu.Unlock()
		e.reviewMemory(x)
	}()
}

func (e *Engine) reviewMemory(x *exec) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	known, err := e.Memory.List(ctx, x.u.ID)
	if err != nil || len(known) >= maxMemoriesTotal {
		return
	}
	// what the user typed, from the stored messages: not tool or file text, not what a hook added
	rows, err := e.DB.Query(ctx, `SELECT content FROM message WHERE chat_id = $1 AND role = 'user' AND content <> ''
		ORDER BY created_at DESC, id DESC LIMIT $2`, x.chat.ID, reviewWindow)
	if err != nil {
		slog.Warn("memory review: read messages", "err", err)
		return
	}
	var said []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return
		}
		said = append(said, clip(c, 1500))
	}
	rows.Close()
	slices.Reverse(said)
	tm, err := e.taskModel(ctx, x)
	if err != nil {
		return
	}
	var out struct {
		Facts []string `json:"facts"`
	}
	if err := e.completeJSON(ctx, tm, reviewPrompt, reviewInput(known, said), &out); err != nil {
		slog.Warn("memory review", "err", err)
		return
	}
	for _, f := range newFacts(out.Facts, known) {
		if _, err := e.Memory.Add(ctx, x.u.ID, f); err != nil {
			slog.Warn("memory review: save", "err", err)
		}
	}
}

func reviewInput(known []memory.Memory, said []string) string {
	var b strings.Builder
	b.WriteString("Already known:\n")
	if len(known) == 0 {
		b.WriteString("(nothing)\n")
	}
	for i, m := range known {
		if i == knownShown {
			break
		}
		fmt.Fprintf(&b, "- %s\n", clip(m.Content, 200))
	}
	b.WriteString("\nThe user's recent messages, oldest first:\n")
	for _, s := range said {
		fmt.Fprintf(&b, "### user\n%s\n\n", s)
	}
	return b.String()
}

// newFacts keeps up to maxAutoFacts facts that are short enough and not already known or repeated
// (compared ignoring case, spaces and punctuation).
func newFacts(candidates []string, known []memory.Memory) []string {
	seen := map[string]bool{}
	for _, m := range known {
		seen[factKey(m.Content)] = true
	}
	var out []string
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		k := factKey(c)
		if k == "" || len([]rune(c)) > maxFactChars || seen[k] || len(out) == maxAutoFacts {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

func factKey(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}
