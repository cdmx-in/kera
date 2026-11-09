package toolserver

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/tools"
)

type handler struct {
	m   *Manager
	all func(context.Context) ([]tools.Provider, error) // tools.All; tests pass their manager's providers
}

// toolEntry is one row of GET /api/v1/tools/.
type toolEntry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"` // openapi | mcp | builtin
	Description   string   `json:"description"`
	Authenticated bool     `json:"authenticated"`
	AuthType      string   `json:"auth_type"`
	Provider      string   `json:"provider,omitempty"`
	AutoEnable    bool     `json:"auto_enable,omitempty"`
	LocalOnly     bool     `json:"local_only,omitempty"`
	ConnectedAt   int64    `json:"connected_at,omitempty"`
	Functions     []string `json:"functions"`
	Error         string   `json:"error,omitempty"`
}

// list answers GET /api/v1/tools/: every provider the caller may use, with
// its function names (each server gets 8 s; a failure leaves functions
// empty and sets error).
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	ps, err := h.all(r.Context())
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var connected map[string]time.Time
	if h.m.oauth != nil {
		if connected, err = h.m.oauth.Connected(r.Context(), u.ID); err != nil {
			api.Fail(w, r, err)
			return
		}
	}
	out := make([]*toolEntry, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Go(func() { out[i] = h.entry(r.Context(), u, p, connected) })
	}
	wg.Wait()
	list := []*toolEntry{}
	for _, e := range out {
		if e != nil {
			list = append(list, e)
		}
	}
	api.JSON(w, http.StatusOK, list)
}

func (h *handler) entry(ctx context.Context, u *auth.User, p tools.Provider, connected map[string]time.Time) *toolEntry {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	e := &toolEntry{ID: p.ID(), Name: p.ID(), Kind: "builtin", AuthType: "none", Authenticated: true, Functions: []string{}}
	if s, ok := p.(*Server); ok {
		if ok, err := s.CanRead(ctx, u); err != nil || !ok || s.ModelOnly() {
			return nil // model-only servers are reached through their assistants, not the picker
		}
		row := s.Row()
		e.Name, e.Kind, e.Description, e.AuthType, e.AutoEnable = s.label(), row.Kind, row.Description, row.Auth.Type, row.AutoEnable
		e.LocalOnly = row.LocalOnly
		if row.Auth.Type == "user_oauth" {
			e.Provider = row.Auth.Provider
			at, ok := connected[row.Auth.Provider]
			e.Authenticated = ok
			if ok {
				e.ConnectedAt = at.Unix()
			}
		}
	}
	specs, err := p.Tools(ctx, u)
	if err != nil {
		slog.Warn("tool list", "server", p.ID(), "err", err)
		e.Error = "unavailable"
		if u.IsAdmin() { // the detail names internal hosts
			e.Error = err.Error()
		}
	}
	for _, sp := range specs {
		e.Functions = append(e.Functions, sp.Name)
	}
	return e
}
