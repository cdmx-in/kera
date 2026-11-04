package knowledge

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Chunk is one piece of a document ready to embed.
type Chunk struct {
	Text    string
	Page    int      // 1-based page for paged documents, 0 otherwise
	Headers []string // markdown breadcrumb (# .. ###) the chunk sits under
}

// EmbedText is what gets embedded: the breadcrumb gives a chunk from deep
// inside a section the context its own text lacks.
func (c Chunk) EmbedText() string {
	if len(c.Headers) == 0 {
		return c.Text
	}
	return strings.Join(c.Headers, " > ") + "\n\n" + c.Text
}

// Splitter cuts documents into chunks of at most Size characters (runes)
// with Overlap characters carried between neighbours.
type Splitter struct {
	Size     int
	Overlap  int
	Markdown bool // split on # .. ### headings first
}

// Page is one page of a paged document (PDF, PPTX); N is 1-based.
type Page struct {
	N    int
	Text string
}

// Split chunks a whole document; pages (when non-empty) are chunked one by one
// so every chunk knows its page.
func (s Splitter) Split(text string, pages []Page) []Chunk {
	if len(pages) == 0 {
		pages = []Page{{Text: text}}
	}
	var out []Chunk
	for _, p := range pages {
		secs := []section{{text: p.Text}}
		if s.Markdown {
			secs = splitMarkdown(p.Text)
		}
		for _, sec := range secs {
			for _, t := range s.splitText(sec.text) {
				out = append(out, Chunk{Text: t, Page: p.N, Headers: sec.headers})
			}
		}
	}
	return out
}

type section struct {
	headers []string
	text    string
}

var (
	headingRe = regexp.MustCompile(`^(#{1,3})[ \t]+(.+?)[ \t#]*$`)
	fenceRe   = regexp.MustCompile("^[ \t]*(```|~~~)")
)

// splitMarkdown starts a section at every level 1-3 heading outside code
// fences. A section keeps its heading line; headers holds the breadcrumb.
func splitMarkdown(text string) []section {
	var (
		out   []section
		stack [3]string
		cur   strings.Builder
		crumb []string
		fence bool
	)
	flush := func() {
		if strings.TrimSpace(cur.String()) != "" {
			out = append(out, section{headers: crumb, text: cur.String()})
		}
		cur.Reset()
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		if fenceRe.MatchString(line) {
			fence = !fence
		}
		if m := headingRe.FindStringSubmatch(strings.TrimRight(line, "\r\n")); m != nil && !fence {
			flush()
			lvl := len(m[1])
			stack[lvl-1] = strings.TrimSpace(m[2])
			for i := lvl; i < len(stack); i++ {
				stack[i] = ""
			}
			crumb = nil
			for _, h := range stack {
				if h != "" {
					crumb = append(crumb, h)
				}
			}
		}
		cur.WriteString(line)
	}
	flush()
	return out
}

var separators = []string{"\n\n", "\n", ". ", " ", ""}

// splitText is a recursive character splitter: it cuts on the coarsest
// separator present, recurses into pieces that are still too long, then
// packs pieces back together up to Size with Overlap carried over.
func (s Splitter) splitText(text string) []string {
	size, overlap := s.Size, s.Overlap
	if size <= 0 {
		size = 1000
	}
	if overlap < 0 || overlap >= size {
		overlap = 0
	}
	var out []string
	for _, c := range merge(pieces(text, separators, size), size, overlap) {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// pieces cuts text into parts no longer than size; joining them gives text back.
func pieces(text string, seps []string, size int) []string {
	if runes(text) <= size {
		return []string{text}
	}
	sep, rest := seps[len(seps)-1], []string{""}
	for i, sp := range seps {
		if sp == "" || strings.Contains(text, sp) {
			sep, rest = sp, seps[i+1:]
			break
		}
	}
	var parts []string
	if sep == "" {
		for _, r := range text {
			parts = append(parts, string(r))
		}
		return parts
	}
	for _, p := range strings.SplitAfter(text, sep) {
		if p == "" {
			continue
		}
		if runes(p) > size && len(rest) > 0 {
			parts = append(parts, pieces(p, rest, size)...)
		} else {
			parts = append(parts, p)
		}
	}
	return parts
}

// merge packs parts into chunks of at most size runes; each new chunk starts
// with the previous chunk's trailing parts that fit in overlap.
func merge(parts []string, size, overlap int) []string {
	var (
		out    []string
		cur    []string
		curLen int
	)
	for _, p := range parts {
		n := runes(p)
		if curLen+n > size && len(cur) > 0 {
			out = append(out, strings.Join(cur, ""))
			for len(cur) > 0 && (curLen > overlap || curLen+n > size) {
				curLen -= runes(cur[0])
				cur = cur[1:]
			}
		}
		cur = append(cur, p)
		curLen += n
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, ""))
	}
	return out
}
