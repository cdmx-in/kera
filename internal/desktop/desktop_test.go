package desktop

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

func TestDesktop(t *testing.T) {
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

	// Fake sidecar: API (one session "c1" with token "tok-c1") and websockify.
	var mu sync.Mutex
	var seen []string
	note := func(s string) { mu.Lock(); seen = append(seen, s); mu.Unlock() }
	sidecarAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note(r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization") + " user=" + r.Header.Get("X-Kera-User-Id"))
		if r.URL.Path != "/sessions/c1/view" && r.URL.Path != "/sessions/c1" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"active": true, "token": "tok-c1"}`)
	}))
	defer sidecarAPI.Close()
	sidecarView := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note("view " + r.URL.RequestURI() + " cookie=" + r.Header.Get("Cookie"))
		if r.URL.Path != "/websockify" || r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "only the websocket is proxied", http.StatusTeapot)
			return
		}
		conn, rw, _ := http.NewResponseController(w).Hijack()
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		b := make([]byte, 4)
		n, _ := io.ReadFull(rw, b)
		conn.Write(b[:n]) // echo
	}))
	defer sidecarView.Close()

	tok := map[string]string{}
	for _, u := range []string{"alice", "bob"} {
		if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, email) VALUES ($1, $1 || '@x.test')`, u); err != nil {
			t.Fatal(err)
		}
		tok[u], _ = auth.IssueToken(keys, u, time.Hour)
	}
	for _, id := range []string{"c1", "c2"} {
		if _, err := pool.Exec(ctx, `INSERT INTO chat (id, user_id, title, mode) VALUES ($1, 'alice', 't', 'cowork')`, id); err != nil {
			t.Fatal(err)
		}
	}
	do := func(user, method, path string, hdr map[string]string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok[user])
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok[user]})
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusSwitchingProtocols {
			return resp, ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	want := func(user, method, path string, code int, body string) {
		t.Helper()
		resp, got := do(user, method, path, nil)
		if resp.StatusCode != code || !strings.Contains(got, body) {
			t.Errorf("%s %s %s = %d %s, want %d %s", user, method, path, resp.StatusCode, got, code, body)
		}
	}

	// desktop.url unset: everything is 404.
	want("alice", "GET", "/api/v1/desktop/c1", 404, "")
	want("alice", "GET", "/api/v1/desktop/c1/websockify", 404, "")

	_ = st.Set(ctx, "desktop.url", sidecarAPI.URL, false)
	_ = st.Set(ctx, "desktop.view_url", sidecarView.URL, false)
	sealed, _ := apply.SealSecret(keys, "svc-key")
	_ = st.Set(ctx, "desktop.key", sealed, false)

	want("alice", "GET", "/api/v1/desktop/c1", 200, `"active":true`)
	want("alice", "GET", "/api/v1/desktop/c2", 200, `"active":false`)
	want("alice", "GET", "/api/v1/desktop/c1/view/vnc.html", 404, "") // no static noVNC files from the sidecar
	want("alice", "GET", "/api/v1/desktop/c1/websockify?token=evil", 400, "")
	// another user's chat (and an unknown chat) is 404, and the sidecar is never asked
	mu.Lock()
	seen = nil
	mu.Unlock()
	want("bob", "GET", "/api/v1/desktop/c1", 404, "")
	want("bob", "GET", "/api/v1/desktop/c1/websockify", 404, "")
	want("bob", "POST", "/api/v1/desktop/c1/end", 404, "")
	want("alice", "GET", "/api/v1/desktop/nope", 404, "")
	if len(seen) != 0 {
		t.Errorf("sidecar asked for a foreign chat: %v", seen)
	}

	// websocket: the session token is forced, cookies stay in Kera, cross-origin is refused
	ws := map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==", "Origin": ts.URL}
	if resp, _ := do("bob", "GET", "/api/v1/desktop/c1/websockify", ws); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob's upgrade on alice's chat = %d", resp.StatusCode)
	}
	resp, _ := do("alice", "GET", "/api/v1/desktop/c1/websockify?token=evil&path=x", ws)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade = %d", resp.StatusCode)
	}
	rwc := resp.Body.(io.ReadWriteCloser)
	rwc.Write([]byte("ping"))
	b := make([]byte, 4)
	if _, err := io.ReadFull(rwc, b); err != nil || string(b) != "ping" {
		t.Errorf("echo = %q %v", b, err)
	}
	rwc.Close()
	ws["Origin"], ws["Sec-Fetch-Site"] = "https://evil.example", "cross-site"
	if resp, _ := do("alice", "GET", "/api/v1/desktop/c1/websockify", ws); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin upgrade = %d", resp.StatusCode)
	}
	delete(ws, "Origin")
	delete(ws, "Sec-Fetch-Site")
	if resp, _ := do("alice", "GET", "/api/v1/desktop/c2/websockify", ws); resp.StatusCode != http.StatusNotFound { // no session: no token
		t.Errorf("upgrade without a session = %d", resp.StatusCode)
	}

	want("alice", "POST", "/api/v1/desktop/c1/end", 200, `"active":false`)
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(seen, "\n")
	for _, s := range []string{"view /websockify?token=tok-c1 cookie=\n", "DELETE /sessions/c1 Bearer svc-key user=alice\n", "GET /sessions/c1/view Bearer svc-key user=alice\n"} {
		if !strings.Contains(all+"\n", s) {
			t.Errorf("sidecar did not see %q in\n%s", s, all)
		}
	}
	if strings.Contains(all, "evil") || strings.Contains(all, "path=") || strings.Contains(all+"\n", "user=\n") {
		t.Errorf("client token reached the sidecar:\n%s", all)
	}
}
