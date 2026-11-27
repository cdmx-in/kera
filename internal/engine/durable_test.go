package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/provider"
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
	peers []*Engine
}

func newDurable(t *testing.T) *durableHarness {
	t.Helper()
	h := &durableHarness{harness: newHarness(t), model: newScriptModel(), tool: newGatedTools()}
	srv := httptest.NewServer(h.model)
	if _, err := h.pool.Exec(context.Background(), `UPDATE connection SET base_url = $1 WHERE id = 'fake'`, srv.URL); err != nil {
		t.Fatal(err)
	}
	tools.Register(h.tool)
	tools.Register(&coreTools{e: h.e}) // ask_user
	t.Cleanup(func() {
		// whatever is still held or running must end before the database goes, also after a failed test
		engines := append([]*Engine{h.e}, h.peers...)
		for _, e := range engines {
			for _, r := range e.runs.live() {
				r.stop(errLostLease)
			}
		}
		h.model.open()
		for _, n := range []string{"write_a", "write_b", "read_a"} {
			h.tool.release(n)
		}
		for _, e := range engines {
			e.Wait()
		}
		srv.Close()
		tools.Register(inertSet("durable-server"))
		tools.Register(inertSet("builtin:core"))
	})
	return h
}

// peer is a second instance on the same database and files.
func (h *durableHarness) peer(t *testing.T) *Engine {
	t.Helper()
	p := New(h.pool, h.e.Keys, h.e.Settings, h.e.Models, h.e.Files, &chats.Store{DB: h.pool}, "http://kera.test")
	h.peers = append(h.peers, p)
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
	if t != nil {
		t.Helper()
	}
	for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			if t == nil {
				panic("timed out waiting for " + what)
			}
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
	if r := h.row(t, mid); r.owner != b.instance || r.attempt != 2 || r.recoveries != 1 {
		t.Fatalf("after the takeover: %+v", r)
	}
	h.e.tick(ctx) // the old owner finds out at its next heartbeat and writes nothing more
	until(t, "the old owner to stop", run.Finished)
	if run.detached() == nil {
		t.Fatal("the old owner ended the run")
	}
	h.model.open()
	if m := h.settled(t, cid, mid); m.Status != "done" || m.Content != "partial rest" {
		t.Fatalf("taken over: %q %q %q", m.Status, m.Content, m.Error)
	}
}

// crash stops a's run the way a killed process would: nothing more is written, and the lease runs out.
func (h *durableHarness) crash(t *testing.T, a *Engine, run *Run) {
	t.Helper()
	run.stop(errLostLease)
	a.Wait()
	h.expire(t, run.MessageID)
}

// resumeOn lets b claim what needs an owner and waits for the reply to end.
func (h *durableHarness) resumeOn(t *testing.T, b *Engine, cid, mid string) chats.Message {
	t.Helper()
	b.tick(context.Background())
	return h.settled(t, cid, mid)
}

// A write that was running when the server died is not run again, a write that had finished keeps its result, and
// the calls after the one in doubt, which never started, run once.
func TestResumeDoesNotRepeatWriteTool(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	h.tool.block("write_a")
	cid, run := h.task(t, h.e, "calls: write_b, write_a, write_b")
	h.tool.await(t, "write_a")
	h.crash(t, h.e, run)
	h.tool.release("write_a") // a run that wrongly calls it again gets an answer, and the count shows it
	m := h.resumeOn(t, b, cid, run.MessageID)
	if a, wb := h.tool.ran("write_a"), h.tool.ran("write_b"); a != 1 || wb != 2 {
		t.Fatalf("write_a ran %d times (want 1), write_b %d times (want 2: one before, the unstarted one after)", a, wb)
	}
	want := "round 1. done: write_b ok #1 | " + unknownResult + " | write_b ok #2"
	if m.Status != "done" || m.Content != want {
		t.Fatalf("resumed %q %q\n got %q\nwant %q", m.Status, m.Error, m.Content, want)
	}
	// the message shows each call once, and the answer's text once
	var steps []string
	var parts []Part
	if err := json.Unmarshal(m.Parts, &parts); err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		for _, s := range p.Steps {
			steps = append(steps, s.CallID+" "+s.Name+" "+s.Status)
		}
	}
	if got, want := strings.Join(steps, ", "), "c1-0 write_b done, c1-1 write_a error, c1-2 write_b done"; got != want {
		t.Fatalf("steps %q, want %q", got, want)
	}
}

func TestResumeRerunsReadOnlyTool(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	h.tool.block("read_a")
	cid, run := h.task(t, h.e, "calls: read_a")
	h.tool.await(t, "read_a")
	h.crash(t, h.e, run)
	h.tool.release("read_a")
	m := h.resumeOn(t, b, cid, run.MessageID)
	if h.tool.ran("read_a") != 2 || m.Content != "round 1. done: read_a ok #2" {
		t.Fatalf("read_a ran %d times, answer %q %q", h.tool.ran("read_a"), m.Content, m.Error)
	}
}

// A read before a write in one round: the read runs again; the write may have started, so it does not.
func TestResumeAfterReadThenWrite(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	h.tool.block("write_a")
	cid, run := h.task(t, h.e, "calls: read_a, write_a")
	h.tool.await(t, "write_a")
	h.crash(t, h.e, run)
	h.tool.release("write_a") // a run that wrongly calls it again gets an answer, and the count shows it
	m := h.resumeOn(t, b, cid, run.MessageID)
	if h.tool.ran("read_a") != 2 || h.tool.ran("write_a") != 1 || m.Content != "round 1. done: read_a ok #2 | "+unknownResult {
		t.Fatalf("read_a %d, write_a %d, answer %q", h.tool.ran("read_a"), h.tool.ran("write_a"), m.Content)
	}
}

// A crash in a later round keeps the earlier rounds as they were and calls nothing of them again.
func TestResumeInALaterRound(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	h.tool.block("write_b")
	cid, run := h.task(t, h.e, "calls: write_a ; read_a ; write_b")
	h.tool.await(t, "write_b")
	h.crash(t, h.e, run)
	h.tool.release("write_b") // a run that wrongly calls it again gets an answer, and the count shows it
	m := h.resumeOn(t, b, cid, run.MessageID)
	if h.tool.ran("write_a") != 1 || h.tool.ran("read_a") != 1 || h.tool.ran("write_b") != 1 {
		t.Fatalf("ran %v", h.tool.count)
	}
	want := "round 1. round 2. round 3. done: write_a ok #1 | read_a ok #1 | " + unknownResult
	if m.Content != want {
		t.Fatalf("answer %q, want %q", m.Content, want)
	}
	var usage, prompt int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*), coalesce(sum(prompt_tokens), 0) FROM usage WHERE message_id = $1`,
		run.MessageID).Scan(&usage, &prompt); err != nil {
		t.Fatal(err)
	}
	// four model calls of 10 prompt tokens each, those of the lost attempt included
	if usage != 1 || prompt != 40 {
		t.Fatalf("usage rows %d with %d prompt tokens, want 1 with 40", usage, prompt)
	}
}

// Before the first model response there is no checkpoint: the answer starts again, once.
func TestResumeFromZeroWithoutCheckpoint(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	cid, run := h.task(t, h.e, "hang")
	until(t, "the model to answer", func() bool { return strings.Contains(run.stateText(), "partial") })
	if r := h.row(t, run.MessageID); r.hasCheckpoint {
		t.Fatal("checkpoint before the first response")
	}
	h.crash(t, h.e, run)
	h.model.open()
	if m := h.resumeOn(t, b, cid, run.MessageID); m.Status != "done" || m.Content != "partial rest" {
		t.Fatalf("from zero: %q %q %q", m.Status, m.Content, m.Error)
	}
}

// Events of a resumed run are numbered after every attempt before it, and the first one carries the whole state.
func TestSeqBaseAfterResume(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	h.tool.block("write_a")
	_, run := h.task(t, h.e, "calls: write_a")
	h.tool.await(t, "write_a")
	seen := run.Seq()
	h.crash(t, h.e, run)
	b.tick(context.Background())
	resumed := b.runs.byMessage(run.MessageID)
	if resumed == nil || resumed.ID != run.ID {
		t.Fatal("the resumed run is not registered under its id")
	}
	wait(t, resumed)
	evs, _, _ := resumed.next(seen) // what a browser that saw the first attempt asks for
	if len(evs) == 0 || evs[0].Type != "snapshot" || evs[0].Seq != 1<<32+1 || evs[len(evs)-1].Type != "done" {
		t.Fatalf("events after %d: %s, first seq %d", seen, types(evs), evs[0].Seq)
	}
	// the callback token of the first attempt does not work on the second
	old, _ := signRunToken(h.e.Keys, run.ID, run.UserID, 1, time.Now().Add(time.Minute))
	now, _ := signRunToken(h.e.Keys, run.ID, run.UserID, resumed.attempt, time.Now().Add(time.Minute))
	if verifyRunToken(h.e.Keys, run.ID, resumed.attempt, old) || !verifyRunToken(h.e.Keys, run.ID, resumed.attempt, now) {
		t.Fatal("callback tokens are not scoped to the attempt")
	}
}

// An owner that was replaced while a read-only call ran must not go on to call a tool that writes.
func TestStaleOwnerCallsNoWriteTool(t *testing.T) {
	h := newDurable(t)
	h.tool.block("read_a")
	_, run := h.task(t, h.e, "calls: read_a, write_a")
	h.tool.await(t, "read_a")
	if _, err := h.pool.Exec(context.Background(), `UPDATE run SET lease_owner = 'other', attempt = attempt + 1 WHERE message_id = $1`,
		run.MessageID); err != nil {
		t.Fatal(err)
	}
	h.tool.release("read_a")
	h.e.Wait()
	if n := h.tool.ran("write_a"); n != 0 {
		t.Fatalf("a stale owner called write_a %d times", n)
	}
	if run.detached() == nil {
		t.Fatal("the stale run did not leave")
	}
}

// A run that keeps taking the server down is given up.
func TestRecoveriesCap(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	h.tool.block("write_a")
	cid, run := h.task(t, h.e, "calls: write_a")
	h.tool.await(t, "write_a")
	h.crash(t, h.e, run)
	if _, err := h.pool.Exec(context.Background(), `UPDATE run SET recoveries = $2 WHERE message_id = $1`, run.MessageID, maxRecoveries); err != nil {
		t.Fatal(err)
	}
	m := h.resumeOn(t, b, cid, run.MessageID)
	if m.Status != "error" || !strings.Contains(m.Error, "several times") || h.tool.ran("write_a") != 1 {
		t.Fatalf("%q %q, write_a ran %d times", m.Status, m.Error, h.tool.ran("write_a"))
	}
}

// Everything that allowed a run to start is checked again before it goes on.
func TestResumeChecksAuthorisation(t *testing.T) {
	cases := []struct {
		name  string
		setup []string // SQL run while no instance owns the run
		set   map[string]any
		want  string // in the message's error
	}{
		{name: "disabled user", setup: []string{`UPDATE "user" SET disabled_at = now()`}, want: "can no longer use Kera"},
		{name: "pending user", setup: []string{`UPDATE "user" SET role = 'pending'`}, want: "can no longer use Kera"},
		{name: "model access revoked", setup: []string{`UPDATE "user" SET role = 'user'`}, want: "no longer have access to this assistant"},
		{name: "cowork permission lost", setup: []string{`UPDATE "user" SET role = 'user'`},
			set:  map[string]any{"models.base_public": true, "user.permissions": map[string]any{"features": map[string]any{"cowork": false}}},
			want: "features.cowork"},
		{name: "schedule permission lost", setup: []string{`UPDATE "user" SET role = 'user'`, `UPDATE run SET origin = 'schedule:s1'`},
			set:  map[string]any{"models.base_public": true, "user.permissions": map[string]any{"features": map[string]any{"cowork": true, "cowork_schedule": false}}},
			want: "features.cowork_schedule"},
		{name: "local run, hosted model now", setup: []string{`UPDATE run SET request = jsonb_set(request, '{local}', 'true')`}, want: "started on a local model"},
		{name: "chat locked to local", setup: []string{`UPDATE chat SET locality = 'local'`}, set: map[string]any{"chat.local_lock": true}, want: "started on a local model"},
		{name: "resume switched off", set: map[string]any{"runs.resume": false}, want: crashedMsg},
		{name: "unreadable checkpoint", setup: []string{`UPDATE run SET checkpoint = 'not json'::bytea`}, want: crashedMsg},
		{name: "checkpoint of another version", setup: []string{`UPDATE run SET checkpoint = '{"v":99}'::bytea`}, want: crashedMsg},
		{name: "unattended time limit passed", setup: []string{`UPDATE run SET deadline = now() - interval '1 second'`}, want: "time limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newDurable(t)
			ctx := context.Background()
			b := h.peer(t)
			h.tool.block("read_a") // read-only: a run that is wrongly continued calls it again
			cid, run := h.task(t, h.e, "calls: read_a")
			h.tool.await(t, "read_a")
			h.crash(t, h.e, run)
			h.tool.release("read_a")
			for _, q := range c.setup {
				if _, err := h.pool.Exec(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			for k, v := range c.set {
				if err := h.e.Settings.Set(ctx, k, v, false); err != nil {
					t.Fatal(err)
				}
			}
			before := len(h.model.seen())
			m := h.resumeOn(t, b, cid, run.MessageID)
			if m.Status != "error" || !strings.Contains(m.Error, c.want) {
				t.Fatalf("status %q, error %q, want %q", m.Status, m.Error, c.want)
			}
			if h.tool.ran("read_a") != 1 || len(h.model.seen()) != before {
				t.Fatalf("a refused run went on: read_a %d, model calls %d -> %d", h.tool.ran("read_a"), before, len(h.model.seen()))
			}
		})
	}
}

// A model server that is still starting after the same reboot does not fail the run: it is tried again.
func TestResumeWaitsForTheModelServer(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	b := h.peer(t)
	h.tool.block("read_a")
	cid, run := h.task(t, h.e, "calls: read_a")
	mid := run.MessageID
	h.tool.await(t, "read_a")
	h.crash(t, h.e, run)
	h.tool.release("read_a")
	var url string
	if err := h.pool.QueryRow(ctx, `SELECT base_url FROM connection WHERE id = 'fake'`).Scan(&url); err != nil {
		t.Fatal(err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	defer down.Close()
	if _, err := h.pool.Exec(ctx, `UPDATE connection SET base_url = $1`, down.URL); err != nil {
		t.Fatal(err)
	}
	b.tick(ctx)
	until(t, "the run to be put back", func() bool { r := h.row(t, mid); return r.state == "queued" })
	if m := h.message(t, cid, mid); m.Status != "streaming" {
		t.Fatalf("failed instead of waiting: %q %q", m.Status, m.Error)
	}
	b.tick(ctx) // not before its time
	if r := h.row(t, mid); r.state != "queued" {
		t.Fatalf("claimed before its retry time: %+v", r)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE connection SET base_url = $1`, url); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE run SET not_before = now()`); err != nil {
		t.Fatal(err)
	}
	if m := h.resumeOn(t, b, cid, mid); m.Status != "done" || !strings.Contains(m.Content, "done: read_a ok #2") {
		t.Fatalf("after the server came back: %q %q %q", m.Status, m.Content, m.Error)
	}
}

// The origin of a run is stored with it, so tools that depend on it (a scheduled task's notes) are still there.
func TestResumeKeepsTheOrigin(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	b := h.peer(t)
	var mu sync.Mutex
	var origins []string
	tools.Register(&originTool{saw: func(o string) { mu.Lock(); origins = append(origins, o); mu.Unlock() }})
	t.Cleanup(func() { tools.Register(inertSet("origin-server")) })
	h.tool.block("write_a")
	cid := h.taskChat(t)
	run, err := h.e.Start(ctx, h.u, &Request{Model: "m1", ChatID: cid, ID: "a1", Unattended: true, Origin: "schedule:s1",
		ToolIDs: []string{"durable-server", "origin-server"}, UserMessage: &UserMessage{ID: "q1", Content: "calls: write_a"}})
	if err != nil {
		t.Fatal(err)
	}
	h.tool.await(t, "write_a")
	h.crash(t, h.e, run)
	if m := h.resumeOn(t, b, cid, "a1"); m.Status != "done" {
		t.Fatalf("%q %q", m.Status, m.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(origins) < 2 || origins[0] != "schedule:s1" || origins[len(origins)-1] != "schedule:s1" {
		t.Fatalf("origins seen by the tool list: %q", origins)
	}
}

type originTool struct{ saw func(string) }

func (*originTool) ID() string { return "origin-server" }
func (o *originTool) Tools(ctx context.Context, _ *auth.User) ([]tools.Spec, error) {
	o.saw(Origin(ctx))
	return []tools.Spec{{Name: "noop"}}, nil
}
func (*originTool) Execute(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{}, nil
}

// What a resumed run may call again: only tools checked to change nothing. Running code is not one of them, and a
// built-in's own flag is not trusted.
func TestRerunnable(t *testing.T) {
	for _, c := range []struct {
		provider, name string
		readOnly, want bool
	}{
		{"builtin:core", "get_current_timestamp", false, true},
		{"builtin:core", "search_chats", false, true},
		{"builtin:core", "ask_user", false, false},
		{"builtin:files", "view_file", false, true},
		{"builtin:web", "fetch_url", false, true},
		{"builtin:knowledge", "query_knowledge_files", false, true},
		{"builtin:skills", "load_skill", false, true},
		{"builtin:code", "execute_code", false, false},
		{"builtin:code", "execute_code", true, false},
		{"builtin:memory", "add_memory", false, false},
		{"builtin:memory", "search_memories", false, false},
		{"builtin:memory", "delete_memory", false, false},
		{"builtin:schedule", "save_task_notes", false, false},
		{"builtin:web", "a_tool_added_later", false, false},
		{"builtin:new", "anything", true, false},
		{"some-server", "fetch_url", false, false},
		{"some-server", "list_things", true, true},
	} {
		got := rerunnable(toolEntry{spec: tools.Spec{Name: c.name, ReadOnly: c.readOnly}, provider: inertSet(c.provider)})
		if got != c.want {
			t.Errorf("%s %s (readOnly %v): rerunnable %v, want %v", c.provider, c.name, c.readOnly, got, c.want)
		}
	}
}

// The tools of a restarted server can differ: a call that was safe is not run through another provider.
func TestResumePlan(t *testing.T) {
	entry := func(p, name string, readOnly bool) toolEntry {
		return toolEntry{spec: tools.Spec{Name: name, ReadOnly: readOnly}, provider: inertSet(p)}
	}
	table := map[string]toolEntry{"look": entry("other", "look", false), "read": entry("s", "read", true), "write": entry("s", "write", false)}
	calls := []provider.ToolCall{{ID: "1", Name: "read"}, {ID: "2", Name: "look"}, {ID: "3", Name: "write"}, {ID: "4", Name: "write"}, {ID: "5", Name: "read"}}
	o := &openRound{Safe: []string{"s", "s", "", "", "stale"}}
	got := resumePlan(table, calls, 0, o)
	if want := []string{"", changedResult, unknownResult, "", ""}; !slices.Equal(got, want) {
		t.Fatalf("plan %q", got)
	}
	if want := []string{"s", "s", "", "", "s"}; !slices.Equal(o.Safe, want) { // the calls that never started are judged anew
		t.Fatalf("safe %q", o.Safe)
	}
	if got := resumePlan(table, calls, 3, &openRound{Safe: []string{"s", "s", "", "", "s"}}); !slices.Equal(got, []string{"", "", "", unknownResult, ""}) {
		t.Fatalf("plan from 3: %q", got)
	}
}

// Images are kept as file references, not as data, and read again only while the user may read them.
func TestCheckpointImages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	f, err := h.e.Files.CreateText(ctx, "u1", "shot.png", "not really a png", nil)
	if err != nil {
		t.Fatal(err)
	}
	img, ok := h.e.image(ctx, f)
	if !ok || img.FileID != f.ID {
		t.Fatalf("image %v %q", ok, img.FileID)
	}
	msgs := []provider.Message{{Role: "user", Content: "look", Data: true, Images: []provider.Image{img, {MIME: "image/png", Data: "inline"}},
		ToolCalls: []provider.ToolCall{{ID: "c", Name: "n", Arguments: json.RawMessage(`{broken`)}}}}
	raw, err := json.Marshal(&checkpoint{Version: checkpointVersion, Messages: ckMessages(msgs)})
	if err != nil {
		t.Fatalf("arguments that are not JSON must survive: %v", err)
	}
	if strings.Contains(string(raw), img.Data) || !strings.Contains(string(raw), f.ID) || !strings.Contains(string(raw), "inline") {
		t.Fatalf("checkpoint %s", raw)
	}
	ck, err := decodeCheckpoint(raw)
	if err != nil {
		t.Fatal(err)
	}
	x := &exec{u: h.u, run: newRun("c", "m", "u1", "m1", true)}
	got := h.e.restoreMessages(ctx, x, ck.Messages)
	if len(got) != 1 || !got[0].Data || len(got[0].Images) != 2 || got[0].Images[0] != img || string(got[0].ToolCalls[0].Arguments) != "{broken" {
		t.Fatalf("restored %+v", got)
	}
	x.u = &auth.User{ID: "someone-else", Role: "user"}
	if got := h.e.restoreMessages(ctx, x, ck.Messages); len(got[0].Images) != 1 {
		t.Fatalf("an unreadable file's image was restored: %+v", got[0].Images)
	}
}

// sseEvents reads a whole event stream.
func sseEvents(t *testing.T, resp *http.Response, onEvent func(typ string, data map[string]any)) {
	t.Helper()
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	typ := ""
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			typ = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			var d map[string]any
			if err := json.Unmarshal([]byte(v), &d); err != nil {
				t.Errorf("bad event data %q", v)
				return
			}
			onEvent(typ, d)
		}
	}
}

// serve exposes one engine's chat routes to its user.
func (h *harness) serve(t *testing.T, e *Engine) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, f := range map[string]http.HandlerFunc{
		"POST /api/chat/completions":                            e.completions,
		"GET /api/v1/chats/{id}/messages/{mid}/stream":          e.resume,
		"POST /api/v1/chats/{id}/messages/{mid}/cancel":         e.cancelRun,
		"POST /api/v1/chats/{id}/messages/{mid}/calls/{callId}": e.answerCall,
		"GET /api/v1/chats/{id}/runs":                           e.chatRuns,
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { f(w, r.WithContext(auth.WithUser(r.Context(), h.u))) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A browser on an instance that does not run the reply stays attached: it gets the stored message, follows the run
// when this instance resumes it, and never mistakes the wait for the end.
func TestHeldStreamFollowsTheResume(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	srv := h.serve(t, b)
	h.tool.block("write_a")
	cid, run := h.task(t, h.e, "calls: write_a")
	h.tool.await(t, "write_a")
	resp, err := http.Get(srv.URL + "/api/v1/chats/" + cid + "/messages/" + run.MessageID + "/stream?after=3")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	var seqs []float64
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		sseEvents(t, resp, func(typ string, d map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, typ)
			if n, ok := d["seq"].(float64); ok {
				seqs = append(seqs, n)
			}
		})
	}()
	seen := func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(got, ",") }
	until(t, "the stored snapshot", func() bool { return seen() == "snapshot" })
	time.Sleep(5 * holdPoll) // held while the owner works: nothing changes, nothing ends
	if s := seen(); s != "snapshot" {
		t.Fatalf("held stream of a live run elsewhere: %s", s)
	}
	h.crash(t, h.e, run)
	b.tick(context.Background())
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("the held stream did not end: %s", seen())
	}
	if s := seen(); !strings.HasPrefix(s, "snapshot,snapshot,") || !strings.HasSuffix(s, ",done") || strings.Count(s, "done") != 1 {
		t.Fatalf("held stream: %s", s)
	}
	for _, n := range seqs {
		if n <= 1<<32 {
			t.Fatalf("a held event carries a seq of an older attempt: %v", seqs)
		}
	}
	h.settled(t, cid, run.MessageID)
}

// A held stream ends with done when the run's row goes, wherever the run ran.
func TestHeldStreamEndsWithTheRun(t *testing.T) {
	h := newDurable(t)
	srv := h.serve(t, h.peer(t))
	cid, run := h.task(t, h.e, "hang")
	until(t, "the model to answer", func() bool { return strings.Contains(run.stateText(), "partial") })
	resp, err := http.Get(srv.URL + "/api/v1/chats/" + cid + "/messages/" + run.MessageID + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var last map[string]any
	var content string
	h.model.open()
	sseEvents(t, resp, func(typ string, d map[string]any) {
		got = append(got, typ)
		last = d
		if typ == "snapshot" {
			b, _ := json.Marshal(d["parts"])
			content = string(b)
		}
	})
	if len(got) < 2 || got[len(got)-1] != "done" || !strings.Contains(content, "partial rest") || last["seq"] != nil {
		t.Fatalf("held stream %v, last %v, parts %s", got, last, content)
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
	if b.runs.byMessage(run.MessageID) != nil {
		t.Fatal("the refused request left a run in memory, which would shadow the real one")
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

// shutdown runs e.Shutdown in the background and returns a channel closed when it returns.
func shutdown(e *Engine, d time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() { e.Shutdown(d); close(done) }()
	until(nil, "the drain to start", e.draining)
	return done
}

func closed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal(what + " did not finish")
	}
}

// A deploy lets the running call finish and records it, starts no other, and hands the run over: the next process
// runs the rest, with nothing in doubt and no recovery counted.
func TestShutdownThenStartResumes(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	h.tool.block("write_a")
	cid, run := h.task(t, h.e, "calls: write_a, write_b")
	mid := run.MessageID
	h.tool.await(t, "write_a")
	done := shutdown(h.e, 20*time.Second)
	select {
	case <-done:
		t.Fatal("shutdown did not wait for the running call")
	case <-time.After(300 * time.Millisecond):
	}
	h.tool.release("write_a")
	closed(t, done, "shutdown")
	if r := h.row(t, mid); !r.found || r.state != "queued" || r.owner != "" || !r.hasCheckpoint {
		t.Fatalf("after the drain: %+v", r)
	}
	if m := h.message(t, cid, mid); m.Status != "streaming" || m.Error != "" {
		t.Fatalf("message after the drain: %q %q", m.Status, m.Error)
	}
	if h.tool.ran("write_b") != 0 {
		t.Fatal("a call started during the drain")
	}
	if run.detached() == nil {
		t.Fatal("an attached stream was told the run is done")
	}
	h.e.tick(context.Background()) // a draining engine claims nothing
	if r := h.row(t, mid); r.state != "queued" {
		t.Fatalf("a draining engine claimed: %+v", r)
	}
	h.tool.block("write_b")
	b.tick(context.Background())
	h.tool.await(t, "write_b")
	if r := h.row(t, mid); r.attempt != 2 || r.recoveries != 0 {
		t.Fatalf("a released run counts no recovery: %+v", r)
	}
	h.tool.release("write_b")
	m := h.settled(t, cid, mid)
	if want := "round 1. done: write_a ok #1 | write_b ok #1"; m.Status != "done" || m.Content != want {
		t.Fatalf("got %q %q, want %q", m.Status, m.Content, want)
	}
}

// A call that does not finish in time is cut off. It gets no result, although a tool server answers a cut call with
// an ordinary error: that would tell the model the write failed, and it would try again.
func TestShutdownCutsASlowCall(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	h.tool.block("write_a")
	cid, run := h.task(t, h.e, "calls: write_a")
	h.tool.await(t, "write_a")
	closed(t, shutdown(h.e, 100*time.Millisecond), "shutdown")
	h.tool.release("write_a")
	if r := h.row(t, run.MessageID); r.state != "queued" {
		t.Fatalf("after the cut: %+v", r)
	}
	m := h.resumeOn(t, b, cid, run.MessageID)
	if want := "round 1. done: " + unknownResult; m.Content != want || h.tool.ran("write_a") != 1 {
		t.Fatalf("write_a ran %d times; answer %q, want %q", h.tool.ran("write_a"), m.Content, want)
	}
}

// A model reply being streamed is not waited for: it is asked for again after the restart.
func TestShutdownDuringModelCall(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	cid, run := h.task(t, h.e, "hang")
	until(t, "the model to answer", func() bool { return strings.Contains(run.stateText(), "partial") })
	started := time.Now()
	closed(t, shutdown(h.e, 20*time.Second), "shutdown")
	if time.Since(started) > 5*time.Second {
		t.Fatalf("shutdown waited %v for a model reply", time.Since(started))
	}
	h.model.open()
	if m := h.resumeOn(t, b, cid, run.MessageID); m.Status != "done" || m.Content != "partial rest" {
		t.Fatalf("%q %q %q", m.Status, m.Content, m.Error)
	}
}

// After a drain the calls that had not started are marked so. That mark must be gone, on disk, before one of them
// runs: otherwise a crash during it would run it a second time.
func TestNothingStartedMarkIsClearedBeforeRunning(t *testing.T) {
	h := newDurable(t)
	b, c := h.peer(t), h.peer(t)
	h.tool.block("write_a")
	cid, run := h.task(t, h.e, "calls: write_a, write_b")
	mid := run.MessageID
	h.tool.await(t, "write_a")
	done := shutdown(h.e, 20*time.Second)
	h.tool.release("write_a")
	closed(t, done, "shutdown")
	h.tool.block("write_b")
	b.tick(context.Background())
	h.tool.await(t, "write_b")
	h.crash(t, b, b.runs.byMessage(mid))
	h.tool.release("write_b")
	m := h.resumeOn(t, c, cid, mid)
	if want := "round 1. done: write_a ok #1 | " + unknownResult; m.Content != want || h.tool.ran("write_b") != 1 {
		t.Fatalf("write_b ran %d times; answer %q, want %q", h.tool.ran("write_b"), m.Content, want)
	}
}

// A drain during a resumed round must not mark the round as not started while the call that was in flight at the
// crash is still in doubt.
func TestDrainKeepsTheDoubt(t *testing.T) {
	h := newDurable(t)
	b, c := h.peer(t), h.peer(t)
	h.tool.block("write_a")
	cid, run := h.task(t, h.e, "calls: read_a, write_a")
	mid := run.MessageID
	h.tool.await(t, "write_a")
	h.crash(t, h.e, run)
	h.tool.release("write_a")
	h.tool.block("read_a")
	b.tick(context.Background())
	h.tool.await(t, "read_a") // the resumed run calls the read again
	done := shutdown(b, 20*time.Second)
	h.tool.release("read_a")
	closed(t, done, "shutdown")
	m := h.resumeOn(t, c, cid, mid)
	if h.tool.ran("write_a") != 1 || !strings.HasSuffix(m.Content, unknownResult) {
		t.Fatalf("write_a ran %d times; answer %q", h.tool.ran("write_a"), m.Content)
	}
}

// Runs that cannot be resumed are stopped and saved as before: a temporary chat, and any chat with resume off.
func TestShutdownSavesPartialAnswer(t *testing.T) {
	h := newDurable(t)
	if err := h.e.Settings.Set(context.Background(), "runs.resume", false, false); err != nil {
		t.Fatal(err)
	}
	cid, run := h.task(t, h.e, "hang")
	until(t, "the model to answer", func() bool { return strings.Contains(run.stateText(), "partial") })
	tmp, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", Temporary: true,
		Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"hang"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	until(t, "the temporary run to answer", func() bool { return strings.Contains(tmp.stateText(), "partial") })
	closed(t, shutdown(h.e, 20*time.Second), "shutdown")
	if !run.Finished() || !tmp.Finished() || tmp.detached() != nil || run.detached() != nil {
		t.Fatal("Shutdown returned with a run still live")
	}
	if m := h.message(t, cid, run.MessageID); m.Status != "error" || m.Content == "" || !strings.Contains(m.Error, "restarted") {
		t.Fatalf("interrupted message: status %q content %q error %v", m.Status, m.Content, m.Error)
	}
	if h.row(t, run.MessageID).found {
		t.Fatal("the row of a run that cannot be resumed stayed")
	}
}

// The time limit of an unattended run is a wall-clock limit: a restart does not give it a new one.
func TestUnattendedDeadlineSurvivesResume(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	b := h.peer(t)
	h.tool.block("write_a")
	cid := h.taskChat(t)
	run, err := h.e.Start(ctx, h.u, &Request{Model: "m1", ChatID: cid, ID: "a1", Unattended: true,
		ToolIDs: []string{"durable-server"}, UserMessage: &UserMessage{ID: "q1", Content: "calls: write_a ; write_b"}})
	if err != nil {
		t.Fatal(err)
	}
	var left float64
	read := `SELECT extract(epoch FROM deadline - now())::float8 FROM run WHERE message_id = 'a1'`
	if err := h.pool.QueryRow(ctx, read).Scan(&left); err != nil || left <= 0 {
		t.Fatalf("no deadline on an unattended run: %v %v", left, err)
	}
	h.tool.await(t, "write_a")
	h.crash(t, h.e, run)
	h.tool.release("write_a")
	if _, err := h.pool.Exec(ctx, `UPDATE run SET deadline = now() + interval '1500 milliseconds'`); err != nil {
		t.Fatal(err)
	}
	h.tool.block("write_b")
	m := h.resumeOn(t, b, cid, "a1")
	if m.Status != "error" || m.Error != timeLimitMsg {
		t.Fatalf("%q %q", m.Status, m.Error)
	}
}

// The trace of the attempts of one run is one list: the later attempt's steps come after the earlier one's.
func TestTraceAcrossAttempts(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	h.tool.block("write_a")
	cid, run := h.task(t, h.e, "calls: write_a")
	h.tool.await(t, "write_a")
	done := shutdown(h.e, 20*time.Second)
	h.tool.release("write_a")
	closed(t, done, "shutdown")
	h.resumeOn(t, b, cid, run.MessageID)
	b.Wait()
	var steps, seqs, early, late int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*), count(DISTINCT seq), count(*) FILTER (WHERE seq < $2),
		count(*) FILTER (WHERE seq >= $2) FROM run_step WHERE run_id = $1`, run.ID, maxTraceSteps).Scan(&steps, &seqs, &early, &late); err != nil {
		t.Fatal(err)
	}
	if steps != seqs || early == 0 || late == 0 {
		t.Fatalf("steps %d, distinct seq %d, first attempt %d, second %d", steps, seqs, early, late)
	}
}

const answerB = `[{"question":"Which one?","answers":["B"]}]`

// told is what the model reads after the answer.
const told = "Q: Which one?\nA: B\n"

// parked starts a task that asks and waits until its run is parked. It returns the chat, the run and the call id.
func (h *durableHarness) parked(t *testing.T, e *Engine, script string) (string, *Run, string) {
	t.Helper()
	cid, run := h.task(t, e, "calls: "+script)
	until(t, "the run to park", func() bool { return h.row(t, run.MessageID).state == "waiting" })
	e.Wait() // returns only because no goroutine is left waiting for the answer
	r := h.row(t, run.MessageID)
	return cid, run, r.waitKey
}

func (h *durableHarness) pending(t *testing.T, cid, mid string) string {
	t.Helper()
	return string(h.message(t, cid, mid).PendingCalls)
}

func (h *durableHarness) exec_(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

// A question is a stored wait: the run holds nothing in memory, a restart keeps it, and the answer, given to any
// instance, carries the run on.
func TestParkedQuestionAnsweredAfterRestart(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	cid, run, call := h.parked(t, h.e, "ask_user")
	mid := run.MessageID
	if !errors.Is(run.detached(), errParked) || h.e.runs.byMessage(mid) != nil {
		t.Fatalf("the parked run is still in memory: %v", run.detached())
	}
	evs, _, _ := run.next(0)
	if got := types(evs); !strings.HasSuffix(got, "tool_call,ask_user") {
		t.Fatalf("events %s", got)
	}
	var hours float64
	if err := h.pool.QueryRow(ctx, `SELECT extract(epoch FROM wake_at - now())::float8 / 3600 FROM run WHERE message_id = $1`, mid).Scan(&hours); err != nil {
		t.Fatal(err)
	}
	if r := h.row(t, mid); r.owner != "" || call != "c1-0" || hours < 11.9 || hours > 12 {
		t.Fatalf("parked row %+v, waits %v hours", r, hours)
	}
	if m := h.message(t, cid, mid); m.Status != "streaming" || !strings.Contains(string(m.PendingCalls), `"c1-0"`) || !strings.Contains(string(m.PendingCalls), "Which one?") {
		t.Fatalf("stored %q %s", m.Status, m.PendingCalls)
	}
	b := h.peer(t) // the restarted server
	b.tick(ctx)
	if r := h.row(t, mid); r.state != "waiting" || r.attempt != 1 {
		t.Fatalf("a waiting run was claimed before it was answered: %+v", r)
	}
	if err := b.Answer(ctx, mid, "not-the-call", json.RawMessage(answerB)); !errors.Is(err, ErrNoSuchCall) {
		t.Fatalf("unknown call: %v", err)
	}
	if err := b.Answer(ctx, mid, call, json.RawMessage(answerB)); err != nil {
		t.Fatal(err)
	}
	if err := b.Answer(ctx, mid, call, json.RawMessage(`true`)); !errors.Is(err, ErrNoSuchCall) {
		t.Fatalf("a second answer: %v", err)
	}
	if p := h.pending(t, cid, mid); p != "[]" {
		t.Fatalf("the card stayed after the answer: %s", p)
	}
	m := h.resumeOn(t, b, cid, mid)
	if want := "round 1. done: " + told; m.Status != "done" || m.Content != want || string(m.PendingCalls) != "[]" {
		t.Fatalf("got %q %q %s, want %q", m.Status, m.Content, m.PendingCalls, want)
	}
	if n := strings.Count(string(m.Parts), `"callId": "c1-0"`); n != 1 || strings.Contains(string(m.Parts), `"running"`) {
		t.Fatalf("the question's step appears %d times: %s", n, m.Parts)
	}
}

func TestParkedQuestionTimesOut(t *testing.T) {
	h := newDurable(t)
	cid, run, call := h.parked(t, h.e, "ask_user")
	h.exec_(t, `UPDATE run SET wake_at = now() - interval '1 second'`)
	m := h.resumeOn(t, h.peer(t), cid, run.MessageID)
	if m.Status != "done" || m.Content != "round 1. done: The user did not answer the questions." || string(m.PendingCalls) != "[]" {
		t.Fatalf("%q %q %s", m.Status, m.Content, m.PendingCalls)
	}
	if err := h.e.Answer(context.Background(), run.MessageID, call, json.RawMessage(answerB)); !errors.Is(err, ErrNoSuchCall) {
		t.Fatalf("an answer after the end: %v", err)
	}
}

// A question that timed out is closed by the read that finds it so: an answer arriving after that is refused, not
// accepted and then ignored.
func TestTimeoutRacingAnswer(t *testing.T) {
	h := newDurable(t)
	_, run, call := h.parked(t, h.e, "ask_user")
	h.exec_(t, `UPDATE run SET wake_at = now() - interval '1 second', state = 'running', lease_owner = $1, lease_until = now() + interval '30 seconds'`, h.e.instance)
	x := &exec{run: &Run{ID: run.ID, MessageID: run.MessageID, attempt: 1}}
	got, err := h.e.takeAnswer(x, call)
	if err != nil || string(got) != "null" {
		t.Fatalf("the timed out question: %s %v", got, err)
	}
	if err := h.e.Answer(context.Background(), run.MessageID, call, json.RawMessage(answerB)); !errors.Is(err, ErrNoSuchCall) {
		t.Fatalf("a late answer was accepted: %v", err)
	}
}

// An answer that arrives while an instance already holds the run (claimed, or still parking) is kept for it.
func TestAnswerWhileResuming(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	cid, run, call := h.parked(t, h.e, "ask_user")
	mid := run.MessageID
	h.exec_(t, `UPDATE run SET state = 'running', lease_owner = 'claimer', lease_until = now() + interval '30 seconds', attempt = attempt + 1`)
	if err := h.e.Answer(ctx, mid, call, json.RawMessage(answerB)); err != nil {
		t.Fatal(err)
	}
	if r := h.row(t, mid); r.state != "running" || r.owner != "claimer" || r.answer == "" {
		t.Fatalf("answered while held: %+v", r)
	}
	h.expire(t, mid) // and that instance dies with it
	if m := h.resumeOn(t, h.peer(t), cid, mid); m.Content != "round 1. done: "+told {
		t.Fatalf("%q %q", m.Content, m.Error)
	}
}

// The answer can arrive between the card and the end of parking: the run is queued at once.
func TestAnswerBeforeParkingIsDone(t *testing.T) {
	h := newDurable(t)
	_, run, call := h.parked(t, h.e, "ask_user")
	mid := run.MessageID
	h.exec_(t, `UPDATE run SET state = 'running', lease_owner = $1, lease_until = now() + interval '30 seconds'`, h.e.instance)
	if err := h.e.Answer(context.Background(), mid, call, json.RawMessage(answerB)); err != nil {
		t.Fatal(err)
	}
	h.e.park(&exec{run: &Run{ID: run.ID, MessageID: mid, attempt: 1, wake: make(chan struct{})}})
	if r := h.row(t, mid); r.state != "queued" || r.owner != "" {
		t.Fatalf("parking over an answer: %+v", r)
	}
}

// A crash between storing the question and parking asks it again, with the time it had left.
func TestCrashBeforeParkingKeepsTheQuestion(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	cid, run, call := h.parked(t, h.e, "ask_user")
	mid := run.MessageID
	var before, after time.Time
	if err := h.pool.QueryRow(ctx, `SELECT wake_at FROM run`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	h.exec_(t, `UPDATE run SET state = 'running', lease_owner = 'dead', lease_until = now() - interval '1 second'`)
	b := h.peer(t)
	b.tick(ctx)
	until(t, "the run to park again", func() bool { return h.row(t, mid).state == "waiting" })
	b.Wait()
	if err := h.pool.QueryRow(ctx, `SELECT wake_at FROM run`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if r := h.row(t, mid); !after.Equal(before) || r.waitKey != call || r.recoveries != 1 || strings.Count(h.pending(t, cid, mid), `"c1-0"`) != 1 {
		t.Fatalf("parked again: %+v, wake %v then %v, pending %s", r, before, after, h.pending(t, cid, mid))
	}
}

// A shutdown that catches a parked run being resumed hands it back with the answer still there.
func TestDrainKeepsAParkedAnswer(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	cid, run, call := h.parked(t, h.e, "ask_user")
	mid := run.MessageID
	if err := h.e.Answer(ctx, mid, call, json.RawMessage(answerB)); err != nil {
		t.Fatal(err)
	}
	b := h.peer(t)
	c, ok, err := b.claim(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	b.startDrain() // the shutdown arrives before the resumed run reads the answer
	b.takeOver(c)
	b.Wait()
	if r := h.row(t, mid); r.state != "queued" || r.waitKey != call || r.answer == "" {
		t.Fatalf("after the drain: %+v", r)
	}
	if m := h.resumeOn(t, h.peer(t), cid, mid); m.Content != "round 1. done: "+told {
		t.Fatalf("the answer was lost: %q %q", m.Content, m.Error)
	}
}

func TestCancelParkedRun(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	b := h.peer(t)
	srv := h.serve(t, b)
	cid, run, call := h.parked(t, h.e, "write_a, ask_user, write_b")
	mid := run.MessageID
	post := func(path, body string) int {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("/api/v1/chats/another-chat/messages/"+mid+"/cancel", ""); got != http.StatusNotFound {
		t.Fatalf("cancel through another chat: %d", got)
	}
	if got := post("/api/v1/chats/"+cid+"/messages/"+mid+"/cancel", ""); got != http.StatusOK {
		t.Fatalf("cancel: %d", got)
	}
	if err := b.Answer(ctx, mid, call, json.RawMessage(answerB)); !errors.Is(err, ErrNoSuchCall) {
		t.Fatalf("an answer to a cancelled run: %v", err)
	}
	m := h.resumeOn(t, b, cid, mid)
	if m.Status != "stopped" || m.Content != "round 1. " || string(m.PendingCalls) != "[]" || h.tool.ran("write_b") != 0 {
		t.Fatalf("%q %q %s, write_b %d", m.Status, m.Content, m.PendingCalls, h.tool.ran("write_b"))
	}
}

// A write before a question keeps its result; one after it runs once, with nothing in doubt.
func TestWritesAroundAQuestion(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	srv := h.serve(t, b)
	cid, run, call := h.parked(t, h.e, "write_a, ask_user, write_b")
	mid := run.MessageID
	if h.tool.ran("write_a") != 1 || h.tool.ran("write_b") != 0 {
		t.Fatalf("before the answer: %v", h.tool.count)
	}
	// through the HTTP route: only the chat's own address works, and only once
	answer := func(chat string) int {
		resp, err := http.Post(srv.URL+"/api/v1/chats/"+chat+"/messages/"+mid+"/calls/"+call, "application/json",
			strings.NewReader(`{"value":`+answerB+`}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := []int{answer("another-chat"), answer(cid), answer(cid)}; !slices.Equal(got, []int{404, 200, 404}) {
		t.Fatalf("answer statuses %v", got)
	}
	m := h.resumeOn(t, b, cid, mid)
	if want := "round 1. done: write_a ok #1 | " + told + " | write_b ok #1"; m.Content != want {
		t.Fatalf("got %q %q, want %q", m.Content, m.Error, want)
	}
	if h.tool.ran("write_a") != 1 || h.tool.ran("write_b") != 1 {
		t.Fatalf("ran %v", h.tool.count)
	}
}

// The answer is recorded the moment it is read: a crash during the next write neither asks again nor repeats it.
func TestWriteAfterAnswerNotRepeated(t *testing.T) {
	h := newDurable(t)
	b, c := h.peer(t), h.peer(t)
	cid, run, call := h.parked(t, h.e, "ask_user, write_b")
	mid := run.MessageID
	if err := b.Answer(context.Background(), mid, call, json.RawMessage(answerB)); err != nil {
		t.Fatal(err)
	}
	h.tool.block("write_b")
	b.tick(context.Background())
	h.tool.await(t, "write_b")
	h.crash(t, b, b.runs.byMessage(mid))
	h.tool.release("write_b")
	m := h.resumeOn(t, c, cid, mid)
	if want := "round 1. done: " + told + " | " + unknownResult; m.Content != want || h.tool.ran("write_b") != 1 {
		t.Fatalf("write_b ran %d times; got %q %q, want %q", h.tool.ran("write_b"), m.Content, m.Error, want)
	}
}

// A browser attached to a parked run keeps its card: the stream is held, and goes on when the answer comes.
func TestHeldStreamShowsTheParkedQuestion(t *testing.T) {
	h := newDurable(t)
	b := h.peer(t)
	srv := h.serve(t, b)
	cid, run, call := h.parked(t, h.e, "ask_user")
	mid := run.MessageID
	resp, err := http.Get(srv.URL + "/api/v1/chats/" + cid + "/messages/" + mid + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var kinds []string
	var cards []int // open cards in each snapshot
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		sseEvents(t, resp, func(typ string, d map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			kinds = append(kinds, typ)
			if p, ok := d["pendingCalls"].([]any); ok {
				cards = append(cards, len(p))
			}
		})
	}()
	first := func() []int { mu.Lock(); defer mu.Unlock(); return slices.Clone(cards) }
	until(t, "the stored card", func() bool { return len(first()) > 0 })
	time.Sleep(5 * holdPoll)
	if got := first(); !slices.Equal(got, []int{1}) {
		t.Fatalf("cards %v", got)
	}
	if err := b.Answer(context.Background(), mid, call, json.RawMessage(answerB)); err != nil {
		t.Fatal(err)
	}
	b.tick(context.Background())
	closed(t, ended, "the held stream")
	mu.Lock()
	defer mu.Unlock()
	if kinds[len(kinds)-1] != "done" || cards[len(cards)-1] != 0 {
		t.Fatalf("held stream %v, cards %v", kinds, cards)
	}
}

// A caller that is not the web app is never asked (nobody could answer), so its run never parks.
func TestRunWithoutAPersonNeverParks(t *testing.T) {
	h := newDurable(t)
	cid := h.taskChat(t)
	srv := h.serve(t, h.e)
	resp, err := http.Post(srv.URL+"/api/chat/completions", "application/json", strings.NewReader(
		`{"model":"m1","stream":false,"chat_id":"`+cid+`","id":"api1","messages":[{"role":"user","content":"calls: ask_user"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct{ Content string }
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(out.Choices) != 1 || !strings.Contains(out.Choices[0].Message.Content, `there is no tool named "ask_user"`) {
		t.Fatalf("%d %+v", resp.StatusCode, out)
	}
	h.settled(t, cid, "api1")
}

// A stream:false caller whose run is handed over is told so, with a status the OpenAI SDKs are told not to retry:
// it is neither a success nor a hang, and sending the request again would run the tools again.
func TestAPICallerOfAHandedOverRun(t *testing.T) {
	h := newDurable(t)
	cid := h.taskChat(t)
	srv := h.serve(t, h.e)
	type reply struct {
		status int
		retry  string
		body   string
	}
	got := make(chan reply, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/chat/completions", "application/json", strings.NewReader(
			`{"model":"m1","stream":false,"chat_id":"`+cid+`","id":"api1","messages":[{"role":"user","content":"hang"}]}`))
		if err != nil {
			got <- reply{body: err.Error()}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- reply{resp.StatusCode, resp.Header.Get("x-should-retry"), string(b)}
	}()
	until(t, "the run to start", func() bool {
		r := h.e.runs.byMessage("api1")
		return r != nil && strings.Contains(r.stateText(), "partial")
	})
	closed(t, shutdown(h.e, 10*time.Second), "shutdown")
	select {
	case r := <-got:
		if r.status != http.StatusServiceUnavailable || r.retry != "false" || !strings.Contains(r.body, `"run_detached"`) || !strings.Contains(r.body, cid) {
			t.Fatalf("%+v", r)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the caller was left waiting")
	}
	// the caller sent its own conversation and no model reply was recorded yet, so this one cannot be rebuilt
	m := h.resumeOn(t, h.peer(t), cid, "api1")
	if m.Status != "error" || m.Error != crashedMsg {
		t.Fatalf("%q %q", m.Status, m.Error)
	}
}

// Temporary chats have nowhere to park: their question waits in memory, as before.
func TestTemporaryChatAsksInMemory(t *testing.T) {
	h := newDurable(t)
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", Temporary: true,
		Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"calls: ask_user"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	call := ""
	until(t, "the question", func() bool {
		evs, _, _ := run.next(0)
		for _, ev := range evs {
			if ev.Type == "ask_user" {
				call, _ = ev.Data["callId"].(string)
			}
		}
		return call != ""
	})
	if run.Finished() {
		t.Fatal("the temporary run did not wait")
	}
	if err := run.answer(call, json.RawMessage(answerB)); err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	if got := run.stateText(); got != "round 1. done: "+told {
		t.Fatalf("%q", got)
	}
}

// A run that cannot be resumed (here: runs.resume off) keeps what was saved of its reply when its owner dies.
func TestCrashOfARunThatCannotResumeKeepsItsText(t *testing.T) {
	h := newDurable(t)
	if err := h.e.Settings.Set(context.Background(), "runs.resume", false, false); err != nil {
		t.Fatal(err)
	}
	h.tool.block("write_b")
	cid, run := h.task(t, h.e, "calls: write_a ; write_b")
	h.tool.await(t, "write_b")
	h.crash(t, h.e, run)
	h.tool.release("write_b")
	m := h.resumeOn(t, h.peer(t), cid, run.MessageID)
	if m.Status != "error" || m.Error != crashedMsg || !strings.HasPrefix(m.Content, "round 1. ") || !strings.Contains(string(m.Parts), "write_a ok #1") {
		t.Fatalf("%q %q content %q parts %s", m.Status, m.Error, m.Content, m.Parts)
	}
	if h.tool.ran("write_a") != 1 || h.tool.ran("write_b") != 1 {
		t.Fatalf("ran %v", h.tool.count)
	}
}

// Features come back from the checkpoint only as far as the user may still use them.
func TestRestoreDropsWhatTheUserMayNoLongerUse(t *testing.T) {
	ck := &checkpoint{Features: map[string]bool{"web_search": true, "code_interpreter": true, "memory": false},
		ProviderTools: []json.RawMessage{json.RawMessage(`{"type":"web_search","search_context_size":"low"}`), json.RawMessage(`{"type":"image_generation"}`)}}
	may := auth.Permissions{}
	if err := json.Unmarshal([]byte(`{"features":{"code_interpreter":true}}`), &may); err != nil {
		t.Fatal(err)
	}
	x := &exec{u: &auth.User{ID: "u2", Role: "user", Permissions: may}, disabledNames: map[string]bool{}}
	ck.restore(x)
	if x.features["web_search"] || !x.features["code_interpreter"] || len(x.providerTools) != 1 || !strings.Contains(string(x.providerTools[0]), "image_generation") {
		t.Fatalf("features %v, provider tools %s", x.features, x.providerTools)
	}
	if len(ck.ProviderTools) != 2 {
		t.Fatal("restore changed the checkpoint")
	}
}

// A waiting run whose reply an older version failed (a rollback and back) is removed, so the message is not busy
// until the question's time is up. A live parked run and a run that is just starting stay.
func TestJanitorDropsRunsOfEndedReplies(t *testing.T) {
	h := newDurable(t)
	ctx := context.Background()
	_, stale, _ := h.parked(t, h.e, "ask_user")
	_, live, _ := h.parked(t, h.e, "ask_user")
	_, starting := h.task(t, h.e, "hang")
	until(t, "the third run", func() bool { return h.row(t, starting.MessageID).state == "running" })
	h.exec_(t, `UPDATE message SET status = 'error', error = 'x' WHERE id = ANY($1)`, []string{stale.MessageID, starting.MessageID})
	if _, err := h.e.failOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	for mid, want := range map[string]bool{stale.MessageID: false, live.MessageID: true, starting.MessageID: true} {
		if got := h.row(t, mid).found; got != want {
			t.Errorf("%s: row kept = %v, want %v", mid, got, want)
		}
	}
}
