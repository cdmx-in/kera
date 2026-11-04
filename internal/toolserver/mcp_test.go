package toolserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/tools"
)

func mcpServer(f *fakeMCP, a Auth) *Server {
	m := &Manager{http: &http.Client{}, sums: map[string][32]byte{}}
	return newServer(m, Row{ID: "fake", Kind: "mcp", URL: f.URL, Auth: a})
}

type recorder struct {
	mu     sync.Mutex
	events []tools.Event
	asks   []tools.AskRequest
	answer string
}

func (r *recorder) call(name, args string) tools.Call {
	return tools.Call{ID: "call-1", Name: name, Args: json.RawMessage(args), ChatID: "chat-1", MessageID: "msg-1",
		User: &auth.User{ID: "u1", Email: "u1@example.com"}, FileIDs: []string{"f1", "f2"},
		CallbackURL: "http://kera/api/v1/runs/r1", CallbackToken: "cb-token",
		Emit: func(e tools.Event) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() },
		Ask: func(_ context.Context, q tools.AskRequest) (json.RawMessage, error) {
			r.mu.Lock()
			r.asks = append(r.asks, q)
			r.mu.Unlock()
			return json.RawMessage(r.answer), nil
		}}
}

func TestMCPListPaginates(t *testing.T) {
	f := newFakeMCP(t)
	s := mcpServer(f, Auth{Type: "none"})
	c, err := s.listMCP(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, sp := range c.specs {
		names = append(names, sp.Name)
	}
	if strings.Join(names, ",") != "echo,ask,pic,fail,hidden" {
		t.Fatalf("names %v", names)
	}
	if c.specs[1].Description != "Ask the user" || string(c.specs[1].Parameters) != `{"type":"object","properties":{}}` {
		t.Fatalf("defaults not filled: %+v", c.specs[1])
	}
	if inits, lists, _, _ := f.stats(); inits != 1 || lists != 2 {
		t.Fatalf("inits %d lists %d", inits, lists)
	}
}

func TestMCPCallJSONAndHeaders(t *testing.T) {
	f := newFakeMCP(t)
	f.token = "static-key"
	s := mcpServer(f, Auth{Type: "bearer", key: "static-key"})
	var rec recorder
	res, err := s.callMCP(context.Background(), rec.call("echo", `{"text":"hi"}`), "")
	if err != nil || res.Content != "hi" || res.IsError {
		t.Fatalf("res %+v err %v", res, err)
	}
	_, _, h, _ := f.stats()
	for k, want := range map[string]string{"X-Kera-User-Id": "u1", "X-Kera-User-Email": "u1@example.com", "X-Kera-Chat-Id": "chat-1",
		"X-Kera-Message-Id": "msg-1", "X-Kera-Files": "f1,f2", "X-Kera-Callback": "http://kera/api/v1/runs/r1",
		"X-Kera-Callback-Token": "cb-token", "Mcp-Session-Id": "s1", "Mcp-Protocol-Version": "2025-06-18"} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
	res, _ = s.callMCP(context.Background(), rec.call("fail", `{}`), "")
	if !res.IsError || res.Content != "boom" {
		t.Fatalf("isError not kept: %+v", res)
	}
}

func TestMCPSessionExpiryReinitializes(t *testing.T) {
	f := newFakeMCP(t)
	s := mcpServer(f, Auth{Type: "none"})
	var rec recorder
	if _, err := s.callMCP(context.Background(), rec.call("echo", `{"text":"a"}`), ""); err != nil {
		t.Fatal(err)
	}
	f.expire()
	res, err := s.callMCP(context.Background(), rec.call("echo", `{"text":"b"}`), "")
	if err != nil || res.Content != "b" {
		t.Fatalf("res %+v err %v", res, err)
	}
	if inits, _, h, _ := f.stats(); inits != 2 || h.Get("Mcp-Session-Id") != "s2" {
		t.Fatalf("inits %d session %q", inits, h.Get("Mcp-Session-Id"))
	}
}

func TestMCPStatelessServer(t *testing.T) {
	f := newFakeMCP(t)
	f.stateless = true
	s := mcpServer(f, Auth{Type: "none"})
	var rec recorder
	res, err := s.callMCP(context.Background(), rec.call("echo", `{"text":"x"}`), "")
	if _, _, h, _ := f.stats(); err != nil || res.Content != "x" || h.Get("Mcp-Session-Id") != "" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestMCPUnauthorized(t *testing.T) {
	f := newFakeMCP(t)
	f.token = "right"
	s := mcpServer(f, Auth{Type: "user_oauth", Provider: "p"})
	var rec recorder
	_, err := s.callMCP(context.Background(), rec.call("echo", `{}`), "wrong")
	if !errors.Is(err, errRejected) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err %v", err)
	}
	if _, _, _, b := f.stats(); b[0] != "wrong" {
		t.Fatalf("bearer not sent: %v", b)
	}
}

func TestMCPElicitationOverSSE(t *testing.T) {
	f := newFakeMCP(t)
	s := mcpServer(f, Auth{Type: "none"})
	rec := recorder{answer: `[{"question":"Name","answers":["Ana"]},{"question":"color","answers":["Blue"]},{"question":"n","answers":["7"]}]`}
	res, err := s.callMCP(context.Background(), rec.call("ask", `{}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "accept Ana b 7" {
		t.Fatalf("content %q", res.Content)
	}
	if len(rec.asks) != 1 || rec.asks[0].Kind != "input" || rec.asks[0].Message != "Who and which colour?" {
		t.Fatalf("asks %+v", rec.asks)
	}
	var qs []askQuestion
	_ = json.Unmarshal(rec.asks[0].Questions, &qs)
	if len(qs) != 3 || qs[0].Header != "Name" || !qs[0].AllowOther || qs[1].Options[1].Label != "Blue" {
		t.Fatalf("questions %+v", qs)
	}
	var descs []string
	for _, e := range rec.events {
		descs = append(descs, e.Data.(map[string]any)["description"].(string))
	}
	if strings.Join(descs, "|") != "Looking things up|Found 3 sheets" {
		t.Fatalf("events %v", descs)
	}

	rec = recorder{answer: `null`} // dismissed
	res, _ = s.callMCP(context.Background(), rec.call("ask", `{}`), "")
	if !strings.HasPrefix(res.Content, "cancel") {
		t.Fatalf("dismissed -> %q", res.Content)
	}
	rec = recorder{answer: `[{"question":"Name","answers":[]}]`} // required field missing
	res, _ = s.callMCP(context.Background(), rec.call("ask", `{}`), "")
	if !strings.HasPrefix(res.Content, "decline") {
		t.Fatalf("missing required -> %q", res.Content)
	}
}

func TestElicitConfirm(t *testing.T) {
	s := &Server{row: Row{ID: "x", Name: "X"}}
	for answer, want := range map[string]string{`true`: "accept", `false`: "decline", `null`: "cancel"} {
		rec := recorder{answer: answer}
		got := s.elicit(context.Background(), rec.call("t", `{}`), json.RawMessage(`{"message":"Delete it?","requestedSchema":{"type":"object","properties":{}}}`))
		if got["action"] != want || rec.asks[0].Kind != "confirm" || rec.asks[0].Title != "X" {
			t.Errorf("%s -> %v (%+v)", answer, got, rec.asks)
		}
	}
}

func TestToResultStructuredFallback(t *testing.T) {
	s := &Server{m: &Manager{}, row: Row{ID: "x"}}
	var rec recorder
	res, err := s.toResult(context.Background(), rec.call("t", `{}`), json.RawMessage(`{"content":[],"structuredContent":{"rows":2}}`))
	if err != nil || res.Content != `{"rows":2}` {
		t.Fatalf("res %+v err %v", res, err)
	}
	res, _ = s.toResult(context.Background(), rec.call("t", `{}`), json.RawMessage(`{"content":[{"type":"text","text":"a"},
		{"type":"resource","resource":{"uri":"x://y","text":"b"}},{"type":"resource_link","uri":"x://z","name":"z"}]}`))
	if res.Content != "a\nb\nResource: z (x://z)" {
		t.Fatalf("joined %q", res.Content)
	}
}

func TestTruncate(t *testing.T) {
	if Truncate("héllo", 10) != "héllo" || Truncate("héllo", 0) != "héllo" {
		t.Fatal("cap applied where it should not")
	}
	got := Truncate("héllo wörld", 4)
	if !strings.HasPrefix(got, "héll\n\n[Truncated: the result had 11 characters and only the first 4") {
		t.Fatalf("got %q", got)
	}
}

func TestFilter(t *testing.T) {
	s := &Server{row: Row{NameFilter: []string{"read_*", "list_spreadsheets"}, Disabled: []string{"read_secret"}}}
	in := []tools.Spec{{Name: "read_sheet"}, {Name: "read_secret"}, {Name: "list_spreadsheets"}, {Name: "write_sheet"}}
	var got []string
	for _, sp := range s.filter(in) {
		got = append(got, sp.Name)
	}
	if strings.Join(got, ",") != "read_sheet,list_spreadsheets" {
		t.Fatalf("got %v", got)
	}
}
