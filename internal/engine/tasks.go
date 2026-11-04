package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/provider"
)

// Background task prompts (our own wording).
const (
	titlePrompt     = `You name chat conversations. Read the conversation and reply with a short title of 3 to 6 words that starts with one fitting emoji and says what the conversation is about. Use the conversation's language. Reply with JSON only: {"title": "..."}`
	tagsPrompt      = `You file chat conversations. Read the conversation and pick 1 to 3 short, broad topic tags (for example "finance", "python", "travel"), most general first, in lowercase. Reply with JSON only: {"tags": ["..."]}`
	followUpsPrompt = `Suggest what the user might ask next. Read the conversation and write 3 short follow-up questions or requests, phrased as the user would type them, that continue naturally from the last answer. Use the conversation's language. Reply with JSON only: {"follow_ups": ["...", "...", "..."]}`
	queryPrompt     = `You write search queries for a document search engine. Read the conversation and write 1 to 3 short keyword queries that would find the information needed to answer the user's last message. Reply with JSON only: {"queries": ["..."]}`
	summaryPrompt   = `Summarise the conversation below so it can replace the original messages. Keep every fact, figure, name, decision, file name, open question and instruction the user gave; drop pleasantries. Write in the conversation's language, as compact notes.`
)

// taskModel picks the model for background tasks: local chats stay local
// (tasks.model_local, else the chat's own model); others use tasks.model_external.
func (e *Engine) taskModel(ctx context.Context, x *exec) (*models.Resolved, error) {
	var id string
	if x.m.Local {
		e.setting(ctx, "tasks.model_local", &id)
	} else {
		e.setting(ctx, "tasks.model_external", &id)
	}
	if id == "" || id == x.m.ID {
		return x.m, nil
	}
	// The task model is a system choice: the user need not be granted it, but
	// the provider still sees who the call is for.
	m, err := e.Models.Resolve(ctx, &auth.User{ID: x.u.ID, Role: "admin"}, id)
	if err != nil {
		slog.Warn("task model unavailable, using the chat model", "model", id, "err", err)
		return x.m, nil
	}
	m.UserHeaders = m.Connection.UserHeaders(x.u)
	return m, nil
}

// complete runs one short non-tool call and returns its text.
func (e *Engine) complete(ctx context.Context, m *models.Resolved, system, user string) (string, provider.Usage, error) {
	client, err := e.Models.Client(m)
	if err != nil {
		return "", provider.Usage{}, err
	}
	params := map[string]any{"think": false}
	for _, k := range []string{"num_ctx", "keep_alive"} {
		if v, ok := m.Params[k]; ok {
			params[k] = v
		}
	}
	var b strings.Builder
	var u provider.Usage
	for ev, err := range client.Stream(ctx, provider.Request{Model: m.ProviderModel, Params: params,
		Messages: []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}}) {
		if err != nil {
			return "", u, err
		}
		switch ev.Kind {
		case provider.EventText:
			b.WriteString(ev.Text)
		case provider.EventUsage:
			u = ev.Usage
		}
	}
	return b.String(), u, nil
}

// completeJSON runs a task whose answer is a JSON object and decodes it.
func (e *Engine) completeJSON(ctx context.Context, m *models.Resolved, system, user string, dst any) error {
	text, _, err := e.complete(ctx, m, system, user)
	if err != nil {
		return err
	}
	return parseLooseJSON(text, dst)
}

// parseLooseJSON decodes the first {...} object in text (models wrap JSON in prose or fences).
func parseLooseJSON(text string, dst any) error {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j < i {
		return fmt.Errorf("no JSON object in %q", clip(text, 120))
	}
	return json.Unmarshal([]byte(text[i:j+1]), dst)
}

// transcript renders the conversation (plus the new answer) for a task prompt.
func (x *exec) transcript(answer string, perMessage int) string {
	var b strings.Builder
	for _, t := range x.turns {
		if t.msg.Role == "system" || t.msg.Role == "tool" || t.msg.Content == "" {
			continue
		}
		fmt.Fprintf(&b, "### %s\n%s\n\n", t.msg.Role, clip(t.msg.Content, perMessage))
	}
	if answer != "" {
		fmt.Fprintf(&b, "### assistant\n%s\n", clip(answer, perMessage))
	}
	return b.String()
}

// backgroundTasks runs title, tags and follow-ups in parallel.
func (e *Engine) backgroundTasks(ctx context.Context, x *exec) {
	answer := x.run.stateText()
	if strings.TrimSpace(answer) == "" {
		return
	}
	// title and tags once: on the first turn of a chat nobody has named yet
	firstTurn := x.chat != nil && userTurns(x.turns) == 1
	doTitle := firstTurn && (x.chat.Title == "" || x.chat.Title == "New Chat") && e.Settings.Bool(ctx, "tasks.title")
	doTags := firstTurn && e.Settings.Bool(ctx, "tasks.tags")
	if doTags {
		if tags, err := e.Chats.Tags(ctx, x.chat.ID); err != nil || len(tags) > 0 {
			doTags = false
		}
	}
	doFollow := x.interactive && e.Settings.Bool(ctx, "tasks.follow_ups")
	if !doTitle && !doTags && !doFollow {
		return
	}
	tm, err := e.taskModel(ctx, x)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	conv := x.transcript(answer, 2000)
	var wg sync.WaitGroup
	if doTitle {
		wg.Go(func() {
			var out struct {
				Title string `json:"title"`
			}
			if err := e.completeJSON(ctx, tm, titlePrompt, conv, &out); err != nil {
				slog.Warn("title task", "err", err)
				return
			}
			title := clip(strings.TrimSpace(strings.Trim(out.Title, `"`)), 80)
			if title == "" {
				return
			}
			if _, err := e.Chats.SetTitle(ctx, x.u.ID, x.chat.ID, title); err != nil {
				slog.Warn("save title", "err", err)
				return
			}
			x.run.title(title)
		})
	}
	if doTags {
		wg.Go(func() {
			var out struct {
				Tags []string `json:"tags"`
			}
			if err := e.completeJSON(ctx, tm, tagsPrompt, conv, &out); err != nil {
				slog.Warn("tags task", "err", err)
				return
			}
			if len(out.Tags) > 3 {
				out.Tags = out.Tags[:3]
			}
			if err := e.Chats.AddTags(ctx, x.chat.ID, out.Tags); err != nil {
				slog.Warn("save tags", "err", err)
			}
		})
	}
	if doFollow {
		wg.Go(func() {
			var out struct {
				FollowUps []string `json:"follow_ups"`
			}
			if err := e.completeJSON(ctx, tm, followUpsPrompt, conv, &out); err != nil {
				slog.Warn("follow-ups task", "err", err)
				return
			}
			var list []string
			for _, f := range out.FollowUps {
				if f = strings.TrimSpace(f); f != "" && len(list) < 5 {
					list = append(list, f)
				}
			}
			if len(list) > 0 {
				x.run.followUps(list)
			}
		})
	}
	wg.Wait()
}

// generateQueries asks the task model for knowledge search queries.
func (e *Engine) generateQueries(ctx context.Context, x *exec) []string {
	tm, err := e.taskModel(ctx, x)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out struct {
		Queries []string `json:"queries"`
	}
	if err := e.completeJSON(ctx, tm, queryPrompt, x.transcript("", 1500), &out); err != nil {
		slog.Warn("query task", "err", err)
		return nil
	}
	var qs []string
	for _, q := range out.Queries {
		if q = strings.TrimSpace(q); q != "" && len(qs) < 3 {
			qs = append(qs, q)
		}
	}
	return qs
}

func (r *Run) stateText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.Text()
}
