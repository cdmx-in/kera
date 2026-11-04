package apply

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

var testEnv = map[string]string{
	"GOOGLE_SECRET": "google-secret-value",
	"GATEWAY_KEY":   "gateway-key-value",
	"JUPYTER_TOKEN": "jupyter-token-value",
	"SHEETS_SECRET": "sheets-secret-value",
	"KB_TOKEN":      "kb-token-value",
	"PLUGINS_KEY":   "plugins-key-value",
	"SPEND_DSN":     "postgres://spend:spend-pass-value@db/spend",
	"DESKTOP_KEY":   "desktop-key-value",
}

func lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func loadFixture(t *testing.T, env map[string]string) *File {
	t.Helper()
	data, err := os.ReadFile("testdata/full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(data, "testdata", lookup(env))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return f
}

type fixture struct {
	db   *pgxpool.Pool
	keys *secret.Keys
	st   *settings.Store
}

func setup(t *testing.T) fixture {
	t.Helper()
	db := testdb.New(t)
	keys, err := secret.Derive(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	// a collection the file grants on (collections come from uploads, not apply)
	mustExec(t, db, `INSERT INTO "user" (id, email, role) VALUES ('u1', 'owner@example.com', 'admin')`)
	mustExec(t, db, `INSERT INTO knowledge (id, user_id, name) VALUES ('k1', 'u1', 'Handbook')`)
	return fixture{db: db, keys: keys, st: settings.New(db)}
}

func mustExec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func count(t *testing.T, db *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func mustApply(t *testing.T, fx fixture, f *File, opt Options) *Result {
	t.Helper()
	res, err := Apply(context.Background(), fx.db, fx.keys, fx.st, f, opt)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return res
}

func assertNoChanges(t *testing.T, res *Result) {
	t.Helper()
	if n := res.Count(Create) + res.Count(Update) + res.Count(Delete); n != 0 {
		var buf bytes.Buffer
		res.Print(&buf)
		t.Fatalf("expected no changes, got %d:\n%s", n, buf.String())
	}
}

func TestApplyFullAndIdempotent(t *testing.T) {
	fx := setup(t)
	ctx := context.Background()
	res := mustApply(t, fx, loadFixture(t, testEnv), Options{})
	if res.Count(Update)+res.Count(Delete)+res.Count(Unchanged) != 0 || res.Count(Create) == 0 {
		t.Fatalf("fresh apply should only create: %+v", res.Changes)
	}
	for kind, want := range map[string]int{"group": 2, "connection": 3, "oauth_provider": 2, "tool_server": 3,
		"hook_server": 1, "model": 3, "knowledge": 1, "skill": 1} {
		got := 0
		for _, c := range res.Changes {
			if c.Kind == kind {
				got++
			}
		}
		if got != want {
			t.Errorf("%s: %d changes, want %d", kind, got, want)
		}
	}

	// every row is managed
	for _, table := range []string{"config", `"group"`, "connection", "oauth_provider", "tool_server", "hook_server", "model", "access_grant", "skill"} {
		if n := count(t, fx.db, "SELECT count(*) FROM "+table+" WHERE NOT managed"); n != 0 {
			t.Errorf("%s: %d unmanaged rows", table, n)
		}
	}

	// prompt files are read relative to the config file
	var prompt, base string
	if err := fx.db.QueryRow(ctx, `SELECT system_prompt, base_model_id FROM model WHERE id = 'assistant'`).Scan(&prompt, &base); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prompt, "You are a helpful assistant.") || base != "web.big-model" {
		t.Errorf("assistant prompt/base = %q / %q", prompt, base)
	}
	// skills: read relative to the config file, frontmatter folded, resources kept, group grant
	if n := count(t, fx.db, `SELECT count(*) FROM skill s JOIN access_grant a ON a.resource_type = 'skill' AND a.resource_id = s.id
		WHERE s.name = 'demo-skill' AND s.description = 'Shows how a skill is laid out. Use it in tests.'
		AND s.resources->>'refs/notes.md' = 'Some notes.' || chr(10)`); n != 1 {
		t.Errorf("demo-skill rows with grant = %d, want 1", n)
	}
	// group grants resolve names to ids
	if n := count(t, fx.db, `SELECT count(*) FROM access_grant a JOIN "group" g ON g.id = a.principal_id
		WHERE a.resource_type = 'tool_server' AND a.resource_id = 'search'`); n != 2 {
		t.Errorf("search tool server group grants = %d, want 2", n)
	}
	if n := count(t, fx.db, `SELECT count(*) FROM access_grant WHERE resource_type = 'knowledge' AND resource_id = 'k1'`); n != 2 {
		t.Errorf("knowledge grants = %d, want 2", n)
	}
	// settings land under the documented keys, merged permissions keep defaults
	var name string
	if err := fx.st.Get(ctx, "site.name", &name); err != nil || name != "Example Chat" {
		t.Errorf("site.name = %q, %v", name, err)
	}
	var iters int
	_ = fx.st.Get(ctx, "chat.max_tool_iterations", &iters)
	if iters != 256 {
		t.Errorf("chat.max_tool_iterations = %d (env default not applied)", iters)
	}
	var perms Permissions
	_ = fx.st.Get(ctx, "user.permissions", &perms)
	if !perms["features"]["api_keys"] || !perms["chat"]["share"] || perms["workspace"]["tools"] {
		t.Errorf("user.permissions not merged with defaults: %v", perms)
	}
	var coworkPrompt string
	_ = fx.st.Get(ctx, "cowork.prompt", &coworkPrompt)
	_ = fx.st.Get(ctx, "cowork.max_tool_iterations", &iters)
	if coworkPrompt != "Work on the goal on your own." || iters != 80 {
		t.Errorf("cowork = %q / %d", coworkPrompt, iters)
	}
	var tpl string
	_ = fx.st.Get(ctx, "rag.template", &tpl)
	if !strings.Contains(tpl, "{{CONTEXT}}") {
		t.Errorf("rag.template = %q", tpl)
	}
	var langs map[string]CodeLanguage
	_ = fx.st.Get(ctx, "code_interpreter.languages", &langs)
	if len(langs) != 3 || langs["php"].Command[1] != "{file}" || langs["typescript"].Fallback[0] != "tsx" || langs["typescript"].Check[3] != "" {
		t.Errorf("code_interpreter.languages = %+v", langs)
	}
	if tok, err := ReadSecret(ctx, fx.st, fx.keys, "code_interpreter.token"); err != nil || tok != testEnv["JUPYTER_TOKEN"] {
		t.Errorf("ReadSecret token mismatch (err %v)", err)
	}
	var login bool
	_ = fx.db.QueryRow(ctx, `SELECT (options->>'login')::boolean FROM oauth_provider WHERE id = 'google'`).Scan(&login)
	if !login {
		t.Error("google provider is not a login provider")
	}

	// second run: nothing to do (secrets compared decrypted, not as ciphertexts)
	assertNoChanges(t, mustApply(t, fx, loadFixture(t, testEnv), Options{}))
}

func TestDryRunWritesNothing(t *testing.T) {
	fx := setup(t)
	res := mustApply(t, fx, loadFixture(t, testEnv), Options{DryRun: true, Prune: true})
	if res.Count(Create) == 0 || !res.DryRun {
		t.Fatalf("dry run plan is empty: %+v", res)
	}
	for _, table := range []string{"config", `"group"`, "connection", "oauth_provider", "tool_server", "hook_server", "model", "access_grant"} {
		if n := count(t, fx.db, "SELECT count(*) FROM "+table); n != 0 {
			t.Errorf("dry run wrote %d rows to %s", n, table)
		}
	}
	var buf bytes.Buffer
	res.Print(&buf)
	if !strings.Contains(buf.String(), "create     model          assistant") || !strings.Contains(buf.String(), "dry run") {
		t.Errorf("plan output:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "value") && strings.Contains(buf.String(), testEnv["GATEWAY_KEY"]) {
		t.Error("plan output contains a secret")
	}
}

func TestPrune(t *testing.T) {
	fx := setup(t)
	mustApply(t, fx, loadFixture(t, testEnv), Options{})
	// a hand-made model and a member in group Ops
	mustExec(t, fx.db, `INSERT INTO model (id, name) VALUES ('handmade', 'Handmade')`)
	mustExec(t, fx.db, `INSERT INTO group_member (group_id, user_id) SELECT id, 'u1' FROM "group" WHERE name = 'Ops'`)

	f := loadFixture(t, testEnv)
	f.Models = f.Models[:1]       // drop researcher and local-model:8b
	f.Groups = []Group{}          // declared but empty: Finance goes, Ops has a member and stays
	f.ToolServers[1].Grants = nil // search no longer references the groups
	f.Knowledge[0].Grants = f.Knowledge[0].Grants[:1]
	other := t.TempDir()
	if err := os.MkdirAll(other+"/other", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other+"/other/SKILL.md", []byte("---\nname: other\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.Skills.Dir = other
	f.HookServers = nil // not declared: kept even with --prune

	// without --prune nothing is deleted
	res := mustApply(t, fx, f, Options{DryRun: true})
	if res.Count(Delete) != 0 {
		t.Fatalf("deletes without --prune: %+v", res.Changes)
	}
	res = mustApply(t, fx, f, Options{Prune: true})
	deleted := map[string]bool{}
	for _, c := range res.Changes {
		if c.Action == Delete {
			deleted[c.Kind+" "+c.ID] = true
		}
	}
	for _, want := range []string{"model researcher", "model local-model:8b", "group Finance", "skill demo-skill"} {
		if !deleted[want] {
			t.Errorf("not deleted: %s (got %v)", want, deleted)
		}
	}
	if n := count(t, fx.db, `SELECT count(*) FROM model WHERE id IN ('researcher', 'local-model:8b')`); n != 0 {
		t.Error("pruned models still present")
	}
	if n := count(t, fx.db, `SELECT count(*) FROM model WHERE id = 'handmade'`); n != 1 {
		t.Error("prune removed an unmanaged model")
	}
	if deleted["group Ops"] {
		t.Error("plan lists a delete for a group that is kept")
	}
	if n := count(t, fx.db, `SELECT count(*) FROM "group" WHERE name = 'Ops'`); n != 1 {
		t.Error("prune removed a group that has members")
	}
	if n := count(t, fx.db, `SELECT count(*) FROM group_member`); n != 1 {
		t.Error("prune touched group members")
	}
	if n := count(t, fx.db, `SELECT count(*) FROM hook_server`); n != 1 {
		t.Error("prune touched an undeclared kind")
	}
	if n := count(t, fx.db, `SELECT count(*) FROM access_grant WHERE resource_type = 'model' AND resource_id = 'researcher'`); n != 0 {
		t.Error("grants of a pruned model survived")
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning for the group with members")
	}
}

func TestAdoptUnmanagedRow(t *testing.T) {
	fx := setup(t)
	mustExec(t, fx.db, `INSERT INTO "group" (id, name) VALUES ('g-ops', 'Ops')`)
	f, err := Parse([]byte("groups:\n  - name: Ops\n"), ".", lookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	res := mustApply(t, fx, f, Options{})
	if res.Changes[0].Action != Update || res.Changes[0].Fields[0] != "managed" {
		t.Fatalf("adoption plan: %+v", res.Changes)
	}
	assertNoChanges(t, mustApply(t, fx, f, Options{}))
	if n := count(t, fx.db, `SELECT count(*) FROM "group" WHERE id = 'g-ops' AND managed`); n != 1 {
		t.Error("group not adopted in place")
	}
}

func TestSecretsEncryptedAtRest(t *testing.T) {
	fx := setup(t)
	mustApply(t, fx, loadFixture(t, testEnv), Options{})
	dump := ""
	for _, q := range []string{
		`SELECT string_agg(coalesce(encode(api_key_enc, 'escape'), ''), '|') FROM connection`,
		`SELECT string_agg(coalesce(encode(client_secret_enc, 'escape'), ''), '|') FROM oauth_provider`,
		`SELECT string_agg(auth::text, '|') FROM tool_server`,
		`SELECT string_agg(coalesce(encode(key_enc, 'escape'), ''), '|') FROM hook_server`,
		`SELECT string_agg(value::text, '|') FROM config`,
	} {
		var s string
		if err := fx.db.QueryRow(context.Background(), q).Scan(&s); err != nil {
			t.Fatal(err)
		}
		dump += s
	}
	for name, v := range testEnv {
		if strings.Contains(dump, v) {
			t.Errorf("plaintext of %s stored in the database", name)
		}
	}
	if !strings.Contains(dump, `"enc"`) || !strings.Contains(dump, `key_enc`) {
		t.Error("expected encrypted setting and tool key")
	}
}

func TestExportRoundTrip(t *testing.T) {
	fx := setup(t)
	ctx := context.Background()
	mustApply(t, fx, loadFixture(t, testEnv), Options{})
	cfg, placeholders, err := Export(ctx, fx.db, fx.keys)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range testEnv {
		if strings.Contains(string(out), v) {
			t.Errorf("export contains the secret %s", name)
		}
	}
	env := map[string]string{
		"KERA_GOOGLE_CLIENT_SECRET":           testEnv["GOOGLE_SECRET"],
		"KERA_CONNECTION_GATEWAY_API_KEY":     testEnv["GATEWAY_KEY"],
		"KERA_CONNECTION_GATEWAY_WEB_API_KEY": testEnv["GATEWAY_KEY"],
		"KERA_CODE_INTERPRETER_TOKEN":         testEnv["JUPYTER_TOKEN"],
		"KERA_OAUTH_MCP_SHEETS_CLIENT_SECRET": testEnv["SHEETS_SECRET"],
		"KERA_TOOL_KB_KEY":                    testEnv["KB_TOKEN"],
		"KERA_HOOK_CARDS_KEY":                 testEnv["PLUGINS_KEY"],
		"KERA_SPEND_SOURCE_DSN":               testEnv["SPEND_DSN"],
		"KERA_DESKTOP_KEY":                    testEnv["DESKTOP_KEY"],
	}
	if len(placeholders) != len(env) {
		t.Errorf("placeholders = %v", placeholders)
	}
	f, err := Parse(out, t.TempDir(), lookup(env))
	if err != nil {
		t.Fatalf("exported YAML does not parse: %v\n%s", err, out)
	}
	assertNoChanges(t, mustApply(t, fx, f, Options{Prune: true}))
	if !strings.Contains(string(out), "Prices are in $$ (USD)") {
		t.Error("a literal $ in a prompt is not escaped in the export")
	}
}

func TestEnvSubstitution(t *testing.T) {
	_, err := Parse([]byte("site:\n  name: ${NAME}\n  url: ${URL:-http://x}\nconnections:\n  - id: a\n    kind: openai\n    url: http://a\n    api_key: ${MISSING_KEY}\n"),
		".", lookup(map[string]string{"NAME": "n"}))
	if err == nil || !strings.Contains(err.Error(), "line 8: environment variable MISSING_KEY is not set") {
		t.Fatalf("err = %v", err)
	}
	f, err := Parse([]byte("site:\n  name: ${NAME}\n  url: ${URL:-http://x}\nchat:\n  max_tool_iterations: ${N}\n"), ".",
		lookup(map[string]string{"NAME": "n", "N": "12"}))
	if err != nil {
		t.Fatal(err)
	}
	if *f.Site.Name != "n" || *f.Site.URL != "http://x" || *f.Chat.MaxToolIterations != 12 {
		t.Errorf("substitution: %+v %+v", f.Site, f.Chat)
	}
}

func TestUnknownKeyAndCollectedErrors(t *testing.T) {
	src := `site:
  name: x
  colour: blue
models:
  - id: a
    prompt: hi
    prompt_file: x.md
    file_context: everything
    grants: [{principal: nobody}]
    knowledge: [{name: K, mode: tools}, {name: L, mode: inject}]
  - id: a
    base: x
    connection: y
tool_servers:
  - id: t
    kind: grpc
    url: http://t
    auth: {type: bearer}
permissions:
  features: {teleport: true}
knowledge:
  - name: Handbook
rag:
  reranker: {url: http://r, local_runs: always}
`
	_, err := Parse([]byte(src), ".", lookup(nil))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{
		`line 3: unknown key "colour"`,
		`line 5: models[a].prompt_file: set either the inline value or the _file, not both`,
		`line 5: models[a].file_context: "everything"`,
		`line 5: models[a].grants: bad principal "nobody"`,
		`line 5: models[a].knowledge.mode: "inject" is not one of [auto retrieve full tools]`,
		`line 11: models: duplicate "a"`,
		`line 15: tool_servers[t].kind: "grpc"`,
		`line 15: tool_servers[t]: auth.key is required for bearer auth`,
		`permissions: unknown permission features.teleport`,
		`rag.reranker.local_runs: "always" is not one of [skip allow]`,
		`line 11: models[a]: set base (a preset) or connection (a base-model override), not both`,
		`line 22: knowledge[Handbook]: grants is required`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), `"tools" is not one of`) {
		t.Error("knowledge mode tools rejected")
	}
}

func TestApplyResolutionErrors(t *testing.T) {
	fx := setup(t)
	f, err := Parse([]byte(`models:
  - id: m
    connection: nowhere
    tools: {servers: [no-such-server]}
    hooks: [no-such-hook]
    grants: [{principal: "group:Ghosts"}]
`), ".", lookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(context.Background(), fx.db, fx.keys, nil, f, Options{})
	for _, want := range []string{`unknown group "Ghosts"`, `connection "nowhere" does not exist`,
		`tool_server "no-such-server" does not exist`, `hook_server "no-such-hook" does not exist`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in err = %v", want, err)
		}
	}
	if n := count(t, fx.db, `SELECT count(*) FROM model`); n != 0 {
		t.Error("a failed apply wrote rows")
	}
}

func TestEverySettingHasADefault(t *testing.T) {
	for key, secret := range settingKeys() {
		if _, ok := settings.Defaults[key]; !ok && !secret {
			t.Errorf("setting %s has no entry in settings.Defaults", key)
		}
	}
}

// After KERA_SECRET_KEY changes, stored secrets no longer decrypt. Apply must
// warn and rewrite them from the file instead of failing, and the next run
// under the new key must be clean.
func TestSecretKeyRotation(t *testing.T) {
	fx := setup(t)
	f := loadFixture(t, testEnv)
	mustApply(t, fx, f, Options{})
	newKeys, err := secret.Derive(strings.Repeat("n", 32))
	if err != nil {
		t.Fatal(err)
	}
	fx.keys = newKeys
	res := mustApply(t, fx, f, Options{})
	if res.Count(Update) == 0 || len(res.Warnings) == 0 {
		t.Fatalf("expected secret rewrites with warnings, got %d updates, warnings %v", res.Count(Update), res.Warnings)
	}
	assertNoChanges(t, mustApply(t, fx, f, Options{}))
}

func TestBrandValidation(t *testing.T) {
	src := `brand:
  accent: {base: "red;}</style>", bright: "#3dafc4"}
  grey: {scale: ["#fff"]}
  background: stars
  splash: {style: spin}
  login: {style: glass-card}
`
	_, err := Parse([]byte(src), ".", lookup(nil))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{`brand.accent.base: "red;}</style>" is not a CSS colour`, `brand.grey.scale: needs 11 colours`,
		`brand.background: "stars"`, `brand.splash.style: "spin"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "bright") || strings.Contains(err.Error(), "login") {
		t.Errorf("valid values rejected:\n%v", err)
	}
	ok := "brand:\n  accent: {base: \"oklch(0.55 0.1 210)\", bright: \"#3DAFC4\"}\n  default_theme: dark\n"
	if _, err := Parse([]byte(ok), ".", lookup(nil)); err != nil {
		t.Errorf("valid brand rejected: %v", err)
	}
}
