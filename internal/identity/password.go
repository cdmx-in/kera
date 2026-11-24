package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

// Email and password sign-in. Passwords are set by an admin (Admin > Users, or
// the CLI) or by their owner under Settings; there is no sign-up route.

const (
	emailMax      = 254
	signinBodyMax = 8 << 10
	// Free tries before the first lock (see auth.Attempts), per kind of key:
	//   - an email tried from one address: strict, because that pair is one guesser at one account;
	//   - an email from anywhere: a loose ceiling, so a spray from many addresses still ends but a
	//     couple of strangers cannot lock the real owner out at 5 tries;
	//   - an address: it may be a whole office, and it catches spraying across many emails.
	pairFree   = 5
	emailFree  = 30
	ipFree     = 20
	changeFree = 5 // the current-password guesses of one signed-in user
)

// errBadLogin is the one answer to every failed sign-in: no such user, wrong
// password, no password, disabled, pending.
var errBadLogin = api.Errf(http.StatusUnauthorized, "invalid_credentials", "Invalid email or password")

// jsonBody refuses anything but a JSON body. A page on another site can post a
// form or a text/plain fetch without asking permission first, but not JSON, so
// this keeps other sites from signing a visitor in (or guessing through their browser).
func jsonBody(r *http.Request) error {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return api.Errf(http.StatusUnsupportedMediaType, "json_required", "Send the request as application/json")
	}
	return nil
}

// passwordEnabled says whether password sign-in is on. The setting
// auth.password.enabled decides when it holds a value; when it holds none, the form is on
// for an install without a login provider (the community stack, where it is the
// only way in) and off where an OAuth login provider exists (an SSO-only
// deployment does not grow a password form by upgrading).
func (h *handler) passwordEnabled(ctx context.Context) bool {
	var on *bool
	if err := h.a.Settings.Get(ctx, "auth.password.enabled", &on); err != nil {
		slog.Error("password sign-in stays off: cannot read auth.password.enabled", "err", err)
		return false
	}
	if on != nil {
		return *on
	}
	var sso bool
	if err := h.a.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM oauth_provider WHERE options @> '{"login": true}')`).Scan(&sso); err != nil {
		slog.Error("password sign-in stays off: cannot list login providers", "err", err)
		return false
	}
	return !sso
}

func (h *handler) signin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !h.passwordEnabled(ctx) {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "password_disabled", "Password sign-in is turned off. Use another sign-in method."))
		return
	}
	if err := jsonBody(r); err != nil {
		api.Fail(w, r, err)
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, signinBodyMax)
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	email := clip(strings.ToLower(strings.TrimSpace(body.Email)), emailMax)
	if email == "" || body.Password == "" || len(body.Password) > auth.PasswordMaxBytes || strings.ContainsRune(email, 0) {
		api.Fail(w, r, errBadLogin) // cannot be a real password: no hashing, nothing to count
		return
	}
	ip := api.RealIP(r, h.a.Env.TrustedProxies)
	pair := auth.Key{Kind: "email+ip", Value: email + "\x00" + ip, Free: pairFree}
	byEmail, byIP := auth.Key{Kind: "email", Value: email, Free: emailFree}, auth.Key{Kind: "ip", Value: ip, Free: ipFree}
	if wait, ok := h.attempts.Begin(pair, byEmail, byIP); !ok {
		slog.Debug("password sign-in refused: locked", "email", email, "ip", ip)
		tooMany(w, wait)
		return
	}

	// Every case below runs the same hash, so time does not tell them apart.
	var id, role, hash string
	var disabled bool
	var epoch int64
	err := h.a.DB.QueryRow(ctx, `SELECT u.id, u.role, u.disabled_at IS NOT NULL, u.session_epoch, COALESCE(p.hash, '')
		FROM "user" u LEFT JOIN auth_password p ON p.user_id = u.id WHERE lower(u.email) = lower($1)`, email).
		Scan(&id, &role, &disabled, &epoch, &hash)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		api.Fail(w, r, fmt.Errorf("sign-in lookup: %w", err))
		return
	}
	match, rehash, err := auth.VerifyPassword(ctx, body.Password, hash)
	if err != nil {
		if ctx.Err() != nil {
			return // the client went away while queueing for a hash slot
		}
		slog.Error("password sign-in: unusable stored hash", "user", id, "err", err) // a damaged row: fails like any wrong password
	}
	reason := ""
	switch {
	case !found:
		reason = "no such user"
	case hash == "":
		reason = "user has no password"
	case !match:
		reason = "wrong password"
	case disabled:
		reason = "user is disabled"
	case role == "pending":
		reason = "user is pending approval"
	}
	if reason != "" {
		slog.Warn("password sign-in failed", "email", email, "ip", ip, "reason", reason)
		api.Fail(w, r, errBadLogin)
		return
	}

	// A good sign-in clears the pair, which is one person at one address. The email-wide and
	// address-wide counts are shared with strangers, so a success only gives back its own try
	// (else an attacker's guesses could be wiped by the owner signing in).
	h.attempts.Reset(pair)
	h.attempts.Refund(byEmail)
	h.attempts.Refund(byIP)
	if rehash {
		h.rehash(ctx, id, hash, body.Password)
	}
	u, err := h.a.Auth.LoadUser(ctx, id)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	u.SessionEpoch = epoch // the epoch the password was checked under, even if it moved since
	api.LogUser(ctx, id)
	h.respondSession(w, r, u)
}

// rehash replaces a hash made with older parameters. It changes the row only
// while it still holds the hash that was just checked, so a concurrent password
// change wins; a failure is logged, the sign-in goes on.
func (h *handler) rehash(ctx context.Context, userID, old, password string) {
	fresh, err := auth.HashPassword(ctx, password)
	if err == nil {
		_, err = h.a.DB.Exec(ctx, `UPDATE auth_password SET hash = $2 WHERE user_id = $1 AND hash = $3`, userID, fresh, old)
	}
	if err != nil {
		slog.Warn("password rehash failed", "user", userID, "err", err)
	}
}

// respondSession issues a session for u (same token, cookie and TTL setting as the OAuth
// callback) and answers with the body GET /api/v1/auths/ gives.
func (h *handler) respondSession(w http.ResponseWriter, r *http.Request, u *auth.User) {
	ttl := h.a.Auth.SessionTTL(r.Context())
	tok, err := auth.IssueSession(h.a.Keys, u.ID, u.SessionEpoch, ttl)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	h.a.Auth.SetSessionCookie(w, tok, ttl)
	out := h.meBody(r.Context(), u)
	out["token"], out["token_type"], out["expires_at"] = tok, "Bearer", time.Now().Add(ttl).Unix()
	api.JSON(w, http.StatusOK, out)
}

func (h *handler) changePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := auth.FromContext(ctx)
	if u.ViaAPIKey {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "session_required", "Sign in with the browser to change your password"))
		return
	}
	if !h.passwordEnabled(ctx) {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "password_disabled", "Password sign-in is turned off."))
		return
	}
	if err := jsonBody(r); err != nil {
		api.Fail(w, r, err)
		return
	}
	var body struct {
		Password    string `json:"password"`
		NewPassword string `json:"new_password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, signinBodyMax)
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := auth.CheckPassword(body.NewPassword, u.Email); err != nil { // says nothing about the current password: no attempt counted
		api.Fail(w, r, api.FieldErr("new_password", "bad_password", err.Error()))
		return
	}
	var hash string
	err := h.a.DB.QueryRow(ctx, `SELECT hash FROM auth_password WHERE user_id = $1`, u.ID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "no_password", "This account has no password. Ask an administrator to set one."))
		return
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("load password: %w", err))
		return
	}
	// The current password is a guess an attacker with a stolen session would make: same limits as sign-in.
	ip := api.RealIP(r, h.a.Env.TrustedProxies)
	byUser, byIP := auth.Key{Kind: "change-password", Value: u.ID, Free: changeFree}, auth.Key{Kind: "ip", Value: ip, Free: ipFree}
	if len(body.Password) > auth.PasswordMaxBytes {
		body.Password = "" // cannot match
	}
	if wait, ok := h.attempts.Begin(byUser, byIP); !ok {
		tooMany(w, wait)
		return
	}
	match, _, err := auth.VerifyPassword(ctx, body.Password, hash)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Error("change password: unusable stored hash", "user", u.ID, "err", err)
	}
	if !match {
		slog.Warn("password change refused", "user", u.ID, "email", u.Email, "ip", ip, "reason", "wrong current password")
		api.Fail(w, r, api.FieldErr("password", "wrong_password", "The current password is not right."))
		return
	}
	h.attempts.Reset(byUser)
	h.attempts.Refund(byIP)
	if body.NewPassword == body.Password {
		api.Fail(w, r, api.FieldErr("new_password", "same_password", "Choose a password you have not used just now."))
		return
	}
	epoch, err := storePassword(ctx, h.a.DB, u.ID, body.NewPassword)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	slog.Info("password changed", "user", u.ID, "ip", ip)
	u.SessionEpoch = epoch // this session continues; every other one ends
	ttl := h.a.Auth.SessionTTL(ctx)
	tok, err := auth.IssueSession(h.a.Keys, u.ID, epoch, ttl)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	h.a.Auth.SetSessionCookie(w, tok, ttl)
	api.JSON(w, http.StatusOK, map[string]any{"status": true, "token": tok, "token_type": "Bearer", "expires_at": time.Now().Add(ttl).Unix()})
}

// setUserPassword is the admin's "Set password": it replaces the password,
// ends the user's sessions and clears their sign-in lock. Nothing returns the
// password or its hash.
func (h *handler) setUserPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := auth.FromContext(ctx)
	id := r.PathValue("id")
	if id == me.ID {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "own_password", "Change your own password under Settings > Account"))
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, signinBodyMax)
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	var email string
	err := h.a.DB.QueryRow(ctx, `SELECT email FROM "user" WHERE id = $1`, id).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "User not found"))
		return
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("load user: %w", err))
		return
	}
	if err := auth.CheckPassword(body.Password, email); err != nil {
		api.Fail(w, r, api.FieldErr("password", "bad_password", err.Error()))
		return
	}
	if _, err := storePassword(ctx, h.a.DB, id, body.Password); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.attempts.ResetPrefix("email+ip", strings.ToLower(email)+"\x00") // every address it was tried from
	h.attempts.Reset(auth.Key{Kind: "email", Value: strings.ToLower(email)})
	h.attempts.Reset(auth.Key{Kind: "change-password", Value: id})
	slog.Info("password set by an admin", "admin", me.ID, "user", id)
	api.JSON(w, http.StatusOK, map[string]any{"status": true})
}

// storePassword hashes and saves a password, adds one to the user's session
// epoch and returns it. The hash is made before the transaction opens.
func storePassword(ctx context.Context, db *pgxpool.Pool, userID, password string) (epoch int64, err error) {
	hash, err := auth.HashPassword(ctx, password)
	if err != nil {
		return 0, fmt.Errorf("hash password: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO auth_password (user_id, hash) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET hash = EXCLUDED.hash`, userID, hash); err != nil {
		return 0, fmt.Errorf("store password: %w", err)
	}
	if err := tx.QueryRow(ctx, `UPDATE "user" SET session_epoch = session_epoch + 1, updated_at = now() WHERE id = $1 RETURNING session_epoch`, userID).Scan(&epoch); err != nil {
		return 0, fmt.Errorf("end sessions: %w", err)
	}
	return epoch, tx.Commit(ctx)
}

// addUser is Admin > Users > Add user: an email, a name, a role and optionally
// a first password (without one the user can only sign in through SSO).
func (h *handler) addUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, signinBodyMax)
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	email, err := normalizeEmail(body.Email)
	if err != nil {
		api.Fail(w, r, api.FieldErr("email", "bad_email", err.Error()))
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name, _, _ = strings.Cut(email, "@")
	}
	if utf8.RuneCountInString(name) > 100 {
		api.Fail(w, r, api.FieldErr("name", "bad_name", "The name is too long (100 characters at most)."))
		return
	}
	switch body.Role {
	case "":
		body.Role = "user"
	case "user", "admin", "pending":
	default:
		api.Fail(w, r, api.FieldErr("role", "bad_role", "Role must be admin, user or pending"))
		return
	}
	hash := ""
	if body.Password != "" {
		if err := auth.CheckPassword(body.Password, email); err != nil {
			api.Fail(w, r, api.FieldErr("password", "bad_password", err.Error()))
			return
		}
		if hash, err = auth.HashPassword(ctx, body.Password); err != nil {
			api.Fail(w, r, fmt.Errorf("hash password: %w", err))
			return
		}
	}
	id := ids.New()
	tx, err := h.a.DB.Begin(ctx)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO "user" (id, name, email, role) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, id, name, email, body.Role)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("add user: %w", err))
		return
	}
	if tag.RowsAffected() == 0 {
		api.Fail(w, r, api.Errf(http.StatusConflict, "email_taken", "A user with this email already exists"))
		return
	}
	if hash != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO auth_password (user_id, hash) VALUES ($1, $2)`, id, hash); err != nil {
			api.Fail(w, r, fmt.Errorf("store password: %w", err))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		api.Fail(w, r, err)
		return
	}
	slog.Info("user added by an admin", "admin", auth.FromContext(ctx).ID, "user", id, "role", body.Role, "password", hash != "")
	u, err := h.loadUser(ctx, id)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, u)
}

// normalizeEmail returns the lower-case form of a plain address (no display
// name, no angle brackets), or an error that is fit to show.
func normalizeEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || len(s) > emailMax || strings.HasSuffix(s, "@") {
		return "", errors.New("Enter an email address like name@example.com.")
	}
	return strings.ToLower(s), nil
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tooMany answers 429 with Retry-After and a message a person can act on.
func tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	api.JSON(w, http.StatusTooManyRequests, map[string]any{
		"detail": "Too many attempts. Try again in " + waitText(secs) + ".", "code": "rate_limited", "retry_after": secs,
	})
}

func waitText(secs int) string {
	switch {
	case secs < 60:
		return plural(secs, "second")
	case secs < 3600:
		return plural((secs+59)/60, "minute")
	}
	return plural((secs+3599)/3600, "hour")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
