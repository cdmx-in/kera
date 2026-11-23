package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/testdb"
)

func TestOwnsPipeline(t *testing.T) {
	const want = "This user owns a pipeline; change its owner first"
	fk := func(pe pgconn.PgError) error { pe.Code = "23503"; return fmt.Errorf("exec: %w", &pe) }
	for name, err := range map[string]error{
		"constraint": fk(pgconn.PgError{ConstraintName: "pipeline_user_id_fkey", TableName: "user"}),
		"renamed constraint": fk(pgconn.PgError{ConstraintName: "fk_owner",
			Message: `update or delete on table "user" violates foreign key constraint "fk_owner" on table "pipeline"`}),
		"detail":   fk(pgconn.PgError{Detail: `Key (id)=(u1) is still referenced from table "pipeline".`}),
		"table":    fk(pgconn.PgError{TableName: "pipeline"}),
		"restrict": fmt.Errorf("exec: %w", &pgconn.PgError{Code: "23001", ConstraintName: "pipeline_user_id_fkey"}), // ON DELETE RESTRICT
	} {
		var ae *api.Error
		if got := ownsPipeline(err); !errors.As(got, &ae) || ae.Status != http.StatusConflict || ae.Message != want {
			t.Errorf("%s: got %v, want 409 %q", name, got, want)
		}
	}

	other := errors.New("boom")
	for name, err := range map[string]error{
		"another table": fk(pgconn.PgError{ConstraintName: "note_user_id_fkey", TableName: "user",
			Message: `update or delete on table "user" violates foreign key constraint "note_user_id_fkey" on table "note"`}),
		"a pipeline_run table": fk(pgconn.PgError{Detail: `Key (id)=(1) is still referenced from table "pipeline_run".`}),
		"other code":           fmt.Errorf("exec: %w", &pgconn.PgError{Code: "23505", ConstraintName: "pipeline_name_key"}),
		"restrict elsewhere":   fmt.Errorf("exec: %w", &pgconn.PgError{Code: "23001", ConstraintName: "note_user_id_fkey"}),
		"not postgres":         other,
	} {
		if got := ownsPipeline(err); got != err {
			t.Errorf("%s: error changed to %v", name, got)
		}
	}
	if ownsPipeline(nil) != nil {
		t.Error("nil became an error")
	}
}

// The real Postgres error carries the names the mapping reads: a stand-in table
// in its own schema (so it never clashes with the migration's pipeline table)
// references the user the way the real one does.
func TestDeleteUserOwnsPipeline(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO "user" (id, email) VALUES ('u1', 'a@example.com'), ('u2', 'b@example.com'), ('u3', 'c@example.com')`,
		`CREATE SCHEMA scratch`,
		`CREATE TABLE scratch.pipeline (id text PRIMARY KEY, user_id text NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT)`,
		`CREATE TABLE scratch.note (id text PRIMARY KEY, user_id text NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT)`,
		`INSERT INTO scratch.pipeline VALUES ('p1', 'u1')`,
		`INSERT INTO scratch.note VALUES ('n1', 'u2')`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	found, err := DeleteUser(ctx, db, "u1")
	var ae *api.Error
	if found || !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		t.Fatalf("owner of a pipeline: found=%v err=%v", found, err)
	}
	found, err = DeleteUser(ctx, db, "u2")
	if found || err == nil || errors.As(err, &ae) {
		t.Fatalf("user referenced elsewhere must stay a plain error: found=%v err=%v", found, err)
	}
	if found, err = DeleteUser(ctx, db, "u3"); !found || err != nil {
		t.Fatalf("free user: found=%v err=%v", found, err)
	}
}
