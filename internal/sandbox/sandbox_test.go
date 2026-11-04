package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
	"github.com/cdmx-in/kera/internal/tools"
)

var png1x1, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

// fakeJupyter answers the kernel REST calls and runs a tiny command language
// over the channels socket: each line of the code is one command.
type fakeJupyter struct {
	t       *testing.T
	token   string
	mu      sync.Mutex
	kernels map[string]bool // id -> alive
	deleted int
	cwd     string
}

func (f *fakeJupyter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+f.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/kernels":
		id := "k" + time.Now().Format("150405.000000000")
		f.kernels[id] = true
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/kernels/"):
		delete(f.kernels, strings.TrimPrefix(r.URL.Path, "/api/kernels/"))
		f.deleted++
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/channels"):
		f.mu.Unlock()
		f.channels(w, r)
		f.mu.Lock()
	default:
		http.NotFound(w, r)
	}
}

var chdirRe = regexp.MustCompile(`chdir\("([^"]+)"\)`)

func (f *fakeJupyter) channels(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(16 << 20)
	ctx := r.Context()
	for {
		var m message
		if err := wsjson.Read(ctx, c, &m); err != nil {
			return
		}
		var req struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(m.Content, &req)
		send := func(kind string, content any) {
			b, _ := json.Marshal(content)
			_ = wsjson.Write(ctx, c, message{Header: msgHeader{MsgID: "x", MsgType: kind},
				ParentHeader: m.Header, Content: b, Channel: "iopub"})
		}
		// noise for another request must be ignored
		_ = wsjson.Write(ctx, c, message{Header: msgHeader{MsgType: "stream"}, ParentHeader: msgHeader{MsgID: "other"},
			Content: json.RawMessage(`{"name":"stdout","text":"NOISE"}`), Channel: "iopub"})
		send("status", map[string]string{"execution_state": "busy"})
		if mm := chdirRe.FindStringSubmatch(req.Code); mm != nil {
			f.cwd = mm[1]
		}
		hang := false
		for _, line := range strings.Split(req.Code, "\n") {
			cmd, arg, _ := strings.Cut(line, " ")
			switch cmd {
			case "print":
				send("stream", map[string]string{"name": "stdout", "text": arg + "\n"})
			case "warn":
				send("stream", map[string]string{"name": "stderr", "text": arg + "\n"})
			case "value":
				send("execute_result", map[string]any{"data": map[string]string{"text/plain": arg}})
			case "plot":
				send("display_data", map[string]any{"data": map[string]string{"image/png": base64.StdEncoding.EncodeToString(png1x1), "text/plain": "<Figure>"}})
			case "write": // write <path> <content>; relative paths land in the cwd
				p, content, _ := strings.Cut(arg, " ")
				if !filepath.IsAbs(p) {
					p = filepath.Join(f.cwd, p)
				}
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				_ = os.WriteFile(p, []byte(content), 0o644)
			case "symlink": // symlink <target> <name>
				target, name, _ := strings.Cut(arg, " ")
				_ = os.Symlink(target, filepath.Join(f.cwd, name))
			case "fail":
				send("error", map[string]any{"ename": "ValueError", "evalue": arg,
					"traceback": []string{"\x1b[0;31mTraceback\x1b[0m", "  line 1", "ValueError: " + arg}})
			case "hang":
				hang = true
			}
		}
		if !hang {
			send("status", map[string]string{"execution_state": "idle"})
		}
	}
}

func TestJupyterExec(t *testing.T) {
	fj := &fakeJupyter{t: t, token: "tok", kernels: map[string]bool{}}
	srv := httptest.NewServer(fj)
	defer srv.Close()
	j := &Jupyter{URL: srv.URL, Token: "tok"}
	ctx := context.Background()
	k, err := j.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := k.Exec(ctx, "print hello\nwarn careful\nvalue 42\nplot\nfail boom", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stdout != "hello\n" || out.Stderr != "careful\n" || out.Result != "42" || len(out.Images) != 1 || out.TimedOut {
		t.Fatalf("output = %+v", out)
	}
	if !strings.HasPrefix(out.Error, "ValueError: boom\nTraceback") || strings.Contains(out.Error, "\x1b") {
		t.Fatalf("error = %q", out.Error)
	}
	out, err = k.Exec(ctx, "print partial\nhang", 300*time.Millisecond)
	if err != nil || !out.TimedOut || out.Stdout != "partial\n" {
		t.Fatalf("timeout: %+v %v", out, err)
	}
	k.Close()
	if fj.deleted != 1 || len(fj.kernels) != 0 {
		t.Fatalf("kernel not deleted: %+v", fj.kernels)
	}
	if _, err := (&Jupyter{URL: srv.URL, Token: "bad"}).Start(ctx); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("bad token: %v", err)
	}
}

func TestHelpers(t *testing.T) {
	code := "df.to_csv('/mnt/data/out.csv')\nos.listdir('/mnt/data')\nopen(\"/mnt/data\")\n'/mnt/database'"
	got := rewritePaths(code, "/mnt/data/", "/mnt/data/runs/r1")
	want := "df.to_csv('/mnt/data/runs/r1/out.csv')\nos.listdir('/mnt/data/runs/r1')\nopen(\"/mnt/data/runs/r1\")\n'/mnt/database'"
	if got != want {
		t.Fatalf("rewrite:\n%s\nwant\n%s", got, want)
	}
	long := strings.Repeat("a", maxOutputChars) + strings.Repeat("z", 100)
	if c := clip(long); len(c) > maxOutputChars+60 || !strings.HasSuffix(c, "zzz") || !strings.Contains(c, "characters cut") {
		t.Fatalf("clip: %d", len(c))
	}
	tb := "KeyError: 'x'\n" + strings.Repeat("frame\n", 100) + "last"
	if s := tail(tb, 3, 1000); !strings.HasPrefix(s, "KeyError: 'x'\n...\n") || !strings.HasSuffix(s, "last") {
		t.Fatalf("tail: %q", s)
	}
	if n := uniqueName("a.csv", map[string]stamp{"a.csv": {}, "a (2).csv": {}}); n != "a (3).csv" {
		t.Fatal(n)
	}
	pre := prelude("/k/runs/r", map[string]stamp{"b.csv": {}, "a b.xlsx": {}}, "HELPER = 1")
	if !strings.Contains(pre, `chdir("/k/runs/r")`) || !strings.Contains(pre, `ATTACHMENTS = ["a b.xlsx","b.csv"]`) || !strings.HasSuffix(pre, "HELPER = 1") {
		t.Fatal(pre)
	}
}

type env struct {
	p      *Provider
	fj     *fakeJupyter
	shared string
	user   *auth.User
	events []tools.Event
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	keys, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(pool)
	fsvc := files.New(pool, st, t.TempDir())
	if err := fsvc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fsvc.Close)
	fj := &fakeJupyter{t: t, token: "s3cret", kernels: map[string]bool{}}
	srv := httptest.NewServer(fj)
	t.Cleanup(srv.Close)
	shared := t.TempDir()
	enc, err := keys.Encrypt([]byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{
		"code_interpreter.engine": "jupyter", "code_interpreter.url": srv.URL, "code_interpreter.timeout": 2,
		"code_interpreter.shared_dir": shared, "code_interpreter.kernel_dir": shared, "code_interpreter.max_file_mb": 1,
		"code_interpreter.prelude": "print prelude-ran",
		"code_interpreter.token":   map[string]string{"enc": base64.StdEncoding.EncodeToString(enc)},
	} {
		if err := st.Set(ctx, k, v, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email, role) VALUES ('alice', 'alice@example.com', 'user'), ('bob', 'bob@example.com', 'user')`); err != nil {
		t.Fatal(err)
	}
	u := &auth.User{ID: "alice", Role: "user", Permissions: auth.Permissions{"features": map[string]any{"code_interpreter": true}}}
	return &env{p: &Provider{Settings: st, Keys: keys, Files: fsvc}, fj: fj, shared: shared, user: u}
}

func (e *env) call(t *testing.T, code string, fileIDs ...string) tools.Result {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"code": code})
	res, err := e.p.Execute(context.Background(), tools.Call{Name: "execute_code", Args: args, User: e.user, RunID: "run1",
		ChatID: "c1", FileIDs: fileIDs, Emit: func(ev tools.Event) { e.events = append(e.events, ev) }})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *env) upload(t *testing.T, owner, name, content string) string {
	t.Helper()
	f, err := e.p.Files.Create(context.Background(), owner, name, "text/csv", strings.NewReader(content), nil)
	if err != nil {
		t.Fatal(err)
	}
	return f.ID
}

func TestExecuteHarvest(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	specs, err := e.p.Tools(ctx, e.user)
	if err != nil || len(specs) != 1 || specs[0].Name != "execute_code" {
		t.Fatalf("tools: %v %v", specs, err)
	}
	if specs, _ := e.p.Tools(ctx, &auth.User{ID: "bob"}); specs != nil {
		t.Fatal("user without the permission sees execute_code")
	}
	mine := e.upload(t, "alice", "data.csv", "a,b\n1,2\n")
	other := e.upload(t, "bob", "secret.csv", "no")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := strings.Join([]string{
		"print hi",
		"write report.csv x,y",
		"write /" + strings.TrimPrefix(e.shared, "/") + "/abs.txt absolute", // rewritten into the run folder
		"write sub/deep.txt nested",
		"write .hidden secret",
		"write big.bin " + strings.Repeat("x", 2<<20),
		"symlink " + outside + " leak.txt",
		"plot",
	}, "\n")
	res := e.call(t, code, mine, other)
	if res.IsError {
		t.Fatalf("error result: %s", res.Content)
	}
	var rep report
	if err := json.Unmarshal([]byte(res.Content), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Stdout != "hi\n" {
		t.Fatalf("stdout %q", rep.Stdout)
	}
	names := map[string]bool{}
	for _, id := range res.Files {
		f, err := e.p.Files.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if f.UserID != "alice" {
			t.Fatalf("harvested file owned by %s", f.UserID)
		}
		names[f.Filename] = true
	}
	for _, want := range []string{"report.csv", "abs.txt", "deep.txt", "output-1.png"} {
		if !names[want] {
			t.Errorf("missing %s in %v", want, names)
		}
	}
	for _, bad := range []string{"data.csv", "secret.csv", ".hidden", "hidden", "big.bin", "leak.txt"} {
		if names[bad] {
			t.Errorf("unexpected %s harvested", bad)
		}
	}
	if len(rep.Skipped) == 0 || !strings.Contains(strings.Join(rep.Skipped, ","), "big.bin") {
		t.Errorf("skipped = %v", rep.Skipped)
	}
	// the staged input was the user's own file only, and the run folder is gone
	entries, _ := os.ReadDir(filepath.Join(e.shared, "runs"))
	if len(entries) != 0 {
		t.Fatalf("run folder left behind: %v", entries)
	}
	if e.fj.deleted != 1 {
		t.Fatalf("kernels deleted = %d", e.fj.deleted)
	}
	if len(e.events) != 2 || e.events[1].Data.(map[string]any)["done"] != true {
		t.Fatalf("events = %+v", e.events)
	}
}

func TestExecuteInputsAndErrors(t *testing.T) {
	e := setup(t)
	in := e.upload(t, "alice", "data.csv", "a,b\n")
	// overwriting an attachment returns it; an untouched one is not returned
	res := e.call(t, "write data.csv a,b,c\nprint done", in)
	if len(res.Files) != 1 {
		t.Fatalf("files = %v (%s)", res.Files, res.Content)
	}
	f, rc := mustOpen(t, e, res.Files[0])
	if f.Filename != "data.csv" || rc != "a,b,c" {
		t.Fatalf("overwritten input = %s %q", f.Filename, rc)
	}
	res = e.call(t, "print nothing", in)
	if len(res.Files) != 0 {
		t.Fatalf("unchanged input harvested: %v", res.Files)
	}
	res = e.call(t, "fail bad value")
	if !res.IsError || !strings.Contains(res.Content, "ValueError: bad value") {
		t.Fatalf("error = %+v", res)
	}
	res = e.call(t, "hang")
	if !res.IsError || !strings.Contains(res.Content, "timed out") {
		t.Fatalf("timeout = %+v", res)
	}
	res = e.call(t, " ")
	if !res.IsError {
		t.Fatal("empty code accepted")
	}
}

func mustOpen(t *testing.T, e *env, id string) (files.File, string) {
	t.Helper()
	rc, f, err := e.p.Files.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return f, string(bytes.TrimSpace(b))
}
