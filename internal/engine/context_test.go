package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/tools"
)

// ---- a scripted model server --------------------------------------------------------

// ctxReq is one chat request the scripted server received.
type ctxReq struct {
	Messages []map[string]any
	Tools    []map[string]any
	toolsLen int
}

func (r ctxReq) text(i int) string {
	s, _ := r.Messages[i]["content"].(string)
	return s
}

func (r ctxReq) last() string { return r.text(len(r.Messages) - 1) }

// tokens estimates the request the way the engine does (characters / 4, 4 per message), plus the tool definitions.
func (r ctxReq) tokens() int {
	n := r.toolsLen / 4
	for i, m := range r.Messages {
		c := len(r.text(i))
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, tc := range calls {
				f := tc.(map[string]any)["function"].(map[string]any)
				c += len(fmt.Sprint(f["name"])) + len(fmt.Sprint(f["arguments"]))
			}
		}
		n += c/4 + 4
	}
	return n
}

// toolResults maps tool_call_id to the tool message's text.
func (r ctxReq) toolResults() map[string]string {
	out := map[string]string{}
	for i, m := range r.Messages {
		if m["role"] == "tool" {
			out[fmt.Sprint(m["tool_call_id"])] = r.text(i)
		}
	}
	return out
}

// sse writes one streamed answer.
type sse struct {
	w      http.ResponseWriter
	fl     http.Flusher
	prompt int
}

func (s *sse) delta(d string) {
	fmt.Fprintf(s.w, "data: {\"choices\":[{\"delta\":%s}]}\n\n", d)
	s.fl.Flush()
}

func (s *sse) text(t string) {
	b, _ := json.Marshal(map[string]string{"content": t})
	s.delta(string(b))
}

func (s *sse) call(id, name, args string) {
	b, _ := json.Marshal(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "function": map[string]any{"name": name, "arguments": args}}}})
	s.delta(string(b))
}

func (s *sse) calls(cs ...[3]string) {
	var list []any
	for i, c := range cs {
		list = append(list, map[string]any{"index": i, "id": c[0], "function": map[string]any{"name": c[1], "arguments": c[2]}})
	}
	b, _ := json.Marshal(map[string]any{"tool_calls": list})
	s.delta(string(b))
}

func (s *sse) done() {
	fmt.Fprintf(s.w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n", s.prompt)
	s.fl.Flush()
}

// ctxModel is an OpenAI-compatible server whose answers a test scripts. Requests of the background tasks
// (title, tags...) and of the summary prompt are answered without calling the script.
type ctxModel struct {
	mu          sync.Mutex
	reqs        []ctxReq // chat requests, in order
	sums        []ctxReq // summary requests
	failSummary bool     // answer the summary prompt with a 500
	handle      func(n int, r ctxReq, s *sse)
	report      func(n int, r ctxReq) int // prompt tokens to report (default: the estimate)
}

func (h *harness) ctxModel(t *testing.T, handle func(n int, r ctxReq, s *sse)) *ctxModel {
	t.Helper()
	m := &ctxModel{handle: handle}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fmt.Fprint(w, `{"data":[{"id":"ctx-m"}]}`)
			return
		}
		var body struct {
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		tj, _ := json.Marshal(body.Tools)
		req := ctxReq{Messages: body.Messages, Tools: body.Tools, toolsLen: len(tj)}
		w.Header().Set("Content-Type", "text/event-stream")
		out := &sse{w: w, fl: w.(http.Flusher), prompt: req.tokens()}
		sys := req.text(0)
		switch {
		case strings.Contains(sys, "Reply with JSON"):
			out.text(`{"title":"T","tags":["t"],"follow_ups":["more?"],"queries":["q"]}`)
		case strings.HasPrefix(sys, "Summarise the conversation"):
			m.mu.Lock()
			m.sums = append(m.sums, req)
			fail := m.failSummary
			m.mu.Unlock()
			if fail {
				http.Error(w, "summary backend down", http.StatusInternalServerError)
				return
			}
			out.text("SUMMARY NOTES")
		default:
			m.mu.Lock()
			m.reqs = append(m.reqs, req)
			n := len(m.reqs) - 1
			m.mu.Unlock()
			if m.report != nil {
				out.prompt = m.report(n, req)
			}
			m.handle(n, req, out)
		}
		out.done()
	}))
	t.Cleanup(srv.Close)
	if _, err := h.pool.Exec(context.Background(), `INSERT INTO connection (id, kind, base_url, model_ids) VALUES ('ctx', 'openai', $1, '{ctx-m}')`, srv.URL); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *ctxModel) requests() []ctxReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.reqs)
}

// ---- tools --------------------------------------------------------------------------

// textTool returns body(n) for its nth call.
type textTool struct {
	id, name string
	body     func(n int) string
	n        atomic.Int32
	readOnly bool
	inputs   []string
	gauge    *gauge
	delay    time.Duration
}

func (f *textTool) ID() string { return f.id }
func (f *textTool) Tools(context.Context, *auth.User) ([]tools.Spec, error) {
	return []tools.Spec{{Name: f.name, Description: "test", Parameters: json.RawMessage(`{"type":"object","properties":{"i":{"type":"integer"}}}`)}}, nil
}
func (f *textTool) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	if f.gauge != nil {
		f.gauge.enter()
		defer f.gauge.leave()
	}
	time.Sleep(f.delay)
	n := int(f.n.Add(1))
	return tools.Result{Content: f.body(n), Inputs: f.inputs}, nil
}

// gauge records how many calls ran at the same time.
type gauge struct{ now, max atomic.Int32 }

func (g *gauge) enter() {
	n := g.now.Add(1)
	for {
		m := g.max.Load()
		if n <= m || g.max.CompareAndSwap(m, n) {
			return
		}
	}
}
func (g *gauge) leave() { g.now.Add(-1) }

// inertSet replaces a built-in set a test registered, so later tests see none of it.
type inertSet string

func (s inertSet) ID() string                                            { return string(s) }
func (inertSet) Tools(context.Context, *auth.User) ([]tools.Spec, error) { return nil, nil }
func (inertSet) Execute(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{}, nil
}

func padded(prefix string, n int) string {
	return prefix + strings.Repeat("0123456789", n/10+1)[:n-len(prefix)]
}

// start runs one request to the end and returns the run.
func (h *harness) startCtx(t *testing.T, req *Request) *Run {
	t.Helper()
	req.Model, req.StreamFormat = "ctx-m", "events"
	if req.Messages == nil && req.UserMessage == nil {
		req.Messages = []WireMessage{{Role: "user", Content: json.RawMessage(`"work"`)}}
	}
	run, err := h.e.Start(context.Background(), h.u, req)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	return run
}

// ---- tests --------------------------------------------------------------------------

// assertPaired checks that every tool call of the request has its result and every result its call.
func assertPaired(t *testing.T, n int, r ctxReq) {
	t.Helper()
	calls, results := map[string]bool{}, map[string]bool{}
	for i, m := range r.Messages {
		switch m["role"] {
		case "assistant":
			cs, _ := m["tool_calls"].([]any)
			for _, c := range cs {
				calls[fmt.Sprint(c.(map[string]any)["id"])] = true
			}
		case "tool":
			id := fmt.Sprint(m["tool_call_id"])
			if !calls[id] {
				t.Fatalf("request %d: result %s at message %d has no call before it", n, id, i)
			}
			results[id] = true
		}
	}
	for id := range calls {
		if !results[id] {
			t.Fatalf("request %d: call %s has no result", n, id)
		}
	}
}

// run40Rounds drives a 16,000-token window with 40 rounds of 8,000-character results and checks every request.
func run40Rounds(t *testing.T, h *harness, m *ctxModel, extra map[string]any) (*Run, []ctxReq) {
	t.Helper()
	h.set(t, map[string]any{"chat.max_tool_iterations": 100})
	tools.Register(&textTool{id: "big-server", name: "big", body: func(n int) string { return padded(fmt.Sprintf("result %d ", n), 8000) }})
	run := h.startCtx(t, &Request{ToolIDs: []string{"server:big-server"}, Temporary: true, Params: map[string]any{"context_window": 16000}})
	reqs := m.requests()
	if len(reqs) != 41 {
		t.Fatalf("%d model requests, want 41", len(reqs))
	}
	peak := 0
	for n, r := range reqs {
		peak = max(peak, r.tokens())
		if tk := r.tokens(); tk > 16000 {
			t.Fatalf("request %d is %d tokens, over the 16000 window", n, tk)
		}
		if r.text(0) != "work" { // no system prompt in these runs: the user's request is the first message
			t.Fatalf("request %d lost the first user message: %q", n, r.text(0))
		}
		assertPaired(t, n, r)
	}
	t.Logf("peak request %d tokens of 16000, %d summary calls", peak, len(m.sums))
	if got := run.stateText(); got != "all done" {
		t.Fatalf("answer %q", got)
	}
	return run, reqs
}

func looping(rounds int) func(n int, r ctxReq, s *sse) {
	return func(n int, r ctxReq, s *sse) {
		if n < rounds {
			s.call(fmt.Sprintf("c%d", n), "big", fmt.Sprintf(`{"i":%d}`, n))
			return
		}
		s.text("all done")
	}
}

// Old tool results are cleared before they can fill the window: 40 rounds fit in 16,000 tokens.
func TestLoopClearsOldResultsToStayUnderWindow(t *testing.T) {
	h := newHarness(t)
	m := h.ctxModel(t, looping(40))
	run, reqs := run40Rounds(t, h, m, nil)
	// the newest rounds are intact, the oldest are one line
	last := reqs[40]
	res := last.toolResults()
	if !strings.HasPrefix(res["c39"], "result 40 ") || !strings.HasPrefix(res["c0"], "[cleared to save context: big returned 8000 characters]") {
		t.Fatalf("c39 %q c0 %q", clip(res["c39"], 40), clip(res["c0"], 80))
	}
	steps := h.stepsOf(t, run.ID)
	var clears, other int
	for _, s := range steps {
		switch {
		case s.Kind == "compaction" && s.Name == "clear":
			clears++
			if s.Detail["tokens_after"].(float64) >= s.Detail["tokens_before"].(float64) || s.Detail["window"] != 16000.0 || s.Detail["window_source"] != "context_window" {
				t.Fatalf("clear step %+v", s)
			}
		case s.Kind == "compaction":
			other++
		}
	}
	if clears == 0 {
		t.Fatalf("no clear step in the trace: %+v", steps)
	}
	if other != 0 {
		t.Fatalf("clearing alone should be enough here, got %d other compaction steps", other)
	}
}

// When clearing leaves too much, the run so far is summarised and the newest rounds stay.
func TestLoopSummarisesWhenClearingIsNotEnough(t *testing.T) {
	h := newHarness(t)
	h.set(t, map[string]any{"chat.context.keep_rounds": 6}) // six rounds alone are over the threshold
	m := h.ctxModel(t, looping(40))
	run, reqs := run40Rounds(t, h, m, nil)
	if len(m.sums) == 0 {
		t.Fatal("the summary prompt was never used")
	}
	var seen bool
	for _, r := range reqs {
		for i := range r.Messages {
			if strings.Contains(r.text(i), "Summary of the work done so far") && strings.Contains(r.text(i), "SUMMARY NOTES") {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("no request carried the summary")
	}
	var ok bool
	for _, s := range h.stepsOf(t, run.ID) {
		if s.Name == "summarise_run" && s.Error == "" && s.Detail["tokens_after"].(float64) < s.Detail["tokens_before"].(float64) {
			ok = true
		}
	}
	if !ok {
		t.Fatal("no summarise_run step in the trace")
	}
	// the summary request itself stays inside the window
	for _, r := range m.sums {
		if r.tokens() > 16000 {
			t.Fatalf("summary request is %d tokens", r.tokens())
		}
	}
}

// A failing summary call falls back to clearing harder; the run goes on under budget.
func TestLoopSummaryFailureFallsBackToClearing(t *testing.T) {
	h := newHarness(t)
	h.set(t, map[string]any{"chat.context.keep_rounds": 6})
	m := h.ctxModel(t, looping(40))
	m.failSummary = true
	run, _ := run40Rounds(t, h, m, nil)
	var failed bool
	for _, s := range h.stepsOf(t, run.ID) {
		if s.Name == "summarise_run" && s.Error == "compaction_failed" {
			failed = true
		}
	}
	if !failed {
		t.Fatal("the failed summary is not in the trace")
	}
}

// With context management off the loop sends everything, as before.
func TestLoopContextManagementOff(t *testing.T) {
	h := newHarness(t)
	h.set(t, map[string]any{"chat.context.enable": false})
	m := h.ctxModel(t, looping(8))
	tools.Register(&textTool{id: "big-server", name: "big", body: func(n int) string { return padded("r", 8000) }})
	h.startCtx(t, &Request{ToolIDs: []string{"server:big-server"}, Temporary: true, Params: map[string]any{"context_window": 16000}})
	reqs := m.requests()
	if res := reqs[len(reqs)-1].toolResults()["c0"]; strings.Contains(res, "cleared") || len(res) != 8000 {
		t.Fatalf("a result was changed with management off: %q", clip(res, 60))
	}
}

// A result longer than the spill size is stored as a file; the model reads it back with view_file in the same run,
// and list_chat_files finds it in a later turn.
func TestSpilledResultIsReadableThroughFileTools(t *testing.T) {
	h := newHarness(t)
	tools.Register(&filesTools{e: h.e})
	t.Cleanup(func() { tools.Register(inertSet("builtin:files")) })
	tools.Register(&textTool{id: "lines-server", name: "dump", body: func(int) string {
		var b strings.Builder
		for i := 1; i <= 1000; i++ {
			if i == 900 {
				b.WriteString("NEEDLE-4711 is on this line\n")
			} else {
				fmt.Fprintf(&b, "row %04d filler filler\n", i)
			}
		}
		return b.String()
	}})
	fileRe := regexp.MustCompile(`\(id ([0-9a-f-]{36})\)`)
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) {
		last := r.last()
		switch {
		case strings.Contains(last, "NEEDLE-4711 is on this line") && r.Messages[len(r.Messages)-1]["role"] == "tool":
			s.text("found it")
		case strings.Contains(last, "Result too long for the prompt"):
			id := fileRe.FindStringSubmatch(last)[1]
			s.call("v1", "view_file", fmt.Sprintf(`{"file":%q,"start_line":900,"max_lines":2}`, id))
		case strings.Contains(last, "list the files"):
			s.call("l1", "list_chat_files", `{}`)
		case r.Messages[len(r.Messages)-1]["role"] == "tool":
			s.text("files: " + last)
		default:
			s.call("d1", "dump", `{}`)
		}
	})
	cid := h.chat(t)
	run := h.startCtx(t, &Request{ChatID: cid, ID: "a1", ToolIDs: []string{"server:lines-server"}, UserMessage: &UserMessage{ID: "q1", Content: "dump it"},
		Params: map[string]any{"context_window": 16000}})
	if got := run.stateText(); got != "found it" {
		t.Fatalf("answer %q; requests %d", got, len(m.requests()))
	}
	reqs := m.requests()
	first := reqs[1].last()
	if !strings.Contains(first, "row 0001") || !strings.Contains(first, "row 1000") || strings.Contains(first, "row 0500") || len(first) > 4000 {
		t.Fatalf("model text: head and tail expected, got %d chars", len(first))
	}
	if !strings.Contains(first, "view_file") || !strings.Contains(first, "grep_chat_files") || !strings.Contains(first, "21000 characters") && !strings.Contains(first, "characters") {
		t.Fatalf("model text lacks the pointer: %s", first)
	}
	// the file is an input of the step, not an attachment of the answer
	run.mu.Lock()
	step := run.state.Parts[0].Steps[0]
	answerFiles := len(run.state.Files)
	run.mu.Unlock()
	if len(step.Files) != 1 || !strings.HasPrefix(step.Files[0].Name, "dump-result-") || answerFiles != 0 {
		t.Fatalf("step files %+v, answer files %d", step.Files, answerFiles)
	}
	steps := h.stepsOf(t, run.ID)
	var spill, tool bool
	for _, s := range steps {
		spill = spill || s.Kind == "compaction" && s.Name == "spill" && s.Detail["tool"] == "dump" && s.Detail["file"] == step.Files[0].ID
		tool = tool || s.Kind == "tool" && s.Name == "dump" && s.Detail["spilled"] == true
	}
	if !spill || !tool {
		t.Fatalf("trace lacks the spill: %+v", steps)
	}
	// a later turn of the chat can still list and read it
	run2 := h.startCtx(t, &Request{ChatID: cid, ID: "a2", ToolIDs: []string{"server:lines-server"},
		UserMessage: &UserMessage{ID: "q2", ParentID: ptr("a1"), Content: "list the files"}, Params: map[string]any{"context_window": 16000}})
	if got := run2.stateText(); !strings.Contains(got, "dump-result-") || !strings.Contains(got, step.Files[0].ID) {
		t.Fatalf("second turn: %q", got)
	}
}

// view_file stops a page at 30,000 characters and says where to go on.
func TestViewFilePageIsCapped(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("a", 1500)
	f, err := h.e.Files.CreateText(context.Background(), "u1", "long.txt", strings.Repeat(long+"\n", 100), nil)
	if err != nil {
		t.Fatal(err)
	}
	ft := &filesTools{e: h.e}
	res, err := ft.Execute(context.Background(), tools.Call{Name: "view_file", Args: json.RawMessage(`{"file":"` + f.ID + `","max_lines":100}`), User: h.u, FileIDs: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) > 33000 || !strings.Contains(res.Content, "[more: continue with start_line 21]") || !strings.Contains(res.Content, "lines 1-20 of 101") {
		t.Fatalf("%d chars, tail %q, head %q", len(res.Content), res.Content[max(len(res.Content)-60, 0):], clip(res.Content, 60))
	}
}

// ---- retries, the cap, the repeat guard, parallel calls ------------------------------

// A call that fails with a 502 before any event is repeated once and gives one answer.
func TestModelCallRetriedBeforeAnyEvent(t *testing.T) {
	h := newHarness(t)
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fmt.Fprint(w, `{"data":[{"id":"ctx-m"}]}`)
			return
		}
		if posts.Add(1) == 1 {
			http.Error(w, "upstream says: bad gateway", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		s := &sse{w: w, fl: w.(http.Flusher), prompt: 5}
		s.text("the answer")
		s.done()
	}))
	t.Cleanup(srv.Close)
	if _, err := h.pool.Exec(context.Background(), `INSERT INTO connection (id, kind, base_url, model_ids) VALUES ('ctx', 'openai', $1, '{ctx-m}')`, srv.URL); err != nil {
		t.Fatal(err)
	}
	h.set(t, map[string]any{"tasks.title": false, "tasks.tags": false, "tasks.follow_ups": false})
	run := h.startCtx(t, &Request{Temporary: true})
	if got := run.stateText(); got != "the answer" {
		t.Fatalf("answer %q (duplicate output?)", got)
	}
	steps := h.stepsOf(t, run.ID)
	if len(steps) != 2 || steps[0].Error != "provider_error" || steps[0].Detail["will_retry"] != true || steps[0].Detail["http_status"] != 502.0 ||
		steps[1].Error != "" || steps[1].Detail["attempt"] != 2.0 {
		t.Fatalf("trace %+v", steps)
	}
	if posts.Load() != 2 {
		t.Fatalf("%d posts", posts.Load())
	}
}

// A failure after the first event is not retried: the text is shown and a second try would repeat it.
func TestModelCallNotRetriedAfterFirstEvent(t *testing.T) {
	h := newHarness(t)
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fmt.Fprint(w, `{"data":[{"id":"ctx-m"}]}`)
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		s := &sse{w: w, fl: w.(http.Flusher)}
		s.text("partial ")
		conn, _, err := w.(http.Hijacker).Hijack() // drop the connection mid-stream
		if err == nil {
			conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	if _, err := h.pool.Exec(context.Background(), `INSERT INTO connection (id, kind, base_url, model_ids) VALUES ('ctx', 'openai', $1, '{ctx-m}')`, srv.URL); err != nil {
		t.Fatal(err)
	}
	run := h.startCtx(t, &Request{Temporary: true})
	if posts.Load() != 1 {
		t.Fatalf("%d posts: a failure after the first event was retried", posts.Load())
	}
	if got := run.stateText(); got != "partial " {
		t.Fatalf("text %q", got)
	}
	steps := h.stepsOf(t, run.ID)
	if len(steps) != 1 || steps[0].Error == "" || steps[0].Detail["will_retry"] != nil {
		t.Fatalf("trace %+v", steps)
	}
}

func TestRetryable(t *testing.T) {
	for err, want := range map[error]bool{
		&provider.HTTPError{Status: 502}:                              true,
		&provider.HTTPError{Status: 429}:                              true,
		&provider.HTTPError{Status: 400}:                              false,
		&provider.HTTPError{Status: 401}:                              false,
		fmt.Errorf("wrapped: %w", provider.ErrIdleTimeout):            true,
		fmt.Errorf("dial: %w", &netErr{}):                             true,
		context.DeadlineExceeded:                                      false,
		fmt.Errorf("openai stream: %w", fmt.Errorf("blocked by pii")): false,
	} {
		if got := retryable(err); got != want {
			t.Errorf("retryable(%v) = %v, want %v", err, got, want)
		}
	}
}

type netErr struct{}

func (*netErr) Error() string   { return "connection refused" }
func (*netErr) Timeout() bool   { return false }
func (*netErr) Temporary() bool { return true }

// At the round cap the model gets one more call, without tools, that asks for the report.
func TestRoundCapAsksForFinalReport(t *testing.T) {
	h := newHarness(t)
	h.set(t, map[string]any{"chat.max_tool_iterations": 2})
	var ran atomic.Int32
	tools.Register(&textTool{id: "big-server", name: "big", body: func(n int) string { ran.Add(1); return fmt.Sprintf("result %d", n) }})
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) {
		if len(r.Tools) == 0 && strings.Contains(r.last(), "Write the final report now") {
			s.text("FINAL REPORT")
			return
		}
		s.call(fmt.Sprintf("c%d", n), "big", fmt.Sprintf(`{"i":%d}`, n))
	})
	run := h.startCtx(t, &Request{ToolIDs: []string{"server:big-server"}, Temporary: true})
	reqs := m.requests()
	if len(reqs) != 3 || ran.Load() != 2 {
		t.Fatalf("%d requests, %d tool runs", len(reqs), ran.Load())
	}
	if len(reqs[2].Tools) != 0 || reqs[2].Messages[len(reqs[2].Messages)-1]["role"] != "user" {
		t.Fatalf("the closing call: %d tools, last %v", len(reqs[2].Tools), reqs[2].Messages[len(reqs[2].Messages)-1])
	}
	if !strings.Contains(run.stateText(), "FINAL REPORT") {
		t.Fatalf("answer %q", run.stateText())
	}
	steps := h.stepsOf(t, run.ID)
	last := steps[len(steps)-1]
	if last.Kind != "model" || last.Finish != "round_cap" || last.Detail["final_report"] != true || last.Detail["provider_finish"] != "stop" {
		t.Fatalf("last step %+v", last)
	}
}

// The same call three times in a row is not run the third time.
func TestRepeatGuard(t *testing.T) {
	h := newHarness(t)
	var ran atomic.Int32
	tools.Register(&textTool{id: "big-server", name: "big", body: func(n int) string { ran.Add(1); return "same" }})
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) {
		if n < 5 {
			s.call(fmt.Sprintf("c%d", n), "big", `{"i":1}`)
			return
		}
		s.text("ok")
	})
	run := h.startCtx(t, &Request{ToolIDs: []string{"server:big-server"}, Temporary: true})
	if ran.Load() != 2 {
		t.Fatalf("the tool ran %d times, want 2", ran.Load())
	}
	res := m.requests()[5].toolResults()
	if res["c0"] != "same" || res["c1"] != "same" || !strings.HasPrefix(res["c2"], "Not run: big was already called") || !strings.HasPrefix(res["c4"], "Not run") {
		t.Fatalf("results %v", res)
	}
	var refused int
	for _, s := range h.stepsOf(t, run.ID) {
		if s.Kind == "tool" && s.Detail["repeat_refused"] == true {
			refused++
		}
	}
	if refused != 3 {
		t.Fatalf("%d refused calls in the trace", refused)
	}
	// a different argument in between breaks the run of identical calls
	if (&loopState{repeatLimit: 3}).repeated(provider.ToolCall{Name: "a"}) != "" {
		t.Fatal("first call refused")
	}
}

// Calls of one round that all belong to read-only built-in sets run together; results keep the call order.
// With any other call in the round everything stays serial.
func TestParallelReadOnlyRound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, features) VALUES ('web', 'ctx-m', '{"web_search": true}')`); err != nil {
		t.Fatal(err)
	}
	g := &gauge{}
	var f1, f2 string
	for i, p := range []*string{&f1, &f2} {
		f, err := h.e.Files.CreateText(ctx, "u1", fmt.Sprintf("in%d.txt", i), "x", nil)
		if err != nil {
			t.Fatal(err)
		}
		*p = f.ID
	}
	web := &textTool{id: "builtin:web", name: "search_web", delay: 60 * time.Millisecond, gauge: g, inputs: []string{f1, f2},
		body: func(n int) string { return fmt.Sprintf("hit-%d", n) }}
	write := &textTool{id: "write-server", name: "write_thing", gauge: g, body: func(int) string { return "written" }}
	tools.Register(web)
	tools.Register(write)
	t.Cleanup(func() { tools.Register(inertSet("builtin:web")) })
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) {
		if n == 0 {
			var cs [][3]string
			for i := range 4 {
				cs = append(cs, [3]string{fmt.Sprintf("r0-%d", i), "search_web", fmt.Sprintf(`{"i":%d}`, i)})
			}
			s.calls(cs...)
			return
		}
		s.text("done")
	})
	h.set(t, map[string]any{"web.search.max_calls_per_turn": 0})
	req := &Request{Temporary: true, Features: map[string]bool{"web_search": true}, ToolIDs: []string{"server:write-server"},
		Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"go"`)}}}
	startModel(t, h, "web", req)
	reqs := m.requests()
	var order []string
	for _, mm := range reqs[1].Messages {
		if mm["role"] == "tool" {
			order = append(order, fmt.Sprint(mm["tool_call_id"]))
		}
	}
	if !slices.Equal(order, []string{"r0-0", "r0-1", "r0-2", "r0-3"}) {
		t.Fatalf("result order %v", order)
	}
	if g.max.Load() < 2 {
		t.Fatalf("read-only calls did not overlap (max %d at once)", g.max.Load())
	}
	// second run: the same round plus a call to a server tool: serial
	g.max.Store(0)
	m.mu.Lock()
	m.reqs = nil
	m.mu.Unlock()
	m.handle = func(n int, r ctxReq, s *sse) {
		if n == 0 {
			s.calls([3]string{"a", "search_web", `{"i":1}`}, [3]string{"b", "search_web", `{"i":2}`}, [3]string{"w", "write_thing", `{}`})
			return
		}
		s.text("done")
	}
	startModel(t, h, "web", req)
	if g.max.Load() != 1 {
		t.Fatalf("a round with a write tool ran %d calls at once", g.max.Load())
	}
}

func startModel(t *testing.T, h *harness, model string, req *Request) *Run {
	t.Helper()
	req.Model, req.StreamFormat = model, "events"
	run, err := h.e.Start(context.Background(), h.u, req)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	return run
}

// ---- size tracking, window, settings, digest ----------------------------------------

func TestWindowResolutionOrder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	local := &models.Resolved{Local: true}
	hosted := &models.Resolved{}
	for _, c := range []struct {
		name   string
		m      *models.Resolved
		params map[string]any
		want   int
		src    string
	}{
		{"param wins", local, map[string]any{"context_window": 32000.0, "num_ctx": 8192.0}, 32000, "context_window"},
		{"num_ctx next", local, map[string]any{"num_ctx": 8192.0}, 8192, "num_ctx"},
		{"local setting", local, map[string]any{}, 16000, "setting"},
		{"hosted setting", hosted, map[string]any{}, 120000, "setting"},
	} {
		n, src := h.e.contextWindow(ctx, &exec{m: c.m, params: c.params})
		if n != c.want || src != c.src {
			t.Errorf("%s: %d %s, want %d %s", c.name, n, src, c.want, c.src)
		}
	}
	h.set(t, map[string]any{"chat.context.window_local": 4096, "chat.compaction.threshold": 50000})
	if n, _ := h.e.contextWindow(ctx, &exec{m: local, params: map[string]any{}}); n != 4096 {
		t.Errorf("window_local ignored: %d", n)
	}
	if n, _ := h.e.contextWindow(ctx, &exec{m: hosted, params: map[string]any{}}); n != 50000 {
		t.Errorf("threshold ignored: %d", n)
	}
}

func TestBudgetSizeUsesReportedPromptPlusNewMessages(t *testing.T) {
	msgs := []provider.Message{{Role: "system", Content: strings.Repeat("s", 400)}, {Role: "user", Content: strings.Repeat("u", 400)}}
	b := &runBudget{toolChars: 800}
	if got, want := b.size(msgs), (400/4+4)*2+200; got != want {
		t.Fatalf("estimate before any report: %d, want %d", got, want)
	}
	b.lastPrompt, b.lastLen = 1000, 2
	msgs = append(msgs, provider.Message{Role: "tool", Content: strings.Repeat("t", 4000)})
	if got, want := b.size(msgs), 1000+4000/4+4; got != want {
		t.Fatalf("reported + added: %d, want %d", got, want)
	}
	b.lastPrompt = 10 // a server that reports far too little is not trusted
	if got := b.size(msgs); got < 1000 {
		t.Fatalf("under-reported size trusted: %d", got)
	}
}

// A call that reports a prompt at 97% of the window is flagged in the trace.
func TestOverflowSuspectedIsTraced(t *testing.T) {
	h := newHarness(t)
	tools.Register(&textTool{id: "big-server", name: "big", body: func(n int) string { return "small" }})
	m := h.ctxModel(t, func(n int, r ctxReq, s *sse) {
		if n == 0 {
			s.call("c0", "big", `{}`)
			return
		}
		s.text("ok")
	})
	m.report = func(n int, r ctxReq) int { return []int{15700, 3000}[min(n, 1)] }
	run := h.startCtx(t, &Request{ToolIDs: []string{"server:big-server"}, Temporary: true, Params: map[string]any{"context_window": 16000}})
	var flagged int
	for _, s := range h.stepsOf(t, run.ID) {
		if s.Name == "overflow_suspected" {
			flagged++
			if s.Detail["prompt_tokens"] != 15700.0 || s.Detail["window"] != 16000.0 {
				t.Fatalf("step %+v", s)
			}
		}
	}
	if flagged != 1 {
		t.Fatalf("%d overflow steps", flagged)
	}
}

func TestContextSettingDefaults(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for key, want := range map[string]string{
		"chat.context.enable": "true", "chat.context.window_local": "16000", "chat.context.clear_at": "0.7", "chat.context.clear_to": "0.5",
		"chat.context.keep_rounds": "4", "chat.context.compact": "true", "chat.context.digest": "true", "tools.spill_chars": "0",
		"chat.model_retries": "2", "chat.repeat_limit": "3", "chat.parallel_tools": "true", "chat.compaction.enable": "true",
		"cowork.web.max_searches": "15", "cowork.web.max_fetches": "15",
	} {
		raw, err := h.e.Settings.Raw(ctx, key)
		if err != nil || string(raw) != want {
			t.Errorf("%s = %s (%v), want %s", key, raw, err, want)
		}
	}
}

// What the digest keeps of an earlier turn's steps, and what it never lets through.
func TestStepDigest(t *testing.T) {
	steps := []*Step{
		{CallID: "1", Name: "search_web", Args: map[string]any{"query": "rates"}, Status: "done", Output: "first line\nsecond line " + strings.Repeat("z", 600)},
		{CallID: "2", Name: "call_api", Args: map[string]any{"url": "https://x.test/?api_key=abcdef1234567890abcdef"}, Status: "done",
			Output: "password: hunter2hunter2 and key sk-" + strings.Repeat("a1", 20)},
		{CallID: "3", Name: "view_file", Args: `{"file":"f"}`, Status: "error", Output: "no such file"},
	}
	raw, _ := json.Marshal([]Part{{Type: "text", Text: "hi"}, {Type: "steps", Steps: steps}})
	full := stepDigest(raw, true)
	for _, want := range []string{"- search_web {\"query\":\"rates\"} -> first line second line", "- view_file {\"file\":\"f\"} -> failed: no such file"} {
		if !strings.Contains(full, want) {
			t.Errorf("digest lacks %q:\n%s", want, full)
		}
	}
	for _, secret := range []string{"hunter2hunter2", "abcdef1234567890abcdef", "sk-a1a1a1"} {
		if strings.Contains(full, secret) {
			t.Errorf("digest holds a secret %q:\n%s", secret, full)
		}
	}
	if strings.Contains(full, strings.Repeat("z", 400)) {
		t.Errorf("result not clipped:\n%s", full)
	}
	names := stepDigest(raw, false)
	if names != "[Tool calls made for the reply above:\n- search_web\n- call_api\n- view_file (failed)]" {
		t.Errorf("names only: %q", names)
	}
	if stepDigest(json.RawMessage(`[{"type":"text","text":"hi"}]`), true) != "" || stepDigest(nil, true) != "" {
		t.Error("a message without steps got a digest")
	}
	many := make([]*Step, 20)
	for i := range many {
		many[i] = &Step{Name: fmt.Sprint("t", i), Status: "done"}
	}
	raw, _ = json.Marshal([]Part{{Type: "steps", Steps: many}})
	if d := stepDigest(raw, true); !strings.Contains(d, "the first 5 are not listed") || strings.Contains(d, "t4 ") || !strings.Contains(d, "t19") {
		t.Errorf("long digest: %s", d)
	}
}

// Earlier assistant turns carry the digest into the next turn's prompt; the setting switches it off.
func TestEarlierTurnsCarryStepDigest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cid := h.chat(t)
	parts, _ := json.Marshal([]Part{{Type: "steps", Steps: []*Step{{CallID: "1", Name: "search_web", Args: map[string]any{"query": "rates"}, Status: "done", Output: "4.5 percent"}}}})
	if err := h.e.Chats.InsertMessage(ctx, &chats.Message{ID: "u1", ChatID: cid, Role: "user", Content: "rates?"}); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Chats.InsertMessage(ctx, &chats.Message{ID: "a1", ChatID: cid, ParentID: ptr("u1"), Role: "assistant", Content: "It is 4.5.", Parts: parts}); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Chats.InsertMessage(ctx, &chats.Message{ID: "u2", ChatID: cid, ParentID: ptr("a1"), Role: "user", Content: "and last year?"}); err != nil {
		t.Fatal(err)
	}
	load := func(local bool) string {
		x := &exec{chat: &chats.Chat{ID: cid}, parentID: "u2", req: &Request{UserMessage: &UserMessage{}}, m: &models.Resolved{Local: local}}
		if err := h.e.loadTurns(ctx, x); err != nil {
			t.Fatal(err)
		}
		return x.turns[1].msg.Content
	}
	if got := load(true); !strings.HasPrefix(got, "It is 4.5.\n\n[Tool calls made") || !strings.Contains(got, "4.5 percent") {
		t.Fatalf("local: %q", got)
	}
	if got := load(false); strings.Contains(got, "4.5 percent") || !strings.Contains(got, "- search_web") {
		t.Fatalf("hosted: %q", got)
	}
	h.set(t, map[string]any{"chat.context.digest": false})
	if got := load(true); got != "It is 4.5." {
		t.Fatalf("digest off: %q", got)
	}
}

// The system prompt and tool definitions count against the between-turn threshold.
func TestCompactionCountsSystemAndTools(t *testing.T) {
	ts := make([]turn, 10)
	for i := range ts {
		ts[i] = turn{ID: fmt.Sprint(i), msg: provider.Message{Role: []string{"user", "assistant"}[i%2], Content: strings.Repeat("x", 400)}}
	}
	if cut := splitForCompaction(ts, 1500, 30, 0); cut != 0 {
		t.Fatalf("turns alone are %d tokens, under 1500: cut %d", estimateTokens(ts), cut)
	}
	if cut := splitForCompaction(ts, 1500, 30, 800); cut == 0 {
		t.Fatal("a prompt and tools of 800 tokens should tip it over")
	}
	x := &exec{system: []string{strings.Repeat("s", 400)}, data: strings.Repeat("d", 400)}
	if n := x.overheadTokens([]provider.Tool{{Name: "t", Description: strings.Repeat("d", 400), Parameters: json.RawMessage(`{}`)}}); n < 300 {
		t.Fatalf("overhead %d", n)
	}
}

func TestCompactionThresholdFollowsWindow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if n := h.e.compactionThreshold(ctx, &exec{m: &models.Resolved{}, params: map[string]any{"context_window": 10000.0}}); n != 7000 {
		t.Errorf("declared window: %d", n)
	}
	if n := h.e.compactionThreshold(ctx, &exec{m: &models.Resolved{Local: true}, params: map[string]any{}}); n != 11200 {
		t.Errorf("local default window: %d", n)
	}
	if n := h.e.compactionThreshold(ctx, &exec{m: &models.Resolved{}, params: map[string]any{}}); n != 120000 {
		t.Errorf("hosted: %d", n)
	}
	if n := h.e.compactionThreshold(ctx, &exec{m: &models.Resolved{}, params: map[string]any{"compact_token_threshold": 5000.0, "context_window": 10000.0}}); n != 5000 {
		t.Errorf("explicit threshold: %d", n)
	}
}
