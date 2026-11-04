// Package memory keeps short facts a user wants the assistant to remember
// across chats: the memory table, its HTTP routes for the settings page, and
// the "builtin:memory" tool set. Search is semantic when rag.embedding is
// configured (vectors stored per embedding model) and full-text otherwise.
package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/provider"
	"github.com/cdmx-in/kera/internal/settings"
)

// MaxChars caps one memory.
const MaxChars = 4000

// ErrNotFound means the user has no memory with that id.
var ErrNotFound = errors.New("memory not found")

// Memory is one remembered fact.
type Memory struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
	Score     float64   `json:"score,omitempty"` // search relevance
}

// Embedder returns a client for an embedding connection (models.Registry.ConnectionClient).
type Embedder func(ctx context.Context, connectionID string) (provider.Client, error)

// Store reads and writes memories.
type Store struct {
	DB       *pgxpool.Pool
	Settings *settings.Store
	Embedder Embedder // nil = full-text search only
}

const cols = `id, user_id, content, created_at, updated_at`

func scan(row pgx.Row) (Memory, error) {
	var m Memory
	err := row.Scan(&m.ID, &m.UserID, &m.Content, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

func collect(rows pgx.Rows, err error, withScore bool) ([]Memory, error) {
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		var m Memory
		dst := []any{&m.ID, &m.UserID, &m.Content, &m.CreatedAt, &m.UpdatedAt}
		if withScore {
			dst = append(dst, &m.Score)
		}
		if err := rows.Scan(dst...); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Enabled reports whether memory applies to u: the feature is on
// (chat.memory.enable), u has features.memory and turned memory on in their
// settings (ui.memory).
func (s *Store) Enabled(ctx context.Context, u *auth.User) (bool, error) {
	if u == nil || !s.Settings.Bool(ctx, "chat.memory.enable") || !u.Can("features.memory") {
		return false, nil
	}
	var on bool
	err := s.DB.QueryRow(ctx, `SELECT coalesce(settings->'ui'->'memory' = 'true'::jsonb, false) FROM "user" WHERE id = $1`, u.ID).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("memory setting: %w", err)
	}
	return on, nil
}

// List returns the user's memories, newest first.
func (s *Store) List(ctx context.Context, userID string) ([]Memory, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+cols+` FROM memory WHERE user_id = $1 ORDER BY updated_at DESC, id`, userID)
	return collect(rows, err, false)
}

func clean(content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", errors.New("content is empty")
	}
	if len([]rune(content)) > MaxChars {
		return "", fmt.Errorf("content is longer than %d characters", MaxChars)
	}
	return content, nil
}

// Add stores a memory and embeds it (best effort).
func (s *Store) Add(ctx context.Context, userID, content string) (Memory, error) {
	content, err := clean(content)
	if err != nil {
		return Memory{}, err
	}
	m, err := scan(s.DB.QueryRow(ctx, `INSERT INTO memory (id, user_id, content) VALUES ($1, $2, $3) RETURNING `+cols,
		ids.New(), userID, content))
	if err != nil {
		return m, fmt.Errorf("add memory: %w", err)
	}
	s.embed(ctx, []Memory{m})
	return m, nil
}

// Update replaces a memory's content.
func (s *Store) Update(ctx context.Context, userID, id, content string) (Memory, error) {
	content, err := clean(content)
	if err != nil {
		return Memory{}, err
	}
	m, err := scan(s.DB.QueryRow(ctx, `UPDATE memory SET content = $3, embedding = NULL, embedding_model_id = NULL, updated_at = now()
		WHERE id = $1 AND user_id = $2 RETURNING `+cols, id, userID, content))
	if err != nil {
		return m, err
	}
	s.embed(ctx, []Memory{m})
	return m, nil
}

// Delete removes one memory.
func (s *Store) Delete(ctx context.Context, userID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM memory WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAll removes every memory of the user.
func (s *Store) DeleteAll(ctx context.Context, userID string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM memory WHERE user_id = $1`, userID)
	return err
}

// Search returns at most k memories relevant to query, best first.
func (s *Store) Search(ctx context.Context, userID, query string, k int) ([]Memory, error) {
	query = strings.TrimSpace(query)
	if k <= 0 {
		k = 5
	}
	if query == "" {
		all, err := s.List(ctx, userID)
		if len(all) > k {
			all = all[:k]
		}
		return all, err
	}
	if hits, ok := s.vectorSearch(ctx, userID, query, k); ok {
		return hits, nil
	}
	words := strings.Fields(query)
	rows, err := s.DB.Query(ctx, `SELECT `+cols+`, ts_rank_cd(to_tsvector('simple', content), q) AS score
		FROM memory, websearch_to_tsquery('simple', $2) q
		WHERE user_id = $1 AND to_tsvector('simple', content) @@ q
		ORDER BY score DESC, updated_at DESC LIMIT $3`, userID, strings.Join(words, " or "), k)
	return collect(rows, err, true)
}

// embedder is the configured embedding model, ready to use.
type embedder struct {
	client provider.Client
	conn   string
	model  string
	dim    int
}

func (s *Store) embedder(ctx context.Context) *embedder {
	if s.Embedder == nil {
		return nil
	}
	e := &embedder{}
	_ = s.Settings.Get(ctx, "rag.embedding.connection", &e.conn)
	_ = s.Settings.Get(ctx, "rag.embedding.model", &e.model)
	_ = s.Settings.Get(ctx, "rag.embedding.dim", &e.dim)
	if e.conn == "" || e.model == "" {
		return nil
	}
	c, err := s.Embedder(ctx, e.conn)
	if err != nil {
		slog.Warn("memory embedder", "connection", e.conn, "err", err)
		return nil
	}
	e.client = c
	return e
}

// modelRow returns the embedding_model row for e (created on first use) and
// its prefixes.
func (s *Store) modelRow(ctx context.Context, e *embedder, dim int) (id, docPrefix, queryPrefix string, err error) {
	var rowDim int
	err = s.DB.QueryRow(ctx, `INSERT INTO embedding_model (id, connection_id, model, dim) VALUES ($1, $2, $3, $4)
		ON CONFLICT (connection_id, model) DO UPDATE SET model = EXCLUDED.model
		RETURNING id, dim, doc_prefix, query_prefix`, ids.New(), e.conn, e.model, dim).Scan(&id, &rowDim, &docPrefix, &queryPrefix)
	if err != nil {
		return "", "", "", fmt.Errorf("embedding model row: %w", err)
	}
	if rowDim != dim {
		return "", "", "", fmt.Errorf("embedding model %s has %d dimensions, got %d", e.model, rowDim, dim)
	}
	return id, docPrefix, queryPrefix, nil
}

// embed stores vectors for ms; failures leave them to full-text search.
func (s *Store) embed(ctx context.Context, ms []Memory) {
	e := s.embedder(ctx)
	if e == nil || len(ms) == 0 {
		return
	}
	if err := s.embedWith(ctx, e, ms); err != nil {
		slog.Warn("memory embedding", "err", err)
	}
}

func (s *Store) embedWith(ctx context.Context, e *embedder, ms []Memory) error {
	dim := e.dim
	modelID, docPrefix := "", ""
	if dim > 0 {
		var err error
		if modelID, docPrefix, _, err = s.modelRow(ctx, e, dim); err != nil {
			return err
		}
	}
	texts := make([]string, len(ms))
	for i, m := range ms {
		texts[i] = docPrefix + m.Content
	}
	vecs, err := e.client.Embed(ctx, e.model, texts)
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	if modelID == "" { // dimension learned from the first vectors
		if len(vecs) == 0 || len(vecs[0]) == 0 {
			return errors.New("embedding model returned no vector")
		}
		if modelID, _, _, err = s.modelRow(ctx, e, len(vecs[0])); err != nil {
			return err
		}
	}
	for i, m := range ms {
		if _, err := s.DB.Exec(ctx, `UPDATE memory SET embedding = $2::vector, embedding_model_id = $3 WHERE id = $1 AND content = $4`,
			m.ID, vectorText(vecs[i]), modelID, m.Content); err != nil {
			return fmt.Errorf("store memory vector: %w", err)
		}
	}
	return nil
}

// vectorSearch backfills missing vectors of the user's memories, then ranks
// by cosine similarity. ok is false when no embedding model is usable.
func (s *Store) vectorSearch(ctx context.Context, userID, query string, k int) ([]Memory, bool) {
	e := s.embedder(ctx)
	if e == nil {
		return nil, false
	}
	// ponytail: backfill on read; users hold a handful of memories. A worker if that ever grows.
	var modelID string
	_ = s.DB.QueryRow(ctx, `SELECT id FROM embedding_model WHERE connection_id = $1 AND model = $2`, e.conn, e.model).Scan(&modelID)
	rows, err := s.DB.Query(ctx, `SELECT `+cols+` FROM memory WHERE user_id = $1
		AND (embedding IS NULL OR embedding_model_id IS DISTINCT FROM nullif($2, '')) LIMIT 256`, userID, modelID)
	missing, err := collect(rows, err, false)
	if err == nil && len(missing) > 0 {
		if err := s.embedWith(ctx, e, missing); err != nil {
			slog.Warn("memory backfill", "err", err)
			return nil, false
		}
	}
	var dim int
	var queryPrefix string
	if err := s.DB.QueryRow(ctx, `SELECT id, dim, query_prefix FROM embedding_model WHERE connection_id = $1 AND model = $2`,
		e.conn, e.model).Scan(&modelID, &dim, &queryPrefix); err != nil {
		return nil, false // nothing embedded yet (no memories)
	}
	vecs, err := e.client.Embed(ctx, e.model, []string{queryPrefix + query})
	if err != nil || len(vecs) != 1 || len(vecs[0]) != dim {
		slog.Warn("memory query embedding", "err", err)
		return nil, false
	}
	rows, err = s.DB.Query(ctx, `SELECT `+cols+`, 1 - (embedding <=> $2::vector) AS score FROM memory
		WHERE user_id = $1 AND embedding_model_id = $3 ORDER BY embedding <=> $2::vector LIMIT $4`,
		userID, vectorText(vecs[0]), modelID, k)
	hits, err := collect(rows, err, true)
	if err != nil {
		slog.Warn("memory vector search", "err", err)
		return nil, false
	}
	return hits, true
}

func vectorText(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
