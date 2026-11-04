// Package desktop is the live view of a chat's Linux desktop (the desktop
// sidecar): the chat owner watches it in noVNC, takes over (for example to
// sign in) and ends it. The noVNC client is bundled in the web app; Kera
// proxies only the websocket to the sidecar's websockify and adds the
// session's token, which never reaches the browser.
package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
)

func init() { app.Register("desktop", Mount) }

type handler struct {
	a     *app.App
	chats *chats.Store
}

// Mount registers the /api/v1/desktop/{chat} routes. All of them answer 404
// while desktop.url is unset, and for chats the caller does not own.
func Mount(a *app.App) error {
	h := &handler{a: a, chats: &chats.Store{DB: a.DB}}
	a.Server.Handle("GET /api/v1/desktop/{chat}", a.Auth.Required(http.HandlerFunc(h.status)))
	a.Server.Handle("POST /api/v1/desktop/{chat}/end", a.Auth.Required(http.HandlerFunc(h.end)))
	a.Server.Handle("GET /api/v1/desktop/{chat}/websockify", a.Auth.Required(http.HandlerFunc(h.websockify)))
	return nil
}

var errOff = api.Errf(http.StatusNotFound, "not_found", "Desktop not available")

type sidecar struct{ url, viewURL, key, user string }

// sidecar checks ownership of {chat} and loads the settings.
func (h *handler) sidecar(r *http.Request) (sidecar, error) {
	ctx := r.Context()
	var s sidecar
	if err := h.a.Settings.Get(ctx, "desktop.url", &s.url); err != nil {
		return s, err
	}
	if s.url == "" {
		return s, errOff
	}
	s.user = auth.FromContext(ctx).ID
	if _, err := h.chats.Get(ctx, s.user, r.PathValue("chat")); err != nil {
		return s, err
	}
	if err := h.a.Settings.Get(ctx, "desktop.view_url", &s.viewURL); err != nil {
		return s, err
	}
	key, err := apply.ReadSecret(ctx, h.a.Settings, h.a.Keys, "desktop.key")
	s.key = key
	return s, err
}

var client = &http.Client{Timeout: 15 * time.Second}

// call sends method to {desktop.url}/sessions/{chat}{suffix} for the caller
// (sessions are keyed by user and chat); a 404 (no desktop for the chat)
// returns nil body and no error.
func (s sidecar) call(ctx context.Context, method, chat, suffix string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.url, "/")+"/sessions/"+url.PathEscape(chat)+suffix, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("X-Kera-User-Id", s.user)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("desktop sidecar: %w", err)
	}
	defer resp.Body.Close()
	var b json.RawMessage
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("desktop sidecar: %s %s: %s", method, suffix, resp.Status)
	}
	err = json.NewDecoder(resp.Body).Decode(&b)
	return b, err
}

// token is the session's websockify token, "" when the chat has no desktop.
func (s sidecar) token(ctx context.Context, chat string) (string, error) {
	b, err := s.call(ctx, http.MethodGet, chat, "/view")
	if b == nil || err != nil {
		return "", err
	}
	var v struct{ Token string }
	err = json.Unmarshal(b, &v)
	return v.Token, err
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	s, err := h.sidecar(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	tok, err := s.token(r.Context(), r.PathValue("chat"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]bool{"active": tok != ""})
}

func (h *handler) end(w http.ResponseWriter, r *http.Request) {
	s, err := h.sidecar(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := s.call(r.Context(), http.MethodDelete, r.PathValue("chat"), ""); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]bool{"active": false})
}

// sameOrigin is the cookie CSRF check auth applies to unsafe methods; a
// websocket upgrade is a GET, so websockify applies it itself.
var sameOrigin = http.NewCrossOriginProtection()

// websockify proxies the noVNC websocket (and nothing else) to
// {desktop.view_url}/websockify with the session's own token; the client's
// query is dropped.
// ponytail: all desktops share one uid inside the sidecar, so a command in one desktop can reach another
// session's VNC (token files, ports); one sidecar per user if that matters. Kera's origin is not exposed:
// no sidecar content is served from it.
func (h *handler) websockify(w http.ResponseWriter, r *http.Request) {
	s, err := h.sidecar(r)
	if err == nil && s.viewURL == "" {
		err = errOff
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_request", "Websocket upgrade required"))
		return
	}
	post := r.Clone(r.Context())
	post.Method = http.MethodPost // checked like an unsafe request: same origin only
	if err := sameOrigin.Check(post); err != nil {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "cross_origin", "Cross-origin request"))
		return
	}
	base, err := url.Parse(strings.TrimRight(s.viewURL, "/"))
	if err != nil {
		api.Fail(w, r, fmt.Errorf("desktop.view_url: %w", err))
		return
	}
	tok, err := s.token(r.Context(), r.PathValue("chat"))
	if err == nil && tok == "" {
		err = errOff
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL = &url.URL{Scheme: base.Scheme, Host: base.Host, Path: base.Path + "/websockify", RawQuery: url.Values{"token": {tok}}.Encode()}
		pr.Out.Host = ""
		pr.Out.Header.Del("Cookie") // the user's session stays in Kera
		pr.Out.Header.Del("Authorization")
	}}).ServeHTTP(w, r)
}
