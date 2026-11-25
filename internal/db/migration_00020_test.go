package db_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/testdb"
)

// 00020 adds the audit log. An event outlives its actor (no foreign key), kinds and outcomes are checked, and
// Down then Up leaves a working table.
func TestMigration00020(t *testing.T) {
	b, err := os.ReadFile("migrations/00020_audit.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, _ := strings.Cut(strings.SplitN(string(b), "-- +goose Up", 2)[1], "-- +goose Down")
	pool := testdb.New(t)
	ctx := context.Background()
	mustExec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	fails := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err == nil {
			t.Errorf("accepted: %s", q)
		}
	}
	mustExec(`INSERT INTO "user" (id, email, role) VALUES ('u1', 'a@example.com', 'admin')`)
	mustExec(`INSERT INTO audit_event (id, actor_id, actor_label, actor_kind, action) VALUES ('e1', 'u1', 'a@example.com', 'user', 'user.create')`)
	mustExec(`DELETE FROM "user" WHERE id = 'u1'`) // the history stays
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_event WHERE actor_id = 'u1' AND outcome = 'ok' AND meta = '{}'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("event of a deleted user: %d %v", n, err)
	}
	mustExec(`INSERT INTO audit_event (id, actor_kind, action) VALUES ('e2', 'anonymous', 'auth.sign_in_failed')`)
	fails(`INSERT INTO audit_event (id, actor_kind, action) VALUES ('e3', 'robot', 'x.y')`)
	fails(`INSERT INTO audit_event (id, actor_kind, action, outcome) VALUES ('e4', 'user', 'x.y', 'maybe')`)

	mustExec(down)
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'audit_event'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit_event is still there after down: %v", err)
	}
	mustExec(up)
	mustExec(`INSERT INTO audit_event (id, actor_kind, action) VALUES ('e1', 'system', 'config.apply')`)
}
