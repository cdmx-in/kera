package engine

import (
	"encoding/json"
	"strings"
	"time"
)

// The message state below mirrors the web app's reducer (web/src/features/chat/reduce.ts):
// what is persisted and what a snapshot carries is exactly what the client
// builds from the same events, so history and live streams render alike.

// FileRef is a file shown on a message.
type FileRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Type        string `json:"type,omitempty"` // file | image | collection
}

// Source is a citation.
type Source struct {
	ID      string  `json:"id,omitempty"`
	Name    string  `json:"name,omitempty"`
	Title   string  `json:"title,omitempty"`
	URL     string  `json:"url,omitempty"`
	FileID  string  `json:"fileId,omitempty"`
	Page    int     `json:"page,omitempty"`
	Snippet string  `json:"snippet,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

// StatusLine is one progress line of a tool step.
type StatusLine struct {
	ID          string `json:"id,omitempty"`
	Action      string `json:"action,omitempty"`
	Description string `json:"description"`
	Done        bool   `json:"done,omitempty"`
}

// Step is one tool call in a "steps" part.
type Step struct {
	CallID      string       `json:"callId"`
	Name        string       `json:"name"`
	Server      string       `json:"server,omitempty"`
	Args        any          `json:"args,omitempty"`
	Status      string       `json:"status"` // running | done | error
	Output      string       `json:"output,omitempty"`
	Files       []FileRef    `json:"files,omitempty"`
	URLs        []string     `json:"urls,omitempty"`
	StatusLines []StatusLine `json:"statusLines"`
}

// Part is one entry of message.parts: text, reasoning, steps, block or compaction.
type Part struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Done       bool            `json:"done,omitempty"`
	DurationMs int64           `json:"durationMs,omitempty"`
	Steps      []*Step         `json:"steps,omitempty"`
	Block      json.RawMessage `json:"block,omitempty"`
	State      string          `json:"state,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	Before     int             `json:"before,omitempty"`
	After      int             `json:"after,omitempty"`
	started    time.Time
}

// PendingCall is an unanswered ask_user or confirm.
type PendingCall struct {
	Kind         string          `json:"kind"` // ask_user | confirm
	CallID       string          `json:"callId"`
	Questions    json.RawMessage `json:"questions,omitempty"`
	Title        string          `json:"title,omitempty"`
	Message      string          `json:"message,omitempty"`
	ConfirmLabel string          `json:"confirmLabel,omitempty"`
	CancelLabel  string          `json:"cancelLabel,omitempty"`
}

// Usage is token usage.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// msgState is the assistant message being produced.
type msgState struct {
	Parts        []*Part
	Files        []FileRef
	Sources      []Source
	PendingCalls []PendingCall
	FollowUps    []string
	Usage        *Usage
	Status       string // streaming | done | error | stopped
	Error        string
}

func (s *msgState) last() *Part {
	if len(s.Parts) == 0 {
		return nil
	}
	return s.Parts[len(s.Parts)-1]
}

func (s *msgState) closeReasoning(now time.Time) {
	if p := s.last(); p != nil && p.Type == "reasoning" && !p.Done {
		p.Done = true
		if p.DurationMs == 0 && !p.started.IsZero() {
			p.DurationMs = now.Sub(p.started).Milliseconds()
		}
	}
}

func (s *msgState) delta(text string, now time.Time) {
	if text == "" {
		return
	}
	s.closeReasoning(now)
	if p := s.last(); p != nil && p.Type == "text" {
		p.Text += text
		return
	}
	s.Parts = append(s.Parts, &Part{Type: "text", Text: text})
}

func (s *msgState) reasoning(text string, now time.Time) {
	if p := s.last(); p != nil && p.Type == "reasoning" && !p.Done {
		p.Text += text
		return
	}
	if text != "" {
		s.Parts = append(s.Parts, &Part{Type: "reasoning", Text: text, started: now})
	}
}

func (s *msgState) step(callID string) *Step {
	for _, p := range s.Parts {
		for _, st := range p.Steps {
			if st.CallID == callID {
				return st
			}
		}
	}
	return nil
}

func (s *msgState) toolCall(st *Step, now time.Time) {
	s.closeReasoning(now)
	if p := s.last(); p != nil && p.Type == "steps" {
		p.Steps = append(p.Steps, st)
		return
	}
	s.Parts = append(s.Parts, &Part{Type: "steps", Steps: []*Step{st}})
}

func (s *msgState) runningStep() *Step {
	p := s.last()
	if p == nil || p.Type != "steps" {
		return nil
	}
	for i := len(p.Steps) - 1; i >= 0; i-- {
		if p.Steps[i].Status == "running" {
			return p.Steps[i]
		}
	}
	return nil
}

// status adds or replaces a line on the step (by line id); lines without a
// step are transient and not stored.
func (s *msgState) status(callID string, line StatusLine, urls []string, hidden bool) {
	if hidden || line.Description == "" || line.Action == "compaction" {
		return
	}
	st := s.step(callID)
	if callID == "" {
		st = s.runningStep()
	}
	if st == nil {
		return
	}
	replaced := false
	if line.ID != "" {
		for i := range st.StatusLines {
			if st.StatusLines[i].ID == line.ID {
				st.StatusLines[i], replaced = line, true
			}
		}
	}
	if !replaced {
		st.StatusLines = append(st.StatusLines, line)
	}
	for _, u := range urls {
		if !containsStr(st.URLs, u) {
			st.URLs = append(st.URLs, u)
		}
	}
}

func (s *msgState) addSources(src []Source) {
	key := func(x Source) string {
		for _, k := range []string{x.URL, x.FileID, x.ID, x.Name} {
			if k != "" {
				return k
			}
		}
		return ""
	}
	seen := map[string]bool{}
	for _, x := range s.Sources {
		seen[key(x)] = true
	}
	for _, x := range src {
		if k := key(x); !seen[k] {
			seen[k] = true
			s.Sources = append(s.Sources, x)
		}
	}
}

func (s *msgState) addFiles(fs []FileRef) {
	for _, f := range fs {
		dup := false
		for _, x := range s.Files {
			dup = dup || x.ID == f.ID
		}
		if !dup {
			s.Files = append(s.Files, f)
		}
	}
}

func (s *msgState) compaction(state, summary string, before, after int) {
	for _, p := range s.Parts {
		if p.Type == "compaction" {
			p.State, p.Summary, p.Before, p.After = state, coalesce(summary, p.Summary), max(before, p.Before), max(after, p.After)
			return
		}
	}
	s.Parts = append(s.Parts, &Part{Type: "compaction", State: state, Summary: summary, Before: before, After: after})
}

// finish closes reasoning and running steps and sets the final status.
func (s *msgState) finish(status string, now time.Time) {
	s.closeReasoning(now)
	for _, p := range s.Parts {
		for _, st := range p.Steps {
			if st.Status == "running" {
				st.Status = "done"
			}
		}
	}
	if s.Status == "streaming" || s.Status == "" {
		s.Status = status
	}
}

// Text is the message's markdown (text parts joined), as the client computes it.
func (s *msgState) Text() string {
	var b strings.Builder
	for _, p := range s.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
