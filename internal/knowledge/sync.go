package knowledge

// Sync keeps a collection equal to a folder that lives somewhere else. It is
// the knowledge half of pipelines: internal/pipeline authenticates the caller,
// owns runs and steps, and calls the functions below; nothing here knows about
// tokens, runs or steps beyond the run number it stamps on rows.
//
// Identity. A synced file is (collection, knowledge_file.origin, file.filename)
// with origin "pipeline:<id>" (SyncOrigin). Rows added by hand or by another
// pipeline never carry it, so they are never replaced or pruned. Change
// detection is by content: file.sha256 against the sha the caller lists.
//
// Replace. A changed file is uploaded as a NEW file and staged beside the old
// one: a knowledge_file row with replaces = the old file id. Staged rows are
// invisible to every reader (stats, file list, Documents, both file tools,
// file access). When the ingest worker has indexed the staged file, store()
// deletes the old row and all its chunks in the same transaction that makes
// the new chunks searchable, then deletes the old file and its blob. A search
// sees the old document or the new one, never both, never neither. A staged
// file that fails, or yields no chunks, is kept hidden and the old version
// stays: the next SyncPlan lists the name for upload again.
//
// Locking. Every call below runs under one advisory lock per collection,
// pg_advisory_xact_lock(hashtext('kera.sync'), hashtext(collection id)). The
// ingest worker does not take it; it only touches the row it is indexing.
//
// Calls, in the order a run makes them (SyncScope names pipeline id, collection
// id and the open run number; the types are JSON-friendly):
//
//	CreateFor(ctx, ownerID, name, desc) (*Collection, error)
//	    A collection owned by ownerID (admin pipeline form: "create a new collection").
//
//	SyncPlan(ctx, SyncPlanReq{SyncScope, Files, Adopt, AllowMassDelete, DryRun}) (SyncPlanResult, error)
//	    The caller lists every file (name, sha256, size). Answers which names to
//	    upload, which are unchanged, which are already staged (received), which
//	    unowned rows were claimed (Adopt), which owned names are no longer
//	    listed (stale) and, when the prune would be refused, why. Stamps
//	    sync_run on the listed rows and drops staged leftovers. DryRun writes
//	    nothing. ErrMoving (409) while the collection changes embedding model.
//	    A listing split over several calls is judged call by call: Stale and
//	    Refused are final only for the last call, SyncPrune decides for good.
//
//	SyncPut(ctx, SyncPutReq{SyncScope, Owner, Name, SHA256, Body}) (SyncPutResult, error)
//	    Stores one file through files.Service.Create (so extraction is queued
//	    in this process), checks its sha256 (400 checksum_mismatch) and stages
//	    it against the owned row of that name. A second put of the same name
//	    replaces the staged row. Result.Replaces is the old file id ("" for a
//	    new name): "+ name" versus "~ name" in a log.
//
//	SyncState(ctx, SyncScope) (SyncStatus, error)
//	    Waiting (extracting or indexing), Indexed and Failed counts for the
//	    files this run listed or uploaded, the first few waiting names and the
//	    first few failures with their reasons. A failed new version says that
//	    the previous version was kept. Moving: the collection is changing
//	    embedding model. Settled() = nothing waits and nothing moves.
//
//	SyncPrune(ctx, SyncPruneReq{SyncScope, Listed, Owned, Allow, DryRun}) (SyncPruneResult, error)
//	    Call once SyncState is settled and the listing was complete. Removes
//	    only this pipeline's rows not listed in this run (and their staged
//	    versions), behind pruneRefused: an empty listing, or more than
//	    max(5, half) of the files the pipeline owned before the run (Owned,
//	    from the run's first SyncPlan), is refused unless Allow.
//
//	SyncRelease(ctx, pipelineID) error
//	    Pipeline deleted: staged versions are dropped, every row it owns goes
//	    back to origin "kera" and stays in its collection.
//
// A same-named row that no pipeline owns is left alone unless the plan adopts
// it: without Adopt a pushed file lands beside it as a second row.
//
// The caller checks that the collection is one of the pipeline's targets, that
// the run is open and that Files holds a complete listing; the code here only
// validates names (one path element, as files.SanitizeName leaves it) and
// sha256 values, and caps a listing at MaxSyncFiles.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/files"
)

// OriginPrefix starts the knowledge_file.origin of every pipeline-owned row.
const OriginPrefix = "pipeline:"

// MaxSyncFiles caps one listing.
const MaxSyncFiles = 20000

// maxReported caps the names SyncState returns.
const maxReported = 20

// SyncOrigin is the knowledge_file.origin of the rows a pipeline owns.
func SyncOrigin(pipelineID string) string { return OriginPrefix + pipelineID }

// ErrMoving is returned while a collection moves to another embedding model.
// ponytail: a move started in the middle of a run can still hide a document
// until the move ends (the swap deletes the old chunks of every model).
var ErrMoving = api.Errf(http.StatusConflict, "reindexing", "This collection is moving to another embedding model; sync again when it has finished")

// SyncScope names what a sync call works on.
type SyncScope struct {
	Pipeline  string // pipeline id
	Knowledge string // collection id
	Run       int64  // the open run: rows it lists or uploads get sync_run = Run
}

// SyncFile is one entry of a listing.
type SyncFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"` // hex of the content
	Size   int64  `json:"size"`
}

// CreateFor creates a collection owned by ownerID.
func (s *Service) CreateFor(ctx context.Context, ownerID, name, desc string) (*Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, api.Errf(http.StatusBadRequest, "missing_name", "name is required")
	}
	return s.create(ctx, &auth.User{ID: ownerID}, name, desc)
}

// Plan ---------------------------------------------------------------------------

// SyncPlanReq is the input of SyncPlan.
type SyncPlanReq struct {
	SyncScope
	Files           []SyncFile
	Adopt           bool // claim the same-named row no pipeline owns (pipeline.adopt, one-shot)
	AllowMassDelete bool // pipeline.allow_mass_delete: only decides Refused
	DryRun          bool
}

// SyncPlanResult says what a listing needs. Every listed name is in exactly
// one of Upload, Unchanged and Received; Adopted is a note on top.
type SyncPlanResult struct {
	Upload    []string `json:"upload"`    // send these with SyncPut
	Unchanged []string `json:"unchanged"` // the same content is live
	Received  []string `json:"received"`  // the same content is staged and waits for indexing
	Adopted   []string `json:"adopted"`   // unowned rows claimed (or that would be, in a dry run)
	Ambiguous []string `json:"ambiguous"` // adopt skipped: two unowned rows have the name
	Stale     []string `json:"stale"`     // owned names this run did not list
	Refused   string   `json:"refused,omitempty"`
	Owned     int      `json:"owned"` // files the pipeline owns here before this call's uploads (adopted ones included)
}

// syncRow is one knowledge_file row with its file's name and hash.
type syncRow struct {
	fileID, name, sha, status, origin string
	replaces                          *string
	syncRun                           *int64
}

func (r syncRow) stamped(run int64) bool { return r.syncRun != nil && *r.syncRun == run }

// syncPlan is classify's verdict: the answer and what to write.
type syncPlan struct {
	SyncPlanResult
	claim []string // file ids to take over
	stamp []string // file ids to stamp with the run
	drop  []string // staged file ids to remove
	stale []syncRow
}

// classify is SyncPlan without the database: listing is what the caller sent,
// rows the collection's rows (the owned ones, plus every other when adopt is
// on). Rows are oldest first; staged rows are those with replaces set.
func classify(listing []SyncFile, rows []syncRow, origin string, run int64, adopt bool) syncPlan {
	p := syncPlan{SyncPlanResult: SyncPlanResult{Upload: []string{}, Unchanged: []string{}, Received: []string{},
		Adopted: []string{}, Ambiguous: []string{}, Stale: []string{}}}
	byName := map[string][]*syncRow{}
	for i := range rows {
		byName[rows[i].name] = append(byName[rows[i].name], &rows[i])
	}
	listed := make(map[string]bool, len(listing))
	for _, f := range listing {
		listed[f.Name] = true
		var base, staged *syncRow
		var free []*syncRow
		for _, r := range byName[f.Name] {
			switch {
			case r.origin == origin && r.replaces == nil:
				base = r // the newest wins
			case r.origin == origin:
				if staged != nil {
					p.drop = append(p.drop, staged.fileID) // put keeps one; an older leftover goes
				}
				staged = r
			case r.replaces == nil && !strings.HasPrefix(r.origin, OriginPrefix):
				free = append(free, r)
			}
		}
		if base == nil && staged == nil && adopt {
			switch len(free) {
			case 0:
			case 1:
				base = free[0]
				base.origin = origin
				p.claim = append(p.claim, base.fileID)
				p.Adopted = append(p.Adopted, f.Name)
			default:
				p.Ambiguous = append(p.Ambiguous, f.Name)
			}
		}
		if base != nil {
			p.stamp = append(p.stamp, base.fileID)
		}
		switch {
		case base != nil && base.sha == f.SHA256 && base.status != "failed":
			p.Unchanged = append(p.Unchanged, f.Name)
			if staged != nil {
				p.drop = append(p.drop, staged.fileID) // the source went back to the live content
			}
		case staged != nil && staged.sha == f.SHA256 && staged.status != "failed":
			p.Received = append(p.Received, f.Name)
			p.stamp = append(p.stamp, staged.fileID)
		default:
			p.Upload = append(p.Upload, f.Name)
			if staged != nil {
				p.drop = append(p.drop, staged.fileID) // put stages a fresh one
			}
		}
	}
	for _, r := range rows {
		if r.origin != origin {
			continue
		}
		if r.replaces == nil {
			p.Owned++
		}
		if listed[r.name] || r.stamped(run) { // a name listed by an earlier call of this run counts as listed
			continue
		}
		if r.replaces != nil {
			p.drop = append(p.drop, r.fileID)
		} else {
			p.stale = append(p.stale, r)
		}
	}
	slices.SortFunc(p.stale, func(a, b syncRow) int { return strings.Compare(a.name, b.name) })
	for _, r := range p.stale {
		p.Stale = append(p.Stale, r.name)
	}
	return p
}

// pruneRefused says why a prune must not run ("" when it may). Removing
// nothing is never refused; otherwise an empty listing (the script lost its
// source) and more than max(5, half) of the owned files (a wrong folder)
// need an admin's Allow.
func pruneRefused(owned, stale, listed int, allow bool) string {
	if stale == 0 || allow {
		return ""
	}
	if listed == 0 {
		return fmt.Sprintf("the listing was empty; removing all %d files was refused", stale)
	}
	if stale > max(5, owned/2) {
		return fmt.Sprintf("removing %d of %d files was refused", stale, owned)
	}
	return ""
}

// checkName accepts one path element exactly as files.SanitizeName leaves it.
func checkName(name string) error {
	if name == "" || name != files.SanitizeName(name) {
		return api.Errf(http.StatusBadRequest, "bad_name",
			fmt.Sprintf("file name %q must be a single name of at most 200 bytes, without directories or control characters", name))
	}
	return nil
}

// checkSHA returns the lower-case hex of a sha256.
func checkSHA(name, sha string) (string, error) {
	sha = strings.ToLower(sha)
	if b, err := hex.DecodeString(sha); err != nil || len(b) != 32 {
		return "", api.Errf(http.StatusBadRequest, "bad_sha256", fmt.Sprintf("%q: sha256 must be 64 hex characters", name))
	}
	return sha, nil
}

// lockSync takes the collection's sync lock for the rest of the transaction
// and reports whether the collection is moving to another embedding model.
func lockSync(ctx context.Context, tx pgx.Tx, kid string) (moving bool, err error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kera.sync'), hashtext($1))`, kid); err != nil {
		return false, fmt.Errorf("lock collection: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT coalesce(meta->>'reindex_to', '') <> '' FROM knowledge WHERE id = $1`, kid).Scan(&moving)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("read collection: %w", err)
	}
	return moving, nil
}

// loadRows reads the collection's rows, oldest first: the owned ones and,
// with all, every other.
func loadRows(ctx context.Context, tx pgx.Tx, kid, origin string, all bool) ([]syncRow, error) {
	rows, err := tx.Query(ctx, `SELECT kf.file_id, f.filename, f.sha256, kf.status, kf.origin, kf.replaces, kf.sync_run
		FROM knowledge_file kf JOIN file f ON f.id = kf.file_id
		WHERE kf.knowledge_id = $1 AND ($3 OR kf.origin = $2) ORDER BY kf.added_at, kf.file_id`, kid, origin, all)
	if err != nil {
		return nil, fmt.Errorf("list sync rows: %w", err)
	}
	// ponytail: the whole collection in memory (a few KB per 1000 files); a
	// query by listed names is the upgrade if collections reach millions.
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (syncRow, error) {
		var x syncRow
		return x, r.Scan(&x.fileID, &x.name, &x.sha, &x.status, &x.origin, &x.replaces, &x.syncRun)
	})
}

// removeStaged removes those of the file ids that are still staged versions
// and returns them. The rows are locked first: a swap that is committing right
// now turns its staged row into the live one, which must not be removed.
func (s *Service) removeStaged(ctx context.Context, tx pgx.Tx, kid string, fids []string) ([]string, error) {
	if len(fids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT file_id FROM knowledge_file WHERE knowledge_id = $1 AND file_id = ANY($2) AND replaces IS NOT NULL
		ORDER BY file_id FOR UPDATE`, kid, fids)
	if err != nil {
		return nil, fmt.Errorf("lock staged versions: %w", err)
	}
	still, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("lock staged versions: %w", err)
	}
	for _, fid := range still {
		if err := s.removeFile(ctx, tx, kid, fid); err != nil {
			return nil, err
		}
	}
	return still, nil
}

// SyncPlan classifies a listing against the collection (see the top of this file).
func (s *Service) SyncPlan(ctx context.Context, r SyncPlanReq) (SyncPlanResult, error) {
	if len(r.Files) > MaxSyncFiles {
		return SyncPlanResult{}, api.Errf(http.StatusBadRequest, "too_many_files", fmt.Sprintf("a listing holds at most %d files", MaxSyncFiles))
	}
	listing := make([]SyncFile, len(r.Files))
	seen := make(map[string]bool, len(r.Files))
	for i, f := range r.Files {
		if err := checkName(f.Name); err != nil {
			return SyncPlanResult{}, err
		}
		sha, err := checkSHA(f.Name, f.SHA256)
		if err != nil {
			return SyncPlanResult{}, err
		}
		if seen[f.Name] {
			return SyncPlanResult{}, api.Errf(http.StatusBadRequest, "duplicate_name", fmt.Sprintf("file name %q is listed twice", f.Name))
		}
		seen[f.Name] = true
		listing[i] = SyncFile{Name: f.Name, SHA256: sha, Size: f.Size}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return SyncPlanResult{}, err
	}
	defer tx.Rollback(ctx)
	moving, err := lockSync(ctx, tx, r.Knowledge)
	if err != nil {
		return SyncPlanResult{}, err
	}
	if moving {
		return SyncPlanResult{}, ErrMoving
	}
	origin := SyncOrigin(r.Pipeline)
	rows, err := loadRows(ctx, tx, r.Knowledge, origin, r.Adopt)
	if err != nil {
		return SyncPlanResult{}, err
	}
	p := classify(listing, rows, origin, r.Run, r.Adopt)
	p.Refused = pruneRefused(p.Owned, len(p.stale), len(listing), r.AllowMassDelete)
	if r.DryRun {
		return p.SyncPlanResult, nil // the deferred rollback ends the transaction
	}
	if len(p.claim) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE knowledge_file SET origin = $3 WHERE knowledge_id = $1 AND file_id = ANY($2) AND origin NOT LIKE 'pipeline:%'`,
			r.Knowledge, p.claim, origin); err != nil {
			return SyncPlanResult{}, fmt.Errorf("adopt files: %w", err)
		}
	}
	// Staged rows are locked before base rows, as the ingest worker's swap does
	// (store locks the staged row, then deletes the base). One UPDATE over both
	// kinds locked them in any order and deadlocked with a swap that was committing.
	dropped, err := s.removeStaged(ctx, tx, r.Knowledge, p.drop)
	if err != nil {
		return SyncPlanResult{}, err
	}
	if len(p.stamp) > 0 {
		for _, kind := range []string{"IS NOT NULL", "IS NULL"} { // staged rows, then base rows
			if _, err := tx.Exec(ctx, `UPDATE knowledge_file SET sync_run = $3 WHERE knowledge_id = $1 AND file_id = ANY($2) AND replaces `+kind+` AND sync_run IS DISTINCT FROM $3`,
				r.Knowledge, p.stamp, r.Run); err != nil {
				return SyncPlanResult{}, fmt.Errorf("stamp files: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return SyncPlanResult{}, fmt.Errorf("commit plan: %w", err)
	}
	for _, fid := range dropped {
		s.dropFile(ctx, fid)
	}
	return p.SyncPlanResult, nil
}

// Put ----------------------------------------------------------------------------

// SyncPutReq is the input of SyncPut.
type SyncPutReq struct {
	SyncScope
	Owner  string    // user id the file belongs to (pipeline.user_id)
	Name   string    // flat file name
	SHA256 string    // hex of the content the caller computed
	Body   io.Reader // the bytes (at most files.max_mb)
}

// SyncPutResult says what was staged.
type SyncPutResult struct {
	Name     string `json:"name"`
	FileID   string `json:"file_id"`
	Replaces string `json:"replaces"` // the file id this version takes over once indexed; "" for a new name
}

// SyncPut stores one file and stages it against the owned row of that name.
func (s *Service) SyncPut(ctx context.Context, r SyncPutReq) (SyncPutResult, error) {
	if err := checkName(r.Name); err != nil {
		return SyncPutResult{}, err
	}
	sha, err := checkSHA(r.Name, r.SHA256)
	if err != nil {
		return SyncPutResult{}, err
	}
	meta := map[string]any{"source": "pipeline", "pipeline": r.Pipeline, "run": r.Run}
	f, err := s.files.Create(ctx, r.Owner, r.Name, "", r.Body, meta)
	if errors.Is(err, files.ErrTooLarge) {
		return SyncPutResult{}, api.Errf(http.StatusRequestEntityTooLarge, "file_too_large", fmt.Sprintf("%q is larger than the upload limit", r.Name))
	}
	if err != nil {
		return SyncPutResult{}, fmt.Errorf("store %q: %w", r.Name, err)
	}
	if f.SHA256 != sha {
		s.dropFile(ctx, f.ID)
		return SyncPutResult{}, api.Errf(http.StatusBadRequest, "checksum_mismatch", fmt.Sprintf("%q: the bytes received do not match the sha256 listed", r.Name))
	}
	replaces, dropped, err := s.stage(ctx, r.SyncScope, r.Name, f.ID)
	if err != nil {
		s.dropFile(ctx, f.ID)
		return SyncPutResult{}, err
	}
	for _, fid := range dropped {
		s.dropFile(ctx, fid)
	}
	s.Wake()
	return SyncPutResult{Name: r.Name, FileID: f.ID, Replaces: replaces}, nil
}

// stage links a new file as the staged version of name. In one transaction and
// in this order (the order matters under READ COMMITTED): lock and drop the
// staged versions of the name, then read the base row, then insert. A swap
// that finished meanwhile has turned its staged row into the base, which the
// read then sees. It returns the file id the new row replaces and the staged
// files it dropped (for the caller to delete after the commit).
func (s *Service) stage(ctx context.Context, sc SyncScope, name, fid string) (replaces string, dropped []string, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback(ctx)
	moving, err := lockSync(ctx, tx, sc.Knowledge)
	if err != nil {
		return "", nil, err
	}
	if moving {
		return "", nil, ErrMoving
	}
	origin := SyncOrigin(sc.Pipeline)
	rows, err := tx.Query(ctx, `SELECT kf.file_id FROM knowledge_file kf JOIN file f ON f.id = kf.file_id
		WHERE kf.knowledge_id = $1 AND kf.origin = $2 AND f.filename = $3 AND kf.replaces IS NOT NULL
		ORDER BY kf.file_id FOR UPDATE OF kf`, sc.Knowledge, origin, name)
	if err != nil {
		return "", nil, fmt.Errorf("find staged versions: %w", err)
	}
	if dropped, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return "", nil, fmt.Errorf("find staged versions: %w", err)
	}
	for _, old := range dropped {
		if err := s.removeFile(ctx, tx, sc.Knowledge, old); err != nil {
			return "", nil, err
		}
	}
	var base *string
	err = tx.QueryRow(ctx, `SELECT kf.file_id FROM knowledge_file kf JOIN file f ON f.id = kf.file_id
		WHERE kf.knowledge_id = $1 AND kf.origin = $2 AND f.filename = $3 AND kf.replaces IS NULL
		ORDER BY kf.added_at DESC, kf.file_id DESC LIMIT 1`, sc.Knowledge, origin, name).Scan(&base)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, fmt.Errorf("find base version: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO knowledge_file (knowledge_id, file_id, origin, replaces, sync_run) VALUES ($1, $2, $3, $4, $5)`,
		sc.Knowledge, fid, origin, base, sc.Run); err != nil {
		return "", nil, fmt.Errorf("stage file: %w", err)
	}
	if base != nil {
		replaces = *base
		if _, err := tx.Exec(ctx, `UPDATE knowledge_file SET sync_run = $3 WHERE knowledge_id = $1 AND file_id = $2`, sc.Knowledge, replaces, sc.Run); err != nil {
			return "", nil, fmt.Errorf("stamp base version: %w", err)
		}
	}
	if err := s.touch(ctx, tx, sc.Knowledge); err != nil {
		return "", nil, err
	}
	return replaces, dropped, tx.Commit(ctx)
}

// State --------------------------------------------------------------------------

// SyncStatus is how far the files of one run in one collection have come.
type SyncStatus struct {
	Waiting      int           `json:"waiting"`       // extracting or indexing
	WaitingNames []string      `json:"waiting_names"` // the first few of them
	Indexed      int           `json:"indexed"`       // live and indexed
	Failed       int           `json:"failed"`
	Failures     []SyncFailure `json:"failures"` // the first few
	Moving       bool          `json:"moving"`   // the collection is changing embedding model
}

// SyncFailure is a file that could not be indexed.
type SyncFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// Settled reports that nothing waits and nothing moves, so the run may close.
func (st SyncStatus) Settled() bool { return st.Waiting == 0 && !st.Moving }

// SyncState reports the files the run listed or uploaded. A name with a staged
// new version counts by the staged row; its old version keeps serving.
func (s *Service) SyncState(ctx context.Context, sc SyncScope) (SyncStatus, error) {
	st := SyncStatus{WaitingNames: []string{}, Failures: []SyncFailure{}}
	err := s.db.QueryRow(ctx, `SELECT coalesce(meta->>'reindex_to', '') <> '' FROM knowledge WHERE id = $1`, sc.Knowledge).Scan(&st.Moving)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, ErrNotFound
	}
	if err != nil {
		return st, fmt.Errorf("read collection: %w", err)
	}
	type row struct {
		name, status, reason string
		staged               bool
	}
	rows, err := s.db.Query(ctx, `SELECT f.filename, kf.status, kf.replaces IS NOT NULL,
			coalesce(nullif(f.error, ''), k.meta->'errors'->>kf.file_id, '')
		FROM knowledge_file kf JOIN file f ON f.id = kf.file_id JOIN knowledge k ON k.id = kf.knowledge_id
		WHERE kf.knowledge_id = $1 AND kf.origin = $2 AND kf.sync_run = $3
		ORDER BY f.filename, kf.replaces IS NOT NULL, kf.file_id`, sc.Knowledge, SyncOrigin(sc.Pipeline), sc.Run)
	if err != nil {
		return st, fmt.Errorf("sync state: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.name, &x.status, &x.staged, &x.reason)
	})
	if err != nil {
		return st, fmt.Errorf("sync state: %w", err)
	}
	var cur []row // one per name: the staged row, else the base
	for _, x := range all {
		if n := len(cur); n > 0 && cur[n-1].name == x.name {
			cur[n-1] = x
		} else {
			cur = append(cur, x)
		}
	}
	for _, x := range cur {
		switch x.status {
		case "failed":
			st.Failed++
			if x.reason == "" {
				x.reason = "indexing failed"
			}
			if x.staged {
				x.reason += "; the previous version was kept"
			}
			if len(st.Failures) < maxReported {
				st.Failures = append(st.Failures, SyncFailure{Name: x.name, Error: x.reason})
			}
		case "completed":
			st.Indexed++
		default: // pending, processing
			st.Waiting++
			if len(st.WaitingNames) < maxReported {
				st.WaitingNames = append(st.WaitingNames, x.name)
			}
		}
	}
	return st, nil
}

// Prune --------------------------------------------------------------------------

// SyncPruneReq is the input of SyncPrune.
type SyncPruneReq struct {
	SyncScope
	Listed int  // names the run listed for this collection (an empty listing is refused)
	Owned  int  // SyncPlanResult.Owned of the run's first plan; 0 counts the owned files now
	Allow  bool // pipeline.allow_mass_delete: lets a refused prune through
	DryRun bool
}

// SyncPruneResult says what was, or would be, removed.
type SyncPruneResult struct {
	Stale   []string `json:"stale"`   // owned names this run did not list
	Removed []string `json:"removed"` // those removed (none when refused or dry)
	Refused string   `json:"refused,omitempty"`
}

// SyncPrune removes the pipeline's rows (and staged versions) that the run did
// not list. It never touches rows the pipeline does not own.
func (s *Service) SyncPrune(ctx context.Context, r SyncPruneReq) (SyncPruneResult, error) {
	res := SyncPruneResult{Stale: []string{}, Removed: []string{}}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if _, err := lockSync(ctx, tx, r.Knowledge); err != nil {
		return res, err
	}
	rows, err := loadRows(ctx, tx, r.Knowledge, SyncOrigin(r.Pipeline), false)
	if err != nil {
		return res, err
	}
	// The guard weighs the removal against what the pipeline owned before the run:
	// this run's own uploads would dilute it (a wrong folder with as many files as
	// the collection has would then wipe it without asking).
	owned := r.Owned
	count := owned == 0
	var stale, staged []syncRow
	for _, x := range rows {
		switch {
		case x.replaces == nil:
			if count {
				owned++
			}
			if !x.stamped(r.Run) {
				stale = append(stale, x)
			}
		case !x.stamped(r.Run):
			staged = append(staged, x)
		}
	}
	slices.SortFunc(stale, func(a, b syncRow) int { return strings.Compare(a.name, b.name) })
	for _, x := range stale {
		res.Stale = append(res.Stale, x.name)
	}
	if res.Refused = pruneRefused(owned, len(stale), r.Listed, r.Allow); res.Refused != "" || r.DryRun || len(stale)+len(staged) == 0 {
		return res, nil
	}
	// staged versions first, then the bases: the order the swap locks them in
	stagedIDs := make([]string, len(staged))
	for i, x := range staged {
		stagedIDs[i] = x.fileID
	}
	drop, err := s.removeStaged(ctx, tx, r.Knowledge, stagedIDs)
	if err != nil {
		return res, err
	}
	for _, x := range stale {
		if err := s.removeFile(ctx, tx, r.Knowledge, x.fileID); err != nil {
			return res, err
		}
		drop = append(drop, x.fileID)
	}
	if err := s.touch(ctx, tx, r.Knowledge); err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit prune: %w", err)
	}
	for _, fid := range drop {
		s.dropFile(ctx, fid)
	}
	res.Removed = res.Stale
	return res, nil
}

// Release ------------------------------------------------------------------------

// SyncRelease ends a pipeline's claim on its collections: staged versions are
// dropped and every row it owns goes back to origin "kera", staying in place.
func (s *Service) SyncRelease(ctx context.Context, pipelineID string) error {
	origin := SyncOrigin(pipelineID)
	rows, err := s.db.Query(ctx, `SELECT DISTINCT knowledge_id FROM knowledge_file WHERE origin = $1`, origin)
	if err != nil {
		return fmt.Errorf("list pipeline collections: %w", err)
	}
	kids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list pipeline collections: %w", err)
	}
	for _, kid := range kids {
		if err := s.release(ctx, kid, origin); err != nil {
			return fmt.Errorf("release collection %s: %w", kid, err)
		}
	}
	return nil
}

func (s *Service) release(ctx context.Context, kid, origin string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := lockSync(ctx, tx, kid); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT file_id FROM knowledge_file WHERE knowledge_id = $1 AND origin = $2 AND replaces IS NOT NULL ORDER BY file_id FOR UPDATE`, kid, origin)
	if err != nil {
		return fmt.Errorf("find staged versions: %w", err)
	}
	staged, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("find staged versions: %w", err)
	}
	for _, fid := range staged {
		if err := s.removeFile(ctx, tx, kid, fid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE knowledge_file SET origin = 'kera', sync_run = NULL WHERE knowledge_id = $1 AND origin = $2`, kid, origin); err != nil {
		return fmt.Errorf("hand files back: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit release: %w", err)
	}
	for _, fid := range staged {
		s.dropFile(ctx, fid)
	}
	return nil
}

// dropFile deletes a file (row and blob) that a swap, a prune or a failed put
// left behind, unless something still refers to it: another collection that
// holds it, or a chat that attached it. Errors are logged: the document was
// already replaced or removed, a stray file is only wasted space.
func (s *Service) dropFile(ctx context.Context, fid string) {
	var used bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM knowledge_file WHERE file_id = $1) OR EXISTS (SELECT 1 FROM chat_file WHERE file_id = $1)`,
		fid).Scan(&used); err != nil {
		slog.Warn("knowledge: check file before delete", "file", fid, "err", err)
		return
	}
	if used {
		return
	}
	if err := s.files.Delete(ctx, fid); err != nil && !errors.Is(err, files.ErrNotFound) {
		slog.Warn("knowledge: delete replaced file", "file", fid, "err", err)
	}
}
