package pipeline

// The steps real runs reported: what the stage titles and descriptions can be attached to. The script decides
// which steps exist; this is what Kera has actually seen of them.

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
)

// reportedStep is one script step as the newest run that got to it reported it.
type reportedStep struct {
	Name       string           `json:"name"`
	Run        int64            `json:"run"`
	Seq        int              `json:"seq"`    // its place in that run, for the link to its log
	Status     string           `json:"status"` // pending or skipped: declared, but no kept run got to it
	StartedAt  *time.Time       `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at"`
	Counts     map[string]int64 `json:"counts"`
	CodeAt     string           `json:"code_at"` // where the script says the step starts; may be empty
	pos        int
}

// reportedSteps reads the script steps of the kept runs (test runs from the script notebook aside), one per name,
// in the order they run. A run that takes a shorter path numbers its later steps lower, so a step's place is the
// highest number any run gave it.
func (s *Service) reportedSteps(ctx context.Context, pipelineID string) (out []reportedStep, runs int, err error) {
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM pipeline_run WHERE pipeline_id = $1 AND trigger <> 'test'`, pipelineID).Scan(&runs); err != nil {
		return nil, 0, fmt.Errorf("count runs: %w", err)
	}
	rows, err := s.db.Query(ctx, `WITH st AS (
			SELECT s.*, max(s.seq) OVER (PARTITION BY s.name) AS pos FROM pipeline_step s JOIN pipeline_run r ON r.id = s.run_id
			WHERE r.pipeline_id = $1 AND r.trigger <> 'test' AND NOT s.kera)
		SELECT DISTINCT ON (name) name, run_id, seq, status, started_at, finished_at, counts, code_at, pos
		FROM st ORDER BY name, (status IN ('pending', 'skipped')), run_id DESC`, pipelineID)
	if err != nil {
		return nil, 0, fmt.Errorf("list reported steps: %w", err)
	}
	out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (reportedStep, error) {
		var o reportedStep
		err := r.Scan(&o.Name, &o.Run, &o.Seq, &o.Status, &o.StartedAt, &o.FinishedAt, &o.Counts, &o.CodeAt, &o.pos)
		o.StartedAt, o.FinishedAt = utcPtr(o.StartedAt), utcPtr(o.FinishedAt)
		if o.Counts == nil {
			o.Counts = map[string]int64{}
		}
		return o, err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("read reported steps: %w", err)
	}
	slices.SortStableFunc(out, func(a, b reportedStep) int { return cmp.Or(a.pos-b.pos, cmp.Compare(a.Name, b.Name)) })
	if out == nil {
		out = []reportedStep{}
	}
	return out, runs, nil
}

func (s *Service) aReportedSteps(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	steps, runs, err := s.reportedSteps(r.Context(), p.ID)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"runs": runs, "steps": steps})
}
