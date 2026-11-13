package files

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

type env struct {
	t     *testing.T
	a     *app.App
	svc   *Service
	url   string
	users map[string]string // name -> token
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	keys, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(pool)
	cfg := config.Env{DataDir: t.TempDir()}
	srv, err := api.New(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Env: cfg, DB: pool, Keys: keys, Settings: st, Server: srv, Auth: auth.New(pool, keys, st, false)}
	if err := Mount(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(For(a).Close)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &env{t: t, a: a, svc: For(a), url: ts.URL, users: map[string]string{}}
	for _, u := range []struct{ id, role string }{{"alice", "user"}, {"bob", "user"}, {"root", "admin"}} {
		if _, err := pool.Exec(context.Background(), `INSERT INTO "user" (id, email, role) VALUES ($1, $1 || '@example.com', $2)`, u.id, u.role); err != nil {
			t.Fatal(err)
		}
		tok, err := auth.IssueToken(keys, u.id, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		e.users[u.id] = tok
	}
	return e
}

func (e *env) do(user, method, path string, body io.Reader, ctype string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Authorization", "Bearer "+e.users[user])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (e *env) json(user, method, path string, want int, out any) {
	e.t.Helper()
	resp := e.do(user, method, path, nil, "")
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

type fileResp struct {
	ID       string         `json:"id"`
	UserID   string         `json:"user_id"`
	Filename string         `json:"filename"`
	Meta     map[string]any `json:"meta"`
	Data     struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"data"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

func multipartBody(t *testing.T, name string, content []byte, metadata string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if metadata != "" {
		_ = mw.WriteField("metadata", metadata)
	}
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(content)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func (e *env) upload(user, name string, content []byte, query string) fileResp {
	e.t.Helper()
	body, ct := multipartBody(e.t, name, content, "")
	resp := e.do(user, "POST", "/api/v1/files/"+query, body, ct)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		e.t.Fatalf("upload %s: %d %s", name, resp.StatusCode, b)
	}
	var f fileResp
	if err := json.Unmarshal(b, &f); err != nil {
		e.t.Fatal(err)
	}
	return f
}

func (e *env) wait(id string) File {
	e.t.Helper()
	f, err := e.svc.Wait(context.Background(), id, 20*time.Second)
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

func (e *env) text(id string) string {
	t, err := e.svc.Text(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return t
}

func xlsxBytes(t *testing.T) []byte {
	t.Helper()
	x := excelize.NewFile()
	defer x.Close()
	_ = x.SetSheetName("Sheet1", "Data")
	_ = x.SetSheetRow("Data", "A1", &[]any{"Name", "Joined", "Amount"})
	_ = x.SetSheetRow("Data", "A2", &[]any{"Ana, B", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), 12.5})
	style, _ := x.NewStyle(&excelize.Style{NumFmt: 14}) // built-in date format
	_ = x.SetCellStyle("Data", "B2", "B2", style)
	if _, err := x.NewSheet("Notes"); err != nil {
		t.Fatal(err)
	}
	_ = x.SetCellValue("Notes", "A1", "hello")
	var buf bytes.Buffer
	if err := x.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A 1x1 PNG.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfe\xa7\x35\x81\x84\x00\x00\x00\x00IEND\xaeB`\x82")

func TestUploadAndExtract(t *testing.T) {
	e := setup(t)
	done := make(chan string, 10)
	e.svc.OnProcessed(func(_ context.Context, f File) { done <- f.Status })

	txt := e.upload("alice", "notes.txt", []byte("hello\xffworld"), "")
	if txt.Data.Status != Pending || txt.UserID != "alice" || txt.Meta["name"] != "notes.txt" ||
		txt.Meta["size"] != float64(11) || !strings.HasPrefix(txt.Meta["content_type"].(string), "text/plain") || txt.CreatedAt == 0 {
		t.Fatalf("upload response: %+v", txt)
	}
	if f := e.wait(txt.ID); f.Status != Completed {
		t.Fatalf("txt: %+v", f)
	}
	if got := e.text(txt.ID); got != "hello\uFFFDworld" {
		t.Fatalf("txt text %q", got)
	}
	<-done

	csvF := e.upload("alice", "t.csv", []byte("a,b\n1,2\n"), "")
	e.wait(csvF.ID)
	if got := e.text(csvF.ID); got != "a,b\n1,2\n" {
		t.Fatalf("csv text %q", got)
	}

	page := `<html><head><title>T</title><style>p{}</style><script>alert(1)</script></head>
		<body><h1>Head</h1><p>One   two</p><div>Three<br>Four</div><table><tr><td>a</td><td>b</td></tr></table></body></html>`
	htmlF := e.upload("alice", "p.html", []byte(page), "")
	e.wait(htmlF.ID)
	got := e.text(htmlF.ID)
	for _, want := range []string{"Head\n", "One two", "Three\nFour"} {
		if !strings.Contains(got, want) {
			t.Fatalf("html text %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "alert") || strings.Contains(got, "p{}") {
		t.Fatalf("html text kept script/style: %q", got)
	}

	xl := e.upload("alice", "book.xlsx", xlsxBytes(t), "")
	f := e.wait(xl.ID)
	if f.Status != Completed {
		t.Fatalf("xlsx: %+v", f)
	}
	got = e.text(xl.ID)
	if !strings.Contains(got, "## Sheet: Data\n") || !strings.Contains(got, "## Sheet: Notes\n") || !strings.Contains(got, `"Ana, B"`) {
		t.Fatalf("xlsx text %q", got)
	}
	if fmt.Sprint(f.Meta["sheets"]) != "[Data Notes]" {
		t.Fatalf("xlsx sheets %v", f.Meta["sheets"])
	}

	img := e.upload("alice", "dot.png", pngBytes, "")
	if f := e.wait(img.ID); f.Status != Completed || f.ContentType != "image/png" {
		t.Fatalf("png: %+v", f)
	}
	if e.text(img.ID) != "" {
		t.Fatal("image has text")
	}

	var st map[string]string
	e.json("alice", "GET", "/api/v1/files/"+txt.ID+"/process/status", 200, &st)
	if st["status"] != Completed {
		t.Fatalf("status %v", st)
	}
	var dc map[string]string
	e.json("alice", "GET", "/api/v1/files/"+csvF.ID+"/data/content", 200, &dc)
	if dc["content"] != "a,b\n1,2\n" {
		t.Fatalf("data/content %v", dc)
	}
	var fr fileResp
	e.json("alice", "GET", "/api/v1/files/"+xl.ID, 200, &fr)
	if fr.Data.Status != Completed || fr.Filename != "book.xlsx" {
		t.Fatalf("get: %+v", fr)
	}
	var list []fileResp
	e.json("alice", "GET", "/api/v1/files/", 200, &list)
	if len(list) != 5 {
		t.Fatalf("list: %d", len(list))
	}
	for i := 1; i < 5; i++ { // one was consumed above
		if st := <-done; st != Completed {
			t.Fatalf("hook saw %s", st)
		}
	}
}

func TestUploadOptions(t *testing.T) {
	e := setup(t)
	f := e.upload("alice", "raw.bin", []byte{1, 2, 3}, "?process=false")
	if f.Data.Status != Completed {
		t.Fatalf("process=false: %+v", f)
	}
	body, ct := multipartBody(t, "m.txt", []byte("x"), `{"source":"kb"}`)
	resp := e.do("alice", "POST", "/api/v1/files/", body, ct)
	var fr fileResp
	_ = json.NewDecoder(resp.Body).Decode(&fr)
	if fmt.Sprint(fr.Meta["data"]) != "map[source:kb]" {
		t.Fatalf("metadata: %+v", fr.Meta)
	}
	// metadata after the file part is merged too
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "late.txt")
	_, _ = fw.Write([]byte("y"))
	_ = mw.WriteField("metadata", `{"k":1}`)
	_ = mw.Close()
	resp = e.do("alice", "POST", "/api/v1/files", &buf, mw.FormDataContentType())
	_ = json.NewDecoder(resp.Body).Decode(&fr)
	got, _ := e.svc.Get(context.Background(), fr.ID)
	if fmt.Sprint(got.Meta["data"]) != "map[k:1]" {
		t.Fatalf("late metadata: %+v", got.Meta)
	}
	if r := e.do("alice", "POST", "/api/v1/files/", strings.NewReader("x"), "text/plain"); r.StatusCode != 400 {
		t.Fatalf("non-multipart: %d", r.StatusCode)
	}
	// bad metadata after the file part: 400 and the stored blob is dropped
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	fw, _ = mw.CreateFormFile("file", "orphan.txt")
	_, _ = fw.Write([]byte("z"))
	_ = mw.WriteField("metadata", `{bad`)
	_ = mw.Close()
	before, _ := e.svc.List(context.Background(), "alice")
	if r := e.do("alice", "POST", "/api/v1/files/", &buf, mw.FormDataContentType()); r.StatusCode != 400 {
		t.Fatalf("bad late metadata: %d", r.StatusCode)
	}
	if after, _ := e.svc.List(context.Background(), "alice"); len(after) != len(before) {
		t.Fatalf("orphan row left: %d -> %d", len(before), len(after))
	}
}

func TestCrossOrigin(t *testing.T) {
	e := setup(t)
	f := e.upload("alice", "c.txt", []byte("c"), "")
	cookieReq := func(method, path string, body io.Reader, ctype string) int {
		req, _ := http.NewRequest(method, e.url+path, body)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: e.users["alice"]})
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := cookieReq("DELETE", "/api/v1/files/"+f.ID, nil, ""); c != http.StatusUnauthorized { // cookie ignored cross-site (auth.Credential)
		t.Fatalf("cross-site cookie DELETE: %d", c)
	}
	body, ct := multipartBody(t, "x.txt", []byte("x"), "")
	if c := cookieReq("POST", "/api/v1/files/", body, ct); c != http.StatusUnauthorized {
		t.Fatalf("cross-site cookie upload: %d", c)
	}
	if c := cookieReq("GET", "/api/v1/files/"+f.ID+"/content", nil, ""); c != 200 {
		t.Fatalf("cross-site cookie GET: %d", c)
	}
	// a Bearer header is not ambient, so it passes
	req, _ := http.NewRequest("DELETE", e.url+"/api/v1/files/"+f.ID, nil)
	req.Header.Set("Authorization", "Bearer "+e.users["alice"])
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("bearer DELETE: %d", resp.StatusCode)
	}
}

func preview(e *env, id, q string) Preview {
	var p Preview
	e.json("alice", "GET", "/api/v1/files/"+id+"/preview"+q, 200, &p)
	return p
}

func TestPreview(t *testing.T) {
	e := setup(t)
	csvData := "\xEF\xBB\xBFname,note\n\"Smith, J\",\"line1\nline2\"\n\"say \"\"hi\"\"\",x\n"
	c := e.upload("alice", "q.csv", []byte(csvData), "")
	p := preview(e, c.ID, "")
	want := [][]string{{"name", "note"}, {"Smith, J", "line1\nline2"}, {`say "hi"`, "x"}}
	if p.Kind != "csv" || fmt.Sprint(p.Rows) != fmt.Sprint(want) || p.Truncated {
		t.Fatalf("csv preview %+v", p)
	}

	lazy := e.upload("alice", "bad.csv", []byte("a,b\"c,d\n1,2,3\n"), "")
	p = preview(e, lazy.ID, "")
	if len(p.Rows) != 2 || p.Rows[0][1] != `b"c` {
		t.Fatalf("lazy csv preview %+v", p)
	}

	tsv := e.upload("alice", "t.tsv", []byte("a\tb\n1\t2\n"), "")
	if p = preview(e, tsv.ID, ""); fmt.Sprint(p.Rows) != "[[a b] [1 2]]" {
		t.Fatalf("tsv preview %+v", p)
	}

	var big strings.Builder
	for i := 0; i < MaxPreviewRows+5; i++ {
		big.WriteString(strings.Repeat("v,", MaxPreviewCols+3) + "v\n")
	}
	bf := e.upload("alice", "big.csv", []byte(big.String()), "")
	if p = preview(e, bf.ID, ""); len(p.Rows) != MaxPreviewRows || len(p.Rows[0]) != MaxPreviewCols || !p.Truncated {
		t.Fatalf("big csv: rows %d cols %d trunc %v", len(p.Rows), len(p.Rows[0]), p.Truncated)
	}

	xl := e.upload("alice", "book.xlsx", xlsxBytes(t), "")
	p = preview(e, xl.ID, "")
	if p.Kind != "xlsx" || fmt.Sprint(p.Sheets) != "[Data Notes]" || p.Sheet != 0 || len(p.Rows) != 2 {
		t.Fatalf("xlsx preview %+v", p)
	}
	if d := p.Rows[1][1]; d == "46096" || !strings.Contains(d, "15") || !strings.Contains(d, "3") {
		t.Fatalf("xlsx date shown as %q", d)
	}
	if p = preview(e, xl.ID, "?sheet=1"); p.Sheet != 1 || fmt.Sprint(p.Rows) != "[[hello]]" {
		t.Fatalf("xlsx sheet 1 %+v", p)
	}

	tx := e.upload("alice", "a.md", []byte("# T\r\nbody\n"), "")
	if p = preview(e, tx.ID, ""); p.Kind != "text" || fmt.Sprint(p.Rows) != "[[# T] [body] []]" {
		t.Fatalf("text preview %+v", p)
	}
	img := e.upload("alice", "dot.png", pngBytes, "")
	if p = preview(e, img.ID, ""); p.Kind != "image" || len(p.Rows) != 0 || p.Sheets == nil {
		t.Fatalf("image preview %+v", p)
	}
	pdf := e.upload("alice", "doc.pdf", []byte("%PDF-1.4\n"), "")
	if p = preview(e, pdf.ID, ""); p.Kind != "pdf" {
		t.Fatalf("pdf preview %+v", p)
	}
}

func TestContentHeaders(t *testing.T) {
	e := setup(t)
	pdf := e.upload("alice", "Q1 report é.pdf", []byte("%PDF-1.4\n%%EOF\n"), "")
	r := e.do("alice", "GET", "/api/v1/files/"+pdf.ID+"/content", nil, "")
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" ||
		!strings.HasPrefix(r.Header.Get("Content-Disposition"), "inline;") || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("pdf headers %v", r.Header)
	}
	r = e.do("alice", "GET", "/api/v1/files/"+pdf.ID+"/content?attachment=true", nil, "")
	if cd := r.Header.Get("Content-Disposition"); cd != `attachment; filename="Q1 report _.pdf"; filename*=UTF-8''Q1%20report%20%C3%A9.pdf` {
		t.Fatalf("attachment disposition %q", cd)
	}
	b, _ := io.ReadAll(r.Body)
	if string(b) != "%PDF-1.4\n%%EOF\n" {
		t.Fatalf("body %q", b)
	}
	htm := e.upload("alice", "x.html", []byte("<script>alert(1)</script>"), "")
	r = e.do("alice", "GET", "/api/v1/files/"+htm.ID+"/content/anything.html", nil, "")
	if !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment;") || r.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("html headers %v", r.Header)
	}
	bin := e.upload("alice", "a.zip", []byte("PK\x03\x04"), "?process=false")
	r = e.do("alice", "GET", "/api/v1/files/"+bin.ID+"/content", nil, "")
	if !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("zip disposition %v", r.Header)
	}
	req, _ := http.NewRequest("GET", e.url+"/api/v1/files/"+pdf.ID+"/content", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: e.users["alice"]})
	req.Header.Set("Range", "bytes=0-3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); resp.StatusCode != 206 || string(b) != "%PDF" {
		t.Fatalf("cookie+range: %d %q", resp.StatusCode, b)
	}
}

func TestAccess(t *testing.T) {
	e := setup(t)
	f := e.upload("alice", "secret.txt", []byte("s"), "")
	e.wait(f.ID)
	for _, p := range []string{"", "/content", "/data/content", "/preview", "/process/status"} {
		e.json("bob", "GET", "/api/v1/files/"+f.ID+p, 404, nil)
	}
	e.json("bob", "DELETE", "/api/v1/files/"+f.ID, 404, nil)
	e.json("bob", "GET", "/api/v1/files/nope", 404, nil)
	if r := e.do("", "GET", "/api/v1/files/"+f.ID, nil, ""); r.StatusCode != 401 {
		t.Fatalf("anonymous: %d", r.StatusCode)
	}
	var list []fileResp
	e.json("bob", "GET", "/api/v1/files/?all=true", 200, &list)
	if len(list) != 0 {
		t.Fatalf("bob sees %d files", len(list))
	}
	e.json("root", "GET", "/api/v1/files/?all=true", 200, &list)
	if len(list) != 1 {
		t.Fatalf("admin all: %d", len(list))
	}
	e.json("root", "GET", "/api/v1/files/"+f.ID, 200, nil)

	// A chat bob owns that links the file grants read, not delete.
	ctx := context.Background()
	if _, err := e.a.DB.Exec(ctx, `INSERT INTO chat (id, user_id) VALUES ('c1', 'bob')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.DB.Exec(ctx, `INSERT INTO chat_file (chat_id, file_id) VALUES ('c1', $1)`, f.ID); err != nil {
		t.Fatal(err)
	}
	e.json("bob", "GET", "/api/v1/files/"+f.ID+"/content", 200, nil)
	e.json("bob", "DELETE", "/api/v1/files/"+f.ID, 403, nil)

	g := e.upload("alice", "kb.txt", []byte("k"), "")
	e.json("bob", "GET", "/api/v1/files/"+g.ID, 404, nil)
	e.svc.RegisterAccessCheck(func(_ context.Context, u *auth.User, id string) (bool, error) {
		return u.ID == "bob" && id == g.ID, nil
	})
	e.json("bob", "GET", "/api/v1/files/"+g.ID, 200, nil)
}

func TestDelete(t *testing.T) {
	e := setup(t)
	f := e.upload("alice", "d.txt", []byte("bye"), "")
	e.wait(f.ID)
	dir := filepath.Join(e.a.Env.DataDir, "uploads", f.ID)
	if _, err := os.Stat(filepath.Join(dir, "d.txt")); err != nil {
		t.Fatal(err)
	}
	e.json("alice", "DELETE", "/api/v1/files/"+f.ID, 200, nil)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("blob dir still there: %v", err)
	}
	e.json("alice", "GET", "/api/v1/files/"+f.ID, 404, nil)
	g := e.upload("alice", "e.txt", []byte("x"), "")
	e.json("root", "DELETE", "/api/v1/files/"+g.ID, 200, nil)
}

func TestSizeLimit(t *testing.T) {
	e := setup(t)
	if err := e.a.Settings.Set(context.Background(), "files.max_mb", 1, false); err != nil {
		t.Fatal(err)
	}
	body, ct := multipartBody(t, "big.txt", bytes.Repeat([]byte("a"), 1<<20+1), "")
	if r := e.do("alice", "POST", "/api/v1/files/", body, ct); r.StatusCode != 413 {
		t.Fatalf("status %d", r.StatusCode)
	}
	body, ct = multipartBody(t, "huge.txt", bytes.Repeat([]byte("a"), 3<<20), "")
	if r := e.do("alice", "POST", "/api/v1/files/", body, ct); r.StatusCode != 413 {
		t.Fatalf("over body cap: status %d", r.StatusCode)
	}
	// The service limit holds without the HTTP body cap too.
	if _, err := e.svc.Create(context.Background(), "alice", "big.txt", "", bytes.NewReader(make([]byte, 1<<20+1)), nil); err != ErrTooLarge {
		t.Fatalf("Create: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(e.a.Env.DataDir, "uploads"))
	if len(entries) != 0 {
		t.Fatalf("leftover upload dirs: %d", len(entries))
	}
	e.upload("alice", "ok.txt", bytes.Repeat([]byte("a"), 1<<20), "")
}

func TestSanitizeName(t *testing.T) {
	long := strings.Repeat("é", 150) + ".xlsx"
	cases := map[string]string{
		"../../etc/passwd":    "passwd",
		`C:\Users\x\evil.txt`: "evil.txt",
		"a\x00b\nc.txt":       "abc.txt",
		"..":                  "file",
		"":                    "file",
		"  ok name.md ":       "ok name.md",
		"bad\xffutf8.txt":     "badutf8.txt",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	got := SanitizeName(long)
	if len(got) > 200 || !strings.HasSuffix(got, ".xlsx") || !strings.HasPrefix(got, "éé") {
		t.Errorf("long name -> %q (%d bytes)", got, len(got))
	}
}

func TestTraversalUpload(t *testing.T) {
	e := setup(t)
	f := e.upload("alice", "../../../escape.txt", []byte("x"), "")
	if f.Filename != "escape.txt" {
		t.Fatalf("filename %q", f.Filename)
	}
	if _, err := os.Stat(filepath.Join(e.a.Env.DataDir, "uploads", f.ID, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.a.Env.DataDir), "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("file escaped the data dir")
	}
}

func TestNoPanic(t *testing.T) {
	_, _, err := noPanic(func() (string, map[string]any, error) { panic("bad xlsx") })
	if err == nil || !strings.Contains(err.Error(), "extraction crashed: bad xlsx") {
		t.Fatalf("panic not turned into an error: %v", err)
	}
	text, _, err := noPanic(func() (string, map[string]any, error) { return "ok", nil, nil })
	if text != "ok" || err != nil {
		t.Fatalf("normal result changed: %q %v", text, err)
	}
}

func TestRestartRecovery(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.svc.Close() // stop the workers, as if the process died
	var ids []string
	for i, status := range []string{Pending, Processing} {
		f, err := e.svc.store(ctx, "alice", fmt.Sprintf("r%d.txt", i), "", strings.NewReader("recovered"), nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.a.DB.Exec(ctx, `UPDATE file SET status = $2 WHERE id = $1`, f.ID, status); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	next := New(e.a.DB, e.a.Settings, e.a.Env.DataDir)
	if err := next.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	for _, id := range ids {
		f, err := next.Wait(ctx, id, 20*time.Second)
		if err != nil || f.Status != Completed {
			t.Fatalf("%s: %+v %v", id, f, err)
		}
	}
}

func TestDocumentsWithoutExtractor(t *testing.T) {
	e := setup(t)
	f := e.upload("alice", "doc.pdf", []byte("%PDF-1.4\n"), "")
	got := e.wait(f.ID)
	if got.Status != Failed || got.Error != "no document extractor configured for .pdf" {
		t.Fatalf("%+v", got)
	}
	var fr fileResp
	e.json("alice", "GET", "/api/v1/files/"+f.ID, 200, &fr)
	if fr.Data.Status != Failed || fr.Data.Error == "" {
		t.Fatalf("%+v", fr)
	}
	u := e.upload("alice", "blob.bin", []byte{0, 1, 2, 0xff, 0xfe}, "")
	if got := e.wait(u.ID); got.Status != Failed || !strings.Contains(got.Error, "unsupported") {
		t.Fatalf("binary: %+v", got)
	}
}

func TestExtractorStub(t *testing.T) {
	e := setup(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, hdr, err := r.FormFile("file")
		if r.URL.Path != "/extract" || err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		b, _ := io.ReadAll(file)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(hdr.Filename, ".odt") {
			w.WriteHeader(415)
			_, _ = w.Write([]byte(`{"detail":"unsupported type .odt"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pages": []map[string]any{{"n": 1, "text": "page one " + string(b)}, {"n": 2, "text": "two"}},
			"meta":  map[string]any{"pages": 2, "title": "Doc"},
		})
	}))
	defer stub.Close()
	if err := e.a.Settings.Set(context.Background(), "rag.extractor.url", stub.URL+"/", false); err != nil {
		t.Fatal(err)
	}
	f := e.upload("alice", "doc.pdf", []byte("PDF"), "")
	got := e.wait(f.ID)
	if got.Status != Completed || e.text(f.ID) != "page one PDF\n\ntwo" || got.Meta["title"] != "Doc" ||
		fmt.Sprint(got.Meta["pages"]) != "[map[chars:12 n:1] map[chars:3 n:2]]" {
		t.Fatalf("%+v text=%q", got, e.text(f.ID))
	}
	o := e.upload("alice", "x.odt", []byte("odt"), "")
	if got := e.wait(o.ID); got.Status != Failed || got.Error != "extractor: HTTP 415: unsupported type .odt" {
		t.Fatalf("%+v", got)
	}
}
