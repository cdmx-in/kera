// Package admin is the admin console's API: every runtime setting with its
// default and managed flag, CRUD for tool servers, hook servers and OAuth
// providers, usage analytics and LiteLLM spend. Users, groups, grants,
// connections and models live in their own packages.
package admin

import (
	"net/http"

	"github.com/cdmx-in/kera/internal/app"
)

func init() { app.Register("admin", Mount) }

type handler struct {
	a     *app.App
	spend spendPool
}

// Mount registers the admin routes (all admin-only).
func Mount(a *app.App) error {
	h := &handler{a: a}
	s := a.Server
	admin := func(f http.HandlerFunc) http.Handler { return a.Auth.Admin(f) }

	s.Handle("GET /api/v1/configs/settings", admin(h.getSettings))
	s.Handle("POST /api/v1/configs/settings", admin(h.setSettings))

	s.Handle("GET /api/v1/configs/tool_servers", admin(h.listToolServers))
	s.Handle("POST /api/v1/configs/tool_servers", admin(h.saveToolServer))
	s.Handle("POST /api/v1/configs/tool_servers/test", admin(h.testToolServer))
	s.Handle("DELETE /api/v1/configs/tool_servers/{id...}", admin(h.deleteToolServer))
	s.Handle("GET /api/v1/configs/hook_servers", admin(h.listHooks))
	s.Handle("POST /api/v1/configs/hook_servers", admin(h.saveHook))
	s.Handle("POST /api/v1/configs/hook_servers/test", admin(h.testHook))
	s.Handle("DELETE /api/v1/configs/hook_servers/{id...}", admin(h.deleteHook))
	s.Handle("GET /api/v1/configs/oauth_providers", admin(h.listProviders))
	s.Handle("POST /api/v1/configs/oauth_providers", admin(h.saveProvider))
	s.Handle("POST /api/v1/configs/oauth_providers/test", admin(h.testProvider))
	s.Handle("DELETE /api/v1/configs/oauth_providers/{id...}", admin(h.deleteProvider))

	s.Handle("GET /api/v1/analytics/summary", admin(h.summary))
	s.Handle("GET /api/v1/analytics/daily", admin(h.daily))
	s.Handle("GET /api/v1/analytics/models", admin(h.byModel))
	s.Handle("GET /api/v1/analytics/users", admin(h.byUser))
	s.Handle("GET /api/v1/analytics/spend", admin(h.getSpend))
	s.Handle("GET /api/v1/admin/guardrails/events", admin(h.guardrailEvents))
	s.Handle("GET /api/v1/admin/guardrails/summary", admin(h.guardrailSummary))
	return nil
}
