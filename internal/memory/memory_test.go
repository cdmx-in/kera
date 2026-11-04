package memory

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
	"github.com/cdmx-in/kera/internal/tools"
)

// bagEmbedder embeds text as a bag of hashed words (8 dims), so texts sharing
// words are close.
type bagEmbedder struct{ calls int }

func (b *bagEmbedder) Stream(context.Context, provider.Request) iter.Seq2[provider.Event, error] {
	return nil
}
func (b *bagEmbedder) ListModels(context.Context) ([]string, error) { return nil, nil }
func (b *bagEmbedder) Embed(_ context.Context, _ string, texts []string) ([][]float32, error) {
	b.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 8)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			h.Write([]byte(strings.Trim(w, ".,?!")))
			v[h.Sum32()%8]++
		}
		v[7] += 0.01
		out[i] = v
	}
	return out, nil
}

func setup(t *testing.T) (*Store, *app.App) {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	keys, _ := secret.Derive(strings.Repeat("k", 32))
	st := settings.New(pool)
	srv, err := api.New(config.Env{DataDir: t.TempDir()}, pool)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{DB: pool, Keys: keys, Settings: st, Server: srv, Auth: auth.New(pool, keys, st, false)}
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email, role, settings) VALUES
		('alice', 'alice@example.com', 'user', '{"ui":{"memory":true}}'), ('bob', 'bob@example.com', 'user', '{}')`); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, "chat.memory.enable", true, true); err != nil {
		t.Fatal(err)
	}
	return &Store{DB: pool, Settings: st}, a
}

var alice = &auth.User{ID: "alice", Role: "user", Permissions: auth.Permissions{"features": map[string]any{"memory": true}}}

func TestStoreFullText(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	if on, _ := s.Enabled(ctx, alice); !on {
		t.Fatal("alice should have memory on")
	}
	bob := &auth.User{ID: "bob", Permissions: alice.Permissions}
	if on, _ := s.Enabled(ctx, bob); on {
		t.Fatal("bob did not turn memory on")
	}
	m1, err := s.Add(ctx, "alice", "Prefers answers in Hindi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "alice", "Works in the finance team"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "bob", "Bob likes finance"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "alice", "   "); err == nil {
		t.Fatal("empty memory accepted")
	}
	hits, err := s.Search(ctx, "alice", "which team, finance?", 5)
	if err != nil || len(hits) != 1 || hits[0].Content != "Works in the finance team" {
		t.Fatalf("fts = %+v %v", hits, err)
	}
	if _, err := s.Update(ctx, "bob", m1.ID, "hijack"); err != ErrNotFound {
		t.Fatalf("cross-user update: %v", err)
	}
	if m, err := s.Update(ctx, "alice", m1.ID, "Prefers answers in English"); err != nil || m.Content != "Prefers answers in English" {
		t.Fatalf("update = %+v %v", m, err)
	}
	if err := s.Delete(ctx, "bob", m1.ID); err != ErrNotFound {
		t.Fatalf("cross-user delete: %v", err)
	}
	if err := s.Delete(ctx, "alice", m1.ID); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.List(ctx, "alice"); len(ms) != 1 {
		t.Fatalf("list = %+v", ms)
	}
}

func TestVectorSearchAndBackfill(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	emb := &bagEmbedder{}
	s.Embedder = func(context.Context, string) (provider.Client, error) { return emb, nil }
	for k, v := range map[string]any{"rag.embedding.connection": "ollama", "rag.embedding.model": "bag"} {
		if err := s.Settings.Set(ctx, k, v, true); err != nil {
			t.Fatal(err)
		}
	}
	// an imported memory has no vector until the first search
	if _, err := s.DB.Exec(ctx, `INSERT INTO memory (id, user_id, content) VALUES ('old', 'alice', 'Drives a red bicycle')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "alice", "Favourite language is Go"); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(ctx, "alice", "what bicycle", 1)
	if err != nil || len(hits) != 1 || hits[0].ID != "old" || hits[0].Score <= 0 {
		t.Fatalf("vector search = %+v %v", hits, err)
	}
	var missing int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM memory WHERE embedding IS NULL`).Scan(&missing)
	if missing != 0 {
		t.Fatalf("%d memories not backfilled", missing)
	}
	var dim int
	_ = s.DB.QueryRow(ctx, `SELECT dim FROM embedding_model WHERE model = 'bag'`).Scan(&dim)
	if dim != 8 {
		t.Fatalf("embedding model dim = %d", dim)
	}
}

func TestProviderAndRoutes(t *testing.T) {
	_, a := setup(t)
	if err := Mount(a); err != nil {
		t.Fatal(err)
	}
	s := For(a)
	ctx := context.Background()
	p := &Provider{Store: s}
	if specs, _ := p.Tools(ctx, alice); len(specs) != 5 {
		t.Fatalf("specs = %d", len(specs))
	}
	var events []tools.Event
	call := func(name, args string) tools.Result {
		res, err := p.Execute(ctx, tools.Call{Name: name, Args: json.RawMessage(args), User: alice,
			Emit: func(e tools.Event) { events = append(events, e) }})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := call("add_memory", `{"content":"Lives in Pune"}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Content), &m); err != nil || m["id"] == nil {
		t.Fatalf("add = %s", res.Content)
	}
	if res := call("search_memories", `{"query":"pune"}`); !strings.Contains(res.Content, "Lives in Pune") {
		t.Fatalf("search = %s", res.Content)
	}
	if res := call("delete_memory", `{"id":"nope"}`); !res.IsError {
		t.Fatal("deleting a missing memory succeeded")
	}
	if res := call("add_memory", `{"content":""}`); !res.IsError {
		t.Fatal("empty memory accepted")
	}
	if len(events) == 0 || events[0].Data.(map[string]any)["action"] != "memory" {
		t.Fatalf("events = %+v", events)
	}

	srv := httptest.NewServer(a.Server.Handler())
	defer srv.Close()
	tok, _ := auth.IssueToken(a.Keys, "alice", time.Hour)
	do := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := do("POST", "/api/v1/memories/add", `{"content":"Uses a standing desk"}`); code != 200 || !strings.Contains(body, `"created_at"`) {
		t.Fatalf("add = %d %s", code, body)
	}
	code, body := do("GET", "/api/v1/memories/", "")
	var ms []map[string]any
	_ = json.Unmarshal([]byte(body), &ms)
	if code != 200 || len(ms) != 2 {
		t.Fatalf("list = %d %s", code, body)
	}
	id := ms[0]["id"].(string)
	if code, body := do("POST", "/api/v1/memories/"+id+"/update", `{"content":"Uses a treadmill desk"}`); code != 200 || !strings.Contains(body, "treadmill") {
		t.Fatalf("update = %d %s", code, body)
	}
	if code, body := do("POST", "/api/v1/memories/query", `{"content":"treadmill","k":3}`); code != 200 || !strings.Contains(body, "treadmill") {
		t.Fatalf("query = %d %s", code, body)
	}
	if code, _ := do("DELETE", "/api/v1/memories/"+id, ""); code != 200 {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := do("DELETE", "/api/v1/memories/"+id, ""); code != 404 {
		t.Fatalf("second delete = %d", code)
	}
	if code, _ := do("DELETE", "/api/v1/memories/delete/user", ""); code != 200 {
		t.Fatalf("delete all = %d", code)
	}
	if ms, _ := s.List(ctx, "alice"); len(ms) != 0 {
		t.Fatalf("left = %+v", ms)
	}
}
