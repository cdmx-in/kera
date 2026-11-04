package importowui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/settings"
)

// The nightly Open WebUI sync, for running both side by side before a
// cutover: when KERA_OWUI_SYNC_DSN is set, "kera serve" imports the source
// every day at KERA_OWUI_SYNC_AT (HH:MM in site.timezone, default 01:30) with
// --prune-missing and --since <start of the last successful full run - 1 h>,
// copying blobs from KERA_OWUI_SYNC_UPLOADS (default /owui/uploads).
// Admins can also start a run and list recent runs:
//
//	POST /api/v1/admin/sync/openwebui   202 {started:true}; 409 while one runs
//	GET  /api/v1/admin/sync/openwebui   {enabled, at, timezone, next_run, running, runs[]}

func init() { app.Register("owuisync", Mount) }

// lockKey is the session advisory lock that keeps imports one at a time,
// across the scheduler, the admin button and the CLI ("kera owui" in hex).
const lockKey int64 = 0x6b6572612d6f7775

// ErrBusy means another import holds the lock.
var ErrBusy = api.Errf(http.StatusConflict, "sync_running", "An Open WebUI import is already running")

// Lock takes the import lock on a dedicated connection; unlock releases it.
func Lock(ctx context.Context, db *pgxpool.Pool) (unlock func(), err error) {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("import lock: %w", err)
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&ok); err != nil {
		conn.Release()
		return nil, fmt.Errorf("import lock: %w", err)
	}
	if !ok {
		conn.Release()
		return nil, ErrBusy
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockKey); err != nil {
			_ = conn.Conn().Close(ctx) // ending the session drops the lock; the pool discards the closed conn
		}
		conn.Release()
	}, nil
}

// LastSince is the --since of the next incremental run: the start of the last
// successful full import (not a dry run, not --only, with blobs) minus an
// hour of slack for clocks and long transactions. Zero means import all.
func LastSince(ctx context.Context, db *pgxpool.Pool) (time.Time, error) {
	var t *time.Time
	err := db.QueryRow(ctx, `SELECT max(started_at) FROM migration_run WHERE finished_at IS NOT NULL AND error = ''
		AND NOT coalesce((flags->>'dry_run')::boolean, false) AND NOT coalesce((flags->>'no_files')::boolean, false)
		AND coalesce(flags->'only', 'null') = 'null'`).Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, err
	}
	return t.Add(-time.Hour), nil
}

// nextRun is the first hh:mm in loc strictly after now.
func nextRun(now time.Time, hh, mm int, loc *time.Location) time.Time {
	n := now.In(loc)
	t := time.Date(n.Year(), n.Month(), n.Day(), hh, mm, 0, 0, loc)
	if !t.After(n) {
		t = time.Date(n.Year(), n.Month(), n.Day()+1, hh, mm, 0, 0, loc)
	}
	return t
}

func parseAt(s string) (hh, mm int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("KERA_OWUI_SYNC_AT: want HH:MM, got %q", s)
	}
	return t.Hour(), t.Minute(), nil
}

type syncer struct {
	db       *pgxpool.Pool
	settings *settings.Store
	dsn      string
	uploads  string
	dataDir  string
	at       string
	hh, mm   int
	running  atomic.Bool
}

// Mount starts the scheduler when KERA_OWUI_SYNC_DSN is set and registers
// the admin routes either way (GET then reports enabled:false).
func Mount(a *app.App) error {
	s := &syncer{db: a.DB, settings: a.Settings, dataDir: a.Env.DataDir,
		dsn: os.Getenv("KERA_OWUI_SYNC_DSN"), uploads: getenv("KERA_OWUI_SYNC_UPLOADS", "/owui/uploads"),
		at: getenv("KERA_OWUI_SYNC_AT", "01:30")}
	var err error
	if s.hh, s.mm, err = parseAt(s.at); err != nil {
		return err
	}
	a.Server.Handle("GET /api/v1/admin/sync/openwebui", a.Auth.Admin(http.HandlerFunc(s.status)))
	a.Server.Handle("POST /api/v1/admin/sync/openwebui", a.Auth.Admin(http.HandlerFunc(s.trigger)))
	if s.dsn != "" {
		go s.loop()
	}
	return nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (s *syncer) location(ctx context.Context) *time.Location {
	var tz string
	_ = s.settings.Get(ctx, "site.timezone", &tz)
	if loc, err := time.LoadLocation(tz); err == nil && tz != "" {
		return loc
	}
	return time.UTC
}

// loop sleeps until the next run time (the timezone is re-read each day).
func (s *syncer) loop() {
	ctx := context.Background()
	for {
		next := nextRun(time.Now(), s.hh, s.mm, s.location(ctx))
		slog.Info("open webui sync scheduled", "at", next.Format(time.RFC3339))
		time.Sleep(time.Until(next))
		if err := s.start(ctx, "schedule"); err != nil {
			slog.Error("open webui sync not started", "err", err)
		}
	}
}

// start takes the lock and runs one import in the background.
func (s *syncer) start(ctx context.Context, trigger string) error {
	if !s.running.CompareAndSwap(false, true) {
		return ErrBusy
	}
	unlock, err := Lock(ctx, s.db)
	if err != nil {
		s.running.Store(false)
		return err
	}
	go func() {
		defer s.running.Store(false)
		defer unlock()
		if err := s.run(context.Background(), trigger); err != nil {
			slog.Error("open webui sync failed", "trigger", trigger, "err", err)
		}
	}()
	return nil
}

func (s *syncer) run(ctx context.Context, trigger string) error {
	since, err := LastSince(ctx, s.db)
	if err != nil {
		return fmt.Errorf("last sync: %w", err)
	}
	opt := Options{UploadsDir: s.uploads, DataDir: s.dataDir, PruneMissing: true, Since: since, Trigger: trigger, Log: slog.Default()}
	src, err := openSource(ctx, s.dsn)
	if err != nil {
		// Record the failure so the admin panel shows it; Run records everything else.
		flags, _ := json.Marshal(map[string]any{"prune_missing": true, "since": since, "uploads_dir": s.uploads, "trigger": trigger})
		_, _ = s.db.Exec(ctx, `INSERT INTO migration_run (source, flags, finished_at, error) VALUES ($1, $2, now(), $3)`,
			SourceName(s.dsn), flags, err.Error())
		return err
	}
	defer src.Close()
	_, err = Run(ctx, src, s.db, SourceName(s.dsn), opt)
	return err
}

func (s *syncer) trigger(w http.ResponseWriter, r *http.Request) {
	if s.dsn == "" {
		api.Fail(w, r, api.Errf(http.StatusServiceUnavailable, "sync_not_configured", "Set KERA_OWUI_SYNC_DSN to enable the Open WebUI sync"))
		return
	}
	if err := s.start(r.Context(), "admin"); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusAccepted, map[string]any{"started": true})
}

type runRow struct {
	ID         int64            `json:"id"`
	Source     string           `json:"source"`
	Trigger    string           `json:"trigger"`
	Since      *time.Time       `json:"since,omitempty"`
	StartedAt  int64            `json:"started_at"`
	FinishedAt *int64           `json:"finished_at"`
	Error      string           `json:"error"`
	Totals     map[string]int   `json:"totals"`
	Stats      map[string]*Stat `json:"stats,omitempty"`
}

func (s *syncer) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `SELECT id, source, flags, counts, extract(epoch FROM started_at)::bigint,
		extract(epoch FROM finished_at)::bigint, error FROM migration_run ORDER BY id DESC LIMIT 10`)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	runs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (runRow, error) {
		var x runRow
		var flags struct {
			Trigger string     `json:"trigger"`
			Since   *time.Time `json:"since"`
		}
		var flagsRaw, countsRaw []byte
		if err := row.Scan(&x.ID, &x.Source, &flagsRaw, &countsRaw, &x.StartedAt, &x.FinishedAt, &x.Error); err != nil {
			return x, err
		}
		_ = json.Unmarshal(flagsRaw, &flags)
		x.Trigger = flags.Trigger
		if flags.Since != nil && !flags.Since.IsZero() {
			x.Since = flags.Since
		}
		var rep Report
		_ = json.Unmarshal(countsRaw, &rep)
		x.Stats, x.Totals = rep.Stats, map[string]int{"inserted": 0, "existing": 0, "skipped": 0, "pruned": 0}
		for _, st := range rep.Stats {
			x.Totals["inserted"] += st.Inserted
			x.Totals["existing"] += st.Existing
			x.Totals["skipped"] += st.SkippedTotal()
			x.Totals["pruned"] += st.Pruned
		}
		return x, nil
	})
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list migration runs: %w", err))
		return
	}
	out := map[string]any{"enabled": s.dsn != "", "at": s.at, "running": s.running.Load(), "runs": runs}
	if runs == nil {
		out["runs"] = []runRow{}
	}
	loc := s.location(ctx)
	out["timezone"] = loc.String()
	if s.dsn != "" {
		out["next_run"] = nextRun(time.Now(), s.hh, s.mm, loc).Unix()
	}
	api.JSON(w, http.StatusOK, out)
}
