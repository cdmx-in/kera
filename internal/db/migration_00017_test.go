package db_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/testdb"
)

// 00017 adds the secret store and lets a pipeline_env row point at a secret instead of holding a value.
// Down drops the rows that only point at a secret and restores NOT NULL; Up must work again afterwards.
func TestMigration00017(t *testing.T) {
	b, err := os.ReadFile("migrations/00017_secret.sql")
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
	mustExec(`INSERT INTO pipeline (id, name, user_id) VALUES ('p1', 'one', 'u1')`)
	mustExec(`INSERT INTO secret (id, name, value_enc) VALUES ('s1', 'a-b.c_d', '\x01')`)
	mustExec(`INSERT INTO pipeline_env (pipeline_id, name, secret_id) VALUES ('p1', 'REF', 's1')`)
	mustExec(`INSERT INTO pipeline_env (pipeline_id, name, value_enc) VALUES ('p1', 'OWN', '\x02')`)
	fails(`INSERT INTO pipeline_env (pipeline_id, name) VALUES ('p1', 'NEITHER')`)
	fails(`DELETE FROM secret WHERE id = 's1'`) // referenced: RESTRICT
	fails(`INSERT INTO secret (id, name, value_enc) VALUES ('s2', 'a-b.c_d', '\x01')`)

	mustExec(down)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_env`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after down %d env rows (want only the one with its own value): %v", n, err)
	}
	fails(`INSERT INTO pipeline_env (pipeline_id, name) VALUES ('p1', 'NULLED')`)
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'secret'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the secret table is still there after down: %v", err)
	}
	mustExec(up)
	mustExec(`INSERT INTO secret (id, name, value_enc) VALUES ('s1', 'x', '\x01')`)
}
