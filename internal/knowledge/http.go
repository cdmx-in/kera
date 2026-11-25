package knowledge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/audit"
	"github.com/cdmx-in/kera/internal/auth"
)

func (s *Service) routes(a *app.App) {
	user := func(f http.HandlerFunc) http.Handler { return a.Auth.Required(f) } // CSRF: see auth.Credential
	for p, f := range map[string]http.HandlerFunc{
		"GET /api/v1/knowledge/{$}":                s.hList,
		"GET /api/v1/knowledge":                    s.hList,
		"POST /api/v1/knowledge/create":            s.hCreate,
		"GET /api/v1/knowledge/{id}":               s.hGet,
		"GET /api/v1/knowledge/{id}/files":         s.hFiles,
		"POST /api/v1/knowledge/{id}/update":       s.hUpdate,
		"DELETE /api/v1/knowledge/{id}/delete":     s.hDelete,
		"POST /api/v1/knowledge/{id}/file/add":     s.hFileAdd,
		"POST /api/v1/knowledge/{id}/file/remove":  s.hFileRemove,
		"POST /api/v1/knowledge/{id}/file/update":  s.hFileUpdate,
		"POST /api/v1/knowledge/{id}/reindex":      s.hReindex,
		"GET /api/v1/knowledge/{id}/grants":        s.hGrants,
		"PUT /api/v1/knowledge/{id}/grants":        s.hSetGrants,
		"POST /api/v1/retrieval/query/collection":  s.hQuery,
		"GET /api/v1/retrieval/embedding":          s.hEmbedding,
		"POST /api/v1/retrieval/query/collections": s.hQuery,
	} {
		a.Server.Handle(p, user(f))
	}
}

// stats are per-collection ingestion counts.
type stats struct {
	Files, Chunks                          int
	Pending, Processing, Completed, Failed int
}

func (s *Service) stats(ctx context.Context, kids []string) (map[string]*stats, error) {
	rows, err := s.db.Query(ctx, `SELECT knowledge_id, status, count(*), coalesce(sum(chunks), 0) FROM knowledge_file
		WHERE knowledge_id = ANY($1) AND replaces IS NULL GROUP BY 1, 2`, kids)
	if err != nil {
		return nil, fmt.Errorf("knowledge stats: %w", err)
	}
	defer rows.Close()
	out := map[string]*stats{}
	for rows.Next() {
		var kid, status string
		var n, chunks int
		if err := rows.Scan(&kid, &status, &n, &chunks); err != nil {
			return nil, err
		}
		st := out[kid]
		if st == nil {
			st = &stats{}
			out[kid] = st
		}
		st.Files += n
		st.Chunks += chunks
		switch status {
		case "pending":
			st.Pending = n
		case "processing":
			st.Processing = n
		case "completed":
			st.Completed = n
		case "failed":
			st.Failed = n
		}
	}
	return out, rows.Err()
}

// reindexIncomplete is how many files a move to another embedding model cannot finish with (see reindexProgress):
// 0 when no move is under way or it is still working.
func (s *Service) reindexIncomplete(ctx context.Context, c *Collection) int {
	to, _ := c.Meta["reindex_to"].(string)
	if to == "" {
		return 0
	}
	busy, missing, err := reindexProgress(ctx, s.db, c.ID, to)
	if err != nil || busy {
		return 0
	}
	return missing
}

// collJSON is the Open WebUI-compatible collection object plus ingestion state.
func (s *Service) collJSON(ctx context.Context, c *Collection, st *stats, write bool) map[string]any {
	if st == nil {
		st = &stats{}
	}
	meta := map[string]any{}
	for k, v := range c.Meta {
		if k != "errors" {
			meta[k] = v
		}
	}
	em := map[string]any(nil)
	if c.EmbeddingModelID != nil {
		if m, err := s.modelByID(ctx, *c.EmbeddingModelID); err == nil {
			em = map[string]any{"id": m.ID, "connection": m.ConnectionID, "model": m.Model, "dim": m.Dim, "reindex_incomplete": s.reindexIncomplete(ctx, c)}
		}
	}
	return map[string]any{
		"id": c.ID, "user_id": c.UserID, "name": c.Name, "description": c.Description, "meta": meta,
		"created_at": c.CreatedAt.Unix(), "updated_at": c.UpdatedAt.Unix(), "write_access": write,
		"embedding_model": em, "fts_config": c.FTSConfig, "reindexing": c.Meta["reindex_to"] != nil,
		"status": map[string]int{"files": st.Files, "chunks": st.Chunks, "pending": st.Pending,
			"processing": st.Processing, "completed": st.Completed, "failed": st.Failed},
	}
}

func (s *Service) hList(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	cs, err := s.readable(ctx, u, nil)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	kids := make([]string, len(cs))
	for i, c := range cs {
		kids[i] = c.ID
	}
	st, err := s.stats(ctx, kids)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	writable, all, err := access.IDs(ctx, s.db, u, access.Knowledge, access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		items = append(items, s.collJSON(ctx, c, st[c.ID], all || c.UserID == u.ID || slices.Contains(writable, c.ID)))
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

type collForm struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (s *Service) hCreate(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	if !u.Can("workspace.knowledge") {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "forbidden", "Permission required: workspace.knowledge"))
		return
	}
	var f collForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if f.Name == nil || strings.TrimSpace(*f.Name) == "" {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "missing_name", "name is required"))
		return
	}
	desc := ""
	if f.Description != nil {
		desc = *f.Description
	}
	c, err := s.create(ctx, u, strings.TrimSpace(*f.Name), desc)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, s.collJSON(ctx, c, nil, true))
}

// withFiles answers with the collection and its files (Open WebUI returns
// this shape from get, file/add and file/remove).
func (s *Service) withFiles(w http.ResponseWriter, r *http.Request, c *Collection) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	st, err := s.stats(ctx, []string{c.ID})
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	write, err := s.can(ctx, u, c, access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	fs, err := s.fileList(ctx, c)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out := s.collJSON(ctx, c, st[c.ID], write)
	out["files"] = fs
	api.JSON(w, http.StatusOK, out)
}

func (s *Service) hGet(w http.ResponseWriter, r *http.Request) {
	c, err := s.load(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), access.Read)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	s.withFiles(w, r, c)
}

// fileList returns the collection's files: {id, filename, meta, data:{status,
// error?}, chunks, created_at, updated_at}; data.status is the ingestion state.
func (s *Service) fileList(ctx context.Context, c *Collection) ([]map[string]any, error) {
	rows, err := s.db.Query(ctx, `SELECT f.id, f.user_id, f.filename, f.meta, f.status, f.error, f.size, kf.status, kf.chunks,
		extract(epoch FROM kf.added_at)::bigint, extract(epoch FROM f.updated_at)::bigint
		FROM knowledge_file kf JOIN file f ON f.id = kf.file_id WHERE kf.knowledge_id = $1 AND kf.replaces IS NULL ORDER BY f.filename, f.id`, c.ID)
	if err != nil {
		return nil, fmt.Errorf("knowledge files: %w", err)
	}
	errs, _ := c.Meta["errors"].(map[string]any)
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (map[string]any, error) {
		var id, uid, name, fstatus, ferr, status string
		var meta map[string]any
		var size int64
		var chunks int
		var added, updated int64
		if err := row.Scan(&id, &uid, &name, &meta, &fstatus, &ferr, &size, &status, &chunks, &added, &updated); err != nil {
			return nil, err
		}
		data := map[string]any{"status": status, "file_status": fstatus}
		if ferr != "" {
			data["error"] = ferr
		} else if e, ok := errs[id].(string); ok && status == "failed" {
			data["error"] = e
		}
		if meta == nil {
			meta = map[string]any{}
		}
		return map[string]any{"id": id, "user_id": uid, "filename": name, "meta": meta, "data": data, "size": size,
			"chunks": chunks, "created_at": added, "updated_at": updated}, nil
	})
}

func (s *Service) hFiles(w http.ResponseWriter, r *http.Request) {
	c, err := s.load(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), access.Read)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	fs, err := s.fileList(r.Context(), c)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": fs, "total": len(fs)})
}

func (s *Service) hUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	c, err := s.load(ctx, u, r.PathValue("id"), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var f collForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if f.Name != nil {
		if strings.TrimSpace(*f.Name) == "" {
			api.Fail(w, r, api.Errf(http.StatusBadRequest, "missing_name", "name is required"))
			return
		}
		c.Name = strings.TrimSpace(*f.Name)
	}
	if f.Description != nil {
		c.Description = *f.Description
	}
	c, err = scanColl(s.db.QueryRow(ctx, `UPDATE knowledge SET name = $2, description = $3, updated_at = now()
		WHERE id = $1 RETURNING `+collCols, c.ID, c.Name, c.Description))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	s.withFiles(w, r, c)
}

func (s *Service) hDelete(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	c, err := s.load(ctx, u, r.PathValue("id"), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	// One statement, so the collection and its grants go together (chunks cascade).
	if _, err := s.db.Exec(ctx, `WITH k AS (DELETE FROM knowledge WHERE id = $1)
		DELETE FROM access_grant WHERE resource_type = 'knowledge' AND resource_id = $1`, c.ID); err != nil {
		api.Fail(w, r, fmt.Errorf("delete knowledge: %w", err))
		return
	}
	audit.Record(ctx, audit.Event{Action: "knowledge.delete", TargetID: c.ID, TargetLabel: c.Name})
	api.JSON(w, http.StatusOK, true)
}

type fileForm struct {
	FileID string `json:"file_id"`
}

// fileTarget loads the collection (write access) and the body's file id.
func (s *Service) fileTarget(w http.ResponseWriter, r *http.Request) (*Collection, string, bool) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	c, err := s.load(ctx, u, r.PathValue("id"), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return nil, "", false
	}
	var f fileForm
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return nil, "", false
	}
	if f.FileID == "" {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "missing_file_id", "file_id is required"))
		return nil, "", false
	}
	return c, f.FileID, true
}

// hFileAdd links a file; one still being extracted is indexed when its
// extraction completes. Adding a file again re-queues it only if it failed.
func (s *Service) hFileAdd(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	c, fid, ok := s.fileTarget(w, r)
	if !ok {
		return
	}
	can, err := s.files.CanRead(ctx, u, fid)
	if err == nil && !can {
		err = api.Errf(http.StatusNotFound, "file_not_found", "File not found")
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := s.addFile(ctx, c.ID, fid); err != nil {
		api.Fail(w, r, err)
		return
	}
	s.withFiles(w, r, c)
}

func (s *Service) addFile(ctx context.Context, kid, fid string) error {
	if err := s.files.Retry(ctx, fid); err != nil { // a file whose extraction failed is extracted again
		return err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO knowledge_file (knowledge_id, file_id) VALUES ($1, $2)
		ON CONFLICT (knowledge_id, file_id) DO UPDATE SET status = CASE WHEN knowledge_file.status = 'failed'
			THEN 'pending' ELSE knowledge_file.status END`, kid, fid); err != nil {
		if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && pe.Code == "23503" {
			return api.Errf(http.StatusNotFound, "file_not_found", "File not found")
		}
		return fmt.Errorf("add knowledge file: %w", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE knowledge SET updated_at = now() WHERE id = $1`, kid); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// execer is the Exec of a pool or a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// removeFile unlinks a file from a collection: its row, all its chunks (of
// every embedding model) and its recorded indexing error. The file itself
// stays (see dropFile).
func (s *Service) removeFile(ctx context.Context, q execer, kid, fid string) error {
	if _, err := q.Exec(ctx, `WITH d AS (DELETE FROM knowledge_file WHERE knowledge_id = $1 AND file_id = $2)
		DELETE FROM chunk WHERE knowledge_id = $1 AND file_id = $2`, kid, fid); err != nil {
		return fmt.Errorf("remove knowledge file: %w", err)
	}
	if _, err := q.Exec(ctx, clearFileErrorSQL, kid, fid); err != nil {
		return fmt.Errorf("clear index error: %w", err)
	}
	return nil
}

// touch moves a collection to the top of the recently-updated order.
func (s *Service) touch(ctx context.Context, q execer, kid string) error {
	if _, err := q.Exec(ctx, `UPDATE knowledge SET updated_at = now() WHERE id = $1`, kid); err != nil {
		return fmt.Errorf("touch knowledge: %w", err)
	}
	return nil
}

func (s *Service) hFileRemove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, fid, ok := s.fileTarget(w, r)
	if !ok {
		return
	}
	if err := s.removeFile(ctx, s.db, c.ID, fid); err != nil {
		api.Fail(w, r, err)
		return
	}
	audit.Record(ctx, audit.Event{Action: "knowledge.file_remove", TargetID: c.ID, TargetLabel: c.Name, Meta: map[string]any{"file": fid}})
	if err := s.touch(ctx, s.db, c.ID); err != nil {
		api.Fail(w, r, err)
		return
	}
	s.withFiles(w, r, c)
}

// hFileUpdate re-indexes one file (after its content changed).
func (s *Service) hFileUpdate(w http.ResponseWriter, r *http.Request) {
	c, fid, ok := s.fileTarget(w, r)
	if !ok {
		return
	}
	if err := s.files.Retry(r.Context(), fid); err != nil { // a file whose extraction failed is extracted again
		api.Fail(w, r, err)
		return
	}
	if _, err := s.queue(r.Context(), c.ID, []string{fid}); err != nil {
		api.Fail(w, r, err)
		return
	}
	s.withFiles(w, r, c)
}

// hReindex re-embeds every file; ?embedding_model= (and optional
// &connection=) moves the collection to another embedding model.
func (s *Service) hReindex(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	c, err := s.load(ctx, u, r.PathValue("id"), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	// Another model means DDL (a new HNSW index) and calls on any connection.
	if (q.Get("embedding_model") != "" || q.Get("connection") != "") && !u.IsAdmin() {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "forbidden", "Only admins can move a collection to another embedding model"))
		return
	}
	n, err := s.Reindex(ctx, c.ID, q.Get("connection"), q.Get("embedding_model"))
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "reindex_failed", err.Error()))
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"status": true, "queued": n})
}

type grantRow struct {
	Principal  string `json:"principal"`
	Permission string `json:"permission"`
	Managed    bool   `json:"managed"`
}

func (s *Service) hGrants(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	c, err := s.load(ctx, u, r.PathValue("id"), access.Read)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	rows, err := s.db.Query(ctx, `SELECT principal_type || ':' || principal_id, permission, managed FROM access_grant
		WHERE resource_type = 'knowledge' AND resource_id = $1 ORDER BY 1, 2`, c.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	gs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[grantRow])
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, nonNil(gs))
}

// hSetGrants replaces the collection's grants (owner, admin or write grant),
// the same body as PUT /api/v1/grants/knowledge/{id}.
func (s *Service) hSetGrants(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	c, err := s.load(ctx, u, r.PathValue("id"), access.Write)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var gs []access.Grant
	if err := api.Decode(r, &gs); err != nil {
		api.Fail(w, r, err)
		return
	}
	for _, g := range gs {
		if _, _, err := access.ParsePrincipal(g.Principal); err != nil {
			api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_principal", err.Error()))
			return
		}
		if g.Permission != "" && g.Permission != access.Read && g.Permission != access.Write {
			api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_permission", "permission must be read or write"))
			return
		}
	}
	if err := access.Replace(ctx, s.db, access.Knowledge, c.ID, gs, false); err != nil {
		api.Fail(w, r, err)
		return
	}
	s.hGrants(w, r)
}

// hQuery is the Open WebUI retrieval query: {collection_names, query, k}.
func (s *Service) hQuery(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), auth.FromContext(r.Context())
	var f struct {
		CollectionNames []string `json:"collection_names"`
		CollectionName  string   `json:"collection_name"`
		Query           string   `json:"query"`
		K               int      `json:"k"`
		KReranker       int      `json:"k_reranker"`
	}
	if err := api.Decode(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if f.CollectionName != "" {
		f.CollectionNames = append(f.CollectionNames, f.CollectionName)
	}
	if strings.TrimSpace(f.Query) == "" || len(f.CollectionNames) == 0 {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_request", "collection_names and query are required"))
		return
	}
	k := f.KReranker
	if k <= 0 {
		k = f.K
	}
	hits, err := s.Search(ctx, u, f.CollectionNames, []string{f.Query}, k)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	docs, metas, dists := []string{}, []map[string]any{}, []float64{}
	for _, h := range hits {
		docs = append(docs, h.Text)
		m := map[string]any{"name": h.FileName, "source": h.FileName, "file_id": h.FileID, "knowledge_id": h.KnowledgeID,
			"chunk": h.Chunk, "chunk_end": h.ChunkEnd}
		if h.Page > 0 {
			m["page"] = h.Page
		}
		if len(h.Headers) > 0 {
			m["headers"] = h.Headers
		}
		metas = append(metas, m)
		dists = append(dists, h.Score)
	}
	api.JSON(w, http.StatusOK, map[string]any{"documents": [][]string{docs}, "metadatas": [][]map[string]any{metas}, "distances": [][]float64{dists}})
}

// hEmbedding reports the configured default embedding model.
func (s *Service) hEmbedding(w http.ResponseWriter, r *http.Request) {
	conn, model, _ := s.defaultModelName(r.Context())
	api.JSON(w, http.StatusOK, map[string]any{"embedding_engine": conn, "embedding_model": model, "embedding_batch_size": s.batchSize(r.Context())})
}
