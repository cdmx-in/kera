// Package knowledge owns knowledge collections: their files, ingestion
// (chunk, embed, store), hybrid search with an optional reranker, the
// Open WebUI-compatible routes, the builtin knowledge tools and the
// retrieval.Retriever the chat engine uses.
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/retrieval"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/tools"
)

func init() { app.Register("knowledge", Mount) }

// Errors handlers return as-is.
var (
	ErrNotFound  = api.Errf(http.StatusNotFound, "not_found", "Knowledge not found")
	ErrForbidden = api.Errf(http.StatusForbidden, "forbidden", "You do not have write access to this knowledge")
)

// Embedder returns the embedding client of a connection (models.Registry in
// production, a stub in tests).
type Embedder interface {
	ConnectionClient(ctx context.Context, id string) (provider.Client, error)
}

// Service is the knowledge feature.
type Service struct {
	db       *pgxpool.Pool
	settings *settings.Store
	files    *files.Service
	models   *models.Registry // may be nil in tests; used to read a model's knowledge
	embedder Embedder
	keys     *secret.Keys // decrypts rag.reranker.api_key; nil in tests without one
	http     *http.Client

	wake    chan struct{}
	stop    chan struct{}
	stopped sync.WaitGroup
	once    sync.Once
	indexes sync.Map // embedding model id -> struct{} once its HNSW index exists

	rerankFailed atomic.Int64 // unix nanos of the last failed rerank call
}

// New builds a service; call Start to run the ingestion worker.
func New(db *pgxpool.Pool, st *settings.Store, fs *files.Service, reg *models.Registry, emb Embedder) *Service {
	return &Service{db: db, settings: st, files: fs, models: reg, embedder: emb,
		http: &http.Client{Timeout: 2 * time.Minute},
		wake: make(chan struct{}, 1), stop: make(chan struct{})}
}

var (
	regMu sync.Mutex
	reg   = map[*app.App]*Service{}
)

// For returns the app's knowledge service, creating it once.
func For(a *app.App) *Service {
	regMu.Lock()
	defer regMu.Unlock()
	if s := reg[a]; s != nil {
		return s
	}
	r := models.For(a)
	s := New(a.DB, a.Settings, files.For(a), r, r)
	reg[a] = s
	return s
}

// Mount wires the service into files, retrieval and tools, registers the
// routes and starts the ingestion worker.
func Mount(a *app.App) error {
	s := For(a)
	s.keys = a.Keys
	s.files.OnProcessed(func(context.Context, files.File) { s.Wake() })
	s.files.RegisterAccessCheck(s.canReadFile)
	retrieval.Register(s)
	tools.Register(&toolSet{s: s})
	s.routes(a)
	return s.Start(context.Background())
}

// Collection is one knowledge row.
type Collection struct {
	ID               string
	UserID           string
	Name             string
	Description      string
	EmbeddingModelID *string
	FTSConfig        string
	Meta             map[string]any
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const collCols = `id, user_id, name, description, embedding_model_id, fts_config::text, meta, created_at, updated_at`

func scanColl(row pgx.Row) (*Collection, error) {
	c := &Collection{}
	var meta []byte
	err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.Description, &c.EmbeddingModelID, &c.FTSConfig, &meta, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan knowledge: %w", err)
	}
	c.Meta = map[string]any{}
	_ = json.Unmarshal(meta, &c.Meta)
	return c, nil
}

// get loads one collection without access checks.
func (s *Service) get(ctx context.Context, id string) (*Collection, error) {
	return scanColl(s.db.QueryRow(ctx, `SELECT `+collCols+` FROM knowledge WHERE id = $1`, id))
}

// can: the owner and admins may do anything; others need a grant.
func (s *Service) can(ctx context.Context, u *auth.User, c *Collection, perm string) (bool, error) {
	if u == nil {
		return false, nil
	}
	if c.UserID == u.ID {
		return true, nil
	}
	return access.Can(ctx, s.db, u, access.Knowledge, c.ID, perm)
}

// load returns collection id when u may hold perm on it: ErrNotFound when u
// cannot even read it (existence is not revealed), ErrForbidden otherwise.
func (s *Service) load(ctx context.Context, u *auth.User, id, perm string) (*Collection, error) {
	c, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{access.Read, perm} {
		ok, err := s.can(ctx, u, c, p)
		if err != nil {
			return nil, err
		}
		if !ok && p == access.Read {
			return nil, ErrNotFound
		}
		if !ok {
			return nil, ErrForbidden
		}
	}
	return c, nil
}

// readable lists collections u may read, newest first. With idsOrNames
// non-nil only collections whose id or name is listed are returned.
func (s *Service) readable(ctx context.Context, u *auth.User, idsOrNames []string) ([]*Collection, error) {
	if u == nil {
		return nil, nil
	}
	granted, all, err := access.IDs(ctx, s.db, u, access.Knowledge, access.Read)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+collCols+` FROM knowledge
		WHERE ($1 OR user_id = $2 OR id = ANY($3)) AND ($4::text[] IS NULL OR id = ANY($4) OR name = ANY($4))
		ORDER BY updated_at DESC, id`, all, u.ID, nonNil(granted), idsOrNames)
	if err != nil {
		return nil, fmt.Errorf("list knowledge: %w", err)
	}
	defer rows.Close()
	var out []*Collection
	for rows.Next() {
		c, err := scanColl(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// create inserts a collection owned by u.
func (s *Service) create(ctx context.Context, u *auth.User, name, desc string) (*Collection, error) {
	cfg := "simple"
	_ = s.settings.Get(ctx, "rag.fts_config", &cfg)
	return scanColl(s.db.QueryRow(ctx, `INSERT INTO knowledge (id, user_id, name, description, fts_config)
		VALUES ($1, $2, $3, $4, $5::regconfig) RETURNING `+collCols, ids.New(), u.ID, name, desc, cfg))
}

// canReadFile grants file access to readers of a collection holding it.
func (s *Service) canReadFile(ctx context.Context, u *auth.User, fileID string) (bool, error) {
	rows, err := s.db.Query(ctx, `SELECT knowledge_id FROM knowledge_file WHERE file_id = $1 AND replaces IS NULL`, fileID)
	if err != nil {
		return false, fmt.Errorf("file collections: %w", err)
	}
	kids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(kids) == 0 {
		return false, err
	}
	cs, err := s.readable(ctx, u, kids)
	return len(cs) > 0, err
}

// Retriever ------------------------------------------------------------------

func toCollections(cs []*Collection) []retrieval.Collection {
	out := make([]retrieval.Collection, 0, len(cs))
	for _, c := range cs {
		out = append(out, retrieval.Collection{ID: c.ID, Name: c.Name, Description: c.Description})
	}
	return out
}

// List implements retrieval.Retriever.
func (s *Service) List(ctx context.Context, u *auth.User) ([]retrieval.Collection, error) {
	cs, err := s.readable(ctx, u, nil)
	return toCollections(cs), err
}

// Resolve implements retrieval.Retriever: ids or names, in the given order.
func (s *Service) Resolve(ctx context.Context, u *auth.User, idsOrNames []string) ([]retrieval.Collection, error) {
	if len(idsOrNames) == 0 {
		return []retrieval.Collection{}, nil
	}
	cs, err := s.readable(ctx, u, idsOrNames)
	if err != nil {
		return nil, err
	}
	rank := func(c *Collection) int {
		if i := slices.Index(idsOrNames, c.ID); i >= 0 {
			return i
		}
		return slices.Index(idsOrNames, c.Name)
	}
	slices.SortStableFunc(cs, func(a, b *Collection) int { return rank(a) - rank(b) })
	return toCollections(cs), nil
}

// Documents implements retrieval.Retriever: every extracted file of the collection.
func (s *Service) Documents(ctx context.Context, u *auth.User, knowledgeID string) ([]retrieval.Doc, error) {
	cs, err := s.readable(ctx, u, []string{knowledgeID})
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT f.id, f.filename, f.text FROM knowledge_file kf JOIN file f ON f.id = kf.file_id
		WHERE kf.knowledge_id = $1 AND kf.replaces IS NULL AND f.status = 'completed' AND f.text IS NOT NULL ORDER BY f.filename, f.id`, cs[0].ID)
	if err != nil {
		return nil, fmt.Errorf("knowledge documents: %w", err)
	}
	defer rows.Close()
	var out []retrieval.Doc
	for rows.Next() {
		d := retrieval.Doc{KnowledgeID: cs[0].ID}
		if err := rows.Scan(&d.FileID, &d.Name, &d.Text); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
