package db_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/testdb"
)

// 00018 adds run_step: kind is one of four values, the optional token columns may be NULL, and
// Down drops the table; Up must work again afterwards.
func TestMigration00018(t *testing.T) {
	b, err := os.ReadFile("migrations/00018_run_step.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, _ := strings.Cut(strings.SplitN(string(b), "-- +goose Up", 2)[1], "-- +goose Down")
	pool := testdb.New(t)
	ctx := context.Background()
	const ins = `INSERT INTO run_step (run_id, message_id, user_id, model_id, seq, kind, started_at) VALUES ('r', 'm', 'u', 'x', 0, $1, now())`
	for _, kind := range []string{"model", "tool", "retrieval", "compaction"} {
		if _, err := pool.Exec(ctx, ins, kind); err != nil {
			t.Fatalf("kind %s: %v", kind, err)
		}
	}
	if _, err := pool.Exec(ctx, ins, "prompt"); err == nil {
		t.Error("an unknown kind was accepted")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM run_step WHERE prompt_tokens IS NULL AND cached_tokens IS NULL AND detail = '{}'`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("defaults: %d rows %v", n, err)
	}
	if _, err := pool.Exec(ctx, down); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'run_step'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the table is still there after down: %v", err)
	}
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
}
