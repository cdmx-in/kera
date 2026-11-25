package chats

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/audit"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

func init() { app.Register("chats", Mount) }

var (
	regMu sync.Mutex
	reg   = map[*app.App]*Store{}
)

// For returns the app's chat store (shared with the chat engine).
func For(a *app.App) *Store {
	regMu.Lock()
	defer regMu.Unlock()
	if s := reg[a]; s != nil {
		return s
	}
	s := &Store{DB: a.DB}
	reg[a] = s
	return s
}

// LiveFunc returns the in-flight state of a streaming message; the engine
// installs it so a reload mid-stream shows everything streamed so far.
type LiveFunc func(messageID string) (Live, bool)

// SetLive installs the engine's live-state lookup.
func (s *Store) SetLive(f LiveFunc) { s.live = f }

// Mount registers /api/v1/chats, /api/v1/folders and /api/v1/evaluations/feedback.
func Mount(a *app.App) error {
	h := &handler{s: For(a)}
	user := func(f http.HandlerFunc) http.Handler { return a.Auth.Required(f) } // CSRF: see auth.Credential
	perm := func(p string, f http.HandlerFunc) http.Handler { return a.Auth.Permission(p, f) }
	for p, fn := range h.routes(user, perm) {
		a.Server.Handle(p, fn)
	}
	a.Server.Handle("GET /api/v1/evaluations/feedbacks/all", a.Auth.Admin(http.HandlerFunc(h.allFeedback)))
	return nil
}

type wrap = func(http.HandlerFunc) http.Handler

// routes is the route table; user and perm add authentication.
func (h *handler) routes(user wrap, perm func(string, http.HandlerFunc) http.Handler) map[string]http.Handler {
	return map[string]http.Handler{
		"GET /api/v1/chats/{$}":                      user(h.list),
		"GET /api/v1/chats/list":                     user(h.list),
		"GET /api/v1/chats/pinned":                   user(h.pinned),
		"GET /api/v1/chats/archived":                 user(h.archived),
		"GET /api/v1/chats/search":                   user(h.search),
		"GET /api/v1/chats/all/tags":                 user(h.userTags),
		"POST /api/v1/chats/tags":                    user(h.byTag),
		"POST /api/v1/chats/new":                     user(h.create),
		"GET /api/v1/chats/{a}/{b}":                  user(h.pair),
		"GET /api/v1/chats/{id}":                     user(h.get),
		"POST /api/v1/chats/{id}":                    user(h.update),
		"DELETE /api/v1/chats/{id}":                  perm("chat.delete", h.delete),
		"POST /api/v1/chats/{id}/pin":                user(h.pin),
		"POST /api/v1/chats/{id}/archive":            user(h.archive),
		"POST /api/v1/chats/{id}/folder":             user(h.move),
		"POST /api/v1/chats/{id}/share":              perm("chat.share", h.share),
		"DELETE /api/v1/chats/{id}/share":            user(h.unshare),
		"POST /api/v1/chats/{id}/tags":               user(h.addTag),
		"DELETE /api/v1/chats/{id}/tags":             user(h.removeTag),
		"DELETE /api/v1/chats/{id}/tags/all":         user(h.clearTags),
		"POST /api/v1/chats/{id}/messages/{mid}":     user(h.editMessage),
		"DELETE /api/v1/chats/{id}/messages/{mid}":   user(h.deleteMessage),
		"GET /api/v1/folders/{$}":                    user(h.folders),
		"POST /api/v1/folders/{$}":                   user(h.createFolder),
		"GET /api/v1/folders/{id}":                   user(h.folder),
		"POST /api/v1/folders/{id}/update":           user(h.renameFolder),
		"POST /api/v1/folders/{id}/update/parent":    user(h.moveFolder),
		"POST /api/v1/folders/{id}/update/expanded":  user(h.expandFolder),
		"DELETE /api/v1/folders/{id}":                user(h.deleteFolder),
		"POST /api/v1/evaluations/feedback":          user(h.createFeedback),
		"GET /api/v1/evaluations/feedback/chat/{id}": user(h.chatVotes),
	}
}

type handler struct{ s *Store }

// pair serves the two-segment GET routes in one pattern, because
// share/{id} and {id}/tags would otherwise be ambiguous for the mux.
func (h *handler) pair(w http.ResponseWriter, r *http.Request) {
	a, b := r.PathValue("a"), r.PathValue("b")
	switch {
	case a == "share":
		r.SetPathValue("share", b)
		h.shared(w, r)
	case a == "folder":
		r.SetPathValue("folder", b)
		h.inFolder(w, r)
	case b == "tags":
		r.SetPathValue("id", a)
		h.tags(w, r)
	case b == "pinned":
		r.SetPathValue("id", a)
		h.isPinned(w, r)
	default:
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "not found"))
	}
}

func uid(r *http.Request) string { return auth.FromContext(r.Context()).ID }

func page(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	return max(p, 0)
}

func (h *handler) listWith(w http.ResponseWriter, r *http.Request, f ListFilter) {
	f.UserID = uid(r)
	if f.Page == 0 {
		f.Page = page(r)
	}
	list, err := h.s.List(r.Context(), f)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, list)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	h.listWith(w, r, ListFilter{FolderID: r.URL.Query().Get("folder_id"), Tag: r.URL.Query().Get("tag")})
}
func (h *handler) pinned(w http.ResponseWriter, r *http.Request) {
	h.listWith(w, r, ListFilter{Pinned: true})
}
func (h *handler) archived(w http.ResponseWriter, r *http.Request) {
	h.listWith(w, r, ListFilter{Archived: true})
}
func (h *handler) inFolder(w http.ResponseWriter, r *http.Request) {
	h.listWith(w, r, ListFilter{FolderID: r.PathValue("folder")})
}

// search: ?text= matches titles and message text as word prefixes;
// "tag:<name>" words filter by tag.
func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	var words []string
	f := ListFilter{}
	for _, w := range strings.Fields(r.URL.Query().Get("text")) {
		if t, ok := strings.CutPrefix(w, "tag:"); ok {
			f.Tag = t
			continue
		}
		words = append(words, w)
	}
	f.Text = strings.Join(words, " ")
	if f.Page = page(r); f.Page == 0 {
		f.Page = 1
	}
	h.listWith(w, r, f)
}

func (h *handler) byTag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.listWith(w, r, ListFilter{Tag: body.Name})
}

func (h *handler) userTags(w http.ResponseWriter, r *http.Request) {
	tags, err := h.s.UserTags(r.Context(), uid(r))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, tagObjects(tags, uid(r)))
}

func tagObjects(tags []string, userID string) []map[string]any {
	out := make([]map[string]any, 0, len(tags))
	for _, t := range tags {
		out = append(out, map[string]any{"id": t, "name": t, "user_id": userID})
	}
	return out
}

// ---- one chat -------------------------------------------------------------------------

// Render loads a chat with its messages, tags and live state as chat JSON.
func (s *Store) Render(ctx context.Context, c Chat) (map[string]any, error) {
	msgs, err := s.Messages(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	tags, err := s.Tags(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(msgs))
	for _, m := range msgs {
		e := EntryOf(m)
		if m.Status == "streaming" && s.live != nil {
			if l, ok := s.live(m.ID); ok {
				e.Overlay(l)
			}
		}
		entries = append(entries, e)
	}
	return ChatJSON(c, entries, tags), nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.s.Get(r.Context(), uid(r), r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	h.render(w, r, c)
}

func (h *handler) render(w http.ResponseWriter, r *http.Request, c Chat) {
	out, err := h.s.Render(r.Context(), c)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out)
}

// chatForm is Open WebUI's ChatForm: {chat: {...}, folder_id?}.
type chatForm struct {
	Chat struct {
		ID      string            `json:"id"`
		Title   *string           `json:"title"`
		Models  []string          `json:"models"`
		Tags    []json.RawMessage `json:"tags"`
		History *struct {
			CurrentID *string                `json:"currentId"`
			Messages  map[string]formMessage `json:"messages"`
		} `json:"history"`
		FolderID *string `json:"folder_id"`
		Pinned   *bool   `json:"pinned"`
		Mode     string  `json:"mode"` // create only: chat (default) | cowork
	} `json:"chat"`
	FolderID *string `json:"folder_id"`
}

type formMessage struct {
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Model     string          `json:"model"`
	Timestamp float64         `json:"timestamp"`
	Done      *bool           `json:"done"`
	Files     json.RawMessage `json:"files"`
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var f chatForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	ctx := r.Context()
	c := Chat{UserID: uid(r), ModelIDs: f.Chat.Models, Mode: cmp.Or(f.Chat.Mode, "chat")}
	switch c.Mode {
	case "chat":
	case "cowork":
		if !auth.FromContext(ctx).Can("features.cowork") {
			api.Fail(w, r, api.Errf(http.StatusForbidden, "forbidden", "Permission required: features.cowork"))
			return
		}
	default:
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_mode", "mode must be chat or cowork"))
		return
	}
	if f.Chat.Title != nil {
		c.Title = strings.TrimSpace(*f.Chat.Title)
	}
	folder := f.FolderID
	if folder == nil {
		folder = f.Chat.FolderID
	}
	if folder != nil && *folder != "" {
		if _, err := h.s.Folder(ctx, c.UserID, *folder); err != nil {
			api.Fail(w, r, err)
			return
		}
		c.FolderID = folder
	}
	if f.Chat.Pinned != nil {
		c.Pinned = *f.Chat.Pinned
	}
	// ponytail: a client-chosen chat id is ignored (Open WebUI clients send ""); ids are ours.
	if err := h.s.Create(ctx, &c); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := h.s.importHistory(ctx, &c, f); err != nil {
		_ = h.s.Delete(context.WithoutCancel(ctx), c.UserID, c.ID)
		api.Fail(w, r, err)
		return
	}
	h.render(w, r, c)
}

// importHistory stores the messages a ChatForm carries (the throwaway-chat
// flow sends the user turn and an empty assistant placeholder).
func (s *Store) importHistory(ctx context.Context, c *Chat, f chatForm) error {
	var tags []string
	for _, t := range f.Chat.Tags {
		var name string
		if json.Unmarshal(t, &name) != nil {
			var o struct{ Name string }
			_ = json.Unmarshal(t, &o)
			name = o.Name
		}
		tags = append(tags, name)
	}
	if err := s.AddTags(ctx, c.ID, tags); err != nil {
		return err
	}
	if f.Chat.History == nil || len(f.Chat.History.Messages) == 0 {
		return nil
	}
	msgs := f.Chat.History.Messages
	// parents first, so parent_id always points at an inserted row
	order := make([]formMessage, 0, len(msgs))
	done := map[string]bool{}
	var visit func(id string, depth int)
	visit = func(id string, depth int) {
		m, ok := msgs[id]
		if !ok || done[id] || depth > len(msgs) {
			return
		}
		if m.ParentID != nil {
			visit(*m.ParentID, depth+1)
		}
		if !done[id] {
			done[id] = true
			order = append(order, m)
		}
	}
	for id := range msgs {
		visit(id, 0)
	}
	base := time.Now()
	for i, fm := range order {
		if fm.ID == "" || (fm.Role != "user" && fm.Role != "assistant" && fm.Role != "system") {
			continue
		}
		m := Message{ID: fm.ID, ChatID: c.ID, Role: fm.Role, ModelID: fm.Model, Content: contentString(fm.Content),
			Files: fm.Files}
		if fm.ParentID != nil && done[*fm.ParentID] {
			m.ParentID = fm.ParentID
		}
		if fm.Done != nil && !*fm.Done && fm.Role == "assistant" {
			m.Status = "streaming"
		}
		if err := s.InsertMessage(ctx, &m); err != nil {
			return err
		}
		ts := base.Add(time.Duration(i) * time.Millisecond)
		if fm.Timestamp > 0 {
			ts = epoch(fm.Timestamp).Add(time.Duration(i) * time.Millisecond)
		}
		if _, err := s.DB.Exec(ctx, `UPDATE message SET created_at = $2, updated_at = $2 WHERE id = $1`, m.ID, ts); err != nil {
			return err
		}
	}
	cur := ""
	if f.Chat.History.CurrentID != nil {
		cur = *f.Chat.History.CurrentID
	}
	if !done[cur] && len(order) > 0 {
		cur = order[len(order)-1].ID
	}
	if cur != "" {
		return s.SetCurrent(ctx, c.ID, cur)
	}
	return nil
}

// epoch reads seconds or milliseconds.
func epoch(v float64) time.Time {
	if v > 1e12 {
		return time.UnixMilli(int64(v))
	}
	return time.Unix(int64(v), 0)
}

// contentString flattens a string or a [{type:"text", text}] list.
func contentString(b json.RawMessage) string {
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	var list []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(b, &list)
	var parts []string
	for _, p := range list {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// update accepts {chat: {title?, history: {currentId}?, folder_id?, pinned?}}.
func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var f chatForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	ctx, u, id := r.Context(), uid(r), r.PathValue("id")
	c, err := h.s.Get(ctx, u, id)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if t := f.Chat.Title; t != nil && strings.TrimSpace(*t) != "" {
		if c, err = h.s.SetTitle(ctx, u, id, strings.TrimSpace(*t)); err != nil {
			api.Fail(w, r, err)
			return
		}
	}
	if f.Chat.History != nil && f.Chat.History.CurrentID != nil && *f.Chat.History.CurrentID != "" {
		if err := h.s.SetCurrent(ctx, id, *f.Chat.History.CurrentID); err != nil {
			api.Fail(w, r, err)
			return
		}
	}
	if f.Chat.Pinned != nil {
		if c, err = h.s.update(ctx, u, id, `pinned = $3`, *f.Chat.Pinned); err != nil {
			api.Fail(w, r, err)
			return
		}
	}
	if c, err = h.s.Get(ctx, u, id); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.render(w, r, c)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Delete(r.Context(), uid(r), r.PathValue("id")); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, true)
}

func (h *handler) toggle(w http.ResponseWriter, r *http.Request, col string) {
	c, err := h.s.update(r.Context(), uid(r), r.PathValue("id"), col+` = NOT `+col)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, summaryOf(c))
}

func (h *handler) pin(w http.ResponseWriter, r *http.Request)     { h.toggle(w, r, "pinned") }
func (h *handler) archive(w http.ResponseWriter, r *http.Request) { h.toggle(w, r, "archived") }

func (h *handler) isPinned(w http.ResponseWriter, r *http.Request) {
	c, err := h.s.Get(r.Context(), uid(r), r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, c.Pinned)
}

func summaryOf(c Chat) Summary {
	return Summary{ID: c.ID, Title: c.Title, UpdatedAt: c.UpdatedAt.Unix(), CreatedAt: c.CreatedAt.Unix(),
		Pinned: c.Pinned, Archived: c.Archived, FolderID: c.FolderID}
}

func (h *handler) move(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FolderID *string `json:"folder_id"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	ctx, u := r.Context(), uid(r)
	if body.FolderID != nil && *body.FolderID == "" {
		body.FolderID = nil
	}
	if body.FolderID != nil {
		if _, err := h.s.Folder(ctx, u, *body.FolderID); err != nil {
			api.Fail(w, r, err)
			return
		}
	}
	c, err := h.s.update(ctx, u, r.PathValue("id"), `folder_id = $3`, body.FolderID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, summaryOf(c))
}

// ---- sharing ----------------------------------------------------------------------------

// share snapshots the chat as it is now; sharing again refreshes the snapshot.
func (h *handler) share(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), uid(r)
	c, err := h.s.Get(ctx, u, r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	msgs, err := h.s.Messages(ctx, c.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	entries := make([]Entry, 0, len(msgs))
	for _, m := range msgs {
		if m.Status != "streaming" {
			entries = append(entries, EntryOf(m))
		}
	}
	cur := ""
	if c.CurrentMessageID != nil {
		cur = *c.CurrentMessageID
	}
	snap := map[string]any{"title": c.Title, "currentId": cur, "messages": entries}
	shareID := ids.New()
	if c.ShareID != nil {
		shareID = *c.ShareID
	}
	tx, err := h.s.DB.Begin(ctx)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO shared_chat (share_id, chat_id, user_id, title, snapshot) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (share_id) DO UPDATE SET title = EXCLUDED.title, snapshot = EXCLUDED.snapshot, updated_at = now()`,
		shareID, c.ID, u, c.Title, snap); err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE chat SET share_id = $2 WHERE id = $1`, c.ID, shareID); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		api.Fail(w, r, err)
		return
	}
	audit.Record(ctx, audit.Event{Action: "chat.share", TargetID: c.ID, Meta: map[string]any{"share_id": shareID}})
	api.JSON(w, http.StatusOK, map[string]any{"id": shareID, "share_id": shareID, "chat_id": c.ID, "title": c.Title})
}

func (h *handler) unshare(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), uid(r)
	c, err := h.s.Get(ctx, u, r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := h.s.DB.Exec(ctx, `DELETE FROM shared_chat WHERE chat_id = $1`, c.ID); err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := h.s.DB.Exec(ctx, `UPDATE chat SET share_id = NULL WHERE id = $1`, c.ID); err != nil {
		api.Fail(w, r, err)
		return
	}
	audit.Record(ctx, audit.Event{Action: "chat.unshare", TargetID: c.ID})
	api.JSON(w, http.StatusOK, true)
}

// shared returns a share snapshot to any signed-in user (the /s/:id page).
func (h *handler) shared(w http.ResponseWriter, r *http.Request) {
	var title string
	var snap []byte
	var created, updated time.Time
	err := h.s.DB.QueryRow(r.Context(), `SELECT title, snapshot, created_at, updated_at FROM shared_chat WHERE share_id = $1`,
		r.PathValue("share")).Scan(&title, &snap, &created, &updated)
	if err != nil {
		api.Fail(w, r, ErrNotFound)
		return
	}
	var s struct {
		Title     string  `json:"title"`
		CurrentID string  `json:"currentId"`
		Messages  []Entry `json:"messages"`
	}
	if err := json.Unmarshal(snap, &s); err != nil {
		api.Fail(w, r, err)
		return
	}
	if title == "" {
		title = s.Title
	}
	cur := s.CurrentID
	c := Chat{ID: r.PathValue("share"), Title: title, CurrentMessageID: &cur, CreatedAt: created, UpdatedAt: updated}
	out := ChatJSON(c, s.Messages, nil)
	delete(out, "user_id")
	api.JSON(w, http.StatusOK, out)
}

// ---- tags -----------------------------------------------------------------------------

func (h *handler) owned(w http.ResponseWriter, r *http.Request) (Chat, bool) {
	c, err := h.s.Get(r.Context(), uid(r), r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, err)
		return c, false
	}
	return c, true
}

func (h *handler) tags(w http.ResponseWriter, r *http.Request) {
	c, ok := h.owned(w, r)
	if !ok {
		return
	}
	tags, err := h.s.Tags(r.Context(), c.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, tagObjects(tags, c.UserID))
}

func (h *handler) tagBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Name string `json:"name"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return "", false
	}
	if NormalizeTag(body.Name) == "" {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_tag", "tag name is required"))
		return "", false
	}
	return NormalizeTag(body.Name), true
}

func (h *handler) addTag(w http.ResponseWriter, r *http.Request) {
	c, ok := h.owned(w, r)
	if !ok {
		return
	}
	name, ok := h.tagBody(w, r)
	if !ok {
		return
	}
	if err := h.s.AddTags(r.Context(), c.ID, []string{name}); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.tags(w, r)
}

func (h *handler) removeTag(w http.ResponseWriter, r *http.Request) {
	c, ok := h.owned(w, r)
	if !ok {
		return
	}
	name, ok := h.tagBody(w, r)
	if !ok {
		return
	}
	if _, err := h.s.DB.Exec(r.Context(), `DELETE FROM chat_tag WHERE chat_id = $1 AND tag = $2`, c.ID, name); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.tags(w, r)
}

func (h *handler) clearTags(w http.ResponseWriter, r *http.Request) {
	c, ok := h.owned(w, r)
	if !ok {
		return
	}
	if _, err := h.s.DB.Exec(r.Context(), `DELETE FROM chat_tag WHERE chat_id = $1`, c.ID); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, true)
}

// ---- messages -------------------------------------------------------------------------

func (h *handler) editMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := h.s.EditMessage(r.Context(), uid(r), r.PathValue("id"), r.PathValue("mid"), body.Content); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.get(w, r)
}

func (h *handler) deleteMessage(w http.ResponseWriter, r *http.Request) {
	if err := h.s.DeleteMessage(r.Context(), uid(r), r.PathValue("id"), r.PathValue("mid")); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.get(w, r)
}

// ---- folders --------------------------------------------------------------------------

func (h *handler) folders(w http.ResponseWriter, r *http.Request) {
	list, err := h.s.Folders(r.Context(), uid(r))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, list)
}

func (h *handler) folder(w http.ResponseWriter, r *http.Request) {
	f, err := h.s.Folder(r.Context(), uid(r), r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, folderErr(err))
		return
	}
	api.JSON(w, http.StatusOK, f)
}

type folderForm struct {
	Name       string  `json:"name"`
	ParentID   *string `json:"parent_id"`
	IsExpanded bool    `json:"is_expanded"`
}

func (h *handler) createFolder(w http.ResponseWriter, r *http.Request) {
	var f folderForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if f.Name = strings.TrimSpace(f.Name); f.Name == "" {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_name", "folder name is required"))
		return
	}
	out, err := h.s.CreateFolder(r.Context(), uid(r), f.Name, f.ParentID)
	if err != nil {
		api.Fail(w, r, folderErr(err))
		return
	}
	api.JSON(w, http.StatusOK, out)
}

func (h *handler) folderUpdate(w http.ResponseWriter, r *http.Request, set string, v any) {
	out, err := h.s.updateFolder(r.Context(), uid(r), r.PathValue("id"), set, v)
	if err != nil {
		api.Fail(w, r, folderErr(err))
		return
	}
	api.JSON(w, http.StatusOK, out)
}

func (h *handler) renameFolder(w http.ResponseWriter, r *http.Request) {
	var f folderForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if f.Name = strings.TrimSpace(f.Name); f.Name == "" {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_name", "folder name is required"))
		return
	}
	h.folderUpdate(w, r, `name = $3`, f.Name)
}

func (h *handler) moveFolder(w http.ResponseWriter, r *http.Request) {
	var f folderForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if f.ParentID != nil && *f.ParentID == "" {
		f.ParentID = nil
	}
	if f.ParentID != nil {
		if *f.ParentID == r.PathValue("id") {
			api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_parent", "a folder cannot contain itself"))
			return
		}
		if _, err := h.s.Folder(r.Context(), uid(r), *f.ParentID); err != nil {
			api.Fail(w, r, folderErr(err))
			return
		}
	}
	// ponytail: deeper cycles (a folder moved under its own descendant) are not checked; the UI has no nesting yet.
	h.folderUpdate(w, r, `parent_id = $3`, f.ParentID)
}

func (h *handler) expandFolder(w http.ResponseWriter, r *http.Request) {
	var f folderForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.folderUpdate(w, r, `expanded = $3`, f.IsExpanded)
}

func (h *handler) deleteFolder(w http.ResponseWriter, r *http.Request) {
	if err := h.s.DeleteFolder(r.Context(), uid(r), r.PathValue("id")); err != nil {
		api.Fail(w, r, folderErr(err))
		return
	}
	api.JSON(w, http.StatusOK, true)
}

func folderErr(err error) error {
	if errors.Is(err, ErrNotFound) {
		return api.Errf(http.StatusNotFound, "not_found", "Folder not found")
	}
	return err
}
