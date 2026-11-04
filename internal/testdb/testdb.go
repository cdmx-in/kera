// Package testdb gives each test its own freshly migrated database on the
// server named by KERA_TEST_DATABASE_URL (tests skip when it is unset).
//
//	docker run -d --name kera-test-db -e POSTGRES_USER=kera -e POSTGRES_PASSWORD=kera-test \
//	  -e POSTGRES_DB=kera -p 127.0.0.1:55432:5432 pgvector/pgvector:pg18
//	export KERA_TEST_DATABASE_URL=postgres://kera:kera-test@127.0.0.1:55432/kera?sslmode=disable
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/db"
)

// New creates, migrates and returns a pool on a throwaway database that is
// dropped when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("KERA_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("KERA_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "kera_t_" + hex.EncodeToString(b)
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect admin db: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		conn.Close(ctx)
		t.Fatalf("create db: %v", err)
	}
	conn.Close(ctx)
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return pool
}
