package audit

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
)

func init() { app.Register("audit", Mount) }

// Mount puts the audit scope on every request, records admin changes (auth.Authenticator.AdminHook), serves
// the admin routes and starts the daily retention purge:
//
//	GET /api/v1/admin/audit          events, newest first, filtered and paged (?cursor=)
//	GET /api/v1/admin/audit/export   the same filters as CSV, at most exportMax rows
//
// Nothing updates or deletes an event except the purge.
func Mount(a *app.App) error {
	l := &Log{db: a.DB, trusted: a.Env.TrustedProxies}
	a.Server.Use(l.middleware)
	a.Auth.AdminHook = l.admin
	h := &handler{a: a}
	a.Server.Handle("GET /api/v1/admin/audit", a.Auth.Admin(http.HandlerFunc(h.list)))
	a.Server.Handle("GET /api/v1/admin/audit/export", a.Auth.Admin(http.HandlerFunc(h.export)))
	go purgeLoop(a)
	return nil
}

type handler struct{ a *app.App }

// Row is one event as the API returns it.
type Row struct {
	ID          string          `json:"id"`
	At          time.Time       `json:"at"`
	ActorID     string          `json:"actor_id"`
	ActorLabel  string          `json:"actor_label"`
	ActorKind   string          `json:"actor_kind"`
	Action      string          `json:"action"`
	TargetType  string          `json:"target_type"`
	TargetID    string          `json:"target_id"`
	TargetLabel string          `json:"target_label"`
	Outcome     string          `json:"outcome"`
	IP          string          `json:"ip"`
	UserAgent   string          `json:"user_agent"`
	Meta        json.RawMessage `json:"meta"`
}

const (
	pageDefault = 50
	pageMax     = 200
	exportMax   = 10_000
)

// filter turns the query string into a WHERE clause and its arguments:
//
//	actor    an actor id, or text in the actor's email or name; also the failed sign-ins against that user
//	         (they are stored with an anonymous actor and the attempted account as the target)
//	action   action prefixes, comma-separated ("auth.,user.")
//	target   a target id, or text in the target's name
//	outcome  outcomes, comma-separated ("denied,failed")
//	from, to RFC 3339 times or YYYY-MM-DD days (to is inclusive: the whole day)
//	q        text anywhere: actor, action, target, meta
func filter(r *http.Request) (string, []any, error) {
	q := r.URL.Query()
	var where []string
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	like := func(s string) string {
		return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s) + "%"
	}
	if v := strings.TrimSpace(q.Get("actor")); v != "" {
		a, l := arg(v), arg(like(v))
		where = append(where, fmt.Sprintf(`(actor_id = %[1]s OR actor_label ILIKE %[2]s OR (action = 'auth.sign_in_failed'
			AND (target_id = %[1]s OR target_label ILIKE %[2]s OR lower(target_label) = (SELECT lower(email) FROM "user" WHERE id = %[1]s))))`, a, l))
	}
	if v := list(q.Get("action")); len(v) > 0 {
		for i := range v {
			v[i] = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(v[i]) + "%"
		}
		where = append(where, "action LIKE ANY("+arg(v)+")")
	}
	if v := strings.TrimSpace(q.Get("target")); v != "" {
		where = append(where, fmt.Sprintf("(target_id = %s OR target_label ILIKE %s)", arg(v), arg(like(v))))
	}
	if v := list(q.Get("outcome")); len(v) > 0 {
		where = append(where, "outcome = ANY("+arg(v)+")")
	}
	for _, b := range []struct {
		name, op string
		end      bool
	}{{"from", ">=", false}, {"to", "<", true}} {
		if v := q.Get(b.name); v != "" {
			t, err := parseTime(v, b.end)
			if err != nil {
				return "", nil, api.Errf(http.StatusBadRequest, "bad_time", b.name+" must be an RFC 3339 time or a YYYY-MM-DD day")
			}
			where = append(where, "at "+b.op+" "+arg(t))
		}
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		p := arg(like(v))
		// ponytail: a scan of the filtered rows; retention keeps the table small. Add a trigram index if it is slow.
		where = append(where, fmt.Sprintf("(actor_label ILIKE %[1]s OR action ILIKE %[1]s OR target_label ILIKE %[1]s OR target_id ILIKE %[1]s OR meta::text ILIKE %[1]s)", p))
	}
	if v := q.Get("cursor"); v != "" {
		us, id, ok := strings.Cut(v, "_")
		n, err := strconv.ParseInt(us, 10, 64)
		if !ok || err != nil {
			return "", nil, api.Errf(http.StatusBadRequest, "bad_cursor", "cursor is not one this server gave")
		}
		where = append(where, fmt.Sprintf("(at, id) < (%s, %s)", arg(time.UnixMicro(n)), arg(id)))
	}
	if len(where) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(where, " AND "), args, nil
}

// list splits a comma-separated parameter, dropping blanks.
func list(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseTime reads an RFC 3339 time or a UTC day; end moves a day to the start of the next one.
func parseTime(s string, end bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err == nil && end {
		t = t.AddDate(0, 0, 1)
	}
	return t, err
}

const rowCols = `id, at, coalesce(actor_id, ''), actor_label, actor_kind, action, target_type, target_id, target_label, outcome, ip, user_agent, meta`

func (h *handler) query(ctx context.Context, where string, args []any, limit int) ([]Row, error) {
	rows, err := h.a.DB.Query(ctx, `SELECT `+rowCols+` FROM audit_event`+where+
		fmt.Sprintf(` ORDER BY at DESC, id DESC LIMIT %d`, limit), args...)
	if err != nil {
		return nil, fmt.Errorf("audit events: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Row, error) {
		var e Row
		err := row.Scan(&e.ID, &e.At, &e.ActorID, &e.ActorLabel, &e.ActorKind, &e.Action, &e.TargetType, &e.TargetID,
			&e.TargetLabel, &e.Outcome, &e.IP, &e.UserAgent, &e.Meta)
		e.At = e.At.UTC()
		return e, err
	})
}

// list answers {"events": [...], "next": cursor}; next is "" on the last page.
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	where, args, err := filter(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	limit := pageDefault
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, pageMax)
	}
	out, err := h.query(r.Context(), where, args, limit+1)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = strconv.FormatInt(last.At.UnixMicro(), 10) + "_" + last.ID
	}
	if out == nil {
		out = []Row{}
	}
	api.JSON(w, http.StatusOK, map[string]any{"events": out, "next": next})
}

// export streams the filtered events as CSV, newest first, at most exportMax rows, and records that it did.
func (h *handler) export(w http.ResponseWriter, r *http.Request) {
	where, args, err := filter(r)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	out, err := h.query(r.Context(), where, args, exportMax)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	var used []string
	for k := range r.URL.Query() {
		used = append(used, k)
	}
	Record(r.Context(), Event{Action: "audit.export", Meta: map[string]any{"rows": len(out), "filters": used}})
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="kera-audit-`+time.Now().UTC().Format("20060102")+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"at", "actor_kind", "actor_id", "actor", "action", "outcome", "target_type", "target_id", "target", "ip", "user_agent", "meta"})
	for _, e := range out {
		_ = cw.Write([]string{e.At.Format(time.RFC3339), e.ActorKind, cell(e.ActorID), cell(e.ActorLabel), e.Action, e.Outcome,
			cell(e.TargetType), cell(e.TargetID), cell(e.TargetLabel), cell(e.IP), cell(e.UserAgent), cell(string(e.Meta))})
	}
	cw.Flush()
}

// cell keeps a spreadsheet from reading a value as a formula (CSV injection): one that starts with = + - @ or
// a tab or carriage return gets a leading apostrophe.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// purge deletes the events older than the setting audit.retention_days (default 365; 0 or less keeps every
// event) and returns how many went.
func purge(ctx context.Context, a *app.App) (int64, error) {
	days := 365
	if err := a.Settings.Get(ctx, "audit.retention_days", &days); err != nil {
		return 0, err
	}
	if days <= 0 {
		return 0, nil
	}
	tag, err := a.DB.Exec(ctx, `DELETE FROM audit_event WHERE at < now() - make_interval(days => $1)`, days)
	if err != nil {
		return 0, fmt.Errorf("purge audit events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// purgeLoop runs purge a minute after start (out of the way of startup), then once a day.
// ponytail: no shutdown hook, like the other background loops; the process exit ends it.
func purgeLoop(a *app.App) {
	time.Sleep(time.Minute)
	for t := time.Tick(24 * time.Hour); ; <-t {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		n, err := purge(ctx, a)
		cancel()
		if err != nil {
			slog.Error("audit purge", "err", err)
		} else if n > 0 {
			slog.Info("audit purge", "deleted", n)
		}
	}
}
