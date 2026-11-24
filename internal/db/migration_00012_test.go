package db_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/testdb"
)

// 00012 runs on populated installs: it adds session_epoch (0 for everyone) and pins
// auth.password.enabled to false where an OAuth login provider exists, so the deployment that
// signs in through SSO does not grow a password form if the provider is ever removed.
func TestMigration00012(t *testing.T) {
	b, err := os.ReadFile("migrations/00012_session_epoch.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.SplitN(strings.SplitN(string(b), "-- +goose Down", 2)[0], "-- +goose Up", 2)[1]

	// before runs the state this deployment is in when the migration arrives.
	run := func(t *testing.T, before ...string) (*pgxpool.Pool, string) {
		t.Helper()
		pool := testdb.New(t)
		ctx := context.Background()
		for _, q := range append([]string{
			`ALTER TABLE "user" DROP COLUMN session_epoch`, // undo what testdb already migrated
			`DELETE FROM config WHERE key = 'auth.password.enabled'`,
			`INSERT INTO "user" (id, email, role) SELECT 'u' || g, 'u' || g || '@example.com', 'user' FROM generate_series(1, 500) g`,
		}, before...) {
			if _, err := pool.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if _, err := pool.Exec(ctx, up); err != nil {
			t.Fatalf("migration: %v", err)
		}
		var n, zero int
		if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE session_epoch = 0) FROM "user"`).Scan(&n, &zero); err != nil || n != 500 || zero != 500 {
			t.Fatalf("users %d, epoch 0 for %d: %v", n, zero, err)
		}
		v := "(unset)"
		_ = pool.QueryRow(ctx, `SELECT value::text FROM config WHERE key = 'auth.password.enabled'`).Scan(&v)
		return pool, v
	}
	login := `INSERT INTO oauth_provider (id, options) VALUES ('sso', '{"login": true}')`

	if _, v := run(t, login); v != "false" {
		t.Errorf("an SSO deployment: auth.password.enabled = %s, want false", v)
	}
	if _, v := run(t); v != "(unset)" {
		t.Errorf("an install with no provider: %s, want it left unset (the form stays on)", v)
	}
	if _, v := run(t, `INSERT INTO oauth_provider (id, options) VALUES ('sheets', '{}')`); v != "(unset)" {
		t.Errorf("a tool-server provider only: %s, want unset", v)
	}
	if _, v := run(t, login, `INSERT INTO config (key, value) VALUES ('auth.password.enabled', 'true')`); v != "true" {
		t.Errorf("an admin's explicit true: %s, want it kept", v)
	}
}
