// Package retrieval is the contract between the chat engine (caller) and the
// knowledge package (implementation), so neither imports the other.
package retrieval

import (
	"context"
	"sync"

	"github.com/cdmx-in/kera/internal/auth"
)

// Hit is one retrieved chunk.
type Hit struct {
	KnowledgeID string   `json:"knowledge_id"`
	FileID      string   `json:"file_id"`
	FileName    string   `json:"file_name"`
	Page        int      `json:"page,omitempty"`
	Headers     []string `json:"headers,omitempty"` // markdown breadcrumb of the chunk
	Text        string   `json:"text"`
	Score       float64  `json:"score"`
	// Chunk and ChunkEnd are the first and last chunk index (0-based, per file
	// and collection) the text covers: equal for a lone chunk, a range once
	// neighbouring chunks were joined into the passage (rag.neighbors).
	Chunk    int   `json:"chunk"`
	ChunkEnd int   `json:"chunk_end"`
	ChunkID  int64 `json:"-"` // the best chunk's row id, for the implementation's own use
}

// Doc is one whole extracted document of a collection (full-context mode).
type Doc struct {
	KnowledgeID string `json:"knowledge_id"`
	FileID      string `json:"file_id"`
	Name        string `json:"name"`
	Text        string `json:"text"`
}

// Collection identifies a knowledge collection.
type Collection struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Retriever is implemented by the knowledge package. Every method enforces
// the user's read access and silently drops collections the user cannot read.
type Retriever interface {
	// Search runs hybrid retrieval (+ rerank when configured) over the
	// collections and returns at most k hits, best first. queries[0] is the
	// question itself, the others are rewrites of it: all are searched and
	// their rankings fused, and a reranker judges against queries[0].
	Search(ctx context.Context, u *auth.User, knowledgeIDs []string, queries []string, k int) ([]Hit, error)
	// Documents returns every completed document of one collection.
	Documents(ctx context.Context, u *auth.User, knowledgeID string) ([]Doc, error)
	// Resolve maps collection ids or names (model presets may store either)
	// to collections the user can read; unknown entries are omitted.
	Resolve(ctx context.Context, u *auth.User, idsOrNames []string) ([]Collection, error)
	// List returns every collection the user can read.
	List(ctx context.Context, u *auth.User) ([]Collection, error)
}

// Reranking is optionally implemented by a Retriever: Reranks reports
// whether searches made with ctx go through a working reranker (so the
// passages are ranked well enough to put in a prompt unasked).
type Reranking interface {
	Reranks(ctx context.Context) bool
}

var (
	mu  sync.RWMutex
	cur Retriever
)

// Register installs the implementation (called by the knowledge package's Mount).
func Register(r Retriever) { mu.Lock(); cur = r; mu.Unlock() }

// Get returns the registered implementation, or nil when knowledge is not mounted.
func Get() Retriever { mu.RLock(); defer mu.RUnlock(); return cur }

type localRunKey struct{}

// WithLocalRun marks ctx as serving a run on a local model, so retrieval
// keeps its documents away from external services (rag.reranker.local_runs).
func WithLocalRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, localRunKey{}, true)
}

// IsLocalRun reports whether ctx was marked by WithLocalRun.
func IsLocalRun(ctx context.Context) bool {
	v, _ := ctx.Value(localRunKey{}).(bool)
	return v
}
