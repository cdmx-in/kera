package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

func deny(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"detail": msg, "code": code})
}

// Optional attaches the user when credentials are valid and continues either way.
func (a *Authenticator) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, err := a.Resolve(r); err == nil {
			r = r.WithContext(WithUser(r.Context(), u))
		}
		next.ServeHTTP(w, r)
	})
}

// Required rejects unauthenticated requests with 401.
func (a *Authenticator) Required(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := a.Resolve(r)
		if err != nil {
			if !errors.Is(err, ErrUnauthenticated) {
				slog.Error("auth", "err", err)
			}
			deny(w, http.StatusUnauthorized, "unauthenticated", "Not authenticated")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

// Admin requires an authenticated admin.
func (a *Authenticator) Admin(next http.Handler) http.Handler {
	return a.Required(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !FromContext(r.Context()).IsAdmin() {
			deny(w, http.StatusForbidden, "forbidden", "Admin access required")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// Permission requires an authenticated user with the dotted permission.
func (a *Authenticator) Permission(path string, next http.Handler) http.Handler {
	return a.Required(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !FromContext(r.Context()).Can(path) {
			deny(w, http.StatusForbidden, "forbidden", "Permission required: "+path)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
