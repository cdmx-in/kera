package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

// Google endpoints, used when a provider row leaves an endpoint empty.
// (Revocation matters only to per-user tool tokens, not to sign-in.)
const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserinfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
)

const (
	flowCookie = "kera_oauth"
	flowTTL    = 10 * time.Minute
)

// provider is a login provider (an oauth_provider row with options.login = true)
// with Google defaults filled in.
type provider struct {
	id          string
	conf        oauth2.Config
	userinfoURL string
	pkce        bool
}

func (h *handler) provider(ctx context.Context, id string) (*provider, error) {
	var authURL, tokenURL, userinfoURL, clientID, scope string
	var secretEnc []byte
	var pkce bool
	err := h.a.DB.QueryRow(ctx, `SELECT authorization_endpoint, token_endpoint, userinfo_endpoint, client_id,
		client_secret_enc, scope, pkce FROM oauth_provider WHERE id = $1 AND options @> '{"login": true}'`, id).
		Scan(&authURL, &tokenURL, &userinfoURL, &clientID, &secretEnc, &scope, &pkce)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("no login provider %q", id)
	}
	if err != nil {
		return nil, fmt.Errorf("load provider %s: %w", id, err)
	}
	clientSecret, err := h.a.Keys.DecryptString(secretEnc)
	if err != nil {
		return nil, fmt.Errorf("provider %s client secret: %w", id, err)
	}
	if scope == "" {
		scope = "openid email profile"
	}
	return &provider{
		id: id,
		conf: oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret,
			RedirectURL: h.a.Env.PublicURL + "/oauth/" + id + "/callback",
			Scopes:      strings.Fields(strings.ReplaceAll(scope, ",", " ")),
			Endpoint:    oauth2.Endpoint{AuthURL: or(authURL, googleAuthURL), TokenURL: or(tokenURL, googleTokenURL)},
		},
		userinfoURL: or(userinfoURL, googleUserinfoURL),
		pkce:        pkce,
	}, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// flow is what the short-lived, encrypted (AES-GCM, so also authenticated)
// cookie carries between login and callback.
type flow struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Verifier string `json:"v,omitempty"`
	Next     string `json:"n"`
	Exp      int64  `json:"e"`
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	p, err := h.provider(r.Context(), r.PathValue("provider"))
	if err != nil {
		h.fail(w, r, "unknown_provider", err)
		return
	}
	f := flow{Provider: p.id, State: randomString(), Next: safeNext(r.URL.Query().Get("next")),
		Exp: time.Now().Add(flowTTL).Unix()}
	var opts []oauth2.AuthCodeOption
	if p.pkce {
		f.Verifier = oauth2.GenerateVerifier()
		opts = append(opts, oauth2.S256ChallengeOption(f.Verifier))
	}
	b, _ := json.Marshal(f)
	sealed, err := h.a.Keys.Encrypt(b)
	if err != nil {
		h.fail(w, r, "server_error", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: base64.RawURLEncoding.EncodeToString(sealed), Path: "/oauth/",
		HttpOnly: true, Secure: h.a.Auth.Secure, SameSite: http.SameSiteLaxMode, MaxAge: int(flowTTL.Seconds())})
	http.Redirect(w, r, p.conf.AuthCodeURL(f.State, opts...), http.StatusFound)
}

func (h *handler) callback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	q := r.URL.Query()
	f, ok := h.readFlow(r)
	if !ok || f.Provider != r.PathValue("provider") || !hmac.Equal([]byte(f.State), []byte(q.Get("state"))) {
		h.fail(w, r, "invalid_state", nil)
		return
	}
	if e := q.Get("error"); e != "" || q.Get("code") == "" {
		h.fail(w, r, "provider_denied", nil)
		return
	}
	p, err := h.provider(ctx, f.Provider)
	if err != nil {
		h.fail(w, r, "unknown_provider", err)
		return
	}
	var opts []oauth2.AuthCodeOption
	if f.Verifier != "" {
		opts = append(opts, oauth2.VerifierOption(f.Verifier))
	}
	tok, err := p.conf.Exchange(ctx, q.Get("code"), opts...)
	if err != nil {
		h.fail(w, r, "exchange_failed", err)
		return
	}
	info, err := fetchUserinfo(ctx, p, tok)
	if err != nil {
		h.fail(w, r, "userinfo_failed", err)
		return
	}
	if code := h.checkEmail(ctx, info); code != "" {
		h.fail(w, r, code, nil)
		return
	}
	userID, role, err := h.resolveUser(ctx, p.id, info)
	if err != nil {
		code := "server_error"
		switch {
		case errors.Is(err, errSignupDisabled):
			code, err = "signup_disabled", nil
		case errors.Is(err, errEmailTaken):
			code, err = "email_taken", nil
		}
		h.fail(w, r, code, err)
		return
	}
	if role == "pending" {
		h.fail(w, r, "account_pending", nil)
		return
	}
	var disabled bool
	if err := h.a.DB.QueryRow(ctx, `SELECT disabled_at IS NOT NULL FROM "user" WHERE id = $1`, userID).Scan(&disabled); err != nil {
		h.fail(w, r, "server_error", err)
		return
	}
	if disabled {
		h.fail(w, r, "account_disabled", nil)
		return
	}
	ttl := h.a.Auth.SessionTTL(ctx)
	session, err := auth.IssueToken(h.a.Keys, userID, ttl)
	if err != nil {
		h.fail(w, r, "server_error", err)
		return
	}
	clearFlow(w, h.a.Auth.Secure)
	h.a.Auth.SetSessionCookie(w, session, ttl)
	http.Redirect(w, r, "/auth#token="+session+"&next="+url.QueryEscape(f.Next), http.StatusFound)
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
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: "", Path: "/oauth/", HttpOnly: true, Secure: secure, MaxAge: -1})
}

// fail ends a browser flow on the sign-in page with an error code.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, code string, err error) {
	if err != nil {
		slog.Warn("oauth sign-in failed", "provider", r.PathValue("provider"), "code", code, "err", err)
	}
	clearFlow(w, h.a.Auth.Secure)
	http.Redirect(w, r, "/auth?error="+url.QueryEscape(code), http.StatusFound)
}

// Userinfo is the subset of the OIDC userinfo response Kera reads.
type Userinfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"` // bool, or "true" from some providers
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

func (u Userinfo) verified() bool { return u.EmailVerified == true || u.EmailVerified == "true" }

func fetchUserinfo(ctx context.Context, p *provider, tok *oauth2.Token) (Userinfo, error) {
	var u Userinfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.userinfoURL, nil)
	if err != nil {
		return u, err
	}
	tok.SetAuthHeader(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return u, fmt.Errorf("userinfo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return u, fmt.Errorf("userinfo: status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&u); err != nil {
		return u, fmt.Errorf("userinfo: decode: %w", err)
	}
	if u.Sub == "" {
		return u, errors.New("userinfo: no sub")
	}
	return u, nil
}

// checkEmail returns an error code, or "" when the email may sign in.
// ponytail: a provider that never sends email_verified cannot be used for
// login; add a per-provider options.trust_email when one is needed.
func (h *handler) checkEmail(ctx context.Context, u Userinfo) string {
	at := strings.LastIndexByte(u.Email, '@')
	if at < 1 || !u.verified() {
		return "email_unverified"
	}
	var allowed []string
	_ = h.a.Settings.Get(ctx, "auth.oauth.allowed_domains", &allowed)
	if len(allowed) == 0 {
		return ""
	}
	domain := u.Email[at+1:]
	for _, d := range allowed {
		if strings.EqualFold(strings.TrimPrefix(d, "@"), domain) {
			return ""
		}
	}
	return "domain_not_allowed"
}

var (
	errSignupDisabled = errors.New("sign-up disabled")
	errEmailTaken     = errors.New("email belongs to another account")
)

// resolveUser finds or creates the user for a verified identity and returns
// its id and role. oauth_sub is stored as "<provider>@<sub>".
func (h *handler) resolveUser(ctx context.Context, providerID string, u Userinfo) (id, role string, err error) {
	sub := providerID + "@" + u.Sub
	st := h.a.Settings
	tx, err := h.a.DB.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT id, role FROM "user" WHERE oauth_sub = $1`, sub).Scan(&id, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id, role FROM "user" WHERE lower(email) = lower($1)`, u.Email).Scan(&id, &role)
		switch {
		case err == nil && !st.Bool(ctx, "auth.oauth.merge_by_email"):
			return "", "", errEmailTaken
		case err == nil:
			_, err = tx.Exec(ctx, `UPDATE "user" SET oauth_sub = $2, updated_at = now() WHERE id = $1`, id, sub)
		case errors.Is(err, pgx.ErrNoRows):
			if !st.Bool(ctx, "auth.signup.oauth") {
				return "", "", errSignupDisabled
			}
			id, role, err = h.createUser(ctx, tx, sub, u)
			if err != nil {
				return "", "", err
			}
			return id, role, tx.Commit(ctx)
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("resolve user: %w", err)
	}
	if st.Bool(ctx, "auth.oauth.update_picture") {
		_, err = tx.Exec(ctx, `UPDATE "user" SET name = COALESCE(NULLIF($2, ''), name),
			profile_image_url = COALESCE(NULLIF($3, ''), profile_image_url), updated_at = now() WHERE id = $1`, id, u.Name, u.Picture)
		if err != nil {
			return "", "", fmt.Errorf("refresh profile: %w", err)
		}
	}
	return id, role, tx.Commit(ctx)
}

// createUser inserts a new user; the very first user becomes admin.
func (h *handler) createUser(ctx context.Context, tx pgx.Tx, sub string, u Userinfo) (id, role string, err error) {
	// Serialise sign-ups so two simultaneous first logins cannot both become admin.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kera.signup'))`); err != nil {
		return "", "", fmt.Errorf("signup lock: %w", err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "user")`).Scan(&exists); err != nil {
		return "", "", fmt.Errorf("count users: %w", err)
	}
	role = "admin"
	if exists {
		role = "user"
		var def string
		_ = h.a.Settings.Get(ctx, "auth.signup.default_role", &def)
		if def == "admin" || def == "pending" {
			role = def
		}
	}
	name := u.Name
	if name == "" {
		name = u.Email[:strings.IndexByte(u.Email, '@')]
	}
	id = ids.New()
	_, err = tx.Exec(ctx, `INSERT INTO "user" (id, name, email, role, profile_image_url, oauth_sub) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, name, strings.ToLower(u.Email), role, u.Picture, sub)
	if err != nil {
		return "", "", fmt.Errorf("create user: %w", err)
	}
	slog.Info("user created", "id", id, "role", role)
	return id, role, nil
}

// safeNext keeps only same-origin relative paths (no open redirect).
func safeNext(n string) string {
	if n == "" || n[0] != '/' || strings.HasPrefix(n, "//") || strings.ContainsAny(n, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(n)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return n
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
