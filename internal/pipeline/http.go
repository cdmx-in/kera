package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/knowledge"
	"github.com/cdmx-in/kera/internal/version"
)

func (s *Service) routes() {
	// Token routes: the pipeline's own token is the only credential (tokenPipeline),
	// so they are mounted without the user middleware, like the run callbacks of
	// the chat engine. No user or admin credential works on them.
	for p, f := range map[string]http.HandlerFunc{
		"POST /api/v1/pipelines/{name}/runs":              s.hStart,
		"POST /api/v1/pipelines/{name}/runs/{run}/steps":  s.hSteps,
		"POST /api/v1/pipelines/{name}/runs/{run}/sync":   s.hSync,
		"PUT /api/v1/pipelines/{name}/runs/{run}/files":   s.hPut,
		"POST /api/v1/pipelines/{name}/runs/{run}/finish": s.hFinish,
		"GET /api/v1/pipelines/{name}/runs/{run}":         s.hRun,
		"GET /api/v1/pipelines/{name}/health":             s.hHealth,
	} {
		s.a.Server.HandleFunc(p, f)
	}
	for p, f := range map[string]http.HandlerFunc{
		"GET /api/v1/admin/pipelines":                               s.aList,
		"POST /api/v1/admin/pipelines":                              s.aCreate,
		"GET /api/v1/admin/pipelines/{name}":                        s.aGet,
		"PUT /api/v1/admin/pipelines/{name}":                        s.aUpdate,
		"DELETE /api/v1/admin/pipelines/{name}":                     s.aDelete,
		"POST /api/v1/admin/pipelines/{name}/token":                 s.aRotate,
		"POST /api/v1/admin/pipelines/{name}/once":                  s.aOnce,
		"GET /api/v1/admin/pipelines/{name}/runs":                   s.aRuns,
		"GET /api/v1/admin/pipelines/{name}/runs/{run}":             s.aRun,
		"GET /api/v1/admin/pipelines/{name}/runs/{run}/steps/{seq}": s.aStep,
		"POST /api/v1/admin/pipelines/{name}/runs/{run}/close":      s.aClose,
	} {
		s.a.Server.Handle(p, s.a.Auth.Admin(f))
	}
}

// decode reads a JSON body of at most limit bytes; an empty body is {}.
func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	err := json.NewDecoder(r.Body).Decode(v)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil || errors.Is(err, io.EOF):
		return nil
	case errors.As(err, &tooBig):
		return api.Errf(http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("the request body is larger than %d bytes", limit))
	}
	return api.Errf(http.StatusBadRequest, "bad_json", "invalid JSON body: "+err.Error())
}

// Token routes --------------------------------------------------------------------

// tokenPipeline authenticates a token route. The bearer must be a "kp-" token
// whose pipeline is the {name} in the path (a token mix-up is a 401, not a
// write to the wrong pipeline); a disabled pipeline answers 403.
func (s *Service) tokenPipeline(w http.ResponseWriter, r *http.Request) (*Pipeline, bool) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok = strings.TrimSpace(tok)
	deny := func() (*Pipeline, bool) {
		api.Fail(w, r, api.Errf(http.StatusUnauthorized, "unauthenticated", "A valid pipeline token for this pipeline is required"))
		return nil, false
	}
	if !strings.HasPrefix(tok, tokenPrefix) {
		return deny()
	}
	p, err := s.pipelineBy(r.Context(), s.db, "token_hash", auth.HashAPIKey(tok))
	if errors.Is(err, errNoPipeline) || (err == nil && p.Name != r.PathValue("name")) {
		return deny()
	}
	if err != nil {
		api.Fail(w, r, err)
		return nil, false
	}
	if !p.Enabled {
		api.Fail(w, r, api.Errf(http.StatusForbidden, "pipeline_disabled", "This pipeline is switched off"))
		return nil, false
	}
	return p, true
}

// tokenRun loads the run in the path for the authenticated pipeline. With open
// the run must still accept reports: not closed and the script not finished.
func (s *Service) tokenRun(w http.ResponseWriter, r *http.Request, p *Pipeline, open bool) (*run, bool) {
	id, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
	if err != nil {
		api.Fail(w, r, errNoRun)
		return nil, false
	}
	rn, err := s.loadRun(r.Context(), s.db, id)
	if err == nil && rn.PipelineID != p.ID {
		err = errNoRun
	}
	if err == nil && open && (rn.FinishedAt != nil || rn.ClientDone) {
		err = errRunClosed
	}
	if err != nil {
		api.Fail(w, r, err)
		return nil, false
	}
	return rn, true
}

// targetOf resolves a collection the way a token may: only through the
// pipeline's own targets, by id, or by name when exactly one target has it.
// Anything else is not_a_target; the API never creates a collection.
func (s *Service) targetOf(ctx context.Context, p *Pipeline, ref string) (collection, error) {
	cols, err := s.collectionsOf(ctx, s.db, p.Targets)
	if err != nil {
		return collection{}, err
	}
	var byName []collection
	for _, c := range cols {
		if c.ID == ref {
			return c, nil
		}
		if c.Name == ref {
			byName = append(byName, c)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return collection{}, api.Errf(http.StatusForbidden, "not_a_target", fmt.Sprintf("%q is not a collection this pipeline writes to", ref))
	}
	return collection{}, api.Errf(http.StatusConflict, "ambiguous_collection", fmt.Sprintf("two collections of this pipeline are named %q; use the id", ref))
}

func (s *Service) runURL(name string, id int64) string {
	return fmt.Sprintf("%s/admin/pipelines/%s/%d", s.a.Env.PublicURL, name, id)
}

func (s *Service) hStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tokenPipeline(w, r)
	if !ok {
		return
	}
	var in startIn
	if err := decode(w, r, &in, maxStepsBody); err != nil {
		api.Fail(w, r, err)
		return
	}
	rn, cols, err := s.startRun(r.Context(), p, in)
	var held *activeError
	if errors.As(err, &held) {
		api.JSON(w, http.StatusConflict, map[string]any{"detail": held.Error(), "code": "run_active", "run": held.Run, "trigger": held.Trigger,
			"started_at": held.StartedAt.UTC(), "active_at": held.ActiveAt.UTC()})
		return
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	names := make([]map[string]string, len(cols))
	for i, c := range cols {
		names[i] = map[string]string{"id": c.ID, "name": c.Name}
	}
	api.JSON(w, http.StatusCreated, map[string]any{"run": rn.ID, "url": s.runURL(p.Name, rn.ID), "version": version.Version,
		"dry_run": rn.DryRun, "collections": names})
}

func (s *Service) hSteps(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tokenPipeline(w, r)
	if !ok {
		return
	}
	rn, ok := s.tokenRun(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		Steps []stepIn `json:"steps"`
	}
	if err := decode(w, r, &in, maxStepsBody); err != nil {
		api.Fail(w, r, err)
		return
	}
	ignored, err := s.applySteps(r.Context(), rn.ID, in.Steps)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": ignored})
}

func (s *Service) hSync(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tokenPipeline(w, r)
	if !ok {
		return
	}
	rn, ok := s.tokenRun(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		Collection string               `json:"collection"`
		Files      []knowledge.SyncFile `json:"files"`
		Complete   bool                 `json:"complete"`
	}
	if err := decode(w, r, &in, 32<<20); err != nil {
		api.Fail(w, r, err)
		return
	}
	ctx := r.Context()
	c, err := s.targetOf(ctx, p, in.Collection)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	// The flags are read now: an admin who ticks "Adopt once" for the next run
	// must not change the one in flight half way.
	plan, err := s.k.SyncPlan(ctx, knowledge.SyncPlanReq{SyncScope: knowledge.SyncScope{Pipeline: p.ID, Knowledge: c.ID, Run: rn.ID},
		Files: in.Files, Adopt: p.Adopt, AllowMassDelete: p.AllowMassDelete, DryRun: rn.DryRun})
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	now := s.now()
	if err := s.editNode(ctx, rn.ID, c.ID, func(st *step, budget *int) {
		st.start(now)
		// push lists twice (the second listing checks what the uploads left): the first
		// listing's numbers stand, because by the second every uploaded file may already
		// be live and would count as unchanged, and a second listing adopts nothing new
		if _, listed := st.Counts["listed"]; !listed {
			st.Counts["listed"], st.Counts["unchanged"] = int64(len(in.Files)), int64(len(plan.Unchanged))
		}
		st.Counts["adopted"] = max(st.Counts["adopted"], int64(len(plan.Adopted)))
		if rn.DryRun { // what a real run would do
			st.Counts["uploaded"], st.Counts["removed"] = int64(len(plan.Upload)), int64(len(plan.Stale))
		}
		st.nodeLines("adopt ", plan.Adopted, now, budget)
		st.nodeLines("not adopted (two files have this name): ", plan.Ambiguous, now, budget)
	}); err != nil {
		api.Fail(w, r, err)
		return
	}
	// The guard weighs a removal against what the pipeline owned before this run's
	// uploads, so the first plan's count stays; Planned adds up what plans asked for.
	t := target{Complete: in.Complete, Listed: len(in.Files), Owned: plan.Owned, Planned: rn.Targets[c.ID].Planned + len(plan.Upload)}
	if prev, ok := rn.Targets[c.ID]; ok {
		t.Owned = prev.Owned
	}
	if _, err := s.db.Exec(ctx, `UPDATE pipeline_run SET active_at = $2, targets = jsonb_set(targets, ARRAY[$3]::text[], $4::jsonb) WHERE id = $1`,
		rn.ID, now, c.ID, t); err != nil {
		api.Fail(w, r, fmt.Errorf("record listing: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, plan)
}

func (s *Service) hPut(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tokenPipeline(w, r)
	if !ok {
		return
	}
	rn, ok := s.tokenRun(w, r, p, true)
	if !ok {
		return
	}
	if rn.DryRun {
		api.Fail(w, r, errDryRun)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	c, err := s.targetOf(ctx, p, q.Get("collection"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := s.planned(ctx, rn, c.ID); err != nil {
		api.Fail(w, r, err)
		return
	}
	res, err := s.k.SyncPut(ctx, knowledge.SyncPutReq{SyncScope: knowledge.SyncScope{Pipeline: p.ID, Knowledge: c.ID, Run: rn.ID},
		Owner: p.UserID, Name: q.Get("name"), SHA256: q.Get("sha256"), Body: r.Body})
	if err != nil {
		s.refused(ctx, rn.ID, c.ID, q.Get("name"), err)
		api.Fail(w, r, err)
		return
	}
	now := s.now()
	mark := "+ "
	if res.Replaces != "" {
		mark = "~ "
	}
	if err := s.editNode(ctx, rn.ID, c.ID, func(st *step, budget *int) {
		st.start(now)
		st.Counts["uploaded"]++
		st.addLog(mark+res.Name, now, budget)
	}); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := s.beat(ctx, rn.ID); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"name": res.Name, "changed": true, "replaced": res.Replaces != ""})
}

// planned refuses an upload the run's plans did not ask for: the collection must
// have been listed, and the uploads (and refusals) so far must stay within what
// the plans asked for, plus a slack for a retried upload. Without it a leaked or
// looping token could store any number of files.
// ponytail: it counts uploads, not names, so a token can still send the planned
// number of files under other names; the upgrade is to keep the planned names on the run.
func (s *Service) planned(ctx context.Context, rn *run, kid string) error {
	want := rn.Targets[kid].Planned
	var done int64
	if err := s.db.QueryRow(ctx, `SELECT coalesce((counts->>'uploaded')::bigint, 0) + coalesce((counts->>'rejected')::bigint, 0)
		FROM pipeline_step WHERE run_id = $1 AND kera AND key = $2`, rn.ID, kid).Scan(&done); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("count uploads: %w", err)
	}
	if want == 0 || done >= int64(want+max(10, want/10)) {
		return api.Errf(http.StatusConflict, "not_planned", "The server did not ask for this upload: list the collection's files first and send only the names it asks for")
	}
	return nil
}

// refused records an upload the server turned down for a reason of the file (too
// large, the checksum, the name) on the collection's step: its log and the count
// that makes the node, and so the run, fail when it closes.
func (s *Service) refused(ctx context.Context, runID int64, kid, name string, err error) {
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Status >= http.StatusInternalServerError {
		return
	}
	now := s.now()
	if err := s.editNode(ctx, runID, kid, func(st *step, budget *int) {
		st.start(now)
		st.Counts["rejected"]++
		st.addLog("FAILED "+capText(name, maxStepName)+": "+ae.Message, now, budget)
	}); err != nil {
		slog.Warn("pipeline: record refused upload", "run", runID, "err", err)
	}
}

func (s *Service) hFinish(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tokenPipeline(w, r)
	if !ok {
		return
	}
	rn, ok := s.tokenRun(w, r, p, false)
	if !ok {
		return
	}
	var in struct {
		Error string `json:"error"`
	}
	if err := decode(w, r, &in, maxStepsBody); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := s.finish(r.Context(), rn.ID, in.Error); err != nil {
		api.Fail(w, r, err)
		return
	}
	s.writeRun(w, r, p.Name, rn.ID)
}

func (s *Service) hRun(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tokenPipeline(w, r)
	if !ok {
		return
	}
	rn, ok := s.tokenRun(w, r, p, false)
	if !ok {
		return
	}
	s.writeRun(w, r, p.Name, rn.ID)
}

func (s *Service) hHealth(w http.ResponseWriter, r *http.Request) {
	p, ok := s.tokenPipeline(w, r)
	if !ok {
		return
	}
	h, good, err := s.healthOf(r.Context(), p, s.now())
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	code := http.StatusOK
	if !good {
		code = http.StatusServiceUnavailable
	}
	api.JSON(w, code, h)
}

// JSON shapes ---------------------------------------------------------------------

type stepOut struct {
	Seq        int              `json:"seq"`
	Kera       bool             `json:"kera"` // a collection step (key = the collection id)
	Key        string           `json:"key"`
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	StartedAt  *time.Time       `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at"`
	Error      string           `json:"error"`
	Counts     map[string]int64 `json:"counts"`
	LogDropped bool             `json:"log_dropped"`
	Log        *string          `json:"log,omitempty"` // the step route only
}

func stepView(st step, withLog bool) stepOut {
	o := stepOut{Seq: st.Seq, Kera: st.Kera, Key: st.Key, Name: st.Name, Status: st.Status, StartedAt: st.StartedAt, FinishedAt: st.FinishedAt,
		Error: st.Error, Counts: st.Counts, LogDropped: st.LogDropped}
	if withLog {
		o.Log = &st.Log
	}
	return o
}

type runOut struct {
	ID         int64             `json:"id"`
	Pipeline   string            `json:"pipeline"`
	Trigger    string            `json:"trigger"`
	DryRun     bool              `json:"dry_run"`
	Status     string            `json:"status"` // running | ok | warning | failed
	ClientDone bool              `json:"client_done"`
	Error      string            `json:"error"`
	Source     map[string]string `json:"source"` // where the script runs, as it reported
	StartedAt  time.Time         `json:"started_at"`
	ActiveAt   time.Time         `json:"active_at"`
	FinishedAt *time.Time        `json:"finished_at"`
	Counts     map[string]int64  `json:"counts"` // the collection steps' counts added up
	Steps      *[]stepOut        `json:"steps,omitempty"`
}

// sumCounts adds up the counts of the collection steps.
func sumCounts(counts ...map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for _, c := range counts {
		for k, v := range c {
			out[k] += v
		}
	}
	return out
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func runView(r *run, name string, steps []step) runOut {
	o := runOut{ID: r.ID, Pipeline: name, Trigger: r.Trigger, DryRun: r.DryRun, Status: r.Status, ClientDone: r.ClientDone, Error: r.Error, Source: orEmpty(r.Source),
		StartedAt: r.StartedAt, ActiveAt: r.ActiveAt, FinishedAt: r.FinishedAt, Counts: map[string]int64{}}
	if steps != nil {
		list := make([]stepOut, len(steps))
		var cs []map[string]int64
		for i, st := range steps {
			list[i] = stepView(st, false)
			if st.Kera {
				cs = append(cs, st.Counts)
			}
		}
		o.Counts, o.Steps = sumCounts(cs...), &list
	}
	return o
}

// writeRun settles the run (counts move on, a finished script closes) and writes it with its steps.
func (s *Service) writeRun(w http.ResponseWriter, r *http.Request, name string, id int64) {
	ctx := r.Context()
	if _, err := s.settle(ctx, id, s.now()); err != nil {
		slog.Warn("pipeline: settle on read", "run", id, "err", err)
	}
	rn, err := s.loadRun(ctx, s.db, id)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	steps, err := s.loadSteps(ctx, s.db, id, false)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if steps == nil {
		steps = []step{}
	}
	api.JSON(w, http.StatusOK, runView(rn, name, steps))
}

type ownerOut struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type targetOut struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Missing bool   `json:"missing"` // the collection was deleted
}

type pipelineOut struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Title           string      `json:"title"`
	Owner           ownerOut    `json:"owner"`
	Targets         []targetOut `json:"targets"`
	EveryMinutes    int         `json:"every_minutes"`
	TokenPrefix     string      `json:"token_prefix"`
	Adopt           bool        `json:"adopt"`
	AllowMassDelete bool        `json:"allow_mass_delete"`
	Enabled         bool        `json:"enabled"`
	Managed         bool        `json:"managed"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	Late            bool        `json:"late"`
	Running         bool        `json:"running"`
	LastRun         *runOut     `json:"last_run"`
	LastOKAt        *time.Time  `json:"last_ok_at"`
}

func (s *Service) pipelineView(ctx context.Context, p *Pipeline) (pipelineOut, error) {
	o := pipelineOut{ID: p.ID, Name: p.Name, Title: p.Title, EveryMinutes: p.EveryMinutes, TokenPrefix: p.TokenPrefix, Adopt: p.Adopt,
		AllowMassDelete: p.AllowMassDelete, Enabled: p.Enabled, Managed: p.Managed, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Targets: []targetOut{}}
	o.Owner.ID = p.UserID
	err := s.db.QueryRow(ctx, `SELECT name, email FROM "user" WHERE id = $1`, p.UserID).Scan(&o.Owner.Name, &o.Owner.Email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return o, fmt.Errorf("read owner: %w", err)
	}
	cols, err := s.collectionsOf(ctx, s.db, p.Targets)
	if err != nil {
		return o, err
	}
	for _, id := range p.Targets {
		t := targetOut{ID: id, Missing: true}
		for _, c := range cols {
			if c.ID == id {
				t.Name, t.Missing = c.Name, false
			}
		}
		o.Targets = append(o.Targets, t)
	}
	last, err := scanRun(s.db.QueryRow(ctx, `SELECT `+runCols+` FROM pipeline_run WHERE pipeline_id = $1 ORDER BY id DESC LIMIT 1`, p.ID))
	if err != nil && !errors.Is(err, errNoRun) {
		return o, err
	}
	if last != nil {
		steps, err := s.loadSteps(ctx, s.db, last.ID, false)
		if err != nil {
			return o, err
		}
		v := runView(last, p.Name, steps)
		v.Steps = nil // the list needs the verdict, not the whole flow
		o.LastRun, o.Running = &v, last.FinishedAt == nil
	}
	if o.LastOKAt, err = s.lastGood(ctx, p); err != nil {
		return o, err
	}
	o.Late = isLate(p.EveryMinutes, o.LastOKAt, p.CreatedAt, s.now())
	return o, nil
}

// Admin routes --------------------------------------------------------------------

func (s *Service) named(w http.ResponseWriter, r *http.Request) (*Pipeline, bool) {
	p, err := s.pipelineBy(r.Context(), s.db, "name", r.PathValue("name"))
	if err != nil {
		api.Fail(w, r, err)
		return nil, false
	}
	return p, true
}

func (s *Service) aList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT `+pipelineCols+` FROM pipeline ORDER BY name`)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list pipelines: %w", err))
		return
	}
	ps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Pipeline, error) { return scanPipeline(r) })
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list pipelines: %w", err))
		return
	}
	out := make([]pipelineOut, 0, len(ps))
	for _, p := range ps {
		o, err := s.pipelineView(r.Context(), p)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		out = append(out, o)
	}
	api.JSON(w, http.StatusOK, out)
}

func (s *Service) aGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	o, err := s.pipelineView(r.Context(), p)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, o)
}

// pipelineIn is the body of create and update; on update only the fields present change.
type pipelineIn struct {
	Name           string    `json:"name"` // create only; a name never changes
	Title          *string   `json:"title"`
	OwnerID        *string   `json:"owner_id"` // update only; create makes the admin the owner
	Targets        *[]string `json:"targets"`  // collection ids (or exact names)
	NewCollections []string  `json:"new_collections"`
	EveryMinutes   *int      `json:"every_minutes"`
	Enabled        *bool     `json:"enabled"`
}

// resolveTargets turns the form's collection references into ids. A reference is
// an id or an exact name; a name two collections share needs the id. Two targets
// may not share a name (a token names its collection by name). New collections
// are only checked here; the caller creates them.
func (s *Service) resolveTargets(ctx context.Context, refs, fresh []string) (idsOut, newNames []string, err error) {
	var names []string
	for _, ref := range refs {
		rows, err := s.db.Query(ctx, `SELECT id, name FROM knowledge WHERE id = $1 OR name = $1`, ref)
		if err != nil {
			return nil, nil, fmt.Errorf("find collection: %w", err)
		}
		found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (collection, error) {
			var c collection
			return c, r.Scan(&c.ID, &c.Name)
		})
		if err != nil {
			return nil, nil, fmt.Errorf("find collection: %w", err)
		}
		pick := -1
		for i, c := range found {
			if c.ID == ref {
				pick = i
			}
		}
		switch {
		case pick < 0 && len(found) == 1:
			pick = 0
		case pick < 0 && len(found) == 0:
			return nil, nil, api.FieldErr("targets", "unknown_collection", fmt.Sprintf("There is no collection %q", ref))
		case pick < 0:
			return nil, nil, api.FieldErr("targets", "ambiguous_collection", fmt.Sprintf("Several collections are named %q; pick it by id", ref))
		}
		if !contains(idsOut, found[pick].ID) {
			idsOut = append(idsOut, found[pick].ID)
			names = append(names, found[pick].Name)
		}
	}
	for _, n := range fresh {
		n = strings.TrimSpace(n)
		if n == "" {
			return nil, nil, api.FieldErr("new_collections", "bad_name", "A new collection needs a name")
		}
		newNames = append(newNames, n)
		names = append(names, n)
	}
	for i, n := range names {
		if contains(names[:i], n) {
			return nil, nil, api.FieldErr("targets", "duplicate_target_name", fmt.Sprintf("Two target collections are named %q; names must differ within a pipeline", n))
		}
	}
	return idsOut, newNames, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// createCollections makes the new collections, owned by the admin who asked.
// ponytail: a later failure leaves them behind (empty, easy to delete).
func (s *Service) createCollections(ctx context.Context, owner string, names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		c, err := s.k.CreateFor(ctx, owner, n, "")
		if err != nil {
			return nil, err
		}
		out = append(out, c.ID)
	}
	return out, nil
}

func checkEvery(v *int) error {
	if v != nil && (*v < 0 || *v > 525600) {
		return api.FieldErr("every_minutes", "bad_every_minutes", "Must be 0 (no expectation) up to 525600 minutes")
	}
	return nil
}

func (s *Service) aCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in pipelineIn
	if err := api.DecodeForm(r, &in); err != nil {
		api.Fail(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !slugRe.MatchString(in.Name) {
		api.Fail(w, r, api.FieldErr("name", "bad_name", "Use 1 to 40 lowercase letters, digits and dashes, starting with a letter or digit"))
		return
	}
	if err := checkEvery(in.EveryMinutes); err != nil {
		api.Fail(w, r, err)
		return
	}
	var refs []string
	if in.Targets != nil {
		refs = *in.Targets
	}
	have, fresh, err := s.resolveTargets(ctx, refs, in.NewCollections)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	u := auth.FromContext(ctx)
	made, err := s.createCollections(ctx, u.ID, fresh)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	token, hash, prefix, err := newToken()
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	now := s.now()
	row := s.db.QueryRow(ctx, `INSERT INTO pipeline (id, name, title, user_id, targets, every_minutes, token_hash, token_prefix, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10) RETURNING `+pipelineCols,
		ids.New(), in.Name, deref(in.Title, ""), u.ID, targetList(have, made), derefInt(in.EveryMinutes), hash, prefix, derefBool(in.Enabled, true), now)
	p, err := scanPipeline(row)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		api.Fail(w, r, api.FieldErr("name", "name_taken", "A pipeline with this name exists"))
		return
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	o, err := s.pipelineView(ctx, p)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusCreated, map[string]any{"pipeline": o, "token": token})
}

// targetList joins the targets; never nil, which Postgres would read as NULL.
func targetList(lists ...[]string) []string {
	out := []string{}
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func deref(p *string, def string) string {
	if p == nil {
		return def
	}
	return strings.TrimSpace(*p)
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func (s *Service) aUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	var in pipelineIn
	if err := api.DecodeForm(r, &in); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := checkEvery(in.EveryMinutes); err != nil {
		api.Fail(w, r, err)
		return
	}
	owner := p.UserID
	if in.OwnerID != nil && *in.OwnerID != p.UserID {
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE id = $1 AND role <> 'pending' AND disabled_at IS NULL`, *in.OwnerID).Scan(&n); err != nil {
			api.Fail(w, r, fmt.Errorf("check owner: %w", err))
			return
		}
		if n == 0 {
			api.Fail(w, r, api.FieldErr("owner_id", "unknown_user", "There is no active user with this id"))
			return
		}
		owner = *in.OwnerID
	}
	targets := p.Targets
	if in.Targets != nil || len(in.NewCollections) > 0 {
		refs := p.Targets
		if in.Targets != nil {
			refs = *in.Targets
		}
		have, fresh, err := s.resolveTargets(ctx, refs, in.NewCollections)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		made, err := s.createCollections(ctx, auth.FromContext(ctx).ID, fresh)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		targets = targetList(have, made)
	}
	// A new owner takes over the files the pipeline uploaded and the target
	// collections the old owner held, in one transaction: the directory sync
	// deletes a user's content with the user.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if owner != p.UserID {
		if _, err := tx.Exec(ctx, `UPDATE file SET user_id = $2 WHERE user_id = $1 AND id IN (SELECT file_id FROM knowledge_file WHERE origin = $3)`,
			p.UserID, owner, knowledge.SyncOrigin(p.ID)); err != nil {
			api.Fail(w, r, fmt.Errorf("move files: %w", err))
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE knowledge SET user_id = $2 WHERE user_id = $1 AND id = ANY($3)`, p.UserID, owner, p.Targets); err != nil {
			api.Fail(w, r, fmt.Errorf("move collections: %w", err))
			return
		}
	}
	// ponytail: a collection dropped from the targets keeps its rows owned by the
	// pipeline (nothing syncs them); adding it back resumes, deleting the pipeline hands them back.
	np, err := scanPipeline(tx.QueryRow(ctx, `UPDATE pipeline SET title = $2, user_id = $3, targets = $4, every_minutes = $5, enabled = $6, updated_at = $7
		WHERE id = $1 RETURNING `+pipelineCols, p.ID, deref(in.Title, p.Title), owner, targets, derefIntOr(in.EveryMinutes, p.EveryMinutes), derefBool(in.Enabled, p.Enabled), s.now()))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		api.Fail(w, r, err)
		return
	}
	o, err := s.pipelineView(ctx, np)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, o)
}

func derefIntOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func (s *Service) aDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	// Switched off first, so no token call starts new work while the rows are handed back.
	// ponytail: a call already in flight can still stage one file; it stays hidden and unowned until removed by hand.
	if _, err := s.db.Exec(ctx, `UPDATE pipeline SET enabled = false WHERE id = $1`, p.ID); err != nil {
		api.Fail(w, r, fmt.Errorf("switch off: %w", err))
		return
	}
	if err := s.k.SyncRelease(ctx, p.ID); err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM pipeline WHERE id = $1`, p.ID); err != nil {
		api.Fail(w, r, fmt.Errorf("delete pipeline: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) aRotate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	token, hash, prefix, err := newToken()
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE pipeline SET token_hash = $2, token_prefix = $3, updated_at = $4 WHERE id = $1`, p.ID, hash, prefix, s.now()); err != nil {
		api.Fail(w, r, fmt.Errorf("rotate token: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"token": token, "token_prefix": prefix})
}

// aOnce sets or clears the one-shot flags; the next ok run clears them itself.
func (s *Service) aOnce(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	var in struct {
		Adopt      *bool `json:"adopt"`
		MassDelete *bool `json:"mass_delete"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Fail(w, r, err)
		return
	}
	np, err := scanPipeline(s.db.QueryRow(r.Context(), `UPDATE pipeline SET adopt = $2, allow_mass_delete = $3, updated_at = $4 WHERE id = $1 RETURNING `+pipelineCols,
		p.ID, derefBool(in.Adopt, p.Adopt), derefBool(in.MassDelete, p.AllowMassDelete), s.now()))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	o, err := s.pipelineView(r.Context(), np)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, o)
}

// aRuns pages a pipeline's runs, newest first: ?before=<run id>&limit=30 (at most 100).
func (s *Service) aRuns(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.Query(ctx, `SELECT `+runCols+` FROM pipeline_run WHERE pipeline_id = $1 AND ($2::bigint = 0 OR id < $2) ORDER BY id DESC LIMIT $3`,
		p.ID, before, limit+1)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list runs: %w", err))
		return
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*run, error) { return scanRun(r) })
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list runs: %w", err))
		return
	}
	more := len(runs) > limit
	if more {
		runs = runs[:limit]
	}
	ids := make([]int64, len(runs))
	for i, x := range runs {
		ids[i] = x.ID
	}
	cr, err := s.db.Query(ctx, `SELECT run_id, counts FROM pipeline_step WHERE run_id = ANY($1) AND kera`, ids)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list run counts: %w", err))
		return
	}
	counts := map[int64][]map[string]int64{}
	for cr.Next() {
		var id int64
		var c map[string]int64
		if err := cr.Scan(&id, &c); err != nil {
			cr.Close()
			api.Fail(w, r, fmt.Errorf("scan run counts: %w", err))
			return
		}
		counts[id] = append(counts[id], c)
	}
	cr.Close()
	if err := cr.Err(); err != nil {
		api.Fail(w, r, err)
		return
	}
	out := make([]runOut, len(runs))
	for i, x := range runs {
		out[i] = runView(x, p.Name, nil)
		out[i].Counts = sumCounts(counts[x.ID]...)
	}
	api.JSON(w, http.StatusOK, map[string]any{"runs": out, "more": more})
}

// adminRun loads the run in the path of an admin route.
func (s *Service) adminRun(w http.ResponseWriter, r *http.Request) (*Pipeline, *run, bool) {
	p, ok := s.named(w, r)
	if !ok {
		return nil, nil, false
	}
	id, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
	if err != nil {
		api.Fail(w, r, errNoRun)
		return nil, nil, false
	}
	rn, err := s.loadRun(r.Context(), s.db, id)
	if err == nil && rn.PipelineID != p.ID {
		err = errNoRun
	}
	if err != nil {
		api.Fail(w, r, err)
		return nil, nil, false
	}
	return p, rn, true
}

func (s *Service) aRun(w http.ResponseWriter, r *http.Request) {
	p, rn, ok := s.adminRun(w, r)
	if !ok {
		return
	}
	s.writeRun(w, r, p.Name, rn.ID)
}

func (s *Service) aStep(w http.ResponseWriter, r *http.Request) {
	_, rn, ok := s.adminRun(w, r)
	if !ok {
		return
	}
	seq, err := strconv.Atoi(r.PathValue("seq"))
	if err != nil {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "Step not found"))
		return
	}
	st, err := scanStep(s.db.QueryRow(r.Context(), `SELECT `+stepCols+`, log FROM pipeline_step WHERE run_id = $1 AND seq = $2`, rn.ID, seq), true)
	if errors.Is(err, pgx.ErrNoRows) {
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "Step not found"))
		return
	}
	if err != nil {
		api.Fail(w, r, fmt.Errorf("read step: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, stepView(st, true))
}

// aClose closes a stuck run as failed (the script died, or indexing is stuck).
func (s *Service) aClose(w http.ResponseWriter, r *http.Request) {
	p, rn, ok := s.adminRun(w, r)
	if !ok {
		return
	}
	if err := s.force(r.Context(), rn.ID, "Closed by an admin at "+utcClock(s.now())+".", s.now()); err != nil {
		api.Fail(w, r, err)
		return
	}
	s.writeRun(w, r, p.Name, rn.ID)
}
