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
// meta. ponytail: write-only for now; add the admin list/leaderboard routes
// when an Evaluations page exists.
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
	id, user := ids.New(), uid(r)
	var created, updated time.Time
	if err := h.s.DB.QueryRow(r.Context(), `INSERT INTO feedback (id, user_id, type, data, meta, snapshot)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at, updated_at`,
		id, user, in.Type, in.Data, in.Meta, in.Snapshot).Scan(&created, &updated); err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"id": id, "user_id": user, "version": 0, "type": in.Type,
		"data": in.Data, "meta": in.Meta, "snapshot": in.Snapshot, "created_at": created.Unix(), "updated_at": updated.Unix()})
}
