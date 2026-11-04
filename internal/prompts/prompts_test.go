package prompts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

func TestPrompts(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	keys, _ := secret.Derive(strings.Repeat("k", 32))
	st := settings.New(pool)
	srv, err := api.New(config.Env{}, pool)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{DB: pool, Keys: keys, Settings: st, Server: srv, Auth: auth.New(pool, keys, st, false)}
	if err := Mount(a); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	tok := map[string]string{}
	for _, u := range []string{"alice", "bob"} {
		if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email) VALUES ($1, $1 || '@x.test')`, u); err != nil {
			t.Fatal(err)
		}
		tok[u], _ = auth.IssueToken(keys, u, time.Hour)
	}
	do := func(user, method, path string, body any, want int, out any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok[user])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, resp.StatusCode, want, rb)
		}
		if out != nil {
			_ = json.Unmarshal(rb, out)
		}
	}
	// Default permissions deny workspace.prompts.
	do("alice", "POST", "/api/v1/prompts/create", map[string]any{"command": "x", "content": "y"}, 403, nil)
	_ = st.Set(ctx, "user.permissions", map[string]any{"workspace": map[string]any{"prompts": true}}, false)

	var p map[string]any
	do("alice", "POST", "/api/v1/prompts/create", map[string]any{"command": "/Summary", "name": "Summarise", "content": "Summarise {{CLIPBOARD}}"}, 200, &p)
	if p["command"] != "summary" || p["title"] != "Summarise" || p["write_access"] != true {
		t.Fatalf("created %v", p)
	}
	id := p["id"].(string)
	do("alice", "POST", "/api/v1/prompts/create", map[string]any{"command": "summary", "content": "again"}, 409, nil)
	do("alice", "POST", "/api/v1/prompts/create", map[string]any{"command": "bad command!", "content": "x"}, 400, nil)
	do("bob", "POST", "/api/v1/prompts/create", map[string]any{"command": "summary", "content": "bob's"}, 200, nil) // per user

	var list []map[string]any
	do("bob", "GET", "/api/v1/prompts/", nil, 200, &list)
	if len(list) != 1 || list[0]["user_id"] != "bob" {
		t.Fatalf("bob list %v", list)
	}
	do("bob", "GET", "/api/v1/prompts/id/"+id, nil, 404, nil)
	do("bob", "POST", "/api/v1/prompts/id/"+id+"/update", map[string]any{"content": "x"}, 404, nil)

	do("alice", "PUT", "/api/v1/prompts/id/"+id+"/grants", []map[string]string{{"principal": "*", "permission": "read"}}, 200, nil)
	do("bob", "GET", "/api/v1/prompts/", nil, 200, &list)
	if len(list) != 2 || list[0]["user_id"] != "bob" || list[1]["write_access"] != false {
		t.Fatalf("shared list %v", list)
	}
	do("bob", "GET", "/api/v1/prompts/command/summary", nil, 200, &p)
	if p["user_id"] != "bob" {
		t.Fatalf("own prompt wins: %v", p)
	}
	do("bob", "POST", "/api/v1/prompts/id/"+id+"/update", map[string]any{"content": "x"}, 403, nil)

	do("alice", "POST", "/api/v1/prompts/id/"+id+"/update", map[string]any{"title": "Short summary", "command": "sum"}, 200, &p)
	if p["command"] != "sum" || p["title"] != "Short summary" || p["content"] != "Summarise {{CLIPBOARD}}" {
		t.Fatalf("updated %v", p)
	}
	do("alice", "DELETE", "/api/v1/prompts/id/"+id+"/delete", nil, 200, nil)
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM access_grant WHERE resource_id = $1`, id).Scan(&n)
	if n != 0 {
		t.Fatal("grants survived")
	}
}
