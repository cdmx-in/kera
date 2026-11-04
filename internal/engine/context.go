package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/cowork"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/retrieval"
)

// maxImageBytes caps an image sent to the model.
const maxImageBytes = 20 << 20

// fileWait is how long a run waits for an attachment's text extraction.
var fileWait = 90 * time.Second

// renderVars fills the {{...}} placeholders of a system prompt.
func renderVars(prompt string, u *auth.User, p userProfile, now time.Time) string {
	if !strings.Contains(prompt, "{{") {
		return prompt
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil || p.Timezone == "" {
		loc, p.Timezone = time.UTC, "UTC"
	}
	t := now.In(loc)
	groups := strings.Join(p.GroupNames, ", ")
	return strings.NewReplacer(
		"{{CURRENT_DATE}}", t.Format("2006-01-02"),
		"{{CURRENT_TIME}}", t.Format("15:04:05"),
		"{{CURRENT_DATETIME}}", t.Format("2006-01-02 15:04:05"),
		"{{CURRENT_WEEKDAY}}", t.Weekday().String(),
		"{{CURRENT_TIMEZONE}}", p.Timezone,
		"{{USER_NAME}}", u.Name,
		"{{USER_EMAIL}}", u.Email,
		"{{USER_ROLE}}", u.Role,
		"{{USER_LANGUAGE}}", p.Language,
		"{{USER_GROUPS}}", groups,
		"{{USER_LOCATION}}", p.Location,
	).Replace(prompt)
}

// buildContext assembles the system prompt, knowledge, attachments and the
// provider-native tools.
func (e *Engine) buildContext(ctx context.Context, x *exec) error {
	// prepended in reverse: preset persona, then the cowork prompt, then the rest
	if x.cowork() {
		x.system = append([]string{renderVars(cowork.Prompt(ctx, e.Settings), x.u, x.profile, time.Now())}, x.system...)
	}
	if p := strings.TrimSpace(x.m.SystemPrompt); p != "" {
		x.system = append([]string{renderVars(p, x.u, x.profile, time.Now())}, x.system...)
	}
	if err := e.attachFiles(ctx, x); err != nil {
		return err
	}
	if err := e.knowledge(ctx, x); err != nil {
		return err
	}
	if x.features["web_search"] && fmt.Sprint(x.m.Features["web_search"]) == "native" && x.m.Connection.Kind == provider.KindResponses {
		x.providerTools = append(x.providerTools, webSearchTool(x.m.Connection.WebSearch()))
	}
	return nil
}

// webSearchTool builds the Responses API web_search tool from connection options.
func webSearchTool(opts json.RawMessage) json.RawMessage {
	var o struct {
		Country     string `json:"country"`
		City        string `json:"city"`
		Timezone    string `json:"timezone"`
		ContextSize string `json:"context_size"`
	}
	_ = json.Unmarshal(opts, &o)
	t := map[string]any{"type": "web_search"}
	if o.ContextSize != "" {
		t["search_context_size"] = o.ContextSize
	}
	loc := map[string]any{"type": "approximate"}
	for k, v := range map[string]string{"country": o.Country, "city": o.City, "timezone": o.Timezone} {
		if v != "" {
			loc[k] = v
		}
	}
	if len(loc) > 1 {
		t["user_location"] = loc
	}
	b, _ := json.Marshal(t)
	return b
}

// attachFiles turns each turn's files into images (multimodal), injected
// text (file_context full) or a listing for the file tools (file_context tools).
func (e *Engine) attachFiles(ctx context.Context, x *exec) error {
	vision := x.m.Capabilities["vision"] != false
	full := x.m.FileContext == "full"
	var listing []string
	waited := false
	for i := range x.turns {
		t := &x.turns[i]
		var blocks []string
		for _, id := range t.files {
			if ok, err := e.Files.CanRead(ctx, x.u, id); err != nil || !ok {
				continue
			}
			f, err := e.Files.Get(ctx, id)
			if err != nil {
				continue
			}
			if strings.HasPrefix(f.ContentType, "image/") {
				if vision && t.msg.Role == "user" {
					if img, ok := e.image(ctx, f); ok {
						t.msg.Images = append(t.msg.Images, img)
					}
				}
				continue
			}
			x.fileIDs = append(x.fileIDs, id)
			if !f.Done() {
				if !waited {
					x.run.status(StatusEvent{ID: "files", Action: "preparing_files", Description: "Reading attached files"})
					waited = true
				}
				if f, err = e.Files.Wait(ctx, id, fileWait); err != nil && ctx.Err() != nil {
					return ctx.Err()
				}
			}
			if full {
				text, _ := e.Files.Text(ctx, id)
				blocks = append(blocks, fmt.Sprintf("<file name=%q>\n%s\n</file>", f.Filename, text))
			} else {
				listing = append(listing, fileLine(f))
			}
		}
		if len(blocks) > 0 {
			t.msg.Content = strings.Join(blocks, "\n\n") + "\n\n" + t.msg.Content
		}
	}
	if waited {
		x.run.status(StatusEvent{ID: "files", Action: "preparing_files", Description: "Read attached files", Done: true, Hidden: true})
	}
	if len(listing) > 0 {
		x.system = append(x.system, "<attached_files>\n"+strings.Join(listing, "\n")+"\n</attached_files>\n"+
			"The user attached these files. Read them with view_file (page through long files) and search them with grep_chat_files before answering; do not guess their contents.")
	}
	return nil
}

func fileLine(f files.File) string {
	line := fmt.Sprintf("- %s (id: %s, %s, %d bytes", f.Filename, f.ID, f.ContentType, f.Size)
	if f.Status == files.Failed {
		line += ", text extraction failed: " + f.Error
	}
	return line + ")"
}

func (e *Engine) image(ctx context.Context, f files.File) (provider.Image, bool) {
	if f.Size > maxImageBytes {
		return provider.Image{}, false
	}
	rc, _, err := e.Files.Open(ctx, f.ID)
	if err != nil {
		slog.Warn("open image", "file", f.ID, "err", err)
		return provider.Image{}, false
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxImageBytes))
	if err != nil {
		return provider.Image{}, false
	}
	return provider.Image{MIME: f.ContentType, Data: base64.StdEncoding.EncodeToString(b)}, true
}

// knowledge injects the model's collections (and those attached to the
// request): whole documents for mode full, retrieved chunks for retrieve.
func (e *Engine) knowledge(ctx context.Context, x *exec) error {
	r := retrieval.Get()
	if r == nil {
		return nil
	}
	modes := map[string]string{} // id or name -> mode
	var refs []string
	for _, k := range x.m.Knowledge {
		for _, key := range []string{k.ID, k.Name} {
			if key != "" {
				refs = append(refs, key)
				modes[key] = e.knowledgeMode(ctx, x, r, k.Mode)
			}
		}
	}
	for _, id := range x.collections {
		refs = append(refs, id)
		if modes[id] == "" {
			modes[id] = "retrieve"
		}
	}
	if len(refs) == 0 {
		return nil
	}
	cols, err := r.Resolve(ctx, x.u, refs)
	if err != nil {
		return fmt.Errorf("resolve knowledge: %w", err)
	}
	var fullIDs, searchIDs []string
	for _, c := range cols {
		switch coalesce(modes[c.ID], modes[c.Name]) {
		case "full":
			fullIDs = append(fullIDs, c.ID)
		case "retrieve":
			searchIDs = append(searchIDs, c.ID)
		}
	}
	var docs []source
	if len(fullIDs) > 0 {
		limit := 400000
		e.setting(ctx, "rag.full_context_max_chars", &limit)
		total := 0
		for _, id := range fullIDs {
			ds, err := r.Documents(ctx, x.u, id)
			if err != nil {
				return fmt.Errorf("load knowledge: %w", err)
			}
			for _, d := range ds {
				if total+len(d.Text) > limit {
					slog.Warn("knowledge full context truncated", "model", x.m.ID, "limit", limit)
					break
				}
				total += len(d.Text)
				docs = append(docs, source{name: d.Name, fileID: d.FileID, text: d.Text})
			}
		}
	}
	if len(searchIDs) > 0 {
		hits, err := e.search(ctx, x, r, searchIDs)
		if err != nil {
			return err
		}
		docs = append(docs, hits...)
	}
	if len(docs) == 0 {
		return nil
	}
	var tmpl string
	e.setting(ctx, "rag.template", &tmpl)
	x.system = append(x.system, ragBlock(tmpl, docs))
	var srcs []Source
	for i, d := range docs {
		srcs = append(srcs, Source{ID: fmt.Sprint(i + 1), Name: d.name, FileID: d.fileID, Page: d.page,
			Snippet: clip(strings.TrimSpace(d.text), 300), Score: d.score})
	}
	x.run.sources(srcs)
	return nil
}

// knowledgeMode resolves a model's knowledge mode. Unset (or "auto") means
// retrieve when searches are reranked; without a working reranker it means
// tools when the run already gives the model tool servers: unreranked
// passages pushed into the prompt then compete with what its tools return
// (in an eval of a KB preset with a facts tool server they doubled the
// invented specifics; reranked passages scored best). Otherwise retrieve.
// Explicit retrieve, full and tools are kept as they are.
func (e *Engine) knowledgeMode(ctx context.Context, x *exec, r retrieval.Retriever, mode string) string {
	if mode != "" && mode != "auto" {
		return mode
	}
	if rr, ok := r.(retrieval.Reranking); ok && rr.Reranks(ctx) {
		return "retrieve"
	}
	if len(x.req.Tools) == 0 && !x.disableServers && len(x.serverIDs()) > 0 && e.builtinAllowed(ctx, x, "builtin:knowledge") {
		return "tools"
	}
	return "retrieve"
}

type source struct {
	name, fileID, text string
	section            string // markdown breadcrumb of a retrieved chunk
	page               int
	score              float64
}

func (e *Engine) search(ctx context.Context, x *exec, r retrieval.Retriever, ids []string) ([]source, error) {
	x.run.status(StatusEvent{ID: "knowledge", Action: "knowledge_search", Description: "Searching knowledge"})
	queries := []string{x.lastUserText()}
	if e.Settings.Bool(ctx, "tasks.query") && userTurns(x.turns) > 1 {
		if q := e.generateQueries(ctx, x); len(q) > 0 {
			queries = q
		}
	}
	k := 6
	e.setting(ctx, "rag.top_k_rerank", &k)
	var out []source
	seen := map[string]bool{}
	for _, q := range queries {
		hits, err := r.Search(ctx, x.u, ids, q, k)
		if err != nil {
			return nil, fmt.Errorf("knowledge search: %w", err)
		}
		for _, h := range hits {
			key := h.FileID + "|" + h.Text
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, source{name: h.FileName, fileID: h.FileID, text: h.Text, page: h.Page, score: h.Score,
				section: strings.Join(h.Headers, " > ")})
		}
	}
	x.run.status(StatusEvent{ID: "knowledge", Action: "knowledge_search", Query: strings.Join(queries, "; "),
		Description: fmt.Sprintf("Found %d passages in knowledge", len(out)), Done: true, Count: len(out)})
	return out, nil
}

func userTurns(ts []turn) int {
	n := 0
	for _, t := range ts {
		if t.msg.Role == "user" {
			n++
		}
	}
	return n
}

const defaultRAGTemplate = `Answer using the sources below when they are relevant. Cite a source by its number in square brackets, like [1], right after the statement it supports. If the sources do not contain the answer, say so plainly instead of guessing.

<sources>
{{CONTEXT}}
</sources>`

func ragBlock(tmpl string, docs []source) string {
	if !strings.Contains(tmpl, "{{CONTEXT}}") {
		tmpl = defaultRAGTemplate
	}
	var b strings.Builder
	for i, d := range docs {
		// the section breadcrumb tells the model where a chunk from deep inside
		// a long document sits (its own text often lacks the heading)
		attrs := fmt.Sprintf("id=\"%d\" name=\"%s\"", i+1, html.EscapeString(d.name))
		if d.section != "" {
			attrs += fmt.Sprintf(" section=\"%s\"", html.EscapeString(d.section))
		}
		fmt.Fprintf(&b, "<source %s>\n%s\n</source>\n", attrs, d.text)
	}
	return strings.Replace(tmpl, "{{CONTEXT}}", strings.TrimSpace(b.String()), 1)
}

// messages is the provider conversation: system sections then the turns.
func (x *exec) messages() []provider.Message {
	var out []provider.Message
	if s := strings.TrimSpace(strings.Join(x.system, "\n\n")); s != "" {
		out = append(out, provider.Message{Role: "system", Content: s})
	}
	for _, t := range x.turns {
		out = append(out, t.msg)
	}
	return out
}
