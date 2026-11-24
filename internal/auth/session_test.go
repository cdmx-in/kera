package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

// A token carries the user's session epoch; adding one to the epoch ends every
// older session, and tokens made before epochs existed read as epoch 0.
func TestSessionEpoch(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	keys, _ := secret.Derive(strings.Repeat("k", 32))
	a := New(pool, keys, settings.New(pool), false)
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email, role) VALUES ('u1', 'ann@example.com', 'user')`); err != nil {
		t.Fatal(err)
	}
	resolve := func(tok string) error {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		_, err := a.Resolve(r)
		return err
	}
	legacy, _ := IssueToken(keys, "u1", time.Hour)
	first, _ := IssueSession(keys, "u1", 0, time.Hour)
	if err := resolve(legacy); err != nil {
		t.Fatalf("token without an epoch: %v", err)
	}
	if err := resolve(first); err != nil {
		t.Fatalf("token at epoch 0: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE "user" SET session_epoch = session_epoch + 1 WHERE id = 'u1'`); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"legacy": legacy, "epoch 0": first} {
		if err := resolve(tok); err != ErrUnauthenticated {
			t.Errorf("%s token after the reset: %v", name, err)
		}
	}
	second, _ := IssueSession(keys, "u1", 1, time.Hour)
	if err := resolve(second); err != nil {
		t.Fatalf("token at the new epoch: %v", err)
	}
	if ahead, _ := IssueSession(keys, "u1", 2, time.Hour); resolve(ahead) != ErrUnauthenticated {
		t.Error("a token from a future epoch was accepted")
	}
}
