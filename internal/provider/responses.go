package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"
)

// responses speaks the OpenAI Responses API. Model listing and embeddings
// use the same /v1 endpoints as chat-completions.
type responses struct{ openAI }

func (o *responses) payload(req Request) map[string]any {
	var system []string
	input := []map[string]any{}
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			system = append(system, m.Content)
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
		case "assistant":
			if m.Content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": m.Content})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name,
					"arguments": argsString(tc.Arguments)})
			}
		default:
			parts := []map[string]any{}
			if m.Content != "" || len(m.Images) == 0 {
				parts = append(parts, map[string]any{"type": "input_text", "text": m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, map[string]any{"type": "input_image", "image_url": imageURL(img)})
			}
			input = append(input, map[string]any{"role": m.Role, "content": parts})
		}
	}
	body := map[string]any{"model": req.Model, "input": input, "stream": true}
	if len(system) > 0 {
		body["instructions"] = strings.Join(system, "\n\n")
	}
	tools := make([]any, 0, len(req.Tools)+len(req.ProviderTools))
	for _, t := range req.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description,
			"parameters": schema(t.Parameters)})
	}
	for _, t := range req.ProviderTools {
		tools = append(tools, t)
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	pick(req.Params, body, []string{"temperature", "top_p", "max_tokens"}, map[string]string{"max_tokens": "max_output_tokens"})
	if e, ok := req.Params["reasoning_effort"]; ok && e != nil {
		body["reasoning"] = map[string]any{"effort": e, "summary": "auto"}
	}
	return body
}

type respAnnotation struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

type respItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Annotations []respAnnotation `json:"annotations"`
	} `json:"content"`
}

type respEvent struct {
	Type       string          `json:"type"`
	Delta      string          `json:"delta"`
	ItemID     string          `json:"item_id"`
	Arguments  string          `json:"arguments"`
	Item       *respItem       `json:"item"`
	Annotation *respAnnotation `json:"annotation"`
	Message    string          `json:"message"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
	Response *struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

type respCall struct {
	callID, name string
	args         strings.Builder
	done         bool
}

// Stream implements Client.
func (o *responses) Stream(ctx context.Context, req Request) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		body, err := o.send(ctx, http.MethodPost, o.base+"/responses", o.payload(req), req.Headers)
		if err != nil {
			yield(Event{}, err)
			return
		}
		defer body.Close()
		calls := map[string]*respCall{}
		var order []string
		emitted := 0
		cited := map[string]bool{}
		emitCall := func(c *respCall) bool {
			if c.done {
				return true
			}
			c.done = true
			emitted++
			return yield(Event{Kind: EventToolCall, ToolCall: ToolCall{ID: c.callID, Name: c.name, Arguments: args(c.args.String())}}, nil)
		}
		cite := func(a respAnnotation) bool {
			if a.Type != "url_citation" || a.URL == "" || cited[a.URL] {
				return true
			}
			cited[a.URL] = true
			return yield(Event{Kind: EventCitation, Citation: Citation{URL: a.URL, Title: a.Title}}, nil)
		}
		for data, err := range sseData(body) {
			if err != nil {
				yield(Event{}, fmt.Errorf("responses stream: %w", err))
				return
			}
			if string(data) == "[DONE]" {
				continue
			}
			var e respEvent
			if err := json.Unmarshal(data, &e); err != nil {
				yield(Event{}, fmt.Errorf("responses stream: decode event: %w", err))
				return
			}
			ok := true
			switch e.Type {
			case "response.output_text.delta":
				ok = yield(Event{Kind: EventText, Text: e.Delta}, nil)
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				ok = yield(Event{Kind: EventReasoning, Text: e.Delta}, nil)
			case "response.output_text.annotation.added":
				if e.Annotation != nil {
					ok = cite(*e.Annotation)
				}
			case "response.output_item.added":
				if e.Item != nil && e.Item.Type == "function_call" {
					c := &respCall{callID: e.Item.CallID, name: e.Item.Name}
					c.args.WriteString(e.Item.Arguments)
					calls[e.Item.ID] = c
					order = append(order, e.Item.ID)
				}
			case "response.function_call_arguments.delta":
				if c := calls[e.ItemID]; c != nil {
					c.args.WriteString(e.Delta)
				}
			case "response.function_call_arguments.done":
				if c := calls[e.ItemID]; c != nil {
					c.args.Reset()
					c.args.WriteString(e.Arguments)
				}
			case "response.output_item.done":
				ok = o.itemDone(e.Item, calls, emitCall, cite)
			case "response.completed", "response.incomplete":
				for _, id := range order {
					if !emitCall(calls[id]) {
						return
					}
				}
				if r := e.Response; r != nil && r.Usage != nil {
					if !yield(Event{Kind: EventUsage, Usage: Usage{PromptTokens: r.Usage.InputTokens, CompletionTokens: r.Usage.OutputTokens}}, nil) {
						return
					}
				}
				yield(Event{Kind: EventDone, FinishReason: finishReason(e, emitted)}, nil)
				return
			case "response.failed":
				msg := "response failed"
				if e.Response != nil && e.Response.Error != nil {
					msg = e.Response.Error.Message
				}
				yield(Event{}, fmt.Errorf("responses: %s", msg))
				return
			case "error":
				msg := e.Message
				if e.Error != nil {
					msg = e.Error.Message
				}
				yield(Event{}, fmt.Errorf("responses: %s", msg))
				return
			}
			if !ok {
				return
			}
		}
		yield(Event{}, fmt.Errorf("responses stream: %w", errTruncated))
	}
}

// itemDone handles a finished output item: a function call is emitted with
// its final arguments; a message contributes its url_citation annotations.
func (o *responses) itemDone(it *respItem, calls map[string]*respCall, emitCall func(*respCall) bool, cite func(respAnnotation) bool) bool {
	if it == nil {
		return true
	}
	switch it.Type {
	case "function_call":
		c := calls[it.ID]
		if c == nil {
			c = &respCall{callID: it.CallID, name: it.Name}
			calls[it.ID] = c
		}
		if it.Arguments != "" {
			c.args.Reset()
			c.args.WriteString(it.Arguments)
		}
		return emitCall(c)
	case "message":
		for _, part := range it.Content {
			for _, a := range part.Annotations {
				if !cite(a) {
					return false
				}
			}
		}
	}
	return true
}

func finishReason(e respEvent, calls int) string {
	if e.Type == "response.incomplete" {
		if e.Response != nil && e.Response.IncompleteDetails != nil && e.Response.IncompleteDetails.Reason == "max_output_tokens" {
			return "length"
		}
		return "incomplete"
	}
	if calls > 0 {
		return "tool_calls"
	}
	return "stop"
}
