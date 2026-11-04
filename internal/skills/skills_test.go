package skills

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/testdb"
	"github.com/cdmx-in/kera/internal/tools"
)

var sample = fstest.MapFS{
	"pack/review/SKILL.md":          {Data: []byte("---\nname: code-review\ndescription: |\n  Reviews code.\n  Finds bugs.\nallowed-tools: Read\n---\n# Review\nUse refs/a.md")},
	"pack/review/refs/a.md":         {Data: []byte("checklist")},
	"pack/review/logo.png":          {Data: []byte{0x89, 'P', 'N', 'G', 0, 1}},
	"pack/review/.git/config":       {Data: []byte("x")},
	"pack/review/nested/SKILL.md":   {Data: []byte("no frontmatter here")},
	"pack/review/nested/tool.py":    {Data: []byte("print(1)")},
	"pack/zdup/SKILL.md":            {Data: []byte("---\nname: code-review\n---\nsecond")},
	"__MACOSX/pack/review/SKILL.md": {Data: []byte("junk")},
}

func TestParse(t *testing.T) {
	list, warns, err := Parse(sample, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Skill{}
	for _, s := range list {
		got[s.Name] = s
	}
	if len(list) != 2 {
		t.Fatalf("skills = %v", list)
	}
	r := got["code-review"]
	if r.Description != "Reviews code. Finds bugs." || !strings.Contains(r.Content, "# Review") {
		t.Errorf("code-review = %+v", r)
	}
	if len(r.Resources) != 1 || r.Resources["refs/a.md"] != "checklist" {
		t.Errorf("resources = %v (nested skill files and binaries must stay out)", r.Resources)
	}
	if n := got["nested"]; n.Content != "no frontmatter here" || n.Resources["tool.py"] != "print(1)" {
		t.Errorf("nested = %+v", n)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "logo.png: not a text file") || !strings.Contains(joined, `"code-review" already used`) {
		t.Errorf("warnings = %v", warns)
	}
}

func TestLoadZipAndDir(t *testing.T) {
	dir := t.TempDir()
	zpath := filepath.Join(dir, "solo.zip")
	f, err := os.Create(zpath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{"SKILL.md": "Top-level skill", "ref.txt": "r"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	list, _, err := Load(zpath)
	if err != nil || len(list) != 1 || list[0].Name != "solo" || list[0].Resources["ref.txt"] != "r" {
		t.Fatalf("zip: %+v %v", list, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "d", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "d", "x", "SKILL.md"), []byte("---\nname: ex\n---\nhi"), 0o644)
	list, _, err = Load(filepath.Join(dir, "d"))
	if err != nil || len(list) != 1 || list[0].Name != "ex" {
		t.Fatalf("dir: %+v %v", list, err)
	}
}

func call(t *testing.T, p *Provider, u *auth.User, name, args string) tools.Result {
	t.Helper()
	res, err := p.Execute(context.Background(), tools.Call{Name: name, Args: json.RawMessage(args), User: u})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestImportAccessAndProvider(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	list, _, err := Parse(sample, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Import(ctx, db, list, []access.Grant{{Principal: "group:g-sec"}})
	if err != nil || len(res) != 2 || res[0].Outcome != Created {
		t.Fatalf("import: %+v %v", res, err)
	}
	// idempotent: same text = unchanged, no duplicate grants; changed text = updated, same id
	res2, err := Import(ctx, db, list, []access.Grant{{Principal: "group:g-sec"}})
	if err != nil || res2[0].Outcome != Unchanged || res2[1].Outcome != Unchanged {
		t.Fatalf("re-import: %+v %v", res2, err)
	}
	var n int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM access_grant WHERE resource_type = 'skill'`).Scan(&n)
	if n != 2 {
		t.Errorf("grants = %d, want 2", n)
	}
	list[0].Content += "\nmore"
	res3, err := Import(ctx, db, list[:1], nil)
	if err != nil || res3[0].Outcome != Updated || res3[0].ID != res[0].ID {
		t.Fatalf("update: %+v %v", res3, err)
	}

	p := &Provider{DB: db}
	admin := &auth.User{ID: "root", Role: "admin"}
	member := &auth.User{ID: "m", Role: "user", Groups: []string{"g-sec"}}
	other := &auth.User{ID: "o", Role: "user"}

	specs, err := p.Tools(ctx, other)
	if err != nil || specs != nil {
		t.Fatalf("user without grants gets tools: %v %v", specs, err)
	}
	specs, err = p.Tools(ctx, member)
	if err != nil || len(specs) != 3 {
		t.Fatalf("member tools: %v %v", specs, err)
	}
	// lazy: only names and descriptions are in the prompt
	if d := specs[1].Description; !strings.Contains(d, "- code-review: Reviews code. Finds bugs.") || strings.Contains(d, "# Review") {
		t.Errorf("load_skill description = %q", d)
	}
	if r := call(t, p, member, "load_skill", `{"name":"code-review"}`); r.IsError || !strings.Contains(r.Content, "# Review") || !strings.Contains(r.Content, "- refs/a.md") {
		t.Errorf("load_skill = %+v", r)
	}
	if r := call(t, p, member, "read_skill_file", `{"name":"code-review","path":"./refs/a.md"}`); r.Content != "checklist" {
		t.Errorf("read_skill_file = %+v", r)
	}
	if r := call(t, p, member, "read_skill_file", `{"name":"code-review","path":"../x"}`); !r.IsError {
		t.Errorf("missing file = %+v", r)
	}
	if r := call(t, p, other, "load_skill", `{"name":"code-review"}`); !r.IsError {
		t.Errorf("ungranted load = %+v", r)
	}
	if r := call(t, p, member, "load_skill", `{"name":"`+res[0].ID+`"}`); !r.IsError {
		t.Errorf("load by id must not work: %+v", r)
	}
	// disabled skills disappear; non-lazy skills ride in the description
	if _, err := db.Exec(ctx, `UPDATE skill SET enabled = false WHERE name = 'nested'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE skill SET lazy = false WHERE name = 'code-review'`); err != nil {
		t.Fatal(err)
	}
	specs, _ = p.Tools(ctx, admin)
	if d := specs[1].Description; strings.Contains(d, "nested") || !strings.Contains(d, "# Review") {
		t.Errorf("admin load_skill description = %q", d)
	}
	if r := call(t, p, admin, "list_skills", `{}`); strings.Contains(r.Content, "nested") || !strings.Contains(r.Content, "code-review") {
		t.Errorf("list_skills = %+v", r)
	}
	if err := Delete(ctx, db, res[0].ID); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(ctx, `SELECT count(*) FROM access_grant WHERE resource_type = 'skill'`).Scan(&n)
	if n != 1 {
		t.Errorf("grants after delete = %d, want 1", n)
	}
}

func TestMigrationDownUp(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	if err := access.Replace(ctx, db, access.Skill, "s1", []access.Grant{{Principal: "*"}}, false); err != nil {
		t.Fatalf("skill grant rejected by the CHECK: %v", err)
	}
	sqldb := stdlib.OpenDBFromPool(db)
	defer sqldb.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, sqldb, os.DirFS("../db/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 2); err != nil {
		t.Fatalf("down: %v", err)
	}
	var exists bool
	_ = db.QueryRow(ctx, `SELECT to_regclass('skill') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("skill table survived down")
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
