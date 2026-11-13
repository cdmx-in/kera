package engine

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/retrieval"
	"github.com/cdmx-in/kera/internal/sandbox"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
	"github.com/cdmx-in/kera/internal/tools"
)

// fakeModel is an OpenAI-compatible server with scripted answers:
// task prompts get JSON, "use tool" calls fake_tool once, "slow" streams
// slowly, anything else answers "hello world".
func fakeModel(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		if r.Method != http.MethodPost { // model info probes
			http.NotFound(w, r)
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		send := func(delta string) {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s}]}\n\n", delta)
			fl.Flush()
		}
		text := func(s string) { b, _ := json.Marshal(map[string]string{"content": s}); send(string(b)) }
		last := body.Messages[len(body.Messages)-1]
		sys := fmt.Sprint(body.Messages[0].Content)
		lastText := fmt.Sprint(last.Content)
		hasTool := false
		for _, tl := range body.Tools {
			hasTool = hasTool || tl.Function.Name == "fake_tool"
		}
		switch {
		case strings.Contains(sys, "Reply with JSON"):
			text(`{"title":"🧪 Test chat","tags":["testing"],"follow_ups":["more?"],"queries":["q"]}`)
		case strings.Contains(sys, "Summarise the conversation"):
			text("SUMMARY")
		case last.Role == "tool":
			text("done: " + lastText)
		case hasTool && strings.Contains(lastText, "use tool"):
			send(`{"tool_calls":[{"index":0,"id":"c1","function":{"name":"fake_tool","arguments":"{\"x\":1}"}}]}`)
		case strings.Contains(lastText, "slow"):
			for i := 0; i < 200; i++ {
				text("x")
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		case strings.Contains(lastText, "echo system"):
			text(sys)
		default:
			text("hello ")
			text("world")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeTools is a tool provider: fake_tool reports status, optionally asks.
type fakeTools struct {
	ask bool
}

func (f *fakeTools) ID() string { return "fake-server" }
func (f *fakeTools) Tools(context.Context, *auth.User) ([]tools.Spec, error) {
	return []tools.Spec{{Name: "fake_tool", Description: "test", Parameters: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (f *fakeTools) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	c.Emit(tools.Event{Type: "status", Data: map[string]any{"description": "working", "action": "tool_call"}})
	if f.ask {
		v, err := c.Ask(ctx, tools.AskRequest{Kind: "confirm", Title: "Go?", Message: "really"})
		if err != nil {
			return tools.Result{Content: "no answer"}, nil
		}
		return tools.Result{Content: "answer " + string(v)}, nil
	}
	return tools.Result{Content: "tool says hi", Sources: []tools.Source{{Name: "doc", URL: "https://example.com"}}}, nil
}

type harness struct {
	e    *Engine
	pool *pgxpool.Pool
	u    *auth.User
	srv  *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	keys, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	srv := fakeModel(t)
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, name, email, role) VALUES ('u1', 'Ada', 'ada@example.com', 'admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO connection (id, kind, base_url) VALUES ('fake', 'openai', $1)`, srv.URL); err != nil {
		t.Fatal(err)
	}
	st := settings.New(pool)
	e := New(pool, keys, st, models.New(pool, keys, st, ""), files.New(pool, st, t.TempDir()), &chats.Store{DB: pool}, "http://kera.test")
	pingEvery = 50 * time.Millisecond
	return &harness{e: e, pool: pool, u: &auth.User{ID: "u1", Name: "Ada", Email: "ada@example.com", Role: "admin"}, srv: srv}
}

func (h *harness) chat(t *testing.T) string {
	c := chats.Chat{UserID: "u1"}
	if err := h.e.Chats.Create(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// wait blocks until the run is done and returns its events.
func wait(t *testing.T, run *Run) []event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !run.Finished() {
		if time.Now().After(deadline) {
			t.Fatal("run did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	evs, _, _ := run.next(0)
	return evs
}

func types(evs []event) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return strings.Join(out, ",")
}

func TestRunPersistsStreamsAndRunsTasks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cid := h.chat(t)
	run, err := h.e.Start(ctx, h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid, ID: "a1",
		UserMessage: &UserMessage{ID: "q1", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := wait(t, run)
	h.e.Wait()
	got := types(evs)
	for _, want := range []string{"delta", "usage", "follow_ups", "title", "done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("events %s lack %s", got, want)
		}
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) || !strings.Contains(string(ev.Raw), `"messageId":"a1"`) {
			t.Fatalf("event %d: %s", i, ev.Raw)
		}
	}
	m, err := h.e.Chats.Message(ctx, cid, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Content != "hello world" || m.Status != "done" || *m.ParentID != "q1" || !strings.Contains(string(m.FollowUps), "more?") {
		t.Fatalf("stored %+v", m)
	}
	c, _ := h.e.Chats.Get(ctx, "u1", cid)
	tags, _ := h.e.Chats.Tags(ctx, cid)
	if c.Title != "🧪 Test chat" || *c.CurrentMessageID != "a1" || *c.Locality != "cloud" || len(tags) != 1 {
		t.Fatalf("chat %+v tags %v", c, tags)
	}
	var n int
	_ = h.pool.QueryRow(ctx, `SELECT sum(prompt_tokens) FROM usage WHERE message_id = 'a1'`).Scan(&n)
	if n != 10 {
		t.Fatalf("usage prompt tokens %d", n)
	}

	// second turn: the stored branch is the context, and the title stays
	run2, err := h.e.Start(ctx, h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid, ID: "a2",
		UserMessage: &UserMessage{ID: "q2", ParentID: ptr("a1"), Content: "again"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(types(wait(t, run2)), "title") {
		t.Fatal("title regenerated on a later turn")
	}
	branch, _ := h.e.Chats.Branch(ctx, cid, "a2")
	if len(branch) != 4 || branch[0].ID != "q1" || branch[3].ID != "a2" {
		t.Fatalf("branch %v", branch)
	}
}

func ptr(s string) *string { return &s }

func TestToolLoopWithServerTool(t *testing.T) {
	h := newHarness(t)
	tools.Register(&fakeTools{})
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", Temporary: true,
		ToolIDs: []string{"server:fake-server"}, Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"please use tool"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	evs := wait(t, run)
	got := types(evs)
	if !strings.Contains(got, "tool_call,status,sources,tool_result,snapshot,delta") {
		t.Fatalf("events %s", got)
	}
	if !strings.Contains(string(evs[0].Raw), `"chatId":"temp:`) {
		t.Fatalf("first temporary event lacks chatId: %s", evs[0].Raw)
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.state.Text() != "done: tool says hi" || len(run.tools) != 1 || run.state.Parts[0].Type != "steps" {
		t.Fatalf("text %q tools %v", run.state.Text(), run.tools)
	}
	st := run.state.Parts[0].Steps[0]
	if st.Status != "done" || st.Server != "fake-server" || len(st.StatusLines) != 1 || st.Output != "tool says hi" {
		t.Fatalf("step %+v", st)
	}
}

func TestAskAnswerAndCancel(t *testing.T) {
	h := newHarness(t)
	tools.Register(&fakeTools{ask: true})
	defer tools.Register(&fakeTools{})
	cid := h.chat(t)
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid, ID: "a1",
		ToolIDs: []string{"fake-server"}, UserMessage: &UserMessage{ID: "q1", Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	var callID string
	for deadline := time.Now().Add(5 * time.Second); callID == ""; {
		if time.Now().After(deadline) {
			t.Fatal("no confirm event")
		}
		evs, _, _ := run.next(0)
		for _, ev := range evs {
			if ev.Type == "confirm" {
				callID, _ = ev.Data["callId"].(string)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	// the pending card is persisted so a reload shows it
	m, _ := h.e.Chats.Message(context.Background(), cid, "a1")
	if !strings.Contains(string(m.PendingCalls), callID) {
		t.Fatalf("pending calls %s", m.PendingCalls)
	}
	if err := run.answer("nope", json.RawMessage("true")); err != ErrNoSuchCall {
		t.Fatalf("unknown call: %v", err)
	}
	if err := run.answer(callID, json.RawMessage("true")); err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	h.e.Wait()
	if run.stateText() != "done: answer true" {
		t.Fatalf("text %q", run.stateText())
	}
	m, _ = h.e.Chats.Message(context.Background(), cid, "a1")
	if string(m.PendingCalls) != "[]" {
		t.Fatalf("pending calls left: %s", m.PendingCalls)
	}

	// cancel a slow answer
	run2, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid, ID: "a2",
		UserMessage: &UserMessage{ID: "q2", ParentID: ptr("a1"), Content: "slow please"}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	run2.cancel(errCancelled)
	evs := wait(t, run2)
	h.e.Wait()
	if last := evs[len(evs)-1]; last.Type != "done" || last.Data["status"] != "stopped" {
		t.Fatalf("last event %s", last.Raw)
	}
	m, _ = h.e.Chats.Message(context.Background(), cid, "a2")
	if m.Status != "stopped" || m.Content == "" {
		t.Fatalf("stopped message %+v", m)
	}
}

// A model whose features say memory: false must not remember, whatever the user's own setting:
// the flag was ignored for memory (it only gated web search and code), so presets that switched
// memory off still had the memory tools.
func TestModelCanSwitchMemoryOff(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		v    any
		want bool
	}{{nil, true}, {true, true}, {"native", true}, {false, false}, {"false", false}, {"off", false}} {
		x := &exec{u: h.u, m: &models.Resolved{Features: map[string]any{"memory": c.v}}, req: &Request{}}
		if got := h.e.features(context.Background(), x)["memory"]; got != c.want {
			t.Errorf("model memory %v: features.memory = %v, want %v", c.v, got, c.want)
		}
	}
	x := &exec{u: h.u, m: &models.Resolved{}, req: &Request{}}
	if !h.e.features(context.Background(), x)["memory"] {
		t.Error("a model that says nothing about memory must leave it on")
	}
}

func TestShutdownSavesPartialAnswer(t *testing.T) {
	h := newHarness(t)
	cid := h.chat(t)
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid, ID: "a1",
		UserMessage: &UserMessage{ID: "q1", Content: "slow please"}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // a few deltas, no tool round, so no checkpoint yet
	h.e.Shutdown(5 * time.Second)
	if !run.Finished() {
		t.Fatal("Shutdown returned with the run still live")
	}
	m, err := h.e.Chats.Message(context.Background(), cid, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "error" || m.Content == "" || !strings.Contains(m.Error, "restarted") {
		t.Fatalf("interrupted message: status %q content %q error %v", m.Status, m.Content, m.Error)
	}
}

// A question that times out is cleared from the stored state too, so the
// Cowork list stops reporting needs_input while the run goes on.
func TestAskTimeoutCheckpoints(t *testing.T) {
	run := newRun("c1", "a1", "u1", "m1", true)
	saved := -1
	run.checkpoint = func() { run.mu.Lock(); saved = len(run.state.PendingCalls); run.mu.Unlock() }
	if _, err := run.ask(context.Background(), PendingCall{Kind: "input"}, 10*time.Millisecond); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("err %v", err)
	}
	if saved != 0 {
		t.Fatalf("last checkpoint saw %d pending calls", saved)
	}
}

func TestLocalLockAndBusy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cid := h.chat(t)
	if _, err := h.pool.Exec(ctx, `UPDATE chat SET locality = 'local' WHERE id = $1`, cid); err != nil {
		t.Fatal(err)
	}
	req := &Request{Model: "m1", StreamFormat: "events", ChatID: cid, UserMessage: &UserMessage{Content: "hi"}}
	if _, err := h.e.Start(ctx, h.u, req); err != nil {
		t.Fatalf("lock is off by default: %v", err)
	}
	h.e.Wait()
	if err := h.e.Settings.Set(ctx, "chat.local_lock", true, false); err != nil {
		t.Fatal(err)
	}
	req.UserMessage = &UserMessage{Content: "hi"}
	if _, err := h.e.Start(ctx, h.u, req); err != errLocalLock {
		t.Fatalf("want local_lock, got %v", err)
	}
	// a second run for a message that is still generating is refused
	cid2 := h.chat(t)
	r1, err := h.e.Start(ctx, h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid2, ID: "busy",
		UserMessage: &UserMessage{Content: "slow"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Start(ctx, h.u, &Request{Model: "m1", ChatID: cid2, ID: "busy", UserMessage: &UserMessage{Content: "x"}}); err != errBusy {
		t.Fatalf("want busy, got %v", err)
	}
	r1.cancel(errCancelled)
	h.e.Wait()
}

func TestSystemPromptVariablesAndHooks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, name, system_prompt) VALUES ('p1', 'm1', 'Preset', 'Hi {{USER_NAME}} <{{USER_EMAIL}}> on {{CURRENT_WEEKDAY}}')`); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env map[string]any
		_ = json.NewDecoder(r.Body).Decode(&env)
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/outlet") {
			fmt.Fprint(w, `{"message":{"content":"rewritten"},"events":[{"type":"status","data":{"description":"outlet ran","done":true}}]}`)
			return
		}
		fmt.Fprint(w, `{"request":{"params":{"temperature":0.1}}}`)
	}))
	defer hook.Close()
	key, _ := h.e.Keys.EncryptString("hook-key")
	if _, err := h.pool.Exec(ctx, `INSERT INTO hook_server (id, url, key_enc, events, scope) VALUES ('h1', $1, $2, '{inlet,outlet}', 'global')`, hook.URL, key); err != nil {
		t.Fatal(err)
	}
	run, err := h.e.Start(ctx, h.u, &Request{Model: "p1", Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"echo system"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	if got := run.stateText(); got != "rewritten" {
		t.Fatalf("outlet did not rewrite: %q", got)
	}
	run.mu.Lock()
	echoed := run.roundText
	run.mu.Unlock()
	if !strings.HasPrefix(echoed, "Hi Ada <ada@example.com> on "+time.Now().UTC().Weekday().String()[:2]) {
		t.Fatalf("system prompt %q", echoed)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seen, "|") != "/inlet Bearer hook-key|/outlet Bearer hook-key" {
		t.Fatalf("hook calls %v", seen)
	}
}

func TestOpenAICompatibleOutput(t *testing.T) {
	h := newHarness(t)
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/chat/completions", strings.NewReader(body))
		r = r.WithContext(auth.WithUser(r.Context(), h.u))
		w := httptest.NewRecorder()
		h.e.completions(w, r)
		return w
	}
	w := call(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`)
	var resp struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Usage map[string]int `json:"usage"`
		Kera  struct {
			RunID string `json:"run_id"`
		} `json:"kera"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Choices[0].Message.Content != "hello world" || resp.Kera.RunID == "" || resp.Usage["total_tokens"] != 12 {
		t.Fatalf("non-stream %d %s", w.Code, w.Body)
	}
	// passthrough: the caller's tool is returned, not executed
	w = call(`{"model":"m1","stream":true,"messages":[{"role":"user","content":"use tool"}],
		"tools":[{"type":"function","function":{"name":"fake_tool","parameters":{"type":"object"}}}]}`)
	out := w.Body.String()
	if !strings.Contains(out, `"tool_calls":[{"function":{"arguments":"{\"x\":1}","name":"fake_tool"}`) ||
		!strings.Contains(out, `"finish_reason":"tool_calls"`) || !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Fatalf("stream %s", out)
	}
	w = call(`{"model":"nope","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"error":{"code":"model_not_found"`) {
		t.Fatalf("error shape %d %s", w.Code, w.Body)
	}
}

func TestResumeReplaysAfterSeqAndSnapshot(t *testing.T) {
	r := newRun("c1", "m1", "u1", "model", true)
	r.delta("a")
	r.delta("b")
	r.toolCall("t1", "x", "", json.RawMessage(`{}`))
	r.toolResult("t1", "ok", nil, false)
	r.done("done")
	read := func(after int64, snap bool) []map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		streamNative(w, req, r, after, snap)
		if w.Header().Get("X-Accel-Buffering") != "no" || w.Header().Get("Content-Type") != "text/event-stream" {
			t.Fatalf("headers %v", w.Header())
		}
		var out []map[string]any
		sc := bufio.NewScanner(w.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var m map[string]any
				_ = json.Unmarshal([]byte(d), &m)
				out = append(out, m)
			}
		}
		return out
	}
	evs := read(2, true)
	if len(evs) != 3 || evs[0]["type"] != "tool_call" || evs[0]["seq"] != float64(3) || evs[2]["type"] != "done" {
		t.Fatalf("after 2: %v", evs)
	}
	evs = read(0, true)
	if len(evs) != 2 || evs[0]["type"] != "snapshot" || evs[0]["seq"] != float64(4) || evs[1]["type"] != "done" {
		t.Fatalf("snapshot resume: %v", evs)
	}
	parts := evs[0]["parts"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["text"] != "ab" {
		t.Fatalf("snapshot parts %v", parts)
	}
}

func TestRunTokens(t *testing.T) {
	k, _ := secret.Derive(strings.Repeat("s", 32))
	tok, _ := signRunToken(k, "r1", "u1", time.Now().Add(time.Minute))
	if !verifyRunToken(k, "r1", tok) || verifyRunToken(k, "r2", tok) || verifyRunToken(k, "r1", tok+"x") {
		t.Fatal("token check")
	}
	old, _ := signRunToken(k, "r1", "u1", time.Now().Add(-time.Second))
	if verifyRunToken(k, "r1", old) {
		t.Fatal("expired token accepted")
	}
}

func TestRenderVars(t *testing.T) {
	u := &auth.User{Name: "Ada", Email: "a@x", Role: "user"}
	now := time.Date(2026, 11, 4, 20, 30, 0, 0, time.UTC)
	got := renderVars("{{CURRENT_DATE}} {{CURRENT_TIME}} {{CURRENT_WEEKDAY}} {{CURRENT_TIMEZONE}} {{USER_NAME}} {{USER_GROUPS}} {{USER_LANGUAGE}}",
		u, userProfile{Timezone: "Asia/Kolkata", GroupNames: []string{"HR", "PM"}, Language: "en-IN"}, now)
	if got != "2026-11-05 02:00:00 Thursday Asia/Kolkata Ada HR, PM en-IN" {
		t.Fatalf("got %q", got)
	}
	if renderVars("x {{CURRENT_TIMEZONE}}", u, userProfile{Timezone: "bogus"}, now) != "x UTC" {
		t.Fatal("bad timezone not replaced by UTC")
	}
}

func TestCompactionSplit(t *testing.T) {
	mk := func(n, size int) []turn {
		var ts []turn
		for i := range n {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			ts = append(ts, turn{ID: fmt.Sprint(i), msg: providerMsg(role, strings.Repeat("x", size))})
		}
		return ts
	}
	if splitForCompaction(mk(10, 100), 1000, 30) != 0 {
		t.Fatal("compacted under the threshold")
	}
	if cut := splitForCompaction(mk(11, 4000), 1000, 30); cut != 8 {
		t.Fatalf("cut %d, want 8 (keep 3 of 11)", cut)
	}
	if splitForCompaction(mk(3, 40000), 1000, 30) != 0 {
		t.Fatal("compacted a 3-turn chat")
	}
}

func TestCompactionStoresSummaryAndCutsNextTurn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for k, v := range map[string]any{"chat.compaction.enable": true, "chat.compaction.threshold": 100, "chat.compaction.retention": 30} {
		if err := h.e.Settings.Set(ctx, k, v, false); err != nil {
			t.Fatal(err)
		}
	}
	cid := h.chat(t)
	var parent *string
	for i := range 6 {
		role := map[int]string{0: "user", 1: "assistant"}[i%2]
		id := fmt.Sprint("m", i)
		if err := h.e.Chats.InsertMessage(ctx, &chats.Message{ID: id, ChatID: cid, ParentID: parent, Role: role, Content: strings.Repeat("y", 200)}); err != nil {
			t.Fatal(err)
		}
		parent = ptr(id)
	}
	run, err := h.e.Start(ctx, h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid, ID: "a",
		UserMessage: &UserMessage{ID: "q", ParentID: parent, Content: "next"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := wait(t, run)
	h.e.Wait()
	if !strings.Contains(types(evs), "compaction,compaction") {
		t.Fatalf("events %s", types(evs))
	}
	var marked string
	_ = h.pool.QueryRow(ctx, `SELECT id FROM message WHERE chat_id = $1 AND context_summary = 'SUMMARY'`, cid).Scan(&marked)
	if marked == "" {
		t.Fatal("summary not stored")
	}
	x := &exec{chat: &chats.Chat{ID: cid}, parentID: "a", req: &Request{UserMessage: &UserMessage{}}}
	if err := h.e.loadTurns(ctx, x); err != nil {
		t.Fatal(err)
	}
	if x.turns[0].msg.Role != "system" || !strings.Contains(x.turns[0].msg.Content, "SUMMARY") || len(x.turns) >= 8 {
		t.Fatalf("turns after summary: %d first %+v", len(x.turns), x.turns[0].msg)
	}
}

func providerMsg(role, content string) provider.Message {
	return provider.Message{Role: role, Content: content}
}

type fakeRetriever struct{}

var searchedLocal atomic.Bool // IsLocalRun of the last fakeRetriever.Search

func (fakeRetriever) Search(ctx context.Context, _ *auth.User, ids []string, q string, k int) ([]retrieval.Hit, error) {
	searchedLocal.Store(retrieval.IsLocalRun(ctx))
	return []retrieval.Hit{{KnowledgeID: ids[0], FileID: "f2", FileName: "hit.md", Text: "retrieved for " + q, Score: 0.9,
		Headers: []string{"Guide", "Refunds"}}}, nil
}
func (fakeRetriever) Documents(_ context.Context, _ *auth.User, id string) ([]retrieval.Doc, error) {
	return []retrieval.Doc{{KnowledgeID: id, FileID: "f1", Name: "policy.md", Text: "whole policy"}}, nil
}
func (fakeRetriever) Resolve(_ context.Context, _ *auth.User, refs []string) ([]retrieval.Collection, error) {
	var out []retrieval.Collection
	for _, r := range refs {
		switch r {
		case "Policies":
			out = append(out, retrieval.Collection{ID: "k1", Name: "Policies"})
		case "k2":
			out = append(out, retrieval.Collection{ID: "k2", Name: "Docs"})
		}
	}
	return out, nil
}
func (fakeRetriever) List(context.Context, *auth.User) ([]retrieval.Collection, error) {
	return nil, nil
}

func TestKnowledgeFullAndRetrieve(t *testing.T) {
	h := newHarness(t)
	retrieval.Register(fakeRetriever{})
	defer retrieval.Register(nil)
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, knowledge) VALUES ('kb', 'm1', '[{"name":"Policies","mode":"full"}]')`); err != nil {
		t.Fatal(err)
	}
	run, err := h.e.Start(ctx, h.u, &Request{Model: "kb", Files: []FileAttach{{Type: "collection", ID: "k2"}},
		Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"echo system"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	evs := wait(t, run)
	sys := run.stateText()
	if !strings.Contains(sys, `<source id="1" name="policy.md">`) || !strings.Contains(sys, "whole policy") ||
		!strings.Contains(sys, `<source id="2" name="hit.md" section="Guide &gt; Refunds">`) || !strings.Contains(sys, "retrieved for echo system") {
		t.Fatalf("system prompt %q", sys)
	}
	if !strings.Contains(types(evs), "sources") {
		t.Fatalf("no sources event: %s", types(evs))
	}
	if searchedLocal.Load() {
		t.Fatal("a cloud model's search was marked local")
	}
}

// Knowledge without a mode is searched by the model itself when it also has
// tool servers (nothing injected); an explicit retrieve still injects, and
// injected passages carry their section breadcrumb.
func TestKnowledgeAutoMode(t *testing.T) {
	h := newHarness(t)
	retrieval.Register(fakeRetriever{})
	defer retrieval.Register(nil)
	tools.Register(&fakeTools{})
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, knowledge, tools) VALUES
		('auto', 'm1', '[{"name":"Policies"}]', '{"servers":["fake-server"]}'),
		('pinned', 'm1', '[{"name":"Policies","mode":"retrieve"}]', '{"servers":["fake-server"]}'),
		('plain', 'm1', '[{"name":"Policies","mode":"auto"}]', '{}')`); err != nil {
		t.Fatal(err)
	}
	for model, inject := range map[string]bool{"auto": false, "pinned": true, "plain": true} {
		run, err := h.e.Start(ctx, h.u, &Request{Model: model, Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"echo system"`)}}})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, run)
		sys := run.stateText()
		if got := strings.Contains(sys, `<source id="1" name="hit.md" section="Guide &gt; Refunds">`); got != inject {
			t.Errorf("%s: injected=%v, want %v: %q", model, got, inject, sys)
		}
	}
	// with a working reranker, auto injects even next to tool servers
	retrieval.Register(rerankingRetriever{})
	run, err := h.e.Start(ctx, h.u, &Request{Model: "auto", Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"echo system"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	if !strings.Contains(run.stateText(), `name="hit.md"`) {
		t.Errorf("reranked auto not injected: %q", run.stateText())
	}
}

type rerankingRetriever struct{ fakeRetriever }

func (rerankingRetriever) Reranks(context.Context) bool { return true }

// Runs on a local (Ollama) model mark the retrieval context as local.
func TestLocalRunMarksRetrieval(t *testing.T) {
	h := newHarness(t)
	retrieval.Register(fakeRetriever{})
	defer retrieval.Register(nil)
	ctx := context.Background()
	ol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"model":"loc"}]}`))
			return
		}
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer ol.Close()
	if _, err := h.pool.Exec(ctx, `INSERT INTO connection (id, kind, base_url) VALUES ('ol', 'ollama', $1)`, ol.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, knowledge) VALUES ('kb-local', 'loc', '[{"name":"Policies"}]')`); err != nil {
		t.Fatal(err)
	}
	run, err := h.e.Start(ctx, h.u, &Request{Model: "kb-local", Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	if !searchedLocal.Load() {
		t.Fatal("local run not marked")
	}
}

// A chat link grants read access, so attaching someone else's file id must not link it.
func TestAttachingAnotherUsersFileDoesNotLinkIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO "user" (id, name, email, role) VALUES ('u2', 'Bob', 'bob@example.com', 'user')`,
		`INSERT INTO file (id, user_id, filename, path) VALUES ('f-ada', 'u1', 'secret.txt', '/x')`,
		`INSERT INTO file (id, user_id, filename, path, status, text) VALUES ('f-bob', 'u2', 'mine.txt', '/y', 'completed', 'hello')`,
	} {
		if _, err := h.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.e.Settings.Set(ctx, "models.base_public", true, false); err != nil {
		t.Fatal(err)
	}
	bob := &auth.User{ID: "u2", Name: "Bob", Email: "bob@example.com", Role: "user"}
	c := chats.Chat{UserID: "u2"}
	if err := h.e.Chats.Create(ctx, &c); err != nil {
		t.Fatal(err)
	}
	run, err := h.e.Start(ctx, bob, &Request{Model: "m1", StreamFormat: "events", ChatID: c.ID,
		UserMessage: &UserMessage{Content: "hi", Files: json.RawMessage(`[{"type":"file","id":"f-ada"},{"type":"file","id":"f-bob"}]`)}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	h.e.Wait()
	if ok, _ := h.e.Files.CanRead(ctx, bob, "f-ada"); ok {
		t.Fatal("bob can read ada's file after attaching its id")
	}
	var linked []string
	rows, _ := h.pool.Query(ctx, `SELECT file_id FROM chat_file WHERE chat_id = $1`, c.ID)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		linked = append(linked, id)
	}
	rows.Close()
	if len(linked) != 1 || linked[0] != "f-bob" {
		t.Fatalf("linked %v, want only f-bob", linked)
	}
}

// Embedding and rerank models cannot be used for a chat run.
func TestNonChatModelRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"nomic-embed-text"}]}`))
	}))
	defer srv.Close()
	if _, err := h.pool.Exec(ctx, `INSERT INTO connection (id, kind, base_url, prefix) VALUES ('emb', 'openai', $1, 'e')`, srv.URL); err != nil {
		t.Fatal(err)
	}
	_, err := h.e.Start(ctx, h.u, &Request{Model: "e.nomic-embed-text", Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}})
	if !errors.Is(err, models.ErrNotChat) {
		t.Fatalf("got %v", err)
	}
}

// fakeSandbox stands in for builtin:code so the run offers execute_code.
type fakeSandbox struct{}

func (fakeSandbox) ID() string { return sandbox.ID }
func (fakeSandbox) Tools(context.Context, *auth.User) ([]tools.Spec, error) {
	return []tools.Spec{{Name: "execute_code", Parameters: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (fakeSandbox) Execute(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{}, nil
}

// The code interpreter's prompt joins the system prompt when execute_code is offered.
func TestCodeInterpreterPrompt(t *testing.T) {
	h := newHarness(t)
	tools.Register(fakeSandbox{})
	ctx := context.Background()
	if err := h.e.Settings.Set(ctx, "code_interpreter.prompt", "RUN-CODE-FOR-REAL", true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, features, default_features) VALUES ('ci', 'm1', '{"code_interpreter": true}', '{code_interpreter}')`); err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]bool{"ci": true, "m1": false} {
		run, err := h.e.Start(ctx, h.u, &Request{Model: model, Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"echo system"`)}}})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, run)
		if got := strings.Contains(run.stateText(), "RUN-CODE-FOR-REAL"); got != want {
			t.Errorf("%s: prompt included = %v: %q", model, got, run.stateText())
		}
	}
}

// A cowork chat gets the cowork prompt right after the preset prompt; a
// plain chat does not, and a user without features.cowork cannot run a task.
func TestCoworkPromptAndPermission(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, name, system_prompt) VALUES ('p1', 'm1', 'Preset', 'PRESET PERSONA')`); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Settings.Set(ctx, "cowork.prompt", "COWORK MARKER for {{USER_NAME}}", false); err != nil {
		t.Fatal(err)
	}
	system := func(u *auth.User, mode string) (string, error) {
		c := chats.Chat{UserID: u.ID, Mode: mode}
		if err := h.e.Chats.Create(ctx, &c); err != nil {
			t.Fatal(err)
		}
		run, err := h.e.Start(ctx, u, &Request{Model: "p1", StreamFormat: "events", ChatID: c.ID,
			UserMessage: &UserMessage{Content: "echo system"}})
		if err != nil {
			return "", err
		}
		wait(t, run)
		h.e.Wait()
		run.mu.Lock()
		defer run.mu.Unlock()
		return run.roundText, nil
	}
	sys, err := system(h.u, "cowork")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sys, "PRESET PERSONA\n\nCOWORK MARKER for Ada") {
		t.Fatalf("cowork system prompt %q", sys)
	}
	if sys, _ := system(h.u, "chat"); strings.Contains(sys, "COWORK") {
		t.Fatalf("plain chat got the cowork prompt: %q", sys)
	}
	for _, q := range []string{`INSERT INTO "user" (id, email) VALUES ('u2', 'b@example.com')`,
		`INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission) VALUES ('g1', 'model', 'p1', 'user', 'u2', 'read')`} {
		if _, err := h.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	u2 := &auth.User{ID: "u2", Role: "user", Permissions: auth.Permissions{"features": map[string]any{"cowork": false}}}
	if _, err := system(u2, "cowork"); err != errCoworkForbidden {
		t.Fatalf("task without features.cowork: %v", err)
	}
	if _, err := h.e.Start(ctx, u2, &Request{Model: "p1", StreamFormat: "events", Temporary: true,
		UserMessage: &UserMessage{Content: "hi"}}); err != errTempForbidden {
		t.Fatalf("temporary chat without features.temporary_chat: %v", err)
	}
	if _, err := system(u2, "chat"); err != nil {
		t.Fatalf("plain chat for the same user: %v", err)
	}
}

// shotTools returns the same screenshot ids on every call.
type shotTools struct{ ids []string }

func (shotTools) ID() string { return "shot-server" }
func (shotTools) Tools(context.Context, *auth.User) ([]tools.Spec, error) {
	return []tools.Spec{{Name: "shot", Parameters: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (s shotTools) Execute(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{Content: "took a screenshot", Images: s.ids}, nil
}

// Tool images reach a model with vision: true as one trailing user turn per
// round holding the round's last image (the last keepToolImages turns kept),
// show on the step only, and become a text note for a model without vision
// set (false or no capabilities) and for types like SVG. Files the run user
// cannot read are dropped.
func TestToolImages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const rounds = 5
	var mu sync.Mutex
	var reqs [][]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"},{"id":"m2"}]}`)
			return
		}
		if r.Method != http.MethodPost { // model info probes
			http.NotFound(w, r)
			return
		}
		var body struct {
			Messages []map[string]any
			Tools    []any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Tools) == 0 { // title and follow-up tasks
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		mu.Lock()
		reqs = append(reqs, body.Messages)
		mu.Unlock()
		calls := 0
		for _, m := range body.Messages {
			if m["tool_calls"] != nil {
				calls++
			}
		}
		delta := `{"content":"ok"}`
		if calls < rounds { // two parallel calls per round
			delta = fmt.Sprintf(`{"tool_calls":[{"index":0,"id":"c%d","function":{"name":"shot","arguments":"{}"}},`+
				`{"index":1,"id":"c%d-b","function":{"name":"shot","arguments":"{}"}}]}`, calls, calls)
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", delta)
	}))
	defer srv.Close()
	for _, q := range []string{
		`INSERT INTO "user" (id, name, email, role) VALUES ('u2', 'Bob', 'bob@example.com', 'user')`,
		`INSERT INTO file (id, user_id, filename, content_type, path) VALUES ('f-ada', 'u1', 'ada.png', 'image/png', '/x')`,
		`INSERT INTO model (id, capabilities) VALUES ('s.m1', '{"vision": true}')`,
		`INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission) VALUES ('g1', 'model', 's.m1', 'user', 'u2', 'read')`,
		`INSERT INTO model (id, base_model_id, capabilities) VALUES ('blind', 's.m1', '{"vision": false}')`,
	} {
		if _, err := h.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO connection (id, kind, base_url, prefix) VALUES ('shots', 'openai', $1, 's')`, srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Settings.Set(ctx, "models.base_public", true, false); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range [][3]string{{"shot.png", "image/png", "PNGDATA"}, {"chart.svg", "image/svg+xml", "<svg/>"}, {"shot.jpg", "image/jpeg", "JPEGDATA"}} {
		f, err := h.e.Files.Create(ctx, "u2", n[0], n[1], strings.NewReader(n[2]), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	tools.Register(shotTools{ids: []string{ids[0], "f-ada", ids[1], ids[2]}})
	jpeg := base64.StdEncoding.EncodeToString([]byte("JPEGDATA"))
	bob := &auth.User{ID: "u2", Name: "Bob", Email: "bob@example.com", Role: "user",
		Permissions: auth.Permissions{"features": map[string]any{"temporary_chat": true}}} // the default every user gets
	start := func(u *auth.User, model string) *Run {
		mu.Lock()
		reqs = nil
		mu.Unlock()
		run, err := h.e.Start(ctx, u, &Request{Model: model, StreamFormat: "events", Temporary: true, ToolIDs: []string{"shot-server"},
			Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"look"`)}}})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, run)
		return run
	}
	imageURLs := func(m map[string]any) []string {
		var out []string
		parts, _ := m["content"].([]any)
		for _, p := range parts {
			if p := p.(map[string]any); p["type"] == "image_url" {
				out = append(out, fmt.Sprint(p["image_url"]))
			}
		}
		return out
	}
	imageParts := func(m map[string]any) int { return len(imageURLs(m)) }

	run := start(bob, "s.m1")
	mu.Lock()
	if len(reqs) != rounds+1 {
		t.Fatalf("%d model requests", len(reqs))
	}
	for i, msgs := range reqs[1:] {
		last := msgs[len(msgs)-1]
		if u := imageURLs(last); last["role"] != "user" || len(u) != 1 || !strings.Contains(u[0], "image/jpeg;base64,"+jpeg) {
			t.Fatalf("request %d: last message %v", i+1, last)
		}
		if tm := msgs[len(msgs)-2]; tm["role"] != "tool" || !strings.Contains(fmt.Sprint(tm["content"]), "(image not shown: image/svg+xml is not a supported image type)") {
			t.Fatalf("request %d: tool message %v", i+1, tm)
		}
	}
	withImages, removed := 0, 0
	for _, m := range reqs[rounds] {
		if imageParts(m) > 0 {
			withImages++
		}
		if m["content"] == "[earlier screenshot removed]" {
			removed++
		}
	}
	mu.Unlock()
	if withImages != keepToolImages || removed != rounds-keepToolImages {
		t.Fatalf("final request: %d image turns, %d removed", withImages, removed)
	}
	run.mu.Lock()
	st := run.state.Parts[0].Steps[0]
	if len(st.Files) != 3 || st.Files[0].ID != ids[0] || st.Files[0].Type != "image" || len(run.state.Files) != 0 {
		t.Fatalf("step files %v, message files %v", st.Files, run.state.Files)
	}
	run.mu.Unlock()

	for _, model := range []string{"blind", "s.m2"} { // vision false; no capabilities at all
		start(h.u, model)
		mu.Lock()
		for _, msgs := range reqs {
			for _, m := range msgs {
				if imageParts(m) > 0 {
					t.Fatalf("%s got an image: %v", model, m)
				}
			}
		}
		if last := reqs[1][len(reqs[1])-1]; last["role"] != "tool" || !strings.Contains(fmt.Sprint(last["content"]), "(image not shown: this model has no vision)") {
			t.Fatalf("%s's tool message %v", model, last)
		}
		mu.Unlock()
	}
}

// The upload callback stores the body as a file of the run's user.
func TestCallbackFileUpload(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	run := newRun("c1", "a1", "u1", "m1", false)
	if err := h.e.runs.add(run); err != nil {
		t.Fatal(err)
	}
	tok, _ := signRunToken(h.e.Keys, run.ID, run.UserID, time.Now().Add(time.Minute))
	post := func(tok, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/runs/"+run.ID+"/files?name=screen.jpg", strings.NewReader(body))
		r.SetPathValue("run", run.ID)
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Content-Type", "image/jpeg")
		w := httptest.NewRecorder()
		h.e.callbackFile(w, r)
		return w
	}
	w := post(tok, "JPEG")
	var got struct{ ID, Name string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Name != "screen.jpg" {
		t.Fatalf("upload %d %s", w.Code, w.Body)
	}
	f, err := h.e.Files.Get(ctx, got.ID)
	if err != nil || f.UserID != "u1" || f.ContentType != "image/jpeg" || f.Meta["run_id"] != run.ID {
		t.Fatalf("file %+v %v", f, err)
	}
	if w := post(tok+"x", "JPEG"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", w.Code)
	}
	if err := h.e.Settings.Set(ctx, "files.max_mb", 1, false); err != nil {
		t.Fatal(err)
	}
	if w := post(tok, strings.Repeat("x", 1<<20+1)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d %s", w.Code, w.Body)
	}
	run.mu.Lock()
	run.uploads = maxRunUploads
	run.mu.Unlock()
	if w := post(tok, "JPEG"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("over the run's upload cap: %d %s", w.Code, w.Body)
	}
}

// localOnlyTools is fakeTools marked local-only (like Gmail or Docs).
type localOnlyTools struct{ fakeTools }

func (*localOnlyTools) ID() string      { return "fake-local" }
func (*localOnlyTools) LocalOnly() bool { return true }

func TestLocalOnlyServerSkippedForHostedModel(t *testing.T) {
	h := newHarness(t)
	tools.Register(&localOnlyTools{})
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", Temporary: true,
		ToolIDs: []string{"server:fake-local"}, Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"please use tool"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := types(wait(t, run)); strings.Contains(got, "tool_call") {
		t.Fatalf("hosted model got a local-only tool: %s", got)
	}
}

// modelOnlyTools is fakeTools marked model-only (confidential assistant tools).
type modelOnlyTools struct{ fakeTools }

func (*modelOnlyTools) ID() string      { return "fake-model-only" }
func (*modelOnlyTools) ModelOnly() bool { return true }

func TestModelOnlyServerOnlyThroughItsModel(t *testing.T) {
	h := newHarness(t)
	tools.Register(&modelOnlyTools{})
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, tools) VALUES ('conf', 'm1', '{"servers":["fake-model-only"]}')`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		model string
		ids   []string
		want  bool
	}{
		{"m1", []string{"server:fake-model-only"}, false}, // picked by hand onto another model: refused
		{"conf", nil, true}, // the model's own server
		{"conf", []string{"server:fake-model-only"}, true}, // the chat UI sends the model's servers as tool_ids
	} {
		run, err := h.e.Start(ctx, h.u, &Request{Model: c.model, StreamFormat: "events", Temporary: true, ToolIDs: c.ids,
			Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"please use tool"`)}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := types(wait(t, run)); strings.Contains(got, "tool_call") != c.want {
			t.Fatalf("%s %v: tool_call=%v, events %s", c.model, c.ids, !c.want, got)
		}
	}
}

// fileTools returns a new file on its first call and records the FileIDs every call sees.
type fileTools struct {
	mu   sync.Mutex
	file string
	seen [][]string
}

func (*fileTools) ID() string { return "file-server" }
func (*fileTools) Tools(context.Context, *auth.User) ([]tools.Spec, error) {
	return []tools.Spec{{Name: "fetch", Parameters: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (f *fileTools) Execute(_ context.Context, c tools.Call) (tools.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, append([]string{}, c.FileIDs...))
	if len(f.seen) == 1 {
		return tools.Result{Content: "fetched", Inputs: []string{f.file}}, nil
	}
	return tools.Result{Content: "used"}, nil
}

// A file a tool returns as an input is in the FileIDs of later tool calls in
// the same run (e.g. execute_code opening a .pptx copied from a linked deck),
// shown on its step and not attached to the answer.
func TestToolFilesReachLaterCalls(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Messages []map[string]any
			Tools    []any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls := 0
		for _, m := range body.Messages {
			if m["tool_calls"] != nil {
				calls++
			}
		}
		delta := `{"content":"ok"}`
		if len(body.Tools) > 0 && calls < 2 {
			delta = fmt.Sprintf(`{"tool_calls":[{"index":0,"id":"c%d","function":{"name":"fetch","arguments":"{}"}}]}`, calls)
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", delta)
	}))
	defer srv.Close()
	if _, err := h.pool.Exec(ctx, `INSERT INTO connection (id, kind, base_url, prefix) VALUES ('files', 'openai', $1, 'f')`, srv.URL); err != nil {
		t.Fatal(err)
	}
	f, err := h.e.Files.Create(ctx, h.u.ID, "deck.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", strings.NewReader("PK"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ft := &fileTools{file: f.ID}
	tools.Register(ft)
	run, err := h.e.Start(ctx, h.u, &Request{Model: "f.m1", StreamFormat: "events", Temporary: true, ToolIDs: []string{"file-server"},
		Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"edit my deck"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.seen) != 2 || len(ft.seen[0]) != 0 || !slices.Contains(ft.seen[1], f.ID) {
		t.Fatalf("FileIDs per call: %v (want [] then [%s])", ft.seen, f.ID)
	}
	// an input is on the step that fetched it, never attached to the answer
	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.state.Files) != 0 {
		t.Fatalf("input attached to the answer: %+v", run.state.Files)
	}
	if st := run.state.Parts[0].Steps[0]; len(st.Files) != 1 || st.Files[0].ID != f.ID {
		t.Fatalf("first step files %+v", st.Files)
	}
}

// The links callback returns only URLs from the user's own messages.
func TestCallbackLinks(t *testing.T) {
	h := newHarness(t)
	run := newRun("c1", "a1", "u1", "m1", false)
	if err := h.e.runs.add(run); err != nil {
		t.Fatal(err)
	}
	run.setUserLinks([]turn{
		{msg: provider.Message{Role: "user", Content: "my deck (https://docs.google.com/presentation/d/AAA/edit?slide=id.p) and https://x.test/a"}},
		{msg: provider.Message{Role: "assistant", Content: "see https://evil.test/b"}},
		{msg: provider.Message{Role: "user", Content: "again https://x.test/a"}},
	})
	tok, _ := signRunToken(h.e.Keys, run.ID, run.UserID, time.Now().Add(time.Minute))
	get := func(tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/runs/"+run.ID+"/links", nil)
		r.SetPathValue("run", run.ID)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.e.callbackLinks(w, r)
		return w
	}
	var got struct{ Links []string }
	if w := get(tok); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("links %d %s", w.Code, w.Body)
	}
	want := []string{"https://docs.google.com/presentation/d/AAA/edit?slide=id.p", "https://x.test/a"}
	if !slices.Equal(got.Links, want) {
		t.Fatalf("links %v, want %v", got.Links, want)
	}
	if w := get(tok + "x"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", w.Code)
	}
}

// Placeholder values in optional, default-null parameters are dropped; real
// values, required parameters and non-null defaults are kept.
func TestDropUnsetArgs(t *testing.T) {
	schema := json.RawMessage(`{"properties":{
		"id":{"type":"string","default":null},
		"bold":{"type":"boolean","default":null},
		"merge_cells":{"type":"boolean","default":null},
		"dropdown_values":{"type":"array","default":null},
		"font_size":{"type":"integer","default":null},
		"color":{"type":"string","default":null},
		"wrap":{"type":"string","default":null},
		"include_notes":{"type":"boolean","default":false}},
		"required":["id"]}`)
	got := dropUnsetArgs(schema, json.RawMessage(`{"id":"","bold":false,"merge_cells":false,"dropdown_values":[],
		"font_size":0,"color":"","wrap":"WRAP","include_notes":false}`))
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"id": "", "wrap": "WRAP", "include_notes": false}
	if fmt.Sprint(m) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", m, want)
	}
	same := json.RawMessage(`{"wrap":"CLIP"}`)
	if string(dropUnsetArgs(schema, same)) != string(same) {
		t.Fatal("args without placeholders must pass through unchanged")
	}
}
