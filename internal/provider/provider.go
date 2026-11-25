// Package provider talks to model servers. Three wire protocols (Ollama
// native chat, OpenAI chat-completions, OpenAI Responses) are normalised to
// one streamed Event type so the chat engine never sees protocol details.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/ids"
)

// Connection kinds (match the connection.kind CHECK constraint).
const (
	KindOllama    = "ollama"
	KindOpenAI    = "openai"
	KindResponses = "responses"
)

// DefaultIdleTimeout aborts a request when the server sends nothing for this long.
const DefaultIdleTimeout = 300 * time.Second

// ErrIdleTimeout is returned when no bytes arrive within the idle timeout.
var ErrIdleTimeout = errors.New("provider: no data received within the idle timeout")

// Image is an image attached to a message. Data is base64 (no data: prefix),
// a data: URL or an http(s) URL.
type Image struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// ToolCall is one fully assembled function call.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Message is one turn of the conversation. Role is system, user, assistant or
// tool; a tool message carries ToolCallID and the function Name.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Images     []Image    `json:"images,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	// Data marks a user-role message that carries reference material (retrieved
	// passages), not something the user typed. The guard treats it like tool
	// output. It is never sent to a provider.
	Data bool `json:"-"`
}

// Tool is a function the model may call. Parameters is a JSON schema.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request is one chat call. Params holds generation options (temperature,
// top_p, max_tokens, seed, stop, num_ctx, think, reasoning_effort,
// keep_alive); each protocol takes the ones it understands and ignores the
// rest. ProviderTools are passed verbatim (Responses API only, e.g.
// {"type":"web_search"}). Headers are added to the HTTP request.
type Request struct {
	Model         string
	Messages      []Message
	Tools         []Tool
	Params        map[string]any
	ProviderTools []json.RawMessage
	Headers       http.Header
}

// Kind tells which field of an Event is set.
type Kind string

// Event kinds.
const (
	EventText      Kind = "text"      // Text
	EventReasoning Kind = "reasoning" // Text
	EventToolCall  Kind = "tool_call" // ToolCall
	EventCitation  Kind = "citation"  // Citation
	EventUsage     Kind = "usage"     // Usage
	EventDone      Kind = "done"      // FinishReason
)

// Citation is a web source the model cited.
type Citation struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Usage is the token count of one call. CachedTokens is the part of the
// prompt the server served from its cache; nil when the server does not say.
type Usage struct {
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	CachedTokens     *int `json:"cached_tokens,omitempty"`
}

// Event is one item of a normalised stream.
type Event struct {
	Kind         Kind
	Text         string
	ToolCall     ToolCall
	Citation     Citation
	Usage        Usage
	FinishReason string
}

// Client is one model server.
type Client interface {
	// Stream runs a chat call. The sequence ends after an EventDone or an error.
	Stream(ctx context.Context, req Request) iter.Seq2[Event, error]
	// ListModels returns the server's model ids.
	ListModels(ctx context.Context) ([]string, error)
	// Embed returns one vector per text.
	Embed(ctx context.Context, model string, texts []string) ([][]float32, error)
}

// Model kinds a KindLister reports; a model it does not name is a chat model
// (or the server could not tell).
const (
	ModelEmbedding = "embedding"
	ModelRerank    = "rerank"
)

// KindLister is implemented by clients whose server can tell chat models from
// embedding and rerank models (Ollama's /api/show capabilities, LiteLLM's
// /model/info mode). Callers type-assert it; errors mean "unknown".
type KindLister interface {
	ModelKinds(ctx context.Context, ids []string) (map[string]string, error)
}

// Config describes a connection.
type Config struct {
	Kind        string
	BaseURL     string
	APIKey      string
	Headers     http.Header   // sent on every request
	HTTP        *http.Client  // default http.DefaultClient (no overall timeout)
	IdleTimeout time.Duration // default DefaultIdleTimeout
}

// New returns the client for cfg.Kind.
func New(cfg Config) (Client, error) {
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		return nil, errors.New("provider: base URL is empty")
	}
	switch cfg.Kind {
	case KindOllama:
		return &ollama{conn{cfg: cfg, base: base}}, nil
	case KindOpenAI:
		return &openAI{conn{cfg: cfg, base: v1(base)}}, nil
	case KindResponses:
		return &responses{openAI{conn{cfg: cfg, base: v1(base)}}}, nil
	}
	return nil, fmt.Errorf("provider: unknown kind %q", cfg.Kind)
}

// v1 makes an OpenAI-style base end in exactly one /v1.
func v1(base string) string {
	if strings.HasSuffix(base, "/v1") {
		return base
	}
	return base + "/v1"
}

// HTTPError is a non-2xx answer from the model server.
type HTTPError struct {
	Method, URL string
	Status      int
	Body        string // first 512 bytes
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("provider: %s %s: %d %s: %s", e.Method, e.URL, e.Status, http.StatusText(e.Status), e.Body)
}

type conn struct {
	cfg  Config
	base string
}

// send performs a JSON request and returns the body of a 2xx response. The
// request is cancelled when no bytes arrive for the idle timeout, from the
// moment it is sent until the body is closed.
func (c *conn) send(ctx context.Context, method, url string, payload any, extra http.Header) (io.ReadCloser, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("provider: encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(c.cfg.IdleTimeout, func() { cancel(ErrIdleTimeout) })
	fail := func(err error) (io.ReadCloser, error) {
		timer.Stop()
		cancel(nil)
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fail(fmt.Errorf("provider: %w", err))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for _, h := range []http.Header{c.cfg.Headers, extra} {
		for k, vs := range h {
			req.Header.Del(k)
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
	}
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		if errors.Is(context.Cause(ctx), ErrIdleTimeout) {
			err = ErrIdleTimeout
		}
		return fail(fmt.Errorf("provider: %s %s: %w", method, url, err))
	}
	if resp.StatusCode/100 != 2 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return fail(&HTTPError{Method: method, URL: url, Status: resp.StatusCode, Body: strings.TrimSpace(string(excerpt))})
	}
	return &idleBody{rc: resp.Body, timer: timer, idle: c.cfg.IdleTimeout, ctx: ctx, cancel: cancel}, nil
}

// getJSON / postJSON decode a whole JSON response into dst.
func (c *conn) postJSON(ctx context.Context, url string, payload, dst any) error {
	body, err := c.send(ctx, http.MethodPost, url, payload, nil)
	if err != nil {
		return err
	}
	defer body.Close()
	if err := json.NewDecoder(io.LimitReader(body, maxBody)).Decode(dst); err != nil {
		return fmt.Errorf("provider: decode %s: %w", url, err)
	}
	return nil
}

func (c *conn) getJSON(ctx context.Context, url string, dst any) error {
	body, err := c.send(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return err
	}
	defer body.Close()
	if err := json.NewDecoder(io.LimitReader(body, maxBody)).Decode(dst); err != nil {
		return fmt.Errorf("provider: decode %s: %w", url, err)
	}
	return nil
}

type idleBody struct {
	rc     io.ReadCloser
	timer  *time.Timer
	idle   time.Duration
	ctx    context.Context
	cancel context.CancelCauseFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	if err != nil && err != io.EOF {
		if errors.Is(context.Cause(b.ctx), ErrIdleTimeout) {
			err = ErrIdleTimeout
		}
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	b.cancel(nil)
	return b.rc.Close()
}

// lines yields non-blank lines, trimmed (NDJSON).
func lines(r io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		for line, err := range rawLines(r) {
			if line = bytes.TrimSpace(line); (err != nil || len(line) > 0) && !yield(line, err) {
				return
			}
		}
	}
}

// sseData yields the data payload of each server-sent event (multi-line
// data joined with \n). Event names, ids and comments are ignored: every
// protocol here also carries the type inside the JSON payload.
func sseData(r io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		var data []byte
		for line, err := range rawLines(r) {
			if err != nil {
				yield(nil, err)
				return
			}
			if len(line) == 0 {
				if data != nil && !yield(data, nil) {
					return
				}
				data = nil
				continue
			}
			v, ok := bytes.CutPrefix(line, []byte("data:"))
			if !ok {
				continue
			}
			v = bytes.TrimPrefix(v, []byte(" "))
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, v...)
		}
		if data != nil {
			yield(data, nil)
		}
	}
}

// Limits on what a model server may send: one stream line, one JSON body.
const (
	maxLine = 16 << 20
	maxBody = 256 << 20
)

// rawLines yields every line (blank ones included) without \r\n. A line
// longer than maxLine is an error.
func rawLines(r io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(nil, maxLine)
		for sc.Scan() {
			if !yield(bytes.Clone(sc.Bytes()), nil) {
				return
			}
		}
		if err := sc.Err(); err != nil {
			yield(nil, err)
		}
	}
}

// pick copies the listed params that are set; rename maps a param name to
// the protocol's name for it.
func pick(params map[string]any, dst map[string]any, keys []string, rename map[string]string) {
	for _, k := range keys {
		v, ok := params[k]
		if !ok || v == nil {
			continue
		}
		if n, ok := rename[k]; ok {
			k = n
		}
		dst[k] = v
	}
}

// imageURL renders an image as a URL (data: URL for base64 payloads).
func imageURL(img Image) string {
	if strings.HasPrefix(img.Data, "http://") || strings.HasPrefix(img.Data, "https://") || strings.HasPrefix(img.Data, "data:") {
		return img.Data
	}
	mime := img.MIME
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + img.Data
}

// argsString renders tool-call arguments as the JSON text OpenAI expects.
func argsString(a json.RawMessage) string {
	if len(bytes.TrimSpace(a)) == 0 {
		return "{}"
	}
	return string(a)
}

// args normalises streamed argument text: empty becomes {}.
func args(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

func newCallID() string { return "call_" + strings.ReplaceAll(ids.New(), "-", "") }
