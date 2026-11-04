package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strings"
)

// ollama speaks Ollama's native API (/api/chat, /api/tags, /api/embed).
type ollama struct{ conn }

var (
	ollamaOptions = []string{"num_ctx", "temperature", "top_p", "top_k", "min_p", "seed", "stop", "repeat_penalty", "num_predict", "max_tokens"}
	ollamaRename  = map[string]string{"max_tokens": "num_predict"}
)

func (o *ollama) payload(req Request) map[string]any {
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		mm := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.Images) > 0 {
			imgs := make([]string, 0, len(m.Images))
			for _, img := range m.Images {
				// ponytail: Ollama only takes base64; http(s) image URLs are passed as-is and
				// rejected by Ollama. The engine sends stored files as base64.
				d := img.Data
				if i := strings.Index(d, ";base64,"); strings.HasPrefix(d, "data:") && i > 0 {
					d = d[i+len(";base64,"):]
				}
				imgs = append(imgs, d)
			}
			mm["images"] = imgs
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, map[string]any{"id": tc.ID, "function": map[string]any{
					"name": tc.Name, "arguments": json.RawMessage(argsString(tc.Arguments))}})
			}
			mm["tool_calls"] = calls
		}
		if m.Role == "tool" && m.Name != "" {
			mm["tool_name"] = m.Name
		}
		msgs = append(msgs, mm)
	}
	body := map[string]any{"model": req.Model, "messages": msgs, "stream": true}
	if len(req.Tools) > 0 {
		body["tools"] = functionTools(req.Tools)
	}
	opts := map[string]any{}
	pick(req.Params, opts, ollamaOptions, ollamaRename)
	if len(opts) > 0 {
		body["options"] = opts
	}
	pick(req.Params, body, []string{"keep_alive", "think"}, nil)
	if _, ok := body["think"]; !ok {
		// gpt-oss style models take a level instead of a boolean.
		pick(req.Params, body, []string{"reasoning_effort"}, map[string]string{"reasoning_effort": "think"})
	}
	return body
}

// functionTools renders tools in the {"type":"function","function":{...}} shape
// shared by Ollama and OpenAI chat-completions.
func functionTools(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": schema(t.Parameters)}})
	}
	return out
}

func schema(p json.RawMessage) json.RawMessage {
	if len(p) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return p
}

type ollamaChunk struct {
	Message struct {
		Content   string `json:"content"`
		Thinking  string `json:"thinking"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Error           string `json:"error"`
}

// Stream implements Client. Ollama sends each tool call whole, so calls are
// emitted as they arrive (with a generated id when Ollama gives none).
func (o *ollama) Stream(ctx context.Context, req Request) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		body, err := o.send(ctx, http.MethodPost, o.base+"/api/chat", o.payload(req), req.Headers)
		if err != nil {
			yield(Event{}, err)
			return
		}
		defer body.Close()
		calls := 0
		for line, err := range lines(body) {
			if err != nil {
				yield(Event{}, fmt.Errorf("ollama stream: %w", err))
				return
			}
			var c ollamaChunk
			if err := json.Unmarshal(line, &c); err != nil {
				yield(Event{}, fmt.Errorf("ollama stream: decode chunk: %w", err))
				return
			}
			if c.Error != "" {
				yield(Event{}, fmt.Errorf("ollama: %s", c.Error))
				return
			}
			if c.Message.Thinking != "" && !yield(Event{Kind: EventReasoning, Text: c.Message.Thinking}, nil) {
				return
			}
			if c.Message.Content != "" && !yield(Event{Kind: EventText, Text: c.Message.Content}, nil) {
				return
			}
			for _, tc := range c.Message.ToolCalls {
				id := tc.ID
				if id == "" {
					id = newCallID()
				}
				calls++
				a := tc.Function.Arguments
				if len(a) == 0 || string(a) == "null" {
					a = json.RawMessage("{}")
				}
				if !yield(Event{Kind: EventToolCall, ToolCall: ToolCall{ID: id, Name: tc.Function.Name, Arguments: a}}, nil) {
					return
				}
			}
			if c.Done {
				if !yield(Event{Kind: EventUsage, Usage: Usage{PromptTokens: c.PromptEvalCount, CompletionTokens: c.EvalCount}}, nil) {
					return
				}
				reason := c.DoneReason
				if calls > 0 {
					reason = "tool_calls"
				}
				yield(Event{Kind: EventDone, FinishReason: reason}, nil)
				return
			}
		}
		yield(Event{}, errors.New("ollama stream: ended before done"))
	}
}

// ListModels implements Client via GET /api/tags.
func (o *ollama) ListModels(ctx context.Context) ([]string, error) {
	var out struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := o.getJSON(ctx, o.base+"/api/tags", &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		id := m.Model
		if id == "" {
			id = m.Name
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ModelKinds implements KindLister via POST /api/show: a model whose
// capabilities lack "completion" cannot chat (embedding models report only
// "embedding"). Models /api/show cannot describe are left out.
func (o *ollama) ModelKinds(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	var firstErr error
	for _, id := range ids {
		var show struct {
			Capabilities []string `json:"capabilities"`
		}
		if err := o.postJSON(ctx, o.base+"/api/show", map[string]any{"model": id}, &show); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(show.Capabilities) > 0 && !slices.Contains(show.Capabilities, "completion") {
			out[id] = ModelEmbedding
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// Embed implements Client via POST /api/embed.
func (o *ollama) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := o.postJSON(ctx, o.base+"/api/embed", map[string]any{"model": model, "input": texts}, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: got %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}
