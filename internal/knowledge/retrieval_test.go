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

// filler documents that share no word with the identifiers searched for.
func filler() docs {
	d := docs{}
	for _, w := range []string{"apple", "banana", "cherry", "damson", "elder", "fig"} {
		d[w+".md"] = "# Fruit\n" + w + " grows on trees and is picked in autumn."
	}
	return d
}

// A word that is only in a file name or in a parent heading of a chunk
// cannot be found by keyword from the chunk text; the heading index finds
// it (the parent-heading case is checked in the row, not in a ranking: the
// parent's own chunk repeats the word). rag.hybrid.head_weight 0 switches the index off, and chunks indexed
// before it existed get their rows from backfillHeads.
func TestHeadingAndFileNameKeywordSearch(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d := filler()
	d["zorblax-runbook.md"] = "# Daemon\nrestart the service, then read the log file."
	d["guide.md"] = "# Quokka\n\n## Setup\nrestart the service, then read the log file."
	kid := e.collection("K", d)
	_ = e.st.Set(ctx, "rag.hybrid.bm25_weight", 1, false) // keyword rankings only: the stub embedder adds noise
	first := func(q string) string {
		t.Helper()
		hits, err := e.s.Search(ctx, e.users2["root"], []string{kid}, []string{q}, 1)
		if err != nil || len(hits) != 1 {
			t.Fatalf("%q: %+v %v", q, hits, err)
		}
		return hits[0].FileName
	}
	var rows, chunks int
	_ = e.s.db.QueryRow(ctx, `SELECT count(*) FROM chunk_head`).Scan(&rows)
	_ = e.s.db.QueryRow(ctx, `SELECT count(*) FROM chunk`).Scan(&chunks)
	if rows != chunks || rows == 0 {
		t.Fatalf("indexing wrote %d heading rows for %d chunks", rows, chunks)
	}
	if first("zorblax") != "zorblax-runbook.md" {
		t.Fatalf("with the heading index: %q", first("zorblax"))
	}
	// the Setup section sits under "Quokka" without saying so itself; its row does
	var under bool
	if err := e.s.db.QueryRow(ctx, `SELECT h.tsv @@ 'quokka'::tsquery AND NOT c.tsv @@ 'quokka'::tsquery FROM chunk_head h
		JOIN chunk c ON c.id = h.chunk_id WHERE c.text LIKE '## Setup%'`).Scan(&under); err != nil || !under {
		t.Fatalf("heading row of the Setup section: %v %v", under, err)
	}
	_ = e.st.Set(ctx, "rag.hybrid.head_weight", 0, false)
	if first("zorblax") == "zorblax-runbook.md" {
		t.Fatal("found by keyword without the heading index: the corpus does not isolate it")
	}
	// chunks indexed before the table existed: no rows, nothing found, then backfilled
	_ = e.st.Set(ctx, "rag.hybrid.head_weight", 0.25, false)
	if _, err := e.s.db.Exec(ctx, `DELETE FROM chunk_head`); err != nil {
		t.Fatal(err)
	}
	if first("zorblax") == "zorblax-runbook.md" {
		t.Fatal("found without heading rows")
	}
	var out strings.Builder
	if err := e.s.backfillHeads(ctx, &out); err != nil {
		t.Fatal(err)
	}
	_ = e.s.db.QueryRow(ctx, `SELECT count(*) FROM chunk_head`).Scan(&rows)
	if rows != chunks || first("zorblax") != "zorblax-runbook.md" || !strings.Contains(out.String(), "wrote") {
		t.Fatalf("after backfill: %d rows of %d chunks, %q\n%s", rows, chunks, first("zorblax"), out.String())
	}
	// a second run writes nothing; deleting chunks removes their rows with them
	out.Reset()
	_ = e.s.backfillHeads(ctx, &out)
	if !strings.Contains(out.String(), "wrote 0 chunk headings") {
		t.Fatal(out.String())
	}
	_, _ = e.s.db.Exec(ctx, `DELETE FROM chunk WHERE knowledge_id = $1`, kid)
	if _ = e.s.db.QueryRow(ctx, `SELECT count(*) FROM chunk_head`).Scan(&rows); rows != 0 {
		t.Fatalf("%d heading rows left after their chunks went", rows)
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
