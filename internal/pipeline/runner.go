package pipeline

// Kera-run pipelines: the script, its environment and its timetable are stored in Kera, the minute
// ticker queues a run when the timetable says so, and a runner (`kera runner`, another container)
// claims the run, executes the script and reports through the same run API as a cron job would.
// Kera itself never executes anything: its image has no shell and no docker socket.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/audit"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/schedule"
	"github.com/cdmx-in/kera/internal/version"
)

const (
	runnerPrefix   = "kr-"
	jobPrefix      = "kj-"
	maxScriptFiles = 200
	maxFileBytes   = 1 << 20
	maxScriptBytes = 8 << 20
	keepVersions   = 50
	maxEnvValue    = 16 << 10
	maxEnvVars     = 100
	claimWaitMax   = 25 * time.Second
	runnerOnline   = 2 * time.Minute
)

var (
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	fileRe    = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/ -]{0,199}$`)
	// the runner sets these itself, so a stored variable may not shadow them; other KERA_ names (KERA_API_KEY, KERA_BASE) are ordinary
	reservedNames = map[string]bool{"KERA_URL": true, "KERA_PIPELINE": true, "KERA_PIPELINE_TOKEN": true, "KERA_RUN": true, "KERA_MARK": true,
		"KERA_SCRIPT_DIR": true, "KERA_TEST": true, "KERA_RUNNER_TOKEN": true, "KERA_RUNNER_TOKEN_FILE": true, "KERA_RUNNER_WORK": true}
	reservedEnv = func(n string) bool { return reservedNames[n] }
)

// Timetable -----------------------------------------------------------------------------

type scheduleOut struct {
	Scheduled bool       `json:"scheduled"`
	Frequency string     `json:"frequency"` // hourly | daily | weekdays | weekly
	AtMinute  int        `json:"at_minute"` // minutes after local midnight; for hourly the minute of the hour
	Weekday   int        `json:"weekday"`   // 0 = Sunday
	Timezone  string     `json:"timezone"`
	NextRunAt *time.Time `json:"next_run_at"`
}

func scheduleView(p *Pipeline) scheduleOut {
	o := scheduleOut{Scheduled: p.Scheduled, Frequency: p.Frequency, AtMinute: p.AtMinute, Weekday: p.Weekday, Timezone: p.Timezone}
	if p.NextRunAt != nil {
		t := p.NextRunAt.UTC()
		o.NextRunAt = &t
	}
	return o
}

// expectedEvery is how often a good run is expected for a timetable, in minutes: the Late flag of a
// Kera-run pipeline follows its schedule instead of a hand-typed number. Weekdays spans a weekend.
func expectedEvery(freq string) int {
	switch freq {
	case "hourly":
		return 60
	case "weekdays":
		return 3 * 1440
	case "weekly":
		return 7 * 1440
	}
	return 1440
}

type scheduleIn struct {
	RunMode   *string `json:"run_mode"`
	Scheduled *bool   `json:"scheduled"`
	Frequency *string `json:"frequency"`
	AtMinute  *int    `json:"at_minute"`
	Weekday   *int    `json:"weekday"`
	Timezone  *string `json:"timezone"`
}

// aSchedule changes how a pipeline is started: by outside scripts (external) or by Kera (kera) on a timetable.
func (s *Service) aSchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	var in scheduleIn
	if err := decode(w, r, &in, 1<<12); err != nil {
		api.Fail(w, r, err)
		return
	}
	mode, sched, freq := p.RunMode, p.Scheduled, p.Frequency
	at, wd, tz := p.AtMinute, p.Weekday, p.Timezone
	if in.RunMode != nil {
		mode = *in.RunMode
	}
	if in.Scheduled != nil {
		sched = *in.Scheduled
	}
	if in.Frequency != nil {
		freq = *in.Frequency
	}
	if in.AtMinute != nil {
		at = *in.AtMinute
	}
	if in.Weekday != nil {
		wd = *in.Weekday
	}
	if in.Timezone != nil {
		tz = *in.Timezone
	}
	switch {
	case mode != "external" && mode != "kera":
		api.Fail(w, r, api.FieldErr("run_mode", "bad_value", "Choose external or kera"))
	case !slices.Contains([]string{"hourly", "daily", "weekdays", "weekly"}, freq):
		api.Fail(w, r, api.FieldErr("frequency", "bad_value", "Choose hourly, daily, weekdays or weekly"))
	case at < 0 || at > 1439 || (freq == "hourly" && at > 59):
		api.Fail(w, r, api.FieldErr("at_minute", "bad_value", "That time is not valid"))
	case wd < 0 || wd > 6:
		api.Fail(w, r, api.FieldErr("weekday", "bad_value", "Choose a weekday"))
	default:
		if _, err := timeLocation(tz); err != nil {
			api.Fail(w, r, api.FieldErr("timezone", "bad_value", "That is not a time zone name, for example Asia/Kolkata"))
			return
		}
		if mode == "kera" && sched {
			if v, err := s.latestVersion(r.Context(), p.ID); err != nil || v == 0 {
				api.Fail(w, r, api.FieldErr("scheduled", "no_script", "Save a script first: there is nothing to run"))
				return
			}
		}
		var next *time.Time
		if mode == "kera" && sched {
			n := schedule.Next(s.now(), freq, at, wd, schedule.Location(tz))
			next = &n
		}
		every := p.EveryMinutes
		if mode == "kera" && sched {
			every = expectedEvery(freq)
		}
		if _, err := s.db.Exec(r.Context(), `UPDATE pipeline SET run_mode=$2, scheduled=$3, frequency=$4, at_minute=$5, weekday=$6, timezone=$7,
			next_run_at=$8, every_minutes=$9, updated_at=$10 WHERE id=$1`, p.ID, mode, sched, freq, at, wd, tz, next, every, s.now()); err != nil {
			api.Fail(w, r, fmt.Errorf("save schedule: %w", err))
			return
		}
		s.writePipeline(w, r, p.ID)
	}
}

func timeLocation(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, errors.New("empty time zone")
	}
	return time.LoadLocation(name)
}

func (s *Service) writePipeline(w http.ResponseWriter, r *http.Request, id string) {
	p, err := s.pipelineBy(r.Context(), s.db, "id", id)
	if err == nil {
		var o pipelineOut
		if o, err = s.pipelineView(r.Context(), p); err == nil {
			api.JSON(w, http.StatusOK, o)
			return
		}
	}
	api.Fail(w, r, err)
}

func (s *Service) latestVersion(ctx context.Context, pipelineID string) (int, error) {
	var v int
	err := s.db.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM pipeline_script WHERE pipeline_id = $1`, pipelineID).Scan(&v)
	return v, err
}

// fireDue queues a run for every Kera-run pipeline whose slot has come. The slot is claimed with a
// compare-and-set on next_run_at (as internal/schedule does), so two servers never queue it twice. A slot
// that comes while the previous run is still going is skipped; a slot missed while Kera was down runs once.
func (s *Service) fireDue(ctx context.Context, now time.Time) {
	rows, err := s.db.Query(ctx, `SELECT `+pipelineCols+` FROM pipeline WHERE run_mode = 'kera' AND scheduled AND enabled AND next_run_at <= $1`, now)
	if err != nil {
		slog.Error("pipeline: list due", "err", err)
		return
	}
	due, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Pipeline, error) { return scanPipeline(r) })
	if err != nil {
		slog.Error("pipeline: read due", "err", err)
		return
	}
	for _, p := range due {
		next := schedule.Next(now, p.Frequency, p.AtMinute, p.Weekday, schedule.Location(p.Timezone))
		tag, err := s.db.Exec(ctx, `UPDATE pipeline SET next_run_at = $3 WHERE id = $1 AND next_run_at = $2`, p.ID, p.NextRunAt, next)
		if err != nil || tag.RowsAffected() == 0 {
			continue // another process took the slot
		}
		if _, err := s.queue(ctx, p, "schedule"); err != nil {
			var held *activeError
			if errors.As(err, &held) {
				slog.Info("pipeline: slot skipped, the previous run is still going", "pipeline", p.Name, "run", held.Run)
			} else {
				slog.Error("pipeline: queue run", "pipeline", p.Name, "err", err)
			}
		}
	}
}

// queue opens a run that a runner will claim, pinned to the newest script version.
func (s *Service) queue(ctx context.Context, p *Pipeline, trigger string) (*run, error) {
	v, err := s.latestVersion(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if v == 0 {
		return nil, api.Errf(http.StatusConflict, "no_script", "This pipeline has no script yet")
	}
	rn, _, err := s.startRun(ctx, p, startIn{Trigger: trigger, internal: true, scriptVersion: v})
	return rn, err
}

// noRunnerText says why a queued run expired.
func (s *Service) noRunnerText(ctx context.Context) string {
	var seen *time.Time
	_ = s.db.QueryRow(ctx, `SELECT max(last_seen_at) FROM pipeline_runner`).Scan(&seen)
	if seen == nil {
		return "No runner picked this up: none has ever connected. Start the runner service (docker compose up -d runner) or create one under Admin > Pipelines > Runners."
	}
	return "No runner picked this up in 10 minutes (the last runner was seen at " + utcClock(*seen) + ")."
}

// aRunNow queues a run now, outside the timetable.
func (s *Service) aRunNow(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	if p.RunMode != "kera" {
		api.Fail(w, r, api.Errf(http.StatusConflict, "not_kera_run", "This pipeline is started from outside; switch it to Kera first"))
		return
	}
	rn, err := s.queue(r.Context(), p, "manual")
	var held *activeError
	if errors.As(err, &held) {
		api.JSON(w, http.StatusConflict, map[string]any{"detail": held.Error(), "code": "run_active", "run": held.Run})
		return
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	s.writeRun(w, r, p.Name, rn.ID)
}

// aCancel stops a run: a queued one is closed at once, a running one is asked to stop and the runner kills it.
func (s *Service) aCancel(w http.ResponseWriter, r *http.Request) {
	p, rn, ok := s.adminRun(w, r)
	if !ok {
		return
	}
	if rn.FinishedAt != nil {
		api.Fail(w, r, errRunClosed)
		return
	}
	who := "an admin"
	if u := auth.FromContext(r.Context()); u != nil && u.Name != "" {
		who = u.Name
	}
	if rn.Queued {
		if err := s.force(r.Context(), rn.ID, "Cancelled by "+who+" before a runner started it.", s.now()); err != nil {
			api.Fail(w, r, err)
			return
		}
	} else if _, err := s.db.Exec(r.Context(), `UPDATE pipeline_run SET cancel_requested = true WHERE id = $1`, rn.ID); err != nil {
		api.Fail(w, r, fmt.Errorf("request cancel: %w", err))
		return
	}
	s.writeRun(w, r, p.Name, rn.ID)
}

// Script versions -------------------------------------------------------------------------

type scriptOut struct {
	Version   int               `json:"version"`
	Files     map[string]string `json:"files"`
	Entry     string            `json:"entry"`
	Args      string            `json:"args"`
	Note      string            `json:"note"`
	CreatedBy string            `json:"created_by"`
	CreatedAt time.Time         `json:"created_at"`
}

type scriptListOut struct {
	Current  *scriptOut `json:"current"`
	Versions []struct {
		Version   int       `json:"version"`
		Note      string    `json:"note"`
		CreatedBy string    `json:"created_by"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"versions"`
}

func (s *Service) loadScript(ctx context.Context, pipelineID string, version int) (*scriptOut, error) {
	o := &scriptOut{}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT s.version, s.files, s.entry, s.args, s.note, coalesce(u.name, ''), s.created_at FROM pipeline_script s
		LEFT JOIN "user" u ON u.id = s.created_by WHERE s.pipeline_id = $1 AND s.version = $2`, pipelineID, version).
		Scan(&o.Version, &raw, &o.Entry, &o.Args, &o.Note, &o.CreatedBy, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, api.Errf(http.StatusNotFound, "not_found", "No such script version")
	}
	if err != nil {
		return nil, fmt.Errorf("read script: %w", err)
	}
	o.CreatedAt = o.CreatedAt.UTC()
	return o, json.Unmarshal(raw, &o.Files)
}

func (s *Service) aScript(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	out := scriptListOut{}
	rows, err := s.db.Query(r.Context(), `SELECT s.version, s.note, coalesce(u.name, ''), s.created_at FROM pipeline_script s
		LEFT JOIN "user" u ON u.id = s.created_by WHERE s.pipeline_id = $1 ORDER BY s.version DESC`, p.ID)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list versions: %w", err))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var v struct {
			Version   int       `json:"version"`
			Note      string    `json:"note"`
			CreatedBy string    `json:"created_by"`
			CreatedAt time.Time `json:"created_at"`
		}
		if err := rows.Scan(&v.Version, &v.Note, &v.CreatedBy, &v.CreatedAt); err != nil {
			api.Fail(w, r, err)
			return
		}
		v.CreatedAt = v.CreatedAt.UTC()
		out.Versions = append(out.Versions, v)
	}
	rows.Close()
	if len(out.Versions) > 0 {
		if out.Current, err = s.loadScript(r.Context(), p.ID, out.Versions[0].Version); err != nil {
			api.Fail(w, r, err)
			return
		}
	}
	api.JSON(w, http.StatusOK, out)
}

func (s *Service) aScriptVersion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "No such script version"))
		return
	}
	sc, err := s.loadScript(r.Context(), p.ID, v)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, sc)
}

type scriptIn struct {
	Files map[string]string `json:"files"`
	Entry string            `json:"entry"`
	Args  string            `json:"args"`
	Note  string            `json:"note"`
}

// cleanScript checks a script before it is stored: names are plain relative paths, sizes are bounded and the entry exists.
func cleanScript(in *scriptIn) error {
	if len(in.Files) == 0 {
		return api.FieldErr("files", "empty", "Add at least one file")
	}
	if len(in.Files) > maxScriptFiles {
		return api.FieldErr("files", "too_many", fmt.Sprintf("At most %d files", maxScriptFiles))
	}
	total := 0
	for name, body := range in.Files {
		if !fileRe.MatchString(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.HasSuffix(name, "/") {
			return api.FieldErr("files", "bad_name", fmt.Sprintf("%q is not a valid file name: use a relative path such as scripts/a.py", name))
		}
		if len(body) > maxFileBytes {
			return api.FieldErr("files", "too_large", fmt.Sprintf("%s is larger than 1 MiB", name))
		}
		if strings.ContainsRune(body, 0) {
			return api.FieldErr("files", "binary", name+" is not a text file")
		}
		total += len(body)
	}
	if total > maxScriptBytes {
		return api.FieldErr("files", "too_large", "The files together are larger than 8 MiB")
	}
	if _, ok := in.Files[in.Entry]; !ok {
		return api.FieldErr("entry", "no_entry", "Choose the file that is started; it must be one of the files")
	}
	if len(in.Args) > 500 || strings.ContainsAny(in.Args, "\x00\n") {
		return api.FieldErr("args", "bad_args", "Arguments: one line, at most 500 characters")
	}
	in.Note = capText(in.Note, 300)
	return nil
}

// aScriptSave stores a new version; the newest one is what the next run uses.
func (s *Service) aScriptSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	var in scriptIn
	if err := decode(w, r, &in, maxScriptBytes+(1<<20)); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := cleanScript(&in); err != nil {
		api.Fail(w, r, err)
		return
	}
	files, _ := json.Marshal(in.Files)
	var by *string
	if u := auth.FromContext(r.Context()); u != nil {
		by = &u.ID
	}
	var v int
	err := s.db.QueryRow(r.Context(), `INSERT INTO pipeline_script (pipeline_id, version, files, entry, args, note, created_by, created_at)
		SELECT $1, coalesce(max(version), 0) + 1, $2::jsonb, $3, $4, $5, $6, $7 FROM pipeline_script WHERE pipeline_id = $1 RETURNING version`,
		p.ID, string(files), in.Entry, strings.TrimSpace(in.Args), in.Note, by, s.now()).Scan(&v)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("save script: %w", err))
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM pipeline_script WHERE pipeline_id = $1 AND version <= $2::int - $3::int
		AND version NOT IN (SELECT script_version FROM pipeline_run WHERE pipeline_id = $1 AND script_version IS NOT NULL AND finished_at IS NULL)`, p.ID, v, keepVersions); err != nil {
		slog.Warn("pipeline: trim script versions", "pipeline", p.Name, "err", err)
	}
	slog.Info("pipeline script saved", "pipeline", p.Name, "version", v, "files", len(in.Files))
	sc, err := s.loadScript(r.Context(), p.ID, v)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusCreated, sc)
}

// Environment ---------------------------------------------------------------------------------

type envOut struct {
	Name      string    `json:"name"`
	Secret    bool      `json:"secret"`
	Value     *string   `json:"value"`                // only for a non-secret variable; a secret is never sent back
	SecretRef *string   `json:"secret_ref,omitempty"` // the stored secret this variable points at (value stays null)
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Service) aEnv(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT e.name, e.secret, e.value_enc, e.updated_at, sc.name FROM pipeline_env e
		LEFT JOIN secret sc ON sc.id = e.secret_id WHERE e.pipeline_id = $1 ORDER BY e.name`, p.ID)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list env: %w", err))
		return
	}
	defer rows.Close()
	out := []envOut{}
	for rows.Next() {
		var e envOut
		var enc []byte
		if err := rows.Scan(&e.Name, &e.Secret, &enc, &e.UpdatedAt, &e.SecretRef); err != nil {
			api.Fail(w, r, err)
			return
		}
		if !e.Secret && e.SecretRef == nil {
			v, err := s.a.Keys.DecryptString(enc)
			if err != nil {
				api.Fail(w, r, err)
				return
			}
			e.Value = &v
		}
		e.UpdatedAt = e.UpdatedAt.UTC()
		out = append(out, e)
	}
	api.JSON(w, http.StatusOK, map[string]any{"env": out})
}

type envIn struct {
	Name      string  `json:"name"`
	Value     *string `json:"value"`      // its own value, or ...
	SecretRef *string `json:"secret_ref"` // ... the name of a stored secret (internal/pipeline/secrets.go); not both
	Secret    *bool   `json:"secret"`
}

func (s *Service) aEnvSet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	var in envIn
	if err := decode(w, r, &in, maxEnvValue+1024); err != nil {
		api.Fail(w, r, err)
		return
	}
	switch {
	case !envNameRe.MatchString(in.Name):
		api.Fail(w, r, api.FieldErr("name", "bad_name", "Letters, digits and underscores, not starting with a digit"))
		return
	case reservedEnv(in.Name):
		api.Fail(w, r, api.FieldErr("name", "reserved", "The runner sets this one itself (KERA_URL, KERA_PIPELINE, KERA_PIPELINE_TOKEN, KERA_RUN, KERA_MARK, KERA_SCRIPT_DIR, KERA_TEST)"))
		return
	case in.SecretRef != nil && in.Value != nil:
		api.Fail(w, r, api.FieldErr("value", "value_and_ref", "Give a value or a stored secret, not both"))
		return
	}
	value := ""
	if in.Value != nil {
		value = *in.Value
	}
	if len(value) > maxEnvValue || strings.ContainsRune(value, 0) {
		api.Fail(w, r, api.FieldErr("value", "bad_value", "At most 16 KiB of text"))
		return
	}
	secret := in.Secret == nil || *in.Secret
	var secretID *string
	if in.SecretRef != nil {
		id, found, err := s.secretID(r.Context(), *in.SecretRef)
		switch {
		case err != nil:
			api.Fail(w, r, err)
			return
		case !found:
			api.Fail(w, r, api.FieldErr("secret_ref", "unknown_secret", "No stored secret has this name"))
			return
		}
		secretID, secret = &id, true // a stored secret is always masked in logs
	}
	var n int
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM pipeline_env WHERE pipeline_id = $1 AND name <> $2`, p.ID, in.Name).Scan(&n)
	if n >= maxEnvVars {
		api.Fail(w, r, api.FieldErr("name", "too_many", fmt.Sprintf("At most %d variables", maxEnvVars)))
		return
	}
	var enc []byte // NULL when the variable points at a stored secret
	if secretID == nil {
		var err error
		if enc, err = s.a.Keys.EncryptString(value); err == nil && enc == nil {
			enc, err = s.a.Keys.Encrypt([]byte{}) // an empty value is allowed; value_enc is only NULL for a reference
		}
		if err != nil {
			api.Fail(w, r, err)
			return
		}
	}
	if _, err := s.db.Exec(r.Context(), `INSERT INTO pipeline_env (pipeline_id, name, value_enc, secret_id, secret, updated_at) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (pipeline_id, name) DO UPDATE SET value_enc = $3, secret_id = $4, secret = $5, updated_at = $6`, p.ID, in.Name, enc, secretID, secret, s.now()); err != nil {
		api.Fail(w, r, fmt.Errorf("save env: %w", err))
		return
	}
	who := ""
	if u := auth.FromContext(r.Context()); u != nil {
		who = u.ID
	}
	ref := ""
	if in.SecretRef != nil {
		ref = *in.SecretRef
	}
	slog.Info("pipeline env set", "pipeline", p.Name, "name", in.Name, "secret", secret, "secret_ref", ref, "by", who) // never the value
	audit.Record(r.Context(), audit.Event{TargetLabel: p.Name, Meta: map[string]any{"variable": in.Name, "write_only": secret, "secret_ref": ref}})
	s.aEnv(w, r)
}

func (s *Service) aEnvDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM pipeline_env WHERE pipeline_id = $1 AND name = $2`, p.ID, r.PathValue("var")); err != nil {
		api.Fail(w, r, fmt.Errorf("delete env: %w", err))
		return
	}
	s.aEnv(w, r)
}

// Runners (admin) -------------------------------------------------------------------------------

type runnerOut struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	Version     string     `json:"version"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
	Online      bool       `json:"online"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *Service) runnerView(id, name, prefix, ver string, seen *time.Time, created time.Time) runnerOut {
	o := runnerOut{ID: id, Name: name, TokenPrefix: prefix, Version: ver, CreatedAt: created.UTC()}
	if seen != nil {
		t := seen.UTC()
		o.LastSeenAt, o.Online = &t, s.now().Sub(t) < runnerOnline
	}
	return o
}

func (s *Service) aRunners(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT id, name, token_prefix, version, last_seen_at, created_at FROM pipeline_runner ORDER BY name`)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list runners: %w", err))
		return
	}
	defer rows.Close()
	out := []runnerOut{}
	for rows.Next() {
		var id, name, prefix, ver string
		var seen *time.Time
		var created time.Time
		if err := rows.Scan(&id, &name, &prefix, &ver, &seen, &created); err != nil {
			api.Fail(w, r, err)
			return
		}
		out = append(out, s.runnerView(id, name, prefix, ver, seen, created))
	}
	api.JSON(w, http.StatusOK, map[string]any{"runners": out})
}

// newRunnerToken returns "kr-" plus 40 random characters, its hash and its display prefix.
func newRunnerToken() (token, hash, prefix string, err error) {
	b := make([]byte, 20)
	if _, err = rand.Read(b); err != nil {
		return
	}
	token = runnerPrefix + hex.EncodeToString(b)
	return token, auth.HashAPIKey(token), token[:10], nil
}

func (s *Service) aRunnerCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(w, r, &in, 1<<10); err != nil {
		api.Fail(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !slugRe.MatchString(in.Name) {
		api.Fail(w, r, api.FieldErr("name", "bad_name", "Use 1 to 40 lowercase letters, digits and dashes"))
		return
	}
	tok, hash, prefix, err := newRunnerToken()
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	id, now := ids.New(), s.now()
	if _, err := s.db.Exec(r.Context(), `INSERT INTO pipeline_runner (id, name, token_hash, token_prefix, created_at) VALUES ($1, $2, $3, $4, $5)`, id, in.Name, hash, prefix, now); err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "unique") {
			api.Fail(w, r, api.FieldErr("name", "name_taken", "A runner with this name exists"))
			return
		}
		api.Fail(w, r, fmt.Errorf("create runner: %w", err))
		return
	}
	audit.Record(r.Context(), audit.Event{TargetID: id, TargetLabel: in.Name})
	api.JSON(w, http.StatusCreated, map[string]any{"runner": s.runnerView(id, in.Name, prefix, "", nil, now), "token": tok})
}

func (s *Service) aRunnerRotate(w http.ResponseWriter, r *http.Request) {
	tok, hash, prefix, err := newRunnerToken()
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var name string
	err = s.db.QueryRow(r.Context(), `UPDATE pipeline_runner SET token_hash = $2, token_prefix = $3 WHERE id = $1 RETURNING name`, r.PathValue("id"), hash, prefix).Scan(&name)
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "Runner not found"))
		return
	}
	// the built-in runner reads its token from a file, so a rotation has to land there too or it would be refused until Kera restarts
	if f := s.a.Env.RunnerTokenFile; name == builtinRunner && f != "" {
		if err := writeRunnerToken(f, tok); err != nil {
			api.Fail(w, r, fmt.Errorf("write the token file: %w", err))
			return
		}
	}
	api.JSON(w, http.StatusOK, map[string]any{"token": tok, "token_prefix": prefix})
}

func (s *Service) aRunnerDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := s.db.Exec(r.Context(), `DELETE FROM pipeline_runner WHERE id = $1`, r.PathValue("id")); err != nil {
		api.Fail(w, r, fmt.Errorf("delete runner: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Runner routes (runner token) -----------------------------------------------------------------------

type runnerID struct{ ID, Name string }

func (s *Service) runnerAuth(w http.ResponseWriter, r *http.Request, ver string) (*runnerID, bool) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok = strings.TrimSpace(tok)
	rn := &runnerID{}
	if strings.HasPrefix(tok, runnerPrefix) {
		err := s.db.QueryRow(r.Context(), `UPDATE pipeline_runner SET last_seen_at = $2, version = CASE WHEN $3 = '' THEN version ELSE $3 END
			WHERE token_hash = $1 RETURNING id, name`, auth.HashAPIKey(tok), s.now(), ver).Scan(&rn.ID, &rn.Name)
		if err == nil {
			return rn, true
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			api.Fail(w, r, err)
			return nil, false
		}
	}
	api.Fail(w, r, api.Errf(http.StatusUnauthorized, "unauthenticated", "A valid runner token is required"))
	return nil, false
}

// job is what a runner receives for one run. The environment is decrypted here and travels only to the runner.
type job struct {
	Run      int64             `json:"run"`
	Pipeline string            `json:"pipeline"`
	Version  int               `json:"version"`
	Entry    string            `json:"entry"`
	Args     string            `json:"args"`
	Files    map[string]string `json:"files"`
	Env      map[string]string `json:"env"`
	Secrets  []string          `json:"secrets"` // names in Env whose values the runner masks in logs
	Token    string            `json:"token"`   // valid for this run only, until it closes
}

type claimIn struct {
	Version string `json:"version"`
	Host    string `json:"host"`
	Wait    int    `json:"wait"` // seconds to wait for work, at most 25
}

// hClaim hands the oldest queued run to the runner, waiting up to Wait seconds for one.
// ponytail: polls once a second inside the request; LISTEN/NOTIFY if many runners ever wait at once.
func (s *Service) hClaim(w http.ResponseWriter, r *http.Request) {
	var in claimIn
	if err := decode(w, r, &in, 1<<12); err != nil {
		api.Fail(w, r, err)
		return
	}
	rn, ok := s.runnerAuth(w, r, capText(in.Version, 60))
	if !ok {
		return
	}
	deadline := time.Now().Add(min(time.Duration(in.Wait)*time.Second, claimWaitMax))
	for {
		j, err := s.claim(r.Context(), rn, in)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		if j != nil {
			api.JSON(w, http.StatusOK, j)
			return
		}
		if !time.Now().Before(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Service) claim(ctx context.Context, rn *runnerID, in claimIn) (*job, error) {
	now := s.now()
	var runID int64
	var pid string
	var ver int
	err := s.db.QueryRow(ctx, `UPDATE pipeline_run SET queued = false, claimed_at = $1, active_at = $1, runner_id = $2
		WHERE id = (SELECT r.id FROM pipeline_run r JOIN pipeline p ON p.id = r.pipeline_id
			WHERE r.queued AND r.finished_at IS NULL AND p.enabled ORDER BY r.id LIMIT 1 FOR UPDATE OF r SKIP LOCKED)
		RETURNING id, pipeline_id, coalesce(script_version, 0)`, now, rn.ID).Scan(&runID, &pid, &ver)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim run: %w", err)
	}
	fail := func(why string, cause error) (*job, error) {
		slog.Error("pipeline: cannot build job", "run", runID, "why", why, "err", cause)
		_ = s.force(ctx, runID, "Kera could not start this run: "+why+".", now)
		return nil, nil
	}
	p, err := s.pipelineBy(ctx, s.db, "id", pid)
	if err != nil {
		return fail("the pipeline is gone", err)
	}
	sc, err := s.loadScript(ctx, pid, ver)
	if err != nil {
		return fail("script version "+strconv.Itoa(ver)+" no longer exists", err)
	}
	env, secrets, err := s.loadEnv(ctx, pid)
	if err != nil {
		return fail("its environment could not be read", err)
	}
	src, _ := json.Marshal(cleanSource(map[string]string{"host": in.Host, "user": rn.Name, "dir": "/work/" + p.Name,
		"command": strings.TrimSpace(sc.Entry + " " + sc.Args), "client": "kera runner " + version.Version}))
	_, _ = s.db.Exec(ctx, `UPDATE pipeline_run SET source = $2::jsonb WHERE id = $1`, runID, string(src))
	return &job{Run: runID, Pipeline: p.Name, Version: ver, Entry: sc.Entry, Args: sc.Args, Files: sc.Files, Env: env, Secrets: secrets, Token: s.jobToken(p.ID, runID)}, nil
}

// loadEnv is the decrypted environment of a pipeline for a job or a session. A variable that points at a stored
// secret gets the secret's current value and is always in secrets (masked in logs); the secret's last_used_at moves.
func (s *Service) loadEnv(ctx context.Context, pipelineID string) (env map[string]string, secrets []string, err error) {
	rows, err := s.db.Query(ctx, `SELECT e.name, coalesce(e.value_enc, sc.value_enc), e.secret, e.secret_id FROM pipeline_env e
		LEFT JOIN secret sc ON sc.id = e.secret_id WHERE e.pipeline_id = $1 ORDER BY e.name`, pipelineID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	env, secrets = map[string]string{}, []string{}
	var used []string
	for rows.Next() {
		var n string
		var enc []byte
		var secret bool
		var sid *string
		if err := rows.Scan(&n, &enc, &secret, &sid); err != nil {
			return nil, nil, err
		}
		if env[n], err = s.a.Keys.DecryptString(enc); err != nil {
			return nil, nil, err
		}
		if secret || sid != nil {
			secrets = append(secrets, n)
		}
		if sid != nil {
			used = append(used, *sid)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows.Close()
	if len(used) > 0 { // a failure here must not stop the job
		if _, err := s.db.Exec(ctx, `UPDATE secret SET last_used_at = $2 WHERE id = ANY($1)`, used, s.now()); err != nil {
			slog.Warn("pipeline: secret last_used_at", "pipeline", pipelineID, "err", err)
		}
	}
	return env, secrets, nil
}

// Job token -------------------------------------------------------------------------------------------

// jobToken is "kj-<run>-<hmac>": it authenticates the runner (and the script it starts) as this pipeline, for this
// run only, while the run is open. Nothing is stored; no plaintext pipeline token is needed.
func (s *Service) jobToken(pipelineID string, run int64) string {
	return fmt.Sprintf("%s%d-%s", jobPrefix, run, hex.EncodeToString(s.a.Keys.SignCall(jobMsg(pipelineID, run))))
}

func jobMsg(pipelineID string, run int64) []byte {
	return []byte("pipeline-job|" + pipelineID + "|" + strconv.FormatInt(run, 10))
}

// jobAuth accepts a job token for the pipeline in the path: the signature must match and the run must still be open.
func (s *Service) jobAuth(r *http.Request, tok string) (*Pipeline, bool) {
	body, _ := strings.CutPrefix(tok, jobPrefix)
	runStr, sig, ok := strings.Cut(body, "-")
	run, err := strconv.ParseInt(runStr, 10, 64)
	if !ok || err != nil {
		return nil, false
	}
	if want := r.PathValue("run"); want != "" && want != runStr {
		return nil, false // a job token is for one run only
	}
	p, err := s.pipelineBy(r.Context(), s.db, "name", r.PathValue("name"))
	if err != nil {
		return nil, false
	}
	got, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.a.Keys.SignCall(jobMsg(p.ID, run))) {
		return nil, false
	}
	var open bool
	if err := s.db.QueryRow(r.Context(), `SELECT finished_at IS NULL FROM pipeline_run WHERE id = $1 AND pipeline_id = $2`, run, p.ID).Scan(&open); err != nil || !open {
		return nil, false
	}
	return p, true
}
