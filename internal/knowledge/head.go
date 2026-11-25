package knowledge

import (
	"context"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
)

// headSelect builds the chunk_head row of every chunk its WHERE clause
// selects: the file name (punctuation turned into spaces, so "payout_rules.md"
// is three words) and the heading breadcrumb, weight A, in the chunk's own
// text-search configuration. chunk.tsv covers the chunk text only.
const headSelect = `INSERT INTO chunk_head (chunk_id, tsv)
	SELECT c.id, setweight(to_tsvector(c.fts_config, regexp_replace(f.filename, '[._-]+', ' ', 'g') || ' ' || coalesce(
		(SELECT string_agg(h, ' ') FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(c.meta->'headers') = 'array'
			THEN c.meta->'headers' ELSE '[]'::jsonb END) h), '')), 'A')
	FROM chunk c JOIN file f ON f.id = c.file_id `

// headBatch is how many chunk ids one backfill statement covers.
const headBatch = 5000

// backfillHeads gives every chunk without a chunk_head row one. It reads the
// chunks in id order, a batch per statement and transaction, and never writes
// to the chunk table, so searches and indexing carry on while it runs and a
// stopped run can simply be started again.
func (s *Service) backfillHeads(ctx context.Context, out io.Writer) error {
	var last int64
	total := 0
	for ctx.Err() == nil {
		var top *int64
		var n int
		err := s.db.QueryRow(ctx, `WITH b AS (SELECT id FROM chunk WHERE id > $1 ORDER BY id LIMIT $2),
			ins AS (`+headSelect+`WHERE c.id IN (SELECT id FROM b) ON CONFLICT DO NOTHING RETURNING chunk_id)
			SELECT (SELECT max(id) FROM b), (SELECT count(*) FROM ins)`, last, headBatch).Scan(&top, &n)
		if err != nil {
			return fmt.Errorf("backfill chunk headings: %w", err)
		}
		if top == nil {
			break
		}
		last, total = *top, total+n
		fmt.Fprintf(out, "  chunks up to id %d: %d headings written so far\n", last, total)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %d chunk headings\n", total)
	return nil
}

// headSearch ranks chunks whose file name or heading breadcrumb matches the
// query (same OR-ed query as textSearch). Among matching chunks the order
// follows the whole chunk: the heading counts at weight A and the text at
// weight D, so within a file the chunks that also use the words rank first.
func (s *Service) headSearch(ctx context.Context, g group, query string, n int) ([]ranked, error) {
	rows, err := s.db.Query(ctx, `SELECT h.chunk_id, ts_rank_cd(c.tsv || h.tsv, x.q, 1)
		FROM chunk_head h JOIN chunk c ON c.id = h.chunk_id,
		(SELECT replace(websearch_to_tsquery($1::regconfig, $2)::text, ' & ', ' | ')::tsquery AS q) x
		WHERE c.knowledge_id = ANY($3) AND c.embedding_model_id = $4 AND h.tsv @@ x.q
		ORDER BY 2 DESC, h.chunk_id LIMIT $5`, g.fts, query, g.ids, g.modelID, n)
	if err != nil {
		return nil, fmt.Errorf("heading search: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ranked, error) {
		var x ranked
		return x, r.Scan(&x.id, &x.score)
	})
	if err != nil {
		return nil, fmt.Errorf("heading search: %w", err)
	}
	return out, nil
}
