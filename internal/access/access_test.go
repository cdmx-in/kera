package access_test

import (
	"context"
	"testing"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/testdb"
)

func TestGrants(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	alice := &auth.User{ID: "alice", Role: "user", Groups: []string{"g-fin"}}
	bob := &auth.User{ID: "bob", Role: "user"}
	admin := &auth.User{ID: "root", Role: "admin"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(access.Replace(ctx, db, access.Model, "finance-desk", []access.Grant{{Principal: "group:g-fin"}}, true))
	must(access.Replace(ctx, db, access.Model, "kera", []access.Grant{{Principal: "*"}}, true))
	must(access.Replace(ctx, db, access.Model, "notes", []access.Grant{{Principal: "user:bob", Permission: access.Write}}, false))

	check := func(u *auth.User, id, perm string, want bool) {
		t.Helper()
		got, err := access.Can(ctx, db, u, access.Model, id, perm)
		must(err)
		if got != want {
			t.Errorf("%s %s %s = %v, want %v", u.ID, perm, id, got, want)
		}
	}
	check(alice, "finance-desk", access.Read, true)
	check(bob, "finance-desk", access.Read, false)
	check(bob, "kera", access.Read, true)
	check(bob, "kera", access.Write, false)
	check(bob, "notes", access.Read, true) // write implies read
	check(admin, "anything", access.Write, true)

	ids, all, err := access.IDs(ctx, db, alice, access.Model, access.Read)
	must(err)
	if all || len(ids) != 2 {
		t.Fatalf("alice ids=%v all=%v", ids, all)
	}
	// a managed replace keeps hand-made grants
	must(access.Replace(ctx, db, access.Model, "notes", nil, true))
	check(bob, "notes", access.Read, true)
	if _, _, err := access.ParsePrincipal("team:x"); err == nil {
		t.Fatal("bad principal accepted")
	}
}
