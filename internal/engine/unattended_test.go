package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/tools"
)

// taskChat is a cowork chat, like a scheduled run's.
func (h *harness) taskChat(t *testing.T) string {
	t.Helper()
	c := chats.Chat{UserID: "u1", Mode: "cowork", Title: "A task"}
	if err := h.e.Chats.Create(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// An unattended run refuses a tool's question at once: the tool is not blocked
// for the 12 h a watched task waits, and nothing is left pending.
func TestUnattendedAskFailsAtOnce(t *testing.T) {
	h := newHarness(t)
	tools.Register(&fakeTools{ask: true})
	defer tools.Register(&fakeTools{})
	cid := h.taskChat(t)
	start := time.Now()
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", ChatID: cid, ID: "a1", Unattended: true,
		ToolIDs: []string{"fake-server"}, UserMessage: &UserMessage{ID: "q1", Content: "use tool"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := wait(t, run)
	h.e.Wait()
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the run waited for an answer: %v", time.Since(start))
	}
	if strings.Contains(types(evs), "confirm") {
		t.Fatalf("a confirm card was shown: %s", types(evs))
	}
	m, err := h.e.Chats.Message(context.Background(), cid, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "done" || m.Content != "done: no answer" || strings.Contains(string(m.PendingCalls), "call") {
		t.Fatalf("stored %q %q pending %s", m.Status, m.Content, m.PendingCalls)
	}
}

// A requested tool server that cannot join fails an unattended run with its name,
// where a watched run would simply go without it.
func TestUnattendedFailsOnMissingServer(t *testing.T) {
	h := newHarness(t)
	tools.Register(&fakeTools{})
	for ids, want := range map[string]string{"nope": "failed", "fake-server": "done"} {
		cid := h.taskChat(t)
		run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", ChatID: cid, ID: "a" + cid, Unattended: true,
			ToolIDs: []string{ids}, UserMessage: &UserMessage{ID: "q" + cid, Content: "hi"}})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, run)
		h.e.Wait()
		m, _ := h.e.Chats.Message(context.Background(), cid, "a"+cid)
		switch want {
		case "failed":
			if m.Status != "error" || !strings.Contains(m.Error, "nope") {
				t.Fatalf("missing server: status %q error %q", m.Status, m.Error)
			}
		default:
			if m.Status != "done" {
				t.Fatalf("working server: status %q error %q", m.Status, m.Error)
			}
		}
	}
	// the same request from a watched run goes on without the server
	cid := h.taskChat(t)
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", ChatID: cid, ID: "a" + cid, StreamFormat: "events",
		ToolIDs: []string{"nope"}, UserMessage: &UserMessage{ID: "q" + cid, Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	if m, _ := h.e.Chats.Message(context.Background(), cid, "a"+cid); m.Status != "done" {
		t.Fatalf("watched run: status %q error %q", m.Status, m.Error)
	}
}

// The preface is its own user turn before the goal, and its links are no consent.
func TestPrefaceIsAUserTurnAndGrantsNoLinks(t *testing.T) {
	h := newHarness(t)
	cid := h.taskChat(t)
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", ChatID: cid, ID: "a1", Unattended: true,
		Preface:     "notes: open https://evil.example/doc",
		UserMessage: &UserMessage{ID: "q1", Content: "echo users https://ok.example/mine"}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	h.e.Wait()
	m, _ := h.e.Chats.Message(context.Background(), cid, "a1")
	if !strings.HasPrefix(m.Content, "notes: open https://evil.example/doc\necho users") {
		t.Fatalf("model saw %q", m.Content)
	}
	run.mu.Lock()
	links := run.userLinks
	run.mu.Unlock()
	if !slices.Contains(links, "https://ok.example/mine") || slices.Contains(links, "https://evil.example/doc") {
		t.Fatalf("links %v", links)
	}
}

func TestUnattendedSystemNote(t *testing.T) {
	h := newHarness(t)
	for _, unattended := range []bool{true, false} {
		cid := h.taskChat(t)
		run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", ChatID: cid, ID: "a" + cid, Unattended: unattended,
			UserMessage: &UserMessage{ID: "q" + cid, Content: "echo system"}})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, run)
		h.e.Wait()
		m, _ := h.e.Chats.Message(context.Background(), cid, "a"+cid)
		if got := strings.Contains(m.Content, "This run is unattended"); got != unattended {
			t.Fatalf("unattended=%v: system prompt has the note = %v: %q", unattended, got, m.Content)
		}
	}
}

func TestUsableServersKeepsOnlyWorkingOnes(t *testing.T) {
	h := newHarness(t)
	tools.Register(&fakeTools{})
	got, err := h.e.UsableServers(context.Background(), h.u, "m1", []string{"server:fake-server", "nope", "builtin:code"})
	if err != nil || !slices.Equal(got, []string{"fake-server"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := h.e.UsableServers(context.Background(), h.u, "no-such-model", nil); err == nil {
		t.Fatal("an unknown assistant was accepted")
	}
}

// The time limit stops an unattended run that hangs, and the user can still read what it wrote.
func TestUnattendedTimeLimit(t *testing.T) {
	h := newHarness(t)
	old := unattendedUnit
	unattendedUnit = 100 * time.Millisecond
	defer func() { unattendedUnit = old }()
	if err := h.e.Settings.Set(context.Background(), "cowork.schedule.max_minutes", 1, false); err != nil {
		t.Fatal(err)
	}
	cid := h.taskChat(t)
	run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", ChatID: cid, ID: "a1", Unattended: true,
		UserMessage: &UserMessage{ID: "q1", Content: "slow"}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, run)
	h.e.Wait()
	m, _ := h.e.Chats.Message(context.Background(), cid, "a1")
	if m.Status != "error" || !strings.Contains(m.Error, "time limit") {
		t.Fatalf("status %q error %q", m.Status, m.Error)
	}
}
