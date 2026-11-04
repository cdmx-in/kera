package toolserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMCP is a Streamable HTTP MCP server (initialize-based revision) for tests.
type fakeMCP struct {
	*httptest.Server
	token string // required bearer ("" = none)

	mu        sync.Mutex
	sessions  map[string]string // session id -> negotiated version
	inits     int
	lists     int
	lastHdr   http.Header
	bearers   []string
	waiting   map[string]chan rpcMsg // server request id -> client response
	stateless bool                   // no Mcp-Session-Id
}

var pngBytes = []byte("\x89PNG\r\n\x1a\nfake")

func newFakeMCP(t *testing.T) *fakeMCP {
	f := &fakeMCP{sessions: map[string]string{}, waiting: map[string]chan rpcMsg{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// stats returns counters under the lock (the race detector cannot see HTTP as synchronisation).
func (f *fakeMCP) stats() (inits, lists int, hdr http.Header, bearers []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inits, f.lists, f.lastHdr, append([]string{}, f.bearers...)
}

func (f *fakeMCP) expire() { f.mu.Lock(); f.sessions = map[string]string{}; f.mu.Unlock() }

func (f *fakeMCP) setToken(tok string) { f.mu.Lock(); f.token = tok; f.mu.Unlock() }

func (f *fakeMCP) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	want := f.token
	f.bearers = append(f.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	f.mu.Unlock()
	if want != "" && r.Header.Get("Authorization") != "Bearer "+want {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="x"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || !strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Error(w, "bad accept", http.StatusNotAcceptable)
		return
	}
	var m rpcMsg
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if m.Method == "" { // a response to one of our requests
		f.mu.Lock()
		ch := f.waiting[string(m.ID)]
		f.mu.Unlock()
		if ch != nil {
			ch <- m
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if m.Method == "initialize" {
		var p struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if _, ok := p.Capabilities["elicitation"]; !ok {
			http.Error(w, "client must declare elicitation", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.inits++
		sid := fmt.Sprintf("s%d", f.inits)
		f.sessions[sid] = "2025-06-18"
		f.mu.Unlock()
		if !f.stateless {
			w.Header().Set("Mcp-Session-Id", sid)
		}
		writeJSON(w, rpcMsg{JSONRPC: "2.0", ID: m.ID, Result: mustJSON(map[string]any{"protocolVersion": "2025-06-18",
			"capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake"}})})
		return
	}
	if !f.stateless {
		sid := r.Header.Get("Mcp-Session-Id")
		f.mu.Lock()
		ver, ok := f.sessions[sid]
		f.mu.Unlock()
		if sid == "" {
			http.Error(w, "missing session", http.StatusBadRequest)
			return
		}
		if !ok {
			http.Error(w, "session expired", http.StatusNotFound)
			return
		}
		if r.Header.Get("MCP-Protocol-Version") != ver {
			http.Error(w, "bad protocol version header", http.StatusBadRequest)
			return
		}
	}
	if m.ID == nil { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	f.mu.Lock()
	f.lastHdr = r.Header.Clone()
	f.mu.Unlock()
	switch m.Method {
	case "tools/list":
		f.list(w, m)
	case "tools/call":
		f.call(w, m)
	default:
		writeJSON(w, rpcMsg{JSONRPC: "2.0", ID: m.ID, Error: &rpcError{Code: -32601, Message: "unknown"}})
	}
}

func (f *fakeMCP) list(w http.ResponseWriter, m rpcMsg) {
	f.mu.Lock()
	f.lists++
	f.mu.Unlock()
	var p struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(m.Params, &p)
	obj := `{"type":"object","properties":{"text":{"type":"string"}}}`
	page1 := []mcpTool{{Name: "echo", Description: "Echo text", InputSchema: json.RawMessage(obj)}, {Name: "ask", Title: "Ask the user"}}
	page2 := []mcpTool{{Name: "pic", Description: "An image"}, {Name: "fail", Description: "Fails"}, {Name: "hidden", Description: "Disabled by config"}}
	res := map[string]any{"tools": page1, "nextCursor": "p2"}
	if p.Cursor == "p2" {
		res = map[string]any{"tools": page2}
	}
	writeJSON(w, rpcMsg{JSONRPC: "2.0", ID: m.ID, Result: mustJSON(res)})
}

func (f *fakeMCP) call(w http.ResponseWriter, m rpcMsg) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Meta      struct {
			ProgressToken any `json:"progressToken"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(m.Params, &p)
	text := func(s string, isErr bool) json.RawMessage {
		return mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": s}}, "isError": isErr})
	}
	switch p.Name {
	case "echo":
		writeJSON(w, rpcMsg{JSONRPC: "2.0", ID: m.ID, Result: text(fmt.Sprint(p.Arguments["text"]), false)})
	case "fail":
		writeJSON(w, rpcMsg{JSONRPC: "2.0", ID: m.ID, Result: text("boom", true)})
	case "pic":
		writeJSON(w, rpcMsg{JSONRPC: "2.0", ID: m.ID, Result: mustJSON(map[string]any{
			"content":           []map[string]any{{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(pngBytes)}},
			"structuredContent": map[string]any{"width": 1}})})
	case "ask":
		f.ask(w, m, p.Meta.ProgressToken)
	}
}

// ask answers over SSE: progress, a log line, an elicitation request, then
// the result built from the client's answer.
func (f *fakeMCP) ask(w http.ResponseWriter, m rpcMsg, progress any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl := w.(http.Flusher)
	send := func(v rpcMsg) {
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", mustJSON(v))
		fl.Flush()
	}
	_, _ = io.WriteString(w, ": keep-alive\n\n")
	send(rpcMsg{JSONRPC: "2.0", Method: "notifications/progress", Params: mustJSON(map[string]any{"progressToken": progress, "progress": 1, "total": 4, "message": "Looking things up"})})
	send(rpcMsg{JSONRPC: "2.0", Method: "notifications/message", Params: mustJSON(map[string]any{"level": "info", "data": "Found 3 sheets"})})
	ch := make(chan rpcMsg, 1)
	f.mu.Lock()
	f.waiting[`"e1"`] = ch
	f.mu.Unlock()
	send(rpcMsg{JSONRPC: "2.0", ID: json.RawMessage(`"e1"`), Method: "elicitation/create", Params: json.RawMessage(`{"message":"Who and which colour?",
		"requestedSchema":{"type":"object","properties":{"name":{"type":"string","title":"Name"},
		"color":{"type":"string","enum":["r","b"],"enumNames":["Red","Blue"]},"n":{"type":"integer"}},"required":["name"]}}`)})
	var out string
	select {
	case resp := <-ch:
		var res struct {
			Action  string         `json:"action"`
			Content map[string]any `json:"content"`
		}
		_ = json.Unmarshal(resp.Result, &res)
		out = fmt.Sprintf("%s %v %v %v", res.Action, res.Content["name"], res.Content["color"], res.Content["n"])
	case <-time.After(5 * time.Second):
		out = "timeout"
	}
	send(rpcMsg{JSONRPC: "2.0", ID: m.ID, Result: mustJSON(map[string]any{"content": []map[string]any{{"type": "text", "text": out}}})})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
