package pipeline

// Script notebook: an admin works on a script as cells. A session is a live shell or Python process on a runner,
// in a scratch folder, that keeps its state between cells; the cells and their output are rows the browser
// polls. Behind a session sits a dry-run pipeline run (trigger "test"), so `kera pipeline push` in a cell can
// plan but never upload or remove. Checks (syntax, ShellCheck) are one-shot jobs the runner answers.
// Everything here is admin-only on the browser side and runner-token on the runner side.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

const (
	sessionIdle   = 20 * time.Minute
	sessionCap    = 3 * time.Hour
	maxCellOutput = 200 << 10
	maxCellCode   = 256 << 10
	maxCells      = 300
	checkWait     = 20 * time.Second
)

// langOf is the interpreter a file runs under, by its extension; "" when it is neither.
func langOf(entry string) string {
	switch strings.ToLower(path.Ext(entry)) {
	case ".sh", ".bash":
		return "bash"
	case ".py":
		return "python"
	}
	return ""
}

func (s *Service) runnerOnline(ctx context.Context) bool {
	var n int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM pipeline_runner WHERE last_seen_at > $1`, s.now().Add(-runnerOnline)).Scan(&n)
	return n > 0
}

var errNoRunner = api.Errf(http.StatusConflict, "no_runner", "No runner is connected, so nothing can run or be checked. Create one under Admin > Pipelines > Runners.")

// Sessions (browser) ------------------------------------------------------------------------------------

type cellOut struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Code       string     `json:"code,omitempty"`
	Status     string     `json:"status"` // queued | running | ok | failed | stopped
	Output     string     `json:"output"`
	ExitCode   *int       `json:"exit_code"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type sessionOut struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"` // starting | ready | closing | closed
	Lang      string    `json:"lang"`
	Entry     string    `json:"entry"`
	Real      bool      `json:"real_workspace"`
	Error     string    `json:"error"`
	Run       int64     `json:"run"`
	CreatedAt time.Time `json:"created_at"`
	Cells     []cellOut `json:"cells"`
}

type sessionIn struct {
	Files map[string]string `json:"files"`
	Entry string            `json:"entry"`
	Real  bool              `json:"real_workspace"`
}

func (s *Service) aSessionStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	var in sessionIn
	if err := decode(w, r, &in, maxScriptBytes+(1<<20)); err != nil {
		api.Fail(w, r, err)
		return
	}
	sc := scriptIn{Files: in.Files, Entry: in.Entry}
	if err := cleanScript(&sc); err != nil {
		api.Fail(w, r, err)
		return
	}
	lang := langOf(in.Entry)
	if lang == "" {
		api.Fail(w, r, api.FieldErr("entry", "bad_entry", "The notebook runs .sh or .py files"))
		return
	}
	ctx := r.Context()
	if !s.runnerOnline(ctx) {
		api.Fail(w, r, errNoRunner)
		return
	}
	// one live session per pipeline: starting another closes the first
	rows, err := s.db.Query(ctx, `SELECT id FROM pipeline_session WHERE pipeline_id = $1 AND status IN ('starting', 'ready')`, p.ID)
	if err == nil {
		old, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, id := range old {
			s.closeSession(ctx, id, "replaced by a new session")
		}
	}
	rn, _, err := s.startRun(ctx, p, startIn{Trigger: "test", DryRun: true, internal: true, noQueue: true,
		Source: map[string]string{"command": "notebook: " + in.Entry, "client": "script notebook"}})
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	files, _ := json.Marshal(in.Files)
	var by *string
	if u := auth.FromContext(ctx); u != nil {
		by = &u.ID
	}
	id := ids.New()
	if _, err := s.db.Exec(ctx, `INSERT INTO pipeline_session (id, pipeline_id, user_id, run_id, lang, entry, files, real, created_at, active_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $9)`, id, p.ID, by, rn.ID, lang, in.Entry, string(files), in.Real, s.now()); err != nil {
		api.Fail(w, r, fmt.Errorf("create session: %w", err))
		return
	}
	s.writeSession(w, r, p, id, time.Time{}, http.StatusCreated)
}

func (s *Service) session(ctx context.Context, p *Pipeline, id string) (*sessionOut, error) {
	o := &sessionOut{Cells: []cellOut{}}
	var run *int64
	err := s.db.QueryRow(ctx, `SELECT id, status, lang, entry, real, error, run_id, created_at FROM pipeline_session WHERE id = $1 AND pipeline_id = $2`, id, p.ID).
		Scan(&o.ID, &o.Status, &o.Lang, &o.Entry, &o.Real, &o.Error, &run, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, api.Errf(http.StatusNotFound, "not_found", "Session not found")
	}
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	if run != nil {
		o.Run = *run
	}
	o.CreatedAt = o.CreatedAt.UTC()
	return o, nil
}

// writeSession answers with the session and its cells; with since, only cells changed after it (the browser polls).
func (s *Service) writeSession(w http.ResponseWriter, r *http.Request, p *Pipeline, id string, since time.Time, code int) {
	o, err := s.session(r.Context(), p, id)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, name, code, status, output, exit_code, started_at, finished_at, updated_at FROM pipeline_cell
		WHERE session_id = $1 AND updated_at > $2 ORDER BY id`, id, since)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("read cells: %w", err))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var c cellOut
		if err := rows.Scan(&c.ID, &c.Name, &c.Code, &c.Status, &c.Output, &c.ExitCode, &c.StartedAt, &c.FinishedAt, &c.UpdatedAt); err != nil {
			api.Fail(w, r, err)
			return
		}
		c.Code = "" // the browser has the code it sent
		c.UpdatedAt = c.UpdatedAt.UTC()
		o.Cells = append(o.Cells, c)
	}
	api.JSON(w, code, o)
}

func (s *Service) aSessionGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	var since time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			since = t
		}
	}
	s.writeSession(w, r, p, r.PathValue("session"), since, http.StatusOK)
}

type cellIn struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

// aCellRun queues a cell: the runner runs it in the session, after the cells queued before it.
func (s *Service) aCellRun(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	var in cellIn
	if err := decode(w, r, &in, maxCellCode+4096); err != nil {
		api.Fail(w, r, err)
		return
	}
	if len(in.Code) > maxCellCode || strings.ContainsRune(in.Code, 0) {
		api.Fail(w, r, api.FieldErr("code", "bad_code", "A cell is at most 256 KiB of text"))
		return
	}
	o, err := s.session(r.Context(), p, r.PathValue("session"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if o.Status != "starting" && o.Status != "ready" {
		api.Fail(w, r, api.Errf(http.StatusConflict, "session_closed", "This session has ended; start a new one"))
		return
	}
	var n int
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM pipeline_cell WHERE session_id = $1`, o.ID).Scan(&n)
	if n >= maxCells {
		api.Fail(w, r, api.Errf(http.StatusConflict, "too_many_cells", fmt.Sprintf("This session has run %d cells; start a new one", maxCells)))
		return
	}
	var id int64
	if err := s.db.QueryRow(r.Context(), `INSERT INTO pipeline_cell (session_id, name, code, created_at, updated_at) VALUES ($1, $2, $3, $4, $4) RETURNING id`,
		o.ID, capText(in.Name, 100), in.Code, s.now()).Scan(&id); err != nil {
		api.Fail(w, r, fmt.Errorf("queue cell: %w", err))
		return
	}
	_, _ = s.db.Exec(r.Context(), `UPDATE pipeline_session SET active_at = $2 WHERE id = $1`, o.ID, s.now())
	api.JSON(w, http.StatusCreated, map[string]any{"cell": id})
}

func (s *Service) aCellStop(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	if _, err := s.session(r.Context(), p, r.PathValue("session")); err != nil {
		api.Fail(w, r, err)
		return
	}
	now := s.now()
	// a queued cell is dropped at once; a running one is interrupted by the runner
	_, _ = s.db.Exec(r.Context(), `UPDATE pipeline_cell SET status = 'stopped', finished_at = $3, updated_at = $3 WHERE id = $2 AND session_id = $1 AND status = 'queued'`,
		r.PathValue("session"), r.PathValue("cell"), now)
	_, err := s.db.Exec(r.Context(), `UPDATE pipeline_cell SET stop = true, updated_at = $3 WHERE id = $2 AND session_id = $1 AND status = 'running'`,
		r.PathValue("session"), r.PathValue("cell"), now)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("stop cell: %w", err))
		return
	}
	s.writeSession(w, r, p, r.PathValue("session"), time.Time{}, http.StatusOK)
}

func (s *Service) aSessionClose(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	if _, err := s.session(r.Context(), p, r.PathValue("session")); err != nil {
		api.Fail(w, r, err)
		return
	}
	s.closeSession(r.Context(), r.PathValue("session"), "")
	s.writeSession(w, r, p, r.PathValue("session"), time.Time{}, http.StatusOK)
}

// closeSession asks the runner to end the session's process and closes the dry run behind it.
func (s *Service) closeSession(ctx context.Context, id, why string) {
	var run *int64
	err := s.db.QueryRow(ctx, `UPDATE pipeline_session SET status = CASE WHEN runner_id IS NULL THEN 'closed' ELSE 'closing' END,
		closed_at = CASE WHEN runner_id IS NULL THEN $2 ELSE closed_at END, error = CASE WHEN $3 = '' THEN error ELSE $3 END
		WHERE id = $1 AND status IN ('starting', 'ready') RETURNING run_id`, id, s.now(), why).Scan(&run)
	if err != nil {
		return
	}
	_, _ = s.db.Exec(ctx, `UPDATE pipeline_cell SET status = 'stopped', finished_at = $2, updated_at = $2 WHERE session_id = $1 AND status = 'queued'`, id, s.now())
	if run != nil {
		_ = s.finish(ctx, *run, "")
	}
}

// sessionTick closes sessions that have been idle or open too long, and finishes the closing of ones whose runner never answered.
func (s *Service) sessionTick(ctx context.Context, now time.Time) {
	rows, err := s.db.Query(ctx, `SELECT id FROM pipeline_session WHERE status IN ('starting', 'ready') AND (active_at < $1 OR created_at < $2)`, now.Add(-sessionIdle), now.Add(-sessionCap))
	if err == nil {
		list, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, id := range list {
			s.closeSession(ctx, id, "closed after being idle")
		}
	}
	_, _ = s.db.Exec(ctx, `UPDATE pipeline_session SET status = 'closed', closed_at = $2 WHERE status = 'closing' AND active_at < $1`, now.Add(-2*time.Minute), now)
	// a session whose runner never took it
	_, _ = s.db.Exec(ctx, `UPDATE pipeline_session SET status = 'closed', closed_at = $2, error = 'No runner started this session.' WHERE status = 'starting' AND runner_id IS NULL AND created_at < $1`, now.Add(-time.Minute), now)
}

// Checks (browser) ---------------------------------------------------------------------------------------

type problem struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity string `json:"severity"` // error | warning | info
	Message  string `json:"message"`
	Tool     string `json:"tool"`
	Code     string `json:"code,omitempty"`
}

type checkResult struct {
	Problems []problem `json:"problems"`
	Tools    []string  `json:"tools"` // what ran: bash -n, shellcheck, python compile
	Error    string    `json:"error,omitempty"`
}

// aCheck validates the editor's files on a runner and waits for the answer (a second or two).
func (s *Service) aCheck(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	if !s.runnerOnline(ctx) {
		api.Fail(w, r, errNoRunner)
		return
	}
	_, _ = s.db.Exec(ctx, `DELETE FROM pipeline_check WHERE created_at < $1`, s.now().Add(-10*time.Minute))
	files, _ := json.Marshal(in.Files)
	id := ids.New()
	if _, err := s.db.Exec(ctx, `INSERT INTO pipeline_check (id, pipeline_id, files, entry, created_at) VALUES ($1, $2, $3::jsonb, $4, $5)`, id, p.ID, string(files), in.Entry, s.now()); err != nil {
		api.Fail(w, r, fmt.Errorf("queue check: %w", err))
		return
	}
	deadline := time.Now().Add(checkWait)
	for time.Now().Before(deadline) {
		var raw []byte
		err := s.db.QueryRow(ctx, `SELECT result FROM pipeline_check WHERE id = $1 AND status = 'done'`, id).Scan(&raw)
		if err == nil {
			var res checkResult
			_ = json.Unmarshal(raw, &res)
			if res.Problems == nil {
				res.Problems = []problem{}
			}
			api.JSON(w, http.StatusOK, res)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
	api.Fail(w, r, api.Errf(http.StatusGatewayTimeout, "check_timeout", fmt.Sprintf("The runner did not answer in %d seconds", int(checkWait/time.Second))))
}

// Runner side ---------------------------------------------------------------------------------------------

type sessionWork struct {
	ID       string            `json:"id"`
	Pipeline string            `json:"pipeline"`
	Lang     string            `json:"lang"`
	Entry    string            `json:"entry"`
	Files    map[string]string `json:"files"`
	Env      map[string]string `json:"env"`
	Secrets  []string          `json:"secrets"`
	Real     bool              `json:"real_workspace"`
	Run      int64             `json:"run"`
	Token    string            `json:"token"`
}

type cellWork struct {
	Session string `json:"session"`
	Cell    int64  `json:"cell"`
	Code    string `json:"code"`
	Name    string `json:"name"`
}

type checkWork struct {
	ID    string            `json:"id"`
	Files map[string]string `json:"files"`
	Entry string            `json:"entry"`
}

// work is one thing for a runner to do besides a run: start or close a session, run or interrupt a cell, check files.
type work struct {
	Kind    string       `json:"kind"` // session | close | cell | stop | check
	Session *sessionWork `json:"session,omitempty"`
	Cell    *cellWork    `json:"cell,omitempty"`
	Check   *checkWork   `json:"check,omitempty"`
}

type workIn struct {
	Version string `json:"version"`
	Wait    int    `json:"wait"`
}

// hWork long-polls for interactive work, like hClaim does for runs.
func (s *Service) hWork(w http.ResponseWriter, r *http.Request) {
	var in workIn
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
		wk, err := s.nextWork(r.Context(), rn)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		if wk != nil {
			api.JSON(w, http.StatusOK, wk)
			return
		}
		if !time.Now().Before(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
	}
}

func (s *Service) nextWork(ctx context.Context, rn *runnerID) (*work, error) {
	now := s.now()
	// 1. close a session this runner holds
	var id string
	err := s.db.QueryRow(ctx, `UPDATE pipeline_session SET status = 'closed', closed_at = $2 WHERE id = (SELECT id FROM pipeline_session
		WHERE status = 'closing' AND runner_id = $1 ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id`, rn.ID, now).Scan(&id)
	if err == nil {
		return &work{Kind: "close", Session: &sessionWork{ID: id}}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("find sessions to close: %w", err)
	}
	// 2. interrupt a running cell
	var sid string
	var cid int64
	err = s.db.QueryRow(ctx, `UPDATE pipeline_cell SET stop_sent = true WHERE id = (SELECT c.id FROM pipeline_cell c JOIN pipeline_session s ON s.id = c.session_id
		WHERE c.stop AND NOT c.stop_sent AND c.status = 'running' AND s.runner_id = $1 ORDER BY c.id LIMIT 1 FOR UPDATE OF c SKIP LOCKED) RETURNING id, session_id`, rn.ID).Scan(&cid, &sid)
	if err == nil {
		return &work{Kind: "stop", Cell: &cellWork{Session: sid, Cell: cid}}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("find cells to stop: %w", err)
	}
	// 3. run the next queued cell of a ready session (one at a time per session)
	var code, name string
	err = s.db.QueryRow(ctx, `UPDATE pipeline_cell SET status = 'running', started_at = $2, updated_at = $2 WHERE id = (SELECT c.id FROM pipeline_cell c
		JOIN pipeline_session s ON s.id = c.session_id WHERE c.status = 'queued' AND s.runner_id = $1 AND s.status = 'ready'
		AND NOT EXISTS (SELECT 1 FROM pipeline_cell x WHERE x.session_id = c.session_id AND x.status = 'running')
		ORDER BY c.id LIMIT 1 FOR UPDATE OF c SKIP LOCKED) RETURNING id, session_id, code, name`, rn.ID, now).Scan(&cid, &sid, &code, &name)
	if err == nil {
		return &work{Kind: "cell", Cell: &cellWork{Session: sid, Cell: cid, Code: code, Name: name}}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("find cells to run: %w", err)
	}
	// 4. start a session nobody has taken
	var pid, lang, entry string
	var run int64
	var raw []byte
	var real bool
	err = s.db.QueryRow(ctx, `UPDATE pipeline_session SET runner_id = $1, active_at = $2 WHERE id = (SELECT id FROM pipeline_session
		WHERE status = 'starting' AND runner_id IS NULL ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id, pipeline_id, run_id, lang, entry, files, real`, rn.ID, now).
		Scan(&sid, &pid, &run, &lang, &entry, &raw, &real)
	if err == nil {
		return s.sessionJob(ctx, sid, pid, run, lang, entry, raw, real)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("find sessions to start: %w", err)
	}
	// 5. a check
	var chk checkWork
	err = s.db.QueryRow(ctx, `UPDATE pipeline_check SET status = 'claimed' WHERE id = (SELECT id FROM pipeline_check WHERE status = 'pending'
		ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id, files, entry`).Scan(&chk.ID, &raw, &chk.Entry)
	if err == nil {
		_ = json.Unmarshal(raw, &chk.Files)
		return &work{Kind: "check", Check: &chk}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("find checks: %w", err)
	}
	return nil, nil
}

func (s *Service) sessionJob(ctx context.Context, sid, pid string, run int64, lang, entry string, raw []byte, real bool) (*work, error) {
	p, err := s.pipelineBy(ctx, s.db, "id", pid)
	if err != nil {
		return nil, err
	}
	env, secrets, err := s.loadEnv(ctx, pid)
	if err != nil {
		return nil, err
	}
	sw := &sessionWork{ID: sid, Pipeline: p.Name, Lang: lang, Entry: entry, Env: env, Secrets: secrets, Real: real, Run: run, Token: s.jobToken(pid, run)}
	if err := json.Unmarshal(raw, &sw.Files); err != nil {
		return nil, err
	}
	return &work{Kind: "session", Session: sw}, nil
}

// ownSession returns the session when it belongs to the calling runner.
func (s *Service) ownSession(w http.ResponseWriter, r *http.Request, rn *runnerID) (id string, run int64, ok bool) {
	id = r.PathValue("session")
	var owner *string
	var rp *int64
	err := s.db.QueryRow(r.Context(), `SELECT runner_id, run_id FROM pipeline_session WHERE id = $1`, id).Scan(&owner, &rp)
	if err != nil || owner == nil || *owner != rn.ID {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "Session not found"))
		return "", 0, false
	}
	if rp != nil {
		run = *rp
	}
	return id, run, true
}

type readyIn struct {
	Error string `json:"error"`
}

// hSessionReady: the runner started the process (or could not).
func (s *Service) hSessionReady(w http.ResponseWriter, r *http.Request) {
	rn, ok := s.runnerAuth(w, r, "")
	if !ok {
		return
	}
	id, run, ok := s.ownSession(w, r, rn)
	if !ok {
		return
	}
	var in readyIn
	if err := decode(w, r, &in, 1<<12); err != nil {
		api.Fail(w, r, err)
		return
	}
	if in.Error != "" {
		_, _ = s.db.Exec(r.Context(), `UPDATE pipeline_session SET status = 'closed', closed_at = $2, error = $3 WHERE id = $1`, id, s.now(), capText(in.Error, 500))
		_ = s.finish(r.Context(), run, "")
	} else {
		_, _ = s.db.Exec(r.Context(), `UPDATE pipeline_session SET status = 'ready', active_at = $2 WHERE id = $1 AND status = 'starting'`, id, s.now())
	}
	w.WriteHeader(http.StatusNoContent)
}

type cellReport struct {
	Append   string `json:"append"`
	Status   string `json:"status"` // ok | failed | stopped when the cell ended
	ExitCode *int   `json:"exit_code"`
}

// hCellReport: output of a running cell, and its end.
func (s *Service) hCellReport(w http.ResponseWriter, r *http.Request) {
	rn, ok := s.runnerAuth(w, r, "")
	if !ok {
		return
	}
	id, run, ok := s.ownSession(w, r, rn)
	if !ok {
		return
	}
	var in cellReport
	if err := decode(w, r, &in, 1<<20); err != nil {
		api.Fail(w, r, err)
		return
	}
	now := s.now()
	end := in.Status == "ok" || in.Status == "failed" || in.Status == "stopped"
	var fin *time.Time
	if end {
		fin = &now
	}
	in.Append, _ = strings.CutSuffix(strings.ToValidUTF8(in.Append, "?"), "\x00")
	_, err := s.db.Exec(r.Context(), `UPDATE pipeline_cell SET
		output = CASE WHEN length(output) >= $4 THEN output WHEN length(output) + length($3) > $4 THEN left(output || $3, $4) || E'\n[output cut]' ELSE output || $3 END,
		status = CASE WHEN $5 <> '' THEN $5 ELSE status END, exit_code = COALESCE($6, exit_code), finished_at = COALESCE($7, finished_at), updated_at = $8
		WHERE id = $2 AND session_id = $1`, id, r.PathValue("cell"), in.Append, maxCellOutput, in.Status, in.ExitCode, fin, now)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("save cell output: %w", err))
		return
	}
	_, _ = s.db.Exec(r.Context(), `UPDATE pipeline_session SET active_at = $2 WHERE id = $1`, id, now)
	_, _ = s.db.Exec(r.Context(), `UPDATE pipeline_run SET active_at = $2 WHERE id = $1 AND finished_at IS NULL`, run, now)
	w.WriteHeader(http.StatusNoContent)
}

// hCheckResult: the runner's answer to a check.
func (s *Service) hCheckResult(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.runnerAuth(w, r, ""); !ok {
		return
	}
	var res checkResult
	if err := decode(w, r, &res, 1<<20); err != nil {
		api.Fail(w, r, err)
		return
	}
	if len(res.Problems) > 200 {
		res.Problems = res.Problems[:200]
	}
	b, _ := json.Marshal(res)
	if _, err := s.db.Exec(r.Context(), `UPDATE pipeline_check SET status = 'done', result = $2::jsonb WHERE id = $1`, r.PathValue("check"), string(b)); err != nil {
		api.Fail(w, r, fmt.Errorf("save check: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
