package auth

import "context"

// User is the authenticated principal for a request.
type User struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Email           string      `json:"email"`
	Role            string      `json:"role"`
	ProfileImageURL string      `json:"profile_image_url"`
	Groups          []string    `json:"groups"` // group ids
	Permissions     Permissions `json:"permissions"`
	ViaAPIKey       bool        `json:"-"`
	SessionEpoch    int64       `json:"-"` // tokens must carry this number; a password change adds one
}

// IsAdmin reports the admin role.
func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

// Can reports a permission; admins can do everything.
func (u *User) Can(path string) bool { return u.IsAdmin() || u.Permissions.Allowed(path) }

type ctxKey struct{}

// WithUser stores u in ctx.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// FromContext returns the request user or nil.
func FromContext(ctx context.Context) *User { u, _ := ctx.Value(ctxKey{}).(*User); return u }
