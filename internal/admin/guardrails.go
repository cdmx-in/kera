package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
)

// guardDays reads ?days= (1..366, default 7) as a start time.
func guardDays(r *http.Request) (time.Time, error) {
	days := 7
	if s := r.URL.Query().Get("days"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 366 {
			return time.Time{}, api.Errf(http.StatusBadRequest, "bad_range", "days must be 1..366")
		}
		days = n
	}
	return time.Now().Add(-time.Duration(days) * day), nil
}

type guardEvent struct {
	ID        int64  `json:"id"`
	UserID    string `json:"user_id"`
	UserName  string `json:"user_name"`
	UserEmail string `json:"user_email"`
	ViaAPIKey bool   `json:"via_api_key"`
	ModelID   string `json:"model_id"`
	Rule      string `json:"rule"`
	Action    string `json:"action"`
	Count     int    `json:"count"`
	CreatedAt int64  `json:"created_at"`
}

// guardrailEvents lists the newest events (?days=7&limit=200).
func (h *handler) guardrailEvents(w http.ResponseWriter, r *http.Request) {
	since, err := guardDays(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	limit := 200
	if n, _ := strconv.Atoi(r.URL.Query().Get("limit")); n > 0 && n <= 1000 {
		limit = n
	}
	rows, err := h.a.DB.Query(r.Context(), `SELECT e.id, COALESCE(e.user_id, ''), COALESCE(u.name, ''), COALESCE(u.email, ''), e.via_api_key,
		e.model_id, e.rule, e.action, e.count, extract(epoch FROM e.created_at)::bigint
		FROM guardrail_event e LEFT JOIN "user" u ON u.id = e.user_id WHERE e.created_at >= $1 ORDER BY e.id DESC LIMIT $2`, since, limit)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("guardrail events: %w", err))
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (guardEvent, error) {
		var e guardEvent
		return e, row.Scan(&e.ID, &e.UserID, &e.UserName, &e.UserEmail, &e.ViaAPIKey, &e.ModelID, &e.Rule, &e.Action, &e.Count, &e.CreatedAt)
	})
	if err != nil {
		api.Fail(w, r, fmt.Errorf("guardrail events: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, nonNil(out))
}

type guardSummary struct {
	Rule   string `json:"rule"`
	Action string `json:"action"`
	Events int64  `json:"events"`
	Hits   int64  `json:"hits"`
}

// guardrailSummary counts events and hits per rule and action (?days=7).
func (h *handler) guardrailSummary(w http.ResponseWriter, r *http.Request) {
	since, err := guardDays(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	rows, err := h.a.DB.Query(r.Context(), `SELECT rule, action, count(*), sum(count)::bigint FROM guardrail_event
		WHERE created_at >= $1 GROUP BY 1, 2 ORDER BY 3 DESC`, since)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("guardrail summary: %w", err))
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (guardSummary, error) {
		var s guardSummary
		return s, row.Scan(&s.Rule, &s.Action, &s.Events, &s.Hits)
	})
	if err != nil {
		api.Fail(w, r, fmt.Errorf("guardrail summary: %w", err))
		return
	}
	api.JSON(w, http.StatusOK, nonNil(out))
}
