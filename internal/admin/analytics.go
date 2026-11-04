package admin

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
)

const day = 24 * time.Hour

// dateRange is a span of whole UTC days, end inclusive.
type dateRange struct{ start, end time.Time }

func (d dateRange) until() time.Time { return d.end.Add(day) } // exclusive bound

// parseRange reads start_date and end_date (YYYY-MM-DD). When either is
// missing and required is false, the range is the 30 days up to today.
func parseRange(r *http.Request, required bool) (dateRange, error) {
	q := r.URL.Query()
	s, e := q.Get("start_date"), q.Get("end_date")
	if s == "" || e == "" {
		if required {
			return dateRange{}, api.Errf(http.StatusBadRequest, "bad_range", "start_date and end_date are required (YYYY-MM-DD)")
		}
		today := time.Now().UTC().Truncate(day)
		return dateRange{start: today.Add(-29 * day), end: today}, nil
	}
	start, err1 := time.Parse(time.DateOnly, s)
	end, err2 := time.Parse(time.DateOnly, e)
	if err1 != nil || err2 != nil {
		return dateRange{}, api.Errf(http.StatusBadRequest, "bad_range", "dates must be YYYY-MM-DD")
	}
	if end.Before(start) || end.Sub(start) > 366*day {
		return dateRange{}, api.Errf(http.StatusBadRequest, "bad_range", "range must be 1..366 days")
	}
	return dateRange{start: start, end: end}, nil
}

// tok reads a token count from message.usage, accepting the OpenAI, Responses
// and Ollama field names (imported history carries whichever the model sent).
func tok(fields ...string) string {
	s := "COALESCE("
	for _, f := range fields {
		s += fmt.Sprintf("CASE WHEN jsonb_typeof(m.usage->'%[1]s') = 'number' THEN (m.usage->>'%[1]s')::numeric::bigint END, ", f)
	}
	return s + "0)"
}

// events is one row per assistant reply in [$1, $2): every assistant message
// (user from its chat, tokens from usage rows that point at it, else from the
// message's own usage) plus usage rows of completions without a stored
// message (API calls, temporary chats).
var events = `WITH um AS (
	SELECT message_id, sum(prompt_tokens)::bigint AS pt, sum(completion_tokens)::bigint AS ct FROM usage
	WHERE message_id IS NOT NULL AND created_at >= $1::timestamptz - interval '1 day' AND created_at < $2::timestamptz + interval '1 day'
	GROUP BY message_id
), ev AS (
	SELECT c.user_id, m.model_id, m.created_at AS at, m.chat_id,
		COALESCE(um.pt, ` + tok("prompt_tokens", "input_tokens", "prompt_eval_count") + `) AS pt,
		COALESCE(um.ct, ` + tok("completion_tokens", "output_tokens", "eval_count") + `) AS ct
	FROM message m JOIN chat c ON c.id = m.chat_id LEFT JOIN um ON um.message_id = m.id
	WHERE m.role = 'assistant' AND m.created_at >= $1 AND m.created_at < $2
	UNION ALL
	SELECT u.user_id, u.model_id, u.created_at, u.chat_id, u.prompt_tokens::bigint, u.completion_tokens::bigint
	FROM usage u WHERE u.created_at >= $1 AND u.created_at < $2
		AND (u.message_id IS NULL OR NOT EXISTS (SELECT 1 FROM message x WHERE x.id = u.message_id AND x.role = 'assistant'))
) `

// query runs events + tail over the range and collects rows with scan.
func query[T any](ctx context.Context, h *handler, d dateRange, tail string, scan pgx.RowToFunc[T]) ([]T, error) {
	rows, err := h.a.DB.Query(ctx, events+tail, d.start, d.until())
	if err != nil {
		return nil, fmt.Errorf("analytics: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("analytics: %w", err)
	}
	return nonNil(out), nil
}

type summary struct {
	StartDate        string `json:"start_date"`
	EndDate          string `json:"end_date"`
	UsersTotal       int64  `json:"users_total"`
	ActiveUsers      int64  `json:"active_users"`
	Messages         int64  `json:"messages"`
	Chats            int64  `json:"chats"`
	Models           int64  `json:"models"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

func (h *handler) summary(w http.ResponseWriter, r *http.Request) {
	d, err := parseRange(r, false)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out, err := query(r.Context(), h, d, `SELECT (SELECT count(*) FROM "user"), count(DISTINCT user_id), count(*),
		count(DISTINCT chat_id), count(DISTINCT model_id), COALESCE(sum(pt), 0)::bigint, COALESCE(sum(ct), 0)::bigint FROM ev`,
		func(row pgx.CollectableRow) (summary, error) {
			s := summary{StartDate: d.start.Format(time.DateOnly), EndDate: d.end.Format(time.DateOnly)}
			return s, row.Scan(&s.UsersTotal, &s.ActiveUsers, &s.Messages, &s.Chats, &s.Models, &s.PromptTokens, &s.CompletionTokens)
		})
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out[0])
}

type dayRow struct {
	Day              string `json:"day"`
	Messages         int64  `json:"messages"`
	Users            int64  `json:"users"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

// daily returns every day of the range, empty days included.
func (h *handler) daily(w http.ResponseWriter, r *http.Request) {
	d, err := parseRange(r, false)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	got, err := query(r.Context(), h, d, `SELECT to_char(at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), count(*), count(DISTINCT user_id),
		sum(pt)::bigint, sum(ct)::bigint FROM ev GROUP BY 1`,
		func(row pgx.CollectableRow) (dayRow, error) {
			var x dayRow
			return x, row.Scan(&x.Day, &x.Messages, &x.Users, &x.PromptTokens, &x.CompletionTokens)
		})
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	byDay := map[string]dayRow{}
	for _, x := range got {
		byDay[x.Day] = x
	}
	out := []dayRow{}
	for t := d.start; !t.After(d.end); t = t.Add(day) {
		k := t.Format(time.DateOnly)
		x := byDay[k]
		x.Day = k
		out = append(out, x)
	}
	api.JSON(w, http.StatusOK, out)
}

type modelRow struct {
	Model            string `json:"model"`
	Messages         int64  `json:"messages"`
	Users            int64  `json:"users"`
	Chats            int64  `json:"chats"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

func (h *handler) byModel(w http.ResponseWriter, r *http.Request) {
	d, err := parseRange(r, false)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out, err := query(r.Context(), h, d, `SELECT model_id, count(*), count(DISTINCT user_id), count(DISTINCT chat_id),
		sum(pt)::bigint, sum(ct)::bigint FROM ev GROUP BY 1 ORDER BY 2 DESC, 1`,
		func(row pgx.CollectableRow) (modelRow, error) {
			var x modelRow
			return x, row.Scan(&x.Model, &x.Messages, &x.Users, &x.Chats, &x.PromptTokens, &x.CompletionTokens)
		})
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out)
}

type userRow struct {
	UserID           string `json:"user_id"`
	Name             string `json:"name"`
	Email            string `json:"email"`
	Messages         int64  `json:"messages"`
	Chats            int64  `json:"chats"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	LastAt           int64  `json:"last_at"` // epoch seconds of the last reply in range
}

func (h *handler) byUser(w http.ResponseWriter, r *http.Request) {
	d, err := parseRange(r, false)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out, err := query(r.Context(), h, d, `SELECT ev.user_id, COALESCE(u.name, ''), COALESCE(u.email, ''), count(*),
		count(DISTINCT ev.chat_id), sum(ev.pt)::bigint, sum(ev.ct)::bigint, extract(epoch FROM max(ev.at))::bigint
		FROM ev LEFT JOIN "user" u ON u.id = ev.user_id GROUP BY 1, 2, 3 ORDER BY 4 DESC, 1`,
		func(row pgx.CollectableRow) (userRow, error) {
			var x userRow
			return x, row.Scan(&x.UserID, &x.Name, &x.Email, &x.Messages, &x.Chats, &x.PromptTokens, &x.CompletionTokens, &x.LastAt)
		})
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, out)
}
