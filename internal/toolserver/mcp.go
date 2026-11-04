package toolserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/tools"
	"github.com/cdmx-in/kera/internal/version"
)

// MCP over Streamable HTTP, initialize-based revisions (2025-03-26 ..
// 2025-11-25): initialize + notifications/initialized, Mcp-Session-Id,
// responses as JSON or as an SSE stream that may carry notifications and
// server requests (elicitation) before the response.
//
// ponytail: the stateless 2026-07-28 revision (per-request _meta, no
// initialize, elicitation as InputRequiredResult) is not spoken; such a server
// fails at initialize with its supported versions in the error. Add it when a
// tool server we run drops the initialize-based revisions.
const mcpVersion = "2025-11-25"

var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// maxMessage caps one JSON-RPC message read from a server.
const maxMessage = 64 << 20

// mcpPool keeps one session per key (the user id for per-user OAuth servers,
// "" when every user shares the server's credential).
type mcpPool struct {
	http *http.Client
	url  string

	mu       sync.Mutex
	sessions map[string]*mcpSession
}

func newMCPPool(c *http.Client, url string) *mcpPool {
	return &mcpPool{http: c, url: url, sessions: map[string]*mcpSession{}}
}

func (p *mcpPool) session(key string) *mcpSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[key]
	if s == nil {
		s = &mcpSession{http: p.http, url: p.url}
		p.sessions[key] = s
	}
	return s
}

func (p *mcpPool) drop(key string) {
	p.mu.Lock()
	delete(p.sessions, key)
	p.mu.Unlock()
}

type mcpSession struct {
	http *http.Client
	url  string
	seq  atomic.Int64

	mu      sync.Mutex // serialises initialize
	ready   bool
	id      string // Mcp-Session-Id, "" for stateless servers
	version string
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 && len(e.Data) < 500 {
		return fmt.Sprintf("%s (%d, %s)", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

// handlers receive what a server sends on a response stream before the response.
type handlers struct {
	notify  func(method string, params json.RawMessage)
	request func(ctx context.Context, method string, params json.RawMessage) (any, *rpcError)
}

var (
	errSessionExpired = errors.New("mcp session expired")
	errRejected       = errors.New("the server rejected the credentials")
)

// request sends one JSON-RPC request, initializing the session first and
// once more when the server answers 404 for an expired session.
func (s *mcpSession) request(ctx context.Context, hdr func(http.Header), method string, params any, h handlers) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		sid, ver, err := s.ensure(ctx, hdr)
		if err != nil {
			return nil, err
		}
		res, err := s.do(ctx, hdr, sid, ver, method, params, h)
		if errors.Is(err, errSessionExpired) && attempt == 0 {
			s.reset(sid)
			continue
		}
		return res, err
	}
}

func (s *mcpSession) reset(sid string) {
	s.mu.Lock()
	if s.id == sid {
		s.ready, s.id = false, ""
	}
	s.mu.Unlock()
}

func (s *mcpSession) ensure(ctx context.Context, hdr func(http.Header)) (sid, ver string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return s.id, s.version, nil
	}
	params := map[string]any{
		"protocolVersion": mcpVersion,
		"capabilities":    map[string]any{"elicitation": map[string]any{}},
		"clientInfo":      map[string]any{"name": "kera", "version": version.Version},
	}
	var got struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	var newID string
	raw, err := s.post(ctx, hdr, "", "", rpcMsg{JSONRPC: "2.0", ID: s.nextID(), Method: "initialize", Params: mustJSON(params)},
		handlers{}, func(r *http.Response) { newID = r.Header.Get("Mcp-Session-Id") })
	if err != nil {
		return "", "", fmt.Errorf("initialize: %w", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return "", "", fmt.Errorf("initialize: bad result: %w", err)
	}
	if !slices.Contains(mcpVersions, got.ProtocolVersion) {
		return "", "", fmt.Errorf("initialize: server speaks MCP %q, Kera speaks %s", got.ProtocolVersion, strings.Join(mcpVersions, ", "))
	}
	note := rpcMsg{JSONRPC: "2.0", Method: "notifications/initialized"}
	if _, err := s.post(ctx, hdr, newID, got.ProtocolVersion, note, handlers{}, nil); err != nil {
		return "", "", fmt.Errorf("initialized notification: %w", err)
	}
	s.ready, s.id, s.version = true, newID, got.ProtocolVersion
	return s.id, s.version, nil
}

func (s *mcpSession) nextID() json.RawMessage { return mustJSON(s.seq.Add(1)) }

func (s *mcpSession) do(ctx context.Context, hdr func(http.Header), sid, ver, method string, params any, h handlers) (json.RawMessage, error) {
	return s.post(ctx, hdr, sid, ver, rpcMsg{JSONRPC: "2.0", ID: s.nextID(), Method: method, Params: mustJSON(params)}, h, nil)
}

// post sends msg and, for a request, returns its result. Notifications and
// responses expect 202 (200 tolerated) and return nil.
func (s *mcpSession) post(ctx context.Context, hdr func(http.Header), sid, ver string, msg rpcMsg, h handlers, seen func(*http.Response)) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(mustJSON(msg)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if ver != "" {
		req.Header.Set("MCP-Protocol-Version", ver)
	}
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	if hdr != nil {
		hdr(req.Header)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if seen != nil {
		seen(resp)
	}
	if resp.StatusCode == http.StatusNotFound && sid != "" {
		return nil, errSessionExpired
	}
	if resp.StatusCode >= 300 {
		return nil, httpError(resp)
	}
	if msg.ID == nil || msg.Method == "" { // notification or response
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, nil
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	reply := func(out rpcMsg) error {
		_, err := s.post(ctx, hdr, sid, ver, out, handlers{}, nil)
		return err
	}
	switch ct {
	case "text/event-stream":
		return readStream(ctx, resp.Body, msg.ID, h, reply)
	case "application/json":
		var m rpcMsg
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxMessage)).Decode(&m); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return result(m)
	}
	return nil, fmt.Errorf("unexpected response type %q (HTTP %d)", resp.Header.Get("Content-Type"), resp.StatusCode)
}

func result(m rpcMsg) (json.RawMessage, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	return m.Result, nil
}

func httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var m rpcMsg
	if json.Unmarshal(body, &m) == nil && m.Error != nil {
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, m.Error)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, errRejected)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// readStream reads SSE events until the response to id arrives, passing
// notifications and server requests to h (requests are answered via reply).
func readStream(ctx context.Context, body io.Reader, id json.RawMessage, h handlers, reply func(rpcMsg) error) (json.RawMessage, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), maxMessage)
	var data []byte
	dispatch := func() (json.RawMessage, bool, error) {
		defer func() { data = data[:0] }()
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, false, nil
		}
		var m rpcMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, false, fmt.Errorf("bad event: %w", err)
		}
		switch {
		case m.Method != "" && m.ID != nil: // server -> client request
			out := rpcMsg{JSONRPC: "2.0", ID: m.ID}
			if h.request == nil {
				out.Error = &rpcError{Code: -32601, Message: "not supported here"}
			} else if res, rerr := h.request(ctx, m.Method, m.Params); rerr != nil {
				out.Error = rerr
			} else {
				out.Result = mustJSON(res)
			}
			return nil, false, reply(out)
		case m.Method != "": // notification
			if h.notify != nil {
				h.notify(m.Method, m.Params)
			}
			return nil, false, nil
		case bytes.Equal(m.ID, id):
			res, err := result(m)
			return res, true, err
		}
		return nil, false, nil // a response to something else: ignore
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if res, done, err := dispatch(); done || err != nil {
				return res, err
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, strings.TrimPrefix(v, " ")...)
		}
		// event:, id:, retry: and comments carry nothing we need
	}
	if res, done, err := dispatch(); done || err != nil {
		return res, err
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	// ponytail: no resumption (GET + Last-Event-ID) of a dropped stream; the call fails and the model may retry.
	return nil, errors.New("stream ended without a response")
}

func mustJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ---- server side of the provider -------------------------------------------------

func (s *Server) sessionKey(u *auth.User) string {
	if s.row.Auth.Type == "user_oauth" && u != nil {
		return u.ID
	}
	return ""
}

func (s *Server) headers(c tools.Call, tok string) func(http.Header) {
	return func(h http.Header) {
		s.authorize(h, tok)
		contextHeaders(h, c)
	}
}

type mcpTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func (s *Server) listMCP(ctx context.Context, u *auth.User, tok string) (cachedSpecs, error) {
	sess := s.mcp.session(s.sessionKey(u))
	var out []tools.Spec
	cursor := ""
	for page := 0; page < 50; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := sess.request(ctx, s.headers(tools.Call{User: u}, tok), "tools/list", params, handlers{})
		if err != nil {
			return cachedSpecs{}, fmt.Errorf("tools/list: %w", err)
		}
		var res struct {
			Tools      []mcpTool `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return cachedSpecs{}, fmt.Errorf("tools/list: %w", err)
		}
		for _, t := range res.Tools {
			out = append(out, toSpec(t))
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return cachedSpecs{specs: out}, nil
}

func toSpec(t mcpTool) tools.Spec {
	desc := t.Description
	if desc == "" {
		desc = t.Title
	}
	params := t.InputSchema
	if len(bytes.TrimSpace(params)) == 0 || string(params) == "null" {
		params = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return tools.Spec{Name: t.Name, Description: desc, Parameters: params}
}

func (s *Server) callMCP(ctx context.Context, c tools.Call, tok string) (tools.Result, error) {
	args := c.Args
	if len(bytes.TrimSpace(args)) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	params := map[string]any{"name": c.Name, "arguments": args}
	if c.ID != "" {
		params["_meta"] = map[string]any{"progressToken": c.ID}
	}
	h := handlers{
		notify: func(method string, p json.RawMessage) { s.notify(c, method, p) },
		request: func(ctx context.Context, method string, p json.RawMessage) (any, *rpcError) {
			return s.serverRequest(ctx, c, method, p)
		},
	}
	raw, err := s.mcp.session(s.sessionKey(c.User)).request(ctx, s.headers(c, tok), "tools/call", params, h)
	if err != nil {
		return tools.Result{}, err
	}
	return s.toResult(ctx, c, raw)
}

// notify turns progress and log notifications into status events.
func (s *Server) notify(c tools.Call, method string, p json.RawMessage) {
	var desc string
	switch method {
	case "notifications/progress":
		var n struct {
			Progress float64  `json:"progress"`
			Total    *float64 `json:"total"`
			Message  string   `json:"message"`
		}
		_ = json.Unmarshal(p, &n)
		desc = n.Message
		if desc == "" && n.Total != nil && *n.Total > 0 {
			desc = fmt.Sprintf("%.0f%%", 100*n.Progress / *n.Total)
		}
	case "notifications/message":
		var n struct {
			Level string          `json:"level"`
			Data  json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(p, &n)
		if n.Level == "debug" {
			return
		}
		var str string
		if json.Unmarshal(n.Data, &str) != nil {
			str = string(n.Data)
		}
		desc = str
	}
	if desc = strings.TrimSpace(desc); desc == "" {
		return
	}
	c.Emit(tools.Event{Type: "status", Data: map[string]any{"action": "tool_call", "description": Truncate(desc, 300),
		"done": false, "tool": c.Name, "server": s.row.ID, "callId": c.ID}})
}

func (s *Server) serverRequest(ctx context.Context, c tools.Call, method string, p json.RawMessage) (any, *rpcError) {
	switch method {
	case "ping":
		return map[string]any{}, nil
	case "elicitation/create":
		return s.elicit(ctx, c, p), nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not supported: " + method}
}

type callResult struct {
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
		URI      string `json:"uri"`
		Name     string `json:"name"`
		Resource *struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
			Blob     string `json:"blob"`
		} `json:"resource"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// toResult joins text parts for the model, saves images and binary
// resources as files, and falls back to structuredContent when there is no text.
func (s *Server) toResult(ctx context.Context, c tools.Call, raw json.RawMessage) (tools.Result, error) {
	var r callResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return tools.Result{}, fmt.Errorf("bad tools/call result: %w", err)
	}
	var texts []string
	out := tools.Result{IsError: r.IsError}
	for i, part := range r.Content {
		switch part.Type {
		case "text":
			texts = append(texts, part.Text)
		case "image", "audio":
			if id, err := s.saveFile(ctx, c, fmt.Sprintf("%s-%d", c.Name, i+1), part.MimeType, part.Data); err == nil {
				out.Files = append(out.Files, id)
				texts = append(texts, fmt.Sprintf("[%s attached to the answer as a file]", part.Type))
			} else {
				texts = append(texts, fmt.Sprintf("[%s could not be saved: %v]", part.Type, err))
			}
		case "resource_link":
			texts = append(texts, fmt.Sprintf("Resource: %s (%s)", part.Name, part.URI))
		case "resource":
			if part.Resource == nil {
				continue
			}
			if part.Resource.Text != "" {
				texts = append(texts, part.Resource.Text)
			} else if part.Resource.Blob != "" {
				if id, err := s.saveFile(ctx, c, baseName(part.Resource.URI, c.Name), part.Resource.MimeType, part.Resource.Blob); err == nil {
					out.Files = append(out.Files, id)
					texts = append(texts, "[file attached to the answer: "+part.Resource.URI+"]")
				}
			}
		}
	}
	out.Content = strings.Join(texts, "\n")
	if strings.TrimSpace(out.Content) == "" && len(r.StructuredContent) > 0 && string(r.StructuredContent) != "null" {
		out.Content = string(r.StructuredContent)
	}
	return out, nil
}

func baseName(uri, def string) string {
	if i := strings.LastIndexAny(uri, "/:"); i >= 0 && i < len(uri)-1 {
		return uri[i+1:]
	}
	return def
}

func (s *Server) saveFile(ctx context.Context, c tools.Call, name, mimeType, b64 string) (string, error) {
	if s.m.files == nil || c.User == nil {
		return "", errors.New("file storage unavailable")
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("bad base64: %w", err)
	}
	if !strings.Contains(name, ".") {
		if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
			name += exts[0]
		}
	}
	f, err := s.m.files.Create(ctx, c.User.ID, name, mimeType, bytes.NewReader(data),
		map[string]any{"source": "tool", "tool_server": s.row.ID, "tool": c.Name, "chat_id": c.ChatID})
	if err != nil {
		return "", err
	}
	return f.ID, nil
}
