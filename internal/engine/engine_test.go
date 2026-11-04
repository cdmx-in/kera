package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/retrieval"
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

func (fakeRetriever) Search(_ context.Context, _ *auth.User, ids []string, q string, k int) ([]retrieval.Hit, error) {
	return []retrieval.Hit{{KnowledgeID: ids[0], FileID: "f2", FileName: "hit.md", Text: "retrieved for " + q, Score: 0.9}}, nil
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
		!strings.Contains(sys, `<source id="2" name="hit.md">`) || !strings.Contains(sys, "retrieved for echo system") {
		t.Fatalf("system prompt %q", sys)
	}
	if !strings.Contains(types(evs), "sources") {
		t.Fatalf("no sources event: %s", types(evs))
	}
}
