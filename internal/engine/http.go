package engine

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/tools"
)

func (e *Engine) routes(a *app.App) {
	user := func(f http.HandlerFunc) http.Handler { return a.Auth.Required(f) } // CSRF: see auth.Credential
	for _, p := range []string{"/api/chat/completions", "/api/v1/chat/completions", "/v1/chat/completions"} {
		a.Server.Handle("POST "+p, user(e.completions))
	}
	a.Server.Handle("GET /api/v1/chats/{id}/messages/{mid}/stream", user(e.resume))
	a.Server.Handle("POST /api/v1/chats/{id}/messages/{mid}/cancel", user(e.cancelRun))
	a.Server.Handle("POST /api/v1/chats/{id}/messages/{mid}/calls/{callId}", user(e.answerCall))
	a.Server.Handle("GET /api/v1/chats/{id}/runs", user(e.chatRuns))
	a.Server.HandleFunc("POST /api/v1/runs/{run}/events", e.callbackEvents)
	a.Server.HandleFunc("POST /api/v1/runs/{run}/ask", e.callbackAsk)
	a.Server.HandleFunc("POST /api/v1/runs/{run}/files", e.callbackFile)
	a.Server.HandleFunc("GET /api/v1/runs/{run}/links", e.callbackLinks)
}

// completionError writes an error both OpenAI clients ({error:{message}})
// and Kera's API client ({detail, code}) understand.
func completionError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *api.Error
	if !errors.As(err, &ae) {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, ae.Status, map[string]any{"detail": ae.Message, "code": ae.Code,
		"error": map[string]any{"message": ae.Message, "code": ae.Code, "type": "invalid_request_error"}})
}

func (e *Engine) completions(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRequest(r)
	if err != nil {
		completionError(w, r, err)
		return
	}
	run, err := e.Start(r.Context(), auth.FromContext(r.Context()), req)
	if err != nil {
		completionError(w, r, err)
		return
	}
	switch {
	case req.StreamFormat == "events":
		streamNative(w, r, run, 0, false)
	case req.Stream:
		streamOpenAI(w, r, run, req.Model)
	default:
		respondOpenAI(w, r, run, req.Model)
	}
}

// ownRun finds the caller's run for /chats/{id}/messages/{mid}/...
func (e *Engine) ownRun(r *http.Request) *Run {
	run := e.runs.byMessage(r.PathValue("mid"))
	if run == nil || run.ChatID != r.PathValue("id") || run.UserID != auth.FromContext(r.Context()).ID {
		return nil
	}
	return run
}

// resume re-attaches to a message's stream after seq ?after= (or Last-Event-ID).
func (e *Engine) resume(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if after == 0 {
		after, _ = strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	}
	run := e.ownRun(r)
	if run != nil && run.Finished() {
		// a run that ended here does not speak for a message that has a run again (taken over, or regenerated)
		if live, err := e.busy(r.Context(), run.MessageID); err != nil || live {
			run = nil
		}
	}
	if run != nil {
		streamNative(w, r, run, after, true)
		return
	}
	e.resumeStored(w, r, after)
}

// resumeStored answers a resume for a run that is not in this process. While the message has a run row (waiting for
// an answer, about to be resumed, or on another instance) the stream is held open; otherwise it is the stored
// message as one snapshot, then done.
func (e *Engine) resumeStored(w http.ResponseWriter, r *http.Request, after int64) {
	u := auth.FromContext(r.Context())
	if _, err := e.Chats.Get(r.Context(), u.ID, r.PathValue("id")); err != nil {
		api.Fail(w, r, err)
		return
	}
	m, err := e.Chats.Message(r.Context(), r.PathValue("id"), r.PathValue("mid"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	if m.Status == "streaming" {
		live, err := e.busy(r.Context(), m.ID)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		if live {
			e.holdStream(w, r, m, after)
			return
		}
		// the run may have ended between the two reads
		if m, err = e.Chats.Message(r.Context(), m.ChatID, m.ID); err != nil {
			api.Fail(w, r, err)
			return
		}
	}
	snap := storedSnapshot(m, json.RawMessage("[]"))
	snap["seq"] = after + 1
	s := newSSE(w)
	if s.event(after+1, "snapshot", mustMarshal(snap, "{}")) != nil {
		return
	}
	if m.Status == "error" {
		_ = s.event(after+2, "error", mustMarshal(map[string]any{"type": "error", "seq": after + 2, "messageId": m.ID,
			"code": "interrupted", "message": coalesce(m.Error, "This answer was interrupted.")}, "{}"))
		after++
	}
	_ = s.event(after+2, "done", mustMarshal(map[string]any{"type": "done", "seq": after + 2, "messageId": m.ID}, "{}"))
}

// storedSnapshot is the stored message as a snapshot event.
func storedSnapshot(m chats.Message, pending json.RawMessage) map[string]any {
	entry := chats.EntryOf(m)
	return map[string]any{"type": "snapshot", "messageId": m.ID, "parts": chats.UIParts(entry.Parts),
		"files": entry.Files, "sources": chats.UISources(entry.Sources), "pendingCalls": pending, "status": m.Status}
}

// holdPoll is how often a held stream looks at the run's row and message.
// ponytail: two small queries per second for each held stream, and no token stream across instances;
// LISTEN/NOTIFY and a chunk table if either matters.
var holdPoll = time.Second

// holdStream keeps a browser attached to a reply whose run is elsewhere: it sends the stored message whenever it
// changes, follows the run when this instance claims it, and ends with done when the run's row is gone. Its events
// have no seq: the numbers belong to the attempts, and a browser skips what it thinks it has seen.
func (e *Engine) holdStream(w http.ResponseWriter, r *http.Request, m chats.Message, after int64) {
	s := newSSE(w)
	send := func(typ string, data map[string]any) bool {
		return s.unnumbered(typ, mustMarshal(data, "{}")) == nil
	}
	snapshot := func(m chats.Message) bool { return send("snapshot", storedSnapshot(m, orRaw(m.PendingCalls, "[]"))) }
	if !snapshot(m) {
		return
	}
	poll, ping := time.NewTicker(holdPoll), time.NewTicker(pingEvery)
	defer poll.Stop()
	defer ping.Stop()
	seen := m.UpdatedAt
	for {
		select {
		case <-r.Context().Done():
			return
		case <-e.drain.Done():
			return // the server waits for open requests before it exits; the browser reconnects
		case <-ping.C:
			if s.ping() != nil {
				return
			}
			continue
		case <-poll.C:
		}
		if run := e.ownRun(r); run != nil && !run.Finished() {
			follow(r, run, after, func(ev event) error { return s.event(ev.Seq, ev.Type, ev.Raw) }, s.ping)
			return // done, or the run left again and the browser reconnects
		}
		live, err := e.busy(r.Context(), m.ID) // the row first: it goes in the transaction that writes the final message
		if err != nil {
			return
		}
		now, err := e.Chats.Message(r.Context(), m.ChatID, m.ID)
		if err != nil {
			return
		}
		if !live || now.Status != "streaming" {
			ok := snapshot(now)
			if ok && now.Status == "error" {
				ok = send("error", map[string]any{"type": "error", "messageId": now.ID, "code": "interrupted",
					"message": coalesce(now.Error, "This answer was interrupted.")})
			}
			if ok {
				done := map[string]any{"type": "done", "messageId": now.ID}
				if now.Status == "stopped" {
					done["status"] = "stopped"
				}
				send("done", done)
			}
			return
		}
		if !now.UpdatedAt.Equal(seen) {
			if seen = now.UpdatedAt; !snapshot(now) {
				return
			}
		}
	}
}

func (e *Engine) cancelRun(w http.ResponseWriter, r *http.Request) {
	// the row first: it reaches a run on another instance, and one that nothing in memory holds
	stored, err := e.requestCancel(r.Context(), auth.FromContext(r.Context()).ID, r.PathValue("id"), r.PathValue("mid"))
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	run := e.ownRun(r)
	switch {
	case run != nil:
		run.stop(errCancelled)
	case stored:
		e.kick()
	default:
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "no running answer for this message"))
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (e *Engine) answerCall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value json.RawMessage `json:"value"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	if len(body.Value) == 0 {
		body.Value = json.RawMessage("null")
	}
	callID, mid := r.PathValue("callId"), r.PathValue("mid")
	run := e.ownRun(r)
	if run != nil && run.answer(callID, body.Value) == nil { // a call that is waiting in this process
		api.JSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	// otherwise the stored question of a parked run
	stored, err := e.hasRun(r.Context(), auth.FromContext(r.Context()).ID, r.PathValue("id"), mid)
	if err == nil && stored {
		err = e.Answer(r.Context(), mid, callID, body.Value)
	}
	switch {
	case err == nil && stored:
		api.JSON(w, http.StatusOK, map[string]any{"ok": true})
	case err != nil && !errors.Is(err, ErrNoSuchCall):
		api.Fail(w, r, err)
	case run == nil && !stored:
		api.Fail(w, r, api.Errf(http.StatusNotFound, "not_found", "no running answer for this message"))
	default:
		api.Fail(w, r, api.Errf(http.StatusNotFound, "no_such_call", "this question was already answered or withdrawn"))
	}
}

// chatRuns lists a chat's live and recent runs (reattach after a reload): those here, and those that only have a
// row (waiting for an answer, queued, or on another instance).
func (e *Engine) chatRuns(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	out := []map[string]any{}
	here := map[string]bool{}
	for _, run := range e.runs.forChat(r.PathValue("id")) {
		if run.UserID != u.ID {
			continue
		}
		here[run.ID] = true
		out = append(out, map[string]any{"run_id": run.ID, "message_id": run.MessageID, "model": run.ModelID,
			"seq": run.Seq(), "done": run.Finished(), "created_at": run.Created.Unix()})
	}
	rows, err := e.DB.Query(r.Context(), `SELECT id, message_id, coalesce(request->>'model', ''), created_at FROM run
		WHERE chat_id = $1 AND user_id = $2 ORDER BY created_at, id`, r.PathValue("id"), u.ID)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("list runs: %w", err))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, mid, model string
		var created time.Time
		if err := rows.Scan(&id, &mid, &model, &created); err != nil {
			api.Fail(w, r, fmt.Errorf("list runs: %w", err))
			return
		}
		if !here[id] {
			out = append(out, map[string]any{"run_id": id, "message_id": mid, "model": model, "seq": 0, "done": false,
				"created_at": created.Unix()})
		}
	}
	api.JSON(w, http.StatusOK, out)
}

// ---- callbacks from tool and hook servers --------------------------------------------

// signRunToken returns a bearer token scoped to one attempt of a run: "<run>.<user>.<exp>.<mac>". The MAC covers the
// attempt, so a tool server still working for an attempt that a restart ended cannot post into the next one.
func signRunToken(k *secret.Keys, runID, userID string, attempt int, exp time.Time) (string, error) {
	if k == nil {
		return "", errors.New("no keys")
	}
	msg := fmt.Sprintf("%s.%s.%d", runID, userID, exp.Unix())
	return msg + "." + base64.RawURLEncoding.EncodeToString(runMAC(k, msg, attempt)), nil
}

func runMAC(k *secret.Keys, msg string, attempt int) []byte {
	return k.SignCall([]byte(fmt.Sprintf("run:%s:%d", msg, attempt)))
}

// verifyRunToken checks a callback token for an attempt of runID.
func verifyRunToken(k *secret.Keys, runID string, attempt int, tok string) bool {
	i := strings.LastIndex(tok, ".")
	if i < 0 {
		return false
	}
	msg, sig := tok[:i], tok[i+1:]
	parts := strings.Split(msg, ".")
	if len(parts) != 3 || parts[0] != runID {
		return false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	want := base64.RawURLEncoding.EncodeToString(runMAC(k, msg, attempt))
	return hmac.Equal([]byte(sig), []byte(want))
}

func (e *Engine) callbackRun(w http.ResponseWriter, r *http.Request) *Run {
	id := r.PathValue("run")
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	run := e.runs.run(id)
	if run == nil || !verifyRunToken(e.Keys, id, run.attempt, strings.TrimSpace(tok)) {
		api.JSON(w, http.StatusUnauthorized, map[string]any{"detail": "invalid run token", "code": "unauthenticated"})
		return nil
	}
	return run
}

// callbackEvents accepts {type, data} with type status, sources, files or block.
func (e *Engine) callbackEvents(w http.ResponseWriter, r *http.Request) {
	run := e.callbackRun(w, r)
	if run == nil {
		return
	}
	var body struct {
		Type string `json:"type"`
		Data any    `json:"data"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	switch body.Type {
	case "status", "sources", "files", "block":
		e.applyEvent(run, "", body.Type, body.Data)
		api.JSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		api.Fail(w, r, api.Errf(http.StatusBadRequest, "bad_event", "type must be status, sources, files or block"))
	}
}

// maxCallbackWait caps the ask long-poll below proxy timeouts.
var maxCallbackWait = 240 * time.Second

// callbackAsk long-polls a question: {kind: confirm|choice|input, title,
// message, questions?, timeout_ms} -> {answer} or {answer: null, reason}.
func (e *Engine) callbackAsk(w http.ResponseWriter, r *http.Request) {
	run := e.callbackRun(w, r)
	if run == nil {
		return
	}
	var q tools.AskRequest
	if err := api.Decode(r, &q); err != nil {
		api.Fail(w, r, err)
		return
	}
	wait := maxCallbackWait
	if q.TimeoutMS > 0 && time.Duration(q.TimeoutMS)*time.Millisecond < wait {
		wait = time.Duration(q.TimeoutMS) * time.Millisecond
	}
	v, err := run.ask(r.Context(), pendingFor(q), wait)
	if err != nil {
		reason := "timeout"
		switch {
		case run.unattended:
			reason = "unattended" // a scheduled run: asking again will not help
		case r.Context().Err() != nil:
			reason = "disconnected"
		case run.Finished():
			reason = "run_ended"
		}
		api.JSON(w, http.StatusOK, map[string]any{"answer": nil, "reason": reason})
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"answer": v})
}

// userLink matches a URL in a user message (up to whitespace or a closing bracket/quote).
var userLink = regexp.MustCompile(`https?://[^\s<>()\[\]"'` + "`" + `]+`)

func (r *Run) setUserLinks(turns []turn) {
	var links []string
	for _, t := range turns {
		if t.msg.Role != "user" {
			continue
		}
		for _, l := range userLink.FindAllString(t.msg.Content, -1) {
			if !slices.Contains(links, l) {
				links = append(links, l)
			}
		}
	}
	r.mu.Lock()
	r.userLinks = links
	r.mu.Unlock()
}

// callbackLinks lists the URLs the user typed in this run's conversation -> {links}.
// A tool uses it as consent: it may open a file only when the user pasted its
// link (e.g. a hosted model copying the one Google deck the user linked). Only
// links, never message text, leave Kera.
func (e *Engine) callbackLinks(w http.ResponseWriter, r *http.Request) {
	run := e.callbackRun(w, r)
	if run == nil {
		return
	}
	run.mu.Lock()
	links := append([]string{}, run.userLinks...)
	run.mu.Unlock()
	api.JSON(w, http.StatusOK, map[string]any{"links": links})
}

// maxRunUploads caps the files one run may store through the callback.
// ponytail: a fixed count; make it a setting if long desktop tasks hit it.
const maxRunUploads = 500

// callbackFile stores the raw body as a file of the run's user (?name=, the
// Content-Type header is the type; files.max_mb caps the size) -> {id, name}.
// A tool returns the id in "images" or "files" of its result.
// ponytail: the files are not linked to the chat (chat_file would list them as
// attachments to list_chat_files, and deleting a chat only drops links, never
// files), so they outlive the chat; clean up by meta.source = "tool" + chat_id
// if storage matters.
func (e *Engine) callbackFile(w http.ResponseWriter, r *http.Request) {
	run := e.callbackRun(w, r)
	if run == nil {
		return
	}
	run.mu.Lock()
	run.uploads++
	over := run.uploads > maxRunUploads
	run.mu.Unlock()
	if over {
		api.Fail(w, r, api.Errf(http.StatusTooManyRequests, "too_many_files", fmt.Sprintf("This run already uploaded %d files", maxRunUploads)))
		return
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	f, err := e.Files.Create(r.Context(), run.UserID, r.URL.Query().Get("name"), ct, r.Body,
		map[string]any{"source": "tool", "run_id": run.ID, "chat_id": run.ChatID, "message_id": run.MessageID})
	if errors.Is(err, files.ErrTooLarge) {
		err = api.Errf(http.StatusRequestEntityTooLarge, "too_large", "File exceeds the upload size limit")
	}
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"id": f.ID, "name": f.Filename})
}
