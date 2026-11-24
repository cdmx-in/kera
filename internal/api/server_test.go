package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestLog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		LogUser(r.Context(), "u1") // as the auth middleware does
	}))
	req := httptest.NewRequest("GET", "/api/v1/chats", nil)
	req.Header.Set("CF-Connecting-IP", "203.0.113.7")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line: %v %q", err, buf.String())
	}
	rid := rec.Header().Get("X-Request-Id")
	if rid == "" || line["request_id"] != rid || line["user"] != "u1" || line["ip"] != "203.0.113.7" {
		t.Fatalf("log line %v, X-Request-Id %q", line, rid)
	}
}

func TestRequestLogSkipsHealthyReady(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	status := http.StatusOK
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ready", nil))
	if buf.Len() != 0 {
		t.Fatalf("a healthy /ready was logged: %s", buf.String())
	}
	status = http.StatusServiceUnavailable
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ready", nil))
	if buf.Len() == 0 {
		t.Fatal("a failing /ready must be logged")
	}
}

func TestSecurityHeaders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "sandbox") // as file downloads do
	})
	h := securityHeaders(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	for k, want := range map[string]string{
		"Content-Security-Policy": "frame-ancestors 'self'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/file", nil))
	if got := rec.Header().Values("Content-Security-Policy"); len(got) != 1 || got[0] != "sandbox" {
		t.Errorf("a handler's own CSP must replace the default, got %q", got)
	}
}
