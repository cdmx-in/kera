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

// knownPrefixes are the document and query prefixes the model cards of some
// common embedding models require (without them retrieval is measurably
// worse); keyed by model name without its ":tag".
var knownPrefixes = map[string][2]string{
	"nomic-embed-text":      {"search_document: ", "search_query: "},
	"nomic-embed-text-v1.5": {"search_document: ", "search_query: "},
	"mxbai-embed-large":     {"", "Represent this sentence for searching relevant passages: "},
	"e5-small":              {"passage: ", "query: "},
	"e5-base":               {"passage: ", "query: "},
	"e5-large":              {"passage: ", "query: "},
	"multilingual-e5-small": {"passage: ", "query: "},
	"multilingual-e5-base":  {"passage: ", "query: "},
	"multilingual-e5-large": {"passage: ", "query: "},
}

// noPrefix, as the value of rag.embedding.doc_prefix or query_prefix, means
// "no prefix at all" even for a model Kera knows; an empty setting means
// "automatic" (the known prefix of the model, else none).
const noPrefix = "none"

// resolvePrefix turns one prefix setting into the prefix a model row gets;
// auto is the known prefix of the model ("" when it has none).
func resolvePrefix(setting, auto string) string {
	switch setting {
	case "":
		return auto
	case noPrefix:
		return ""
	}
	return setting
}

// prefixesFor returns the prefixes of model: rag.embedding.doc_prefix and
// rag.embedding.query_prefix when set ("none" = no prefix), else the known
// ones, else none. Used when a model row is created and by kera reindex.
func (s *Service) prefixesFor(ctx context.Context, model string) (doc, query string) {
	name, _, _ := strings.Cut(model, ":")
	auto := knownPrefixes[name]
	var d, q string
	_ = s.settings.Get(ctx, "rag.embedding.doc_prefix", &d)
	_ = s.settings.Get(ctx, "rag.embedding.query_prefix", &q)
	return resolvePrefix(d, auto[0]), resolvePrefix(q, auto[1])
}

// syncPrefixes writes the prefixes the settings give (prefixesFor) to the
// embedding model rows of a collection (its model and a reindex target), so a
// reindex also reverts a prefix that was removed from the settings. A prefix
// change only takes effect for vectors made after it, so it is done when the
// collection is reindexed.
func (s *Service) syncPrefixes(ctx context.Context, kid string) error {
	rows, err := s.db.Query(ctx, `SELECT id, model FROM embedding_model
		WHERE id IN (SELECT embedding_model_id FROM knowledge WHERE id = $1 UNION SELECT meta->>'reindex_to' FROM knowledge WHERE id = $1)`, kid)
	if err != nil {
		return fmt.Errorf("embedding prefixes: %w", err)
	}
	type row struct{ id, model string }
	var models []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.model); err != nil {
			rows.Close()
			return fmt.Errorf("embedding prefixes: %w", err)
		}
		models = append(models, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("embedding prefixes: %w", err)
	}
	for _, m := range models {
		doc, query := s.prefixesFor(ctx, m.model)
		if _, err := s.db.Exec(ctx, `UPDATE embedding_model SET doc_prefix = $2, query_prefix = $3 WHERE id = $1`, m.id, doc, query); err != nil {
			return fmt.Errorf("embedding prefixes: %w", err)
		}
	}
	return nil
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
	doc, query := s.prefixesFor(ctx, model)
	m, err = scanModel(s.db.QueryRow(ctx, `INSERT INTO embedding_model (id, connection_id, model, dim, batch, doc_prefix, query_prefix)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (connection_id, model) DO UPDATE SET batch = EXCLUDED.batch
		RETURNING `+emCols, ids.New(), conn, model, len(vecs[0]), s.batchSize(ctx), doc, query))
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
