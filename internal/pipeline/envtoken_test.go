package pipeline

import (
	"net/http"
	"strings"
	"testing"
)

// A pipeline token reads the variables only after an admin allows it, only for its own pipeline, and the audit
// log names the variables without their values.
func TestEnvByToken(t *testing.T) {
	e := newEnv(t, false)
	p, _ := e.mkPipe("outside")
	other, _ := e.mkPipe("other")
	base := "/api/v1/admin/pipelines/outside"
	e.call(e.admin, "POST", "/api/v1/admin/secrets", map[string]any{"name": "shared-key", "value": "s3cr3t-value"}, http.StatusCreated, nil)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "API_KEY", "secret_ref": "shared-key"}, http.StatusOK, nil)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "API_URL", "value": "https://api.example.com", "secret": false}, http.StatusOK, nil)

	var refused struct{ Code string }
	e.call(p.tok, "GET", p.path("/env"), nil, http.StatusForbidden, &refused) // off by default

	var view pipelineOut
	e.call(e.admin, "PUT", base, map[string]any{"env_by_token": true}, http.StatusOK, &view)
	if !view.EnvByToken {
		t.Fatal("env_by_token was not switched on")
	}
	var got struct{ Env map[string]string }
	e.call(p.tok, "GET", p.path("/env"), nil, http.StatusOK, &got)
	if len(got.Env) != 2 || got.Env["API_KEY"] != "s3cr3t-value" || got.Env["API_URL"] != "https://api.example.com" {
		t.Fatalf("env: %d variables, or a wrong value", len(got.Env))
	}
	for _, tok := range []string{"", other.tok, e.admin, e.adminKey} {
		e.call(tok, "GET", p.path("/env"), nil, http.StatusUnauthorized, nil)
	}
	e.call(other.tok, "GET", other.path("/env"), nil, http.StatusForbidden, nil) // its own flag is still off

	e.call(e.admin, "PUT", base, map[string]any{"title": "renamed"}, http.StatusOK, &view) // another change keeps the flag
	if !view.EnvByToken {
		t.Fatal("an unrelated update cleared env_by_token")
	}
	if n := e.count(`SELECT count(*) FROM audit_event WHERE action = 'pipeline.env_read' AND target_label = 'outside' AND meta->'variables' ? 'API_KEY'`); n != 1 {
		t.Fatalf("%d env_read audit events, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_event WHERE action = 'pipeline.update' AND meta->>'env_shared' = 'true'`); n != 1 {
		t.Fatalf("%d audit events say the sharing was switched on, want 1", n)
	}
	var all string
	if err := e.pool.QueryRow(t.Context(), `SELECT coalesce(string_agg(meta::text || actor_label, ' '), '') FROM audit_event`).Scan(&all); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(all, "s3cr3t-value") {
		t.Fatal("the audit log holds a secret value")
	}

	e.call(e.admin, "PUT", base, map[string]any{"env_by_token": false}, http.StatusOK, &view)
	e.call(p.tok, "GET", p.path("/env"), nil, http.StatusForbidden, nil)
	e.call(e.admin, "PUT", base, map[string]any{"env_by_token": true, "enabled": false}, http.StatusOK, nil)
	e.call(p.tok, "GET", p.path("/env"), nil, http.StatusForbidden, nil) // a switched-off pipeline gives nothing
}
