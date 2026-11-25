package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/tools"
)

// assumeWindow gives the test model the window Kera assumes for a model that declares none (a hosted model reads it from
// chat.compaction.threshold, a local one from chat.context.window_local: the same code path, the source is "setting").
func assumeWindow(t *testing.T, h *harness) {
	t.Helper()
	h.set(t, map[string]any{"chat.compaction.threshold": 16000})
}

// A small window, as a local server gives by default: 16,384 tokens, while Kera assumes 16,000. The fake server counts
// 3.3 characters per token (what markdown measured on a local model) or 2.8 (denser, like JSON and code), and answers
// HTTP 500 "no user query found in messages" to any request over the window, as Ollama does.
const smallWindow = 16384

var smallRatios = []float64{3.3, 2.8}

func toolMsg(id string, chars int) provider.Message {
	return provider.Message{Role: "tool", ToolCallID: id, Name: "t", Content: padded("r-"+id+" ", chars)}
}

func callMsg(ids ...string) provider.Message {
	m := provider.Message{Role: "assistant"}
	for _, id := range ids {
		m.ToolCalls = append(m.ToolCalls, provider.ToolCall{ID: id, Name: "t", Arguments: json.RawMessage(`{}`)})
	}
	return m
}

func slicesMax(v []int) int {
	out := 0
	for _, n := range v {
		out = max(out, n)
	}
	return out
}

// runError is the code and message of the run's error event.
func runError(run *Run) (code, msg string) {
	run.mu.Lock()
	defer run.mu.Unlock()
	for _, ev := range run.events {
		if ev.Type == "error" {
			code, _ = ev.Data["code"].(string)
			msg, _ = ev.Data["message"].(string)
		}
	}
	return code, msg
}

// C: the estimate follows what the server reports.
func TestCalibrateCharsPerToken(t *testing.T) {
	msgs := func(chars int) []provider.Message {
		return []provider.Message{{Role: "system", Content: strings.Repeat("s", chars/2)}, {Role: "user", Content: strings.Repeat("u", chars/2)}}
	}
	for _, c := range []struct {
		name   string
		chars  int
		prompt int
		before float64
		want   float64
	}{
		{"markdown measured on a local model", 52135, 15583 + 8, 0, 3.346},
		{"dense json and code", 28000, 10000 + 8, 0, 2.8},
		{"english prose is capped at the old four", 40000, 8000 + 8, 0, maxCharsPerToken},
		{"cjk text is floored", 9000, 9000 + 8, 0, minCharsPerToken},
		{"a small prompt says little: the default stays", 2000, 700, 0, 0},
		{"a server that counts only the uncached part is not trusted", 40000, 3000, 0, 0},
		{"a later report replaces an earlier one", 30000, 10000 + 8, 3.9, 3.0},
	} {
		b := &runBudget{cpt: c.before}
		b.calibrate(msgs(c.chars), c.prompt)
		if math.Abs(b.cpt-c.want) > 0.01 {
			t.Errorf("%s: chars per token %.3f, want %.3f", c.name, b.cpt, c.want)
		}
	}
	// the estimate for new messages uses it, and the default is 3.3, not 4
	b := &runBudget{}
	if got := b.tokens(3300); got != 1000 {
		t.Errorf("default: 3300 characters = %d tokens, want 1000", got)
	}
	b.cpt = 2.5
	if got, want := b.estimate([]provider.Message{{Content: strings.Repeat("x", 2500)}}), 1004; got != want {
		t.Errorf("calibrated estimate %d, want %d", got, want)
	}
	if got := estimateTokens([]turn{{msg: provider.Message{Content: strings.Repeat("x", 330)}}}); got != 104 {
		t.Errorf("between-turn estimate %d, want 104", got)
	}
}

// A: what one result may add.
func TestCallShare(t *testing.T) {
	// ceiling 0.85 x 16000 = 13600 tokens; 3.3 characters per token
	base := []provider.Message{{Role: "system", Content: strings.Repeat("s", 3300)}, {Role: "user", Content: "task"}}
	for _, c := range []struct {
		name   string
		manage bool
		window int
		extra  []provider.Message
		n      int
		min    int
		max    int
	}{
		{"unmanaged: no limit", false, 16000, nil, 3, 0, 0},
		{"one call gets the room", true, 16000, []provider.Message{callMsg("a")}, 1, 40000, 44000},
		{"seven calls share it", true, 16000, []provider.Message{callMsg("a", "b", "c", "d", "e", "f", "g")}, 7, 5700, 6500},
		{"earlier results are room: they can be cleared", true, 16000, []provider.Message{callMsg("x"), toolMsg("x", 30000), callMsg("a")}, 1, 40000, 44000},
		{"a small window still leaves something", true, 2000, []provider.Message{callMsg("a", "b")}, 2, minCallChars, minCallChars},
	} {
		b := &runBudget{manage: c.manage, window: c.window, base: len(base), spilled: map[string]string{}}
		got := b.callShare(append(slices.Clone(base), c.extra...), c.n)
		if got < c.min || got > c.max {
			t.Errorf("%s: share %d, want %d to %d", c.name, got, c.min, c.max)
		}
	}
}

// A: a page of view_file or a grep stops at the share the engine gives it, and says where to go on.
func TestFileToolsHonourMaxChars(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var b strings.Builder
	for l := 1; l <= 300; l++ {
		b.WriteString(padded(fmt.Sprintf("line %03d needle ", l), 99) + "\n")
	}
	f, err := h.e.Files.CreateText(ctx, "u1", "notes.txt", b.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ft := &filesTools{e: h.e}
	call := func(name, args string, maxChars int) string {
		res, err := ft.Execute(ctx, tools.Call{Name: name, Args: json.RawMessage(args), User: h.u, FileIDs: []string{f.ID}, MaxChars: maxChars})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content
	}
	for _, c := range []struct {
		maxChars int
		lines    int // lines of the page, at least 1
		more     string
	}{
		{0, 287, "continue with start_line 288"}, // no share: the page stops at 30,000 characters
		{3000, 28, "continue with start_line 29"},
		{1000, 9, "continue with start_line 10"},
		{50, 1, "continue with start_line 2"}, // never less than one line
	} {
		page := call("view_file", `{"file":"`+f.ID+`","max_lines":500}`, c.maxChars)
		if got := strings.Count(page, "needle"); got != c.lines || !strings.Contains(page, c.more) {
			t.Errorf("max %d: %d lines (want %d), tail %q", c.maxChars, got, c.lines, page[max(len(page)-60, 0):])
		}
		if c.maxChars >= 1000 && len(page) > c.maxChars+capSlack {
			t.Errorf("max %d: page of %d characters", c.maxChars, len(page))
		}
	}
	all := call("grep_chat_files", `{"pattern":"needle","max_results":200}`, 0)
	short := call("grep_chat_files", `{"pattern":"needle","max_results":200}`, 3000)
	if strings.Count(all, "needle") != 200 || len(short) > 3000+capSlack || !strings.Contains(short, "stopped after") || strings.Count(short, "needle") < 20 {
		t.Errorf("grep: %d matches unlimited, limited to %d characters: %q", strings.Count(all, "needle"), len(short), short[max(len(short)-120, 0):])
	}
}

func TestCutResult(t *testing.T) {
	long := strings.Repeat("0123456789", 300) // 3000
	once := cutResult(long, 1000)
	if len(once) > 1000 || !strings.HasPrefix(once, "0123456789") || !strings.Contains(once, "of 3000 characters shown") || !strings.HasSuffix(once, "narrower query.]") {
		t.Fatalf("cut once: %d characters, tail %q", len(once), once[max(len(once)-130, 0):])
	}
	if cutResult(long, 3000) != long || cutResult("short", 1000) != "short" {
		t.Fatal("a result that fits was changed")
	}
	// cut again: one note, the size of the original
	twice := cutResult(once, 600)
	if len(twice) > 600 || strings.Count(twice, cutHead) != 1 || !strings.Contains(twice, "of 3000 characters shown") {
		t.Fatalf("cut twice: %d characters, tail %q", len(twice), twice[max(len(twice)-130, 0):])
	}
	// never inside a character
	accents := strings.Repeat("é", 2000)
	if cut := cutResult(accents, 777); !utf8.ValidString(cut) || len(cut) > 777 {
		t.Fatalf("cut accents: valid %v, %d bytes", utf8.ValidString(cut), len(cut))
	}
}

// A: a result that is not spilled (here spilling is off) and is far over its share is cut with a note, a round of three
// shares the room, and the next prompt fits.
func TestResultsOverTheirShareAreCutWithANote(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	assumeWindow(t, h)
	h.set(t, map[string]any{"tools.spill_chars": -1, "web.search.max_calls_per_turn": 0})
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, features) VALUES ('web', 'ctx-m', '{"web_search": true}')`); err != nil {
		t.Fatal(err)
	}
	tools.Register(&textTool{id: "builtin:web", name: "search_web", body: func(n int) string { return padded(fmt.Sprintf("hit %d ", n), 60000) }})
	t.Cleanup(func() { tools.Register(inertSet("builtin:web")) })
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) {
		if n == 0 {
			s.calls([3]string{"s0", "search_web", `{"i":0}`}, [3]string{"s1", "search_web", `{"i":1}`}, [3]string{"s2", "search_web", `{"i":2}`})
			return
		}
		s.text("summary of three hits")
	})
	m.window, m.ratio = smallWindow, 3.3
	run := startModel(t, h, "web", &Request{Temporary: true, Features: map[string]bool{"web_search": true},
		Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"search"`)}}})
	if got := run.stateText(); got != "summary of three hits" || m.overflows != 0 {
		t.Fatalf("answer %q, %d refused calls, error %q", got, m.overflows, run.state.Error)
	}
	res := m.requests()[1].toolResults()
	for _, id := range []string{"s0", "s1", "s2"} {
		if r := res[id]; len(r) > 15000 || len(r) < 8000 || !strings.Contains(r, "of 60000 characters shown") || !strings.HasPrefix(r, "hit ") {
			t.Fatalf("result %s: %d characters, tail %q", id, len(r), r[max(len(r)-130, 0):])
		}
	}
	var capped int
	for _, s := range h.stepsOf(t, run.ID) {
		if s.Kind == "tool" && s.Detail["capped"] == true && s.Detail["result_chars"] == 60000.0 {
			capped++
		}
	}
	if capped != 3 {
		t.Fatalf("%d capped tool steps in the trace, want 3", capped)
	}
}
