package importowui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/testdb"
)

// sourceDDL is the subset of the source schema the importer reads.
const sourceDDL = `
CREATE SCHEMA src;
SET search_path = src;
CREATE TABLE "user" (id text, name text, email text, role text, profile_image_url text, settings json, info json, oauth json,
	last_active_at bigint, created_at bigint, updated_at bigint);
CREATE TABLE api_key (id text, user_id text, key text, data json, expires_at bigint, last_used_at bigint, created_at bigint);
CREATE TABLE "group" (id text, name text, description text, permissions json, created_at bigint, updated_at bigint);
CREATE TABLE group_member (group_id text, user_id text);
CREATE TABLE access_grant (id text, resource_type text, resource_id text, principal_type text, principal_id text, permission text, created_at bigint);
CREATE TABLE folder (id text, parent_id text, user_id text, name text, is_expanded boolean, created_at bigint, updated_at bigint);
CREATE TABLE chat (id text, user_id text, title text, summary text, share_id text, folder_id text, archived boolean, pinned boolean,
	chat json, meta json, created_at bigint, updated_at bigint);
CREATE TABLE chat_message (id text, chat_id text, output json);
CREATE TABLE chat_file (chat_id text, file_id text, message_id text);
CREATE TABLE shared_chat (id text, title text, chat json, created_at bigint, updated_at bigint);
CREATE TABLE file (id text, user_id text, filename text, path text, meta json, data json, created_at bigint, updated_at bigint);
CREATE TABLE knowledge (id text, user_id text, name text, description text, meta json, created_at bigint, updated_at bigint);
CREATE TABLE knowledge_file (knowledge_id text, file_id text, created_at bigint);
CREATE TABLE memory (id text, user_id text, content text, created_at bigint, updated_at bigint);
CREATE TABLE prompt (id text, user_id text, command text, name text, content text, created_at bigint, updated_at bigint);
CREATE TABLE feedback (id text, user_id text, type text, data json, meta json, snapshot json, created_at bigint, updated_at bigint);
CREATE TABLE model (id text, params text);
CREATE TABLE config (key text, value json);
CREATE TABLE tag (id text, name text, user_id text);
INSERT INTO tag VALUES ('work', 'Work', 'u1'), ('work', 'Work (Bob)', 'u2');

INSERT INTO "user" VALUES
 ('u1', 'Ann', 'ann@example.com', 'admin', '/user.png', '{"ui":{"theme":"dark"}}', null, '{"google":{"sub":"g-1"}}', 1700000100, 1700000000, 1700000050),
 ('u2', 'Bob', 'Bob@Example.com', 'user', 'data:image/png;base64,` + "AAAA" + `', null, null, null, 1700000100, 1700000000, 1700000050),
 ('u3', 'Cy', '', 'user', '', null, null, null, 1700000100, 1700000000, 1700000050);
INSERT INTO api_key VALUES
 ('k1', 'u1', 'sk-0123456789abcdef0123456789abcdef', '{"name":"cli"}', null, null, 1700000000),
 ('k2', 'u1', 'sk-expired000000000000000000000000', null, 1000000000, null, 1700000000),
 ('k3', 'ghost', 'sk-ownerless0000000000000000000000', null, null, null, 1700000000);
INSERT INTO "group" VALUES ('g1', 'Team', 'd', '{"features":{"memories":true,"notes":true},"workspace":{"models":true}}', 1700000000, 1700000000);
INSERT INTO group_member VALUES ('g1', 'u1'), ('g1', 'u2'), ('g1', 'ghost');
INSERT INTO access_grant VALUES
 ('a1', 'knowledge', 'kb1', 'group', 'g1', 'read', 1700000000),
 ('a2', 'tool', 't1', 'user', '*', 'read', 1700000000),
 ('a3', 'model', 'm1', 'user', 'u2', 'read', 1700000000);
INSERT INTO folder VALUES ('fo2', 'fo1', 'u1', 'Child', false, 1700000000, 1700000000), ('fo1', null, 'u1', 'Parent', true, 1700000000, 1700000000);
INSERT INTO chat VALUES
 ('c1', 'u1', 'First', '', 'sh1', 'fo2', false, true,
  '{"models":["m1"],"history":{"currentId":"m2","messages":{
    "m1":{"id":"m1","parentId":null,"role":"user","content":"q","timestamp":1700000001,"files":[{"id":"f1","file":{"data":{"content":"x"}}}]},
    "m2":{"id":"m2","parentId":"m1","role":"assistant","model":"m1","timestamp":1700000002,
          "content":"<details type=\"reasoning\" done=\"true\" duration=\"2\">\n<summary>t</summary>\n> think\n</details>\nanswer"}}}}',
  '{"tags":["work","work","todo"]}', 1700000000, 1700000010),
 ('c2', 'u2', 'Clone', '', null, null, false, false,
  '{"history":{"currentId":"m1","messages":{
    "m1":{"id":"m1","parentId":null,"role":"user","content":"same id as in c1","timestamp":1700000003},
    "x2":{"id":"x2","parentId":"m1","role":"assistant","content":"","timestamp":1700000004}}}}',
  null, 1700000000, 1700000020),
 ('c3', 'ghost', 'Orphan', '', null, null, false, false, '{}', null, 1700000000, 1700000000);
INSERT INTO chat_message VALUES ('c2-x2', 'c2', '[{"type":"message","content":[{"type":"output_text","text":"from output"}]}]');
INSERT INTO chat_file VALUES ('c1', 'f1', 'm1'), ('c1', 'f-gone', null);
INSERT INTO shared_chat VALUES ('sh1', 'First', '{"history":{"currentId":"m1","messages":{"m1":{"id":"m1","role":"user","content":"q","timestamp":1700000001}}}}', 1700000000, 1700000000);
INSERT INTO file VALUES
 ('f1', 'u1', 'notes.txt', '/app/data/uploads/f1_notes.txt', '{"name":"notes.txt","content_type":"text/plain","size":5,"data":{}}', '{"content":"hello"}', 1700000000, 1700000000),
 ('f2', 'u1', 'lost.pdf', '/app/data/uploads/f2_lost.pdf', '{"name":"lost.pdf","content_type":"application/pdf","size":9}', '{}', 1700000000, 1700000000);
INSERT INTO knowledge VALUES ('kb1', 'u1', 'Docs', 'all docs', null, 1700000000, 1700000000);
INSERT INTO knowledge_file VALUES ('kb1', 'f1', 1700000000), ('kb1', 'f2', 1700000000);
INSERT INTO memory VALUES ('me1', 'u1', 'likes tea', 1700000000, 1700000000);
INSERT INTO prompt VALUES ('p1', 'u1', 'summarize', 'Summarize', 'Summarize {{x}}', 1700000000, 1700000000);
INSERT INTO feedback VALUES ('fb1', 'u2', 'rating', '{"rating":1}', '{"chat_id":"c1"}', '{}', 1700000000, 1700000000);
INSERT INTO model VALUES ('preset/one', '{"system":"You are helpful."}'), ('base', '{}'), ('bad', 'not json');
INSERT INTO config VALUES
 ('rag.top_k', '5'), ('rag.openai.api_key', '"sk-secret"'), ('ui.enable_signup', 'false'),
 ('tool_server.connections', '[{"url":"http://tools:8000","key":"tok","auth_type":"bearer","headers":{"X-A":"b"}}]'),
 ('webhook_url', '"http://hook"');
RESET search_path;
`

// fixture creates a Kera target and a synthetic source schema in the same
// throwaway database (the source is reached through search_path).
func fixture(t *testing.T) (src, dst *pgxpool.Pool) {
	t.Helper()
	dst = testdb.New(t)
	ctx := context.Background()
	if _, err := dst.Exec(ctx, sourceDDL); err != nil {
		t.Fatalf("source fixture: %v", err)
	}
	cfg := dst.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = "src"
	src, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(src.Close)
	return src, dst
}

func count(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestImportSynthetic(t *testing.T) {
	src, dst := fixture(t)
	ctx := context.Background()
	// A user created in Kera before the import, with the same email as u2.
	if _, err := dst.Exec(ctx, `INSERT INTO "user" (id, name, email) VALUES ('native-bob', 'Bob', 'bob@example.com');
		INSERT INTO chat (id, user_id, title) VALUES ('native-chat', 'native-bob', 'mine')`); err != nil {
		t.Fatal(err)
	}
	uploads, data := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(uploads, "f1_notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	opt := Options{UploadsDir: uploads, DataDir: data, Log: discardLog()}

	// Dry run writes nothing (but is recorded).
	dry := opt
	dry.DryRun = true
	if _, err := Run(ctx, src, dst, "test", dry); err != nil {
		t.Fatal(err)
	}
	if n := count(t, dst, `SELECT count(*) FROM chat`); n != 1 {
		t.Fatalf("dry run wrote chats: %d", n)
	}

	rep, err := Run(ctx, src, dst, "test", opt)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	PrintReport(&buf, rep, false)
	t.Log("\n" + buf.String())
	s := rep.Stats
	check := func(name string, inserted, skipped int) {
		t.Helper()
		if s[name].Inserted != inserted || s[name].SkippedTotal() != skipped {
			t.Errorf("%s: inserted %d skipped %d, want %d/%d", name, s[name].Inserted, s[name].SkippedTotal(), inserted, skipped)
		}
	}
	check("users", 1, 1)    // u1 new, u2 merged into native-bob, u3 has no email
	check("api_keys", 1, 2) // expired and ownerless keys skipped
	check("group_members", 2, 1)
	check("grants", 2, 1) // tool grant skipped
	check("chats", 2, 1)  // ghost-owned chat skipped
	check("messages", 4, 0)
	check("chat_tags", 2, 0)
	if n := count(t, dst, `SELECT count(*) FROM chat_tag WHERE chat_id = 'c1' AND tag IN ('Work', 'todo')`); n != 2 {
		t.Errorf("chat tags by name: %d", n)
	}
	check("files", 2, 0)
	check("chat_files", 1, 1)
	check("knowledge_files", 2, 0)
	check("shared_chats", 1, 0)

	// Users: merge by email, avatar kept, oauth sub stored.
	if n := count(t, dst, `SELECT count(*) FROM group_member WHERE user_id = 'native-bob'`); n != 1 {
		t.Errorf("merged user's membership: %d", n)
	}
	if n := count(t, dst, `SELECT count(*) FROM "user" WHERE id = 'u1' AND oauth_sub = 'g-1' AND origin = 'openwebui' AND settings->'ui'->>'theme' = 'dark'`); n != 1 {
		t.Error("u1 not imported as expected")
	}
	// The API key authenticates as its owner.
	var owner string
	if err := dst.QueryRow(ctx, `SELECT user_id FROM api_key WHERE key_hash = $1`, auth.HashAPIKey("sk-0123456789abcdef0123456789abcdef")).Scan(&owner); err != nil || owner != "u1" {
		t.Errorf("api key owner %q, %v", owner, err)
	}
	if n := count(t, dst, `SELECT count(*) FROM "group" WHERE id = 'g1' AND permissions = '{"features":{"memory":true},"workspace":{"models":true}}'::jsonb`); n != 1 {
		t.Error("group permissions not mapped")
	}
	if n := count(t, dst, `SELECT count(*) FROM folder WHERE id = 'fo2' AND parent_id = 'fo1'`); n != 1 {
		t.Error("folder parent not linked")
	}
	// Chats: message tree, details converted, colliding id re-keyed, output parts.
	if n := count(t, dst, `SELECT count(*) FROM chat WHERE id = 'c1' AND current_message_id = 'm2' AND folder_id = 'fo2' AND pinned AND share_id = 'sh1' AND model_ids = '{m1}'`); n != 1 {
		t.Error("chat c1 fields")
	}
	if n := count(t, dst, `SELECT count(*) FROM message WHERE id = 'm2' AND content = 'answer' AND parts->0->>'type' = 'reasoning' AND parts->0->>'text' = 'think'`); n != 1 {
		t.Error("details block not converted")
	}
	if n := count(t, dst, `SELECT count(*) FROM message WHERE id = 'm1' AND chat_id = 'c1' AND files->0->'file' IS NULL`); n != 1 {
		t.Error("message file ref not stripped")
	}
	if n := count(t, dst, `SELECT count(*) FROM message WHERE chat_id = 'c2' AND id = 'c2-m1'`); n != 1 {
		t.Error("shared message id not re-keyed")
	}
	if n := count(t, dst, `SELECT count(*) FROM chat WHERE id = 'c2' AND current_message_id = 'x2'`); n != 1 {
		t.Error("current id not moved to leaf")
	}
	if n := count(t, dst, `SELECT count(*) FROM message WHERE id = 'x2' AND parent_id = 'c2-m1' AND parts->0->>'text' = 'from output'`); n != 1 {
		t.Error("output parts or re-keyed parent wrong")
	}
	if n := count(t, dst, `SELECT count(*) FROM shared_chat WHERE share_id = 'sh1' AND chat_id = 'c1' AND snapshot->'messages'->0->>'id' = 'm1'`); n != 1 {
		t.Error("share snapshot")
	}
	// Files: blob copied with sha, missing blob marked failed, text carried.
	sum := sha256.Sum256([]byte("hello"))
	if n := count(t, dst, `SELECT count(*) FROM file WHERE id = 'f1' AND status = 'completed' AND text = 'hello' AND sha256 = $1
		AND path = 'uploads/f1/notes.txt' AND NOT meta ? 'data'`, hex.EncodeToString(sum[:])); n != 1 {
		t.Error("f1 row")
	}
	if b, err := os.ReadFile(filepath.Join(data, "uploads", "f1", "notes.txt")); err != nil || string(b) != "hello" {
		t.Errorf("blob copy: %q %v", b, err)
	}
	if n := count(t, dst, `SELECT count(*) FROM file WHERE id = 'f2' AND status = 'failed' AND error = 'blob missing'`); n != 1 {
		t.Error("f2 should be failed")
	}
	if len(rep.MissingBlobs) != 1 || rep.MissingBlobs[0] != "f2" {
		t.Errorf("missing blobs %v", rep.MissingBlobs)
	}

	// Second run: nothing new, nothing duplicated.
	rep2, err := Run(ctx, src, dst, "test", opt)
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range rep2.Stats {
		if st.Inserted != 0 {
			t.Errorf("re-run inserted %d %s", st.Inserted, name)
		}
	}
	if n := count(t, dst, `SELECT count(*) FROM migration_run WHERE finished_at IS NOT NULL AND error = ''`); n != 3 {
		t.Errorf("migration runs %d", n)
	}

	v, err := Verify(ctx, src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if !v.OK() || v.Keys.Checked != 1 || v.Keys.OK != 1 {
		buf.Reset()
		PrintVerification(&buf, v)
		t.Fatalf("verify:\n%s", buf.String())
	}

	// A chat deleted at the source: verify reports it, prune removes it and
	// leaves the native chat alone.
	if _, err := dst.Exec(ctx, `DELETE FROM src.chat WHERE id = 'c2'; DELETE FROM src.api_key WHERE id = 'k1'`); err != nil {
		t.Fatal(err)
	}
	if v, err := Verify(ctx, src, dst); err != nil || v.OK() || v.Report.Stats["chats"].Pruned != 1 {
		t.Fatalf("verify after delete: ok=%v err=%v", v.OK(), err)
	}
	// c1 is gone at the source too, but was continued in Kera: prune keeps it.
	if _, err := dst.Exec(ctx, `DELETE FROM src.chat WHERE id = 'c1';
		INSERT INTO message (id, chat_id, parent_id, role, content) VALUES ('native-msg', 'c1', 'm2', 'user', 'more')`); err != nil {
		t.Fatal(err)
	}
	prune := opt
	prune.PruneMissing = true
	prune.Only = []string{"users", "chats"}
	rep3, err := Run(ctx, src, dst, "test", prune)
	if err != nil {
		t.Fatal(err)
	}
	if rep3.Stats["chats"].Pruned != 1 || rep3.Stats["api_keys"].Pruned != 1 {
		t.Errorf("pruned chats %d keys %d", rep3.Stats["chats"].Pruned, rep3.Stats["api_keys"].Pruned)
	}
	if n := count(t, dst, `SELECT count(*) FROM chat WHERE id IN ('c1', 'c2', 'native-chat')`); n != 2 {
		t.Errorf("after prune: %d", n)
	}
	if n := count(t, dst, `SELECT count(*) FROM "user" WHERE id = 'native-bob'`); n != 1 {
		t.Error("native user pruned")
	}
}

func TestExportPresetsAndConfig(t *testing.T) {
	src, _ := fixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	n, err := ExportPresets(ctx, src, dir)
	if err != nil || n != 1 {
		t.Fatalf("export %d %v", n, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "preset_one.md")); err != nil || string(b) != "You are helpful." {
		t.Fatalf("preset file %q %v", b, err)
	}
	var buf bytes.Buffer
	if err := ReportConfig(ctx, src, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`rag.top_k = 5`, `rag.openai.api_key = "[redacted]"`, `ui.enable_signup = false`,
		`"key":"[redacted]"`, `"auth_type":"bearer"`, `"X-A":"[redacted]"`} {
		if !strings.Contains(out, want) {
			t.Errorf("config report lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sk-secret") || strings.Contains(out, "tok") && !strings.Contains(out, "tokens") && strings.Contains(out, `"tok"`) || strings.Contains(out, "webhook_url") {
		t.Errorf("config report leaks or over-selects:\n%s", out)
	}
}

func TestRedact(t *testing.T) {
	for name, secret := range map[string]bool{
		"rag.openai.api_key": true, "web.search.sougou_api_sk": true, "client_secret": true, "accessToken": true,
		"key": true, "auth_type": false, "rag.tokenizer_model": false, "web.search.yacy_username": false, "rag.top_k": false,
	} {
		if secretName(name) != secret {
			t.Errorf("secretName(%q) = %v", name, !secret)
		}
	}
	if got := redact("url", "postgres://u:pw@h/db"); got != "postgres://u:xxxxx@h/db" {
		t.Errorf("url redaction %v", got)
	}
	if got := redact("x", "Bearer abc"); got != redacted {
		t.Errorf("bearer %v", got)
	}
	if got := redact("info", map[string]any{"oauth_client_info": "opaque", "note": "gAAAAABfernet"}); got.(map[string]any)["oauth_client_info"] != redacted ||
		got.(map[string]any)["note"] != redacted {
		t.Errorf("client info / fernet %v", got)
	}
	if got := redact("api_key", ""); got != "" {
		t.Errorf("empty secret should stay visible, got %v", got)
	}
}

func TestParseFlags(t *testing.T) {
	if _, err := parseOnly("users,bogus"); err == nil {
		t.Error("bogus entity accepted")
	}
	if o, err := parseOnly("users, chats"); err != nil || len(o) != 2 {
		t.Errorf("parseOnly %v %v", o, err)
	}
	if d, err := parseSince("2026-11-01"); err != nil || !d.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("since %v %v", d, err)
	}
	if _, err := parseSince("yesterday"); err == nil {
		t.Error("bad since accepted")
	}
	if got := SourceName("postgres://u:secret@db:5432/x") + SourceName("postgres://u@db/x?password=secret&sslmode=disable"); strings.Contains(got, "secret") {
		t.Errorf("SourceName leaks: %s", got)
	}
	if safeName("../../etc/passwd") != ".._.._etc_passwd" || safeName("..") != "file" || safeName("") != "file" {
		t.Error("safeName")
	}
}

// TestImportSnapshot imports a real source database (KERA_TEST_OWUI_DSN)
// into a fresh target without blobs, checks counts against the source, runs
// it again for idempotency, checks the message trees and runs verify.
func TestImportSnapshot(t *testing.T) {
	dsn := os.Getenv("KERA_TEST_OWUI_DSN")
	if dsn == "" {
		t.Skip("KERA_TEST_OWUI_DSN not set")
	}
	dst := testdb.New(t)
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	src, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	u, _ := url.Parse(dsn)
	opt := Options{NoFiles: true, Log: discardLog()}
	rep, err := Run(ctx, src, dst, u.Redacted(), opt)
	if err != nil {
		t.Fatal(err)
	}
	tables := map[string]string{
		"users": `"user"`, "api_keys": "api_key", "groups": `"group"`, "folders": "folder", "chats": "chat",
		"files": "file", "knowledge": "knowledge", "grants": "access_grant", "memories": "memory", "prompts": "prompt", "feedback": "feedback",
	}
	for name, table := range tables {
		st := rep.Stats[name]
		if want := count(t, src, `SELECT count(*) FROM `+table); st.Source != want {
			t.Errorf("%s: read %d of %d source rows", name, st.Source, want)
		}
		if got := count(t, dst, `SELECT count(*) FROM `+table+` WHERE origin = 'openwebui'`); got+st.Notes["merged into an existing user with the same email or login"] != st.Source-st.SkippedTotal() {
			t.Errorf("%s: target %d, source %d, skipped %d", name, got, st.Source, st.SkippedTotal())
		}
	}
	if got, want := count(t, dst, `SELECT count(*) FROM message`), rep.Stats["messages"].Source-rep.Stats["messages"].SkippedTotal(); got != want {
		t.Errorf("messages: target %d want %d", got, want)
	}

	rep2, err := Run(ctx, src, dst, u.Redacted(), opt)
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range rep2.Stats {
		if st.Inserted != 0 || st.Source != rep.Stats[name].Source {
			t.Errorf("re-run %s: inserted %d, source %d vs %d", name, st.Inserted, st.Source, rep.Stats[name].Source)
		}
	}

	// Tree integrity for every chat: parents exist in the same chat and the
	// current message is an existing leaf.
	if n := count(t, dst, `SELECT count(*) FROM message m WHERE parent_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM message p WHERE p.id = m.parent_id AND p.chat_id = m.chat_id)`); n != 0 {
		t.Errorf("%d messages with a missing parent", n)
	}
	if n := count(t, dst, `SELECT count(*) FROM chat c WHERE EXISTS (SELECT 1 FROM message m WHERE m.chat_id = c.id) AND (
		NOT EXISTS (SELECT 1 FROM message m WHERE m.id = c.current_message_id AND m.chat_id = c.id)
		OR EXISTS (SELECT 1 FROM message k WHERE k.parent_id = c.current_message_id AND k.chat_id = c.id))`); n != 0 {
		t.Errorf("%d chats whose current message is missing or not a leaf", n)
	}
	// The sample chat with the most messages walks from its leaf to a root.
	var sample string
	if err := dst.QueryRow(ctx, `SELECT chat_id FROM message GROUP BY chat_id ORDER BY count(*) DESC, chat_id LIMIT 1`).Scan(&sample); err != nil {
		t.Fatal(err)
	}
	depth := count(t, dst, `WITH RECURSIVE up AS (
			SELECT m.id, m.parent_id, 1 AS d FROM message m JOIN chat c ON c.current_message_id = m.id WHERE c.id = $1
			UNION ALL SELECT p.id, p.parent_id, up.d + 1 FROM message p JOIN up ON p.id = up.parent_id)
		SELECT max(d) FROM up WHERE parent_id IS NULL`, sample)
	if depth < 2 {
		t.Errorf("sample chat %s: active branch depth %d", sample, depth)
	}

	v, err := Verify(ctx, src, dst)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	PrintVerification(&buf, v)
	t.Log("\n" + buf.String())
	if !v.OK() {
		t.Fatal("verify mismatch")
	}
	if want := count(t, src, `SELECT count(*) FROM api_key`); v.Keys.OK != want {
		t.Errorf("keys resolving %d of %d", v.Keys.OK, want)
	}
}
