package settings

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Admin > Help (web/src/features/admin/help/data.ts) and docs/how-it-answers.md name settings, defaults and fixed
// numbers. These tests keep them true: a key that is renamed or a default that changes fails here.

const helpData = "../../web/src/features/admin/help/data.ts"

var (
	helpEntry = regexp.MustCompile(`\{ key: '([^']+)', def: '([^']*)', where: '[^']*' \}`)
	helpFixed = regexp.MustCompile(`file: '([^']+)', snippet: '([^']+)'`)
	// a settings key in prose or code: rag.top_k, chat.context.clear_at, guardrails.pii ...
	helpKey = regexp.MustCompile(`\b(?:rag|chat|tools|trace|cowork|guardrails|tasks|audit|web)\.[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*`)
	spaces  = regexp.MustCompile(`\s+`)
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func shown(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return "(empty)"
		}
		return s
	}
	return string(raw)
}

func TestHelpDataDefaults(t *testing.T) {
	entries := helpEntry.FindAllStringSubmatch(readFile(t, helpData), -1)
	if len(entries) < 40 {
		t.Fatalf("found only %d settings in the help data", len(entries))
	}
	for _, m := range entries {
		raw, ok := Defaults[m[1]]
		if !ok {
			t.Errorf("%s is in the help data but not in defaults.go", m[1])
			continue
		}
		if got := shown(raw); got != m[2] {
			t.Errorf("%s: the help data says the default is %q, defaults.go has %q", m[1], m[2], got)
		}
	}
}

func TestHelpDataKeysExist(t *testing.T) {
	for _, path := range []string{helpData, "../../docs/how-it-answers.md"} {
		for _, k := range helpKey.FindAllString(readFile(t, path), -1) {
			if strings.HasSuffix(k, ".md") || strings.HasSuffix(k, ".go") {
				continue // a file name such as trace.md
			}
			if _, ok := Defaults[k]; !ok {
				t.Errorf("%s names %s, which is not in defaults.go", path, k)
			}
		}
	}
}

func TestHelpDataFixedNumbers(t *testing.T) {
	fixed := helpFixed.FindAllStringSubmatch(readFile(t, helpData), -1)
	if len(fixed) < 8 {
		t.Fatalf("found only %d fixed numbers in the help data", len(fixed))
	}
	for _, m := range fixed {
		src := spaces.ReplaceAllString(readFile(t, "../../"+m[1]), " ")
		if !strings.Contains(src, spaces.ReplaceAllString(m[2], " ")) {
			t.Errorf("%s no longer contains %q", m[1], m[2])
		}
	}
}
