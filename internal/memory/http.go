package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/tools"
)

// ID is the tool set id.
const ID = "builtin:memory"

func init() { app.Register("memory", Mount) }

var (
	regMu sync.Mutex
	regs  = map[*app.App]*Store{}
)

// For returns the app's memory store (the chat engine uses it to inject
// relevant memories into the prompt).
func For(a *app.App) *Store {
	regMu.Lock()
	defer regMu.Unlock()
	if s := regs[a]; s != nil {
		return s
	}
	reg := models.For(a)
	s := &Store{DB: a.DB, Settings: a.Settings, Embedder: reg.ConnectionClient}
	regs[a] = s
	return s
}

// Mount registers the tool provider and the /api/v1/memories routes.
func Mount(a *app.App) error {
	s := For(a)
	tools.Register(&Provider{Store: s})
	h := &handler{s: s}
	for p, fn := range map[string]http.HandlerFunc{
		"GET /api/v1/memories":                    h.list,
		"GET /api/v1/memories/{$}":                h.list,
		"POST /api/v1/memories/add":               h.add,
		"POST /api/v1/memories/query":             h.query,
		"POST /api/v1/memories/{id}/update":       h.update,
		"DELETE /api/v1/memories/{id}":            h.delete,
		"DELETE /api/v1/memories/delete/user":     h.deleteAll,
		"DELETE /api/v1/memories/delete/user/{$}": h.deleteAll,
	} {
		a.Server.Handle(p, a.Auth.Permission("features.memory", fn))
	}
	return nil
}

type handler struct{ s *Store }

// JSON is the Open WebUI-compatible shape (epoch seconds).
func JSON(m Memory) map[string]any {
	out := map[string]any{"id": m.ID, "user_id": m.UserID, "content": m.Content,
		"created_at": m.CreatedAt.Unix(), "updated_at": m.UpdatedAt.Unix()}
	if m.Score != 0 {
		out["score"] = m.Score
	}
	return out
}

func list(ms []Memory) []map[string]any {
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		out = append(out, JSON(m))
	}
	return out
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		err = api.Errf(http.StatusNotFound, "not_found", "memory not found")
	}
	api.Fail(w, r, err)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	ms, err := h.s.List(r.Context(), auth.FromContext(r.Context()).ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, list(ms))
}

type form struct {
	Content string `json:"content"`
	K       int    `json:"k"`
}

func decode(r *http.Request) (form, error) {
	var f form
	if err := api.Decode(r, &f); err != nil {
		return f, err
	}
	if _, err := clean(f.Content); err != nil {
		return f, api.Errf(http.StatusBadRequest, "bad_content", err.Error())
	}
	return f, nil
}

func (h *handler) add(w http.ResponseWriter, r *http.Request) {
	f, err := decode(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	m, err := h.s.Add(r.Context(), auth.FromContext(r.Context()).ID, f.Content)
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, JSON(m))
}

func (h *handler) query(w http.ResponseWriter, r *http.Request) {
	f, err := decode(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	ms, err := h.s.Search(r.Context(), auth.FromContext(r.Context()).ID, f.Content, min(max(f.K, 1), 50))
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, list(ms))
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	f, err := decode(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	m, err := h.s.Update(r.Context(), auth.FromContext(r.Context()).ID, r.PathValue("id"), f.Content)
	if err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, JSON(m))
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Delete(r.Context(), auth.FromContext(r.Context()).ID, r.PathValue("id")); err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, true)
}

func (h *handler) deleteAll(w http.ResponseWriter, r *http.Request) {
	if err := h.s.DeleteAll(r.Context(), auth.FromContext(r.Context()).ID); err != nil {
		fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, true)
}

// Provider implements tools.Provider over the memory store.
type Provider struct{ Store *Store }

// ID implements tools.Provider.
func (p *Provider) ID() string { return ID }

func spec(name, desc, params string) tools.Spec {
	return tools.Spec{Name: name, Description: desc, Parameters: json.RawMessage(params)}
}

var specs = []tools.Spec{
	spec("add_memory", "Remember a lasting fact or preference about the user for future chats (one short sentence). Only when the user asks you to remember something or states a durable preference.",
		`{"type":"object","properties":{"content":{"type":"string","description":"The fact to remember"}},"required":["content"]}`),
	spec("search_memories", "Search what you remember about the user.",
		`{"type":"object","properties":{"query":{"type":"string","description":"What to look for"},"count":{"type":"integer","description":"Maximum results (default 5)"}},"required":["query"]}`),
	spec("list_memories", "List everything you remember about the user.", `{"type":"object","properties":{}}`),
	spec("update_memory", "Replace the text of a memory (ids come from search_memories or list_memories).",
		`{"type":"object","properties":{"id":{"type":"string"},"content":{"type":"string"}},"required":["id","content"]}`),
	spec("delete_memory", "Forget a memory (ids come from search_memories or list_memories).",
		`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
}

// Tools implements tools.Provider: only for users with memory turned on.
func (p *Provider) Tools(ctx context.Context, u *auth.User) ([]tools.Spec, error) {
	on, err := p.Store.Enabled(ctx, u)
	if err != nil || !on {
		return nil, err
	}
	return specs, nil
}

// Execute implements tools.Provider.
func (p *Provider) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	if on, err := p.Store.Enabled(ctx, c.User); err != nil || !on {
		return errResult("memory is turned off for this user"), err
	}
	var a struct {
		ID, Content, Query string
		Count              int
	}
	if err := json.Unmarshal(c.Args, &a); err != nil && len(c.Args) > 0 {
		return errResult("invalid arguments"), nil
	}
	uid := c.User.ID
	var v any
	var err error
	desc := ""
	switch c.Name {
	case "add_memory":
		var m Memory
		m, err = p.Store.Add(ctx, uid, a.Content)
		v, desc = JSON(m), "Saved to memory"
	case "update_memory":
		var m Memory
		m, err = p.Store.Update(ctx, uid, a.ID, a.Content)
		v, desc = JSON(m), "Updated a memory"
	case "delete_memory":
		err, v, desc = p.Store.Delete(ctx, uid, a.ID), map[string]bool{"deleted": true}, "Forgot a memory"
	case "search_memories":
		var ms []Memory
		ms, err = p.Store.Search(ctx, uid, a.Query, min(max(a.Count, 1), 20))
		v, desc = list(ms), fmt.Sprintf("Found %d memories", len(ms))
	case "list_memories":
		var ms []Memory
		ms, err = p.Store.List(ctx, uid)
		v, desc = list(ms), fmt.Sprintf("Read %d memories", len(ms))
	default:
		return errResult("unknown tool " + c.Name), nil
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return errResult("no memory with that id"), nil
		}
		if c.Name == "add_memory" || c.Name == "update_memory" {
			if _, cerr := clean(a.Content); cerr != nil {
				return errResult(cerr.Error()), nil
			}
		}
		return tools.Result{}, err
	}
	c.Emit(tools.Event{Type: "status", Data: map[string]any{"action": "memory", "description": desc, "done": true}})
	b, _ := json.Marshal(v)
	return tools.Result{Content: string(b)}, nil
}

func errResult(msg string) tools.Result {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return tools.Result{Content: string(b), IsError: true}
}
