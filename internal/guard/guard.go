package guard

import (
	"context"
	"iter"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/provider"
)

// Mode is what a check does with a hit.
type Mode string

// Modes. Redact applies to PII only; Block ends the call with a 400.
const (
	Off    Mode = "off"
	Log    Mode = "log"
	Redact Mode = "redact"
	Block  Mode = "block"
)

// Policy is the merged guardrails configuration for one call.
type Policy struct {
	PII             Mode // input: messages, tool results, embedding texts
	OutputPII       Mode // model text: off | log | redact
	Injection       Mode // user turns: off | log | block
	Moderation      Mode // last user turn: off | log | block
	ModerationModel string
	LocalExempt     bool     // skip everything for local (Ollama) models
	Blocklist       []string // admin regexes on user turns, always block
}

// Event is one audit entry: a rule, what was done and how many hits.
type Event struct {
	Rule   string
	Action string
	Count  int
}

// Audit receives the events of one call (the context carries the user).
type Audit func(ctx context.Context, events []Event)

// Active reports whether any check is on.
func (p Policy) Active() bool {
	on := func(m Mode) bool { return m != "" && m != Off }
	return on(p.PII) || on(p.OutputPII) || on(p.Injection) || on(p.Moderation) || len(p.Blocklist) > 0
}

type client struct {
	inner     provider.Client
	p         Policy
	blocklist []*regexp.Regexp
	mod       Moderator
	audit     Audit
}

// Wrap returns inner guarded by p. Local models pass through when
// p.LocalExempt is set; so does an all-off policy.
func Wrap(inner provider.Client, p Policy, local bool, mod Moderator, audit Audit) provider.Client {
	if !p.Active() || (local && p.LocalExempt) {
		return inner
	}
	c := &client{inner: inner, p: p, mod: mod, audit: audit}
	for _, s := range p.Blocklist {
		re, err := regexp.Compile("(?i)" + s)
		if err != nil {
			slog.Warn("guardrails: blocklist pattern skipped", "pattern", s, "err", err)
			continue
		}
		c.blocklist = append(c.blocklist, re)
	}
	return c
}

func (c *client) ListModels(ctx context.Context) ([]string, error) { return c.inner.ListModels(ctx) }

func (c *client) emit(ctx context.Context, evs []Event) {
	if len(evs) > 0 && c.audit != nil {
		c.audit(ctx, evs)
	}
}

func blocked(rule string) error {
	return api.Errf(http.StatusBadRequest, "guardrail_blocked", "Blocked by the "+rule+" guardrail")
}

// scanPII applies the PII mode to one text: the (possibly redacted) text,
// the events and whether the call must stop.
func (c *client) scanPII(text string, events *[]Event) (string, bool) {
	if c.p.PII == "" || c.p.PII == Off {
		return text, false
	}
	red, hits := Scan(text)
	stop := false
	for rule, n := range hits {
		*events = append(*events, Event{Rule: rule, Action: string(c.p.PII), Count: n})
		if c.p.PII == Block {
			stop = true
		}
	}
	if c.p.PII == Redact {
		return red, false
	}
	return text, stop
}

// checkInput scans the conversation. Tool results count as input (they come
// from outside); system prompts are the operator's and are skipped.
func (c *client) checkInput(ctx context.Context, msgs []provider.Message) ([]provider.Message, []Event, error) {
	var events []Event
	var stop error
	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	lastUser := -1
	for i := range out {
		m := &out[i]
		if m.Role == "system" {
			continue
		}
		if m.Role == "user" {
			lastUser = i
			if c.p.Injection != "" && c.p.Injection != Off {
				if n := ScanInjection(m.Content); n > 0 {
					events = append(events, Event{Rule: "injection", Action: string(c.p.Injection), Count: n})
					if c.p.Injection == Block && stop == nil {
						stop = blocked("injection")
					}
				}
			}
			for _, re := range c.blocklist {
				if n := len(re.FindAllStringIndex(m.Content, -1)); n > 0 {
					events = append(events, Event{Rule: "blocklist", Action: string(Block), Count: n})
					if stop == nil {
						stop = blocked("blocklist")
					}
				}
			}
		}
		text, s := c.scanPII(m.Content, &events)
		m.Content = text
		if s && stop == nil {
			stop = blocked("pii")
		}
	}
	if stop == nil && lastUser >= 0 && (c.p.Moderation == Log || c.p.Moderation == Block) {
		if c.mod == nil {
			events = append(events, Event{Rule: "moderation.unavailable", Action: string(Log), Count: 1})
		} else if cats, err := c.mod(ctx, out[lastUser].Content); err != nil {
			slog.Warn("guardrails: moderation failed", "err", err)
			events = append(events, Event{Rule: "moderation.error", Action: string(Log), Count: 1})
		} else {
			for _, cat := range cats {
				events = append(events, Event{Rule: "moderation." + cat, Action: string(c.p.Moderation), Count: 1})
			}
			if len(cats) > 0 && c.p.Moderation == Block {
				stop = blocked("moderation")
			}
		}
	}
	return out, events, stop
}

// holdBack is how much model text stays buffered so a value split across
// deltas can still be masked. ponytail: fixed window; a value wider than
// this split across many deltas passes through.
const holdBack = 48

func (c *client) Stream(ctx context.Context, req provider.Request) iter.Seq2[provider.Event, error] {
	return func(yield func(provider.Event, error) bool) {
		msgs, events, err := c.checkInput(ctx, req.Messages)
		c.emit(ctx, events)
		if err != nil {
			yield(provider.Event{}, err)
			return
		}
		req.Messages = msgs
		redact := c.p.OutputPII == Redact
		logOnly := c.p.OutputPII == Log
		var pending, all strings.Builder
		outHits := map[string]int{}
		flush := func(final bool) bool {
			if pending.Len() == 0 {
				return true
			}
			text := pending.String()
			pending.Reset()
			if !final {
				cut := len(text) - holdBack
				if cut <= 0 {
					pending.WriteString(text)
					return true
				}
				cut = SafeCut(text, cut)
				for cut > 0 && !utf8.RuneStart(text[cut]) {
					cut--
				}
				pending.WriteString(text[cut:])
				text = text[:cut]
			}
			red, hits := Scan(text)
			for k, n := range hits {
				outHits[k] += n
			}
			if red == "" {
				return true
			}
			return yield(provider.Event{Kind: provider.EventText, Text: red}, nil)
		}
		for ev, err := range c.inner.Stream(ctx, req) {
			if err != nil {
				yield(ev, err)
				return
			}
			switch {
			case ev.Kind == provider.EventText && redact:
				pending.WriteString(ev.Text)
				if !flush(false) {
					return
				}
				continue
			case ev.Kind == provider.EventText && logOnly:
				all.WriteString(ev.Text)
			case redact:
				if !flush(true) {
					return
				}
			}
			if !yield(ev, nil) {
				return
			}
		}
		if redact {
			flush(true)
		}
		if logOnly {
			_, outHits = Scan(all.String())
		}
		var out []Event
		for k, n := range outHits {
			out = append(out, Event{Rule: "output." + k, Action: string(c.p.OutputPII), Count: n})
		}
		c.emit(ctx, out)
	}
}

func (c *client) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	var events []Event
	out := make([]string, len(texts))
	var stop bool
	for i, t := range texts {
		text, s := c.scanPII(t, &events)
		out[i] = text
		stop = stop || s
	}
	c.emit(ctx, events)
	if stop {
		return nil, blocked("pii")
	}
	return c.inner.Embed(ctx, model, out)
}
