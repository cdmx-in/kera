package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/tools"
)

// scriptModel is an OpenAI-compatible model whose tool calls the user message writes: "calls: a,b ; c" asks for a
// and b in the first round and c in the second. After the last round it answers "done: " and every tool result it was
// sent, so a test reads in the answer what the model was told. "hang" in the message streams "partial " and then
// waits for the gate.
type scriptModel struct {
	mu       sync.Mutex
	gate     chan struct{} // closed: hanging answers go on
	requests []string      // every conversation received, as "role: text" lines
}

func newScriptModel() *scriptModel { return &scriptModel{gate: make(chan struct{})} }

func (s *scriptModel) open() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.gate:
	default:
		close(s.gate)
	}
}

func (s *scriptModel) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *scriptModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
		return
	}
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Messages []struct {
			Role      string            `json:"role"`
			Content   any               `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	send := func(delta any) {
		b, _ := json.Marshal(delta)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s}]}\n\n", b)
		fl.Flush()
	}
	text := func(v string) { send(map[string]string{"content": v}) }
	var user, sys string
	var results, all []string
	rounds := 0
	for _, m := range body.Messages {
		c := ""
		if m.Content != nil {
			c = fmt.Sprint(m.Content)
		}
		all = append(all, m.Role+": "+c)
		switch {
		case m.Role == "system":
			sys += c
		case m.Role == "user" && strings.Contains(c, "calls:"):
			user, results, rounds = c, nil, 0
		case m.Role == "user" && user == "":
			user = c
		case m.Role == "tool":
			results = append(results, c)
		case len(m.ToolCalls) > 0:
			rounds++
		}
	}
	s.mu.Lock()
	s.requests = append(s.requests, strings.Join(all, "\n"))
	gate := s.gate
	s.mu.Unlock()
	switch {
	case strings.Contains(sys, "Reply with JSON"):
		text(`{"title":"Durable","tags":["t"],"follow_ups":["more?"],"queries":["q"]}`)
	case strings.Contains(user, "calls:"):
		_, script, _ := strings.Cut(user, "calls:")
		plan := strings.Split(script, ";")
		if rounds >= len(plan) {
			text("done: " + strings.Join(results, " | "))
			break
		}
		text(fmt.Sprintf("round %d. ", rounds+1))
		var calls []any
		for i, name := range strings.Split(plan[rounds], ",") {
			name = strings.TrimSpace(name)
			args := `{}`
			if name == "ask_user" {
				args = `{"questions":[{"question":"Which one?","header":"Pick","options":[{"label":"A"},{"label":"B"}]}]}`
			}
			calls = append(calls, map[string]any{"index": i, "id": fmt.Sprintf("c%d-%d", rounds+1, i),
				"function": map[string]any{"name": name, "arguments": args}})
		}
		send(map[string]any{"tool_calls": calls})
	case strings.Contains(user, "hang"):
		text("partial ")
		select {
		case <-gate:
			text("rest")
		case <-r.Context().Done():
			return
		}
	default:
		text("hello")
	}
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
}

// gatedTools is a tool server: write_a and write_b change something, read_a is declared read-only. Every execution is
// counted. A tool in hold waits until it is released or its context ends; like a real tool server, it then answers
// with an error result and no error.
type gatedTools struct {
	mu      sync.Mutex
	count   map[string]int
	hold    map[string]chan struct{}
	started chan string
}

func newGatedTools() *gatedTools {
	return &gatedTools{count: map[string]int{}, hold: map[string]chan struct{}{}, started: make(chan string, 100)}
}

func (g *gatedTools) ID() string { return "durable-server" }

func (g *gatedTools) Tools(context.Context, *auth.User) ([]tools.Spec, error) {
	s := func(name string, readOnly bool) tools.Spec {
		return tools.Spec{Name: name, Description: "test", Parameters: json.RawMessage(`{"type":"object"}`), ReadOnly: readOnly}
	}
	return []tools.Spec{s("write_a", false), s("write_b", false), s("read_a", true)}, nil
}

func (g *gatedTools) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	g.mu.Lock()
	g.count[c.Name]++
	n, gate := g.count[c.Name], g.hold[c.Name]
	g.mu.Unlock()
	g.started <- c.Name
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return tools.Result{Content: c.Name + " failed: context canceled", IsError: true}, nil
		}
	}
	return tools.Result{Content: fmt.Sprintf("%s ok #%d", c.Name, n)}, nil
}

func (g *gatedTools) block(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hold[name] = make(chan struct{})
}

func (g *gatedTools) release(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ch := g.hold[name]; ch != nil {
		close(ch)
		delete(g.hold, name)
	}
}

func (g *gatedTools) ran(name string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.count[name]
}

// await waits until the named tool has started.
func (g *gatedTools) await(t *testing.T, name string) {
	t.Helper()
	for {
		select {
		case got := <-g.started:
			if got == name {
				return
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s did not start", name)
		}
	}
}

type durableHarness struct {
	*harness
	model *scriptModel
	tool  *gatedTools
}

func newDurable(t *testing.T) *durableHarness {
	t.Helper()
	h := &durableHarness{harness: newHarness(t), model: newScriptModel(), tool: newGatedTools()}
	srv := httptest.NewServer(h.model)
	if _, err := h.pool.Exec(context.Background(), `UPDATE connection SET base_url = $1 WHERE id = 'fake'`, srv.URL); err != nil {
		t.Fatal(err)
	}
	tools.Register(h.tool)
	t.Cleanup(func() {
		// whatever is still held must end before the database goes
		h.model.open()
		for _, n := range []string{"write_a", "write_b", "read_a"} {
			h.tool.release(n)
		}
		h.e.Wait()
		srv.Close()
		tools.Register(inertSet("durable-server"))
	})
	return h
}

// peer is a second instance on the same database and files.
func (h *harness) peer(t *testing.T) *Engine {
	t.Helper()
	p := New(h.pool, h.e.Keys, h.e.Settings, h.e.Models, h.e.Files, &chats.Store{DB: h.pool}, "http://kera.test")
	t.Cleanup(p.Wait)
	return p
}

// task starts a run in a task chat of its own and returns the chat id and the run.
func (h *durableHarness) task(t *testing.T, e *Engine, prompt string) (string, *Run) {
	t.Helper()
	cid := h.taskChat(t)
	run, err := e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid, ID: "a-" + cid,
		ToolIDs: []string{"durable-server"}, UserMessage: &UserMessage{ID: "q-" + cid, Content: prompt}})
	if err != nil {
		t.Fatal(err)
	}
	return cid, run
}

func (h *harness) message(t *testing.T, cid, mid string) chats.Message {
	t.Helper()
	m, err := h.e.Chats.Message(context.Background(), cid, mid)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// runRow is the part of a run row the tests look at.
type runRow struct {
	state, owner         string
	attempt, recoveries  int
	cancelled            bool
	waitKey, answer      string
	hasCheckpoint, found bool
}

func (h *harness) row(t *testing.T, mid string) runRow {
	t.Helper()
	var r runRow
	var n int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM run WHERE message_id = $1`, mid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return r
	}
	r.found = true
	if err := h.pool.QueryRow(context.Background(), `SELECT state, coalesce(lease_owner, ''), attempt, recoveries, cancel_requested,
		coalesce(wait_key, ''), coalesce(answer::text, ''), checkpoint IS NOT NULL FROM run WHERE message_id = $1`, mid).
		Scan(&r.state, &r.owner, &r.attempt, &r.recoveries, &r.cancelled, &r.waitKey, &r.answer, &r.hasCheckpoint); err != nil {
		t.Fatal(err)
	}
	return r
}

// expire makes the lease of a message's run look abandoned.
func (h *harness) expire(t *testing.T, mid string) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(), `UPDATE run SET lease_until = now() - interval '1 second' WHERE message_id = $1`, mid); err != nil {
		t.Fatal(err)
	}
}

// until waits for cond.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
	}
}

// settled waits until the message is no longer being written and returns it.
func (h *harness) settled(t *testing.T, cid, mid string) chats.Message {
	t.Helper()
	var m chats.Message
	until(t, "the reply to end", func() bool { m = h.message(t, cid, mid); return m.Status != "streaming" && !h.row(t, mid).found })
	return m
}

func TestRunRowRemovedAtFinish(t *testing.T) {
	h := newDurable(t)
	cid, run := h.task(t, h.e, "hang")
	mid := run.MessageID
	until(t, "the model to answer", func() bool { return strings.Contains(run.stateText(), "partial") })
	if r := h.row(t, mid); !r.found || r.state != "running" || r.owner != h.e.instance || r.attempt != 1 {
		t.Fatalf("row of a live run: %+v", r)
	}
	h.model.open()
	wait(t, run)
	h.e.Wait()
	if r := h.row(t, mid); r.found {
		t.Fatalf("the row was not removed: %+v", r)
	}
	if m := h.message(t, cid, mid); m.Status != "done" || m.Content != "partial rest" {
		t.Fatalf("message %q %q", m.Status, m.Content)
	}
	var n int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM usage WHERE message_id = $1`, mid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("usage rows %d, %v", n, err)
	}
	// a temporary chat has no row
	tmp, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", Temporary: true,
		Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, tmp)
	h.e.Wait()
	var rows int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM run`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("run rows %d, %v", rows, err)
	}
}

// An owner that has been replaced writes nothing: not the message, not the usage, not the row, not a title.
func TestStaleOwnerIsFenced(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	cid := h.chat(t) // titled "New Chat", so the title task of a finished run would rename it
	run, err := h.e.Start(ctx, h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid, ID: "a1",
		UserMessage: &UserMessage{ID: "q1", Content: "hang"}})
	if err != nil {
		t.Fatal(err)
	}
	mid := run.MessageID
	until(t, "the model to answer", func() bool { return strings.Contains(run.stateText(), "partial") })
	// another instance takes the run over and writes its own state; the old owner has not noticed
	if _, err := h.pool.Exec(ctx, `UPDATE run SET lease_owner = 'other', attempt = attempt + 1 WHERE message_id = $1`, mid); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE message SET content = 'the new owner' WHERE id = $1`, mid); err != nil {
		t.Fatal(err)
	}
	x := &exec{u: h.u, run: run, chat: &chats.Chat{ID: cid}}
	if err := h.e.hold(ctx, x); !errors.Is(err, errLostLease) {
		t.Fatalf("hold: %v", err)
	}
	if err := h.e.Chats.SaveProgress(ctx, mid, chats.Progress{Content: "stale", Status: "streaming", Fence: h.e.fence(x)}); !errors.Is(err, chats.ErrFenced) {
		t.Fatalf("fenced save: %v", err)
	}
	x.m = &models.Resolved{ID: "m1", Local: true}
	if err := h.e.endRun(ctx, x, chats.Progress{Content: "stale", Status: "done"}, true, &usageRow{userID: "u1", modelID: "m1"}); !errors.Is(err, errLostLease) {
		t.Fatalf("end of run: %v", err)
	}
	h.model.open() // the old owner's model answer ends: it must not be stored anywhere
	h.e.Wait()
	if !run.Finished() || run.detached() == nil {
		t.Fatal("the stale run is still attached")
	}
	evs, _, _ := run.next(0)
	if strings.Contains(types(evs), "done") {
		t.Fatalf("a stale owner reported the run done: %s", types(evs))
	}
	if m := h.message(t, cid, mid); m.Status != "streaming" || m.Content != "the new owner" {
		t.Fatalf("the stale owner wrote the message: %q %q", m.Status, m.Content)
	}
	if r := h.row(t, mid); !r.found || r.owner != "other" {
		t.Fatalf("the stale owner touched the row: %+v", r)
	}
	c, _ := h.e.Chats.Get(ctx, "u1", cid)
	var usage int
	_ = h.pool.QueryRow(ctx, `SELECT count(*) FROM usage WHERE message_id = $1`, mid).Scan(&usage)
	if usage != 0 || c.Title != "New Chat" || c.Locality != nil {
		t.Fatalf("end-of-run writes by a stale owner: usage %d, title %q, locality %v", usage, c.Title, c.Locality)
	}
}

func TestLeaseExpiryTakeover(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	b := h.peer(t)
	cid, run := h.task(t, h.e, "hang")
	mid := run.MessageID
	until(t, "the model to answer", func() bool { return strings.Contains(run.stateText(), "partial") })
	b.tick(ctx) // a live lease is left alone
	if r := h.row(t, mid); r.owner != h.e.instance || r.attempt != 1 {
		t.Fatalf("a live run was claimed: %+v", r)
	}
	h.e.tick(ctx) // and the owner's heartbeat keeps it
	h.expire(t, mid)
	b.tick(ctx)
	b.Wait()
	m := h.settled(t, cid, mid)
	if m.Status != "error" {
		t.Fatalf("a claimed run in this version ends: %q %q", m.Status, m.Error)
	}
	// the old owner finds out at its next heartbeat and writes nothing more
	h.e.tick(ctx)
	h.e.Wait()
	if !run.Finished() || run.detached() == nil {
		t.Fatal("the old owner still runs")
	}
	if got := h.message(t, cid, mid); got.Status != "error" || got.Error != m.Error {
		t.Fatalf("the old owner changed the outcome: %q %q", got.Status, got.Error)
	}
}

// A cancel on any instance reaches the owner at its next heartbeat.
func TestCancelFromAnotherInstance(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	b := h.peer(t)
	cid, run := h.task(t, h.e, "hang")
	mid := run.MessageID
	until(t, "the model to answer", func() bool { return strings.Contains(run.stateText(), "partial") })
	if ok, err := b.requestCancel(ctx, "someone-else", cid, mid); err != nil || ok {
		t.Fatalf("another user's cancel: %v %v", ok, err)
	}
	if ok, err := b.requestCancel(ctx, "u1", cid, mid); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	h.e.tick(ctx)
	wait(t, run)
	if m := h.settled(t, cid, mid); m.Status != "stopped" || m.Content != "partial " {
		t.Fatalf("cancelled message %q %q %q", m.Status, m.Content, m.Error)
	}
}

// A second instance cannot start a message the first one is writing, and writes nothing while it tries.
func TestBusyAcrossInstances(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	cid, run := h.task(t, h.e, "hang")
	_, err := b.Start(context.Background(), h.u, &Request{Model: "m1", ChatID: cid, ID: run.MessageID,
		UserMessage: &UserMessage{ID: "q-second", Content: "again"}})
	if !errors.Is(err, errBusy) {
		t.Fatalf("want busy, got %v", err)
	}
	if _, err := h.e.Chats.Message(context.Background(), cid, "q-second"); err == nil {
		t.Fatal("the refused request stored its user message")
	}
}

// Deleting a chat, a message or a user removes the run row (and its checkpoint) and stops the run.
func TestDeletingRemovesTheRun(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	del := map[string]func(cid, mid string) error{
		"chat":    func(cid, _ string) error { return h.e.Chats.Delete(ctx, "u1", cid) },
		"message": func(cid, mid string) error { return h.e.Chats.DeleteMessage(ctx, "u1", cid, mid) },
		"user":    func(_, _ string) error { _, err := h.pool.Exec(ctx, `DELETE FROM "user" WHERE id = 'u1'`); return err },
	}
	for _, name := range []string{"chat", "message", "user"} { // the user goes last
		cid, run := h.task(t, h.e, "hang")
		mid := run.MessageID
		until(t, "the model to answer", func() bool { return strings.Contains(run.stateText(), "partial") })
		if err := del[name](cid, mid); err != nil {
			t.Fatalf("delete %s: %v", name, err)
		}
		if r := h.row(t, mid); r.found {
			t.Fatalf("deleting the %s left the run row", name)
		}
		h.e.tick(ctx) // the heartbeat finds the row gone
		until(t, "the run to stop", run.Finished)
		if run.detached() == nil {
			t.Fatalf("deleting the %s: the run finished as if it were still wanted", name)
		}
	}
	h.e.Wait()
}

// Replies marked as being written with no run behind them are failed after a minute; a live run's is not.
func TestJanitorFailsOnlyOrphans(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	cid, run := h.task(t, h.e, "hang")
	if _, err := h.pool.Exec(ctx, `INSERT INTO message (id, chat_id, role, status, updated_at) VALUES
		('old-orphan', $1, 'assistant', 'streaming', now() - interval '2 minutes'),
		('new-orphan', $1, 'assistant', 'streaming', now())`, cid); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE message SET updated_at = now() - interval '1 hour' WHERE id = $1`, run.MessageID); err != nil {
		t.Fatal(err)
	}
	n, err := h.e.failOrphans(ctx)
	if err != nil || n != 1 {
		t.Fatalf("failed %d, %v", n, err)
	}
	for id, want := range map[string]string{"old-orphan": "error", "new-orphan": "streaming", run.MessageID: "streaming"} {
		if got := h.message(t, cid, id).Status; got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}

// A NUL in a tool result cannot be stored in text or jsonb; it must not stop the message from being saved.
func TestSaveDropsNUL(t *testing.T) {
	h := newHarness(t)
	cid := h.chat(t)
	if err := h.e.Chats.InsertMessage(context.Background(), &chats.Message{ID: "a1", ChatID: cid, Role: "assistant"}); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Chats.SaveProgress(context.Background(), "a1", chats.Progress{Content: "a\x00b", Status: "done",
		Parts: []map[string]any{{"type": "text", "text": "x\x00y", "n": json.Number("12345678901234567890")}}}); err != nil {
		t.Fatal(err)
	}
	m := h.message(t, cid, "a1")
	if m.Content != "ab" || !strings.Contains(string(m.Parts), `"xy"`) || !strings.Contains(string(m.Parts), "12345678901234567890") {
		t.Fatalf("stored %q %s", m.Content, m.Parts)
	}
}
