package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// docs is a throwaway corpus: file name -> markdown text.
type docs map[string]string

// collection creates a collection, uploads and indexes the documents.
func (e *env) collection(name string, d docs) string {
	e.t.Helper()
	var c coll
	e.req("root", "POST", "/api/v1/knowledge/create", map[string]any{"name": name}, 200, &c)
	for fname, text := range d {
		fid := e.upload("root", fname, text)
		e.req("root", "POST", "/api/v1/knowledge/"+c.ID+"/file/add", map[string]any{"file_id": fid}, 200, nil)
		e.waitFile(c.ID, fid, "completed")
	}
	return c.ID
}

// scoreReranker serves /rerank: a document scores by the word of scores it
// contains (0.05 when none).
func scoreReranker(t *testing.T, scores map[string]float64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		res := []map[string]any{}
		for i, d := range body.Documents {
			score := 0.05
			for word, s := range scores {
				if strings.Contains(d, word) {
					score = s
				}
			}
			res = append(res, map[string]any{"index": i, "relevance_score": score})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": res})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A threshold that no passage reaches ranks the passages instead of
// returning none: rag.min_results keeps the best few.
func TestRerankKeepsMinResults(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	kid := e.collection("K", docs{"l.md": "# One\napples\n# Two\nbananas\n# Three\ncherries\n# Four\ndates\n# Five\nelderberries"})
	srv := scoreReranker(t, map[string]float64{"cherries": 0.5, "dates": 0.4})
	_ = e.st.Set(ctx, "rag.reranker.url", srv.URL, false)
	_ = e.st.Set(ctx, "rag.relevance_threshold", 0.9, false)
	search := func(k int) []string {
		t.Helper()
		hits, err := e.s.Search(ctx, e.users2["root"], []string{kid}, []string{"fruit"}, k)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hits {
			out = append(out, h.Text)
		}
		return out
	}
	got := search(5)
	if len(got) != 3 || !strings.Contains(got[0], "cherries") || !strings.Contains(got[1], "dates") {
		t.Fatalf("default min_results 3: %q", got)
	}
	_ = e.st.Set(ctx, "rag.min_results", 0, false)
	if got := search(5); len(got) != 0 {
		t.Fatalf("min_results 0 with an unreachable threshold: %q", got)
	}
	_ = e.st.Set(ctx, "rag.min_results", 4, false)
	if got := search(2); len(got) != 2 { // the caller's k still caps the answer
		t.Fatalf("k=2: %q", got)
	}
}

func TestFuseListsSumsEveryList(t *testing.T) {
	// 7 is second in both query rankings, 1 and 2 first in one each.
	got := fuseLists([]rankedList{{[]int64{1, 7}, 1}, {[]int64{2, 7}, 1}, {nil, 1}})
	if len(got) != 3 || got[0].id != 7 {
		t.Fatalf("got %+v", got)
	}
	if d := got[0].score - 2.0/62; d > 1e-12 || d < -1e-12 {
		t.Fatalf("score %v", got[0].score)
	}
	if got[1].id != 1 || got[2].id != 2 { // equal scores keep first-seen order
		t.Fatalf("order %+v", got)
	}
}

// Two queries, k=2. The passage that is second for both queries beats the
// best passage of either query once the rankings are fused; searched one
// query at a time and concatenated (rag.fuse_queries off) it comes after
// them and the answer is longer than k.
func TestSearchFusesQueries(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	kid := e.collection("K", docs{"d.md": "# One\nalpha alpha alpha\n# Two\nbeta beta beta\n# Three\nalpha beta"})
	_ = e.st.Set(ctx, "rag.hybrid.bm25_weight", 1, false) // keyword rankings only: the stub embedder adds noise
	search := func() []string {
		t.Helper()
		hits, err := e.s.Search(ctx, e.users2["root"], []string{kid}, []string{"alpha", "beta"}, 2)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hits {
			f := strings.Fields(h.Text)
			out = append(out, f[2]+" "+f[3])
		}
		return out
	}
	got := search()
	if len(got) != 2 || got[0] != "alpha beta" {
		t.Fatalf("fused: %q", got)
	}
	_ = e.st.Set(ctx, "rag.fuse_queries", false, false)
	got = search()
	if len(got) != 3 || got[0] != "alpha alpha" {
		t.Fatalf("concatenated: %q", got)
	}
}
