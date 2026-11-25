package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/tools"
)

// A read-only (test) run offers only the tools their server declares read-only: an unmarked tool is a write,
// so the model never sees it, the answer starts by saying which tools were left out, and the model is told it is a test.
func TestReadOnlyRunWithholdsToolsNotDeclaredReadOnly(t *testing.T) {
	h := newHarness(t)
	defer tools.Register(&fakeTools{})
	for _, c := range []struct {
		name     string
		provider *fakeTools
		readOnly bool
		wantTool bool // fake_tool ran, so its result reached the model
	}{
		{"unmarked tool in a normal run", &fakeTools{}, false, true},
		{"unmarked tool in a read-only run", &fakeTools{}, true, false},
		{"read-only tool in a read-only run", &fakeTools{readOnly: true}, true, true},
	} {
		tools.Register(c.provider)
		cid := h.taskChat(t)
		run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", ChatID: cid, ID: "a" + cid, Unattended: true, ReadOnly: c.readOnly,
			ToolIDs: []string{"fake-server"}, UserMessage: &UserMessage{ID: "q" + cid, Content: "use tool"}})
		if err != nil {
			t.Fatal(err)
		}
		evs := wait(t, run)
		h.e.Wait()
		m, _ := h.e.Chats.Message(context.Background(), cid, "a"+cid)
		if got := strings.HasPrefix(m.Content, "done: tool says hi"); got != c.wantTool || m.Status != "done" {
			t.Fatalf("%s: status %q content %q, tool ran = %v", c.name, m.Status, m.Content, got)
		}
		var raw strings.Builder
		for _, ev := range evs {
			raw.Write(ev.Raw)
		}
		note := "Test run: tools that may change things are not available (fake_tool)"
		said := strings.Contains(raw.String(), note)
		if said != (c.readOnly && !c.wantTool) || strings.Contains(m.Content, note) != said {
			t.Fatalf("%s: the note is in the stream = %v, in the stored answer = %v: %s", c.name, said, strings.Contains(m.Content, note), raw.String())
		}
	}
}

func TestReadOnlyRunKeepsTheBuiltInSetsThatChangeNothing(t *testing.T) {
	for id, want := range map[string]bool{"builtin:core": true, "builtin:web": true, "builtin:knowledge": true, "builtin:files": true,
		"builtin:code": true, "builtin:memory": false, "builtin:schedule": false, "some-server": false} {
		if testSets[id] != want {
			t.Errorf("%s: kept = %v, want %v", id, testSets[id], want)
		}
	}
}

func TestReadOnlySystemNote(t *testing.T) {
	h := newHarness(t)
	for _, readOnly := range []bool{true, false} {
		cid := h.taskChat(t)
		run, err := h.e.Start(context.Background(), h.u, &Request{Model: "m1", ChatID: cid, ID: "a" + cid, Unattended: true, ReadOnly: readOnly,
			UserMessage: &UserMessage{ID: "q" + cid, Content: "echo system"}})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, run)
		h.e.Wait()
		m, _ := h.e.Chats.Message(context.Background(), cid, "a"+cid)
		if got := strings.Contains(m.Content, "This is a test run: you can only read"); got != readOnly {
			t.Fatalf("readOnly=%v: system prompt has the note = %v: %q", readOnly, got, m.Content)
		}
	}
}

func TestWithheldNoteIsShortAndSorted(t *testing.T) {
	got := withheldNote([]string{"b", "a", "b", "c", "d", "e", "f", "g", "h", "i", "j"})
	if want := "Test run: tools that may change things are not available (a, b, c, d, e, f, g, h and 2 more)"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
