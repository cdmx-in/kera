package db_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/testdb"
)

// Migration 00012 pins auth.password.enabled to false on a deployment that already has an OAuth
// login provider and no value, so upgrading changes nothing for it; a fresh install, a tool-server
// provider and a value an admin set are left as they are.
func TestMigration12PinsPasswordSignIn(t *testing.T) {
	up := func() string {
		b, err := os.ReadFile("migrations/00012_session_epoch.sql")
		if err != nil {
			t.Fatal(err)
		}
		return strings.SplitN(strings.SplitN(string(b), "-- +goose Down", 2)[0], "-- +goose Up", 2)[1]
	}()
	for _, tc := range []struct {
		name, setup, want string
	}{
		{"SSO deployment, no value", `INSERT INTO oauth_provider (id, options) VALUES ('google', '{"login": true}')`, "false"},
		{"fresh install", ``, ""},
		{"tool-server provider only", `INSERT INTO oauth_provider (id, options) VALUES ('mcp:sheets', '{}')`, ""},
		{"login provider and an explicit true", `INSERT INTO oauth_provider (id, options) VALUES ('google', '{"login": true}');
			INSERT INTO config (key, value) VALUES ('auth.password.enabled', 'true')`, "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := testdb.New(t)
			ctx := context.Background()
			// Put the database back where it was before 00012, with some users in it.
			for _, q := range []string{`ALTER TABLE "user" DROP COLUMN session_epoch`,
				`INSERT INTO "user" (id, email, role) SELECT 'u'||g, 'u'||g||'@example.com', 'user' FROM generate_series(1, 300) g`, tc.setup, up} {
				if q == "" {
					continue
				}
				if _, err := pool.Exec(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			var got string
			_ = pool.QueryRow(ctx, `SELECT value::text FROM config WHERE key = 'auth.password.enabled'`).Scan(&got)
			if got != tc.want {
				t.Fatalf("auth.password.enabled = %q, want %q", got, tc.want)
			}
			var users, zero int
			if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE session_epoch = 0) FROM "user"`).Scan(&users, &zero); err != nil || users != zero {
				t.Fatalf("users %d, with epoch 0: %d (%v)", users, zero, err)
			}
		})
	}
}
