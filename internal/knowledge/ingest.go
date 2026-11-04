package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Ingestion is a state machine on knowledge_file.status, with the table as
// the queue: pending -> processing -> completed | failed. A pending row is
// taken once its file is extracted (file.status completed); a pending row
// whose file failed extraction fails too. Uploads, imports, reindex and the
// files OnProcessed hook only set rows pending and Wake the worker, so the
// same path serves the server and `kera reindex`.

// Start resets rows a previous run left processing and starts the worker.
func (s *Service) Start(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `UPDATE knowledge_file SET status = 'pending' WHERE status = 'processing'`); err != nil {
		return fmt.Errorf("reset processing knowledge files: %w", err)
	}
	s.stopped.Add(1)
	go s.work()
	s.Wake()
	return nil
}

// Close stops the worker after its current file.
func (s *Service) Close() {
	s.once.Do(func() { close(s.stop) })
	s.stopped.Wait()
}

// Wake makes the worker look for pending rows.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ponytail: one worker (embedding servers are usually one GPU); a pool of
// workers claiming with SKIP LOCKED is the upgrade if ingestion lags.
func (s *Service) work() {
	defer s.stopped.Done()
	tick := time.NewTicker(time.Minute) // safety net for a missed wake-up
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
		case <-tick.C:
		}
		for {
			select {
			case <-s.stop:
				return
			default:
			}
			did, err := s.Step(context.Background())
			if err != nil {
				slog.Error("knowledge: ingest", "err", err)
				break
			}
			if !did {
				break
			}
		}
	}
}

// Step processes one pending row. It reports false when nothing was ready.
func (s *Service) Step(ctx context.Context) (bool, error) {
	r, err := s.step(ctx)
	return r != nil, err
}

// stepResult describes one processed row (Err is the indexing error, if any).
type stepResult struct {
	KnowledgeID, FileID string
	Chunks              int
	Err                 error
}

func (s *Service) step(ctx context.Context) (*stepResult, error) {
	if _, err := s.db.Exec(ctx, `UPDATE knowledge_file kf SET status = 'failed' FROM file f
		WHERE f.id = kf.file_id AND kf.status = 'pending' AND f.status = 'failed'`); err != nil {
		return nil, fmt.Errorf("fail unextractable files: %w", err)
	}
	var kid, fid string
	err := s.db.QueryRow(ctx, `UPDATE knowledge_file SET status = 'processing'
		WHERE (knowledge_id, file_id) = (SELECT kf.knowledge_id, kf.file_id FROM knowledge_file kf JOIN file f ON f.id = kf.file_id
			WHERE kf.status = 'pending' AND f.status = 'completed' ORDER BY kf.added_at, kf.file_id LIMIT 1 FOR UPDATE OF kf SKIP LOCKED)
		RETURNING knowledge_id, file_id`).Scan(&kid, &fid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim knowledge file: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	r := &stepResult{KnowledgeID: kid, FileID: fid}
	r.Chunks, r.Err = s.index(ctx, kid, fid)
	// The error is recorded before the status flips to failed, so a reader
	// never sees "failed" without its reason.
	if err := s.setFileError(ctx, kid, fid, r.Err); err != nil {
		return r, err
	}
	if r.Err != nil {
		slog.Warn("knowledge: indexing failed", "knowledge", kid, "file", fid, "err", r.Err)
		if _, err := s.db.Exec(ctx, `UPDATE knowledge_file SET status = 'failed' WHERE knowledge_id = $1 AND file_id = $2`, kid, fid); err != nil {
			return r, fmt.Errorf("mark failed: %w", err)
		}
	} else {
		slog.Debug("knowledge: indexed", "knowledge", kid, "file", fid, "chunks", r.Chunks)
	}
	return r, s.finishReindex(ctx, kid)
}

// setFileError records (or clears) the last indexing error of a file.
// ponytail: kept in knowledge.meta.errors to avoid a schema change; a
// knowledge_file.error column is the upgrade.
func (s *Service) setFileError(ctx context.Context, kid, fid string, perr error) error {
	var err error
	if perr == nil {
		_, err = s.db.Exec(ctx, `UPDATE knowledge SET meta = meta #- ARRAY['errors', $2] WHERE id = $1 AND meta ? 'errors'`, kid, fid)
	} else {
		msg, _ := json.Marshal(truncate(perr.Error(), 500))
		_, err = s.db.Exec(ctx, `UPDATE knowledge SET meta = jsonb_set(CASE WHEN jsonb_typeof(meta->'errors') = 'object' THEN meta
			ELSE meta || '{"errors":{}}' END, ARRAY['errors', $2], $3::jsonb) WHERE id = $1`, kid, fid, string(msg))
	}
	if err != nil {
		return fmt.Errorf("record index error: %w", err)
	}
	return nil
}

// target returns the embedding model new chunks of a collection go to: the
// reindex target while one runs, else the collection's model, else the
// configured default (which then becomes the collection's model).
func (s *Service) target(ctx context.Context, c *Collection) (*embedModel, error) {
	if t, ok := c.Meta["reindex_to"].(string); ok && t != "" {
		return s.modelByID(ctx, t)
	}
	if c.EmbeddingModelID != nil {
		return s.modelByID(ctx, *c.EmbeddingModelID)
	}
	conn, name, err := s.defaultModelName(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.ensureModel(ctx, conn, name)
	if err != nil {
		return nil, err
	}
	// First index of the collection: pick its model and text-search config.
	cfg := "simple"
	_ = s.settings.Get(ctx, "rag.fts_config", &cfg)
	if _, err := s.db.Exec(ctx, `UPDATE knowledge SET embedding_model_id = $2,
		fts_config = CASE WHEN EXISTS (SELECT 1 FROM chunk WHERE knowledge_id = $1) THEN fts_config ELSE $3::regconfig END
		WHERE id = $1 AND embedding_model_id IS NULL`, c.ID, m.ID, cfg); err != nil {
		return nil, fmt.Errorf("set collection model: %w", err)
	}
	return m, nil
}

// index chunks and embeds one file into one collection and marks it completed.
func (s *Service) index(ctx context.Context, kid, fid string) (int, error) {
	c, err := s.get(ctx, kid)
	if err != nil {
		return 0, err
	}
	m, err := s.target(ctx, c)
	if err != nil {
		return 0, err
	}
	if c, err = s.get(ctx, kid); err != nil { // fts_config may have just been set
		return 0, err
	}
	f, err := s.files.Get(ctx, fid)
	if err != nil {
		return 0, err
	}
	text, err := s.files.Text(ctx, fid)
	if err != nil {
		return 0, err
	}
	chunks := s.splitter(ctx).Split(text, pagesOf(text, f.Meta))
	inputs := make([]string, len(chunks))
	for i, ch := range chunks {
		inputs[i] = m.DocPrefix + ch.EmbedText()
	}
	vecs, err := s.embed(ctx, m, inputs)
	if err != nil {
		return 0, err
	}
	if err := s.ensureIndex(ctx, m); err != nil { // before the chunks become visible
		return 0, err
	}
	return len(chunks), s.store(ctx, c, fid, m, chunks, vecs)
}

// store replaces the file's chunks for model m in one transaction, so
// searches see either the old or the new chunks.
func (s *Service) store(ctx context.Context, c *Collection, fid string, m *embedModel, chunks []Chunk, vecs [][]float32) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var st string
	err = tx.QueryRow(ctx, `SELECT status FROM knowledge_file WHERE knowledge_id = $1 AND file_id = $2 FOR UPDATE`, c.ID, fid).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("file was removed from the collection while indexing")
	}
	if err != nil {
		return fmt.Errorf("lock knowledge file: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chunk WHERE knowledge_id = $1 AND file_id = $2 AND embedding_model_id = $3`, c.ID, fid, m.ID); err != nil {
		return fmt.Errorf("delete old chunks: %w", err)
	}
	b := &pgx.Batch{}
	for i, ch := range chunks {
		meta := map[string]any{}
		if ch.Page > 0 {
			meta["page"] = ch.Page
		}
		if len(ch.Headers) > 0 {
			meta["headers"] = ch.Headers
		}
		b.Queue(`INSERT INTO chunk (knowledge_id, file_id, idx, text, meta, fts_config, embedding, embedding_model_id)
			VALUES ($1, $2, $3, $4, $5, $6::regconfig, $7::vector, $8)`, c.ID, fid, i, ch.Text, meta, c.FTSConfig, vectorLiteral(vecs[i]), m.ID)
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return fmt.Errorf("insert chunks: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE knowledge_file SET status = 'completed', chunks = $3 WHERE knowledge_id = $1 AND file_id = $2`,
		c.ID, fid, len(chunks)); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Service) splitter(ctx context.Context) Splitter {
	sp := Splitter{Size: 1000, Overlap: 100, Markdown: true}
	_ = s.settings.Get(ctx, "rag.chunk_size", &sp.Size)
	_ = s.settings.Get(ctx, "rag.chunk_overlap", &sp.Overlap)
	_ = s.settings.Get(ctx, "rag.markdown_headers", &sp.Markdown)
	return sp
}

// pagesOf cuts the text of a paged document back into pages using
// meta.pages [{n, chars}] (pages are joined by "\n\n" at extraction).
func pagesOf(text string, meta map[string]any) []Page {
	raw, ok := meta["pages"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	rs := []rune(text)
	var out []Page
	pos := 0
	for _, p := range raw {
		pm, _ := p.(map[string]any)
		n, _ := pm["n"].(float64)
		chars, _ := pm["chars"].(float64)
		end := min(pos+int(chars), len(rs))
		if pos > end {
			return nil // meta does not match the text: chunk it as one document
		}
		out = append(out, Page{N: int(n), Text: string(rs[pos:end])})
		pos = end + 2
	}
	return out
}

// queue marks files of a collection pending (all when fileIDs is empty).
func (s *Service) queue(ctx context.Context, kid string, fileIDs []string) (int, error) {
	tag, err := s.db.Exec(ctx, `UPDATE knowledge_file SET status = 'pending'
		WHERE knowledge_id = $1 AND ($2::text[] IS NULL OR file_id = ANY($2))`, kid, fileIDs)
	if err != nil {
		return 0, fmt.Errorf("queue knowledge files: %w", err)
	}
	s.Wake()
	return int(tag.RowsAffected()), nil
}

// Reindex re-embeds every file of a collection. With model set (a model
// name on connection conn, "" = rag.embedding.connection) the collection
// moves to that embedding model: new chunks are written beside the old ones,
// which keep serving queries until every file is done (see finishReindex).
func (s *Service) Reindex(ctx context.Context, kid, conn, model string) (int, error) {
	if model == "" {
		// Cancels a move in progress: drop the chunks it already wrote.
		if _, err := s.db.Exec(ctx, `WITH k AS (UPDATE knowledge SET meta = meta - 'reindex_to' WHERE id = $1 RETURNING embedding_model_id)
			DELETE FROM chunk c USING k WHERE c.knowledge_id = $1 AND k.embedding_model_id IS NOT NULL
				AND c.embedding_model_id IS DISTINCT FROM k.embedding_model_id`, kid); err != nil {
			return 0, fmt.Errorf("cancel reindex: %w", err)
		}
		return s.queue(ctx, kid, nil)
	}
	if conn == "" {
		var err error
		if conn, _, err = s.defaultModelName(ctx); err != nil {
			conn = "ollama"
		}
	}
	m, err := s.ensureModel(ctx, conn, model)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE knowledge SET meta = CASE WHEN embedding_model_id IS NULL OR embedding_model_id = $2
		THEN meta - 'reindex_to' ELSE meta || jsonb_build_object('reindex_to', $2::text) END,
		embedding_model_id = coalesce(embedding_model_id, $2) WHERE id = $1`, kid, m.ID); err != nil {
		return 0, fmt.Errorf("start reindex: %w", err)
	}
	return s.queue(ctx, kid, nil)
}

// finishReindex flips a collection to its reindex target once no file is
// left to index, then drops the chunks of the old model.
func (s *Service) finishReindex(ctx context.Context, kid string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var to *string
	err = tx.QueryRow(ctx, `SELECT meta->>'reindex_to' FROM knowledge WHERE id = $1 FOR UPDATE`, kid).Scan(&to)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && to == nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read reindex state: %w", err)
	}
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM knowledge_file kf JOIN file f ON f.id = kf.file_id
		WHERE kf.knowledge_id = $1 AND kf.status IN ('pending', 'processing') AND f.status <> 'failed')`, kid).Scan(&busy); err != nil {
		return fmt.Errorf("check reindex progress: %w", err)
	}
	if busy {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE knowledge SET embedding_model_id = $2, meta = meta - 'reindex_to', updated_at = now() WHERE id = $1`, kid, *to); err != nil {
		return fmt.Errorf("switch embedding model: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chunk WHERE knowledge_id = $1 AND embedding_model_id IS DISTINCT FROM $2`, kid, *to); err != nil {
		return fmt.Errorf("drop old chunks: %w", err)
	}
	slog.Info("knowledge: reindex finished", "knowledge", kid, "embedding_model", *to)
	return tx.Commit(ctx)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimSpace(s) + "..."
}
