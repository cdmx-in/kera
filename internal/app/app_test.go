package app

import (
	"context"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/testdb"
)

// KERA_EXTRACTOR_URL becomes the default of rag.extractor.url; without it nothing changes.
func TestExtractorURLDefault(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	for _, want := range []string{"", "http://extract:8000"} {
		a, err := New(ctx, config.Env{DatabaseURL: pool.Config().ConnString(), SecretKey: strings.Repeat("k", 32), PublicURL: "http://localhost:8080", ExtractorURL: want})
		if err != nil {
			t.Fatal(err)
		}
		var got string
		if err := a.Settings.Get(ctx, "rag.extractor.url", &got); err != nil || got != want {
			t.Fatalf("with KERA_EXTRACTOR_URL=%q the setting reads %q (%v)", want, got, err)
		}
		a.Close()
	}
}
