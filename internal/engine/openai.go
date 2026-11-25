package engine

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/provider"
)

// The OpenAI-compatible output: the same run, rendered as chat.completion
// chunks (stream) or one chat.completion object, with Kera's extras under "kera".

func chunk(run *Run, model string, created int64, choices any) map[string]any {
	return map[string]any{"id": "chatcmpl-" + run.ID, "object": "chat.completion.chunk", "created": created,
		"model": model, "choices": choices}
}

func deltaChoice(delta map[string]any, finish any) []any {
	return []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}
}

// sideEvents ride along as chunks with no choices.
var sideEvents = map[string]bool{"status": true, "sources": true, "files": true, "block": true, "tool_call": true,
	"tool_result": true, "title": true, "follow_ups": true, "compaction": true, "ask_user": true, "confirm": true}

func streamOpenAI(w http.ResponseWriter, r *http.Request, run *Run, model string) {
	s := newSSE(w)
	created := time.Now().Unix()
	send := func(v any) error { return s.data(mustMarshal(v, "{}")) }
	if send(chunk(run, model, created, deltaChoice(map[string]any{"role": "assistant", "content": ""}, nil))) != nil {
		return
	}
	follow(r, run, 0, func(ev event) error {
		switch {
		case ev.Type == "delta":
			return send(chunk(run, model, created, deltaChoice(map[string]any{"content": ev.Data["text"]}, nil)))
		case ev.Type == "reasoning" && ev.Data["text"] != nil:
			return send(chunk(run, model, created, deltaChoice(map[string]any{"reasoning_content": ev.Data["text"]}, nil)))
		case ev.Type == "tool_call" && ev.Data["server"] == "client":
			return nil // passthrough calls go out with the final chunk
		case sideEvents[ev.Type]:
			c := chunk(run, model, created, []any{})
			c["kera"] = map[string]any{"type": ev.Type, "data": ev.Data}
			return send(c)
		case ev.Type == "error":
			return send(map[string]any{"error": map[string]any{"message": ev.Data["message"], "code": ev.Data["code"]}})
		case ev.Type == "done":
			return finishStream(s, run, model, created)
		}
		return nil
	}, s.ping)
}

func finishStream(s *sseWriter, run *Run, model string, created int64) error {
	send := func(v any) error { return s.data(mustMarshal(v, "{}")) }
	run.mu.Lock()
	calls, finish, u, stopped := run.passthrough, coalesce(run.finish, "stop"), run.usage, run.state.Status == "stopped"
	run.mu.Unlock()
	if len(calls) > 0 {
		if err := send(chunk(run, model, created, deltaChoice(map[string]any{"tool_calls": wireCalls(calls)}, nil))); err != nil {
			return err
		}
	}
	if stopped {
		finish = "stop"
	}
	if err := send(chunk(run, model, created, deltaChoice(map[string]any{}, finish))); err != nil {
		return err
	}
	c := chunk(run, model, created, []any{})
	c["usage"] = usageJSON(u)
	if err := send(c); err != nil {
		return err
	}
	_, err := s.w.Write([]byte("data: [DONE]\n\n"))
	if err == nil {
		err = s.rc.Flush()
	}
	return err
}

func usageJSON(u Usage) map[string]any {
	return map[string]any{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
		"total_tokens": u.PromptTokens + u.CompletionTokens}
}

func wireCalls(calls []provider.ToolCall) []any {
	out := make([]any, 0, len(calls))
	for i, c := range calls {
		args := string(c.Arguments)
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		out = append(out, map[string]any{"index": i, "id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": args}})
	}
	return out
}

// respondOpenAI waits for the run and returns one chat.completion. The
// content is the final answer only (the text of the last model round).
func respondOpenAI(w http.ResponseWriter, r *http.Request, run *Run, model string) {
	follow(r, run, 0, func(event) error { return nil }, func() error { return nil })
	if !run.Finished() {
		return // client went away; the run continues and is persisted
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.state.Status == "error" {
		code := "error"
		for _, ev := range run.events {
			if ev.Type == "error" {
				code, _ = ev.Data["code"].(string)
			}
		}
		// a refusal of the request itself (guardrails, local lock) is the caller's 400, not an upstream failure
		status, typ := http.StatusBadGateway, "server_error"
		if code == "guardrail_blocked" || code == "local_lock" || code == "hook_error" || code == "context_length_exceeded" {
			status, typ = http.StatusBadRequest, "invalid_request_error"
		}
		api.JSON(w, status, map[string]any{"detail": run.state.Error, "code": code,
			"error": map[string]any{"message": run.state.Error, "code": code, "type": typ}})
		return
	}
	content := run.roundText
	if strings.TrimSpace(content) == "" {
		content = run.state.Text()
	}
	msg := map[string]any{"role": "assistant", "content": content}
	if run.reasoning != "" {
		msg["reasoning_content"] = run.reasoning
	}
	finish := coalesce(run.finish, "stop")
	if len(run.passthrough) > 0 {
		msg["tool_calls"] = wireCalls(run.passthrough)
		finish = "tool_calls"
	}
	toolsList := run.tools
	if toolsList == nil {
		toolsList = []toolRecord{}
	}
	api.JSON(w, http.StatusOK, map[string]any{
		"id": "chatcmpl-" + run.ID, "object": "chat.completion", "created": run.Created.Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   usageJSON(run.usage),
		"sources": nestedSources(run.state.Sources),
		"kera": map[string]any{"run_id": run.ID, "message_id": run.MessageID, "chat_id": run.ChatID, "tool_calls": toolsList,
			"sources": orSources(run.state.Sources), "files": orFiles(run.state.Files),
			"parts": json.RawMessage(mustMarshal(run.state.Parts, "[]"))},
	})
}

// nestedSources renders citations in Open WebUI's {source, document, metadata} shape.
func nestedSources(src []Source) []any {
	out := []any{}
	for _, s := range src {
		name := coalesce(s.Name, s.Title, s.URL)
		meta := map[string]any{"name": name, "source": coalesce(s.URL, s.FileID, name)}
		if s.FileID != "" {
			meta["file_id"] = s.FileID
		}
		if s.Page > 0 {
			meta["page"] = s.Page - 1
		}
		out = append(out, map[string]any{"source": map[string]any{"id": coalesce(s.FileID, s.URL, s.ID), "name": name, "url": s.URL},
			"document": []string{s.Snippet}, "metadata": []any{meta}})
	}
	return out
}

func orSources(s []Source) []Source {
	if s == nil {
		return []Source{}
	}
	return s
}

func orFiles(f []FileRef) []FileRef {
	if f == nil {
		return []FileRef{}
	}
	return f
}
