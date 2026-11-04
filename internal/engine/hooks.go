package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/provider"
)

// hookServer is one enabled hook_server row.
type hookServer struct {
	ID, URL, Key, Scope, OnError string
	ModelIDs                     []string
	Config                       json.RawMessage
	Timeout                      time.Duration
}

// hooksFor returns the hooks for an event that apply to the model, by priority.
func (e *Engine) hooksFor(ctx context.Context, x *exec, event string) ([]hookServer, error) {
	rows, err := e.DB.Query(ctx, `SELECT id, url, key_enc, scope, model_ids, config, timeout_ms, on_error
		FROM hook_server WHERE enabled AND $1 = ANY(events) ORDER BY priority, id`, event)
	if err != nil {
		return nil, fmt.Errorf("list hooks: %w", err)
	}
	defer rows.Close()
	var out []hookServer
	for rows.Next() {
		var h hookServer
		var key []byte
		var ms int
		if err := rows.Scan(&h.ID, &h.URL, &key, &h.Scope, &h.ModelIDs, &h.Config, &ms, &h.OnError); err != nil {
			return nil, fmt.Errorf("scan hook: %w", err)
		}
		if h.Scope != "global" && !slices.Contains(h.ModelIDs, x.m.ID) && !slices.Contains(x.m.Hooks, h.ID) &&
			(x.m.BaseModelID == "" || !slices.Contains(h.ModelIDs, x.m.BaseModelID)) {
			continue
		}
		if h.Key, err = e.Keys.DecryptString(key); err != nil {
			slog.Error("hook key cannot be decrypted", "hook", h.ID)
		}
		h.Timeout = time.Duration(ms) * time.Millisecond
		if h.Timeout <= 0 {
			h.Timeout = 10 * time.Second
		}
		if event == "outlet" && ms <= 0 {
			h.Timeout = 20 * time.Second
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// hookReply is what a hook server answers.
type hookReply struct {
	Request *struct {
		Messages      []provider.Message `json:"messages"`
		Params        map[string]any     `json:"params"`
		Features      map[string]bool    `json:"features"`
		ProviderTools []json.RawMessage  `json:"provider_tools"`
		DisableTools  json.RawMessage    `json:"disable_tools"`
	} `json:"request"`
	Message *struct {
		Content *string           `json:"content"`
		Parts   []json.RawMessage `json:"parts"`
	} `json:"message"`
	Events []struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"events"`
	Error string `json:"error"`
}

// runHooks calls every inlet or outlet hook in order and applies the replies.
func (e *Engine) runHooks(ctx context.Context, x *exec, event string) error {
	hooks, err := e.hooksFor(ctx, x, event)
	if err != nil || len(hooks) == 0 {
		return err
	}
	for _, h := range hooks {
		reply, err := e.callHook(ctx, x, h, event)
		if err == nil && reply.Error != "" {
			return api.Errf(http.StatusBadRequest, "hook_error", reply.Error)
		}
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			if h.OnError == "abort" {
				return fmt.Errorf("hook %s: %w", h.ID, err)
			}
			slog.Warn("hook failed, skipped", "hook", h.ID, "event", event, "err", err)
			continue
		}
		e.applyHook(x, reply)
	}
	return nil
}

func (e *Engine) envelope(x *exec, h hookServer, event string) map[string]any {
	run := x.run
	tok, _ := signRunToken(e.Keys, run.ID, run.UserID, time.Now().Add(15*time.Minute))
	msgs := make([]provider.Message, 0, len(x.turns))
	for _, t := range x.turns {
		msgs = append(msgs, t.msg)
	}
	env := map[string]any{
		"hook": map[string]any{"id": h.ID, "config": json.RawMessage(orRaw(h.Config, "{}"))},
		"run":  map[string]any{"id": run.ID, "chat_id": run.ChatID, "message_id": run.MessageID, "temporary": !run.Persisted},
		"user": map[string]any{"id": x.u.ID, "name": x.u.Name, "email": x.u.Email, "role": x.u.Role, "groups": orStrings(x.profile.GroupNames)},
		"model": map[string]any{"id": x.m.ID, "base_model_id": x.m.BaseModelID, "local": x.m.Local,
			"connection": map[string]any{"id": x.m.Connection.ID, "kind": x.m.Connection.Kind}},
		"request":  map[string]any{"messages": msgs, "params": x.params, "features": x.features, "provider_tools": orRaws(x.providerTools)},
		"files":    orStrings(x.fileIDs),
		"callback": map[string]any{"url": e.callbackURL(run.ID), "token": tok},
	}
	if event == "outlet" {
		run.mu.Lock()
		env["message"] = map[string]any{"id": run.MessageID, "content": run.state.Text(),
			"parts": json.RawMessage(mustMarshal(run.state.Parts, "[]"))}
		run.mu.Unlock()
	}
	return env
}

func (e *Engine) callHook(ctx context.Context, x *exec, h hookServer, event string) (hookReply, error) {
	var reply hookReply
	body, err := json.Marshal(e.envelope(x, h, event))
	if err != nil {
		return reply, err
	}
	ctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.URL, "/")+"/"+event, bytes.NewReader(body))
	if err != nil {
		return reply, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Key != "" {
		req.Header.Set("Authorization", "Bearer "+h.Key)
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return reply, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return reply, err
	}
	if resp.StatusCode/100 != 2 {
		return reply, fmt.Errorf("status %d: %s", resp.StatusCode, clip(strings.TrimSpace(string(b)), 200))
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return reply, nil
	}
	if err := json.Unmarshal(b, &reply); err != nil {
		return reply, fmt.Errorf("decode reply: %w", err)
	}
	return reply, nil
}

func (e *Engine) applyHook(x *exec, r hookReply) {
	if q := r.Request; q != nil {
		if q.Messages != nil {
			x.turns = x.turns[:0]
			for _, m := range q.Messages {
				x.turns = append(x.turns, turn{msg: m})
			}
		}
		for k, v := range q.Params {
			x.params[k] = v
		}
		for k, v := range q.Features {
			x.features[k] = v
		}
		if q.ProviderTools != nil {
			x.providerTools = q.ProviderTools
		}
		var all bool
		var names []string
		if json.Unmarshal(q.DisableTools, &all) == nil && all {
			x.disableServers = true
		} else if json.Unmarshal(q.DisableTools, &names) == nil {
			for _, n := range names {
				x.disabledNames[n] = true
			}
		}
	}
	if m := r.Message; m != nil {
		run := x.run
		run.mu.Lock()
		if m.Parts != nil {
			var parts []*Part
			if b, err := json.Marshal(m.Parts); err == nil && json.Unmarshal(b, &parts) == nil {
				run.state.Parts = parts
			}
		} else if m.Content != nil {
			kept := run.state.Parts[:0:0]
			for _, p := range run.state.Parts {
				if p.Type != "text" {
					kept = append(kept, p)
				}
			}
			run.state.Parts = append(kept, &Part{Type: "text", Text: *m.Content})
		}
		run.emitLocked("snapshot", run.snapshotLocked())
		run.mu.Unlock()
	}
	for _, ev := range r.Events {
		var data any
		_ = json.Unmarshal(ev.Data, &data)
		e.applyEvent(x.run, "", ev.Type, data)
	}
}

func orRaw(b json.RawMessage, empty string) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(empty)
	}
	return b
}

func orRaws(v []json.RawMessage) []json.RawMessage {
	if v == nil {
		return []json.RawMessage{}
	}
	return v
}

func orStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
