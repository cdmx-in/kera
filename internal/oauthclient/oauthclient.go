// Package oauthclient holds each user's OAuth grant for tool servers that act
// on the user's own account (auth type user_oauth): the browser flow
// (authorization code + PKCE S256), the encrypted token store, refresh before
// expiry and RFC 7009 revocation on disconnect.
package oauthclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/secret"
)

var errUnknownProvider = errors.New("unknown oauth provider")

// ErrNotConnected means the user holds no (usable) token for the provider.
var ErrNotConnected = errors.New("not connected")

// refreshBefore is how long before expiry a token is refreshed.
const refreshBefore = 5 * time.Minute

// Service reads and writes oauth_token rows.
type Service struct {
	db        *pgxpool.Pool
	keys      *secret.Keys
	publicURL string
	http      *http.Client

	refreshMu sync.Mutex // ponytail: one lock for every refresh; per-(user, provider) locks if refreshes ever queue up

	mu        sync.Mutex
	listeners []func(userID, provider string)
}

// New builds a service.
func New(db *pgxpool.Pool, keys *secret.Keys, publicURL string) *Service {
	return &Service{db: db, keys: keys, publicURL: strings.TrimRight(publicURL, "/"),
		http: &http.Client{Timeout: 20 * time.Second}}
}

var (
	regMu sync.Mutex
	reg   = map[*app.App]*Service{}
)

// For returns the app's service, creating it on first use (tool servers call
// it from their Mount; the routes are registered by this package's Mount).
func For(a *app.App) *Service {
	regMu.Lock()
	defer regMu.Unlock()
	if s := reg[a]; s != nil {
		return s
	}
	s := New(a.DB, a.Keys, a.Env.PublicURL)
	reg[a] = s
	return s
}

// OnChange subscribes fn to connects and disconnects (e.g. to drop caches).
func (s *Service) OnChange(fn func(userID, provider string)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

func (s *Service) changed(userID, provider string) {
	s.mu.Lock()
	ls := append([]func(string, string){}, s.listeners...)
	s.mu.Unlock()
	for _, fn := range ls {
		fn(userID, provider)
	}
}

// Provider is an oauth_provider row prepared for the code flow.
type Provider struct {
	ID           string
	Conf         oauth2.Config
	RevokeURL    string
	PKCE         bool
	SendResource bool
	google       bool
}

// RedirectURL is the callback registered at the provider for id.
func (s *Service) RedirectURL(id string) string {
	return s.publicURL + "/oauth/clients/" + id + "/callback"
}

// Provider loads a provider row (sign-in providers are excluded).
func (s *Service) Provider(ctx context.Context, id string) (*Provider, error) {
	var authURL, tokenURL, revokeURL, clientID, scope string
	var secretEnc []byte
	var pkce, sendResource bool
	err := s.db.QueryRow(ctx, `SELECT authorization_endpoint, token_endpoint, revocation_endpoint, client_id,
		client_secret_enc, scope, pkce, send_resource FROM oauth_provider
		WHERE id = $1 AND NOT options @> '{"login": true}'`, id).
		Scan(&authURL, &tokenURL, &revokeURL, &clientID, &secretEnc, &scope, &pkce, &sendResource)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w %q", errUnknownProvider, id)
	}
	if err != nil {
		return nil, fmt.Errorf("load oauth provider %s: %w", id, err)
	}
	if authURL == "" || tokenURL == "" {
		// ponytail: endpoints must be configured; add issuer metadata discovery (RFC 8414) when a provider needs it.
		return nil, fmt.Errorf("oauth provider %s: authorization_endpoint and token_endpoint are required", id)
	}
	clientSecret, err := s.keys.DecryptString(secretEnc)
	if err != nil {
		return nil, fmt.Errorf("oauth provider %s client secret: %w", id, err)
	}
	return &Provider{
		ID: id,
		Conf: oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: s.RedirectURL(id),
			Scopes:   strings.Fields(strings.ReplaceAll(scope, ",", " ")),
			Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL},
		},
		RevokeURL: revokeURL, PKCE: pkce, SendResource: sendResource,
		google: isGoogle(authURL),
	}, nil
}

// isGoogle: Google issues a refresh token only with access_type=offline, and
// again on a later consent only with prompt=consent.
func isGoogle(authURL string) bool {
	u, err := url.Parse(authURL)
	return err == nil && u.Hostname() == "accounts.google.com"
}

// stored is the encrypted token blob.
type stored struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
}

func (t stored) oauth() *oauth2.Token {
	return &oauth2.Token{AccessToken: t.AccessToken, TokenType: t.TokenType, RefreshToken: t.RefreshToken, Expiry: t.Expiry}
}

func fromOAuth(t *oauth2.Token) stored {
	return stored{AccessToken: t.AccessToken, TokenType: t.TokenType, RefreshToken: t.RefreshToken, Expiry: t.Expiry}
}

// Save stores (or replaces) the user's token for provider.
func (s *Service) Save(ctx context.Context, userID, provider string, t *oauth2.Token) error {
	b, _ := json.Marshal(fromOAuth(t))
	enc, err := s.keys.Encrypt(b)
	if err != nil {
		return err
	}
	scope, _ := t.Extra("scope").(string)
	var exp *time.Time
	if !t.Expiry.IsZero() {
		exp = &t.Expiry
	}
	_, err = s.db.Exec(ctx, `INSERT INTO oauth_token (id, user_id, provider, token_enc, scope, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, provider) DO UPDATE SET token_enc = EXCLUDED.token_enc,
		scope = CASE WHEN EXCLUDED.scope = '' THEN oauth_token.scope ELSE EXCLUDED.scope END,
		expires_at = EXCLUDED.expires_at, updated_at = now()`, ids.New(), userID, provider, enc, scope, exp)
	if err != nil {
		return fmt.Errorf("save oauth token: %w", err)
	}
	return nil
}

func (s *Service) load(ctx context.Context, userID, provider string) (stored, error) {
	var enc []byte
	err := s.db.QueryRow(ctx, `SELECT token_enc FROM oauth_token WHERE user_id = $1 AND provider = $2`, userID, provider).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return stored{}, ErrNotConnected
	}
	if err != nil {
		return stored{}, fmt.Errorf("load oauth token: %w", err)
	}
	plain, err := s.keys.Decrypt(enc)
	if err != nil {
		// a rotated secret key makes old tokens unreadable: the user reconnects
		slog.Warn("oauth token undecryptable, dropping it", "provider", provider, "user", userID)
		_ = s.remove(ctx, userID, provider)
		return stored{}, ErrNotConnected
	}
	var t stored
	if err := json.Unmarshal(plain, &t); err != nil || t.AccessToken == "" {
		_ = s.remove(ctx, userID, provider)
		return stored{}, ErrNotConnected
	}
	return t, nil
}

// Connected reports whether the user holds a token and since when.
func (s *Service) Connected(ctx context.Context, userID string) (map[string]time.Time, error) {
	rows, err := s.db.Query(ctx, `SELECT provider, created_at FROM oauth_token WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("list oauth tokens: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var p string
		var at time.Time
		if err := rows.Scan(&p, &at); err != nil {
			return nil, err
		}
		out[p] = at
	}
	return out, rows.Err()
}

// AccessToken returns a usable access token, refreshing it when it expires
// within five minutes. A refresh the provider rejects deletes the grant and
// returns ErrNotConnected; a network failure keeps it and returns the error.
func (s *Service) AccessToken(ctx context.Context, userID, provider string) (string, error) {
	t, err := s.load(ctx, userID, provider)
	if err != nil {
		return "", err
	}
	if t.Expiry.IsZero() || time.Until(t.Expiry) > refreshBefore {
		return t.AccessToken, nil
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if t, err = s.load(ctx, userID, provider); err != nil { // another caller may have refreshed meanwhile
		return "", err
	}
	if time.Until(t.Expiry) > refreshBefore {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" {
		if time.Now().Before(t.Expiry) {
			return t.AccessToken, nil // still valid for a few minutes; nothing to refresh with
		}
		_ = s.remove(ctx, userID, provider)
		return "", ErrNotConnected
	}
	p, err := s.Provider(ctx, provider)
	if err != nil {
		return "", err
	}
	src := p.Conf.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, s.http), &oauth2.Token{
		RefreshToken: t.RefreshToken, Expiry: time.Unix(1, 0)}) // forces a refresh
	nt, err := src.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.Response != nil && re.Response.StatusCode < 500 {
			slog.Info("oauth refresh rejected, grant dropped", "provider", provider, "user", userID, "status", re.Response.StatusCode)
			_ = s.remove(ctx, userID, provider)
			return "", ErrNotConnected
		}
		return "", fmt.Errorf("refresh %s token: %w", provider, err)
	}
	if nt.RefreshToken == "" {
		nt.RefreshToken = t.RefreshToken
	}
	if err := s.Save(ctx, userID, provider, nt); err != nil {
		return "", err
	}
	return nt.AccessToken, nil
}

func (s *Service) remove(ctx context.Context, userID, provider string) error {
	_, err := s.db.Exec(context.WithoutCancel(ctx), `DELETE FROM oauth_token WHERE user_id = $1 AND provider = $2`, userID, provider)
	s.changed(userID, provider)
	if err != nil {
		return fmt.Errorf("delete oauth token: %w", err)
	}
	return nil
}

// ErrRevokeFailed means the provider did not confirm the revocation; the
// grant is kept so the user can retry.
var ErrRevokeFailed = errors.New("revocation failed")

// Disconnect revokes the grant at the provider (RFC 7009) and deletes the
// token. Only an unreachable provider or a 5xx keeps the row (ErrRevokeFailed,
// the user retries); a 4xx means the token is already invalid or will never be
// accepted, so retrying cannot help. No token is not an error; a provider
// row that no longer exists only deletes the token.
func (s *Service) Disconnect(ctx context.Context, userID, provider string) error {
	t, err := s.load(ctx, userID, provider)
	if errors.Is(err, ErrNotConnected) {
		return nil
	}
	if err != nil {
		return err
	}
	p, err := s.Provider(ctx, provider)
	if errors.Is(err, errUnknownProvider) {
		return s.remove(ctx, userID, provider)
	}
	if err != nil {
		return err
	}
	if p.RevokeURL != "" {
		if err := s.revoke(ctx, p, t); err != nil {
			return err
		}
	}
	return s.remove(ctx, userID, provider)
}

func (s *Service) revoke(ctx context.Context, p *Provider, t stored) error {
	tok, hint := t.RefreshToken, "refresh_token" // revoking the refresh token ends the whole grant
	if tok == "" {
		tok, hint = t.AccessToken, "access_token"
	}
	form := url.Values{"token": {tok}, "token_type_hint": {hint}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if p.Conf.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(p.Conf.ClientID), url.QueryEscape(p.Conf.ClientSecret)) // RFC 6749 2.3.1
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s was not reachable", ErrRevokeFailed, p.ID)
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: %s answered HTTP %d", ErrRevokeFailed, p.ID, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		slog.Warn("oauth revocation refused, dropping the grant anyway", "provider", p.ID, "status", resp.StatusCode)
	} else {
		slog.Info("oauth grant revoked", "provider", p.ID)
	}
	return nil
}
