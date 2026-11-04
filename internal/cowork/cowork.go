// Package cowork is task mode: a chat with mode "cowork" where the assistant
// works on a goal on its own (tools, sandbox, knowledge) and asks only when
// it is blocked. The chat engine adds Prompt to such runs; this package lists
// a user's tasks with a derived status for the Cowork page.
package cowork

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/settings"
)

func init() { app.Register("cowork", Mount) }

// Mount registers GET /api/v1/cowork.
func Mount(a *app.App) error {
	a.Server.Handle("GET /api/v1/cowork", a.Auth.Permission("features.cowork", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := List(r.Context(), a.DB, auth.FromContext(r.Context()).ID)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		api.JSON(w, http.StatusOK, list)
	})))
	return nil
}

// DefaultPrompt is used when the cowork.prompt setting is empty.
const DefaultPrompt = "You are working on a task on your own. The user's message is the goal.\n" +
	"- Start by restating the goal in one sentence and listing a short plan.\n" +
	"- Then carry out the plan with the tools available to you (code, files, web, knowledge). Do the work; do not describe what you would do.\n" +
	"- Ask the user (ask_user) only when you are blocked or a decision is theirs to make. Bundle your questions into one ask.\n" +
	"- When a step fails, try a different approach before giving up.\n" +
	"- Deliver results as files with descriptive names when the output is more than a short answer.\n" +
	"- End with a short report: what was done, the files produced, and anything left open."

// Prompt returns the cowork.prompt setting, or DefaultPrompt.
func Prompt(ctx context.Context, st *settings.Store) string {
	var p string
	_ = st.Get(ctx, "cowork.prompt", &p)
	if strings.TrimSpace(p) == "" {
		return DefaultPrompt
	}
	return p
}

// Task is one row of the task list.
type Task struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"` // running | needs_input | done | stopped | error
	Model     string `json:"model"`
	Files     int    `json:"files"` // distinct files on assistant messages
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// List returns the user's unarchived tasks, newest first. The status comes
// from the current message: no assistant leaf yet counts as running for two
// minutes (the first turn is being set up), then as stopped (the run never
// started: model error, permission change, tab closed).
// ponytail: first 200 only, no paging; add ?page= when someone has more.
func List(ctx context.Context, db *pgxpool.Pool, userID string) ([]Task, error) {
	rows, err := db.Query(ctx, `SELECT c.id, c.title,
			CASE WHEN (m.id IS NULL OR m.role <> 'assistant') AND c.updated_at > now() - interval '2 minutes' THEN 'running'
				WHEN m.id IS NULL OR m.role <> 'assistant' THEN 'stopped'
				WHEN m.status = 'streaming' AND jsonb_typeof(m.pending_calls) = 'array' AND jsonb_array_length(m.pending_calls) > 0 THEN 'needs_input'
				WHEN m.status = 'streaming' THEN 'running'
				ELSE m.status END,
			coalesce(nullif(m.model_id, ''), c.model_ids[cardinality(c.model_ids)], ''),
			(SELECT count(DISTINCT f->>'id') FROM message am
				CROSS JOIN jsonb_array_elements(CASE WHEN jsonb_typeof(am.files) = 'array' THEN am.files ELSE '[]' END) f
				WHERE am.chat_id = c.id AND am.role = 'assistant' AND f->>'id' <> ''),
			c.created_at, c.updated_at
		FROM chat c LEFT JOIN message m ON m.id = c.current_message_id AND m.chat_id = c.id
		WHERE c.user_id = $1 AND c.mode = 'cowork' AND NOT c.archived
		ORDER BY c.updated_at DESC, c.id LIMIT 200`, userID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		var cr, up time.Time
		if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.Model, &t.Files, &cr, &up); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		t.CreatedAt, t.UpdatedAt = cr.Unix(), up.Unix()
		out = append(out, t)
	}
	return out, rows.Err()
}
