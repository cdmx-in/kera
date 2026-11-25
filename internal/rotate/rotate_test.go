package rotate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

// randHex returns a throwaway value; fixtures are built at run time so no credential-looking literal is committed.
func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func derive(t *testing.T, key string) *secret.Keys {
	t.Helper()
	k, err := secret.Derive(key)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func keyFile(t *testing.T, key, suffix string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, []byte(key+suffix), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustExec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

type world struct {
	db               *pgxpool.Pool
	oldKey, newKey   string
	oldKeys, newKeys *secret.Keys
	file             *apply.File
	tokenPlain       string // the oauth_token row
	envPlain         string // the pipeline_env row named TOKEN
	secretPlain      string // the secret row s1
}

// newWorld fills every location with a throwaway old key: most through kera apply (the real write paths of
// connections, OAuth providers, tool and hook servers and secret settings), the rest by hand.
func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	w := &world{db: testdb.New(t), oldKey: randHex(t, 20), newKey: randHex(t, 20)}
	w.oldKeys, w.newKeys = derive(t, w.oldKey), derive(t, w.newKey)

	mustExec(t, w.db, `INSERT INTO "user" (id, email, role) VALUES ('u1', 'owner@example.com', 'admin')`)
	mustExec(t, w.db, `INSERT INTO knowledge (id, user_id, name) VALUES ('k1', 'u1', 'Handbook')`)
	data, err := os.ReadFile("../apply/testdata/full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, name := range []string{"GOOGLE_SECRET", "GATEWAY_KEY", "JUPYTER_TOKEN", "SHEETS_SECRET", "KB_TOKEN", "PLUGINS_KEY", "DESKTOP_KEY"} {
		env[name] = "throwaway-" + randHex(t, 8)
	}
	env["SPEND_DSN"] = "postgres://u:" + randHex(t, 8) + "@db/spend"
	w.file, err = apply.Parse(data, "../apply/testdata", func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apply.Apply(ctx, w.db, w.oldKeys, settings.New(w.db), w.file, apply.Options{}); err != nil {
		t.Fatal(err)
	}

	w.tokenPlain = `{"access_token":"` + randHex(t, 8) + `"}`
	sealed, err := w.oldKeys.Encrypt([]byte(w.tokenPlain))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, w.db, `INSERT INTO oauth_token (id, user_id, provider, token_enc) VALUES ('t1', 'u1', 'mcp:sheets', $1)`, sealed)
	mustExec(t, w.db, `INSERT INTO pipeline (id, name, user_id) VALUES ('p1', 'nightly', 'u1')`)
	w.envPlain = randHex(t, 8)
	for name, plain := range map[string]string{"TOKEN": w.envPlain, "EMPTY": ""} {
		sealed, err := w.oldKeys.Encrypt([]byte(plain)) // an empty value is a non-empty ciphertext (value_enc is NOT NULL)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, w.db, `INSERT INTO pipeline_env (pipeline_id, name, value_enc, secret) VALUES ('p1', $1, $2, true)`, name, sealed)
	}
	// a stored secret and a variable that points at it (value_enc is NULL there)
	w.secretPlain = randHex(t, 8)
	sealed, err = w.oldKeys.Encrypt([]byte(w.secretPlain))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, w.db, `INSERT INTO secret (id, name, value_enc) VALUES ('s1', 'shared-token', $1)`, sealed)
	mustExec(t, w.db, `INSERT INTO pipeline_env (pipeline_id, name, secret_id, secret) VALUES ('p1', 'REF', 's1', true)`)
	return w
}

// dump is every row of every table that holds ciphertext, as text: two equal dumps mean nothing changed.
func dump(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var tables []string
	for _, l := range locations {
		if !slices.Contains(tables, l.Table) {
			tables = append(tables, l.Table)
		}
	}
	var b strings.Builder
	for _, tb := range tables {
		rows, err := db.Query(context.Background(), "SELECT x::text FROM "+tb+" x ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			b.WriteString(tb + ": " + s + "\n")
		}
		rows.Close()
	}
	return b.String()
}

func export(t *testing.T, w *world, keys *secret.Keys) string {
	t.Helper()
	cfg, _, err := apply.Export(context.Background(), w.db, keys)
	if err != nil {
		t.Fatal(err)
	}
	b, err := apply.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// plains maps "location row" to the decrypted value of every stored secret.
func plains(t *testing.T, w *world, keys *secret.Keys) map[string]string {
	t.Helper()
	items, err := scan(context.Background(), w.db, keys)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, it := range items {
		if it.err != nil {
			t.Fatalf("%s %s: %v", it.loc.Name(), it.row(), it.err)
		}
		out[it.loc.Name()+" "+it.row()] = string(it.plain)
	}
	return out
}

func TestRotateEndToEnd(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)

	// every location holds something, including an empty plaintext in a NOT NULL column
	items, err := scan(ctx, w.db, w.oldKeys)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range locations {
		if !slices.ContainsFunc(items, func(it item) bool { return it.loc.Name() == l.Name() && it.err == nil }) {
			t.Errorf("the fixture stores nothing in %s", l.Name())
		}
	}
	if slices.ContainsFunc(items, func(it item) bool { return it.loc.Table == "pipeline_env" && it.key[1] == "REF" }) {
		t.Error("a pipeline_env row that points at a stored secret was read as a ciphertext")
	}
	if !slices.ContainsFunc(items, func(it item) bool { return it.loc.Table == "pipeline_env" && len(it.plain) == 0 }) {
		t.Error("the fixture has no pipeline_env row with an empty value")
	}
	before := plains(t, w, w.oldKeys)
	exportBefore := export(t, w, w.oldKeys)
	rowsBefore := dump(t, w.db)

	var out bytes.Buffer
	if err := Check(ctx, w.db, w.oldKeys, &out); err != nil || !strings.Contains(out.String(), "all "+strconv.Itoa(len(items))+" stored secrets open") {
		t.Fatalf("check with the old key: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := Check(ctx, w.db, w.newKeys, &out); err == nil || !strings.Contains(out.String(), "FAILED") {
		t.Fatalf("check with the new key before rotating passed: %v\n%s", err, out.String())
	}

	// a dry run reports and writes nothing
	out.Reset()
	if err := Rotate(ctx, w.db, w.oldKeys, w.newKeys, true, &out); err != nil || !strings.Contains(out.String(), "dry run") {
		t.Fatalf("dry run: %v\n%s", err, out.String())
	}
	if dump(t, w.db) != rowsBefore {
		t.Fatal("a dry run changed rows")
	}

	out.Reset()
	if err := Rotate(ctx, w.db, w.oldKeys, w.newKeys, false, &out); err != nil {
		t.Fatalf("rotate: %v\n%s", err, out.String())
	}
	for _, want := range []string{"re-encrypted", "restart", "signed out", "15 minutes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the summary does not say %q:\n%s", want, out.String())
		}
	}
	for _, v := range before {
		if v != "" && strings.Contains(out.String(), v) {
			t.Fatal("the output shows a secret value")
		}
	}
	if dump(t, w.db) == rowsBefore {
		t.Fatal("rotate changed nothing")
	}

	out.Reset()
	if err := Check(ctx, w.db, w.newKeys, &out); err != nil {
		t.Fatalf("check with the new key: %v\n%s", err, out.String())
	}
	if err := Check(ctx, w.db, w.oldKeys, &out); err == nil {
		t.Fatal("the old key still opens everything")
	}
	after := plains(t, w, w.newKeys)
	if len(after) != len(before) {
		t.Fatalf("%d secrets before, %d after", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed its value", k)
		}
	}
	if after["oauth_token.token_enc t1"] != w.tokenPlain || after["pipeline_env.value_enc p1/TOKEN"] != w.envPlain || after["pipeline_env.value_enc p1/EMPTY"] != "" || after["secret.value_enc s1"] != w.secretPlain {
		t.Error("the hand-written rows lost their values")
	}

	// the same config file still matches the database, and everything else about the rows is as it was
	res, err := apply.Apply(ctx, w.db, w.newKeys, settings.New(w.db), w.file, apply.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := res.Count(apply.Create) + res.Count(apply.Update) + res.Count(apply.Delete); n != 0 || len(res.Warnings) != 0 {
		var buf bytes.Buffer
		res.Print(&buf)
		t.Fatalf("apply after rotating: %d changes, warnings %v\n%s", n, res.Warnings, buf.String())
	}
	if export(t, w, w.newKeys) != exportBefore {
		t.Error("the exported config differs after rotating")
	}
	if v, err := apply.ReadSecret(ctx, settings.New(w.db), w.newKeys, "code_interpreter.token"); err != nil || v == "" {
		t.Errorf("code_interpreter.token after rotating: %v", err)
	}
}

// A row that does not open with the old key stops the rotation before anything is written.
func TestRotateStopsOnUndecryptableRow(t *testing.T) {
	ctx := context.Background()
	third := derive(t, randHex(t, 20))
	foreign, err := third.Encrypt([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(foreign)
	for name, tc := range map[string]struct {
		sql  string
		arg  any
		want string
	}{
		"bytea":      {`UPDATE connection SET api_key_enc = $1 WHERE id = 'gateway'`, foreign, "connection.api_key_enc gateway"},
		"json field": {`UPDATE tool_server SET auth = jsonb_set(auth, '{key_enc}', to_jsonb($1::text)) WHERE id = 'kb'`, b64, "tool_server.auth.key_enc kb"},
		"not base64": {`UPDATE tool_server SET auth = jsonb_set(auth, '{key_enc}', to_jsonb($1::text)) WHERE id = 'kb'`, "not base64!", "tool_server.auth.key_enc kb"},
		"setting":    {`UPDATE config SET value = jsonb_build_object('enc', $1::text) WHERE key = 'code_interpreter.token'`, b64, "config.value.enc code_interpreter.token"},
		"composite":  {`UPDATE pipeline_env SET value_enc = $1 WHERE name = 'TOKEN'`, foreign, "pipeline_env.value_enc p1/TOKEN"},
		"too short":  {`UPDATE oauth_token SET token_enc = '\x0102' WHERE id = 't1'`, nil, "oauth_token.token_enc t1"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			if tc.arg == nil {
				mustExec(t, w.db, tc.sql)
			} else {
				mustExec(t, w.db, tc.sql, tc.arg)
			}
			rows := dump(t, w.db)
			var out bytes.Buffer
			err := Rotate(ctx, w.db, w.oldKeys, w.newKeys, false, &out)
			if err == nil || !strings.Contains(out.String(), "FAILED "+tc.want) || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("rotate: %v\n%s", err, out.String())
			}
			if dump(t, w.db) != rows {
				t.Fatal("rotate changed rows although a secret did not open")
			}
			if err := Rotate(ctx, w.db, w.oldKeys, w.newKeys, true, &out); err == nil {
				t.Fatal("a dry run passed")
			}
			out.Reset()
			if err := Check(ctx, w.db, w.oldKeys, &out); err == nil || !strings.Contains(out.String(), "FAILED "+tc.want) {
				t.Fatalf("check: %v\n%s", err, out.String())
			}
		})
	}
}

// A failure after some rows were written takes the whole rotation back.
func TestRotateIsOneTransaction(t *testing.T) {
	w := newWorld(t)
	// pipeline_env is written after connection and the other tables before it
	mustExec(t, w.db, `CREATE FUNCTION refuse() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`)
	mustExec(t, w.db, `CREATE TRIGGER refuse BEFORE UPDATE ON pipeline_env FOR EACH ROW EXECUTE FUNCTION refuse()`)
	rows := dump(t, w.db)
	var out bytes.Buffer
	if err := Rotate(context.Background(), w.db, w.oldKeys, w.newKeys, false, &out); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("rotate: %v\n%s", err, out.String())
	}
	if dump(t, w.db) != rows {
		t.Fatal("rows written before the failure were kept")
	}
	if err := Check(context.Background(), w.db, w.oldKeys, &out); err != nil {
		t.Fatalf("the old key no longer opens everything: %v", err)
	}
}

func TestRotateLock(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	conn, err := w.db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLock); err != nil {
		t.Fatal(err)
	}
	rows := dump(t, w.db)
	var out bytes.Buffer
	if err := Rotate(ctx, w.db, w.oldKeys, w.newKeys, false, &out); err == nil || !strings.Contains(err.Error(), "another rotation") {
		t.Fatalf("a second rotation ran: %v", err)
	}
	if dump(t, w.db) != rows {
		t.Fatal("the refused rotation changed rows")
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, advisoryLock); err != nil {
		t.Fatal(err)
	}
	if err := Rotate(ctx, w.db, w.oldKeys, w.newKeys, false, &out); err != nil {
		t.Fatalf("rotate after the lock was released: %v", err)
	}
}

// The command line: argument parsing, the key files, and the errors that make the process exit 1 (main exits 1
// for any error a command returns).
func TestCommand(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	dbURL := keyFile(t, w.db.Config().ConnString(), "\n") // the URL file form, with the newline an editor leaves
	t.Setenv("KERA_DATABASE_URL", "")
	t.Setenv("KERA_DATABASE_URL_FILE", dbURL)
	t.Setenv("KERA_SECRET_KEY", "")
	oldFile, newFile := keyFile(t, w.oldKey, "\n"), keyFile(t, w.newKey, "")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := Command(ctx, args, &out)
		return out.String(), err
	}
	wantErr := func(args []string, want string) {
		t.Helper()
		if out, err := run(args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("kera secrets %v: error %v, output %q, want %q", args, err, out, want)
		}
	}

	for _, args := range [][]string{
		nil, {"bogus"}, {"check", "extra"}, {"rotate"}, {"rotate", "--old-key-file", oldFile}, {"rotate", "--new-key-file", newFile},
		{"rotate", "--old-key-file", oldFile, "--new-key-file", newFile, "stray"},
	} {
		wantErr(args, "usage:")
	}
	wantErr([]string{"rotate", "--nope"}, "flag provided but not defined")
	wantErr([]string{"rotate", "--old-key-file", oldFile, "--new-key-file", oldFile}, "same key")
	wantErr([]string{"rotate", "--old-key-file", oldFile, "--new-key-file", keyFile(t, w.oldKey, "\r\n")}, "same key")
	short := keyFile(t, "too-short", "\n")
	wantErr([]string{"rotate", "--old-key-file", oldFile, "--new-key-file", short}, "--new-key-file "+short)
	missing := filepath.Join(t.TempDir(), "nope")
	wantErr([]string{"rotate", "--old-key-file", missing, "--new-key-file", newFile}, missing)

	// check needs the key in the environment, like every command that decrypts
	t.Setenv("KERA_SECRET_KEY_FILE", newFile)
	wantErr([]string{"check"}, "do not open")
	t.Setenv("KERA_SECRET_KEY_FILE", oldFile)
	if out, err := run("check"); err != nil || !strings.Contains(out, "stored secrets open") {
		t.Fatalf("check: %v\n%s", err, out)
	}

	rows := dump(t, w.db)
	if out, err := run("rotate", "--dry-run", "--old-key-file", oldFile, "--new-key-file", newFile); err != nil || !strings.Contains(out, "dry run") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if dump(t, w.db) != rows {
		t.Fatal("dry run wrote")
	}
	wantErr([]string{"rotate", "--old-key-file", newFile, "--new-key-file", oldFile}, "do not open with the old key") // the wrong way round
	if dump(t, w.db) != rows {
		t.Fatal("a rotation with the wrong old key wrote")
	}
	if out, err := run("rotate", "--old-key-file="+oldFile, "-new-key-file", newFile); err != nil || !strings.Contains(out, "re-encrypted") {
		t.Fatalf("rotate: %v\n%s", err, out)
	}
	wantErr([]string{"check"}, "do not open") // the environment still has the old key
	t.Setenv("KERA_SECRET_KEY_FILE", newFile)
	if out, err := run("check"); err != nil {
		t.Fatalf("check with the new key: %v\n%s", err, out)
	}
	wantErr([]string{"rotate", "--old-key-file", oldFile, "--new-key-file", newFile}, "do not open with the old key") // already done
}
