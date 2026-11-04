package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/retrieval"
)

// searchConfig is the rag.* settings a query uses.
type searchConfig struct {
	TopK        int     // candidates after fusion (rag.top_k)
	TopKRerank  int     // results returned (rag.top_k_rerank)
	BM25Weight  float64 // weight of the full-text rank in the fusion
	Threshold   float64 // minimum reranker score (only applied with a reranker)
	RerankURL   string
	RerankModel string
	RerankKey   string // rag.reranker.api_key, sent as a bearer token
	LocalRuns   string // rag.reranker.local_runs: skip | allow
	LocalURL    string // rag.reranker.local_url: reranker for local runs
}

func (s *Service) searchConfig(ctx context.Context) searchConfig {
	c := searchConfig{TopK: 30, TopKRerank: 6, BM25Weight: 0.5, LocalRuns: "skip"}
	_ = s.settings.Get(ctx, "rag.top_k", &c.TopK)
	_ = s.settings.Get(ctx, "rag.top_k_rerank", &c.TopKRerank)
	_ = s.settings.Get(ctx, "rag.hybrid.bm25_weight", &c.BM25Weight)
	_ = s.settings.Get(ctx, "rag.relevance_threshold", &c.Threshold)
	_ = s.settings.Get(ctx, "rag.reranker.url", &c.RerankURL)
	_ = s.settings.Get(ctx, "rag.reranker.model", &c.RerankModel)
	_ = s.settings.Get(ctx, "rag.reranker.local_runs", &c.LocalRuns)
	_ = s.settings.Get(ctx, "rag.reranker.local_url", &c.LocalURL)
	if s.keys != nil && c.RerankURL != "" {
		key, err := apply.ReadSecret(ctx, s.settings, s.keys, "rag.reranker.api_key")
		if err != nil {
			slog.Warn("knowledge: reranker api key unreadable", "err", err)
		}
		c.RerankKey = key
	}
	c.TopK = max(c.TopK, 1)
	c.TopKRerank = max(c.TopKRerank, 1)
	c.BM25Weight = min(max(c.BM25Weight, 0), 1)
	return c
}

// reranker returns the endpoint and key a query may use: runs on a local
// model never send their documents to rag.reranker.url unless local_runs is
// "allow"; they use rag.reranker.local_url (no key) or the fused order.
func (c searchConfig) reranker(ctx context.Context) (url, key string) {
	if retrieval.IsLocalRun(ctx) && c.LocalRuns != "allow" {
		return c.LocalURL, ""
	}
	return c.RerankURL, c.RerankKey
}

// rerankCooldown is how long Reranks reports false after a failed rerank.
const rerankCooldown = 5 * time.Minute

// Reranks implements retrieval.Reranking: a reranker is configured for ctx
// (see searchConfig.reranker) and has not failed in the last rerankCooldown.
func (s *Service) Reranks(ctx context.Context) bool {
	url, _ := s.searchConfig(ctx).reranker(ctx)
	return url != "" && time.Since(time.Unix(0, s.rerankFailed.Load())) > rerankCooldown
}

// Search implements retrieval.Retriever.
func (s *Service) Search(ctx context.Context, u *auth.User, knowledgeIDs []string, query string, k int) ([]retrieval.Hit, error) {
	if len(knowledgeIDs) == 0 || strings.TrimSpace(query) == "" {
		return []retrieval.Hit{}, nil
	}
	cs, err := s.readable(ctx, u, knowledgeIDs)
	if err != nil {
		return nil, err
	}
	cfg := s.searchConfig(ctx)
	if k > 0 {
		cfg.TopKRerank = k
	}
	return s.search(ctx, cs, query, cfg)
}

// group is the collections searched with one query embedding and one
// text-search configuration.
type group struct {
	modelID string
	fts     string
	ids     []string
}

func groupsOf(cs []*Collection) []group {
	var out []group
	for _, c := range cs {
		if c.EmbeddingModelID == nil {
			continue // never indexed
		}
		i := slices.IndexFunc(out, func(g group) bool { return g.modelID == *c.EmbeddingModelID && g.fts == c.FTSConfig })
		if i < 0 {
			out = append(out, group{modelID: *c.EmbeddingModelID, fts: c.FTSConfig})
			i = len(out) - 1
		}
		out[i].ids = append(out[i].ids, c.ID)
	}
	return out
}

type ranked struct {
	id    int64
	score float64 // cosine distance (vector) or text rank (full text)
}

// search runs vector + full-text retrieval per group, fuses the rankings,
// reranks when a reranker is configured and returns the best cfg.TopKRerank hits.
func (s *Service) search(ctx context.Context, cs []*Collection, query string, cfg searchConfig) ([]retrieval.Hit, error) {
	var vec, fts []ranked
	for _, g := range groupsOf(cs) {
		m, err := s.modelByID(ctx, g.modelID)
		if err != nil {
			return nil, err
		}
		qv, err := s.embed(ctx, m, []string{m.QueryPrefix + query})
		if err != nil {
			return nil, err
		}
		v, err := s.vectorSearch(ctx, m, g.ids, qv[0], cfg.TopK)
		if err != nil {
			return nil, err
		}
		t, err := s.textSearch(ctx, g, query, cfg.TopK)
		if err != nil {
			return nil, err
		}
		vec, fts = append(vec, v...), append(fts, t...)
	}
	// ponytail: distances of different embedding models are compared as if
	// they were one scale; collections searched together normally share one.
	slices.SortStableFunc(vec, func(a, b ranked) int { return cmpFloat(a.score, b.score) })
	slices.SortStableFunc(fts, func(a, b ranked) int { return cmpFloat(b.score, a.score) })
	fused := fuse(idsOf(vec), idsOf(fts), cfg.BM25Weight)
	if len(fused) > cfg.TopK {
		fused = fused[:cfg.TopK]
	}
	hits, err := s.loadHits(ctx, fused)
	if err != nil {
		return nil, err
	}
	if url, key := cfg.reranker(ctx); url != "" && len(hits) > 0 {
		rr, err := s.rerank(ctx, cfg, url, key, query, hits)
		if err == nil {
			return rr, nil
		}
		s.rerankFailed.Store(time.Now().UnixNano())
		slog.Warn("knowledge: rerank failed, using fused order", "err", err)
	}
	if len(hits) > cfg.TopKRerank {
		hits = hits[:cfg.TopKRerank]
	}
	return hits, nil
}

func idsOf(rs []ranked) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.id
	}
	return out
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

type fusedHit struct {
	id    int64
	score float64
}

// rrfK is the usual reciprocal rank fusion constant.
const rrfK = 60

// fuse merges two best-first rankings by weighted reciprocal rank fusion:
// score = w/(k+rank_text) + (1-w)/(k+rank_vector), ranks from 1; an id
// missing from a ranking gets nothing from it.
func fuse(vector, text []int64, textWeight float64) []fusedHit {
	scores := map[int64]float64{}
	var order []int64
	add := func(list []int64, w float64) {
		seen := map[int64]bool{}
		for i, id := range list {
			if seen[id] {
				continue
			}
			seen[id] = true
			if _, ok := scores[id]; !ok {
				order = append(order, id)
			}
			scores[id] += w / float64(rrfK+i+1)
		}
	}
	add(vector, 1-textWeight)
	add(text, textWeight)
	out := make([]fusedHit, len(order))
	for i, id := range order {
		out[i] = fusedHit{id: id, score: scores[id]}
	}
	slices.SortStableFunc(out, func(a, b fusedHit) int { return cmpFloat(b.score, a.score) })
	return out
}

// vectorSearch returns the nearest chunks of one embedding model. The model
// id and dimension are written into the SQL so the planner can match the
// model's partial HNSW index; both were validated when the index was made.
func (s *Service) vectorSearch(ctx context.Context, m *embedModel, kids []string, q []float32, n int) ([]ranked, error) {
	if !safeID.MatchString(m.ID) || m.Dim <= 0 {
		return nil, fmt.Errorf("embedding model %q: unsafe id or dimension", m.ID)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// iterative_scan keeps scanning the graph until enough rows pass the
	// knowledge_id filter (pgvector >= 0.8).
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.ef_search = 100`); err != nil {
		return nil, fmt.Errorf("set ef_search: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
		return nil, fmt.Errorf("set iterative_scan: %w", err)
	}
	dist := fmt.Sprintf(`(embedding::vector(%d) <=> $1::vector(%d))`, m.Dim, m.Dim)
	q2 := fmt.Sprintf(`SELECT id, %s FROM chunk WHERE embedding_model_id = '%s' AND knowledge_id = ANY($2)
		ORDER BY %s LIMIT $3`, dist, m.ID, dist)
	rows, err := tx.Query(ctx, q2, vectorLiteral(q), kids, n)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ranked, error) {
		var x ranked
		return x, r.Scan(&x.id, &x.score)
	})
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	return out, tx.Commit(ctx)
}

// textSearch ranks chunks by full-text match. websearch_to_tsquery parses
// the query (quotes, -negation); its AND between words becomes OR, since a
// natural-language question rarely has every word in one chunk.
func (s *Service) textSearch(ctx context.Context, g group, query string, n int) ([]ranked, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id, ts_rank_cd(c.tsv, x.q, 1)
		FROM chunk c, (SELECT replace(websearch_to_tsquery($1::regconfig, $2)::text, ' & ', ' | ')::tsquery AS q) x
		WHERE c.knowledge_id = ANY($3) AND c.embedding_model_id = $4 AND c.tsv @@ x.q
		ORDER BY 2 DESC, c.id LIMIT $5`, g.fts, query, g.ids, g.modelID, n)
	if err != nil {
		return nil, fmt.Errorf("text search: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ranked, error) {
		var x ranked
		return x, r.Scan(&x.id, &x.score)
	})
	if err != nil {
		return nil, fmt.Errorf("text search: %w", err)
	}
	return out, nil
}

// loadHits reads the chunks in fused order.
func (s *Service) loadHits(ctx context.Context, fused []fusedHit) ([]retrieval.Hit, error) {
	if len(fused) == 0 {
		return []retrieval.Hit{}, nil
	}
	idList := make([]int64, len(fused))
	for i, f := range fused {
		idList[i] = f.id
	}
	rows, err := s.db.Query(ctx, `SELECT c.id, c.knowledge_id, c.file_id, f.filename, c.text, c.meta
		FROM chunk c JOIN file f ON f.id = c.file_id WHERE c.id = ANY($1)`, idList)
	if err != nil {
		return nil, fmt.Errorf("load chunks: %w", err)
	}
	defer rows.Close()
	byID := map[int64]retrieval.Hit{}
	for rows.Next() {
		var id int64
		var h retrieval.Hit
		var meta struct {
			Page    int      `json:"page"`
			Headers []string `json:"headers"`
		}
		if err := rows.Scan(&id, &h.KnowledgeID, &h.FileID, &h.FileName, &h.Text, &meta); err != nil {
			return nil, err
		}
		h.Page, h.Headers = meta.Page, meta.Headers
		byID[id] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]retrieval.Hit, 0, len(fused))
	for _, f := range fused {
		if h, ok := byID[f.id]; ok {
			h.Score = f.score
			out = append(out, h)
		}
	}
	return out, nil
}

// rerank orders hits with a Cohere/Jina-style POST /rerank endpoint and keeps
// the best cfg.TopKRerank at or above cfg.Threshold.
func (s *Service) rerank(ctx context.Context, cfg searchConfig, url, key, query string, hits []retrieval.Hit) ([]retrieval.Hit, error) {
	docs := make([]string, len(hits))
	for i, h := range hits {
		docs[i] = Chunk{Text: h.Text, Headers: h.Headers}.EmbedText()
	}
	res, err := rerankCall(ctx, s.http, url, key, cfg.RerankModel, query, docs, cfg.TopKRerank)
	if err != nil {
		return nil, err
	}
	out := make([]retrieval.Hit, 0, len(res))
	for _, r := range res {
		if r.Index < 0 || r.Index >= len(hits) {
			return nil, fmt.Errorf("rerank: index %d out of range", r.Index)
		}
		if r.Score < cfg.Threshold {
			continue
		}
		h := hits[r.Index]
		h.Score = r.Score
		out = append(out, h)
	}
	slices.SortStableFunc(out, func(a, b retrieval.Hit) int { return cmpFloat(b.Score, a.Score) })
	if len(out) > cfg.TopKRerank {
		out = out[:cfg.TopKRerank]
	}
	return out, nil
}

type rerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
}

// rerankCall posts {model, query, documents, top_n} to <url>/rerank (url may
// already end in /rerank; a Cohere-style base ending in /v1 or /v2 works the
// same) and reads {results:[{index, relevance_score}]}, the shape of the
// sidecar, TEI's Cohere route, Cohere itself and LiteLLM's /rerank (extra
// fields such as id, meta or document are ignored). key, when set, goes as
// Authorization: Bearer.
func rerankCall(ctx context.Context, hc *http.Client, url, key, model, query string, docs []string, topN int) ([]rerankResult, error) {
	url = strings.TrimRight(url, "/")
	if !strings.HasSuffix(url, "/rerank") {
		url += "/rerank"
	}
	body, err := json.Marshal(map[string]any{"model": model, "query": query, "documents": docs, "top_n": topN})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("rerank: read: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rerank: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out struct {
		Results []rerankResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("rerank: bad response: %w", err)
	}
	return out.Results, nil
}
