package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/provider"
)

// Request is POST /api/chat/completions: an OpenAI body plus Kera / Open
// WebUI extensions.
type Request struct {
	Model        string          `json:"model"`
	Messages     []WireMessage   `json:"messages"`
	Stream       bool            `json:"stream"`
	StreamFormat string          `json:"stream_format"` // "events" = native dialect
	ChatID       string          `json:"chat_id"`
	ID           string          `json:"id"` // assistant message id
	ParentID     string          `json:"parent_id"`
	UserMessage  *UserMessage    `json:"user_message"`
	Files        []FileAttach    `json:"files"`
	Features     map[string]bool `json:"features"`
	ToolIDs      []string        `json:"tool_ids"`
	Tools        []WireTool      `json:"tools"` // passthrough: the caller runs these
	Temporary    bool            `json:"temporary"`

	Params map[string]any `json:"-"` // generation options from the body

	// In-process only (never decoded from HTTP). Unattended: nobody is watching
	// (a scheduled run), so questions are refused at once and a requested tool
	// server that cannot join fails the run. Preface is text for the model that
	// is not the user's own words (saved notes), sent as a separate user turn so
	// its links are no consent and the guardrails and hooks still see it.
	Unattended bool   `json:"-"`
	Preface    string `json:"-"`
	// ReadOnly is a test run: only tools their server declares read-only (and the built-in sets
	// that change nothing outside the run) are offered, so a trial cannot write to a connected app.
	ReadOnly bool `json:"-"`
}

// UserMessage is the new user turn the web app sends with a completion.
type UserMessage struct {
	ID       string          `json:"id"`
	ParentID *string         `json:"parentId"`
	Content  string          `json:"content"`
	Files    json.RawMessage `json:"files"`
}

// FileAttach references an uploaded file or a knowledge collection.
type FileAttach struct {
	Type string `json:"type"` // file | collection | image
	ID   string `json:"id"`
}

// WireMessage is an OpenAI chat message.
type WireMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []WireToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Files      []FileAttach    `json:"files,omitempty"`
}

// WireToolCall is an OpenAI tool call.
type WireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// WireTool is an OpenAI tool definition.
type WireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// paramKeys are the body keys copied into provider params (request values
// win over the preset's).
var paramKeys = []string{"temperature", "top_p", "max_tokens", "seed", "stop", "reasoning_effort",
	"frequency_penalty", "presence_penalty", "num_ctx", "think", "keep_alive", "top_k", "min_p", "repeat_penalty"}

func decodeRequest(r *http.Request) (*Request, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 64<<20))
	if err != nil {
		return nil, api.Errf(http.StatusRequestEntityTooLarge, "too_large", "request body too large")
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, api.Errf(http.StatusBadRequest, "bad_json", "invalid JSON body: "+err.Error())
	}
	var all map[string]any
	_ = json.Unmarshal(body, &all)
	req.Params = map[string]any{}
	if p, ok := all["params"].(map[string]any); ok {
		for _, k := range paramKeys {
			if v, ok := p[k]; ok && v != nil {
				req.Params[k] = v
			}
		}
	}
	for _, k := range paramKeys {
		if v, ok := all[k]; ok && v != nil {
			req.Params[k] = v
		}
	}
	if v, ok := all["max_completion_tokens"]; ok && v != nil {
		req.Params["max_tokens"] = v
	}
	if req.Model == "" {
		return nil, api.Errf(http.StatusBadRequest, "model_required", "model is required")
	}
	return &req, nil
}

// contentOf splits OpenAI content (a string or a list of text / image_url
// parts) into text and images.
func contentOf(raw json.RawMessage) (string, []provider.Image) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw), nil
	}
	var texts []string
	var imgs []provider.Image
	for _, p := range parts {
		switch {
		case p.Text != "":
			texts = append(texts, p.Text)
		case p.ImageURL != nil && p.ImageURL.URL != "":
			imgs = append(imgs, provider.Image{MIME: dataMIME(p.ImageURL.URL), Data: p.ImageURL.URL})
		}
	}
	return strings.Join(texts, "\n"), imgs
}

func dataMIME(u string) string {
	if rest, ok := strings.CutPrefix(u, "data:"); ok {
		if i := strings.IndexAny(rest, ";,"); i > 0 {
			return rest[:i]
		}
	}
	return ""
}

// providerMessages converts the caller's messages.
func providerMessages(in []WireMessage) []provider.Message {
	out := make([]provider.Message, 0, len(in))
	for _, m := range in {
		text, imgs := contentOf(m.Content)
		pm := provider.Message{Role: m.Role, Content: text, Images: imgs, ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			args := json.RawMessage(tc.Function.Arguments)
			if !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			pm.ToolCalls = append(pm.ToolCalls, provider.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: args})
		}
		out = append(out, pm)
	}
	return out
}

// clientTools converts passthrough tool definitions.
func clientTools(in []WireTool) []provider.Tool {
	var out []provider.Tool
	for _, t := range in {
		if t.Function.Name == "" {
			continue
		}
		params := t.Function.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, provider.Tool{Name: t.Function.Name, Description: t.Function.Description, Parameters: params})
	}
	return out
}
