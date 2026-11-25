// Package audit is the record of who did what, when, to what: sign-ins, changes made through the admin API,
// stored secrets, pipelines, sharing. Admins read it under Admin > Audit; docs/audit.md says what is in it.
//
// A request records with Record(r.Context(), Event{...}). Every admin route that may change something (any
// method but GET, HEAD and OPTIONS) leaves one event without help: its action comes from the route (actions),
// its target from the path, its outcome from the status. A handler calls Record only to say more, and inside
// an admin route that call fills in the route's event instead of adding a second one.
//
// What never goes in: a value of a secret, token, password, API key or environment variable, and message or
// file content. Meta carries names and flags; keys that look like they hold a value are dropped (secretKey),
// and so are strings that look like a Kera credential.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

// Outcomes.
const (
	OK     = "ok"
	Denied = "denied" // refused: no permission, wrong credentials
	Failed = "failed" // tried and went wrong: a bad request, a conflict, an error
)

// Actor kinds.
const (
	KindUser      = "user"
	KindAPIKey    = "api_key"
	KindService   = "service"   // Kera acting on its own (an integration sync)
	KindSystem    = "system"    // the command line on the host
	KindAnonymous = "anonymous" // nobody signed in (a failed sign-in)
)

// Event is one thing that happened. Blank actor fields come from the request's user (or the With scope);
// a blank outcome is OK.
type Event struct {
	Action      string // stable dotted verb from the catalogue: "user.role_change"
	TargetType  string // defaults to the action's first word
	TargetID    string
	TargetLabel string // a name or email as it was at the time
	Outcome     string
	Meta        map[string]any // names, flags and counts only; see clean
	ActorID     string
	ActorLabel  string
	ActorKind   string
}

// Limits on what one row holds.
const (
	maxField   = 200
	maxAgent   = 300
	maxMeta    = 4096 // bytes of JSON; a larger meta is replaced by {"truncated": true}
	maxMetaStr = 200
	maxMetaLen = 50 // keys of one map, items of one list
)

// Log writes events to the database.
type Log struct {
	db      *pgxpool.Pool
	trusted []netip.Prefix // KERA_TRUSTED_PROXIES, for the client address
	limit   limiter
	now     func() time.Time
}

type ctxKey struct{}

// scope is what Record finds in a context: where to write, the request it belongs to (nil outside one) and,
// while an admin route runs, the event the route will write when its handler returns.
type scope struct {
	log         *Log
	req         *http.Request
	kind, label string // default actor outside a request (With)

	mu       sync.Mutex
	inAdmin  bool
	explicit *Event
}

// With returns ctx for code that runs outside a request (the command line, a background sync): events recorded
// with it are written to db, and their actor is kind and label unless the event names one.
func With(ctx context.Context, db *pgxpool.Pool, kind, label string) context.Context {
	return context.WithValue(ctx, ctxKey{}, &scope{log: &Log{db: db}, kind: kind, label: label})
}

// CLI is With for the command line on the host (kera apply, kera user ...): the operator is the actor.
func CLI(ctx context.Context, db *pgxpool.Pool) context.Context {
	return With(ctx, db, KindSystem, "Kera command line")
}

// Record saves e. It never fails the caller: a problem is logged and the request goes on. Without an audit
// scope in ctx (the feature is not mounted, as in some tests) it does nothing. Inside an admin route it
// replaces the route's own event (one per request: the last call wins), written when the handler returns.
func Record(ctx context.Context, e Event) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("audit: record panicked", "action", e.Action, "value", v)
		}
	}()
	s, _ := ctx.Value(ctxKey{}).(*scope)
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.inAdmin {
		s.explicit = &e
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.log.write(ctx, s, e)
}

// middleware puts the request's scope in its context.
func (l *Log) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, &scope{log: l, req: r})))
	})
}

// admin is auth.Authenticator.AdminHook: it records one event for every admin request that may change something.
func (l *Log) admin(w http.ResponseWriter, r *http.Request, next http.Handler) {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		next.ServeHTTP(w, r)
		return
	}
	s, _ := r.Context().Value(ctxKey{}).(*scope)
	if s == nil { // the middleware did not run (a handler built without it)
		s = &scope{log: l, req: r}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, s))
	}
	s.mu.Lock()
	s.inAdmin = true
	s.mu.Unlock()
	sw := &statusWriter{ResponseWriter: w}
	defer func() {
		v := recover()
		s.mu.Lock()
		e := Event{}
		if s.explicit != nil {
			e = *s.explicit
		}
		s.inAdmin, s.explicit = false, nil // a late Record (a goroutine the handler started) writes on its own
		s.mu.Unlock()
		status := sw.status
		if status == 0 && v != nil {
			status = http.StatusInternalServerError
		}
		fromRoute(&e, r, status)
		l.write(r.Context(), s, e)
		if v != nil {
			panic(v)
		}
	}()
	next.ServeHTTP(sw, r)
}

// fromRoute fills what e leaves blank from the route: the action from the catalogue (admin.request with the
// method and route for one it does not list), the target from the first path wildcard, the other wildcards
// as meta, the outcome from the status.
func fromRoute(e *Event, r *http.Request, status int) {
	meta := map[string]any{}
	if e.Action == "" {
		if e.Action = actions[r.Pattern]; e.Action == "" {
			method, route, _ := strings.Cut(r.Pattern, " ")
			e.Action, meta["method"], meta["route"] = "admin.request", method, route
		}
	}
	_, route, _ := strings.Cut(r.Pattern, " ")
	first := true
	for seg := range strings.SplitSeq(route, "/") {
		if len(seg) < 3 || seg[0] != '{' || seg[len(seg)-1] != '}' || seg == "{$}" {
			continue
		}
		name := strings.TrimSuffix(seg[1:len(seg)-1], "...")
		if first {
			first = false
			if e.TargetID == "" {
				e.TargetID = r.PathValue(name)
			}
			continue
		}
		meta[name] = r.PathValue(name)
	}
	if len(meta) > 0 {
		if e.Meta == nil {
			e.Meta = map[string]any{}
		}
		for k, v := range meta {
			if _, set := e.Meta[k]; !set {
				e.Meta[k] = v
			}
		}
	}
	if e.Outcome == "" {
		e.Outcome = outcomeOf(status)
	}
}

func outcomeOf(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Denied
	case status >= 400:
		return Failed
	}
	return OK // 0: the handler wrote nothing, which net/http sends as 200
}

// write fills the blanks of e and inserts it.
func (l *Log) write(ctx context.Context, s *scope, e Event) {
	if e.Outcome == "" {
		e.Outcome = OK
	}
	if e.ActorKind == "" && e.ActorID == "" && e.ActorLabel == "" {
		if u := auth.FromContext(ctx); u != nil {
			e.ActorID, e.ActorLabel, e.ActorKind = u.ID, u.Email, KindUser
			if e.ActorLabel == "" {
				e.ActorLabel = u.Name
			}
			if u.ViaAPIKey {
				e.ActorKind = KindAPIKey
			}
		} else {
			e.ActorKind, e.ActorLabel = s.kind, s.label
		}
	}
	if e.ActorKind == "" {
		e.ActorKind = KindAnonymous
		if e.ActorID != "" {
			e.ActorKind = KindUser
		}
	}
	if e.TargetType == "" {
		e.TargetType, _, _ = strings.Cut(e.Action, ".")
	}
	var ip, agent string
	if s.req != nil {
		ip, agent = api.RealIP(s.req, l.trusted), s.req.UserAgent()
	}
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if e.Outcome != OK && !l.limit.allow(ip, now) {
		return // logged once a minute by allow
	}
	meta, err := json.Marshal(clean(e.Meta, 0))
	if err != nil || len(meta) > maxMeta {
		meta = []byte(`{"truncated": true}`)
	}
	var actorID *string
	if e.ActorID != "" {
		id := clip(e.ActorID, maxField)
		actorID = &id
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := l.db.Exec(wctx, `INSERT INTO audit_event (id, at, actor_id, actor_label, actor_kind, action,
		target_type, target_id, target_label, outcome, ip, user_agent, meta) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		ids.New(), now, actorID, clip(e.ActorLabel, maxField), e.ActorKind, clip(e.Action, 64),
		clip(e.TargetType, 64), clip(e.TargetID, maxField), clip(e.TargetLabel, maxField), e.Outcome,
		clip(ip, 64), clip(agent, maxAgent), meta); err != nil {
		slog.Error("audit: event not recorded", "action", e.Action, "err", err)
	}
}

// secretWords are what a meta key ends with when it would hold something that must not be stored: a password,
// token, key, secret, credential or value, or message text (a plural "s" counts too). A key that only refers to
// one ends in another word and stays: "secret_ref" (a stored secret's name), "changed" (a list of field names).
var secretWords = []string{"password", "passwd", "pwd", "secret", "token", "key", "credential", "cookie",
	"authorization", "dsn", "value", "content", "body", "message", "prompt", "text"}

func secretKey(k string) bool {
	k = strings.TrimSuffix(strings.ToLower(k), "s")
	for _, w := range secretWords {
		if strings.HasSuffix(k, w) {
			return true
		}
	}
	return false
}

// credentialPrefixes start the credentials Kera issues (API keys, pipeline and runner tokens) and a bearer header.
var credentialPrefixes = []string{"sk-", "kp-", "kr-", "Bearer "}

// clean returns the meta to store: secret-looking keys dropped, strings that look like a credential replaced,
// strings, lists and maps cut to size, anything but strings, numbers, booleans, lists and maps left out.
func clean(m map[string]any, depth int) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if len(out) >= maxMetaLen || secretKey(k) {
			continue
		}
		if v = cleanValue(v, depth); v != nil {
			out[clip(k, 64)] = v
		}
	}
	return out
}

func cleanValue(v any, depth int) any {
	switch x := v.(type) {
	case string:
		for _, p := range credentialPrefixes {
			if strings.HasPrefix(x, p) {
				return "[redacted]"
			}
		}
		return clip(x, maxMetaStr)
	case bool, int, int64, float64:
		return x
	case []string:
		out := make([]any, 0, min(len(x), maxMetaLen))
		for _, s := range x[:min(len(x), maxMetaLen)] {
			out = append(out, cleanValue(s, depth))
		}
		return out
	case []any:
		out := make([]any, 0, min(len(x), maxMetaLen))
		for _, s := range x[:min(len(x), maxMetaLen)] {
			if c := cleanValue(s, depth); c != nil {
				out = append(out, c)
			}
		}
		return out
	case map[string]any:
		if depth >= 2 {
			return nil
		}
		return clean(x, depth+1)
	}
	return nil
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// limiter caps events whose outcome is not OK, per client address and in all, per minute, so a stream of failed
// sign-ins or refused calls cannot fill the table. What is over the cap is dropped (one log line a minute says so).
//
// ponytail: in memory and per process, reset when the minute turns; each replica allows the cap. Move it to the
// database if Kera runs as several replicas.
type limiter struct {
	mu      sync.Mutex
	minute  int64
	total   int
	per     map[string]int
	dropped bool
}

const (
	perAddress = 30  // non-OK events from one address per minute
	perMinute  = 600 // non-OK events in all per minute
)

func (l *limiter) allow(addr string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if m := now.Unix() / 60; m != l.minute || l.per == nil {
		l.minute, l.total, l.per, l.dropped = m, 0, map[string]int{}, false
	}
	if l.total < perMinute && l.per[addr] < perAddress {
		l.total++
		l.per[addr]++
		return true
	}
	if !l.dropped {
		l.dropped = true
		slog.Warn("audit: too many refused or failed events this minute; dropping the rest", "address", addr)
	}
	return false
}

// statusWriter remembers the status an admin handler sent.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flush keeps streaming responses working through the wrapper.
func (w *statusWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
