package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/retrieval"
	"github.com/cdmx-in/kera/internal/tools"
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

// A new embedding model row gets the prefixes its model card asks for (or the
// ones set in the settings); an existing row changes only when its collection
// is reindexed with a prefix set.
func TestEmbeddingPrefixes(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	prefixes := func(model string) [2]string {
		t.Helper()
		m, err := e.s.ensureModel(ctx, "ollama", model)
		if err != nil {
			t.Fatal(err)
		}
		return [2]string{m.DocPrefix, m.QueryPrefix}
	}
	// "stub" (the collection's model) is created before any prefix is set
	kid := e.collection("K", docs{"a.md": "# A\nalpha"})
	if got := prefixes("nomic-embed-text:latest"); got != [2]string{"search_document: ", "search_query: "} {
		t.Fatalf("known model: %q", got)
	}
	if got := prefixes("mxbai-embed-large"); got[0] != "" || !strings.HasPrefix(got[1], "Represent this sentence") {
		t.Fatalf("query-only prefix: %q", got)
	}
	_ = e.st.Set(ctx, "rag.embedding.query_prefix", "Q: ", false)
	if got := prefixes("unlisted"); got != [2]string{"", "Q: "} {
		t.Fatalf("setting on an unknown model: %q", got)
	}
	if got := prefixes("stub"); got != [2]string{} {
		t.Fatalf("stub row: %q", got)
	}
	_ = e.st.Set(ctx, "rag.embedding.doc_prefix", "D: ", false)
	if _, err := e.s.Reindex(ctx, kid, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := prefixes("stub"); got != [2]string{"D: ", "Q: "} {
		t.Fatalf("after reindex: %q", got)
	}
	if got := prefixes("mxbai-embed-large"); got[0] != "" { // other models keep their rows
		t.Fatalf("other model changed: %q", got)
	}
}

// query_knowledge_files numbers its passages through Call.Cite: a passage
// found by a second search keeps its number, new ones continue; without a
// registry each call numbers from 1.
func TestQueryToolNumbersThroughTheRunsRegistry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	kid := e.collection("K", docs{"d.md": "# S1\nant\n# S2\nbee\n# S3\ncat\n# S4\ndog"})
	_ = e.st.Set(ctx, "rag.hybrid.bm25_weight", 1, false)
	ts := &toolSet{s: e.s}
	nums := map[string]int{}
	cite := func(key string) int {
		if _, ok := nums[key]; !ok {
			nums[key] = len(nums) + 1
		}
		return nums[key]
	}
	call := func(q string, c func(string) int) map[string]int {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"query": q, "knowledge_ids": []string{kid}, "count": 2})
		res, err := ts.Execute(ctx, tools.Call{Name: "query_knowledge_files", Args: args, User: e.users2["root"], Cite: c, Emit: func(tools.Event) {}})
		if err != nil || res.IsError {
			t.Fatalf("%v %+v", err, res)
		}
		var items []struct {
			Source int    `json:"source"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal([]byte(res.Content), &items); err != nil || len(items) != len(res.Sources) {
			t.Fatalf("%v %s", err, res.Content)
		}
		out := map[string]int{}
		for i, it := range items {
			if res.Sources[i].Cite != it.Source {
				t.Fatalf("source %d: cite %d, result says %d", i, res.Sources[i].Cite, it.Source)
			}
			out[strings.Fields(it.Text)[2]] = it.Source
		}
		return out
	}
	first := call("ant bee", cite)
	second := call("bee cat", cite)
	if len(first) != 2 || len(second) != 2 || first["bee"] != second["bee"] || second["cat"] != 3 {
		t.Fatalf("first %v second %v", first, second)
	}
	if alone := call("bee cat", nil); alone["bee"]+alone["cat"] != 3 { // 1 and 2 in some order
		t.Fatalf("without a registry: %v", alone)
	}
}

func TestJoinChunksWritesTheOverlapOnce(t *testing.T) {
	prev := "The first chunk talks about payouts. The bank confirms the payout on day two."
	next := "The bank confirms the payout on day two. Then the beneficiary is credited."
	want := "The first chunk talks about payouts. The bank confirms the payout on day two. Then the beneficiary is credited."
	if got := joinChunks(prev, next); got != want {
		t.Fatalf("overlap with a trailing space: %q", got)
	}
	if got := joinChunks("alpha one.", "beta two."); got != "alpha one.\n\nbeta two." {
		t.Fatalf("no overlap: %q", got)
	}
	// a multi-byte character at the cut is never split
	if got := joinChunks("ééééééééééé", "éééééééééééx"); !strings.HasSuffix(got, "x") {
		t.Fatalf("multi-byte: %q", got)
	}
}

// Hits come best first; the passages of one file are in document order, hits
// that sit next to each other share one passage, and a run longer than the
// cap is cut, the piece without a hit dropped.
func TestPassagesMergeOrderAndCap(t *testing.T) {
	text := map[int]string{}
	for i := range 8 {
		text[i] = strings.Repeat(string(rune('a'+i)), 100)
	}
	hit := func(i int, score float64) retrieval.Hit {
		return retrieval.Hit{KnowledgeID: "k", FileID: "f", FileName: "f.md", Text: text[i], Chunk: i, ChunkEnd: i, Score: score}
	}
	// chunk 6 scored best, then 1 and 2 (neighbours of each other)
	got := passages([]retrieval.Hit{hit(6, 0.9), hit(1, 0.5), hit(2, 0.4)}, maps.Clone(text), 1, 10000)
	if len(got) != 2 || got[0].Chunk != 0 || got[0].ChunkEnd != 3 || got[1].Chunk != 5 || got[1].ChunkEnd != 7 {
		t.Fatalf("ranges %+v", got)
	}
	if got[0].Score != 0.5 || got[1].Score != 0.9 || strings.Count(got[0].Text, "b") != 100 {
		t.Fatalf("best score of the piece, each chunk once: %+v", got)
	}
	// 4 chunks of 100 characters under a 250-character cap: pieces of two; the piece holding only neighbours goes
	got = passages([]retrieval.Hit{hit(1, 0.5)}, maps.Clone(text), 3, 250)
	if len(got) != 1 || got[0].Chunk != 0 || got[0].ChunkEnd != 1 {
		t.Fatalf("cap: %+v", got)
	}
}

// With rag.neighbors a hit comes with the chunks around it as one passage,
// two hits next to each other share it, and the chunk index says what the
// passage covers. 0 returns the lone chunk.
func TestSearchReturnsNeighbouringChunksOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	pad := strings.Repeat("xx ", 55)
	var doc strings.Builder
	for i, w := range []string{"ant", "bee", "cat", "dog", "eel", "fox", "gnu"} {
		fmt.Fprintf(&doc, "# S%d\n%s %s\n", i+1, w, pad)
	}
	kid := e.collection("K", docs{"d.md": doc.String()})
	_ = e.st.Set(ctx, "rag.hybrid.bm25_weight", 1, false)
	search := func(q string, k int) []retrieval.Hit {
		t.Helper()
		hits, err := e.s.Search(ctx, e.users2["root"], []string{kid}, []string{q}, k)
		if err != nil {
			t.Fatal(err)
		}
		return hits
	}
	has := func(h retrieval.Hit, words ...string) bool {
		for _, w := range words {
			if !strings.Contains(h.Text, w+" xx") {
				return false
			}
		}
		return true
	}
	if h := search("dog", 1); len(h) != 1 || h[0].Chunk != 3 || h[0].ChunkEnd != 3 || has(h[0], "cat") {
		t.Fatalf("neighbors 0: %+v", h)
	}
	_ = e.st.Set(ctx, "rag.neighbors", 1, false)
	h := search("dog", 1)
	if len(h) != 1 || h[0].Chunk != 2 || h[0].ChunkEnd != 4 || !has(h[0], "cat", "dog", "eel") || has(h[0], "bee") {
		t.Fatalf("neighbors 1: %+v", h)
	}
	if !strings.HasPrefix(h[0].Headers[0], "S4") { // the passage is described by its hit
		t.Fatalf("headers %v", h[0].Headers)
	}
	h = search("dog eel", 2) // adjacent hits: one passage, no chunk twice
	if len(h) != 1 || h[0].Chunk != 2 || h[0].ChunkEnd != 5 || strings.Count(h[0].Text, "dog xx") != 1 || strings.Count(h[0].Text, "eel xx") != 1 {
		t.Fatalf("adjacent hits: %+v", h)
	}
	_ = e.st.Set(ctx, "rag.passage_max_chars", 500, false) // two chunks of about 170 characters fit, three do not
	h = search("dog", 1)
	if len(h) != 1 || h[0].Chunk != 2 || h[0].ChunkEnd != 3 {
		t.Fatalf("cap 500: %+v", h)
	}
	_ = e.st.Set(ctx, "rag.passage_max_chars", 400, false)
	_ = e.st.Set(ctx, "rag.neighbors", 3, false)
	if h = search("dog", 1); len(h) != 1 || h[0].Chunk > 3 || h[0].ChunkEnd < 3 || h[0].ChunkEnd-h[0].Chunk > 1 {
		t.Fatalf("cap 400 (floor 500): %+v", h)
	}
}
