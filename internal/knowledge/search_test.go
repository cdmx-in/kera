package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestFuseWeightedRRF(t *testing.T) {
	// 1 is first in both lists, 2 only in vector, 3 only in text.
	got := fuse([]int64{1, 2}, []int64{1, 3}, 0.5)
	if len(got) != 3 || got[0].id != 1 {
		t.Fatalf("got %+v", got)
	}
	want1 := 0.5/61 + 0.5/61
	if d := got[0].score - want1; d > 1e-12 || d < -1e-12 {
		t.Fatalf("score %v, want %v", got[0].score, want1)
	}
	// Tie between 2 (vector rank 2) and 3 (text rank 2) at w=0.5 keeps vector first (stable).
	if got[1].id != 2 || got[2].id != 3 {
		t.Fatalf("order %+v", got)
	}
	// Text-only weighting puts text hits first.
	got = fuse([]int64{2, 1}, []int64{3, 1}, 1)
	if got[0].id != 3 || got[len(got)-1].score != 0 {
		t.Fatalf("w=1: %+v", got)
	}
	// Duplicates in one list count once.
	got = fuse([]int64{5, 5}, nil, 0)
	if len(got) != 1 || got[0].score != 1.0/61 {
		t.Fatalf("dups: %+v", got)
	}
}

func TestRerankCall(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" || r.Method != http.MethodPost {
			http.Error(w, "bad path "+r.URL.Path, 404)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}`))
	}))
	defer srv.Close()
	for _, u := range []string{srv.URL, srv.URL + "/", srv.URL + "/rerank"} {
		res, err := rerankCall(context.Background(), srv.Client(), u, "", "m", "q", []string{"a", "b"}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 2 || res[0].Index != 1 || res[0].Score != 0.9 {
			t.Fatalf("got %+v", res)
		}
	}
	if got["model"] != "m" || got["query"] != "q" || got["top_n"] != 2.0 || len(got["documents"].([]any)) != 2 {
		t.Fatalf("request %v", got)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) }))
	defer bad.Close()
	if _, err := rerankCall(context.Background(), bad.Client(), bad.URL, "", "m", "q", []string{"a"}, 1); err == nil {
		t.Fatal("want an error on HTTP 500")
	}
}

func TestVectorLiteralAndIndexName(t *testing.T) {
	if s := vectorLiteral([]float32{1, -0.5, 0.25}); s != "[1,-0.5,0.25]" {
		t.Fatal(s)
	}
	if n := indexName("0195-AB_c"); n != "chunk_hnsw_0195ab_c" {
		t.Fatal(n)
	}
	if safeID.MatchString("x'; DROP TABLE chunk; --") {
		t.Fatal("unsafe id accepted")
	}
}

// TestRerankSidecar runs against a real reranker (sidecars/rerank, TEI,
// LiteLLM's /rerank, ...) when KERA_TEST_RERANK_URL is set; KERA_TEST_RERANK_MODEL
// and KERA_TEST_RERANK_KEY name a hosted model and its bearer key.
func TestRerankSidecar(t *testing.T) {
	url := os.Getenv("KERA_TEST_RERANK_URL")
	if url == "" {
		t.Skip("KERA_TEST_RERANK_URL not set")
	}
	docs := []string{"Berlin is the capital of Germany.", "Paris is the capital and largest city of France.", "Bananas are yellow."}
	res, err := rerankCall(context.Background(), &http.Client{Timeout: 10 * time.Minute}, url, os.Getenv("KERA_TEST_RERANK_KEY"), os.Getenv("KERA_TEST_RERANK_MODEL"), "What is the capital of France?", docs, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Index != 1 || res[0].Score <= res[1].Score || res[0].Score < 0.5 {
		t.Fatalf("got %+v", res)
	}
}
