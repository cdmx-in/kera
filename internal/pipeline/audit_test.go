package pipeline

import (
	"net/http"
	"strings"
	"testing"
)

// The audit log names stored secrets and variables, never their values: not in a column, not in meta.
func TestAuditHoldsNoSecretValues(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	v1, v2, v3 := throwaway(t), throwaway(t), throwaway(t)
	const secrets = "/api/v1/admin/secrets"
	e.call(e.admin, "POST", secrets, map[string]any{"name": "read-token", "description": "read access", "value": v1}, http.StatusCreated, nil)
	e.call(e.admin, "PUT", secrets+"/read-token", map[string]any{"value": v2, "description": "rotated"}, http.StatusOK, nil)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "API_KEY", "value": v3}, http.StatusOK, nil)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "REF", "secret_ref": "read-token"}, http.StatusOK, nil)
	e.call(e.admin, "DELETE", base+"/env/REF", nil, http.StatusOK, nil)
	e.call(e.admin, "POST", secrets, map[string]any{"name": "copy", "from": map[string]string{"pipeline": "docs", "variable": "API_KEY"}}, http.StatusCreated, nil)
	e.call(e.admin, "DELETE", secrets+"/copy", nil, http.StatusNoContent, nil)
	e.call(e.user, "PUT", secrets+"/read-token", map[string]any{"value": v1}, http.StatusForbidden, nil)

	rows, err := e.pool.Query(t.Context(), `SELECT action || ' ' || outcome || ' ' || target_id || ' ' || meta::text, row_to_json(a)::text
		FROM audit_event a WHERE action LIKE 'secret.%' OR action LIKE 'pipeline.env%' ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var short, whole string
		if err := rows.Scan(&short, &whole); err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{v1, v2, v3} {
			if strings.Contains(whole, v) {
				t.Fatalf("an audit row holds a secret value: %s", whole)
			}
		}
		got = append(got, short)
	}
	want := []string{
		`secret.create ok read-token {}`,
		`secret.update ok read-token {"changed": ["value", "description"]}`,
		`pipeline.env_set ok docs {"variable": "API_KEY", "secret_ref": "", "write_only": true}`,
		`pipeline.env_set ok docs {"variable": "REF", "secret_ref": "read-token", "write_only": true}`,
		`pipeline.env_delete ok docs {"var": "REF"}`,
		`secret.create ok copy {"copied_from": "docs/API_KEY"}`,
		`secret.delete ok copy {}`,
		`secret.update denied read-token {}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("audit events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
