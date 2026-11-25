package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type captured struct {
	path   string
	header http.Header
	body   map[string]any
}

// stub serves body for any request and records the last one.
func stub(t *testing.T, status int, ctype string, chunks ...string) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.path, c.header = r.URL.Path, r.Header.Clone()
		c.body = nil
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &c.body); err != nil {
				t.Errorf("request body: %v", err)
			}
		}
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		for _, ch := range chunks {
			io.WriteString(w, ch)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func collect(seq iter.Seq2[Event, error]) ([]Event, error) {
	var out []Event
	for e, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, e)
	}
	return out, nil
}

func client(t *testing.T, kind, url string) Client {
	t.Helper()
	c, err := New(Config{Kind: kind, BaseURL: url, APIKey: "test-key",
		Headers: http.Header{"X-Conn": {"c1"}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func text(evs []Event, k Kind) string {
	var sb strings.Builder
	for _, e := range evs {
		if e.Kind == k {
			sb.WriteString(e.Text)
		}
	}
	return sb.String()
}

func calls(evs []Event) []ToolCall {
	var out []ToolCall
	for _, e := range evs {
		if e.Kind == EventToolCall {
			out = append(out, e.ToolCall)
		}
	}
	return out
}

func last(evs []Event) Event { return evs[len(evs)-1] }

func usageOf(evs []Event) Usage {
	for _, e := range evs {
		if e.Kind == EventUsage {
			return e.Usage
		}
	}
	return Usage{}
}

func jsonEq(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("bad json %s: %v", got, err)
	}
	_ = json.Unmarshal([]byte(want), &b)
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func dig(m map[string]any, path ...any) any {
	var cur any = m
	for _, p := range path {
		switch k := p.(type) {
		case string:
			mm, _ := cur.(map[string]any)
			cur = mm[k]
		case int:
			l, _ := cur.([]any)
			if k >= len(l) {
				return nil
			}
			cur = l[k]
		}
	}
	return cur
}

var history = []Message{
	{Role: "system", Content: "be brief"},
	{Role: "user", Content: "look", Images: []Image{{MIME: "image/jpeg", Data: "QUJD"}}},
	{Role: "assistant", ToolCalls: []ToolCall{{ID: "c0", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}}},
	{Role: "tool", ToolCallID: "c0", Name: "lookup", Content: "found"},
}

var tools = []Tool{{Name: "lookup", Description: "find", Parameters: json.RawMessage(`{"type":"object"}`)}}

func TestOllamaStream(t *testing.T) {
	srv, got := stub(t, 200, "application/x-ndjson",
		`{"message":{"role":"assistant","content":"","thinking":"hmm"},"done":false}`+"\n",
		`{"message":{"role":"assistant","content":"Hel"},"done":false}`+"\n"+`{"message":{"content":"lo"},"done":false}`+"\n",
		`{"message":{"content":"","tool_calls":[{"function":{"name":"a","arguments":{"x":1}}},{"id":"given","function":{"name":"b","arguments":{}}}]},"done":false}`+"\n",
		`{"message":{"content":""},"done":true,"done_reason":"stop","prompt_eval_count":11,"eval_count":7}`+"\n")
	c := client(t, KindOllama, srv.URL+"/")
	evs, err := collect(c.Stream(context.Background(), Request{Model: "qwen", Messages: history, Tools: tools,
		Params:  map[string]any{"num_ctx": 65536, "think": false, "temperature": 0.2, "max_tokens": 100, "compact_token_threshold": 5},
		Headers: http.Header{"X-User-Email": {"a@example.com"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if text(evs, EventReasoning) != "hmm" || text(evs, EventText) != "Hello" {
		t.Fatalf("text: %+v", evs)
	}
	cs := calls(evs)
	if len(cs) != 2 || cs[0].Name != "a" || !strings.HasPrefix(cs[0].ID, "call_") || cs[1].ID != "given" {
		t.Fatalf("calls: %+v", cs)
	}
	jsonEq(t, cs[0].Arguments, `{"x":1}`)
	if u := usageOf(evs); u.PromptTokens != 11 || u.CompletionTokens != 7 {
		t.Fatalf("usage %+v", u)
	}
	if l := last(evs); l.Kind != EventDone || l.FinishReason != "tool_calls" {
		t.Fatalf("done %+v", l)
	}
	// request shape
	if got.path != "/api/chat" || got.header.Get("Authorization") != "Bearer test-key" ||
		got.header.Get("X-Conn") != "c1" || got.header.Get("X-User-Email") != "a@example.com" {
		t.Fatalf("request %s %v", got.path, got.header)
	}
	b := got.body
	if dig(b, "options", "num_ctx") != 65536.0 || dig(b, "options", "num_predict") != 100.0 || dig(b, "think") != false ||
		dig(b, "options", "compact_token_threshold") != nil || dig(b, "stream") != true {
		t.Fatalf("options: %v", b)
	}
	if dig(b, "messages", 1, "images", 0) != "QUJD" || dig(b, "messages", 3, "tool_name") != "lookup" ||
		dig(b, "messages", 2, "tool_calls", 0, "function", "arguments", "q") != "x" ||
		dig(b, "tools", 0, "function", "name") != "lookup" {
		t.Fatalf("messages: %v", b)
	}
}

func TestOllamaErrors(t *testing.T) {
	srv, _ := stub(t, 200, "application/x-ndjson", `{"message":{"content":"a"},"done":false}`+"\n"+`{"error":"model crashed"}`+"\n")
	evs, err := collect(client(t, KindOllama, srv.URL).Stream(context.Background(), Request{Model: "m"}))
	if err == nil || !strings.Contains(err.Error(), "model crashed") || text(evs, EventText) != "a" {
		t.Fatalf("err=%v evs=%+v", err, evs)
	}
	srv2, _ := stub(t, 404, "application/json", `{"error":"model \"m\" not found"}`)
	_, err = collect(client(t, KindOllama, srv2.URL).Stream(context.Background(), Request{Model: "m"}))
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 404 || !strings.Contains(he.Body, "not found") {
		t.Fatalf("err=%v", err)
	}
	srv3, _ := stub(t, 200, "application/x-ndjson", `{"message":{"content":"a"},"done":false}`+"\n")
	if _, err := collect(client(t, KindOllama, srv3.URL).Stream(context.Background(), Request{Model: "m"})); err == nil {
		t.Fatal("truncated stream accepted")
	}
}

func TestOllamaListEmbed(t *testing.T) {
	srv, got := stub(t, 200, "application/json", `{"models":[{"name":"qwen:7b","model":"qwen:7b"},{"name":"bge"}]}`)
	ids, err := client(t, KindOllama, srv.URL).ListModels(context.Background())
	if err != nil || strings.Join(ids, ",") != "qwen:7b,bge" || got.path != "/api/tags" {
		t.Fatalf("%v %v %s", ids, err, got.path)
	}
	srv2, got2 := stub(t, 200, "application/json", `{"embeddings":[[0.1,0.2],[0.3,0.4]]}`)
	vecs, err := client(t, KindOllama, srv2.URL).Embed(context.Background(), "bge", []string{"a", "b"})
	if err != nil || len(vecs) != 2 || vecs[1][0] != float32(0.3) || got2.path != "/api/embed" || dig(got2.body, "input", 1) != "b" {
		t.Fatalf("%v %v %v", vecs, err, got2.body)
	}
}

func sse(events ...string) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = "data: " + e + "\n\n"
	}
	return out
}

func TestOpenAIStream(t *testing.T) {
	srv, got := stub(t, 200, "text/event-stream", sse(
		`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think "}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning":"more"}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":" again","reasoning":" again"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"other","arguments":"{\"y\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"weather","arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Pune\"}"}},{"index":1,"function":{"arguments":"2}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":9,"total_tokens":29}}`,
		`[DONE]`)...)
	for _, base := range []string{srv.URL, srv.URL + "/v1", srv.URL + "/v1/"} {
		evs, err := collect(client(t, KindOpenAI, base).Stream(context.Background(), Request{Model: "gpt", Messages: history, Tools: tools,
			Params: map[string]any{"temperature": 0.5, "num_ctx": 4096, "reasoning_effort": "low"}, Headers: http.Header{"X-Conn": {"override"}}}))
		if err != nil {
			t.Fatal(err)
		}
		if got.path != "/v1/chat/completions" {
			t.Fatalf("path %s for base %s", got.path, base)
		}
		if text(evs, EventReasoning) != "think more again" || text(evs, EventText) != "Hi" {
			t.Fatalf("text %+v", evs)
		}
		cs := calls(evs)
		if len(cs) != 2 || cs[0].ID != "call_a" || cs[0].Name != "get_weather" || cs[1].Name != "other" {
			t.Fatalf("calls %+v", cs)
		}
		jsonEq(t, cs[0].Arguments, `{"city":"Pune"}`)
		jsonEq(t, cs[1].Arguments, `{"y":2}`)
		if u := usageOf(evs); u.PromptTokens != 20 || u.CompletionTokens != 9 {
			t.Fatalf("usage %+v", u)
		}
		if l := last(evs); l.Kind != EventDone || l.FinishReason != "tool_calls" {
			t.Fatalf("done %+v", l)
		}
	}
	b := got.body
	if dig(b, "stream_options", "include_usage") != true || dig(b, "temperature") != 0.5 || dig(b, "num_ctx") != nil ||
		dig(b, "reasoning_effort") != "low" || dig(b, "tools", 0, "function", "name") != "lookup" {
		t.Fatalf("body %v", b)
	}
	if dig(b, "messages", 1, "content", 1, "image_url", "url") != "data:image/jpeg;base64,QUJD" ||
		dig(b, "messages", 2, "tool_calls", 0, "function", "arguments") != `{"q":"x"}` ||
		dig(b, "messages", 3, "tool_call_id") != "c0" {
		t.Fatalf("messages %v", b["messages"])
	}
	if got.header.Get("X-Conn") != "override" || got.header.Get("Authorization") != "Bearer test-key" {
		t.Fatalf("headers %v", got.header)
	}
}

func TestOpenAIErrors(t *testing.T) {
	srv, _ := stub(t, 200, "text/event-stream", sse(`{"choices":[{"delta":{"content":"a"}}]}`, `{"error":{"message":"rate limited"}}`)...)
	if _, err := collect(client(t, KindOpenAI, srv.URL).Stream(context.Background(), Request{Model: "m"})); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err=%v", err)
	}
	srv2, _ := stub(t, 401, "application/json", `{"error":{"message":"bad key"}}`)
	_, err := collect(client(t, KindOpenAI, srv2.URL).Stream(context.Background(), Request{Model: "m"}))
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 401 || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("err=%v", err)
	}
	srv3, _ := stub(t, 200, "text/event-stream", sse(`{"choices":[{"delta":{"content":"a"}}]}`)...)
	if _, err := collect(client(t, KindOpenAI, srv3.URL).Stream(context.Background(), Request{Model: "m"})); err == nil {
		t.Fatal("truncated stream accepted")
	}
}

func TestOpenAIListEmbed(t *testing.T) {
	srv, got := stub(t, 200, "application/json", `{"object":"list","data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`)
	ids, err := client(t, KindResponses, srv.URL).ListModels(context.Background())
	if err != nil || strings.Join(ids, ",") != "gpt-a,gpt-b" || got.path != "/v1/models" {
		t.Fatalf("%v %v %s", ids, err, got.path)
	}
	srv2, got2 := stub(t, 200, "application/json", `{"data":[{"index":1,"embedding":[2]},{"index":0,"embedding":[1]}]}`)
	vecs, err := client(t, KindOpenAI, srv2.URL+"/v1").Embed(context.Background(), "e", []string{"a", "b"})
	if err != nil || vecs[0][0] != 1 || vecs[1][0] != 2 || got2.path != "/v1/embeddings" {
		t.Fatalf("%v %v %s", vecs, err, got2.path)
	}
}

func TestResponsesStream(t *testing.T) {
	srv, got := stub(t, 200, "text/event-stream", []string{
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{}}\n\n",
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"plan\"}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"See \"}\n\n",
		"data: {\"type\":\"response.output_text.annotation.added\",\"annotation\":{\"type\":\"url_citation\",\"url\":\"https://a.example\",\"title\":\"A\"}}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"this\"}\n\n",
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"annotations\":[{\"type\":\"url_citation\",\"url\":\"https://a.example\",\"title\":\"A\"},{\"type\":\"url_citation\",\"url\":\"https://b.example\",\"title\":\"B\"}]}]}}\n\n",
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"fc1\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"\"}}\n\n",
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"fc2\",\"call_id\":\"call_2\",\"name\":\"other\",\"arguments\":\"\"}}\n\n",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc1\",\"delta\":\"{\\\"q\\\":\"}\n\n",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc2\",\"delta\":\"{}\"}\n\n",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc1\",\"delta\":\"\\\"z\\\"}\"}\n\n",
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"fc1\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"z\\\"}\"}}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":30,\"output_tokens\":12}}}\n\n",
	}...)
	c := client(t, KindResponses, srv.URL+"/v1")
	evs, err := collect(c.Stream(context.Background(), Request{Model: "gpt", Messages: history, Tools: tools,
		ProviderTools: []json.RawMessage{json.RawMessage(`{"type":"web_search","search_context_size":"low"}`)},
		Params:        map[string]any{"reasoning_effort": "medium", "max_tokens": 500, "seed": 1}}))
	if err != nil {
		t.Fatal(err)
	}
	if text(evs, EventReasoning) != "plan" || text(evs, EventText) != "See this" {
		t.Fatalf("text %+v", evs)
	}
	var cites []string
	for _, e := range evs {
		if e.Kind == EventCitation {
			cites = append(cites, e.Citation.URL+"|"+e.Citation.Title)
		}
	}
	if strings.Join(cites, ",") != "https://a.example|A,https://b.example|B" {
		t.Fatalf("citations %v", cites)
	}
	cs := calls(evs)
	if len(cs) != 2 || cs[0].ID != "call_1" || cs[1].ID != "call_2" {
		t.Fatalf("calls %+v", cs)
	}
	jsonEq(t, cs[0].Arguments, `{"q":"z"}`)
	jsonEq(t, cs[1].Arguments, `{}`)
	if u := usageOf(evs); u.PromptTokens != 30 || u.CompletionTokens != 12 {
		t.Fatalf("usage %+v", u)
	}
	if l := last(evs); l.FinishReason != "tool_calls" {
		t.Fatalf("done %+v", l)
	}
	b := got.body
	if got.path != "/v1/responses" || b["instructions"] != "be brief" || dig(b, "reasoning", "effort") != "medium" ||
		dig(b, "max_output_tokens") != 500.0 || dig(b, "seed") != nil {
		t.Fatalf("body %v", b)
	}
	if dig(b, "input", 0, "content", 1, "type") != "input_image" || dig(b, "input", 1, "type") != "function_call" ||
		dig(b, "input", 1, "call_id") != "c0" || dig(b, "input", 2, "type") != "function_call_output" ||
		dig(b, "input", 2, "output") != "found" {
		t.Fatalf("input %v", b["input"])
	}
	if dig(b, "tools", 0, "name") != "lookup" || dig(b, "tools", 1, "type") != "web_search" {
		t.Fatalf("tools %v", b["tools"])
	}
}

func TestResponsesErrors(t *testing.T) {
	srv, _ := stub(t, 200, "text/event-stream", sse(`{"type":"response.output_text.delta","delta":"a"}`,
		`{"type":"response.failed","response":{"error":{"message":"server overloaded"}}}`)...)
	if _, err := collect(client(t, KindResponses, srv.URL).Stream(context.Background(), Request{Model: "m"})); err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err=%v", err)
	}
	srv2, _ := stub(t, 200, "text/event-stream", sse(`{"type":"error","message":"bad tool"}`)...)
	if _, err := collect(client(t, KindResponses, srv2.URL).Stream(context.Background(), Request{Model: "m"})); err == nil || !strings.Contains(err.Error(), "bad tool") {
		t.Fatalf("err=%v", err)
	}
	srv3, _ := stub(t, 200, "text/event-stream", sse(`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`)...)
	evs, err := collect(client(t, KindResponses, srv3.URL).Stream(context.Background(), Request{Model: "m"}))
	if err != nil || last(evs).FinishReason != "length" {
		t.Fatalf("%v %+v", err, evs)
	}
}

func TestIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		gap := 30 * time.Millisecond
		if r.URL.Query().Get("stall") != "" {
			gap = 2 * time.Second
		}
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%d\"}}]}\n\n", i)
			w.(http.Flusher).Flush()
			select {
			case <-time.After(gap):
			case <-r.Context().Done():
				return
			}
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c, _ := New(Config{Kind: KindOpenAI, BaseURL: srv.URL, IdleTimeout: 150 * time.Millisecond})
	// steady chunks keep the stream alive past the idle timeout in total
	evs, err := collect(c.Stream(context.Background(), Request{Model: "m"}))
	if err != nil || text(evs, EventText) != "01234" {
		t.Fatalf("steady: %v %+v", err, evs)
	}
	stall, _ := New(Config{Kind: KindOpenAI, BaseURL: srv.URL, IdleTimeout: 150 * time.Millisecond,
		HTTP: &http.Client{Transport: rewrite{"stall=1"}}})
	start := time.Now()
	_, err = collect(stall.Stream(context.Background(), Request{Model: "m"}))
	if !errors.Is(err, ErrIdleTimeout) || time.Since(start) > time.Second {
		t.Fatalf("stall: %v after %s", err, time.Since(start))
	}
}

// rewrite adds a query string to every request.
type rewrite struct{ q string }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.RawQuery = r.q
	return http.DefaultTransport.RoundTrip(req)
}

func TestLineLimit(t *testing.T) {
	long := strings.NewReader(strings.Repeat("x", maxLine+1) + "\n")
	var err error
	for _, e := range rawLines(long) {
		err = e
	}
	if err == nil {
		t.Fatal("over-long line accepted")
	}
}

func TestIsContextOverflow(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ollama template after the server cut the prompt", &HTTPError{Status: 500, Body: `{"error":"no user query found in messages"}`}, true},
		{"ollama exceeded", &HTTPError{Status: 500, Body: `{"error":"prompt too long; exceeded max context length by 1234 tokens"}`}, true},
		{"openai", &HTTPError{Status: 400, Body: `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","code":"context_length_exceeded"}}`}, true},
		{"vllm", &HTTPError{Status: 400, Body: `{"message":"This model's Maximum Context Length is 4096 tokens"}`}, true},
		{"litellm", &HTTPError{Status: 400, Body: `litellm.ContextWindowExceededError: ...`}, true},
		{"anthropic style", &HTTPError{Status: 400, Body: `{"message":"prompt is too long: 250000 tokens > 200000 maximum"}`}, true},
		{"too large for the proxy", &HTTPError{Status: 413, Body: "<html>"}, true},
		{"wrapped", fmt.Errorf("call: %w", &HTTPError{Status: 500, Body: `{"error":"no user query found in messages"}`}), true},
		{"ollama stream error", errors.New("ollama: prompt too long; exceeded max context length"), true},
		{"plain 500", &HTTPError{Status: 500, Body: "internal error"}, false},
		{"bad gateway", &HTTPError{Status: 502, Body: "bad gateway"}, false},
		{"rate limit", &HTTPError{Status: 429, Body: "slow down"}, false},
		{"auth", &HTTPError{Status: 401, Body: "bad key"}, false},
		{"the url names a context path", &HTTPError{Method: "POST", URL: "http://x/context-window/chat", Status: 500, Body: "oops"}, false},
		{"idle timeout", ErrIdleTimeout, false},
	} {
		if got := IsContextOverflow(c.err); got != c.want {
			t.Errorf("%s: IsContextOverflow = %v, want %v", c.name, got, c.want)
		}
	}
}
