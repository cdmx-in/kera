package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cdmx-in/kera/internal/guard"
)

// An earlier assistant turn is replayed as its text. The digest adds one short line per tool call it made, so a follow-up
// question ("use the second source", "now do the same for March") still knows what was done. It is built from the
// stored message parts, which keep only a clipped preview of each result.

const (
	digestSteps  = 15  // newest steps listed
	digestArgs   = 150 // characters of arguments per step
	digestResult = 300 // characters of result per step
)

// stepDigest returns the digest of a stored assistant message, or "" when it made no tool call.
// detail=false lists tool names only: arguments and results of tools can hold private data (mail, memory,
// internal documents), and a hosted model must not get from a digest what it never saw this turn.
// With detail, arguments and results are clipped and run through the guard's secret masking.
// ponytail: clipping and secret masking are the only filters here; personal data is left to the guardrails
// policy, which scans every assistant message sent to the model. Add a per-tool opt-out if a tool's output must never be replayed.
func stepDigest(parts json.RawMessage, detail bool) string {
	var ps []Part
	if json.Unmarshal(parts, &ps) != nil {
		return ""
	}
	var steps []*Step
	for _, p := range ps {
		if p.Type == "steps" {
			steps = append(steps, p.Steps...)
		}
	}
	if len(steps) == 0 {
		return ""
	}
	omitted := max(len(steps)-digestSteps, 0)
	var b strings.Builder
	b.WriteString("[Tool calls made for the reply above")
	if omitted > 0 {
		fmt.Fprintf(&b, " (the first %d are not listed)", omitted)
	}
	b.WriteString(":")
	for _, s := range steps[omitted:] {
		b.WriteString("\n- " + s.Name)
		if !detail {
			if s.Status == "error" {
				b.WriteString(" (failed)")
			}
			continue
		}
		fmt.Fprintf(&b, " %s", oneLine(stepArgs(s.Args), digestArgs))
		out := oneLine(citeRe.ReplaceAllString(s.Output, ""), digestResult) // source numbers belong to that run's list, not the next one's
		switch {
		case s.Status == "error":
			fmt.Fprintf(&b, " -> failed: %s", out)
		case out != "":
			fmt.Fprintf(&b, " -> %s", out)
		}
	}
	b.WriteString("]")
	return b.String()
}

// stepArgs renders a stored step's arguments.
func stepArgs(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// oneLine masks secrets, folds s onto one line and clips it.
func oneLine(s string, n int) string {
	s, _ = guard.MaskSecrets(s)
	return clip(strings.Join(strings.Fields(s), " "), n)
}
