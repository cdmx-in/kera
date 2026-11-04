package cowork

import (
	"context"
	"testing"

	"github.com/cdmx-in/kera/internal/testdb"
)

// The five statuses, the output file count, and which chats are left out.
func TestListStatuses(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO "user" (id, email) VALUES ('u1', 'a@x'), ('u2', 'b@x')`)
	// task id -> leaf status, pending calls, assistant files
	leaf := map[string][3]string{
		"t-run":   {"streaming", `[]`, `[]`},
		"t-ask":   {"streaming", `[{"callId":"c1"}]`, `[]`},
		"t-done":  {"done", `[]`, `[{"id":"f1","name":"a.csv"},{"id":"f2","name":"b.md"}]`},
		"t-stop":  {"stopped", `[]`, `[]`},
		"t-error": {"error", `[]`, `[]`},
	}
	n := 0
	for id, l := range leaf {
		n++
		exec(`INSERT INTO chat (id, user_id, title, mode, model_ids, updated_at) VALUES ($1, 'u1', $1, 'cowork', '{m-old,m-new}', now() - make_interval(secs => $2))`, id, n)
		exec(`INSERT INTO message (id, chat_id, role, content, files) VALUES ($1 || '-q', $1, 'user', 'goal', '[{"type":"collection","id":"k1"}]')`, id)
		exec(`INSERT INTO message (id, chat_id, parent_id, role, model_id, status, pending_calls, files) VALUES ($1 || '-a', $1, $1 || '-q', 'assistant', 'm-run', $2, $3, $4)`,
			id, l[0], l[1], l[2])
		exec(`UPDATE chat SET current_message_id = $1 || '-a' WHERE id = $1`, id)
	}
	// an earlier turn of t-done produced f1 again and f3: distinct ids count once
	exec(`INSERT INTO message (id, chat_id, role, model_id, files) VALUES ('t-done-a0', 't-done', 'assistant', 'm-run', '[{"id":"f1"},{"id":"f3"}]')`)
	// a new task with no messages yet; left out: archived, plain chat, someone else's
	exec(`INSERT INTO chat (id, user_id, mode, model_ids) VALUES ('t-new', 'u1', 'cowork', '{m-new}'), ('t-arch', 'u1', 'cowork', '{}'),
		('c-plain', 'u1', 'chat', '{}'), ('t-other', 'u2', 'cowork', '{}')`)
	exec(`UPDATE chat SET archived = true WHERE id = 't-arch'`)
	// a task whose first run never started: running only for a grace period
	exec(`INSERT INTO chat (id, user_id, mode, model_ids, updated_at) VALUES ('t-orphan', 'u1', 'cowork', '{m-run}', now() - interval '10 minutes')`)

	list, err := List(ctx, db, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 7 || list[0].ID != "t-new" {
		t.Fatalf("list %+v", list)
	}
	want := map[string]string{"t-run": "running", "t-ask": "needs_input", "t-done": "done", "t-stop": "stopped",
		"t-error": "error", "t-new": "running", "t-orphan": "stopped"}
	for _, task := range list {
		if task.Status != want[task.ID] {
			t.Errorf("%s: status %q, want %q", task.ID, task.Status, want[task.ID])
		}
		files, model := 0, "m-run"
		if task.ID == "t-done" {
			files = 3
		}
		if task.ID == "t-new" {
			model = "m-new"
		}
		if task.Files != files || task.Model != model || task.CreatedAt == 0 || task.UpdatedAt == 0 {
			t.Errorf("%s: %+v", task.ID, task)
		}
	}
}
