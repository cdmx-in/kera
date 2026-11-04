package websearch

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestInternet searches DuckDuckGo and reads a page for real (KERA_TEST_INTERNET=1).
func TestInternet(t *testing.T) {
	if os.Getenv("KERA_TEST_INTERNET") == "" {
		t.Skip("KERA_TEST_INTERNET not set")
	}
	ctx := context.Background()
	e := DefaultEngines(&http.Client{Timeout: 20 * time.Second})
	hits, err := e.Search(ctx, EngineConfig{Engine: "duckduckgo"}, "golang net/http package documentation", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("search: %v %v", hits, err)
	}
	for _, h := range hits {
		t.Logf("hit: %s | %s | %.60s", h.Title, h.Link, h.Snippet)
	}
	p, err := Fetch(ctx, NewClient(20*time.Second, false), "https://go.dev/doc/effective_go")
	if err != nil || !strings.Contains(p.Text, "gofmt") {
		t.Fatalf("fetch: %v %.200s", err, p.Text)
	}
	t.Logf("fetched %q: %d chars: %.300s", p.Title, len(p.Text), p.Text)
	ic, b := NewFavicons(NewClient(10*time.Second, false)).fetch(ctx, "go.dev")
	if ic == "" || len(b) == 0 {
		t.Fatal("no favicon for go.dev")
	}
	t.Logf("favicon go.dev: %s %d bytes", ic, len(b))
}
