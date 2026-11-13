package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	ready := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" || !ready {
			http.Error(w, `{"ready":false}`, http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	port := srv.Listener.Addr().String()[strings.LastIndex(srv.Listener.Addr().String(), ":"):]

	if err := health(port); err != nil { // ":PORT", as KERA_LISTEN is usually set
		t.Fatalf("ready server reported unhealthy: %v", err)
	}
	ready = false
	if err := health(port); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("unready server: %v", err)
	}
	if err := health("8080"); err == nil {
		t.Fatal("a listen address without a port separator must be an error")
	}
}
