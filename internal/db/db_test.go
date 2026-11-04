package db_test

import (
	"context"
	"testing"

	"github.com/cdmx-in/kera/internal/testdb"
)

// TestSchemaInvariants checks the schema features the app relies on.
func TestSchemaInvariants(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO "user" (id, email) VALUES ('u1', 'A@Example.com')`)
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email) VALUES ('u2', 'a@example.COM')`); err == nil {
		t.Fatal("case-insensitive email uniqueness not enforced")
	}
	// vectors of different dimensions coexist; per-model partial HNSW index works.
	exec(`INSERT INTO embedding_model (id, connection_id, model, dim) VALUES ('e3', 'c', 'm3', 3), ('e2', 'c', 'm2', 2)`)
	exec(`INSERT INTO knowledge (id, user_id, name, fts_config) VALUES ('k1', 'u1', 'K', 'english')`)
	exec(`INSERT INTO file (id, user_id, filename, path) VALUES ('f1', 'u1', 'a.md', '/x')`)
	exec(`INSERT INTO chunk (knowledge_id, file_id, idx, text, fts_config, embedding, embedding_model_id) VALUES
		('k1','f1',0,'running dogs','english','[1,0,0]','e3'), ('k1','f1',1,'cats','english','[0,1]','e2')`)
	exec(`CREATE INDEX chunk_hnsw_e3 ON chunk USING hnsw ((embedding::vector(3)) vector_cosine_ops) WHERE embedding_model_id = 'e3'`)
	var txt string
	if err := pool.QueryRow(ctx, `SELECT text FROM chunk WHERE embedding_model_id = 'e3'
		ORDER BY embedding::vector(3) <=> '[1,0,0]' LIMIT 1`).Scan(&txt); err != nil || txt != "running dogs" {
		t.Fatalf("vector query: %q %v", txt, err)
	}
	// stored tsvector follows the collection's language (english stems "running" -> "run").
	if err := pool.QueryRow(ctx, `SELECT text FROM chunk WHERE tsv @@ websearch_to_tsquery('english', 'run')`).Scan(&txt); err != nil {
		t.Fatalf("fts: %v", err)
	}
}
