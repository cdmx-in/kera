// Package tools is the contract between the chat engine (which runs the
// tool-calling loop) and everything that provides tools: built-in sets
// (files, code interpreter, web), OpenAPI tool servers and MCP servers.
// Providers register themselves; the engine looks them up by id.
package tools

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/cdmx-in/kera/internal/auth"
)

// Spec is one function the model may call (OpenAI function schema).
type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON schema object
}

// Event is a progress message a tool emits while it runs. Types match the
// chat stream: "status" (Data: map with description, done, hidden, action,
// query, urls...), "sources" (Data: []Source), "files" (Data: []string file ids).
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// AskRequest asks the user something and waits for the answer.
// Kind "confirm" answers true/false; "choice" and "input" answer JSON.
type AskRequest struct {
	Kind      string          `json:"kind"`
	Title     string          `json:"title"`
	Message   string          `json:"message"`
	Questions json.RawMessage `json:"questions,omitempty"`
	TimeoutMS int             `json:"timeout_ms,omitempty"`
}

// Call is one tool invocation with everything a provider may need.
type Call struct {
	ID        string          // tool call id from the model
	Name      string          // function name (unique across the run)
	Args      json.RawMessage // arguments as sent by the model
	User      *auth.User
	ModelID   string
	ChatID    string // empty for API calls without a chat; "temp:<run>" for temporary chats
	MessageID string // assistant message being produced
	RunID     string
	FileIDs   []string // files attached to the conversation branch
	// CallbackURL and CallbackToken let a remote tool server post events or
	// ask the user (POST {CallbackURL}/events, POST {CallbackURL}/ask with
	// Authorization: Bearer {CallbackToken}).
	CallbackURL   string
	CallbackToken string
	Emit          func(Event)                                                      // never nil
	Ask           func(ctx context.Context, r AskRequest) (json.RawMessage, error) // never nil
}

// Source is a citation a tool returns.
type Source struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	FileID  string `json:"file_id,omitempty"`
	Page    int    `json:"page,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// Result is what a tool returns. Content goes to the model; Parts are UI
// blocks (e.g. {"type":"cards","items":[...]}); Files are file ids to show
// as attachments of the answer; Sources become citations.
type Result struct {
	Content string            `json:"content"`
	Parts   []json.RawMessage `json:"parts,omitempty"`
	Files   []string          `json:"files,omitempty"`
	Sources []Source          `json:"sources,omitempty"`
	IsError bool              `json:"is_error,omitempty"`
}

// Provider offers a set of tools. ID is the tool server id from config
// ("astra-kb-tools", "google-sheets") or a built-in set id ("builtin:files",
// "builtin:code", "builtin:web", "builtin:memory", ...).
type Provider interface {
	ID() string
	// Tools lists the functions available to u right now (nil when the user
	// lacks access or, for per-user OAuth servers, has not connected).
	Tools(ctx context.Context, u *auth.User) ([]Spec, error)
	Execute(ctx context.Context, c Call) (Result, error)
}

var (
	mu        sync.RWMutex
	providers = map[string]Provider{}
	dynamic   []func(ctx context.Context) ([]Provider, error)
)

// Register adds a static provider (built-in sets).
func Register(p Provider) { mu.Lock(); providers[p.ID()] = p; mu.Unlock() }

// RegisterSource adds a function that returns providers whose set changes at
// runtime (tool servers loaded from the database).
func RegisterSource(f func(ctx context.Context) ([]Provider, error)) {
	mu.Lock()
	dynamic = append(dynamic, f)
	mu.Unlock()
}

// All returns every provider, static first, sorted by id within each group.
func All(ctx context.Context) ([]Provider, error) {
	mu.RLock()
	static := make([]Provider, 0, len(providers))
	for _, p := range providers {
		static = append(static, p)
	}
	srcs := append([]func(context.Context) ([]Provider, error){}, dynamic...)
	mu.RUnlock()
	sort.Slice(static, func(i, j int) bool { return static[i].ID() < static[j].ID() })
	out := static
	for _, f := range srcs {
		ps, err := f(ctx)
		if err != nil {
			return nil, err
		}
		sort.Slice(ps, func(i, j int) bool { return ps[i].ID() < ps[j].ID() })
		out = append(out, ps...)
	}
	return out, nil
}

// Get returns the provider with id, or nil.
func Get(ctx context.Context, id string) (Provider, error) {
	ps, err := All(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.ID() == id {
			return p, nil
		}
	}
	return nil, nil
}
