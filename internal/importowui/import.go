// Package importowui copies an Open WebUI 0.13 Postgres database into Kera:
// users, keys, groups, grants, folders, chats (history tree -> message rows),
// files (with blobs), knowledge, memories, prompts and feedback. Source ids
// are kept and every row is tagged origin='openwebui', so re-runs upsert in
// place and --prune-missing never touches rows created in Kera.
package importowui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Origin tags imported rows.
const Origin = "openwebui"

// Entities are the --only names, in import order.
var Entities = []string{"users", "groups", "folders", "files", "chats", "knowledge", "grants", "memories", "prompts", "feedback"}

// Options control one import run.
type Options struct {
	UploadsDir   string    // source uploads directory (read only)
	DataDir      string    // KERA_DATA_DIR; blobs go to <DataDir>/uploads/<id>/<name>
	DryRun       bool      // run everything in a transaction and roll it back; copy no blobs
	NoFiles      bool      // keep file rows but skip blob checks and copies
	PruneMissing bool      // delete origin='openwebui' rows that are gone from the source
	Only         []string  // subset of Entities; empty means all
	Since        time.Time // chats and files updated at or after this time (zero = all)
	Log          *slog.Logger
}

func (o Options) wants(entity string) bool {
	if len(o.Only) == 0 {
		return true
	}
	for _, e := range o.Only {
		if e == entity {
			return true
		}
	}
	return false
}

// Stat is the outcome for one entity.
type Stat struct {
	Source    int            `json:"source"`            // rows read from the source
	Inserted  int            `json:"inserted"`          // rows that were not in the target
	Existing  int            `json:"existing"`          // rows already in the target (updated, or kept because newer)
	Skipped   map[string]int `json:"skipped,omitempty"` // reason -> rows not imported
	Notes     map[string]int `json:"notes,omitempty"`   // imported with an adjustment
	Pruned    int            `json:"pruned,omitempty"`  // target rows deleted (or, in verify, extra)
	Decisions []string       `json:"-"`                 // first decisions, for --dry-run
}

func (s *Stat) skip(reason, id string) {
	if s.Skipped == nil {
		s.Skipped = map[string]int{}
	}
	s.Skipped[reason]++
	s.decide("skip " + id + ": " + reason)
}

func (s *Stat) note(what string) {
	if s.Notes == nil {
		s.Notes = map[string]int{}
	}
	s.Notes[what]++
}

func (s *Stat) decide(d string) {
	if len(s.Decisions) < 20 {
		s.Decisions = append(s.Decisions, d)
	}
}

// SkippedTotal sums Skipped.
func (s *Stat) SkippedTotal() int {
	n := 0
	for _, v := range s.Skipped {
		n += v
	}
	return n
}

// Report collects per-entity stats in a stable order.
type Report struct {
	Order        []string         `json:"-"`
	Stats        map[string]*Stat `json:"stats"`
	MissingBlobs []string         `json:"missing_blobs,omitempty"` // file ids
}

// Stat returns (creating) the stat for name.
func (r *Report) Stat(name string) *Stat {
	if r.Stats == nil {
		r.Stats = map[string]*Stat{}
	}
	s, ok := r.Stats[name]
	if !ok {
		s = &Stat{}
		r.Stats[name] = s
		r.Order = append(r.Order, name)
	}
	return s
}

// importer carries the state of one run.
type importer struct {
	src   *pgxpool.Pool
	tx    pgx.Tx
	opt   Options
	rep   *Report
	log   *slog.Logger
	users map[string]string // source user id -> target user id
	// filled by steps, used by later ones
	pruneDirs []string // blob dirs to remove after commit
}

// Run imports src into dst and records a migration_run row. source is a
// display name for the source (never a DSN with a password).
func Run(ctx context.Context, src, dst *pgxpool.Pool, source string, opt Options) (*Report, error) {
	flags, _ := json.Marshal(map[string]any{"dry_run": opt.DryRun, "no_files": opt.NoFiles, "prune_missing": opt.PruneMissing,
		"only": opt.Only, "since": opt.Since, "uploads_dir": opt.UploadsDir})
	var runID int64
	if err := dst.QueryRow(ctx, `INSERT INTO migration_run (source, flags) VALUES ($1, $2) RETURNING id`, source, flags).Scan(&runID); err != nil {
		return nil, fmt.Errorf("record migration run: %w", err)
	}
	rep, _, err := execute(ctx, src, dst, opt)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	counts, _ := json.Marshal(rep)
	if _, uerr := dst.Exec(ctx, `UPDATE migration_run SET counts = $2, finished_at = now(), error = $3 WHERE id = $1`, runID, counts, msg); uerr != nil && err == nil {
		err = fmt.Errorf("finish migration run: %w", uerr)
	}
	return rep, err
}

// execute runs every wanted step in one transaction, committed unless DryRun.
// It also returns the source -> target user id map it resolved.
func execute(ctx context.Context, src, dst *pgxpool.Pool, opt Options) (*Report, map[string]string, error) {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	im := &importer{src: src, opt: opt, rep: &Report{}, log: opt.Log, users: map[string]string{}}
	tx, err := dst.Begin(ctx)
	if err != nil {
		return im.rep, nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck // no-op after commit
	im.tx = tx
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"users", im.importUsers}, {"groups", im.importGroups}, {"folders", im.importFolders},
		{"files", im.importFiles}, {"chats", im.importChats}, {"knowledge", im.importKnowledge},
		{"grants", im.importGrants}, {"memories", im.importMemories}, {"prompts", im.importPrompts},
		{"feedback", im.importFeedback},
	}
	if err := im.loadUserMap(ctx); err != nil {
		return im.rep, nil, err
	}
	for _, s := range steps {
		if !opt.wants(s.name) {
			continue
		}
		start := time.Now()
		if err := s.fn(ctx); err != nil {
			return im.rep, nil, fmt.Errorf("import %s: %w", s.name, err)
		}
		im.log.Info("import step done", "entity", s.name, "ms", time.Since(start).Milliseconds())
	}
	if opt.PruneMissing {
		if err := im.prune(ctx); err != nil {
			return im.rep, nil, fmt.Errorf("prune: %w", err)
		}
	}
	if opt.DryRun {
		return im.rep, im.users, nil // deferred rollback
	}
	if err := tx.Commit(ctx); err != nil {
		return im.rep, nil, fmt.Errorf("commit: %w", err)
	}
	for _, d := range im.pruneDirs {
		if err := os.RemoveAll(d); err != nil {
			im.log.Warn("remove pruned blob dir", "dir", d, "err", err)
		}
	}
	return im.rep, im.users, nil
}

// Outcomes of upsert.
const (
	outInserted = iota // the row was new
	outUpdated         // an existing row was updated in place
	outKept            // a conflict was left alone (DO NOTHING, or the target row is newer)
)

// upsert runs an INSERT ... RETURNING (xmax = 0) statement and records whether
// the row was new. No returned row means a conflict that was left alone.
func (im *importer) upsert(ctx context.Context, st *Stat, id, sql string, args ...any) (int, error) {
	var inserted bool
	err := im.tx.QueryRow(ctx, sql, args...).Scan(&inserted)
	return st.record(id, inserted, err)
}

func (s *Stat) record(id string, inserted bool, err error) (int, error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		s.Existing++
		s.decide("keep " + id)
		return outKept, nil
	case err != nil:
		return 0, fmt.Errorf("upsert %s: %w", id, err)
	case inserted:
		s.Inserted++
		s.decide("insert " + id)
		return outInserted, nil
	}
	s.Existing++
	s.decide("update " + id)
	return outUpdated, nil
}

// sourceRows streams a source query, calling fn per row.
func (im *importer) sourceRows(ctx context.Context, sql string, args []any, fn func(pgx.Rows) error) error {
	rows, err := im.src.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("source query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// targetIDs returns the ids present in a target table.
func (im *importer) targetIDs(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := im.tx.Query(ctx, `SELECT id FROM `+table)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", table, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", table, err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// sourceIDs lists every id of a source table (for pruning; ignores --since).
func (im *importer) sourceIDs(ctx context.Context, sql string) ([]string, error) {
	rows, err := im.src.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("source ids: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (im *importer) since() int64 {
	if im.opt.Since.IsZero() {
		return 0
	}
	return im.opt.Since.Unix()
}

func (im *importer) progress(entity string, done, total int) {
	if done%200 == 0 || done == total {
		im.log.Info("import progress", "entity", entity, "done", done, "total", total)
	}
}

// jsonObject decodes a JSON object column; anything else yields an empty map.
func jsonObject(b []byte) map[string]any {
	m := map[string]any{}
	if len(b) > 0 {
		if json.Unmarshal(b, &m) != nil || m == nil {
			m = map[string]any{}
		}
	}
	return m
}

func ts(v *int64) time.Time {
	if v == nil || *v == 0 {
		return time.Now().UTC()
	}
	return epoch(*v)
}

// SourceName renders a DSN for display and the migration_run row, without a password.
func SourceName(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		return "openwebui"
	}
	return u.Redacted()
}

// safeName keeps a stored file name to one path element.
func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// put is upsert for callers that only need the error.
func (im *importer) put(ctx context.Context, st *Stat, id, sql string, args ...any) error {
	_, err := im.upsert(ctx, st, id, sql, args...)
	return err
}
