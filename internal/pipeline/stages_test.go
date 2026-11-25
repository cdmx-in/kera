package pipeline

import (
	"net/http"
	"strings"
	"testing"
)

func TestStagesCatalogRoundTripReplaceAndLimits(t *testing.T) {
	e := newEnv(t, false)
	e.mkPipe("docs")
	base := "/api/v1/admin/pipelines/docs"
	var p pipelineOut
	e.call(e.admin, "GET", base, nil, http.StatusOK, &p)
	if p.Stages == nil || len(p.Stages) != 0 {
		t.Fatalf("a pipeline with no catalog has an empty list, not null: %#v", p.Stages)
	}
	e.call(e.admin, "PUT", base+"/stages", map[string]any{"stages": []map[string]any{
		{"name": "fetch-alf", "title": "Fetch AlfBoard", "about": "Downloads tasks and comments."},
		{"name": "index", "title": "  Build search index  ", "about": ""},
		{"name": "drift"}, // nothing to say: not stored, the page falls back to the name
	}}, http.StatusOK, &p)
	if len(p.Stages) != 2 || p.Stages[0].Name != "fetch-alf" || p.Stages[0].Title != "Fetch AlfBoard" || p.Stages[1].Title != "Build search index" {
		t.Fatalf("stages: %+v", p.Stages)
	}
	// the list is replaced, in the order sent
	e.call(e.admin, "PUT", base+"/stages", map[string]any{"stages": []map[string]any{{"name": "index", "title": "Index"}, {"name": "fetch-alf", "about": "Only a description"}}}, http.StatusOK, &p)
	if len(p.Stages) != 2 || p.Stages[0].Name != "index" || p.Stages[1].Title != "" || p.Stages[1].About != "Only a description" {
		t.Fatalf("replace: %+v", p.Stages)
	}
	// it also shows in the list the page reads
	var list []pipelineOut
	e.call(e.admin, "GET", "/api/v1/admin/pipelines", nil, http.StatusOK, &list)
	if len(list) != 1 || len(list[0].Stages) != 2 {
		t.Fatalf("list: %+v", list)
	}
	for _, bad := range []map[string]any{
		{"stages": nil},
		{"stages": []map[string]any{{"name": "", "title": "x"}}},
		{"stages": []map[string]any{{"name": strings.Repeat("a", 101), "title": "x"}}},
		{"stages": []map[string]any{{"name": "a", "title": "x"}, {"name": "a", "title": "y"}}},
		{"stages": []map[string]any{{"name": "a", "title": strings.Repeat("t", 81)}}},
		{"stages": []map[string]any{{"name": "a", "about": strings.Repeat("d", 401)}}},
	} {
		e.call(e.admin, "PUT", base+"/stages", bad, http.StatusBadRequest, nil)
	}
	e.call(e.user, "PUT", base+"/stages", map[string]any{"stages": []map[string]any{}}, http.StatusForbidden, nil)
	e.call(e.admin, "PUT", base+"/stages", map[string]any{"stages": []map[string]any{}}, http.StatusOK, &p) // an empty list clears it
	if len(p.Stages) != 0 {
		t.Fatalf("clear: %+v", p.Stages)
	}
	// secrets are masked like every other text the admin pages show
	e.call(e.admin, "PUT", base+"/stages", map[string]any{"stages": []map[string]any{{"name": "a", "about": "token kp-" + strings.Repeat("a", 40)}}}, http.StatusOK, &p)
	if strings.Contains(p.Stages[0].About, "kp-aaaa") {
		t.Fatalf("a token in a description must be masked: %q", p.Stages[0].About)
	}
	e.call(e.admin, "DELETE", base, nil, http.StatusOK, nil)
	if n := e.count(`SELECT count(*) FROM pipeline_stage`); n != 0 {
		t.Fatalf("stages must go with the pipeline: %d", n)
	}
}
