// Package auth authenticates requests (session JWT in a Bearer header or the
// kera_session cookie, or an "sk-" API key) and loads the user with merged
// permissions.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
)

// CookieName is the HttpOnly session cookie (lets <img>/<iframe> fetch files).
const CookieName = "kera_session"

// Authenticator resolves credentials to users.
type Authenticator struct {
	DB       *pgxpool.Pool
	Keys     *secret.Keys
	Settings *settings.Store
	Secure   bool // set Secure on cookies (public URL is https)

	mu     sync.Mutex
	seenAt map[string]time.Time // throttles last_active_at writes
}

// New builds an Authenticator.
func New(db *pgxpool.Pool, keys *secret.Keys, st *settings.Store, secure bool) *Authenticator {
	return &Authenticator{DB: db, Keys: keys, Settings: st, Secure: secure, seenAt: map[string]time.Time{}}
}

// ErrUnauthenticated means no valid credential was presented.
var ErrUnauthenticated = errors.New("not authenticated")

// Credential extracts the bearer value or cookie from r.
func Credential(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if v, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(v)
		}
	}
	// Browsers attach the cookie on their own, so an unsafe (non-GET) request
	// may use it only when it comes from our own origin (CSRF).
	if c, err := r.Cookie(CookieName); err == nil && sameOrigin.Check(r) == nil {
		return c.Value
	}
	return ""
}

var sameOrigin = http.NewCrossOriginProtection()

// Resolve authenticates r. It returns ErrUnauthenticated when no valid credential is present.
func (a *Authenticator) Resolve(r *http.Request) (*User, error) {
	cred := Credential(r)
	if cred == "" {
		return nil, ErrUnauthenticated
	}
	ctx := r.Context()
	var userID string
	viaKey := IsAPIKey(cred)
	if viaKey {
		err := a.DB.QueryRow(ctx, `UPDATE api_key SET last_used_at = now() WHERE key_hash = $1 RETURNING user_id`,
			HashAPIKey(cred)).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnauthenticated
		}
		if err != nil {
			return nil, fmt.Errorf("api key lookup: %w", err)
		}
	} else {
		c, err := VerifyToken(a.Keys, cred)
		if err != nil {
			return nil, ErrUnauthenticated
		}
		userID = c.Sub
	}
	u, err := a.LoadUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	u.ViaAPIKey = viaKey
	if viaKey && !u.Can("features.api_keys") {
		return nil, ErrUnauthenticated
	}
	a.touch(ctx, u.ID)
	return u, nil
}

// LoadUser reads a user, their groups and merged permissions.
func (a *Authenticator) LoadUser(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := a.DB.QueryRow(ctx, `SELECT id, name, email, role, profile_image_url FROM "user" WHERE id = $1`, id).
		Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.ProfileImageURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	if u.Role == "pending" {
		return nil, ErrUnauthenticated
	}
	rows, err := a.DB.Query(ctx, `SELECT g.id, g.permissions FROM "group" g JOIN group_member m ON m.group_id = g.id WHERE m.user_id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("load groups: %w", err)
	}
	defer rows.Close()
	var overlays []Permissions
	for rows.Next() {
		var gid string
		var raw []byte
		if err := rows.Scan(&gid, &raw); err != nil {
			return nil, err
		}
		u.Groups = append(u.Groups, gid)
		overlays = append(overlays, ParsePermissions(raw))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	base, err := a.Settings.Raw(ctx, "user.permissions")
	if err != nil {
		return nil, err
	}
	u.Permissions = Merge(ParsePermissions(base), overlays...)
	return u, nil
}

func (a *Authenticator) touch(ctx context.Context, id string) {
	a.mu.Lock()
	last := a.seenAt[id]
	now := time.Now()
	if now.Sub(last) < time.Minute {
		a.mu.Unlock()
		return
	}
	a.seenAt[id] = now
	a.mu.Unlock()
	_, _ = a.DB.Exec(context.WithoutCancel(ctx), `UPDATE "user" SET last_active_at = now() WHERE id = $1`, id)
}

// SetSessionCookie issues the HttpOnly cookie for token.
func (a *Authenticator) SetSessionCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: a.Secure, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds())})
}

// ClearSessionCookie removes the cookie.
func (a *Authenticator) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: a.Secure, MaxAge: -1})
}

// SessionTTL reads auth.jwt_expiry_hours.
func (a *Authenticator) SessionTTL(ctx context.Context) time.Duration {
	h := 720
	_ = a.Settings.Get(ctx, "auth.jwt_expiry_hours", &h)
	if h <= 0 {
		h = 720
	}
	return time.Duration(h) * time.Hour
}
