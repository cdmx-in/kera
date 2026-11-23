package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/cdmx-in/kera/internal/testdb"
)

// TestPipelinesSchema checks the constraints the pipeline code relies on and
// that Down removes staged versions before it drops their columns.
func TestPipelinesSchema(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) error {
		_, err := pool.Exec(ctx, sql, args...)
		return err
	}
	must := func(sql string, args ...any) {
		t.Helper()
		if err := exec(sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO "user" (id, email) VALUES ('u1', 'u1@example.com')`)
	must(`INSERT INTO pipeline (id, name, user_id) VALUES ('p1', 'docs-sync', 'u1')`)
	// a pipeline's owner cannot be deleted
	if exec(`DELETE FROM "user" WHERE id = 'u1'`) == nil {
		t.Fatal("deleted the owner of a pipeline")
	}
	// one open run per pipeline, any number of closed ones
	must(`INSERT INTO pipeline_run (pipeline_id) VALUES ('p1')`)
	if exec(`INSERT INTO pipeline_run (pipeline_id) VALUES ('p1')`) == nil {
		t.Fatal("two open runs")
	}
	must(`UPDATE pipeline_run SET finished_at = now(), status = 'ok'`)
	must(`INSERT INTO pipeline_run (pipeline_id) VALUES ('p1')`)
	if exec(`UPDATE pipeline_run SET status = 'bogus'`) == nil {
		t.Fatal("unknown run status")
	}
	// a script step and a Kera step may share a key; two of one kind may not
	must(`INSERT INTO pipeline_step (run_id, seq, kera, key, name) SELECT id, 1, false, 'k1', 'k1' FROM pipeline_run WHERE finished_at IS NULL`)
	must(`INSERT INTO pipeline_step (run_id, seq, kera, key, name) SELECT id, 2, true, 'k1', 'Docs' FROM pipeline_run WHERE finished_at IS NULL`)
	if exec(`INSERT INTO pipeline_step (run_id, seq, kera, key, name) SELECT id, 3, true, 'k1', 'Docs again' FROM pipeline_run WHERE finished_at IS NULL`) == nil {
		t.Fatal("two Kera steps for one collection")
	}
	// deleting a pipeline takes its runs and steps along
	must(`DELETE FROM pipeline WHERE id = 'p1'`)
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM pipeline_run) + (SELECT count(*) FROM pipeline_step)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("runs left: %d %v", n, err)
	}

	// Down: staged versions (and their files) go, live rows stay.
	must(`INSERT INTO knowledge (id, user_id, name) VALUES ('k1', 'u1', 'K')`)
	must(`INSERT INTO file (id, user_id, filename, path) VALUES ('old', 'u1', 'a.md', 'x'), ('new', 'u1', 'a.md', 'y')`)
	must(`INSERT INTO knowledge_file (knowledge_id, file_id, origin) VALUES ('k1', 'old', 'pipeline:p1')`)
	must(`INSERT INTO knowledge_file (knowledge_id, file_id, origin, replaces, sync_run) VALUES ('k1', 'new', 'pipeline:p1', 'old', 7)`)

	sqldb := stdlib.OpenDBFromPool(pool)
	defer sqldb.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, sqldb, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 10); err != nil {
		t.Fatalf("down: %v", err)
	}
	var live, files, cols, tables int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM knowledge_file), (SELECT count(*) FROM file),
		(SELECT count(*) FROM information_schema.columns WHERE table_name = 'knowledge_file' AND column_name IN ('replaces', 'sync_run')),
		(SELECT count(*) FROM information_schema.tables WHERE table_name LIKE 'pipeline%')`).Scan(&live, &files, &cols, &tables); err != nil {
		t.Fatal(err)
	}
	if live != 1 || files != 1 || cols != 0 || tables != 0 {
		t.Fatalf("after down: %d rows, %d files, %d columns, %d tables", live, files, cols, tables)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
