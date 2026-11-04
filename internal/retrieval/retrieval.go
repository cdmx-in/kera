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
	// collections and returns at most k hits, best first.
	Search(ctx context.Context, u *auth.User, knowledgeIDs []string, query string, k int) ([]Hit, error)
	// Documents returns every completed document of one collection.
	Documents(ctx context.Context, u *auth.User, knowledgeID string) ([]Doc, error)
	// Resolve maps collection ids or names (model presets may store either)
	// to collections the user can read; unknown entries are omitted.
	Resolve(ctx context.Context, u *auth.User, idsOrNames []string) ([]Collection, error)
	// List returns every collection the user can read.
	List(ctx context.Context, u *auth.User) ([]Collection, error)
}

var (
	mu  sync.RWMutex
	cur Retriever
)

// Register installs the implementation (called by the knowledge package's Mount).
func Register(r Retriever) { mu.Lock(); cur = r; mu.Unlock() }

// Get returns the registered implementation, or nil when knowledge is not mounted.
func Get() Retriever { mu.RLock(); defer mu.RUnlock(); return cur }
