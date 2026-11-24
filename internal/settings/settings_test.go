package settings

import (
	"context"
	"testing"

	"github.com/cdmx-in/kera/internal/testdb"
)

// SetDefault is how an environment variable supplies a setting's default: a stored value wins,
// removing it brings the default back, and another store (another process) is not affected.
func TestSetDefault(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	st := New(pool)
	get := func(s *Store) string {
		t.Helper()
		var v string
		if err := s.Get(ctx, "rag.extractor.url", &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := get(st); got != "" {
		t.Fatalf("built-in default = %q", got)
	}
	st.SetDefault("rag.extractor.url", "http://extract:8000")
	if got := get(st); got != "http://extract:8000" {
		t.Fatalf("default from the environment = %q", got)
	}
	if d := string(st.Default("rag.extractor.url")); d != `"http://extract:8000"` {
		t.Fatalf("Default = %s", d)
	}
	if got := get(New(pool)); got != "" || string(Defaults["rag.extractor.url"]) != `""` {
		t.Fatalf("a default leaked out of its store: %q %s", got, Defaults["rag.extractor.url"])
	}
	// A stored value wins, an empty one too (that is how an admin turns extraction off).
	for _, stored := range []string{"http://other:9000", ""} {
		if err := st.Set(ctx, "rag.extractor.url", stored, false); err != nil {
			t.Fatal(err)
		}
		if got := get(st); got != stored {
			t.Fatalf("stored %q but read %q", stored, got)
		}
	}
	// Reset (delete the row) goes back to the default.
	if _, err := pool.Exec(ctx, `DELETE FROM config WHERE key = 'rag.extractor.url'`); err != nil {
		t.Fatal(err)
	}
	st.Invalidate("rag.extractor.url")
	if got := get(st); got != "http://extract:8000" {
		t.Fatalf("after reset = %q", got)
	}
}
