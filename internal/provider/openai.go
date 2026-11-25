package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"sort"
)

// openAI speaks OpenAI chat-completions (also LiteLLM, vLLM, llama.cpp ...).
type openAI struct{ conn }

var openAIParams = []string{"temperature", "top_p", "max_tokens", "seed", "stop", "reasoning_effort", "frequency_penalty", "presence_penalty"}

func (o *openAI) payload(req Request) map[string]any {
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		mm := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.Images) > 0 {
			parts := []map[string]any{}
			if m.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL(img)}})
			}
			mm["content"] = parts
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, map[string]any{"id": tc.ID, "type": "function",
					"function": map[string]any{"name": tc.Name, "arguments": argsString(tc.Arguments)}})
			}
			mm["tool_calls"] = calls
			if m.Content == "" {
				mm["content"] = nil
			}
		}
		if m.Role == "tool" {
			mm["tool_call_id"] = m.ToolCallID
		}
		msgs = append(msgs, mm)
	}
	body := map[string]any{"model": req.Model, "messages": msgs, "stream": true,
		"stream_options": map[string]any{"include_usage": true}}
	if len(req.Tools) > 0 {
		body["tools"] = functionTools(req.Tools)
	}
	pick(req.Params, body, openAIParams, nil)
	return body
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		Details          *struct {
			Cached *int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type callBuilder struct {
	id, name string
	args     []byte
}

// Stream implements Client. Tool-call fragments are assembled by index and
// emitted once the choice finishes (or the stream ends).
func (o *openAI) Stream(ctx context.Context, req Request) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		body, err := o.send(ctx, http.MethodPost, o.base+"/chat/completions", o.payload(req), req.Headers)
		if err != nil {
			yield(Event{}, err)
			return
		}
		defer body.Close()
		calls := map[int]*callBuilder{}
		finish, sawDone := "", false
		flush := func() bool {
			idx := make([]int, 0, len(calls))
			for i := range calls {
				idx = append(idx, i)
			}
			sort.Ints(idx)
			for _, i := range idx {
				c := calls[i]
				if c.id == "" {
					c.id = newCallID()
				}
				if !yield(Event{Kind: EventToolCall, ToolCall: ToolCall{ID: c.id, Name: c.name, Arguments: args(string(c.args))}}, nil) {
					return false
				}
			}
			clear(calls)
			return true
		}
		for data, err := range sseData(body) {
			if err != nil {
				yield(Event{}, fmt.Errorf("openai stream: %w", err))
				return
			}
			if string(data) == "[DONE]" {
				sawDone = true
				break
			}
			var c openAIChunk
			if err := json.Unmarshal(data, &c); err != nil {
				yield(Event{}, fmt.Errorf("openai stream: decode chunk: %w", err))
				return
			}
			if c.Error != nil {
				yield(Event{}, fmt.Errorf("openai: %s", c.Error.Message))
				return
			}
			for _, ch := range c.Choices {
				d := ch.Delta
				r := d.ReasoningContent // some servers send the same text in both fields
				if r == "" {
					r = d.Reasoning
				}
				if r != "" && !yield(Event{Kind: EventReasoning, Text: r}, nil) {
					return
				}
				if d.Content != "" && !yield(Event{Kind: EventText, Text: d.Content}, nil) {
					return
				}
				for _, tc := range d.ToolCalls {
					b := calls[tc.Index]
					if b == nil {
						b = &callBuilder{}
						calls[tc.Index] = b
					}
					if tc.ID != "" {
						b.id = tc.ID
					}
					b.name += tc.Function.Name
					b.args = append(b.args, tc.Function.Arguments...)
				}
				if ch.FinishReason != "" {
					finish = ch.FinishReason
					if !flush() {
						return
					}
				}
			}
			if c.Usage != nil {
				u := Usage{PromptTokens: c.Usage.PromptTokens, CompletionTokens: c.Usage.CompletionTokens}
				if c.Usage.Details != nil {
					u.CachedTokens = c.Usage.Details.Cached
				}
				if !yield(Event{Kind: EventUsage, Usage: u}, nil) {
					return
				}
			}
		}
		if !sawDone && finish == "" {
			yield(Event{}, fmt.Errorf("openai stream: %w", errTruncated))
			return
		}
		if !flush() {
			return
		}
		if finish == "" {
			finish = "stop"
		}
		yield(Event{Kind: EventDone, FinishReason: finish}, nil)
	}
}

// ListModels implements Client via GET /v1/models.
func (o *openAI) ListModels(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := o.getJSON(ctx, o.base+"/models", &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// ModelKinds implements KindLister via GET /v1/model/info, which LiteLLM
// serves ({data:[{model_name, model_info:{mode}}]}); other OpenAI-compatible
// servers answer 404 and the caller falls back to name heuristics.
func (o *openAI) ModelKinds(ctx context.Context, _ []string) (map[string]string, error) {
	var info struct {
		Data []struct {
			ModelName string `json:"model_name"`
			ModelInfo struct {
				Mode string `json:"mode"`
			} `json:"model_info"`
		} `json:"data"`
	}
	if err := o.getJSON(ctx, o.base+"/model/info", &info); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, d := range info.Data {
		switch d.ModelInfo.Mode {
		case ModelEmbedding, ModelRerank:
			out[d.ModelName] = d.ModelInfo.Mode
		}
	}
	return out, nil
}

// Embed implements Client via POST /v1/embeddings.
func (o *openAI) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := o.postJSON(ctx, o.base+"/embeddings", map[string]any{"model": model, "input": texts}, &out); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("openai embed: index %d out of range", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	for i, v := range vecs {
		if v == nil {
			return nil, fmt.Errorf("openai embed: no vector for input %d", i)
		}
	}
	return vecs, nil
}

var errTruncated = errors.New("stream ended before completion")
