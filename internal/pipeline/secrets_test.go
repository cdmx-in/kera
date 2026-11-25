package pipeline

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// throwaway builds a value at run time, so no fixture literal looks like a token.
func throwaway(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "tv-" + hex.EncodeToString(b)
}

type secretsList struct {
	Secrets []secretOut `json:"secrets"`
}

func TestSecretsAdminAPI(t *testing.T) {
	e := newEnv(t, false)
	v1, v2 := throwaway(t), throwaway(t)
	const path = "/api/v1/admin/secrets"

	// admin only
	for _, c := range []struct{ method, path string }{{"GET", path}, {"POST", path}, {"PUT", path + "/x"}, {"DELETE", path + "/x"}} {
		e.call(e.user, c.method, c.path, map[string]any{"name": "x", "value": v1}, http.StatusForbidden, nil)
		e.call("", c.method, c.path, nil, http.StatusUnauthorized, nil)
	}

	// create: the name, the value and the count are checked
	for _, bad := range []map[string]any{
		{"name": "Upper", "value": v1}, {"name": "has space", "value": v1}, {"name": "-lead", "value": v1}, {"name": "", "value": v1},
		{"name": strings.Repeat("a", 101), "value": v1},
		{"name": "ok", "value": ""}, {"name": "ok"}, {"name": "ok", "value": "a\x00b"}, {"name": "ok", "value": strings.Repeat("x", maxEnvValue+1)},
	} {
		e.call(e.admin, "POST", path, bad, http.StatusBadRequest, nil)
	}
	var list secretsList
	e.call(e.admin, "POST", path, map[string]any{"name": "svc.read-token_1", "description": "read access", "value": v1}, http.StatusCreated, &list)
	e.call(e.admin, "POST", path, map[string]any{"name": "svc.read-token_1", "value": v2}, http.StatusBadRequest, nil) // taken
	if len(list.Secrets) != 1 || list.Secrets[0].Name != "svc.read-token_1" || list.Secrets[0].Description != "read access" || len(list.Secrets[0].UsedBy) != 0 {
		t.Fatalf("created: %+v", list)
	}

	// the value is never returned, by any route, and is stored encrypted
	code, raw := e.do(e.admin, "GET", path, nil)
	if code != http.StatusOK || strings.Contains(string(raw), v1) {
		t.Fatalf("list leaks the value: %d %s", code, raw)
	}
	var stored []byte
	if err := e.pool.QueryRow(t.Context(), `SELECT value_enc FROM secret WHERE name = 'svc.read-token_1'`).Scan(&stored); err != nil || strings.Contains(string(stored), v1) || len(stored) == 0 {
		t.Fatalf("stored in the clear or missing: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM secret WHERE last_used_at IS NOT NULL`); n != 0 {
		t.Fatalf("a secret nobody used has last_used_at")
	}

	// update is write-only: description alone keeps the value, a new value rotates it, an empty one is refused
	e.call(e.admin, "PUT", path+"/svc.read-token_1", map[string]any{"description": "renamed"}, http.StatusOK, &list)
	if list.Secrets[0].Description != "renamed" {
		t.Fatalf("description: %+v", list)
	}
	var same []byte
	_ = e.pool.QueryRow(t.Context(), `SELECT value_enc FROM secret WHERE name = 'svc.read-token_1'`).Scan(&same)
	if string(same) != string(stored) {
		t.Fatal("a description change touched the value")
	}
	e.call(e.admin, "PUT", path+"/svc.read-token_1", map[string]any{"value": ""}, http.StatusBadRequest, nil)
	e.call(e.admin, "PUT", path+"/svc.read-token_1", map[string]any{}, http.StatusBadRequest, nil)
	e.call(e.admin, "PUT", path+"/nope", map[string]any{"value": v2}, http.StatusNotFound, nil)
	code, raw = e.do(e.admin, "PUT", path+"/svc.read-token_1", map[string]any{"value": v2})
	if code != http.StatusOK || strings.Contains(string(raw), v1) || strings.Contains(string(raw), v2) {
		t.Fatalf("update leaks a value: %d %s", code, raw)
	}

	// delete: unknown is 404, an unreferenced secret goes
	e.call(e.admin, "DELETE", path+"/nope", nil, http.StatusNotFound, nil)
	e.call(e.admin, "POST", path, map[string]any{"name": "spare", "value": v1}, http.StatusCreated, nil)
	e.call(e.admin, "DELETE", path+"/spare", nil, http.StatusNoContent, nil)

	// at most 200
	for i := 0; i < maxSecrets; i++ {
		if _, err := e.pool.Exec(t.Context(), `INSERT INTO secret (id, name, value_enc) VALUES ($1, $2, '\x01') ON CONFLICT DO NOTHING`, "id"+itoa(int64(i)), "n"+itoa(int64(i))); err != nil {
			t.Fatal(err)
		}
	}
	e.call(e.admin, "POST", path, map[string]any{"name": "one-too-many", "value": v1}, http.StatusBadRequest, nil)
}

func TestEnvReferencesAStoredSecret(t *testing.T) {
	e := newEnv(t, false)
	_, base := e.kmode("docs")
	val, rotated := throwaway(t), throwaway(t)
	e.call(e.admin, "POST", "/api/v1/admin/secrets", map[string]any{"name": "shared-key", "value": val}, http.StatusCreated, nil)

	// exactly one of value and secret_ref; an unknown secret is a field error
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "API_KEY", "value": "x", "secret_ref": "shared-key"}, http.StatusBadRequest, nil)
	var bad struct{ Field string }
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "API_KEY", "secret_ref": "missing"}, http.StatusBadRequest, &bad)
	if bad.Field != "secret_ref" {
		t.Fatalf("field = %q", bad.Field)
	}
	var env struct{ Env []envOut }
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "API_KEY", "secret_ref": "shared-key"}, http.StatusOK, &env)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "OWN", "value": "plain-own-value", "secret": false}, http.StatusOK, &env)
	var ref *envOut
	for i := range env.Env {
		if env.Env[i].Name == "API_KEY" {
			ref = &env.Env[i]
		}
	}
	if ref == nil || !ref.Secret || ref.Value != nil || ref.SecretRef == nil || *ref.SecretRef != "shared-key" {
		t.Fatalf("listing of a referencing row: %+v", env.Env)
	}
	code, raw := e.do(e.admin, "GET", base+"/env", nil)
	if code != http.StatusOK || strings.Contains(string(raw), val) || !strings.Contains(string(raw), `"value":null`) {
		t.Fatalf("env listing: %d %s", code, raw)
	}

	// the secret lists who uses it and refuses deletion
	var list secretsList
	e.call(e.admin, "GET", "/api/v1/admin/secrets", nil, http.StatusOK, &list)
	if u := list.Secrets[0].UsedBy; len(u) != 1 || u[0] != (usedBy{Pipeline: "docs", Variable: "API_KEY"}) {
		t.Fatalf("used_by: %+v", list)
	}
	var conflict struct {
		Code   string
		UsedBy []usedBy `json:"used_by"`
	}
	e.call(e.admin, "DELETE", "/api/v1/admin/secrets/shared-key", nil, http.StatusConflict, &conflict)
	if conflict.Code != "in_use" || len(conflict.UsedBy) != 1 || conflict.UsedBy[0].Variable != "API_KEY" {
		t.Fatalf("409 body: %+v", conflict)
	}

	// a job gets the decrypted value, the name is masked like any secret, and last_used_at moves
	tok := e.runner("r1")
	e.call(e.admin, "POST", base+"/run", nil, http.StatusOK, nil)
	var j job
	e.call(tok, "POST", "/api/v1/runner/claim", map[string]any{}, http.StatusOK, &j)
	if j.Env["API_KEY"] != val || j.Env["OWN"] != "plain-own-value" || !slices.Contains(j.Secrets, "API_KEY") || slices.Contains(j.Secrets, "OWN") {
		t.Fatalf("job: env %v secrets %v", j.Env, j.Secrets)
	}
	var vals []string
	for _, n := range j.Secrets {
		vals = append(vals, j.Env[n])
	}
	if got := maskValues("token="+val+" region=x", vals); strings.Contains(got, val) {
		t.Fatalf("a referenced secret is not masked: %q", got)
	}
	if n := e.count(`SELECT count(*) FROM secret WHERE name = 'shared-key' AND last_used_at IS NOT NULL`); n != 1 {
		t.Fatal("last_used_at was not set when the job env was built")
	}

	// rotating the secret reaches the referencing pipeline with no change to it; sessions read the same way
	e.call(e.admin, "PUT", "/api/v1/admin/secrets/shared-key", map[string]any{"value": rotated}, http.StatusOK, nil)
	var pid string
	if err := e.pool.QueryRow(t.Context(), `SELECT id FROM pipeline WHERE name = 'docs'`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	got, secrets, err := e.s.loadEnv(t.Context(), pid)
	if err != nil || got["API_KEY"] != rotated || !slices.Contains(secrets, "API_KEY") {
		t.Fatalf("loadEnv after rotating: %v %v %v", got, secrets, err)
	}

	// switching the variable back to its own value frees the secret
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "API_KEY", "value": "own-again"}, http.StatusOK, nil)
	e.call(e.admin, "DELETE", "/api/v1/admin/secrets/shared-key", nil, http.StatusNoContent, nil)

	// deleting a referencing variable also frees it
	e.call(e.admin, "POST", "/api/v1/admin/secrets", map[string]any{"name": "other", "value": val}, http.StatusCreated, nil)
	e.call(e.admin, "PUT", base+"/env", map[string]any{"name": "OTHER", "secret_ref": "other"}, http.StatusOK, nil)
	e.call(e.admin, "DELETE", base+"/env/OTHER", nil, http.StatusOK, nil)
	e.call(e.admin, "DELETE", "/api/v1/admin/secrets/other", nil, http.StatusNoContent, nil)
}
