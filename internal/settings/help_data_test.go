package settings

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Admin > Help (web/src/features/admin/help/data.ts), docs/how-it-answers.md and the diagrams in docs/images/how-it-answers-*.svg
// name settings, defaults and fixed numbers. These tests keep them true: a key that is renamed or a default that changes
// fails here. The labels, numbers and settings of each box in the diagrams are checked against the Help data in help.test.tsx.

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
	svgs, err := filepath.Glob("../../docs/images/how-it-answers-*.svg")
	if err != nil || len(svgs) != 6 {
		t.Fatalf("want 6 diagrams (3 x light and dark), found %d (%v)", len(svgs), err)
	}
	for _, path := range append([]string{helpData, "../../docs/how-it-answers.md"}, svgs...) {
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

// The diagrams are shown through <img>, which runs no scripts and loads nothing: keep them well-formed, self-contained and small.
func TestHelpDiagramFiles(t *testing.T) {
	svgs, _ := filepath.Glob("../../docs/images/how-it-answers-*.svg")
	banned := map[string]bool{"script": true, "foreignObject": true, "image": true, "a": true, "iframe": true}
	for _, path := range svgs {
		raw := readFile(t, path)
		if len(raw) > 60*1024 {
			t.Errorf("%s is %d bytes, over 60 KB", path, len(raw))
		}
		dec := xml.NewDecoder(bytes.NewReader([]byte(raw)))
		root := ""
		for {
			tok, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Errorf("%s is not well-formed XML: %v", path, err)
				break
			}
			switch v := tok.(type) {
			case xml.StartElement:
				if root == "" {
					root = v.Name.Local
				}
				if banned[v.Name.Local] {
					t.Errorf("%s has a <%s> element", path, v.Name.Local)
				}
				for _, a := range v.Attr {
					if strings.HasPrefix(a.Name.Local, "on") && a.Name.Space == "" {
						t.Errorf("%s has an event attribute %s", path, a.Name.Local)
					}
					if a.Name.Local == "href" && !strings.HasPrefix(a.Value, "#") {
						t.Errorf("%s links outside itself: href=%q", path, a.Value)
					}
					if a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
						if a.Value != "http://www.w3.org/2000/svg" {
							t.Errorf("%s declares the namespace %q", path, a.Value)
						}
						continue
					}
					if strings.Contains(a.Value, "://") {
						t.Errorf("%s has a URL in %s=%q", path, a.Name.Local, a.Value)
					}
				}
			case xml.CharData:
				if strings.Contains(string(v), "://") {
					t.Errorf("%s has a URL in its text", path)
				}
			case xml.Comment:
				if strings.Contains(string(v), "://") {
					t.Errorf("%s has a URL in a comment", path)
				}
			}
		}
		if root != "svg" {
			t.Errorf("%s: the root element is %q", path, root)
		}
		if !strings.Contains(raw, "prefers-reduced-motion: reduce") {
			t.Errorf("%s has no reduced-motion rule", path)
		}
		if strings.Contains(raw, "url(") && regexp.MustCompile(`url\((?:[^#]|$)`).MatchString(raw) {
			t.Errorf("%s loads something through url()", path)
		}
	}
}

// Every diagram the README and the doc point at exists, and the doc keeps a Mermaid text version under each.
func TestHelpDiagramsAreShown(t *testing.T) {
	ref := regexp.MustCompile(`(?:srcset|src)="((?:docs/)?images/how-it-answers-[a-z]+-(?:light|dark)\.svg)"`)
	for path, dir := range map[string]string{"../../README.md": "../../docs/", "../../docs/how-it-answers.md": "../../docs/"} {
		md := readFile(t, path)
		found := ref.FindAllStringSubmatch(md, -1)
		if len(found) < 4 {
			t.Errorf("%s shows %d diagram files, want at least 4", path, len(found))
		}
		for _, m := range found {
			name := strings.TrimPrefix(m[1], "docs/")
			if _, err := os.Stat(dir + name); err != nil {
				t.Errorf("%s points at %s, which does not exist", path, m[1])
			}
		}
		if strings.Contains(md, "```mermaid") != strings.HasSuffix(path, "how-it-answers.md") {
			t.Errorf("%s: Mermaid belongs in docs/how-it-answers.md only", path)
		}
	}
}
