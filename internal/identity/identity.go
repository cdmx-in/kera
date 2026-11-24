// Package identity is sign-in (OAuth/OIDC), the current-user and API-key
// routes, and the admin API for users, groups and access grants.
package identity

import (
	"net/http"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
)

func init() { app.Register("identity", Mount) }

type handler struct {
	a        *app.App
	attempts *auth.Attempts // password sign-in and change-password tries
}

func newHandler(a *app.App) *handler { return &handler{a: a, attempts: auth.NewAttempts()} }

// Mount registers the identity routes.
func Mount(a *app.App) error {
	h := newHandler(a)
	s, au := a.Server, a.Auth
	admin := func(f http.HandlerFunc) http.Handler { return au.Admin(f) }
	user := func(f http.HandlerFunc) http.Handler { return au.Required(f) }
	keys := func(f http.HandlerFunc) http.Handler { return au.Permission("features.api_keys", f) }

	s.HandleFunc("GET /oauth/{provider}/login", h.login)
	s.HandleFunc("GET /oauth/{provider}/callback", h.callback)

	s.Handle("GET /api/v1/auths/{$}", user(h.me))
	s.HandleFunc("POST /api/v1/auths/signout", h.signout)
	s.HandleFunc("GET /api/v1/auths/signout", h.signout) // Open WebUI clients sign out with GET
	s.HandleFunc("GET /api/v1/auths/providers", h.providers)
	s.HandleFunc("POST /api/v1/auths/signin", h.signin)
	s.Handle("POST /api/v1/auths/update/password", user(h.changePassword))
	s.Handle("POST /api/v1/auths/add", admin(h.addUser))
	s.Handle("GET /api/v1/auths/api_keys", keys(h.listKeys))
	s.Handle("POST /api/v1/auths/api_keys", keys(h.createKey))
	s.Handle("DELETE /api/v1/auths/api_keys/{id}", keys(h.deleteKey))
	s.Handle("GET /api/v1/auths/api_key", keys(h.compatGetKey))
	s.Handle("POST /api/v1/auths/api_key", keys(h.compatNewKey))
	s.Handle("DELETE /api/v1/auths/api_key", keys(h.compatDeleteKeys))

	s.Handle("GET /api/v1/users/{$}", admin(h.listUsers))
	s.Handle("GET /api/v1/users/{id}", admin(h.getUser))
	s.Handle("POST /api/v1/users/{id}/update", admin(h.updateUser))
	s.Handle("POST /api/v1/users/{id}/password", admin(h.setUserPassword))
	s.Handle("DELETE /api/v1/users/{id}", admin(h.deleteUser))
	s.Handle("GET /api/v1/users/default/permissions", admin(h.getDefaultPerms))
	s.Handle("POST /api/v1/users/default/permissions", admin(h.setDefaultPerms))
	s.Handle("GET /api/v1/users/user/settings", user(h.getSettings))
	s.Handle("POST /api/v1/users/user/settings/update", user(h.updateSettings))
	s.Handle("GET /api/v1/users/{id}/profile/image", user(h.profileImage))

	s.Handle("GET /api/v1/groups/{$}", admin(h.listGroups))
	s.Handle("POST /api/v1/groups/create", admin(h.createGroup))
	s.Handle("GET /api/v1/groups/id/{id}", admin(h.getGroup))
	s.Handle("POST /api/v1/groups/id/{id}/update", admin(h.updateGroup))
	s.Handle("POST /api/v1/groups/id/{id}/users/add", admin(h.addMembers))
	s.Handle("POST /api/v1/groups/id/{id}/users/remove", admin(h.removeMembers))
	s.Handle("DELETE /api/v1/groups/id/{id}/delete", admin(h.deleteGroup))

	s.Handle("GET /api/v1/grants/{type}/{id...}", admin(h.getGrants))
	s.Handle("PUT /api/v1/grants/{type}/{id...}", admin(h.putGrants))
	return nil
}
