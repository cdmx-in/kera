package chats

import (
	"net/http"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/ids"
)

// createFeedback is POST /api/v1/evaluations/feedback: a rating in the Open
// WebUI shape {type, data:{rating, model_id, comment, ...}, meta, snapshot},
// filed under the caller. Bots (the Google Chat bridge) send the real voter in
// meta. A vote that names meta.message_id replaces the caller's earlier vote on
// that message, so changing thumbs up to down leaves one row.
func (h *handler) createFeedback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type     string         `json:"type"`
		Data     map[string]any `json:"data"`
		Meta     map[string]any `json:"meta"`
		Snapshot map[string]any `json:"snapshot"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Fail(w, r, err)
		return
	}
	if in.Type == "" {
		in.Type = "rating"
	}
	for _, m := range []*map[string]any{&in.Data, &in.Meta, &in.Snapshot} {
		if *m == nil {
			*m = map[string]any{}
		}
	}
	if v, ok := in.Data["rating"]; ok && v != 1.0 && v != -1.0 {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_rating", "rating must be 1 or -1"))
		return
	}
	if c, _ := in.Data["comment"].(string); len(c) > 2000 {
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "comment_too_long", "comment is limited to 2000 characters"))
		return
	}
	msgID, _ := in.Meta["message_id"].(string)
	id, user := ids.New(), uid(r)
	var created, updated time.Time
	if err := h.s.DB.QueryRow(r.Context(), `WITH replaced AS (
			DELETE FROM feedback WHERE user_id = $2 AND $7 <> '' AND meta->>'message_id' = $7)
		INSERT INTO feedback (id, user_id, type, data, meta, snapshot)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at, updated_at`,
		id, user, in.Type, in.Data, in.Meta, in.Snapshot, msgID).Scan(&created, &updated); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"id": id, "user_id": user, "version": 0, "type": in.Type,
		"data": in.Data, "meta": in.Meta, "snapshot": in.Snapshot, "created_at": created.Unix(), "updated_at": updated.Unix()})
}

// chatVotes is GET /api/v1/evaluations/feedback/chat/{id}: the caller's own
// votes in one chat, {message_id: rating}, so the thumbs show after a reload.
func (h *handler) chatVotes(w http.ResponseWriter, r *http.Request) {
	rows, err := h.s.DB.Query(r.Context(), `SELECT meta->>'message_id', (data->>'rating')::int FROM feedback
		WHERE user_id = $1 AND meta->>'chat_id' = $2 AND meta ? 'message_id' AND data->>'rating' IN ('1', '-1')`,
		uid(r), r.PathValue("id"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var msg string
		var rating int
		if err := rows.Scan(&msg, &rating); err != nil {
			api.Fail(w, r, err)
			return
		}
		out[msg] = rating
	}
	if err := rows.Err(); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out)
}

// allFeedback is GET /api/v1/evaluations/feedbacks/all (admins): the newest
// 200 votes with the voter, newest first.
// ponytail: no paging or filters; add them when 200 rows stop being enough.
func (h *handler) allFeedback(w http.ResponseWriter, r *http.Request) {
	rows, err := h.s.DB.Query(r.Context(), `SELECT f.id, COALESCE(u.name, ''), COALESCE(u.email, ''),
			COALESCE(f.data->>'model_id', ''), COALESCE((f.data->>'rating')::int, 0), COALESCE(f.data->>'comment', ''),
			COALESCE(f.meta->>'source', 'web'), COALESCE(f.meta->>'voter', ''), COALESCE(f.meta->>'chat_id', ''), f.created_at
		FROM feedback f LEFT JOIN "user" u ON u.id = f.user_id
		WHERE f.data->>'rating' IN ('1', '-1') ORDER BY f.created_at DESC LIMIT 200`)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, email, model, comment, source, voter, chat string
		var rating int
		var at time.Time
		if err := rows.Scan(&id, &name, &email, &model, &rating, &comment, &source, &voter, &chat, &at); err != nil {
			api.Fail(w, r, err)
			return
		}
		out = append(out, map[string]any{"id": id, "user_name": name, "user_email": email, "model_id": model, "rating": rating,
			"comment": comment, "source": source, "voter": voter, "chat_id": chat, "created_at": at.Unix()})
	}
	if err := rows.Err(); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out)
}
