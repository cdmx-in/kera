package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush keeps SSE working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type logKey struct{}

// logInfo is what handlers below the logger add to its line.
type logInfo struct{ user string }

// LogUser names the authenticated user on the request's log line (the auth
// middleware calls it; the logger runs above authentication).
func LogUser(ctx context.Context, userID string) {
	if li, ok := ctx.Value(logKey{}).(*logInfo); ok {
		li.user = userID
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var b [8]byte
		_, _ = rand.Read(b[:])
		rid := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-Id", rid)
		li := &logInfo{}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), logKey{}, li)))
		// the container healthcheck polls /ready every few seconds; only failures are worth a line
		if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/health" || (r.URL.Path == "/ready" && sw.status < 400) {
			return
		}
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "ms", time.Since(start).Milliseconds(),
			"request_id", rid, "user", li.user, "ip", clientIP(r))
	})
}

// clientIP is the address the edge saw. The headers are client-supplied when
// someone reaches the origin port directly, so it is for logs only, never for access.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic", "path", r.URL.Path, "value", v, "stack", string(debug.Stack()))
				JSON(w, http.StatusInternalServerError, map[string]any{"detail": "internal error", "code": "internal"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
