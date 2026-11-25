// Package pipeline lets outside scripts keep knowledge collections in sync with
// a folder that lives somewhere else, and shows each run as a flow of steps.
//
// Kera never starts or retries a script. A script (or `kera pipeline push`)
// opens a run with the pipeline's token, reports its steps and logs, lists a
// collection's files, uploads what the server asks for and finishes the run.
// Kera then waits for the new files to be indexed and, if the listing was
// complete and nothing failed, removes the files the folder no longer holds
// (settle). The sync itself is internal/knowledge/sync.go; this package owns
// tokens, runs, steps, logs, the ticker that closes stuck runs, and the CLI.
//
// Data: pipeline, pipeline_run, pipeline_step (migration 00011). A step is
// either a script step (reported by the script, keyed by its name) or a Kera
// step (one per target collection, keyed by the collection id, written only
// by this package). docs/pipelines.md has the JSON shapes.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/guard"
	"github.com/cdmx-in/kera/internal/knowledge"
)

func init() { app.Register("pipeline", Mount) }

const (
	tokenPrefix = "kp-"

	maxStepsBody = 1 << 20   // bytes of one steps report
	maxSteps     = 100       // script steps per run
	maxErrorLen  = 2000      // characters of an error text
	maxStepLog   = 256 << 10 // bytes of log kept per step (the tail)
	maxRunLog    = 4 << 20   // bytes of log kept per run
	maxRuns      = 50        // runs kept per pipeline, trimmed at run start
	maxStepName  = 100       // bytes
	maxCounts    = 20        // count keys per step
	maxNodeLines = 200       // names a Kera step lists in its log per kind

	idleLimit = 30 * time.Minute // no report from the script, or no indexing progress
	scriptCap = 3 * time.Hour    // longest script phase

	// keraSeqBase numbers the Kera steps so they never collide with script
	// steps (at most maxSteps of them) and sort after them.
	keraSeqBase = 1000
)

// slugRe is a pipeline name: it appears in URLs, environment variables and commands.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Service is the pipeline feature.
type Service struct {
	a   *app.App
	db  *pgxpool.Pool
	k   *knowledge.Service
	now func() time.Time // the clock every stored time comes from; tests move it

	// settleMu serialises settle, force and finish.
	// ponytail: one process; a pg_advisory_xact_lock on the run is the upgrade
	// if two servers ever share a database (it must not be held across the
	// knowledge calls: a poll storm would then use up the pool).
	settleMu sync.Mutex
}

// New builds the service; Mount also starts the ticker and the routes.
func New(a *app.App, k *knowledge.Service) *Service {
	return &Service{a: a, db: a.DB, k: k, now: time.Now}
}

// Mount registers the routes and starts the minute ticker.
func Mount(a *app.App) error {
	s := New(a, knowledge.For(a))
	s.routes()
	go s.loop()
	return nil
}

// dbq is the part of a pool or a transaction the helpers use.
type dbq interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Pipeline ------------------------------------------------------------------------

// Pipeline is one pipeline row.
type Pipeline struct {
	ID, Name, Title, UserID string
	Targets                 []string // knowledge ids it may write
	EveryMinutes            int
	TokenHash, TokenPrefix  string
	Adopt, AllowMassDelete  bool
	Enabled, Managed        bool
	CreatedAt, UpdatedAt    time.Time
}

const pipelineCols = `id, name, title, user_id, targets, every_minutes, coalesce(token_hash, ''), token_prefix,
	adopt, allow_mass_delete, enabled, managed, created_at, updated_at`

func scanPipeline(row pgx.Row) (*Pipeline, error) {
	p := &Pipeline{}
	err := row.Scan(&p.ID, &p.Name, &p.Title, &p.UserID, &p.Targets, &p.EveryMinutes, &p.TokenHash, &p.TokenPrefix,
		&p.Adopt, &p.AllowMassDelete, &p.Enabled, &p.Managed, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoPipeline
	}
	if err != nil {
		return nil, fmt.Errorf("scan pipeline: %w", err)
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return p, nil
}

var (
	errNoPipeline = api.Errf(http.StatusNotFound, "not_found", "Pipeline not found")
	errNoRun      = api.Errf(http.StatusNotFound, "not_found", "Run not found")
	errRunClosed  = api.Errf(http.StatusConflict, "run_closed", "This run is closed")
	errDryRun     = api.Errf(http.StatusBadRequest, "dry_run", "This is a dry run: nothing is uploaded")
)

func (s *Service) pipelineBy(ctx context.Context, q dbq, col, val string) (*Pipeline, error) {
	return scanPipeline(q.QueryRow(ctx, `SELECT `+pipelineCols+` FROM pipeline WHERE `+col+` = $1`, val))
}

// Runs ----------------------------------------------------------------------------

// target is what a run records about one collection's listing.
type target struct {
	Complete bool `json:"complete"` // the script listed every file of the folder
	Listed   int  `json:"listed"`
	Owned    int  `json:"owned"`   // files the pipeline owned here before the run (the first plan's value; the prune guard weighs against it)
	Planned  int  `json:"planned"` // uploads the plans of this run asked for, added up (a PUT beyond that is refused)
}

// run is one pipeline_run row.
type run struct {
	ID         int64
	PipelineID string
	Trigger    string
	DryRun     bool
	Status     string
	ClientDone bool // the script finished; Kera may still be indexing
	Targets    map[string]target
	Error      string
	Source     map[string]string // where the script runs, as it reported (may be empty)
	StartedAt  time.Time
	ActiveAt   time.Time
	FinishedAt *time.Time
}

const runCols = `id, pipeline_id, trigger, dry_run, status, client_done, targets, error, source, started_at, active_at, finished_at`

func scanRun(row pgx.Row) (*run, error) {
	r := &run{}
	err := row.Scan(&r.ID, &r.PipelineID, &r.Trigger, &r.DryRun, &r.Status, &r.ClientDone, &r.Targets, &r.Error, &r.Source, &r.StartedAt, &r.ActiveAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoRun
	}
	if err != nil {
		return nil, fmt.Errorf("scan run: %w", err)
	}
	r.StartedAt, r.ActiveAt, r.FinishedAt = r.StartedAt.UTC(), r.ActiveAt.UTC(), utcPtr(r.FinishedAt)
	if r.Targets == nil {
		r.Targets = map[string]target{}
	}
	return r, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func (s *Service) loadRun(ctx context.Context, q dbq, id int64) (*run, error) {
	return scanRun(q.QueryRow(ctx, `SELECT `+runCols+` FROM pipeline_run WHERE id = $1`, id))
}

// activeError is "a run is already open" with who holds it.
type activeError struct {
	Run       int64
	Trigger   string
	StartedAt time.Time
	ActiveAt  time.Time
}

func (e *activeError) Error() string {
	return fmt.Sprintf("this pipeline already has a run in progress (run %d, started %s UTC, last report %s UTC)",
		e.Run, e.StartedAt.UTC().Format("2006-01-02 15:04"), e.ActiveAt.UTC().Format("15:04"))
}

type collection struct{ ID, Name string }

// collectionsOf returns the pipeline's target collections that still exist, in target order.
func (s *Service) collectionsOf(ctx context.Context, q dbq, targets []string) ([]collection, error) {
	rows, err := q.Query(ctx, `SELECT id, name FROM knowledge WHERE id = ANY($1)`, targets)
	if err != nil {
		return nil, fmt.Errorf("list target collections: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (collection, error) {
		var c collection
		return c, r.Scan(&c.ID, &c.Name)
	})
	if err != nil {
		return nil, fmt.Errorf("list target collections: %w", err)
	}
	var out []collection
	for _, id := range targets {
		if i := slices.IndexFunc(all, func(c collection) bool { return c.ID == id }); i >= 0 {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// startIn is the body of a run start.
type startIn struct {
	Trigger string            `json:"trigger"`
	Steps   []string          `json:"steps"`
	DryRun  bool              `json:"dry_run"`
	Source  map[string]string `json:"source,omitempty"` // where the script runs; see cleanSource
}

// sourceKeys are the facts a script may report about where it runs; anything else is dropped.
var sourceKeys = []string{"host", "command", "dir", "user", "client"}

const maxSourceValue = 300

// cleanSource keeps the known keys and runs each value through capText (secret mask, cap), so what a
// script says about itself is safe to store and show; a command line can carry a token.
func cleanSource(in map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range sourceKeys {
		if v := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, capText(in[k], maxSourceValue)); strings.TrimSpace(v) != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// startRun opens a run: old runs beyond maxRuns are trimmed, the declared script
// steps and one pending Kera step per target collection are created. It returns
// *activeError while another run is open (the partial unique index decides).
func (s *Service) startRun(ctx context.Context, p *Pipeline, in startIn) (*run, []collection, error) {
	if in.Trigger == "" {
		in.Trigger = "script"
	}
	if in.Trigger != "script" && in.Trigger != "push" {
		return nil, nil, api.Errf(http.StatusBadRequest, "bad_trigger", "trigger must be script or push")
	}
	names, err := cleanNames(in.Steps)
	if err != nil {
		return nil, nil, err
	}
	cols, err := s.collectionsOf(ctx, s.db, p.Targets)
	if err != nil {
		return nil, nil, err
	}
	src, _ := json.Marshal(cleanSource(in.Source))
	now := s.now()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM pipeline_run WHERE pipeline_id = $1 AND finished_at IS NOT NULL
		AND id NOT IN (SELECT id FROM pipeline_run WHERE pipeline_id = $1 ORDER BY id DESC LIMIT $2)`, p.ID, maxRuns-1); err != nil {
		return nil, nil, fmt.Errorf("trim old runs: %w", err)
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO pipeline_run (pipeline_id, trigger, dry_run, started_at, active_at, source) VALUES ($1, $2, $3, $4, $4, $5::jsonb) RETURNING id`,
		p.ID, in.Trigger, in.DryRun, now, string(src)).Scan(&id)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return nil, nil, s.holder(ctx, p.ID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("insert run: %w", err)
	}
	for i, n := range names {
		if _, err := tx.Exec(ctx, `INSERT INTO pipeline_step (run_id, seq, kera, key, name) VALUES ($1, $2, false, $3, $3)`, id, i+1, n); err != nil {
			return nil, nil, fmt.Errorf("insert step: %w", err)
		}
	}
	for i, c := range cols {
		if _, err := tx.Exec(ctx, `INSERT INTO pipeline_step (run_id, seq, kera, key, name) VALUES ($1, $2, true, $3, $4)`, id, keraSeqBase+i, c.ID, c.Name); err != nil {
			return nil, nil, fmt.Errorf("insert collection step: %w", err)
		}
	}
	r, err := s.loadRun(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	return r, cols, tx.Commit(ctx)
}

// holder describes the run that blocks a start.
func (s *Service) holder(ctx context.Context, pipelineID string) error {
	e := &activeError{}
	err := s.db.QueryRow(ctx, `SELECT id, trigger, started_at, active_at FROM pipeline_run WHERE pipeline_id = $1 AND finished_at IS NULL`, pipelineID).
		Scan(&e.Run, &e.Trigger, &e.StartedAt, &e.ActiveAt)
	if err != nil {
		return fmt.Errorf("find the open run: %w", err)
	}
	return e
}

// cleanNames validates declared step names (no duplicates, at most maxSteps).
func cleanNames(in []string) ([]string, error) {
	var out []string
	for _, n := range in {
		n = cleanName(n)
		if n == "" {
			return nil, api.Errf(http.StatusBadRequest, "bad_step", fmt.Sprintf("a step name must be 1 to %d characters", maxStepName))
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) > maxSteps {
		return nil, api.Errf(http.StatusBadRequest, "too_many_steps", fmt.Sprintf("a run holds at most %d steps", maxSteps))
	}
	return out, nil
}

// cleanName trims a step name and strips control characters; "" when it is unusable.
func cleanName(n string) string {
	n = strings.TrimSpace(guard.StripControl(n))
	if n == "" || len(n) > maxStepName || !utf8.ValidString(n) {
		return ""
	}
	return n
}

// Steps ---------------------------------------------------------------------------

// step is one pipeline_step row.
type step struct {
	Seq        int
	Kera       bool
	Key, Name  string
	Status     string
	StartedAt  *time.Time
	FinishedAt *time.Time
	Error      string
	Counts     map[string]int64
	Log        string
	LogDropped bool
}

const stepCols = `seq, kera, key, name, status, started_at, finished_at, error, counts, log_dropped`

func scanStep(row pgx.Row, withLog bool) (step, error) {
	var st step
	dst := []any{&st.Seq, &st.Kera, &st.Key, &st.Name, &st.Status, &st.StartedAt, &st.FinishedAt, &st.Error, &st.Counts, &st.LogDropped}
	if withLog {
		dst = append(dst, &st.Log)
	}
	if err := row.Scan(dst...); err != nil {
		return st, err
	}
	st.StartedAt, st.FinishedAt = utcPtr(st.StartedAt), utcPtr(st.FinishedAt)
	if st.Counts == nil {
		st.Counts = map[string]int64{}
	}
	return st, nil
}

// loadSteps reads a run's steps in display order (script steps, then Kera steps).
func (s *Service) loadSteps(ctx context.Context, q dbq, runID int64, withLog bool) ([]step, error) {
	cols := stepCols
	if withLog {
		cols += ", log"
	}
	rows, err := q.Query(ctx, `SELECT `+cols+` FROM pipeline_step WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	defer rows.Close()
	var out []step
	for rows.Next() {
		st, err := scanStep(rows, withLog)
		if err != nil {
			return nil, fmt.Errorf("scan step: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// saveStep writes back everything a report can change.
func saveStep(ctx context.Context, q dbq, runID int64, st step) error {
	if st.Counts == nil {
		st.Counts = map[string]int64{}
	}
	if _, err := q.Exec(ctx, `UPDATE pipeline_step SET status = $3, started_at = $4, finished_at = $5, error = $6, counts = $7, log = $8, log_dropped = $9
		WHERE run_id = $1 AND seq = $2`, runID, st.Seq, st.Status, st.StartedAt, st.FinishedAt, st.Error, st.Counts, st.Log, st.LogDropped); err != nil {
		return fmt.Errorf("save step: %w", err)
	}
	return nil
}

// stepIn is one item of a steps report.
type stepIn struct {
	Name   string           `json:"name"`
	Status string           `json:"status"` // pending | running | ok | warning | failed | skipped
	Error  string           `json:"error"`
	Counts map[string]int64 `json:"counts"`
	Log    string           `json:"log"`
}

// rank orders statuses; a closed step only moves up to warning or failed.
func rank(status string) int {
	switch status {
	case "pending":
		return 0
	case "running":
		return 1
	case "ok", "skipped":
		return 2
	case "warning":
		return 3
	case "failed":
		return 4
	}
	return -1
}

func closed(status string) bool { return rank(status) >= 2 }

var countKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,39}$`)

// capText masks secrets, strips control characters and cuts to n characters.
func capText(s string, n int) string {
	s, _ = guard.MaskSecrets(s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n-1]) + "…"
	}
	return s
}

// report applies one item to a step. A step moves pending -> running -> closed;
// a closed step ignores everything but a worse verdict (warning, then failed),
// so re-entering a finished step changes nothing. budget is what is left of the
// run's log allowance (nil: unlimited).
func (st *step) report(in stepIn, now time.Time, budget *int) {
	to := in.Status
	switch {
	case to == "" && st.Status == "pending":
		to = "running"
	case to == "":
		to = st.Status
	}
	switch {
	case to == "pending" || rank(to) < 0:
	case !closed(st.Status):
		st.Status = to
		if st.StartedAt == nil && to != "skipped" {
			st.StartedAt = &now
		}
		if closed(to) {
			st.FinishedAt = &now
		}
	case rank(to) > rank(st.Status): // a closed step only gets worse: ok -> warning -> failed
		st.Status, st.FinishedAt = to, &now
	}
	if e := capText(in.Error, maxErrorLen); e != "" && !strings.Contains(st.Error, e) {
		if st.Error != "" {
			e = st.Error + "; " + e
		}
		st.Error = capText(e, maxErrorLen)
	}
	if st.Counts == nil {
		st.Counts = map[string]int64{}
	}
	for k, v := range in.Counts {
		if _, have := st.Counts[k]; countKeyRe.MatchString(k) && (have || len(st.Counts) < maxCounts) {
			st.Counts[k] = v
		}
	}
	st.addLog(in.Log, now, budget)
}

// addLog appends text, one timestamped line per line (the server's UTC clock),
// masked and stripped. The step keeps the last maxStepLog bytes; a run keeps
// maxRunLog bytes in all, after which new lines are dropped. Either way
// LogDropped says that the log is not the whole story.
// ponytail: a secret split over two reports (a private key cut at a flush) is
// masked only up to the cut; the rules work on one report at a time.
func (st *step) addLog(text string, now time.Time, budget *int) {
	text, _ = guard.MaskSecrets(text)
	var sb strings.Builder
	ts := now.UTC().Format("15:04:05") + " "
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			sb.WriteString(ts + strings.TrimRight(line, " \t") + "\n")
		}
	}
	add := sb.String()
	if add == "" {
		return
	}
	if budget != nil {
		if len(add) > *budget {
			st.LogDropped = true
			return
		}
		*budget -= len(add)
	}
	st.Log += add
	if len(st.Log) > maxStepLog {
		cut := st.Log[len(st.Log)-maxStepLog:]
		if i := strings.IndexByte(cut, '\n'); i >= 0 {
			cut = cut[i+1:] // drop the partial first line
		}
		st.Log, st.LogDropped = strings.ToValidUTF8(cut, ""), true
	}
}

// runBudget is what is left of the run's log allowance.
func runBudget(ctx context.Context, q dbq, runID int64) (int, error) {
	var used int64
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(octet_length(log)), 0) FROM pipeline_step WHERE run_id = $1`, runID).Scan(&used); err != nil {
		return 0, fmt.Errorf("measure run log: %w", err)
	}
	return max(0, maxRunLog-int(used)), nil
}

// applySteps applies a script's report in order and returns how many items it
// had to ignore (unusable name, unknown status, a 101st step). The run's row is
// locked, so reports of one run apply one at a time.
func (s *Service) applySteps(ctx context.Context, runID int64, items []stepIn) (ignored int, err error) {
	now := s.now()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	r, err := scanRun(tx.QueryRow(ctx, `SELECT `+runCols+` FROM pipeline_run WHERE id = $1 FOR UPDATE`, runID))
	if err != nil {
		return 0, err
	}
	if r.FinishedAt != nil || r.ClientDone {
		return 0, errRunClosed
	}
	var names []string
	for i := range items {
		items[i].Name = cleanName(items[i].Name)
		names = append(names, items[i].Name)
	}
	rows, err := tx.Query(ctx, `SELECT `+stepCols+`, log FROM pipeline_step WHERE run_id = $1 AND NOT kera AND key = ANY($2)`, runID, names)
	if err != nil {
		return 0, fmt.Errorf("load steps: %w", err)
	}
	have := map[string]*step{}
	for rows.Next() {
		st, err := scanStep(rows, true)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan step: %w", err)
		}
		have[st.Key] = &st
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var count, next int
	if err := tx.QueryRow(ctx, `SELECT count(*), coalesce(max(seq), 0) FROM pipeline_step WHERE run_id = $1 AND NOT kera`, runID).Scan(&count, &next); err != nil {
		return 0, fmt.Errorf("count steps: %w", err)
	}
	budget, err := runBudget(ctx, tx, runID)
	if err != nil {
		return 0, err
	}
	var created, touched []string
	for _, in := range items {
		if in.Name == "" || (in.Status != "" && rank(in.Status) < 0) {
			ignored++
			continue
		}
		st := have[in.Name]
		if st == nil {
			if count >= maxSteps {
				ignored++
				continue
			}
			count++
			next++
			st = &step{Seq: next, Key: in.Name, Name: in.Name, Status: "pending", Counts: map[string]int64{}}
			have[in.Name] = st
			created = append(created, in.Name)
		}
		st.report(in, now, &budget)
		if !slices.Contains(touched, in.Name) {
			touched = append(touched, in.Name)
		}
	}
	for _, n := range touched {
		st := have[n]
		if slices.Contains(created, n) {
			if _, err := tx.Exec(ctx, `INSERT INTO pipeline_step (run_id, seq, kera, key, name) VALUES ($1, $2, false, $3, $3)`, runID, st.Seq, n); err != nil {
				return 0, fmt.Errorf("insert step: %w", err)
			}
		}
		if err := saveStep(ctx, tx, runID, *st); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE pipeline_run SET active_at = $2 WHERE id = $1`, runID, now); err != nil {
		return 0, fmt.Errorf("touch run: %w", err)
	}
	return ignored, tx.Commit(ctx)
}

// beat records that the script (or an upload) is alive.
func (s *Service) beat(ctx context.Context, runID int64) error {
	if _, err := s.db.Exec(ctx, `UPDATE pipeline_run SET active_at = $2 WHERE id = $1 AND finished_at IS NULL`, runID, s.now()); err != nil {
		return fmt.Errorf("touch run: %w", err)
	}
	return nil
}

// editNode changes one Kera step. fn gets the step and the log budget.
func (s *Service) editNode(ctx context.Context, runID int64, kid string, fn func(st *step, budget *int)) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	st, err := scanStep(tx.QueryRow(ctx, `SELECT `+stepCols+`, log FROM pipeline_step WHERE run_id = $1 AND kera AND key = $2 FOR UPDATE`, runID, kid), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the collection was added to the pipeline after this run started
	}
	if err != nil {
		return fmt.Errorf("load collection step: %w", err)
	}
	budget, err := runBudget(ctx, tx, runID)
	if err != nil {
		return err
	}
	fn(&st, &budget)
	if err := saveStep(ctx, tx, runID, st); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// start marks a Kera step as running.
func (st *step) start(now time.Time) {
	if st.Status == "pending" {
		st.Status, st.StartedAt = "running", &now
	}
}

// markRejected fails a collection step whose uploads the server refused (a file
// over the size limit, bytes that do not match the sha256 listed): those files
// keep their old version and the run goes red, like a file that fails to index.
// ponytail: a name the script uploads again and gets through still counts as
// refused for this run (the next run is clean); the upgrade is to keep the
// refused names on the step and drop one on its next good PUT.
func (st *step) markRejected() {
	if n := st.Counts["rejected"]; n > 0 {
		st.Status = "failed"
		st.Error = strings.TrimSpace(st.Error + fmt.Sprintf(" %d files were refused on upload and keep their previous versions; the FAILED lines of the log say why.", n))
	}
}

// nodeLines logs one line per name (at most maxNodeLines, then a summary line).
func (st *step) nodeLines(prefix string, names []string, now time.Time, budget *int) {
	var sb strings.Builder
	for i, n := range names {
		if i == maxNodeLines {
			fmt.Fprintf(&sb, "%s... and %d more\n", prefix, len(names)-i)
			break
		}
		sb.WriteString(prefix + n + "\n")
	}
	st.addLog(sb.String(), now, budget)
}

// Settling ------------------------------------------------------------------------

// settle moves a run on: it refreshes the counts of the collection steps and,
// once the script is done and every file is indexed or failed, closes each
// collection (failed files recorded, stale files pruned behind the guard) and
// then the run. It is idempotent and safe to call from finish, the ticker and
// GET run. It reports whether the run is closed.
func (s *Service) settle(ctx context.Context, runID int64, now time.Time) (bool, error) {
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	r, err := s.loadRun(ctx, s.db, runID)
	if err != nil {
		return false, err
	}
	if r.FinishedAt != nil {
		return true, nil
	}
	p, err := s.pipelineBy(ctx, s.db, "id", r.PipelineID)
	if err != nil {
		return false, err
	}
	steps, err := s.loadSteps(ctx, s.db, runID, false)
	if err != nil {
		return false, err
	}
	scriptFailed := r.Error != ""
	for _, st := range steps {
		scriptFailed = scriptFailed || (!st.Kera && st.Status == "failed")
	}
	waiting := false
	progress := false
	for _, n := range steps {
		if !n.Kera || n.FinishedAt != nil || n.Status != "running" {
			continue
		}
		st, err := s.nodeState(ctx, r, n)
		if err != nil {
			return false, err
		}
		if st.Indexed+st.Failed > int(n.Counts["indexed"]+n.Counts["failed"]) {
			progress = true
		}
		counts := func(x *step) {
			x.Counts["waiting"], x.Counts["indexed"], x.Counts["failed"] = int64(st.Waiting), int64(st.Indexed), int64(st.Failed)
			if st.Failed > 0 {
				x.Error = failureText(st, int(x.Counts["listed"]))
			}
		}
		if !r.ClientDone || !st.Settled() {
			waiting = waiting || r.ClientDone
			if err := s.editNode(ctx, runID, n.Key, func(x *step, _ *int) { counts(x) }); err != nil {
				return false, err
			}
			continue
		}
		if err := s.closeNode(ctx, p, r, n, st, scriptFailed, now, counts); err != nil {
			return false, err
		}
	}
	if progress {
		if _, err := s.db.Exec(ctx, `UPDATE pipeline_run SET active_at = $2 WHERE id = $1 AND finished_at IS NULL`, runID, now); err != nil {
			return false, fmt.Errorf("record progress: %w", err)
		}
	}
	if !r.ClientDone || waiting {
		return false, nil
	}
	return true, s.closeRun(ctx, p, r, "", now)
}

// nodeState is SyncState for a collection step; a dry run has nothing to wait for
// and a collection that was deleted meanwhile counts as settled (closeNode fails it).
func (s *Service) nodeState(ctx context.Context, r *run, n step) (knowledge.SyncStatus, error) {
	if r.DryRun {
		return knowledge.SyncStatus{WaitingNames: []string{}, Failures: []knowledge.SyncFailure{}}, nil
	}
	st, err := s.k.SyncState(ctx, knowledge.SyncScope{Pipeline: r.PipelineID, Knowledge: n.Key, Run: r.ID})
	if errors.Is(err, knowledge.ErrNotFound) {
		return knowledge.SyncStatus{WaitingNames: []string{}, Failures: []knowledge.SyncFailure{}}, nil
	}
	return st, err
}

// failureText says which files of a collection could not be indexed.
func failureText(st knowledge.SyncStatus, listed int) string {
	if st.Failed == 0 {
		return ""
	}
	f := st.Failures[0]
	text := fmt.Sprintf("%d of %d files could not be indexed: %s: %s", st.Failed, max(listed, st.Failed), f.Name, strings.TrimRight(f.Error, "."))
	if st.Failed > 1 {
		text += fmt.Sprintf(" (and %d more)", st.Failed-1)
	}
	return capText(text+".", maxErrorLen) // the reason is the extractor's own text: mask it, cut it
}

// waitingText says what a stalled collection is still waiting for.
func waitingText(st knowledge.SyncStatus) string {
	switch {
	case st.Waiting > 0:
		return fmt.Sprintf("%d files still waiting to be indexed: %s", st.Waiting, strings.Join(st.WaitingNames, ", "))
	case st.Moving:
		return "the collection is changing its embedding model"
	}
	return ""
}

// closeNode finishes one collection step: records failed files, prunes the
// files the folder no longer holds (only when the listing was complete and the
// script reported no failure) and sets the step's final status.
func (s *Service) closeNode(ctx context.Context, p *Pipeline, r *run, n step, st knowledge.SyncStatus, scriptFailed bool, now time.Time, counts func(*step)) error {
	t := r.Targets[n.Key]
	var removed []string
	var refused, skipped string
	switch {
	case r.DryRun:
	case !t.Complete:
		skipped = "prune skipped: the listing was partial"
	case scriptFailed:
		skipped = "prune skipped: the script reported a failure"
	default:
		res, err := s.k.SyncPrune(ctx, knowledge.SyncPruneReq{
			SyncScope: knowledge.SyncScope{Pipeline: p.ID, Knowledge: n.Key, Run: r.ID}, Listed: t.Listed, Owned: t.Owned, Allow: p.AllowMassDelete})
		if errors.Is(err, knowledge.ErrNotFound) {
			skipped = "prune skipped: the collection no longer exists"
		} else if err != nil {
			return fmt.Errorf("prune %q: %w", n.Name, err)
		}
		removed, refused = res.Removed, res.Refused
		if refused != "" {
			n.Counts["refused"] = int64(len(res.Stale))
		}
	}
	return s.editNode(ctx, r.ID, n.Key, func(x *step, budget *int) {
		counts(x)
		x.Status = "ok"
		for _, f := range st.Failures {
			x.addLog("FAILED "+f.Name+": "+f.Error, now, budget)
		}
		if st.Failed > 0 {
			x.Status = "failed"
		}
		x.markRejected()
		x.nodeLines("- ", removed, now, budget)
		x.Counts["removed"] = int64(len(removed))
		if skipped != "" {
			x.addLog(skipped, now, budget)
		}
		if refused != "" {
			x.addLog("prune refused: "+refused, now, budget)
			x.Counts["refused"] = n.Counts["refused"]
			x.Error = strings.TrimSpace(x.Error + " Prune refused: " + refused + ".")
			if x.Status == "ok" {
				x.Status = "warning"
			}
		}
		x.Error = capText(x.Error, maxErrorLen)
		x.FinishedAt = &now
	})
}

// closeRun closes the run. forced is why it is closed early ("" after a normal
// finish): then a script step that is still running failed with that reason.
// Steps that never ran become skipped; the worst step decides the status.
func (s *Service) closeRun(ctx context.Context, p *Pipeline, r *run, forced string, now time.Time) error {
	steps, err := s.loadSteps(ctx, s.db, r.ID, false)
	if err != nil {
		return err
	}
	status := "ok"
	for _, st := range steps {
		to := st.Status
		switch {
		case to == "pending":
			to = "skipped"
		case to == "running" && !st.Kera && forced != "":
			to = "failed"
			st.Error = forced
		case to == "running":
			to = "ok"
		}
		if to != st.Status {
			if _, err := s.db.Exec(ctx, `UPDATE pipeline_step SET status = $3, error = $4, finished_at = coalesce(finished_at, $5) WHERE run_id = $1 AND seq = $2`,
				r.ID, st.Seq, to, st.Error, now); err != nil {
				return fmt.Errorf("close step: %w", err)
			}
		}
		if rank(to) > rank(status) {
			status = to
		}
	}
	steps, err = s.loadSteps(ctx, s.db, r.ID, false)
	if err != nil {
		return err
	}
	msg := composeError(forced, r.Error, steps)
	if msg != "" && rank(status) < rank("failed") && (forced != "" || r.Error != "") {
		status = "failed"
	}
	tag, err := s.db.Exec(ctx, `UPDATE pipeline_run SET status = $2, error = $3, finished_at = $4, active_at = $4, client_done = true WHERE id = $1 AND finished_at IS NULL`,
		r.ID, status, msg, now)
	if err != nil {
		return fmt.Errorf("close run: %w", err)
	}
	if tag.RowsAffected() == 1 && !r.DryRun {
		// The one-shot flags did their job. A good run clears them (a warning one
		// too: a flag left on would let the next run adopt or delete without an
		// admin asking). A failed run clears the ones it already used: files were
		// adopted or removed, and a later run must not do that again unasked.
		var adopted, removed int64
		for _, st := range steps {
			if st.Kera {
				adopted, removed = adopted+st.Counts["adopted"], removed+st.Counts["removed"]
			}
		}
		good := status == "ok" || status == "warning"
		if _, err := s.db.Exec(ctx, `UPDATE pipeline SET adopt = adopt AND NOT $2, allow_mass_delete = allow_mass_delete AND NOT $3
			WHERE id = $1 AND (adopt OR allow_mass_delete)`, p.ID, good || adopted > 0, good || removed > 0); err != nil {
			return fmt.Errorf("clear one-shot flags: %w", err)
		}
	}
	return nil
}

// composeError writes the run's error in plain words. forced (why the run was
// closed early) or the script's own finish error comes first; without either,
// the failed script steps are named. Failed or refused collections follow.
func composeError(forced, finish string, steps []step) string {
	var parts []string
	switch {
	case forced != "":
		parts = append(parts, forced)
	case finish != "":
		parts = append(parts, finish)
	default:
		for _, st := range steps {
			if !st.Kera && st.Status == "failed" {
				parts = append(parts, strings.TrimRight(fmt.Sprintf("Failed at step %s: %s", st.Name, st.Error), ": .")+".")
			}
		}
	}
	for _, st := range steps {
		if st.Kera && (st.Status == "failed" || st.Status == "warning") && st.Error != "" {
			parts = append(parts, st.Name+": "+st.Error)
		}
	}
	return capText(strings.Join(parts, " "), maxErrorLen)
}

// finish records that the script is done: a step still running ends (ok, or
// failed when the script reports an error) and the run settles.
func (s *Service) finish(ctx context.Context, runID int64, errText string) error {
	now := s.now()
	errText = capText(errText, maxErrorLen)
	to := "ok"
	if errText != "" {
		to = "failed"
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := scanRun(tx.QueryRow(ctx, `SELECT `+runCols+` FROM pipeline_run WHERE id = $1 FOR UPDATE`, runID))
	if err != nil {
		return err
	}
	if r.FinishedAt != nil || r.ClientDone {
		return nil // finishing twice is fine
	}
	if _, err := tx.Exec(ctx, `UPDATE pipeline_step SET status = $2, finished_at = $3, error = CASE WHEN $2 = 'failed' THEN $4 ELSE error END
		WHERE run_id = $1 AND NOT kera AND status = 'running'`, runID, to, now, errText); err != nil {
		return fmt.Errorf("end running steps: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE pipeline_run SET client_done = true, error = $2, active_at = $3 WHERE id = $1`, runID, errText, now); err != nil {
		return fmt.Errorf("mark script done: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_, err = s.settle(ctx, runID, now)
	return err
}

// force closes a run that cannot finish by itself (an idle or overlong script, no
// indexing progress, an admin's Close): collection steps that still wait fail with
// the names of the files, a script step that still runs fails with the reason,
// the run fails. Nothing is pruned.
func (s *Service) force(ctx context.Context, runID int64, reason string, now time.Time) error {
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	r, err := s.loadRun(ctx, s.db, runID)
	if err != nil || r.FinishedAt != nil {
		return err
	}
	p, err := s.pipelineBy(ctx, s.db, "id", r.PipelineID)
	if err != nil {
		return err
	}
	steps, err := s.loadSteps(ctx, s.db, runID, false)
	if err != nil {
		return err
	}
	for _, n := range steps {
		if !n.Kera || n.FinishedAt != nil || n.Status != "running" {
			continue
		}
		st, err := s.nodeState(ctx, r, n)
		if err != nil {
			return err
		}
		if err := s.editNode(ctx, runID, n.Key, func(x *step, budget *int) {
			x.Counts["waiting"], x.Counts["indexed"], x.Counts["failed"] = int64(st.Waiting), int64(st.Indexed), int64(st.Failed)
			x.Status = "ok"
			if st.Failed > 0 {
				x.Status, x.Error = "failed", failureText(st, int(x.Counts["listed"]))
			}
			if w := waitingText(st); w != "" {
				x.Status, x.Error = "failed", strings.TrimSpace(x.Error+" "+capText(w, maxErrorLen)+".")
				x.addLog(w, now, budget)
			}
			x.markRejected()
			x.Error = capText(x.Error, maxErrorLen)
			x.addLog("prune skipped: the run was closed early", now, budget)
			x.FinishedAt = &now
		}); err != nil {
			return err
		}
	}
	return s.closeRun(ctx, p, r, reason, now)
}

// utcClock formats a time for a message ("06:14 UTC").
func utcClock(t time.Time) string { return t.UTC().Format("15:04") + " UTC" }

// Ticker --------------------------------------------------------------------------

// ponytail: no shutdown hook, like the other background loops; the process exit ends it.
func (s *Service) loop() {
	for range time.Tick(time.Minute) {
		s.tickSafe(context.Background(), s.now())
	}
}

func (s *Service) tickSafe(ctx context.Context, now time.Time) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("pipeline tick panicked", "value", v)
		}
	}()
	s.tick(ctx, now)
}

// tick closes runs that cannot finish by themselves: a script that has been
// silent for idleLimit or running for scriptCap, and a run whose files make no
// indexing progress for idleLimit. It settles runs whose script is done.
func (s *Service) tick(ctx context.Context, now time.Time) {
	rows, err := s.db.Query(ctx, `SELECT id, client_done, started_at, active_at FROM pipeline_run WHERE finished_at IS NULL ORDER BY id`)
	if err != nil {
		slog.Error("pipeline: list open runs", "err", err)
		return
	}
	type open struct {
		id              int64
		done            bool
		started, active time.Time
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (open, error) {
		var o open
		return o, r.Scan(&o.id, &o.done, &o.started, &o.active)
	})
	if err != nil {
		slog.Error("pipeline: read open runs", "err", err)
		return
	}
	for _, o := range runs {
		var reason string
		switch {
		case !o.done && now.Sub(o.active) > idleLimit:
			reason = "The script stopped reporting at " + utcClock(o.active) + "."
		case !o.done && now.Sub(o.started) > scriptCap:
			reason = "The script was still running after 3 hours and was stopped at " + utcClock(now) + "."
		case o.done:
			closedNow, err := s.settle(ctx, o.id, now)
			if err != nil {
				slog.Error("pipeline: settle run", "run", o.id, "err", err)
				continue
			}
			if closedNow {
				continue
			}
			// settle moves active_at when files make progress; read it again
			var active time.Time
			if err := s.db.QueryRow(ctx, `SELECT active_at FROM pipeline_run WHERE id = $1`, o.id).Scan(&active); err != nil || now.Sub(active) <= idleLimit {
				continue
			}
			reason = "Indexing made no progress for 30 minutes (since " + utcClock(active) + ")."
		}
		if reason == "" {
			continue
		}
		if err := s.force(ctx, o.id, reason, now); err != nil {
			slog.Error("pipeline: close run", "run", o.id, "err", err)
		}
	}
}

// Lateness and health -------------------------------------------------------------

// isLate: a good run is expected every every minutes, with a slack of 10 percent
// (at least 15 minutes). last is the finish of the last good run that synced
// every target; created_at stands in until there is one.
func isLate(every int, last *time.Time, created, now time.Time) bool {
	if every <= 0 {
		return false
	}
	ref := created
	if last != nil {
		ref = *last
	}
	d := time.Duration(every) * time.Minute
	return now.Sub(ref) > d+max(15*time.Minute, d/10)
}

// lastGood is the finish of the newest ok or warning run that synced every
// target of the pipeline. A run that touched no collection, or only some, does
// not count, and neither does a dry run. nil when there is none.
func (s *Service) lastGood(ctx context.Context, p *Pipeline) (*time.Time, error) {
	var t *time.Time
	err := s.db.QueryRow(ctx, `SELECT max(finished_at) FROM pipeline_run r
		WHERE pipeline_id = $1 AND finished_at IS NOT NULL AND status IN ('ok', 'warning') AND NOT dry_run
		AND coalesce((SELECT bool_and(r.targets ? t) FROM unnest($2::text[]) AS t), true)`, p.ID, p.Targets).Scan(&t)
	if err != nil {
		return nil, fmt.Errorf("find the last good run: %w", err)
	}
	return utcPtr(t), nil
}

// health is the answer of the pipeline's health URL.
type health struct {
	Status string     `json:"status"` // ok | warning | never_run | failed | late
	Detail string     `json:"detail"`
	LastOK *time.Time `json:"last_ok_at"`
}

// healthOf says whether the pipeline is as healthy as it should be: the last
// closed run did not fail and a good run is not overdue. ok is false for 503.
func (s *Service) healthOf(ctx context.Context, p *Pipeline, now time.Time) (h health, ok bool, err error) {
	if h.LastOK, err = s.lastGood(ctx, p); err != nil {
		return h, false, err
	}
	var status, msg string
	err = s.db.QueryRow(ctx, `SELECT status, error FROM pipeline_run WHERE pipeline_id = $1 AND finished_at IS NOT NULL AND NOT dry_run ORDER BY id DESC LIMIT 1`, p.ID).Scan(&status, &msg)
	if errors.Is(err, pgx.ErrNoRows) {
		status, err = "never_run", nil
	}
	if err != nil {
		return h, false, fmt.Errorf("find the last run: %w", err)
	}
	h.Status, h.Detail = status, ""
	switch {
	case status == "failed":
		h.Detail = strings.TrimSpace("The last run failed. " + msg)
		return h, false, nil
	case isLate(p.EveryMinutes, h.LastOK, p.CreatedAt, now):
		ref := p.CreatedAt
		if h.LastOK != nil {
			ref = *h.LastOK
		}
		h.Status = "late"
		h.Detail = fmt.Sprintf("No good run for %s; one is expected every %s.", human(now.Sub(ref)), human(time.Duration(p.EveryMinutes)*time.Minute))
		return h, false, nil
	case status == "never_run":
		h.Detail = "No run has finished yet."
	case status == "warning":
		h.Detail = strings.TrimSpace("The last run finished with a warning. " + msg)
	}
	return h, true, nil
}

// human writes a duration as "5 min", "3 h" or "2 days".
func human(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m < 90:
		return strconv.Itoa(max(m, 1)) + " min"
	case m < 48*60:
		return strconv.Itoa((m+30)/60) + " h"
	}
	return strconv.Itoa((m+720)/1440) + " days"
}

// Tokens --------------------------------------------------------------------------

// newToken makes a pipeline token and the values to store.
func newToken() (token, hash, prefix string, err error) {
	token, hash, err = auth.NewKey(tokenPrefix)
	return token, hash, auth.KeyPrefix(token), err
}
