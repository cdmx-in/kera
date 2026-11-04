package knowledge

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitMarkdownBreadcrumb(t *testing.T) {
	doc := "intro line\n# Payouts\nabout payouts\n## Statuses\nGO_AHEAD then PAID\n```\n# not a heading\n```\n### Hold\nheld\n## Fees\nfee text\n# Other\nx\n"
	secs := splitMarkdown(doc)
	var got [][]string
	for _, s := range secs {
		got = append(got, s.headers)
	}
	want := [][]string{nil, {"Payouts"}, {"Payouts", "Statuses"}, {"Payouts", "Statuses", "Hold"}, {"Payouts", "Fees"}, {"Other"}}
	if len(got) != len(want) {
		t.Fatalf("sections %v, want %v", got, want)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("section %d headers %v, want %v", i, got[i], want[i])
		}
	}
	if !strings.Contains(secs[2].text, "# not a heading") {
		t.Fatalf("fenced heading should stay inside its section: %q", secs[2].text)
	}
	if !strings.HasPrefix(secs[1].text, "# Payouts") {
		t.Fatalf("section keeps its heading line: %q", secs[1].text)
	}
	var b strings.Builder
	for _, s := range secs {
		b.WriteString(s.text)
	}
	if b.String() != doc {
		t.Fatal("sections must cover the whole document")
	}
}

func TestSplitTextSizeAndOverlap(t *testing.T) {
	var words []string
	for i := range 400 {
		words = append(words, "w"+strings.Repeat("x", i%7))
	}
	text := strings.Join(words, " ")
	sp := Splitter{Size: 100, Overlap: 20}
	chunks := sp.splitText(text)
	if len(chunks) < 10 {
		t.Fatalf("expected many chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if utf8.RuneCountInString(c) > 100 {
			t.Fatalf("chunk %d has %d runes", i, utf8.RuneCountInString(c))
		}
		if i > 0 {
			prev := strings.Fields(chunks[i-1])
			first := strings.Fields(c)[0]
			if !slices.Contains(prev[len(prev)/2:], first) {
				t.Fatalf("chunk %d should start inside the previous chunk's tail (overlap): %q / %q", i, chunks[i-1], c)
			}
		}
	}
	// Every word survives.
	joined := strings.Join(chunks, " ")
	for _, w := range []string{words[0], words[len(words)-1]} {
		if !strings.Contains(joined, w) {
			t.Fatalf("lost %q", w)
		}
	}
}

func TestSplitTextPrefersParagraphs(t *testing.T) {
	p1 := strings.Repeat("alpha ", 10)
	p2 := strings.Repeat("beta ", 10)
	chunks := Splitter{Size: 80, Overlap: 0}.splitText(p1 + "\n\n" + p2)
	if len(chunks) != 2 || !strings.HasPrefix(chunks[1], "beta") || strings.Contains(chunks[0], "beta") {
		t.Fatalf("want one chunk per paragraph, got %q", chunks)
	}
}

func TestSplitTextUnbreakable(t *testing.T) {
	chunks := Splitter{Size: 10, Overlap: 0}.splitText(strings.Repeat("é", 35))
	if len(chunks) != 4 || utf8.RuneCountInString(chunks[0]) != 10 || utf8.RuneCountInString(chunks[3]) != 5 {
		t.Fatalf("got %q", chunks)
	}
}

func TestSplitPagesAndEmbedText(t *testing.T) {
	sp := Splitter{Size: 1000, Overlap: 100, Markdown: true}
	cs := sp.Split("", []Page{{N: 1, Text: "# Title\nfirst page"}, {N: 2, Text: "second page"}})
	if len(cs) != 2 || cs[0].Page != 1 || cs[1].Page != 2 {
		t.Fatalf("got %+v", cs)
	}
	if got := cs[0].EmbedText(); got != "Title\n\n# Title\nfirst page" {
		t.Fatalf("embed text %q", got)
	}
	if cs[1].EmbedText() != "second page" {
		t.Fatal("no breadcrumb, no prefix")
	}
}

func TestPagesOf(t *testing.T) {
	text := "páge one" + "\n\n" + "two"
	meta := map[string]any{"pages": []any{map[string]any{"n": 1.0, "chars": 8.0}, map[string]any{"n": 2.0, "chars": 3.0}}}
	ps := pagesOf(text, meta)
	if len(ps) != 2 || ps[0].Text != "páge one" || ps[1].Text != "two" || ps[1].N != 2 {
		t.Fatalf("got %+v", ps)
	}
	if pagesOf("x", map[string]any{}) != nil {
		t.Fatal("no pages meta means one document")
	}
}
