package knowledge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/ids"
)

// embedModel is one embedding_model row.
type embedModel struct {
	ID           string
	ConnectionID string
	Model        string
	Dim          int
	DocPrefix    string
	QueryPrefix  string
}

const emCols = `id, connection_id, model, dim, doc_prefix, query_prefix`

func scanModel(row pgx.Row) (*embedModel, error) {
	m := &embedModel{}
	if err := row.Scan(&m.ID, &m.ConnectionID, &m.Model, &m.Dim, &m.DocPrefix, &m.QueryPrefix); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Service) modelByID(ctx context.Context, id string) (*embedModel, error) {
	m, err := scanModel(s.db.QueryRow(ctx, `SELECT `+emCols+` FROM embedding_model WHERE id = $1`, id))
	if err != nil {
		return nil, fmt.Errorf("embedding model %s: %w", id, err)
	}
	return m, nil
}

// defaultModelName reads rag.embedding.{connection,model}.
func (s *Service) defaultModelName(ctx context.Context) (conn, model string, err error) {
	_ = s.settings.Get(ctx, "rag.embedding.connection", &conn)
	_ = s.settings.Get(ctx, "rag.embedding.model", &model)
	if model == "" {
		return "", "", errors.New("no embedding model is set: choose one in Admin > Settings > Retrieval > Embedding")
	}
	if conn == "" {
		conn = "ollama"
	}
	return conn, model, nil
}

// ensureModel returns the embedding_model row for (conn, model), creating it
// with the dimension of a probe vector the first time.
func (s *Service) ensureModel(ctx context.Context, conn, model string) (*embedModel, error) {
	m, err := scanModel(s.db.QueryRow(ctx, `SELECT `+emCols+` FROM embedding_model WHERE connection_id = $1 AND model = $2`, conn, model))
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("embedding model: %w", err)
	}
	vecs, err := s.embed(ctx, &embedModel{ConnectionID: conn, Model: model}, []string{"dimension probe"})
	if err != nil {
		return nil, err
	}
	m, err = scanModel(s.db.QueryRow(ctx, `INSERT INTO embedding_model (id, connection_id, model, dim, batch)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (connection_id, model) DO UPDATE SET batch = EXCLUDED.batch
		RETURNING `+emCols, ids.New(), conn, model, len(vecs[0]), s.batchSize(ctx)))
	if err != nil {
		return nil, fmt.Errorf("register embedding model: %w", err)
	}
	return m, nil
}

func (s *Service) batchSize(ctx context.Context) int {
	n := 32
	_ = s.settings.Get(ctx, "rag.embedding.batch", &n)
	return max(n, 1)
}

// embed returns one vector per text, calling the server batch by batch.
func (s *Service) embed(ctx context.Context, m *embedModel, texts []string) ([][]float32, error) {
	cl, err := s.embedder.ConnectionClient(ctx, m.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("embedding connection %q is not available (%w): check Admin > Settings > Retrieval > Embedding", m.ConnectionID, err)
	}
	n := s.batchSize(ctx)
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += n {
		vs, err := cl.Embed(ctx, m.Model, texts[i:min(i+n, len(texts))])
		if err != nil {
			return nil, fmt.Errorf("embed with %s: %w", m.Model, err)
		}
		for _, v := range vs {
			if len(v) == 0 || (m.Dim > 0 && len(v) != m.Dim) {
				return nil, fmt.Errorf("embed with %s: got a %d-dimension vector, want %d", m.Model, len(v), m.Dim)
			}
		}
		out = append(out, vs...)
	}
	return out, nil
}

// vectorLiteral renders v in pgvector's text format.
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// indexName is the partial HNSW index of one embedding model.
func indexName(modelID string) string {
	return "chunk_hnsw_" + strings.ToLower(strings.ReplaceAll(modelID, "-", ""))
}

// ensureIndex creates the model's partial HNSW index once per process. It
// runs outside any transaction (CONCURRENTLY) and replaces an index left
// invalid by an interrupted build.
func (s *Service) ensureIndex(ctx context.Context, m *embedModel) error {
	if _, ok := s.indexes.Load(m.ID); ok {
		return nil
	}
	if !safeID.MatchString(m.ID) || m.Dim <= 0 || m.Dim > 16000 {
		return fmt.Errorf("embedding model %q: unsafe id or dimension %d", m.ID, m.Dim)
	}
	name := indexName(m.ID)
	var valid *bool
	// An index another process is still building is invalid too: leave it.
	err := s.db.QueryRow(ctx, `SELECT i.indisvalid OR EXISTS (SELECT 1 FROM pg_stat_progress_create_index p WHERE p.index_relid = c.oid)
		FROM pg_class c JOIN pg_index i ON i.indexrelid = c.oid WHERE c.relname = $1`, name).Scan(&valid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check index %s: %w", name, err)
	}
	if valid != nil && !*valid {
		if _, err := s.db.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+name); err != nil {
			return fmt.Errorf("drop invalid index %s: %w", name, err)
		}
	}
	// Identifiers and literals are checked above; DDL cannot take parameters.
	q := fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON chunk USING hnsw ((embedding::vector(%d)) vector_cosine_ops)
		WHERE embedding_model_id = '%s'`, name, m.Dim, m.ID)
	if _, err := s.db.Exec(ctx, q); err != nil {
		return fmt.Errorf("create index %s: %w", name, err)
	}
	s.indexes.Store(m.ID, struct{}{})
	return nil
}
