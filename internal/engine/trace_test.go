package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/retrieval"
	"github.com/cdmx-in/kera/internal/tools"
)

type stepRow struct {
	Kind, Name, Finish, Error  string
	Round                      int
	Prompt, Cached, Completion *int
	Detail                     map[string]any
}

// stepsOf reads a finished run's trace in order.
func (h *harness) stepsOf(t *testing.T, runID string) []stepRow {
	t.Helper()
	h.e.Wait()
	rows, err := h.pool.Query(context.Background(), `SELECT kind, name, round, prompt_tokens, cached_tokens, completion_tokens, finish, error, detail
		FROM run_step WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []stepRow
	for rows.Next() {
		var s stepRow
		var d []byte
		if err := rows.Scan(&s.Kind, &s.Name, &s.Round, &s.Prompt, &s.Cached, &s.Completion, &s.Finish, &s.Error, &d); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(d, &s.Detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (h *harness) set(t *testing.T, kv map[string]any) {
	t.Helper()
	for k, v := range kv {
		if err := h.e.Settings.Set(context.Background(), k, v, false); err != nil {
			t.Fatal(err)
		}
	}
}

// A tool run writes one row per model round and tool call: sizes, tokens and timings, no text.
func TestTraceRecordsRoundsAndToolCalls(t *testing.T) {
	h := newHarness(t)
	tools.Register(&fakeTools{})
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", Temporary: true,
		ToolIDs: []string{"server:fake-server"}, Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"please use tool, secret-prompt-text"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	got := h.stepsOf(t, run.ID)
	if len(got) != 3 {
		t.Fatalf("%d steps: %+v", len(got), got)
	}
	m0, tool, m1 := got[0], got[1], got[2]
	if m0.Kind != "model" || m0.Round != 0 || m0.Finish != "tool_calls" || m0.Detail["tool_calls"] != 1.0 || m0.Detail["tools_offered"].(float64) < 1 || m0.Detail["tool_def_chars"].(float64) <= 0 {
		t.Fatalf("first round %+v", m0)
	}
	if m0.Prompt == nil || *m0.Prompt != 10 || m0.Completion == nil || *m0.Completion != 2 || m0.Cached != nil {
		t.Fatalf("tokens %v %v cached %v (the fake server reports no cached count)", m0.Prompt, m0.Completion, m0.Cached)
	}
	if tool.Kind != "tool" || tool.Name != "fake_tool" || tool.Round != 0 || tool.Detail["server"] != "fake-server" ||
		tool.Detail["result_chars"] != float64(len("tool says hi")) || tool.Detail["is_error"] != false || tool.Detail["truncated"] != false {
		t.Fatalf("tool step %+v", tool)
	}
	if m1.Kind != "model" || m1.Round != 1 || m1.Finish != "stop" || m1.Detail["truncated"] != false || m1.Detail["prompt_chars"].(float64) <= m0.Detail["prompt_chars"].(float64) {
		t.Fatalf("second round %+v", m1)
	}
	// nothing typed by the user, said by the model or returned by the tool is in the table
	var dump string
	if err := h.pool.QueryRow(context.Background(), `SELECT string_agg(to_jsonb(r)::text, ' ') FROM run_step r`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"secret-prompt-text", "tool says hi", "hello world", "done:"} {
		if strings.Contains(dump, text) {
			t.Fatalf("the trace holds %q: %s", text, dump)
		}
	}
}

// A failed model call is recorded as a code and a status, not the server's message.
func TestTraceFlagsFailures(t *testing.T) {
	h := newHarness(t)
	h.set(t, map[string]any{"chat.model_retries": 0}) // retries are tested in context_test.go
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.Error(w, "upstream says: key sk-test-not-a-real-key rejected", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"bad-m"}]}`)
	}))
	t.Cleanup(bad.Close)
	if _, err := h.pool.Exec(context.Background(), `INSERT INTO connection (id, kind, base_url, model_ids) VALUES ('bad', 'openai', $1, '{bad-m}')`, bad.URL); err != nil {
		t.Fatal(err)
	}
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "bad-m", Temporary: true,
		Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	got := h.stepsOf(t, run.ID)
	if len(got) != 1 || got[0].Kind != "model" || got[0].Error != "provider_error" || got[0].Detail["http_status"] != 502.0 {
		t.Fatalf("steps %+v", got)
	}
	var dump string
	if err := h.pool.QueryRow(context.Background(), `SELECT string_agg(to_jsonb(r)::text, ' ') FROM run_step r`).Scan(&dump); err != nil || strings.Contains(dump, "sk-test") {
		t.Fatalf("the server's message is in the trace: %s %v", dump, err)
	}
}

// Knowledge retrieval and compaction get their own steps.
func TestTraceRetrievalAndCompaction(t *testing.T) {
	h := newHarness(t)
	retrieval.Register(fakeRetriever{})
	defer retrieval.Register(nil)
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `INSERT INTO model (id, base_model_id, knowledge) VALUES ('kb', 'm1', '[{"name":"Policies","mode":"retrieve"}]')`); err != nil {
		t.Fatal(err)
	}
	h.set(t, map[string]any{"chat.compaction.enable": true, "chat.compaction.threshold": 100, "chat.compaction.retention": 30})
	cid := h.chat(t)
	var parent *string
	for i := range 6 {
		id := fmt.Sprint("m", i)
		if err := h.e.Chats.InsertMessage(ctx, &chats.Message{ID: id, ChatID: cid, ParentID: parent, Role: []string{"user", "assistant"}[i%2], Content: strings.Repeat("y", 200)}); err != nil {
			t.Fatal(err)
		}
		parent = ptr(id)
	}
	run, err := h.e.Start(ctx, h.u, &Request{Model: "kb", StreamFormat: "events", ChatID: cid, ID: "a",
		UserMessage: &UserMessage{ID: "q", ParentID: parent, Content: "next"}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	got := h.stepsOf(t, run.ID)
	kinds := ""
	for _, s := range got {
		kinds += s.Kind + " "
	}
	// compaction runs once the prompt and tools exist, so their size counts; the 100-token window is also far under the
	// prompt, which the guard notes (nothing it can shorten, the window is only assumed, so the call is sent)
	if kinds != "retrieval compaction compaction model " {
		t.Fatalf("kinds %q", kinds)
	}
	r, c := got[0], got[1]
	if g := got[2]; g.Name != "too_large" || g.Detail["sent"] != true || g.Error != "" {
		t.Fatalf("guard step %+v", g)
	}
	if c.Name != "summarise" || c.Error != "" || c.Detail["turns_summarised"].(float64) < 2 || c.Detail["tokens_after"].(float64) >= c.Detail["tokens_before"].(float64) {
		t.Fatalf("compaction step %+v", c)
	}
	if r.Name != "knowledge" || r.Detail["hits"] != 1.0 || r.Detail["top_score"] != 0.9 || r.Detail["collections"] != 1.0 || r.Detail["reranked"] != false {
		t.Fatalf("retrieval step %+v", r)
	}
}

// With trace.enable off nothing is written.
func TestTraceOff(t *testing.T) {
	h := newHarness(t)
	h.set(t, map[string]any{"trace.enable": false})
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", Temporary: true, Messages: []WireMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	if got := h.stepsOf(t, run.ID); len(got) != 0 {
		t.Fatalf("steps %+v", got)
	}
}

// A trace that cannot be written never fails the chat: the answer is still saved and delivered.
func TestTraceWriteFailureDoesNotFailTheRun(t *testing.T) {
	h := newHarness(t)
	if _, err := h.pool.Exec(context.Background(), `DROP TABLE run_step`); err != nil {
		t.Fatal(err)
	}
	cid := h.chat(t)
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", StreamFormat: "events", ChatID: cid,
		UserMessage: &UserMessage{Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := wait(t, run)
	h.e.Wait()
	if strings.Contains(types(evs), "error") {
		t.Fatalf("events %s", types(evs))
	}
	var status, content string
	if err := h.pool.QueryRow(context.Background(), `SELECT status, content FROM message WHERE id = $1`, run.MessageID).Scan(&status, &content); err != nil || status != "done" || content != "hello world" {
		t.Fatalf("message %q %q: %v", status, content, err)
	}
}

// Steps beyond the cap are counted on the last kept step, not stored.
func TestTraceCap(t *testing.T) {
	var tr runTrace
	tr.enable(true)
	for range maxTraceSteps + 5 {
		tr.add(traceStep{Kind: stepTool, Detail: map[string]any{}})
	}
	steps, dropped := tr.take()
	if len(steps) != maxTraceSteps || dropped != 5 {
		t.Fatalf("kept %d dropped %d", len(steps), dropped)
	}
	var off runTrace
	off.add(traceStep{Kind: stepTool})
	if s, _ := off.take(); len(s) != 0 {
		t.Fatal("a trace that is off kept a step")
	}
}

// The purge deletes rows older than trace.retention_days (30 by default, at least one day).
func TestTracePurge(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	add := func(run string, age time.Duration) {
		if _, err := h.pool.Exec(ctx, `INSERT INTO run_step (run_id, message_id, user_id, model_id, seq, kind, started_at) VALUES ($1, $1, 'u1', 'm1', 0, 'model', $2)`,
			run, time.Now().Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	left := func() string {
		rows, err := h.pool.Query(ctx, `SELECT run_id FROM run_step ORDER BY run_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		return strings.Join(ids, ",")
	}
	const day = 24 * time.Hour
	add("fresh", time.Hour)
	add("ten-days", 10*day)
	add("forty-days", 40*day)
	if n, err := h.e.purgeTrace(ctx); err != nil || n != 1 || left() != "fresh,ten-days" {
		t.Fatalf("default retention: deleted %d err %v left %s", n, err, left())
	}
	h.set(t, map[string]any{"trace.retention_days": 7})
	if n, err := h.e.purgeTrace(ctx); err != nil || n != 1 || left() != "fresh" {
		t.Fatalf("7 days: deleted %d err %v left %s", n, err, left())
	}
	h.set(t, map[string]any{"trace.retention_days": 0}) // a zero or negative value still keeps a day
	if n, err := h.e.purgeTrace(ctx); err != nil || n != 0 || left() != "fresh" {
		t.Fatalf("0 days: deleted %d err %v left %s", n, err, left())
	}
}
