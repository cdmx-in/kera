package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"iter"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/retrieval"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
	"github.com/cdmx-in/kera/internal/tools"
)

// stubEmbed is a bag-of-words embedder: each word lands in a hashed bucket,
// so texts sharing words are close. Model "wide" has twice the dimension.
type stubEmbed struct {
	fail  atomic.Bool
	calls atomic.Int64
}

func (s *stubEmbed) ConnectionClient(context.Context, string) (provider.Client, error) { return s, nil }
func (s *stubEmbed) ListModels(context.Context) ([]string, error)                      { return nil, nil }
func (s *stubEmbed) Stream(context.Context, provider.Request) iter.Seq2[provider.Event, error] {
	return func(func(provider.Event, error) bool) {}
}

func (s *stubEmbed) Embed(_ context.Context, model string, texts []string) ([][]float32, error) {
	s.calls.Add(1)
	if s.fail.Load() {
		return nil, errors.New("embedding server down")
	}
	dim := 16
	if model == "wide" {
		dim = 32
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, dim)
		v[0] = 0.01
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(strings.Trim(w, ".,?!#")))
			v[h.Sum32()%uint32(dim)]++
		}
		var n float64
		for _, x := range v {
			n += float64(x * x)
		}
		for j := range v {
			v[j] /= float32(math.Sqrt(n))
		}
		out[i] = v
	}
	return out, nil
}

type env struct {
	t      *testing.T
	s      *Service
	emb    *stubEmbed
	url    string
	st     *settings.Store
	users  map[string]string
	users2 map[string]*auth.User
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
	cfg := config.Env{DataDir: t.TempDir()}
	srv, err := api.New(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{Env: cfg, DB: pool, Keys: keys, Settings: st, Server: srv, Auth: auth.New(pool, keys, st, false)}
	for k, v := range map[string]any{
		"rag.embedding.connection": "ollama", "rag.embedding.model": "stub", "rag.chunk_size": 200, "rag.chunk_overlap": 20,
		"user.permissions": map[string]any{"chat": map[string]any{"file_upload": true}, "workspace": map[string]any{"knowledge": true}},
	} {
		if err := st.Set(ctx, k, v, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := files.Mount(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.For(a).Close)
	emb := &stubEmbed{}
	s := New(pool, st, files.For(a), nil, emb)
	regMu.Lock()
	reg[a] = s
	regMu.Unlock()
	if err := Mount(a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &env{t: t, s: s, emb: emb, url: ts.URL, st: st, users: map[string]string{}, users2: map[string]*auth.User{}}
	for _, u := range []struct{ id, role string }{{"alice", "user"}, {"bob", "user"}, {"root", "admin"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email, role) VALUES ($1, $1 || '@example.com', $2)`, u.id, u.role); err != nil {
			t.Fatal(err)
		}
		tok, err := auth.IssueToken(keys, u.id, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		e.users[u.id] = tok
		lu, err := a.Auth.LoadUser(ctx, u.id)
		if err != nil {
			t.Fatal(err)
		}
		e.users2[u.id] = lu
	}
	return e
}

func (e *env) req(user, method, path string, body any, want int, out any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.url+path, rd)
	req.Header.Set("Authorization", "Bearer "+e.users[user])
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s as %s: status %d, want %d: %s", method, path, user, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

func (e *env) upload(user, name, content string) string {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write([]byte(content))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", e.url+"/api/v1/files/", &buf)
	req.Header.Set("Authorization", "Bearer "+e.users[user])
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var f struct{ ID string }
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || json.Unmarshal(b, &f) != nil {
		e.t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	return f.ID
}

type fileItem struct {
	ID       string         `json:"id"`
	Filename string         `json:"filename"`
	Meta     map[string]any `json:"meta"`
	Chunks   int            `json:"chunks"`
	Data     struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"data"`
}

// waitFile polls the collection's file list until fid reaches status.
func (e *env) waitFile(kid, fid, status string) fileItem {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var out struct {
			Items []fileItem `json:"items"`
			Total int        `json:"total"`
		}
		e.req("root", "GET", "/api/v1/knowledge/"+kid+"/files", nil, 200, &out)
		for _, f := range out.Items {
			if f.ID == fid && f.Data.Status == status {
				return f
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("file %s never reached %s: %+v", fid, status, out.Items)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

const payoutDoc = `# Payouts
A payout moves money to a beneficiary.
## Statuses
A payout goes from GO_AHEAD to PAID after the bank confirms. UNDER_REVIEW payouts wait for compliance.
## Fees
Fees are charged per corridor and shown on the client portal invoice.
# Roles
Four-eyes approval needs a maker and a checker in the management portal.
`

type coll struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	WriteAccess bool           `json:"write_access"`
	Status      map[string]int `json:"status"`
	Files       []fileItem     `json:"files"`
	Embedding   *struct {
		Model string `json:"model"`
		Dim   int    `json:"dim"`
	} `json:"embedding_model"`
}

func TestCollectionsIngestSearchAccess(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var c coll
	e.req("alice", "POST", "/api/v1/knowledge/create", map[string]any{"name": "Payments KB", "description": "payments", "access_control": map[string]any{}}, 200, &c)
	e.req("bob", "POST", "/api/v1/knowledge/create", map[string]any{"name": ""}, 400, nil)

	// A file added while still pending is indexed once extraction completes.
	fid := e.upload("alice", "payouts.md", payoutDoc)
	e.req("alice", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": fid}, 200, &c)
	f := e.waitFile(c.ID, fid, "completed")
	if f.Chunks < 3 || f.Filename != "payouts.md" || f.Meta["name"] != "payouts.md" {
		t.Fatalf("indexed file %+v", f)
	}
	var idx int
	if err := e.s.db.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname LIKE 'chunk_hnsw_%'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("hnsw index count %d err %v", idx, err)
	}
	var headers []string
	if err := e.s.db.QueryRow(ctx, `SELECT meta->'headers' FROM chunk WHERE text LIKE '%GO_AHEAD%'`).Scan(&headers); err != nil ||
		strings.Join(headers, ">") != "Payouts>Statuses" {
		t.Fatalf("breadcrumb %v %v", headers, err)
	}

	// List and get shapes.
	var list struct {
		Items []coll `json:"items"`
		Total int    `json:"total"`
	}
	e.req("alice", "GET", "/api/v1/knowledge/", nil, 200, &list)
	if list.Total != 1 || list.Items[0].Name != "Payments KB" || !list.Items[0].WriteAccess || list.Items[0].Status["completed"] != 1 ||
		list.Items[0].Embedding == nil || list.Items[0].Embedding.Dim != 16 {
		t.Fatalf("list %+v", list)
	}
	e.req("alice", "GET", "/api/v1/knowledge/"+c.ID, nil, 200, &c)
	if len(c.Files) != 1 {
		t.Fatalf("get files %+v", c.Files)
	}

	// Compat retrieval query.
	var q struct {
		Documents [][]string         `json:"documents"`
		Metadatas [][]map[string]any `json:"metadatas"`
		Distances [][]float64        `json:"distances"`
	}
	e.req("alice", "POST", "/api/v1/retrieval/query/collection", map[string]any{"collection_names": []string{c.ID}, "query": "which status follows GO_AHEAD once the bank confirms", "k": 2}, 200, &q)
	if len(q.Documents) != 1 || len(q.Documents[0]) != 2 || len(q.Metadatas[0]) != 2 || len(q.Distances[0]) != 2 {
		t.Fatalf("query shape %+v", q)
	}
	if !strings.Contains(q.Documents[0][0], "GO_AHEAD") || q.Metadatas[0][0]["file_id"] != fid || q.Metadatas[0][0]["name"] != "payouts.md" ||
		q.Metadatas[0][0]["source"] != "payouts.md" {
		t.Fatalf("top hit %q %v", q.Documents[0][0], q.Metadatas[0][0])
	}

	// Bob sees nothing until granted.
	e.req("bob", "GET", "/api/v1/knowledge/"+c.ID, nil, 404, nil)
	e.req("bob", "GET", "/api/v1/files/"+fid, nil, 404, nil)
	e.req("bob", "GET", "/api/v1/knowledge/", nil, 200, &list)
	if list.Total != 0 {
		t.Fatalf("bob sees %+v", list)
	}
	hits, err := e.s.Search(ctx, e.users2["bob"], []string{c.ID}, "payout", 3)
	if err != nil || len(hits) != 0 {
		t.Fatalf("bob search %v %v", hits, err)
	}
	e.req("bob", "PUT", "/api/v1/knowledge/"+c.ID+"/grants", []map[string]string{{"principal": "user:bob", "permission": "read"}}, 404, nil)
	e.req("alice", "PUT", "/api/v1/knowledge/"+c.ID+"/grants", []map[string]string{{"principal": "user:bob", "permission": "read"}}, 200, nil)
	e.req("bob", "GET", "/api/v1/knowledge/"+c.ID, nil, 200, &c)
	if c.WriteAccess {
		t.Fatal("read grant must not give write access")
	}
	e.req("bob", "GET", "/api/v1/files/"+fid, nil, 200, nil) // collection readers may read its files
	e.req("bob", "POST", "/api/v1/knowledge/"+c.ID+"/update", map[string]any{"name": "x"}, 403, nil)
	e.req("bob", "DELETE", "/api/v1/knowledge/"+c.ID+"/delete", nil, 403, nil)
	bobFile := e.upload("bob", "b.md", "bob notes")
	e.req("bob", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": bobFile}, 403, nil)
	// Alice cannot attach a file she cannot read.
	e.req("alice", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": bobFile}, 404, nil)

	// Retriever: Resolve by name or id, Documents, List.
	bob := e.users2["bob"]
	rc, err := e.s.Resolve(ctx, bob, []string{"missing", "Payments KB"})
	if err != nil || len(rc) != 1 || rc[0].ID != c.ID {
		t.Fatalf("resolve by name %+v %v", rc, err)
	}
	rc, _ = e.s.Resolve(ctx, e.users2["root"], []string{c.ID})
	if len(rc) != 1 {
		t.Fatal("resolve by id")
	}
	docs, err := e.s.Documents(ctx, bob, "Payments KB")
	if err != nil || len(docs) != 1 || !strings.Contains(docs[0].Text, "Four-eyes") || docs[0].Name != "payouts.md" {
		t.Fatalf("documents %+v %v", docs, err)
	}
	hits, err = e.s.Search(ctx, bob, []string{"Payments KB"}, "maker checker approval", 1)
	if err != nil || len(hits) != 1 || !strings.Contains(hits[0].Text, "checker") || hits[0].FileName != "payouts.md" {
		t.Fatalf("search %+v %v", hits, err)
	}
	if retrieval.Get() == nil {
		t.Fatal("retriever not registered")
	}

	// Remove the file: chunks go, the file stays.
	e.req("alice", "POST", "/api/v1/knowledge/"+c.ID+"/file/remove", map[string]any{"file_id": fid}, 200, &c)
	var n int
	_ = e.s.db.QueryRow(ctx, `SELECT count(*) FROM chunk WHERE knowledge_id = $1`, c.ID).Scan(&n)
	if n != 0 || len(c.Files) != 0 {
		t.Fatalf("chunks %d files %+v", n, c.Files)
	}
	e.req("alice", "DELETE", "/api/v1/knowledge/"+c.ID+"/delete", nil, 200, nil)
	_ = e.s.db.QueryRow(ctx, `SELECT count(*) FROM access_grant WHERE resource_id = $1`, c.ID).Scan(&n)
	if n != 0 {
		t.Fatal("grants survive delete")
	}
	e.req("alice", "GET", "/api/v1/knowledge/"+c.ID, nil, 404, nil)
}

func TestIngestionStateMachine(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var c coll
	e.req("root", "POST", "/api/v1/knowledge/create", map[string]any{"name": "HR"}, 200, &c)

	// Embedding failure -> failed with the reason; reindex after recovery -> completed.
	e.emb.fail.Store(true)
	fid := e.upload("root", "leave.md", "# Leave\nTwenty days of paid leave per year.")
	e.req("root", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": fid}, 200, nil)
	f := e.waitFile(c.ID, fid, "failed")
	if !strings.Contains(f.Data.Error, "embedding server down") {
		t.Fatalf("error %+v", f)
	}
	e.emb.fail.Store(false)
	e.req("root", "POST", "/api/v1/knowledge/"+c.ID+"/reindex", nil, 200, nil)
	if f = e.waitFile(c.ID, fid, "completed"); f.Data.Error != "" {
		t.Fatalf("error not cleared: %+v", f)
	}

	// A file whose extraction fails fails in the collection too.
	var bad string
	if err := e.s.db.QueryRow(ctx, `INSERT INTO file (id, user_id, filename, path, status, error) VALUES ('bad', 'root', 'x.pdf', 'nowhere', 'failed', 'cannot extract')
		RETURNING id`).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	e.req("root", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": bad}, 200, nil)
	if f = e.waitFile(c.ID, bad, "failed"); f.Data.Error != "cannot extract" {
		t.Fatalf("extraction failure %+v", f)
	}

	// Imported rows: pending link to a file that already has text.
	if _, err := e.s.db.Exec(ctx, `INSERT INTO file (id, user_id, filename, path, status, text) VALUES ('imp', 'root', 'imp.md', 'nowhere', 'completed', 'Imported policy text about travel.')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec(ctx, `INSERT INTO knowledge_file (knowledge_id, file_id, status, origin) VALUES ($1, 'imp', 'pending', 'openwebui')`, c.ID); err != nil {
		t.Fatal(err)
	}
	e.s.Wake()
	e.waitFile(c.ID, "imp", "completed")

	// Re-adding a completed file does not re-index it; file/update does.
	calls := e.emb.calls.Load()
	e.req("root", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": "imp"}, 200, nil)
	time.Sleep(200 * time.Millisecond)
	if e.emb.calls.Load() != calls {
		t.Fatal("re-adding a completed file re-embedded it")
	}

	// A row left processing by a crash is retried on Start.
	if _, err := e.s.db.Exec(ctx, `UPDATE knowledge_file SET status = 'processing' WHERE file_id = 'imp'`); err != nil {
		t.Fatal(err)
	}
	e.s.Close()
	e.s.stop, e.s.once = make(chan struct{}), sync.Once{}
	if err := e.s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	e.waitFile(c.ID, "imp", "completed")
}

func TestReindexToAnotherModel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var c coll
	e.req("root", "POST", "/api/v1/knowledge/create", map[string]any{"name": "Docs"}, 200, &c)
	fid := e.upload("root", "a.md", "alpha beta gamma\n\ndelta epsilon")
	e.req("root", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": fid}, 200, nil)
	e.waitFile(c.ID, fid, "completed")
	// Moving to another model is admin-only; a write grant is not enough.
	e.req("root", "PUT", "/api/v1/knowledge/"+c.ID+"/grants", []map[string]string{{"principal": "user:alice", "permission": "write"}}, 200, nil)
	e.req("alice", "POST", "/api/v1/knowledge/"+c.ID+"/reindex?embedding_model=wide", nil, 403, nil)
	e.req("alice", "POST", "/api/v1/knowledge/"+c.ID+"/reindex?connection=other", nil, 403, nil)
	e.req("root", "POST", "/api/v1/knowledge/"+c.ID+"/reindex?embedding_model=wide", nil, 200, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.req("root", "GET", "/api/v1/knowledge/"+c.ID, nil, 200, &c)
		if c.Embedding != nil && c.Embedding.Model == "wide" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never switched: %+v", c.Embedding)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var models, idx int
	_ = e.s.db.QueryRow(ctx, `SELECT count(DISTINCT embedding_model_id) FROM chunk WHERE knowledge_id = $1`, c.ID).Scan(&models)
	_ = e.s.db.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname LIKE 'chunk_hnsw_%'`).Scan(&idx)
	if models != 1 || c.Embedding.Dim != 32 || idx != 2 {
		t.Fatalf("models %d dim %d indexes %d", models, c.Embedding.Dim, idx)
	}
	hits, err := e.s.Search(ctx, e.users2["root"], []string{c.ID}, "delta", 1)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search after reindex %v %v", hits, err)
	}

	// A plain reindex cancels a move in progress and drops the chunks it wrote.
	old, err := e.s.ensureModel(ctx, "ollama", "stub")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec(ctx, `UPDATE knowledge SET meta = meta || jsonb_build_object('reindex_to', $2::text) WHERE id = $1`, c.ID, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec(ctx, `INSERT INTO chunk (knowledge_id, file_id, idx, text, embedding_model_id) VALUES ($1, $2, 99, 'stray', $3)`, c.ID, fid, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Reindex(ctx, c.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	var stray int
	_ = e.s.db.QueryRow(ctx, `SELECT count(*) FROM chunk WHERE knowledge_id = $1 AND embedding_model_id = $2`, c.ID, old.ID).Scan(&stray)
	if stray != 0 {
		t.Fatalf("%d chunks of the cancelled move left", stray)
	}
}

func TestRerankerAndThreshold(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var c coll
	e.req("root", "POST", "/api/v1/knowledge/create", map[string]any{"name": "R"}, 200, &c)
	fid := e.upload("root", "r.md", "# One\napples\n# Two\nbananas\n# Three\ncherries")
	e.req("root", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": fid}, 200, nil)
	e.waitFile(c.ID, fid, "completed")
	// The reranker prefers the document mentioning cherries, scores the rest low.
	rr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var res []map[string]any
		for i, d := range body.Documents {
			score := 0.1
			if strings.Contains(d, "cherries") {
				score = 0.95
			}
			res = append(res, map[string]any{"index": i, "relevance_score": score})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": res})
	}))
	defer rr.Close()
	_ = e.st.Set(ctx, "rag.reranker.url", rr.URL, false)
	_ = e.st.Set(ctx, "rag.relevance_threshold", 0.5, false)
	hits, err := e.s.Search(ctx, e.users2["root"], []string{c.ID}, "apples", 3)
	if err != nil || len(hits) != 1 || !strings.Contains(hits[0].Text, "cherries") || hits[0].Score != 0.95 {
		t.Fatalf("reranked %+v %v", hits, err)
	}
	// A broken reranker falls back to the fused order.
	_ = e.st.Set(ctx, "rag.reranker.url", "http://127.0.0.1:1", false)
	hits, err = e.s.Search(ctx, e.users2["root"], []string{c.ID}, "apples", 3)
	if err != nil || len(hits) != 3 || !strings.Contains(hits[0].Text, "apples") {
		t.Fatalf("fallback %+v %v", hits, err)
	}
}

func TestKnowledgeTools(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var c coll
	e.req("alice", "POST", "/api/v1/knowledge/create", map[string]any{"name": "Policies", "description": "HR"}, 200, &c)
	fid := e.upload("alice", "travel-policy.md", "# Travel\nEconomy class for flights under six hours.")
	e.req("alice", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": fid}, 200, nil)
	e.waitFile(c.ID, fid, "completed")
	p, _ := tools.Get(ctx, ToolSetID)
	if p == nil {
		t.Fatal("tool set not registered")
	}
	specs, _ := p.Tools(ctx, e.users2["alice"])
	if len(specs) != 4 {
		t.Fatalf("specs %+v", specs)
	}
	var events []tools.Event
	call := func(user, name, args string) tools.Result {
		r, err := p.Execute(ctx, tools.Call{Name: name, Args: json.RawMessage(args), User: e.users2[user],
			Emit: func(ev tools.Event) { events = append(events, ev) }})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call("alice", "list_knowledge", `{}`); !strings.Contains(r.Content, `"Policies"`) || !strings.Contains(r.Content, `"files":1`) {
		t.Fatal(r.Content)
	}
	if r := call("alice", "search_knowledge_files", `{"query":"travel"}`); !strings.Contains(r.Content, fid) {
		t.Fatal(r.Content)
	}
	if r := call("alice", "search_knowledge_files", `{"query":"payroll"}`); r.Content != "[]" {
		t.Fatal(r.Content)
	}
	r := call("alice", "query_knowledge_files", `{"query":"flights economy"}`)
	if len(r.Sources) == 0 || r.Sources[0].FileID != fid || !strings.Contains(r.Content, "Economy") || len(events) != 2 {
		t.Fatalf("%+v %+v", r, events)
	}
	if r := call("alice", "view_knowledge_file", fmt.Sprintf(`{"file_id":%q,"offset":9,"max_chars":7}`, fid)); !strings.Contains(r.Content, `"text":"Economy"`) {
		t.Fatal(r.Content)
	}
	// Bob has no access: nothing listed, file not viewable.
	if r := call("bob", "list_knowledge", `{}`); r.Content != "[]" {
		t.Fatal(r.Content)
	}
	if r := call("bob", "view_knowledge_file", fmt.Sprintf(`{"file_id":%q}`, fid)); !r.IsError {
		t.Fatal(r.Content)
	}
}

// An index left invalid by an interrupted build is dropped and built again.
func TestEnsureIndexReplacesInvalid(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	m, err := e.s.ensureModel(ctx, "ollama", "stub")
	if err != nil {
		t.Fatal(err)
	}
	name := indexName(m.ID)
	for _, q := range []string{`CREATE TABLE dup (x int)`, `INSERT INTO dup VALUES (1), (1)`} {
		if _, err := e.s.db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.s.db.Exec(ctx, `CREATE UNIQUE INDEX CONCURRENTLY `+name+` ON dup (x)`); err == nil {
		t.Fatal("want a failed build")
	}
	if err := e.s.ensureIndex(ctx, m); err != nil {
		t.Fatal(err)
	}
	var table string
	var valid bool
	if err := e.s.db.QueryRow(ctx, `SELECT i.indrelid::regclass::text, i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = $1`, name).Scan(&table, &valid); err != nil || table != "chunk" || !valid {
		t.Fatalf("index on %q valid %v err %v", table, valid, err)
	}
}
