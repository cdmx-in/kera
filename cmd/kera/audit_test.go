package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/testdb"
)

// stdoutOf returns what f writes to standard output.
func stdoutOf(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

// kera apply and kera user login-link record as the "Kera command line" actor. The sign-in link's token and
// the secret a config file sets never reach the log, only counts, kinds and names.
func TestAuditApplyAndLoginLink(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	t.Setenv("KERA_DATABASE_URL", "postgres://unused.invalid/kera") // loginLink reads the environment, not the pool's address
	t.Setenv("KERA_SECRET_KEY", strings.Repeat("k", 32))
	t.Setenv("KERA_URL", "https://kera.example.com")
	keys, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}

	const apiKey = "rerank-api-key-value-123"
	f, err := apply.Parse([]byte("site: {name: Test}\nrag:\n  reranker: {url: 'https://rerank.example.com', api_key: "+apiKey+"}\n"), ".", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	res, err := apply.Apply(ctx, pool, keys, nil, f, apply.Options{})
	if err != nil {
		t.Fatal(err)
	}
	recordApply(ctx, pool, res, true, nil)
	recordApply(ctx, pool, nil, false, errors.New("config is invalid"))

	// an empty install: the first user is created as admin; then the link for an existing user
	secrets := []string{apiKey}
	for _, args := range [][]string{{"--ttl", "1h", "Root@Example.com"}, {"root@example.com"}} {
		link := stdoutOf(t, func() {
			if err := loginLink(ctx, pool, args); err != nil {
				t.Fatalf("login-link %v: %v", args, err)
			}
		})
		_, token, _ := strings.Cut(strings.TrimSpace(link), "#token=")
		if len(token) < 20 {
			t.Fatalf("no token in %q", link)
		}
		secrets = append(secrets, token)
	}

	r, err := pool.Query(ctx, `SELECT action || ' ' || outcome || ' ' || actor_kind || ' ' || actor_label || ' ' || coalesce(actor_id, '-') || ' ' ||
		target_type || ' ' || target_label || ' ' || meta::text, row_to_json(a)::text FROM audit_event a ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var got []string
	for r.Next() {
		var short, whole string
		if err := r.Scan(&short, &whole); err != nil {
			t.Fatal(err)
		}
		for _, secretValue := range secrets {
			if strings.Contains(whole, secretValue) {
				t.Fatalf("an audit row contains a secret value: %s", whole)
			}
		}
		got = append(got, short)
	}
	want := []string{
		`config.apply ok system Kera command line - config  {"kinds": ["setting"], "prune": true, "created": 3, "deleted": 0, "updated": 0}`,
		`config.apply failed system Kera command line - config  {"prune": false}`,
		`auth.login_link ok system Kera command line - user root@example.com {"hours": 1}`,
		`auth.login_link ok system Kera command line - user root@example.com {"hours": 12}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
