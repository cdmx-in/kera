package pipeline

// Stage titles and descriptions: a per-pipeline catalog the admin pages use instead of the raw step name.

import (
	"context"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
)

const (
	maxStages     = 200
	maxStageTitle = 80
	maxStageAbout = 400
)

type stageOut struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	About string `json:"about"`
}

func (s *Service) stagesOf(ctx context.Context, pipelineID string) ([]stageOut, error) {
	rows, err := s.db.Query(ctx, `SELECT name, title, about FROM pipeline_stage WHERE pipeline_id = $1 ORDER BY position, name`, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("list stages: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stageOut, error) {
		var o stageOut
		return o, r.Scan(&o.Name, &o.Title, &o.About)
	})
	if err != nil {
		return nil, fmt.Errorf("read stages: %w", err)
	}
	if out == nil {
		out = []stageOut{}
	}
	return out, nil
}

type stagesIn struct {
	Stages []stageOut `json:"stages"`
}

// aStages replaces the whole list; the order sent is the order shown.
func (s *Service) aStages(w http.ResponseWriter, r *http.Request) {
	p, ok := s.named(w, r)
	if !ok {
		return
	}
	var in stagesIn
	if err := decode(w, r, &in, 1<<20); err != nil {
		api.Fail(w, r, err)
		return
	}
	if in.Stages == nil {
		api.Fail(w, r, api.FieldErr("stages", "missing", "Send the list of stages"))
		return
	}
	if len(in.Stages) > maxStages {
		api.Fail(w, r, api.FieldErr("stages", "too_many", fmt.Sprintf("At most %d stages", maxStages)))
		return
	}
	seen := map[string]bool{}
	for i := range in.Stages {
		st := &in.Stages[i]
		if st.Name == "" || len(st.Name) > maxStepName || !utf8.ValidString(st.Name) {
			api.Fail(w, r, api.FieldErr("stages", "bad_name", "A stage name is 1 to 100 characters: the step name the script reports"))
			return
		}
		if seen[st.Name] {
			api.Fail(w, r, api.FieldErr("stages", "duplicate", fmt.Sprintf("%q is listed twice", st.Name)))
			return
		}
		seen[st.Name] = true
		if utf8.RuneCountInString(st.Title) > maxStageTitle || utf8.RuneCountInString(st.About) > maxStageAbout {
			api.Fail(w, r, api.FieldErr("stages", "too_long", fmt.Sprintf("A title is at most %d characters and a description at most %d", maxStageTitle, maxStageAbout)))
			return
		}
		st.Title, st.About = capText(st.Title, maxStageTitle), capText(st.About, maxStageAbout)
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `DELETE FROM pipeline_stage WHERE pipeline_id = $1`, p.ID); err != nil {
		api.Fail(w, r, fmt.Errorf("clear stages: %w", err))
		return
	}
	for i, st := range in.Stages {
		if st.Title == "" && st.About == "" {
			continue // nothing to say: the page falls back to the name
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO pipeline_stage (pipeline_id, name, title, about, position) VALUES ($1, $2, $3, $4, $5)`, p.ID, st.Name, st.Title, st.About, i); err != nil {
			api.Fail(w, r, fmt.Errorf("save stage: %w", err))
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		api.Fail(w, r, err)
		return
	}
	s.writePipeline(w, r, p.ID)
}
