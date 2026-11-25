package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/tools"
)

// A tool result longer than the budget's spill size is stored as a file of the user and the model gets its
// head, its tail and one line saying where the rest is. view_file and grep_chat_files read the file, in this
// run (the file joins the run's readable files) and in later turns (it is linked to the chat).

const (
	spillHead = 2000 // characters of the result the model still sees at the start
	spillTail = 1000 // ... and at the end
	spillWrap = 500  // longer lines are wrapped when stored: view_file clips a line at 2,000 and grep at 500
)

// spillSkip are the tools whose results are never spilled: the file tools (a spilled file read back would spill again).
var spillSkip = map[string]bool{"view_file": true, "grep_chat_files": true, "list_chat_files": true}

// spilled is a result stored as a file.
type spilled struct{ id, name string }

// ponytail: spilled files outlive the chat (deleting a chat only drops its links, like tool uploads); clean up by
// meta.source = "spill" if storage matters.

// spill stores res.Content as a file when it is too long and returns the text the model reads instead.
// ok is false when nothing was spilled (short result, error, spilling off or the file could not be stored).
func (e *Engine) spill(ctx context.Context, x *exec, b *runBudget, c provider.ToolCall, res tools.Result, round int) (text string, f spilled, ok bool) {
	if b.spillAt <= 0 || res.IsError || spillSkip[c.Name] {
		return "", f, false
	}
	n := utf8.RuneCountInString(res.Content)
	if n <= b.spillAt {
		return "", f, false
	}
	started := time.Now()
	id := ids.New()
	name := fmt.Sprintf("%s-result-%s.txt", safeName(c.Name), id[max(len(id)-8, 0):])
	meta := map[string]any{"source": "spill", "run_id": x.run.ID, "chat_id": x.run.ChatID, "message_id": x.run.MessageID, "tool": c.Name}
	file, err := e.Files.CreateText(ctx, x.u.ID, name, wrapLines(res.Content, spillWrap), meta)
	if err != nil {
		slog.Warn("spill tool result", "run", x.run.ID, "tool", c.Name, "err", err)
		return "", f, false
	}
	if x.chat != nil { // later turns of the chat can list and read it
		if err := e.Chats.LinkFiles(ctx, x.chat.ID, x.run.MessageID, []string{file.ID}); err != nil {
			slog.Warn("link spilled file", "run", x.run.ID, "file", file.ID, "err", err)
		}
	}
	f = spilled{id: file.ID, name: file.Filename}
	b.mu.Lock()
	b.spilled[c.ID] = fmt.Sprintf("%q (id %s)", f.name, f.id)
	b.mu.Unlock()
	head, tail := runeHead(res.Content, spillHead), runeTail(res.Content, spillTail)
	text = fmt.Sprintf("[Result too long for the prompt: %d characters. The full text is stored in file %q (id %s); read the rest with view_file (start_line, max_lines) or search it with grep_chat_files. The start and the end are shown here.]\n%s\n[... %d characters not shown ...]\n%s",
		n, f.name, f.id, head, max(n-spillHead-spillTail, 0), tail)
	x.run.trace.add(traceStep{Kind: stepCompaction, Name: "spill", Round: round, Started: started, Duration: time.Since(started),
		Detail: map[string]any{"tool": c.Name, "result_chars": n, "kept_chars": utf8.RuneCountInString(text), "threshold": b.spillAt, "file": f.id}})
	return text, f, true
}

// safeName keeps a tool name usable in a file name.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, s)
}

// runeHead is the first n characters of s.
func runeHead(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for k := 0; k < n; k++ {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i]
}

// runeTail is the last n characters of s.
func runeTail(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := len(s)
	for k := 0; k < n; k++ {
		_, w := utf8.DecodeLastRuneInString(s[:i])
		i -= w
	}
	return s[i:]
}

// wrapLines breaks lines longer than width characters (at a space when one is near the end) so a line
// reader that clips long lines still shows every character.
func wrapLines(s string, width int) string {
	if len(s) <= width && !strings.Contains(s, "\r") {
		return s
	}
	var b strings.Builder
	for i, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		r := []rune(line)
		for len(r) > width {
			cut := width
			for k := width; k > width*4/5; k-- { // prefer a space in the last fifth
				if r[k-1] == ' ' {
					cut = k
					break
				}
			}
			b.WriteString(string(r[:cut]))
			b.WriteByte('\n')
			r = r[cut:]
		}
		b.WriteString(string(r))
	}
	return b.String()
}
