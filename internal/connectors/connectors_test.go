package connectors

import (
	"context"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

// A real run records each account it created, disabled, enabled or deleted as an audit event with the
// integration as the actor (kind service, no user id); a dry run, other actions and the report's warnings
// leave nothing, and no value from the run's output is stored.
func TestRunRecordsUserChangesAsTheIntegration(t *testing.T) {
	pool := testdb.New(t)
	a := &app.App{DB: pool, Settings: settings.New(pool)}
	ctx := context.Background()
	const leaked = "service-account-token-abc123"
	c := Connector{Name: "directory", Title: "Directory sync", Run: func(context.Context, *app.App, bool) (*Report, error) {
		rep := NewReport()
		rep.Add("create", "new@example.com", "new in the directory")
		rep.Add("disable", "gone@example.com", "suspended in the directory")
		rep.Add("enable", "back@example.com", "active again")
		rep.Add("delete", "old@example.com", "disabled for 30 days")
		rep.Add("join", "new@example.com", "Ops") // a group change: not an account change
		rep.Warn("could not read %s", leaked)
		return rep, nil
	}}
	rows := func() []string {
		t.Helper()
		r, err := pool.Query(ctx, `SELECT action || ' ' || outcome || ' ' || actor_kind || ' ' || actor_label || ' ' || coalesce(actor_id, '-') || ' ' ||
			target_type || ' ' || target_label || ' ' || meta::text, row_to_json(a)::text FROM audit_event a ORDER BY at, id`)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []string
		for r.Next() {
			var short, whole string
			if err := r.Scan(&short, &whole); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(whole, leaked) {
				t.Fatalf("an audit row contains a value from the run: %s", whole)
			}
			out = append(out, short)
		}
		return out
	}

	if _, err := Run(ctx, a, c, "cli", true); err != nil {
		t.Fatal(err)
	}
	if got := rows(); len(got) != 0 {
		t.Fatalf("a dry run recorded %v", got)
	}
	if _, err := Run(ctx, a, c, "cli", false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`user.create ok service Directory sync - user new@example.com {"reason": "new in the directory", "integration": "directory"}`,
		`user.disable ok service Directory sync - user gone@example.com {"reason": "suspended in the directory", "integration": "directory"}`,
		`user.enable ok service Directory sync - user back@example.com {"reason": "active again", "integration": "directory"}`,
		`user.delete ok service Directory sync - user old@example.com {"reason": "disabled for 30 days", "integration": "directory"}`,
	}
	if got := rows(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
