// Package connectors runs scheduled integrations with outside systems (for
// example the Google Workspace directory sync in ./gworkspace). A provider
// registers a Connector from init(); this package schedules it, keeps runs
// one at a time and records each run in connector_run.
//
// Settings per connector: connectors.<name>.enabled and
// connectors.<name>.interval_minutes; the scheduler re-reads them every
// minute, so changes need no restart. Admin routes:
//
//	GET  /api/v1/admin/connectors                       connectors + last runs
//	POST /api/v1/admin/connectors/{name}/run[?dry_run=1] 202; 409 while it runs
package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
)

// Change is one thing a run did (or, in a dry run, would do).
type Change struct {
	Action string `json:"action"` // create, update, disable, enable, delete, join, leave, ...
	Target string `json:"target"` // usually an email
	Detail string `json:"detail,omitempty"`
}

// Report is what a run returns and what connector_run.report stores.
type Report struct {
	Counts   map[string]int `json:"counts"`
	Changes  []Change       `json:"changes"`
	Warnings []string       `json:"warnings"`
}

// NewReport returns an empty report.
func NewReport() *Report {
	return &Report{Counts: map[string]int{}, Changes: []Change{}, Warnings: []string{}}
}

// Add records a change and counts it under its action.
func (r *Report) Add(action, target, detail string) {
	r.Changes = append(r.Changes, Change{action, target, detail})
	r.Counts[action]++
}

// Warn records something an admin should look at; the run still succeeds.
func (r *Report) Warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Connector is one integration. Run must make no changes when dryRun is set
// and should return a partial report along with any error.
type Connector struct {
	Name  string // settings prefix connectors.<Name>
	Title string
	Run   func(ctx context.Context, a *app.App, dryRun bool) (*Report, error)
}

var (
	registry []Connector
	running  sync.Map // name -> struct{}; one run per connector in this process
)

// Register adds a connector. Call it from init().
func Register(c Connector) { registry = append(registry, c) }

func find(name string) (Connector, bool) {
	i := slices.IndexFunc(registry, func(c Connector) bool { return c.Name == name })
	if i < 0 {
		return Connector{}, false
	}
	return registry[i], true
}

// ErrBusy means a run of the same connector is in progress (here or in another process).
var ErrBusy = api.Errf(http.StatusConflict, "sync_running", "This integration is already running")

func init() { app.Register("connectors", Mount) }

// Mount registers the admin routes and starts the scheduler.
func Mount(a *app.App) error {
	a.Server.Handle("GET /api/v1/admin/connectors", a.Auth.Admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := status(r.Context(), a)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		api.JSON(w, http.StatusOK, out)
	})))
	a.Server.Handle("POST /api/v1/admin/connectors/{name}/run", a.Auth.Admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := find(r.PathValue("name"))
		if !ok {
			api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "Unknown integration"))
			return
		}
		if err := start(a, c, "admin", r.URL.Query().Get("dry_run") == "1"); err != nil {
			api.Fail(w, r, err)
			return
		}
		api.JSON(w, http.StatusAccepted, map[string]any{"started": true})
	})))
	go schedule(a)
	return nil
}

// schedule is the routine cron: every minute it starts each enabled connector
// whose last real run began at least interval_minutes ago.
// ponytail: no shutdown hook, like the other background loops; the process exit ends it.
func schedule(a *app.App) {
	for range time.Tick(time.Minute) {
		ctx := context.Background()
		// A run is capped at 30 minutes (start); one still open after 35 belongs
		// to a process that died, and would otherwise show as running forever.
		if _, err := a.DB.Exec(ctx, `UPDATE connector_run SET finished_at = now(), error = 'interrupted: the server stopped during the run'
			WHERE finished_at IS NULL AND started_at < now() - interval '35 minutes'`); err != nil {
			slog.Error("connector schedule: close orphaned runs", "err", err)
		}
		for _, c := range registry {
			next, err := nextRun(ctx, a, c.Name)
			if err != nil {
				slog.Error("connector schedule", "connector", c.Name, "err", err)
				continue
			}
			if next == nil || time.Now().Before(*next) {
				continue
			}
			if err := start(a, c, "schedule", false); err != nil && !errors.Is(err, ErrBusy) {
				slog.Error("connector not started", "connector", c.Name, "err", err)
			}
		}
	}
}

// nextRun is when the connector is due, or nil while it is disabled. Dry runs
// do not count; failed runs do, so a broken connector retries once per interval.
func nextRun(ctx context.Context, a *app.App, name string) (*time.Time, error) {
	if !a.Settings.Bool(ctx, "connectors."+name+".enabled") {
		return nil, nil
	}
	minutes := 60
	_ = a.Settings.Get(ctx, "connectors."+name+".interval_minutes", &minutes)
	minutes = max(minutes, 5)
	var last *time.Time
	err := a.DB.QueryRow(ctx, `SELECT max(started_at) FROM connector_run WHERE connector = $1 AND NOT dry_run`, name).Scan(&last)
	if err != nil {
		return nil, fmt.Errorf("last run: %w", err)
	}
	t := time.Now()
	if last != nil {
		t = last.Add(time.Duration(minutes) * time.Minute)
	}
	return &t, nil
}

// start runs c in the background; ErrBusy when it is already running here.
func start(a *app.App, c Connector, trigger string, dryRun bool) error {
	if _, busy := running.LoadOrStore(c.Name, struct{}{}); busy {
		return ErrBusy
	}
	go func() {
		defer running.Delete(c.Name)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if _, err := run(ctx, a, c, trigger, dryRun); err != nil {
			slog.Error("connector run failed", "connector", c.Name, "trigger", trigger, "dry_run", dryRun, "err", err)
		}
	}()
	return nil
}

// Run runs c now under its lock and records the run.
func Run(ctx context.Context, a *app.App, c Connector, trigger string, dryRun bool) (*Report, error) {
	if _, busy := running.LoadOrStore(c.Name, struct{}{}); busy {
		return nil, ErrBusy
	}
	defer running.Delete(c.Name)
	return run(ctx, a, c, trigger, dryRun)
}

func run(ctx context.Context, a *app.App, c Connector, trigger string, dryRun bool) (*Report, error) {
	// A session advisory lock keeps the scheduler, the admin button and the
	// CLI (another process) from running the same connector at once.
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("connector lock: %w", err)
	}
	defer conn.Release()
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('kera.connector'), hashtext($1))`, c.Name).Scan(&ok); err != nil {
		return nil, fmt.Errorf("connector lock: %w", err)
	}
	if !ok {
		return nil, ErrBusy
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('kera.connector'), hashtext($1))`, c.Name); err != nil {
			_ = conn.Conn().Close(ctx) // ending the session drops the lock
		}
	}()

	var id int64
	if err := a.DB.QueryRow(ctx, `INSERT INTO connector_run (connector, trigger, dry_run) VALUES ($1, $2, $3) RETURNING id`,
		c.Name, trigger, dryRun).Scan(&id); err != nil {
		return nil, fmt.Errorf("record run: %w", err)
	}
	rep, runErr := runNoPanic(ctx, a, c, dryRun)
	if rep == nil {
		rep = NewReport()
	}
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
	}
	raw, _ := json.Marshal(rep)
	// A fresh context, so a run that timed out is still recorded as finished.
	done, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.DB.Exec(done, `UPDATE connector_run SET finished_at = now(), error = $2, report = $3 WHERE id = $1`, id, msg, raw); err != nil {
		slog.Error("record connector run", "id", id, "err", err)
	}
	slog.Info("connector run", "connector", c.Name, "trigger", trigger, "dry_run", dryRun, "counts", rep.Counts, "err", msg)
	return rep, runErr
}

// runNoPanic is c.Run with a panic turned into the run's error, so the run row
// is still finished and the process (the whole server) survives.
func runNoPanic(ctx context.Context, a *app.App, c Connector, dryRun bool) (rep *Report, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("connector crashed: %v", v)
		}
	}()
	return c.Run(ctx, a, dryRun)
}

type connectorOut struct {
	Name            string     `json:"name"`
	Title           string     `json:"title"`
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	Running         bool       `json:"running"`
	NextRun         *time.Time `json:"next_run"`
}

type runOut struct {
	ID         int64           `json:"id"`
	Connector  string          `json:"connector"`
	Trigger    string          `json:"trigger"`
	DryRun     bool            `json:"dry_run"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	Error      string          `json:"error"`
	Report     json.RawMessage `json:"report"`
}

func status(ctx context.Context, a *app.App) (map[string]any, error) {
	cs := []connectorOut{}
	for _, c := range registry {
		o := connectorOut{Name: c.Name, Title: c.Title, IntervalMinutes: 60}
		o.Enabled = a.Settings.Bool(ctx, "connectors."+c.Name+".enabled")
		_ = a.Settings.Get(ctx, "connectors."+c.Name+".interval_minutes", &o.IntervalMinutes)
		_, o.Running = running.Load(c.Name)
		var err error
		if o.NextRun, err = nextRun(ctx, a, c.Name); err != nil {
			return nil, err
		}
		cs = append(cs, o)
	}
	rows, err := a.DB.Query(ctx, `SELECT id, connector, trigger, dry_run, started_at, finished_at, error, report
		FROM connector_run ORDER BY id DESC LIMIT 20`)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[runOut])
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	if runs == nil {
		runs = []runOut{}
	}
	return map[string]any{"connectors": cs, "runs": runs}, nil
}

// CLIUsage is the help text for Command.
const CLIUsage = `  kera connectors list    list integrations
  kera connectors run NAME [-dry-run]
                          run an integration now and print its report
`

// Command implements "kera connectors". a needs DB, Keys and Settings only.
func Command(ctx context.Context, a *app.App, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: kera connectors list|run")
	}
	switch args[0] {
	case "list":
		for _, c := range registry {
			fmt.Fprintf(out, "%-20s %s (enabled: %v)\n", c.Name, c.Title, a.Settings.Bool(ctx, "connectors."+c.Name+".enabled"))
		}
		return nil
	case "run":
		fs := flag.NewFlagSet("connectors run", flag.ContinueOnError)
		dry := fs.Bool("dry-run", false, "report what would change without changing anything")
		if len(args) < 2 {
			return errors.New("usage: kera connectors run NAME [-dry-run]")
		}
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		c, ok := find(args[1])
		if !ok {
			return fmt.Errorf("unknown integration %q", args[1])
		}
		rep, err := Run(ctx, a, c, "cli", *dry)
		if rep != nil {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			_ = enc.Encode(rep)
		}
		return err
	}
	return fmt.Errorf("unknown connectors command %q", args[0])
}
