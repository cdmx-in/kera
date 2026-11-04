package oauthclient

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
)

func init() { app.Register("oauthclient", Mount) }

const (
	flowCookie = "kera_oauth_client" // the sign-in flow uses its own cookie, so both can run at once
	flowTTL    = 10 * time.Minute
)

// Mount registers the browser flow and the disconnect route.
func Mount(a *app.App) error {
	h := &handler{a: a, s: For(a)}
	a.Server.HandleFunc("GET /oauth/clients/{provider}/authorize", h.authorize)
	a.Server.HandleFunc("GET /oauth/clients/{provider}/callback", h.callback)
	a.Server.Handle("DELETE /api/v1/oauth/clients/{provider}/session", a.Auth.Required(http.HandlerFunc(h.disconnect)))
	return nil
}

type handler struct {
	a *app.App
	s *Service
}

// flow travels in an AES-GCM sealed (so also authenticated) cookie between
// authorize and callback.
type flow struct {
	Provider string `json:"p"`
	User     string `json:"u"`
	State    string `json:"s"`
	Verifier string `json:"v,omitempty"`
	Next     string `json:"n"`
	Exp      int64  `json:"e"`
}

func (h *handler) authorize(w http.ResponseWriter, r *http.Request) {
	u, err := h.a.Auth.Resolve(r)
	if err != nil {
		http.Redirect(w, r, "/auth?redirect="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	id := r.PathValue("provider")
	p, err := h.s.Provider(r.Context(), id)
	if err != nil {
		h.fail(w, r, "unknown_provider", "/", err)
		return
	}
	server, serverURL := h.toolServer(r.Context(), id)
	next := safeNext(r.URL.Query().Get("next"), "/?tools="+url.QueryEscape(server))
	f := flow{Provider: id, User: u.ID, State: randomString(), Next: next, Exp: time.Now().Add(flowTTL).Unix()}
	var opts []oauth2.AuthCodeOption
	if p.PKCE {
		f.Verifier = oauth2.GenerateVerifier()
		opts = append(opts, oauth2.S256ChallengeOption(f.Verifier))
	}
	if p.google {
		opts = append(opts, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	}
	if p.SendResource && serverURL != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", serverURL)) // RFC 8707
	}
	b, _ := json.Marshal(f)
	sealed, err := h.a.Keys.Encrypt(b)
	if err != nil {
		h.fail(w, r, "server_error", next, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: base64.RawURLEncoding.EncodeToString(sealed),
		Path: "/oauth/clients/", HttpOnly: true, Secure: h.a.Auth.Secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(flowTTL.Seconds())})
	http.Redirect(w, r, p.Conf.AuthCodeURL(f.State, opts...), http.StatusFound)
}

func (h *handler) callback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	q := r.URL.Query()
	id := r.PathValue("provider")
	f, ok := h.readFlow(r)
	if !ok || f.Provider != id || !hmac.Equal([]byte(f.State), []byte(q.Get("state"))) {
		h.fail(w, r, "invalid_state", "/", nil)
		return
	}
	// The grant goes to whoever started the flow, and only if that user is
	// still the one signed in (no token planted into another session).
	if u, err := h.a.Auth.Resolve(r); err != nil || u.ID != f.User {
		h.fail(w, r, "session_mismatch", f.Next, err)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		h.fail(w, r, "provider_denied", f.Next, nil)
		return
	}
	p, err := h.s.Provider(ctx, id)
	if err != nil {
		h.fail(w, r, "unknown_provider", f.Next, err)
		return
	}
	var opts []oauth2.AuthCodeOption
	if f.Verifier != "" {
		opts = append(opts, oauth2.VerifierOption(f.Verifier))
	}
	tok, err := p.Conf.Exchange(context.WithValue(ctx, oauth2.HTTPClient, h.s.http), q.Get("code"), opts...)
	if err != nil {
		h.fail(w, r, "exchange_failed", f.Next, err)
		return
	}
	if err := h.s.Save(ctx, f.User, id, tok); err != nil {
		h.fail(w, r, "server_error", f.Next, err)
		return
	}
	h.s.changed(f.User, id)
	slog.Info("oauth tool grant connected", "provider", id, "user", f.User, "refresh_token", tok.RefreshToken != "")
	clearFlow(w, h.a.Auth.Secure)
	http.Redirect(w, r, f.Next, http.StatusFound)
}

func (h *handler) disconnect(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	id := r.PathValue("provider")
	err := h.s.Disconnect(r.Context(), u.ID, id)
	if errors.Is(err, ErrRevokeFailed) {
		// 409, never 502/504: a CDN in front may replace a 5xx body with its own page.
		api.Fail(w, r, api.Errf(http.StatusConflict, "revoke_failed", err.Error()+"; the connection was kept, try again"))
		return
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// toolServer returns the first tool server that uses provider (for the
// default return path and the RFC 8707 resource); the provider id otherwise.
func (h *handler) toolServer(ctx context.Context, provider string) (id, serverURL string) {
	err := h.a.DB.QueryRow(ctx, `SELECT id, url FROM tool_server WHERE auth->>'type' = 'user_oauth'
		AND auth->>'provider' = $1 ORDER BY sort, id LIMIT 1`, provider).Scan(&id, &serverURL)
	if err != nil {
		return provider, ""
	}
	return id, serverURL
}

func (h *handler) readFlow(r *http.Request) (flow, bool) {
	var f flow
	c, err := r.Cookie(flowCookie)
	if err != nil {
		return f, false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return f, false
	}
	plain, err := h.a.Keys.Decrypt(sealed)
	if err != nil || json.Unmarshal(plain, &f) != nil || time.Now().Unix() > f.Exp || f.State == "" {
		return f, false
	}
	return f, true
}

func clearFlow(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: "", Path: "/oauth/clients/", HttpOnly: true, Secure: secure, MaxAge: -1})
}

// fail ends the browser flow back in the app with ?oauth_error=<code>.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, code, next string, err error) {
	if err != nil {
		slog.Warn("oauth tool connect failed", "provider", r.PathValue("provider"), "code", code, "err", err)
	}
	clearFlow(w, h.a.Auth.Secure)
	u, _ := url.Parse(next)
	q := u.Query()
	q.Set("oauth_error", code)
	q.Del("tools")
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// safeNext keeps only same-origin relative paths (no open redirect).
func safeNext(n, def string) string {
	if n == "" || n[0] != '/' || strings.HasPrefix(n, "//") || strings.ContainsAny(n, "\\\r\n\t") {
		return def
	}
	u, err := url.Parse(n)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return def
	}
	return n
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
