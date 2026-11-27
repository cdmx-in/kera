package engine

import (
	"fmt"
	"net/http"
	"time"
)

// pingEvery keeps proxies (Cloudflare cuts idle responses) from closing streams.
var pingEvery = 15 * time.Second

func sseHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
}

// sseWriter writes server-sent events and flushes after each one.
type sseWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func newSSE(w http.ResponseWriter) *sseWriter {
	sseHeaders(w)
	s := &sseWriter{w: w, rc: http.NewResponseController(w)}
	_ = s.rc.Flush()
	return s
}

func (s *sseWriter) event(seq int64, typ string, data []byte) error {
	if _, err := fmt.Fprintf(s.w, "id: %d\nevent: %s\ndata: %s\n\n", seq, typ, data); err != nil {
		return err
	}
	return s.rc.Flush()
}

// unnumbered is an event without a seq, which leaves the client's resume cursor where it is.
func (s *sseWriter) unnumbered(typ string, data []byte) error {
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", typ, data); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *sseWriter) data(data []byte) error {
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *sseWriter) ping() error {
	if _, err := fmt.Fprint(s.w, ": ping\n\n"); err != nil {
		return err
	}
	return s.rc.Flush()
}

// follow calls fn for every event of run after seq `after` until done, the
// client goes away (the run keeps going) or fn fails. ping runs every pingEvery.
func follow(r *http.Request, run *Run, after int64, fn func(event) error, ping func() error) {
	tick := time.NewTicker(pingEvery)
	defer tick.Stop()
	for {
		evs, wake, finished := run.next(after)
		for _, ev := range evs {
			if err := fn(ev); err != nil {
				return
			}
			after = ev.Seq
		}
		if finished && len(evs) == 0 {
			return
		}
		if len(evs) > 0 {
			continue // re-check: more may have arrived while writing
		}
		select {
		case <-wake:
		case <-tick.C:
			if err := ping(); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// streamNative writes the native event dialect. When snapshotFirst is set
// and the client has nothing yet, the current state goes out first as one
// snapshot (a reload resumes from it instead of replaying every delta).
func streamNative(w http.ResponseWriter, r *http.Request, run *Run, after int64, snapshotFirst bool) {
	s := newSSE(w)
	if snapshotFirst && after == 0 {
		run.mu.Lock()
		seq := run.seqLocked()
		if run.finished {
			seq-- // replay the final done event after the snapshot so the client finishes
		}
		var raw []byte
		if seq > run.base {
			payload := run.snapshotLocked()
			payload["type"], payload["seq"], payload["messageId"] = "snapshot", seq, run.MessageID
			if !run.Persisted {
				payload["chatId"] = run.ChatID
			}
			raw = mustMarshal(payload, "{}")
		}
		run.mu.Unlock()
		if raw != nil {
			if s.event(seq, "snapshot", raw) != nil {
				return
			}
			after = seq
		}
	}
	follow(r, run, after, func(ev event) error { return s.event(ev.Seq, ev.Type, ev.Raw) }, s.ping)
}
