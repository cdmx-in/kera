package importowui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/jackc/pgx/v5"
)

// blob is what the target already knows about a file's copy.
type blob struct {
	sha  string
	size int64
}

func (im *importer) importFiles(ctx context.Context) error {
	st := im.rep.Stat("files")
	known := map[string]blob{}
	rows, err := im.tx.Query(ctx, `SELECT id, sha256, size FROM file WHERE origin = 'openwebui' AND sha256 <> ''`)
	if err != nil {
		return fmt.Errorf("list target files: %w", err)
	}
	var id, sum string
	var size int64
	if _, err := pgx.ForEachRow(rows, []any{&id, &sum, &size}, func() error { known[id] = blob{sum, size}; return nil }); err != nil {
		return fmt.Errorf("list target files: %w", err)
	}
	var total int
	if err := im.src.QueryRow(ctx, `SELECT count(*) FROM file WHERE coalesce(updated_at, 0) >= $1`, im.since()).Scan(&total); err != nil {
		return fmt.Errorf("count source files: %w", err)
	}
	return im.sourceRows(ctx, `SELECT id, user_id, coalesce(filename, ''), coalesce(path, ''), meta, data, created_at, updated_at
		FROM file WHERE coalesce(updated_at, 0) >= $1 ORDER BY id`, []any{im.since()}, func(r pgx.Rows) error {
		var id, userID, filename, srcPath string
		var metaJ, dataJ []byte
		var created, updated *int64
		if err := r.Scan(&id, &userID, &filename, &srcPath, &metaJ, &dataJ, &created, &updated); err != nil {
			return err
		}
		st.Source++
		im.progress("files", st.Source, total)
		uid, ok := im.users[userID]
		switch {
		case !ok:
			st.skip("owner missing", id)
			return nil
		case safeName(id) != id: // the id becomes a directory name under uploads/
			st.skip("id is not a plain name", id)
			return nil
		}
		return im.importFile(ctx, st, fileRow{id: id, userID: uid, filename: filename, srcPath: srcPath,
			meta: jsonObject(metaJ), data: jsonObject(dataJ), created: ts(created), updated: ts(updated)}, known[id])
	})
}

type fileRow struct {
	id, userID, filename, srcPath string
	meta, data                    map[string]any
	created, updated              any
}

func (im *importer) importFile(ctx context.Context, st *Stat, f fileRow, prev blob) error {
	name, _ := f.meta["name"].(string)
	if f.filename != "" {
		name = f.filename
	}
	name = safeName(name)
	contentType, _ := f.meta["content_type"].(string)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	size := int64(0)
	if n, ok := f.meta["size"].(float64); ok {
		size = int64(n)
	}
	meta := map[string]any{}
	for k, v := range f.meta {
		if k != "data" { // per-upload processing options, not file metadata
			meta[k] = v
		}
	}
	status, text := "pending", (*string)(nil)
	if s, ok := f.data["content"].(string); ok && s != "" {
		status, text = "completed", &s
	}
	errMsg, sum := "", ""
	rel := path.Join("uploads", f.id, name)
	if !im.opt.NoFiles {
		var err error
		sum, size, err = im.copyBlob(f, rel, prev, size)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			status, errMsg = "failed", "blob missing"
			im.rep.MissingBlobs = append(im.rep.MissingBlobs, f.id)
			st.note("blob missing")
		case err != nil:
			return fmt.Errorf("file %s: %w", f.id, err)
		}
	}
	return im.put(ctx, st, f.id, `INSERT INTO file (id, user_id, filename, content_type, size, sha256, path, status, error, text,
			meta, origin, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'openwebui', $12, $13)
		ON CONFLICT (id) DO UPDATE SET user_id = EXCLUDED.user_id, filename = EXCLUDED.filename,
			content_type = EXCLUDED.content_type, size = EXCLUDED.size,
			sha256 = CASE WHEN EXCLUDED.sha256 = '' THEN file.sha256 ELSE EXCLUDED.sha256 END,
			path = EXCLUDED.path, status = EXCLUDED.status, error = EXCLUDED.error, text = EXCLUDED.text,
			meta = EXCLUDED.meta, updated_at = EXCLUDED.updated_at
		WHERE file.origin = 'openwebui' AND file.updated_at <= EXCLUDED.updated_at
		RETURNING (xmax = 0)`,
		f.id, f.userID, name, contentType, size, sum, rel, status, errMsg, text, meta, f.created, f.updated)
}

// copyBlob copies the source upload (found by the basename of its stored
// path) to <DataDir>/<rel> and returns its sha256 and size. A copy already in
// place with the size the target recorded is reused. In a dry run it only
// checks that the source exists.
func (im *importer) copyBlob(f fileRow, rel string, prev blob, metaSize int64) (string, int64, error) {
	base := filepath.Base(filepath.FromSlash(f.srcPath))
	if f.srcPath == "" {
		base = f.id + "_" + f.filename
	}
	src := filepath.Join(im.opt.UploadsDir, safeName(base)) // one element: never leaves the uploads dir
	info, err := os.Stat(src)
	if err != nil {
		return "", metaSize, err
	}
	if !info.Mode().IsRegular() {
		return "", metaSize, fs.ErrNotExist
	}
	if im.opt.DryRun {
		return "", info.Size(), nil
	}
	dst := filepath.Join(im.opt.DataDir, filepath.FromSlash(rel))
	if prev.sha != "" && prev.size == info.Size() {
		if d, err := os.Stat(dst); err == nil && d.Size() == info.Size() {
			return prev.sha, prev.size, nil
		}
	}
	sum, err := copyFile(src, dst)
	if err != nil {
		return "", 0, err
	}
	return sum, info.Size(), nil
}

func copyFile(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", fmt.Errorf("create upload dir: %w", err)
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return "", fmt.Errorf("create blob: %w", err)
	}
	h := sha256.New()
	_, cerr := io.Copy(io.MultiWriter(out, h), in)
	if err := errors.Join(cerr, out.Close()); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("copy blob: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", fmt.Errorf("place blob: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (im *importer) importKnowledge(ctx context.Context) error {
	st := im.rep.Stat("knowledge")
	err := im.sourceRows(ctx, `SELECT id, user_id, coalesce(name, ''), coalesce(description, ''), meta, created_at, updated_at
		FROM knowledge ORDER BY id`, nil, func(r pgx.Rows) error {
		var id, userID, name, desc string
		var meta []byte
		var created, updated *int64
		if err := r.Scan(&id, &userID, &name, &desc, &meta, &created, &updated); err != nil {
			return err
		}
		st.Source++
		uid, ok := im.users[userID]
		if !ok {
			st.skip("owner missing", id)
			return nil
		}
		return im.put(ctx, st, id, `INSERT INTO knowledge (id, user_id, name, description, meta, origin, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'openwebui', $6, $7)
			ON CONFLICT (id) DO UPDATE SET user_id = EXCLUDED.user_id, name = EXCLUDED.name, description = EXCLUDED.description,
				meta = EXCLUDED.meta, updated_at = EXCLUDED.updated_at
			WHERE knowledge.origin = 'openwebui' AND knowledge.updated_at <= EXCLUDED.updated_at
			RETURNING (xmax = 0)`, id, uid, name, desc, jsonObject(meta), ts(created), ts(updated))
	})
	if err != nil {
		return err
	}
	return im.importKnowledgeFiles(ctx)
}

// importKnowledgeFiles links files as pending with no chunks: Kera's indexer
// embeds them later. Existing links are left alone so a re-run never resets
// indexing progress.
func (im *importer) importKnowledgeFiles(ctx context.Context) error {
	st := im.rep.Stat("knowledge_files")
	kn, err := im.targetIDs(ctx, "knowledge")
	if err != nil {
		return err
	}
	files, err := im.targetIDs(ctx, "file")
	if err != nil {
		return err
	}
	return im.sourceRows(ctx, `SELECT knowledge_id, file_id, created_at FROM knowledge_file ORDER BY 1, 2`, nil, func(r pgx.Rows) error {
		var kid, fid string
		var created *int64
		if err := r.Scan(&kid, &fid, &created); err != nil {
			return err
		}
		st.Source++
		id := kid + "/" + fid
		switch {
		case !kn[kid]:
			st.skip("knowledge missing", id)
			return nil
		case !files[fid]:
			st.skip("file missing", id)
			return nil
		}
		return im.put(ctx, st, id, `INSERT INTO knowledge_file (knowledge_id, file_id, status, chunks, added_at, origin)
			VALUES ($1, $2, 'pending', 0, $3, 'openwebui') ON CONFLICT DO NOTHING RETURNING true`, kid, fid, ts(created))
	})
}
