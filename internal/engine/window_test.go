package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/tools"
)

// A small window, as a local server gives by default: 16,384 tokens, while Kera assumes 16,000. The fake server counts
// 3.3 characters per token (what markdown measured on a local model) or 2.8 (denser, like JSON and code), and answers
// HTTP 500 "no user query found in messages" to any request over the window, as Ollama does.
const smallWindow = 16384

var smallRatios = []float64{3.3, 2.8}

// assumeWindow gives the test model the window Kera assumes for a model that declares none (a hosted model reads it from
// chat.compaction.threshold, a local one from chat.context.window_local: the same code path, the source is "setting").
func assumeWindow(t *testing.T, h *harness) {
	t.Helper()
	h.set(t, map[string]any{"chat.compaction.threshold": 16000})
}

var lastNumbered = regexp.MustCompile(`(?m)^(\d+): `)

// nextLine is where a model continues reading after a page: the last line number it can see, plus one.
func nextLine(page string) int {
	last := 0
	for _, m := range lastNumbered.FindAllStringSubmatch(page, -1) {
		last, _ = strconv.Atoi(m[1])
	}
	return last + 1
}

// A round of 7 view_file calls, each a page of 30,000 characters, used to put 210,000 characters into the next prompt.
// The pages now share what is left of the window, say where to go on, and the run finishes without one refused call.
func TestSevenParallelFilePagesStayInsideASmallWindow(t *testing.T) {
	for _, ratio := range smallRatios {
		t.Run(fmt.Sprint(ratio, "_chars_per_token"), func(t *testing.T) { sevenParallelFilePages(t, ratio) })
	}
}

func sevenParallelFilePages(t *testing.T, ratio float64) {
	h := newHarness(t)
	ctx := context.Background()
	assumeWindow(t, h)
	tools.Register(&filesTools{e: h.e})
	t.Cleanup(func() { tools.Register(inertSet("builtin:files")) })
	var ids []string
	var attach []FileAttach
	for i := range 7 {
		var b strings.Builder
		for l := 1; l <= 400; l++ {
			b.WriteString(padded(fmt.Sprintf("file %d line %03d ", i, l), 99) + "\n")
		}
		f, err := h.e.Files.CreateText(ctx, "u1", fmt.Sprintf("doc-%d.txt", i), b.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
		attach = append(attach, FileAttach{Type: "file", ID: f.ID})
	}
	const rounds = 3
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) {
		if n >= rounds {
			s.text("table done")
			return
		}
		res := r.toolResults()
		var cs [][3]string
		for i, id := range ids {
			start := 1
			if n > 0 {
				start = nextLine(res[fmt.Sprintf("p%d-%d", n-1, i)])
			}
			cs = append(cs, [3]string{fmt.Sprintf("p%d-%d", n, i), "view_file", fmt.Sprintf(`{"file":%q,"start_line":%d,"max_lines":500}`, id, start)})
		}
		s.calls(cs...)
	})
	m.window, m.ratio = smallWindow, ratio
	run := h.startCtx(t, &Request{Temporary: true, Files: attach})
	reqs := m.requests()
	if got := run.stateText(); got != "table done" {
		t.Fatalf("answer %q after %d requests (%d refused for their size); run error %q", got, len(reqs), m.overflows, run.state.Error)
	}
	if m.overflows != 0 {
		t.Fatalf("%d calls were sent over the window", m.overflows)
	}
	if len(reqs) != rounds+1 {
		t.Fatalf("%d model requests, want %d", len(reqs), rounds+1)
	}
	for n, r := range reqs {
		assertPaired(t, n, r)
	}
	first := reqs[1].toolResults()
	for i := range ids {
		page := first[fmt.Sprintf("p0-%d", i)]
		if !strings.Contains(page, fmt.Sprintf("file %d line 001", i)) || !strings.Contains(page, "continue with start_line") {
			t.Fatalf("page %d: %d characters, head %q, tail %q", i, len(page), clip(page, 50), page[max(len(page)-120, 0):])
		}
	}
	if big := len(first["p0-0"]); big > 12000 {
		t.Fatalf("a page of one of 7 parallel calls is %d characters", big)
	}
	t.Logf("%d requests, peak %d of %d tokens", len(reqs), slicesMax(m.sizes), smallWindow)
}

func slicesMax(v []int) int {
	out := 0
	for _, n := range v {
		out = max(out, n)
	}
	return out
}

// Many large knowledge results: each is spilled to a file, but the pointers and excerpts of 20 results still add up to
// more than the window, and the model reads the files back. Old results must go, and no call may exceed the window.
func TestManySpilledResultsStayInsideASmallWindow(t *testing.T) {
	for _, ratio := range smallRatios {
		t.Run(fmt.Sprint(ratio, "_chars_per_token"), func(t *testing.T) { manySpilledResults(t, ratio) })
	}
}

func manySpilledResults(t *testing.T, ratio float64) {
	h := newHarness(t)
	ctx := context.Background()
	assumeWindow(t, h)
	// the retrieved passages and instructions of a knowledge assistant: about a third of the window before any tool runs
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, features, system_prompt) VALUES ('web', 'ctx-m', '{"web_search": true}', $1)`,
		padded("You answer from the knowledge base. ", 17000)); err != nil {
		t.Fatal(err)
	}
	tools.Register(&filesTools{e: h.e})
	tools.Register(&textTool{id: "builtin:web", name: "search_web", body: func(n int) string { return padded(fmt.Sprintf("topic %d ", n), 12000+n*450) }})
	t.Cleanup(func() { tools.Register(inertSet("builtin:files")); tools.Register(inertSet("builtin:web")) })
	h.set(t, map[string]any{"web.search.max_calls_per_turn": 0})
	const rounds, per = 6, 4
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) {
		if n >= rounds {
			s.text("eight topics done")
			return
		}
		var cs [][3]string
		for i := range per {
			cs = append(cs, [3]string{fmt.Sprintf("k%d-%d", n, i), "search_web", fmt.Sprintf(`{"i":%d}`, n*per+i)})
		}
		s.calls(cs...)
	})
	m.window, m.ratio = smallWindow, ratio
	m.summary = padded("Notes on what was found so far. ", 9000) // a summary written by a local model is long
	run := startModel(t, h, "web", &Request{Temporary: true, Features: map[string]bool{"web_search": true},
		Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"look up all of it"`)}}})
	reqs := m.requests()
	if got := run.stateText(); got != "eight topics done" {
		t.Fatalf("answer %q after %d requests (%d refused for their size); run error %q", got, len(reqs), m.overflows, run.state.Error)
	}
	if m.overflows != 0 {
		t.Fatalf("%d calls were sent over the window", m.overflows)
	}
	for n, r := range reqs {
		assertPaired(t, n, r)
	}
	var spills int
	for _, s := range h.stepsOf(t, run.ID) {
		if s.Name == "spill" {
			spills++
		}
	}
	if spills != rounds*per {
		t.Fatalf("%d spill steps, want %d", spills, rounds*per)
	}
	t.Logf("%d requests, peak %d of %d tokens", len(reqs), slicesMax(m.sizes), smallWindow)
}

// ---- unit tests ------------------------------------------------------------------------

func unitExec() *exec {
	x := &exec{run: &Run{}, m: &models.Resolved{ID: "unit"}}
	x.run.trace.enable(true)
	return x
}

func stepNames(x *exec) []string {
	steps, _ := x.run.trace.take()
	var out []string
	for _, s := range steps {
		out = append(out, s.Name)
	}
	return out
}

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

func TestLevelFor(t *testing.T) {
	for _, c := range []struct {
		name  string
		lens  []int
		need  int
		floor int
		want  int
	}{
		{"one result", []int{10000}, 4000, 500, 6000},
		{"the largest only", []int{10000, 2000, 1000}, 4000, 500, 6000},
		{"two share it", []int{10000, 10000, 1000}, 8000, 500, 6000},
		{"the largest goes down to the next and both go on", []int{10000, 5000, 1000}, 9000, 500, 3000},
		{"everything to the floor is not enough", []int{3000, 2000}, 10000, 500, 500},
		{"nothing to cut", nil, 1000, 500, 500},
	} {
		if got := levelFor(c.lens, c.need, c.floor); got != c.want {
			t.Errorf("%s: level %d, want %d", c.name, got, c.want)
		}
	}
}

// B: when rounds are fat, results of earlier rounds go largest first, the newest round and the start of the run stay.
func TestClearRecentTakesLargestEarlierResultsFirst(t *testing.T) {
	x, b := unitExec(), &runBudget{window: 16000, clearTo: 0.5, spilled: map[string]string{}, base: 2, manage: true}
	msgs := []provider.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"},
		callMsg("a1", "a2"), toolMsg("a1", 10000), toolMsg("a2", 2000),
		callMsg("b1", "b2"), toolMsg("b1", 6000), toolMsg("b2", 6000),
		callMsg("c1"), toolMsg("c1", 20000)}
	out := (&Engine{}).clearRecent(x, b, msgs, 3, 8000)
	cleared := func(i int) bool { return strings.HasPrefix(out[i].Content, clearedPrefix) }
	if !cleared(3) || !cleared(6) || !cleared(7) || cleared(4) || cleared(9) {
		t.Fatalf("cleared a1 %v b1 %v b2 %v, a2 %v c1 %v (want true true true false false)", cleared(3), cleared(6), cleared(7), cleared(4), cleared(9))
	}
	if out[0].Content != "sys" || out[1].Content != "task" || msgs[3].Content == out[3].Content {
		t.Fatalf("the start of the run changed, or the input was modified in place")
	}
	if n := b.size(out); n > 8000 {
		t.Fatalf("size %d after clearing, target 8000", n)
	}
	if got := stepNames(x); !slices.Equal(got, []string{"clear"}) {
		t.Fatalf("steps %v", got)
	}
	// already under the target: nothing happens
	if same := (&Engine{}).clearRecent(x, b, msgs, 3, 30000); strings.HasPrefix(same[3].Content, clearedPrefix) {
		t.Fatal("cleared with room to spare")
	}
}

// D: the largest results, the newest round's too, are cut to one length; small ones stay whole.
func TestTruncateLargestCutsTheBiggestToACommonLength(t *testing.T) {
	x, b := unitExec(), &runBudget{window: 16000, spilled: map[string]string{}, base: 2, manage: true}
	msgs := []provider.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"},
		callMsg("a1", "a2", "a3"), toolMsg("a1", 20000), toolMsg("a2", 20000), toolMsg("a3", 1500)}
	before := b.size(msgs)
	out := (&Engine{}).truncateLargest(x, b, msgs, 1, 8000)
	if len(out[3].Content) != len(out[4].Content) || len(out[3].Content) >= 20000 || len(out[5].Content) != 1500 || !strings.Contains(out[3].Content, "characters shown") {
		t.Fatalf("lengths %d %d %d", len(out[3].Content), len(out[4].Content), len(out[5].Content))
	}
	if n := b.size(out); n > 8000 || n >= before {
		t.Fatalf("size %d (was %d), target 8000", n, before)
	}
	if !strings.HasPrefix(out[3].Content, "r-a1 ") || out[0].Content != "sys" || out[1].Content != "task" {
		t.Fatal("the head of a result or the start of the run changed")
	}
	if got := stepNames(x); !slices.Equal(got, []string{"truncate"}) {
		t.Fatalf("steps %v", got)
	}
}

// D: the guard never lets a call out over the ceiling when something can be shortened, and says so when nothing can.
func TestGuardShortensThenFailsOnlyForADeclaredWindow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	msgs := func(task int) []provider.Message {
		return []provider.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: strings.Repeat("t", task)},
			callMsg("a1", "a2"), toolMsg("a1", 30000), toolMsg("a2", 30000)}
	}
	newBudget := func(source string) *runBudget {
		return &runBudget{window: 16000, source: source, manage: true, base: 2, spilled: map[string]string{}}
	}
	// two 30,000-character results over a 16,000 window: cut, and the call fits
	x, b := unitExec(), newBudget("setting")
	out, err := h.e.guard(ctx, x, b, msgs(100), 1, true)
	if err != nil || b.size(out) > b.ceiling() {
		t.Fatalf("size %d over the ceiling %d, err %v", b.size(out), b.ceiling(), err)
	}
	if got := stepNames(x); !slices.Contains(got, "truncate") || slices.Contains(got, "too_large") {
		t.Fatalf("steps %v", got)
	}
	// a task that alone is over the window: a declared window fails, an assumed one sends the call
	big := 70000 // 21,000 tokens
	x, b = unitExec(), newBudget("context_window")
	if _, err := h.e.guard(ctx, x, b, msgs(big), 1, true); err == nil || !strings.Contains(err.Error(), "context window of 16000") {
		t.Fatalf("declared window: err %v", err)
	}
	var ae *api.Error
	if _, err := h.e.guard(ctx, unitExec(), newBudget("num_ctx"), msgs(big), 1, true); !errors.As(err, &ae) || ae.Code != "context_length_exceeded" {
		t.Fatalf("declared window: error %v, want code context_length_exceeded", err)
	}
	for _, source := range []string{"setting", "server"} {
		x = unitExec()
		if _, err := h.e.guard(ctx, x, newBudget(source), msgs(big), 1, true); err != nil {
			t.Fatalf("%s window: %v", source, err)
		}
		if got := stepNames(x); !slices.Contains(got, "too_large") {
			t.Fatalf("%s window: steps %v", source, got)
		}
	}
}

// ---- engine-level tests: caps, refusals, retry, summary ---------------------------------

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

// D: a prompt the model's declared window cannot hold ends the run with a clear error, and no call is made.
func TestPromptOverADeclaredWindowFailsWithoutACall(t *testing.T) {
	h := newHarness(t)
	big := json.RawMessage(strconv.Quote(strings.Repeat("a long attached text ", 1500))) // 31,500 characters, about 9,500 tokens
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) { s.text("never") })
	tools.Register(&textTool{id: "big-server", name: "big", body: func(int) string { return "x" }})
	run := h.startCtx(t, &Request{Temporary: true, ToolIDs: []string{"server:big-server"}, Params: map[string]any{"context_window": 4096},
		Messages: []WireMessage{{Role: "user", Content: big}}})
	if n := len(m.requests()); n != 0 {
		t.Fatalf("%d calls were made", n)
	}
	code, msg := runError(run)
	if code != "context_length_exceeded" || !strings.Contains(msg, "context window of 4096") || !strings.Contains(msg, "shorten the message") {
		t.Fatalf("error %q: %q", code, msg)
	}
	var tooLarge bool
	for _, s := range h.stepsOf(t, run.ID) {
		tooLarge = tooLarge || s.Name == "too_large" && s.Error == "context_length_exceeded" && s.Detail["window"] == 4096.0
	}
	if !tooLarge {
		t.Fatal("no too_large step in the trace")
	}
	// the same text with the default window is sent: the window is only a guess, the server decides
	h2 := newHarness(t)
	m2 := h2.ctxModel(t, func(n int, r ctxReq, s *sse) { s.text("answered") })
	if got := h2.startCtx(t, &Request{Temporary: true, ToolIDs: []string{"server:big-server"}, Messages: []WireMessage{{Role: "user", Content: big}}}).stateText(); got != "answered" || len(m2.requests()) != 1 {
		t.Fatalf("default window: answer %q after %d calls", got, len(m2.requests()))
	}
}

// E: the server refuses a prompt the estimate thought fine (its window is smaller than assumed). The loop does not send it
// again: it shortens it once and retries once, and the run completes.
func TestRefusedPromptIsShortenedAndRetriedOnce(t *testing.T) {
	h := newHarness(t)
	assumeWindow(t, h) // Kera assumes 16,000; the server really has 6,000
	tools.Register(&textTool{id: "big-server", name: "big", body: func(n int) string { return padded(fmt.Sprintf("result %d ", n), 14000) }})
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) {
		if n < 3 {
			s.call(fmt.Sprintf("c%d", n), "big", `{}`)
			return
		}
		s.text("finished")
	})
	m.window, m.ratio = 6000, 3.3
	run := h.startCtx(t, &Request{ToolIDs: []string{"server:big-server"}, Temporary: true})
	if got := run.stateText(); got != "finished" {
		code, msg := runError(run)
		t.Fatalf("answer %q; error %s %q; %d refused; sizes %v", got, code, msg, m.overflows, m.sizes)
	}
	if m.overflows != 1 {
		t.Fatalf("%d calls were refused, want exactly 1 (not resent unchanged); sizes %v", m.overflows, m.sizes)
	}
	var retry map[string]any
	var model []stepRow
	for _, s := range h.stepsOf(t, run.ID) {
		if s.Name == "overflow_retry" {
			retry = s.Detail
		}
		if s.Kind == "model" && s.Error != "" {
			model = append(model, s)
		}
	}
	if retry == nil || retry["tokens_after"].(float64) >= retry["tokens_before"].(float64) || retry["window_after"].(float64) >= retry["window_before"].(float64) || retry["http_status"] != 500.0 {
		t.Fatalf("overflow_retry step %v", retry)
	}
	if len(model) != 1 || model[0].Error != "context_length_exceeded" || model[0].Detail["will_retry"] != nil {
		t.Fatalf("the refused model call in the trace: %+v", model)
	}
	// a later call fits the learned window as well
	if n := slicesMax(m.sizes); n > 6000 {
		t.Fatalf("an accepted call of %d tokens", n)
	}
}

// E: when nothing can be shortened the refusal ends the run at once, with the clear error, after one call.
func TestRefusalThatCannotBeShortenedEndsAfterOneCall(t *testing.T) {
	h := newHarness(t)
	assumeWindow(t, h)
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) { s.text("never") })
	m.window, m.ratio = 1500, 3.3
	run := h.startCtx(t, &Request{Temporary: true, Messages: []WireMessage{{Role: "user", Content: json.RawMessage(strconv.Quote(strings.Repeat("words ", 2000)))}}})
	code, msg := runError(run)
	if code != "context_length_exceeded" || !strings.Contains(msg, "does not fit") {
		t.Fatalf("error %q: %q", code, msg)
	}
	if m.overflows != 1 {
		t.Fatalf("%d calls were refused, want 1", m.overflows)
	}
	var nothing bool
	for _, s := range h.stepsOf(t, run.ID) {
		nothing = nothing || s.Name == "overflow_retry" && s.Error == "nothing_to_shrink"
	}
	if !nothing {
		t.Fatal("no overflow_retry step with nothing_to_shrink")
	}
}

// E: an error that is not an overflow keeps its retries (three calls for a 500 that says nothing about the window).
func TestOtherServerErrorsKeepTheirRetries(t *testing.T) {
	h := newHarness(t)
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fmt.Fprint(w, `{"data":[{"id":"ctx-m"}]}`)
			return
		}
		posts++
		http.Error(w, `{"error":"model runner crashed"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	if _, err := h.pool.Exec(context.Background(), `INSERT INTO connection (id, kind, base_url, model_ids) VALUES ('ctx', 'openai', $1, '{ctx-m}')`, srv.URL); err != nil {
		t.Fatal(err)
	}
	h.set(t, map[string]any{"tasks.title": false, "tasks.tags": false, "tasks.follow_ups": false})
	run := h.startCtx(t, &Request{Temporary: true})
	if code, _ := runError(run); code != "provider_error" || posts != 3 {
		t.Fatalf("code %q after %d calls, want provider_error after 3", code, posts)
	}
}

// F: a mid-run summary is asked to stay short and is cut when it does not: a summary of 40,000 characters leaves the
// next prompt inside the window.
func TestMidRunSummaryIsBounded(t *testing.T) {
	h := newHarness(t)
	h.set(t, map[string]any{"chat.context.keep_rounds": 6})
	m := h.ctxModel(t, looping(40))
	m.summary = padded("Notes on everything found so far. ", 40000)
	run, reqs := run40Rounds(t, h, m, nil)
	if len(m.sums) == 0 {
		t.Fatal("no summary was asked for")
	}
	if !strings.Contains(m.sums[0].text(0), "Keep the notes under") {
		t.Fatalf("the summary prompt sets no length: %q", clip(m.sums[0].text(0), 100))
	}
	var longest int
	for _, r := range reqs {
		for i := range r.Messages {
			if t := r.text(i); strings.Contains(t, "Summary of the work done so far") {
				longest = max(longest, len(t))
			}
		}
	}
	// an eighth of 16,000 tokens at the 4 characters per token this server reports, and the wrapper around the notes
	if longest == 0 || longest > 2000*4+300 {
		t.Fatalf("the summary in the prompt is %d characters", longest)
	}
	for _, s := range h.stepsOf(t, run.ID) {
		if s.Name == "summarise_run" && s.Error == "" && s.Detail["summary_chars"].(float64) > 8000 {
			t.Fatalf("summarise_run step: %+v", s.Detail)
		}
	}
}
