package chats

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/testdb"
)

// server mounts the chat routes with a fake sign-in (X-User header); only u1 may use cowork.
func server(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	pool := testdb.New(t)
	if _, err := pool.Exec(context.Background(), `INSERT INTO "user" (id, email) VALUES ('u1', 'a@x'), ('u2', 'b@x')`); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: pool}
	h := &handler{s: s}
	user := func(f http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f(w, r.WithContext(auth.WithUser(r.Context(), &auth.User{ID: r.Header.Get("X-User"), Role: "user",
				Permissions: auth.Permissions{"features": map[string]any{"cowork": r.Header.Get("X-User") == "u1"}}})))
		})
	}
	mux := http.NewServeMux()
	for p, fn := range h.routes(user, func(_ string, f http.HandlerFunc) http.Handler { return user(f) }) {
		mux.Handle(p, fn)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, s
}

func do(t *testing.T, srv *httptest.Server, user, method, path, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("X-User", user)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return res.StatusCode
}

// The throwaway-chat flow: ChatForm with a user turn and an empty
// assistant placeholder, then the Open WebUI-shaped chat JSON.
func TestCreateFromChatFormAndCompatShape(t *testing.T) {
	srv, s := server(t)
	form := `{"chat":{"id":"","title":"Google Chat","models":["m"],"tags":["Bot"],"history":{"currentId":"a1","messages":{
		"a1":{"id":"a1","parentId":"q1","childrenIds":[],"role":"assistant","content":"","model":"m","timestamp":1700000001,"done":false},
		"q1":{"id":"q1","parentId":null,"childrenIds":["a1"],"role":"user","content":[{"type":"text","text":"hello"}],"timestamp":1700000000}}}}}`
	var created map[string]any
	if code := do(t, srv, "u1", "POST", "/api/v1/chats/new", form, &created); code != 200 {
		t.Fatalf("create %d", code)
	}
	id := created["id"].(string)
	var c struct {
		ID    string   `json:"id"`
		Title string   `json:"title"`
		Tags  []string `json:"tags"`
		Chat  struct {
			Models  []string `json:"models"`
			History struct {
				CurrentID string                    `json:"currentId"`
				Messages  map[string]map[string]any `json:"messages"`
			} `json:"history"`
			Messages []map[string]any `json:"messages"`
		} `json:"chat"`
	}
	do(t, srv, "u1", "GET", "/api/v1/chats/"+id, "", &c)
	h := c.Chat.History
	if c.Title != "Google Chat" || h.CurrentID != "a1" || len(c.Chat.Messages) != 2 || c.Tags[0] != "bot" {
		t.Fatalf("chat %+v", c)
	}
	q, a := h.Messages["q1"], h.Messages["a1"]
	if q["content"] != "hello" || q["parentId"] != nil || q["childrenIds"].([]any)[0] != "a1" || q["timestamp"] != float64(1700000000) {
		t.Fatalf("user message %v", q)
	}
	if a["done"] != false || a["status"] != "streaming" || a["parentId"] != "q1" {
		t.Fatalf("assistant placeholder %v", a)
	}
	for _, k := range []string{"parts", "files", "sources", "pendingCalls", "followUps", "statusHistory", "usage", "createdAt"} {
		if _, ok := a[k]; !ok {
			t.Fatalf("message lacks %s", k)
		}
	}
	// other users cannot see it
	if code := do(t, srv, "u2", "GET", "/api/v1/chats/"+id, "", nil); code != 404 {
		t.Fatalf("foreign chat: %d", code)
	}
	// delete one message: its subtree goes and the leaf moves to the parent
	c.Chat.History.Messages = nil // decoding merges into an existing map
	do(t, srv, "u1", "DELETE", "/api/v1/chats/"+id+"/messages/a1", "", &c)
	if len(c.Chat.History.Messages) != 1 || c.Chat.History.CurrentID != "q1" {
		t.Fatalf("after delete %+v", c.Chat.History)
	}
	do(t, srv, "u1", "POST", "/api/v1/chats/"+id+"/messages/q1", `{"content":"edited"}`, &c)
	if c.Chat.History.Messages["q1"]["content"] != "edited" {
		t.Fatal("edit lost")
	}
	_ = s
}

func TestListSearchPinArchiveFoldersShare(t *testing.T) {
	srv, s := server(t)
	ctx := context.Background()
	mk := func(title, text string) string {
		c := Chat{UserID: "u1", Title: title}
		if err := s.Create(ctx, &c); err != nil {
			t.Fatal(err)
		}
		m := Message{ChatID: c.ID, Role: "user", Content: text}
		if err := s.InsertMessage(ctx, &m); err != nil {
			t.Fatal(err)
		}
		if err := s.SetCurrent(ctx, c.ID, m.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		return c.ID
	}
	a := mk("Vector search notes", "embeddings and HNSW")
	b := mk("Lisbon trip", "pastel de nata")
	var list []Summary
	do(t, srv, "u1", "GET", "/api/v1/chats/?page=1", "", &list)
	if len(list) != 2 || list[0].ID != b {
		t.Fatalf("list %v", list)
	}
	do(t, srv, "u1", "GET", "/api/v1/chats/search?text=vect", "", &list)
	if len(list) != 1 || list[0].ID != a {
		t.Fatalf("title prefix search %v", list)
	}
	do(t, srv, "u1", "GET", "/api/v1/chats/search?text=nata", "", &list)
	if len(list) != 1 || list[0].ID != b {
		t.Fatalf("message search %v", list)
	}
	do(t, srv, "u1", "POST", "/api/v1/chats/"+a+"/pin", "", nil)
	do(t, srv, "u1", "GET", "/api/v1/chats/pinned", "", &list)
	if len(list) != 1 || !list[0].Pinned {
		t.Fatalf("pinned %v", list)
	}
	var pinned bool
	do(t, srv, "u1", "GET", "/api/v1/chats/"+a+"/pinned", "", &pinned)
	if !pinned {
		t.Fatal("pinned flag")
	}
	do(t, srv, "u1", "POST", "/api/v1/chats/"+b+"/archive", "", nil)
	do(t, srv, "u1", "GET", "/api/v1/chats/", "", &list)
	if len(list) != 1 {
		t.Fatalf("archived chat still listed %v", list)
	}
	do(t, srv, "u1", "GET", "/api/v1/chats/archived", "", &list)
	if len(list) != 1 || list[0].ID != b {
		t.Fatalf("archived %v", list)
	}

	var f Folder
	do(t, srv, "u1", "POST", "/api/v1/folders/", `{"name":"Research"}`, &f)
	if code := do(t, srv, "u2", "POST", "/api/v1/chats/"+a+"/folder", `{"folder_id":"`+f.ID+`"}`, nil); code != 404 {
		t.Fatalf("foreign move %d", code)
	}
	do(t, srv, "u1", "POST", "/api/v1/chats/"+a+"/folder", `{"folder_id":"`+f.ID+`"}`, nil)
	do(t, srv, "u1", "GET", "/api/v1/chats/folder/"+f.ID, "", &list)
	if len(list) != 1 || list[0].ID != a {
		t.Fatalf("folder chats %v", list)
	}
	do(t, srv, "u1", "POST", "/api/v1/folders/"+f.ID+"/update", `{"name":"Papers"}`, &f)
	if f.Name != "Papers" {
		t.Fatalf("rename %v", f)
	}
	do(t, srv, "u1", "DELETE", "/api/v1/folders/"+f.ID, "", nil)
	c, _ := s.Get(ctx, "u1", a)
	if c.FolderID != nil {
		t.Fatal("chat not moved out of the deleted folder")
	}

	var tags []map[string]string
	do(t, srv, "u1", "POST", "/api/v1/chats/"+a+"/tags", `{"name":"Machine Learning"}`, &tags)
	if len(tags) != 1 || tags[0]["name"] != "machine_learning" {
		t.Fatalf("tags %v", tags)
	}
	do(t, srv, "u1", "GET", "/api/v1/chats/search?text=tag:machine_learning", "", &list)
	if len(list) != 1 {
		t.Fatalf("tag search %v", list)
	}

	var share map[string]any
	do(t, srv, "u1", "POST", "/api/v1/chats/"+a+"/share", "", &share)
	sid, _ := share["share_id"].(string)
	var shared struct {
		Title string `json:"title"`
		Chat  struct {
			History struct {
				Messages map[string]any `json:"messages"`
			} `json:"history"`
		} `json:"chat"`
	}
	if code := do(t, srv, "u2", "GET", "/api/v1/chats/share/"+sid, "", &shared); code != 200 || shared.Title != "Vector search notes" || len(shared.Chat.History.Messages) != 1 {
		t.Fatalf("shared %d %+v", code, shared)
	}
	do(t, srv, "u1", "DELETE", "/api/v1/chats/"+a+"/share", "", nil)
	if code := do(t, srv, "u2", "GET", "/api/v1/chats/share/"+sid, "", nil); code != 404 {
		t.Fatalf("unshared still visible: %d", code)
	}
	var title struct{ Title string }
	do(t, srv, "u1", "POST", "/api/v1/chats/"+a, `{"chat":{"title":"Renamed"}}`, &title)
	if title.Title != "Renamed" {
		t.Fatalf("rename %v", title)
	}
	if code := do(t, srv, "u1", "DELETE", "/api/v1/chats/"+a, "", nil); code != 200 {
		t.Fatalf("delete %d", code)
	}
}

func TestUIPartsAndSourcesFromImportedShapes(t *testing.T) {
	parts := UIParts(json.RawMessage(`[{"type":"status","action":"web_search","description":"Searched 2 sites","urls":["https://a.example"]},
		{"type":"reasoning","text":"hm","done":true},{"type":"tool_call","callId":"c1","name":"search","arguments":"{\"q\":1}"},
		{"type":"tool_result","callId":"c1","name":"search","result":"found"},{"type":"text","text":"answer"}]`))
	var got []map[string]any
	_ = json.Unmarshal(parts, &got)
	if len(got) != 4 || got[0]["type"] != "steps" || got[2]["type"] != "steps" || got[3]["text"] != "answer" {
		t.Fatalf("parts %s", parts)
	}
	step := got[2]["steps"].([]any)[0].(map[string]any)
	if step["output"] != "found" || step["args"].(map[string]any)["q"] != float64(1) {
		t.Fatalf("step %v", step)
	}
	native := json.RawMessage(`[{"type":"text","text":"x"}]`)
	if string(UIParts(native)) != string(native) {
		t.Fatal("engine parts were rewritten")
	}
	src := UISources(json.RawMessage(`[{"source":{"name":"kb","id":"k"},"document":["chunk one","chunk two"],
		"metadata":[{"name":"a.pdf","file_id":"f1","page":0},{"name":"b.md","source":"https://b.example/x"}]}]`))
	var s []map[string]any
	_ = json.Unmarshal(src, &s)
	if len(s) != 2 || s[0]["fileId"] != "f1" || s[0]["page"] != float64(1) || s[1]["url"] != "https://b.example/x" {
		t.Fatalf("sources %s", src)
	}
}

func TestPrefixQuery(t *testing.T) {
	if got := PrefixQuery("Vect-or  search's!"); got != "vect:* & or:* & search:* & s:*" {
		t.Fatalf("got %q", got)
	}
	if PrefixQuery("  ¿? ") != "" {
		t.Fatal("punctuation only")
	}
}

// The Google Chat bridge posts votes in the Open WebUI feedback shape.
func TestFeedback(t *testing.T) {
	srv, s := server(t)
	body := `{"type":"rating","data":{"rating":-1,"model_id":"m","comment":"Google Chat vote by a@x"},
		"meta":{"source":"google-chat","voter":"a@x"},"snapshot":{"chat":{"chat":{"title":"Google Chat","messages":[]}}}}`
	var out map[string]any
	if code := do(t, srv, "u2", "POST", "/api/v1/evaluations/feedback", body, &out); code != 200 {
		t.Fatalf("feedback %d %v", code, out)
	}
	if out["id"] == "" || out["user_id"] != "u2" || out["type"] != "rating" {
		t.Fatalf("response %v", out)
	}
	var n int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM feedback WHERE user_id = 'u2' AND data->>'model_id' = 'm'
		AND (data->>'rating')::int = -1 AND meta->>'voter' = 'a@x' AND snapshot->'chat'->'chat'->>'title' = 'Google Chat'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stored rows %d %v", n, err)
	}
	if code := do(t, srv, "u2", "POST", "/api/v1/evaluations/feedback", `{}`, &out); code != 200 || out["type"] != "rating" {
		t.Fatalf("empty body: %d %v", code, out)
	}
	if code := do(t, srv, "u2", "POST", "/api/v1/evaluations/feedback", `not json`, nil); code != 400 {
		t.Fatalf("bad json: %d", code)
	}
}

// Cowork tasks are chats with mode cowork: created behind features.cowork,
// carried in the chat JSON and kept out of every sidebar list.
func TestCoworkModeListAndCreate(t *testing.T) {
	srv, s := server(t)
	ctx := context.Background()
	var task, plain map[string]any
	if code := do(t, srv, "u1", "POST", "/api/v1/chats/new", `{"chat":{"title":"Goal","models":["m"],"mode":"cowork"}}`, &task); code != 200 || task["mode"] != "cowork" {
		t.Fatalf("create task %d %v", code, task)
	}
	if code := do(t, srv, "u1", "POST", "/api/v1/chats/new", `{"chat":{"title":"Plain"}}`, &plain); code != 200 || plain["mode"] != "chat" {
		t.Fatalf("create chat %d %v", code, plain)
	}
	tid := task["id"].(string)
	var got map[string]any
	if do(t, srv, "u1", "GET", "/api/v1/chats/"+tid, "", &got); got["mode"] != "cowork" {
		t.Fatalf("get task %v", got)
	}
	if code := do(t, srv, "u2", "POST", "/api/v1/chats/new", `{"chat":{"mode":"cowork"}}`, nil); code != 403 {
		t.Fatalf("cowork without permission: %d", code)
	}
	if code := do(t, srv, "u1", "POST", "/api/v1/chats/new", `{"chat":{"mode":"agent"}}`, &got); code != 400 || got["code"] != "bad_mode" {
		t.Fatalf("bad mode: %d %v", code, got)
	}
	// update never changes the mode
	do(t, srv, "u1", "POST", "/api/v1/chats/"+tid, `{"chat":{"title":"Renamed","mode":"chat","pinned":true}}`, &got)
	if got["mode"] != "cowork" {
		t.Fatalf("update changed mode: %v", got["mode"])
	}
	do(t, srv, "u1", "POST", "/api/v1/chats/"+tid+"/tags", `{"name":"t"}`, nil)
	for _, path := range []string{"/api/v1/chats/", "/api/v1/chats/?page=1", "/api/v1/chats/pinned", "/api/v1/chats/search?text=renamed", "/api/v1/chats/search?text=tag:t"} {
		var list []Summary
		do(t, srv, "u1", "GET", path, "", &list)
		for _, c := range list {
			if c.ID == tid {
				t.Fatalf("%s lists the task", path)
			}
		}
	}
	var tags []map[string]string
	if do(t, srv, "u1", "GET", "/api/v1/chats/all/tags", "", &tags); len(tags) != 0 {
		t.Fatalf("task tags listed: %v", tags)
	}
	do(t, srv, "u1", "POST", "/api/v1/chats/"+tid+"/archive", "", nil)
	if list, _ := s.List(ctx, ListFilter{UserID: "u1", Archived: true}); len(list) != 0 {
		t.Fatalf("archived list shows the task %v", list)
	}
	if list, _ := s.List(ctx, ListFilter{UserID: "u1", Archived: true, Mode: "cowork"}); len(list) != 1 || list[0].ID != tid {
		t.Fatalf("cowork filter %v", list)
	}
}
