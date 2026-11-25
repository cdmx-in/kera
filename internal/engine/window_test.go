package engine

import (
	"math"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/provider"
)

// C: the estimate follows what the server reports.
func TestCalibrateCharsPerToken(t *testing.T) {
	msgs := func(chars int) []provider.Message {
		return []provider.Message{{Role: "system", Content: strings.Repeat("s", chars/2)}, {Role: "user", Content: strings.Repeat("u", chars/2)}}
	}
	for _, c := range []struct {
		name   string
		chars  int
		prompt int
		before float64
		want   float64
	}{
		{"markdown measured on a local model", 52135, 15583 + 8, 0, 3.346},
		{"dense json and code", 28000, 10000 + 8, 0, 2.8},
		{"english prose is capped at the old four", 40000, 8000 + 8, 0, maxCharsPerToken},
		{"cjk text is floored", 9000, 9000 + 8, 0, minCharsPerToken},
		{"a small prompt says little: the default stays", 2000, 700, 0, 0},
		{"a server that counts only the uncached part is not trusted", 40000, 3000, 0, 0},
		{"a later report replaces an earlier one", 30000, 10000 + 8, 3.9, 3.0},
	} {
		b := &runBudget{cpt: c.before}
		b.calibrate(msgs(c.chars), c.prompt)
		if math.Abs(b.cpt-c.want) > 0.01 {
			t.Errorf("%s: chars per token %.3f, want %.3f", c.name, b.cpt, c.want)
		}
	}
	// the estimate for new messages uses it, and the default is 3.3, not 4
	b := &runBudget{}
	if got := b.tokens(3300); got != 1000 {
		t.Errorf("default: 3300 characters = %d tokens, want 1000", got)
	}
	b.cpt = 2.5
	if got, want := b.estimate([]provider.Message{{Content: strings.Repeat("x", 2500)}}), 1004; got != want {
		t.Errorf("calibrated estimate %d, want %d", got, want)
	}
	if got := estimateTokens([]turn{{msg: provider.Message{Content: strings.Repeat("x", 330)}}}); got != 104 {
		t.Errorf("between-turn estimate %d, want 104", got)
	}
}
