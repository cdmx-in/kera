// Package api wires HTTP routes. Feature packages register their handlers on
// the shared mux through Server.Handle so this file stays a thin index.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/db"
	"github.com/cdmx-in/kera/internal/version"
)

// Server holds shared dependencies for handlers.
type Server struct {
	Env config.Env
	DB  *pgxpool.Pool
	mux *http.ServeMux
	spa *spa
}

// New builds the server with core routes; feature packages add theirs via Handle.
func New(env config.Env, pool *pgxpool.Pool) (*Server, error) {
	s := &Server{Env: env, DB: pool, mux: http.NewServeMux()}
	sp, err := newSPA(env)
	if err != nil {
		return nil, err
	}
	s.spa = sp
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]any{"status": true})
	})
	s.mux.HandleFunc("GET /ready", s.ready)
	s.mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]any{"version": version.Version})
	})
	s.mux.Handle("/", s.spa)
	return s, nil
}

// Handle registers a route pattern (Go 1.22 syntax, e.g. "GET /api/v1/chats/{id}").
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// HandleFunc registers a handler function.
func (s *Server) HandleFunc(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.DB.Ping(ctx); err != nil {
		JSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "reason": "database unreachable"})
		return
	}
	if pending, err := db.Pending(ctx, s.DB); err != nil || pending {
		JSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "reason": "migrations pending"})
		return
	}
	JSON(w, http.StatusOK, map[string]any{"ready": true})
}

// Handler returns the root handler with logging, panic recovery and the
// default security headers.
func (s *Server) Handler() http.Handler { return recoverer(logRequests(securityHeaders(s.mux))) }

// securityHeaders sets defaults before the route runs; a handler that needs a
// stricter policy (file downloads: CSP sandbox) replaces it with Header().Set.
// frame-ancestors only, not a full CSP: the index page has inline scripts.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "frame-ancestors 'self'") // no clickjacking; the PDF preview is same-origin
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// ListenAndServe serves until ctx is cancelled, then drains for up to 30 s.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Env.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: chat responses stream for minutes.
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("kera listening", "addr", s.Env.Listen, "url", s.Env.PublicURL, "version", version.Version)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
