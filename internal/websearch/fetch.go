package websearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// UserAgent is sent to search engines and fetched pages; several sites
// refuse library user agents.
const UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

const maxPageBytes = 5 << 20

// Page is a fetched page reduced to text.
type Page struct {
	URL   string
	Title string
	Text  string
}

// Fetch downloads rawURL with c and extracts its readable text.
func Fetch(ctx context.Context, c *http.Client, rawURL string) (Page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Page{}, errors.New("not an http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en;q=0.9")
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			return Page{}, ErrBlocked
		}
		return Page{}, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Page{}, fmt.Errorf("the site answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return Page{}, fmt.Errorf("read page: %w", err)
	}
	ct := resp.Header.Get("Content-Type")
	mt, _, _ := mime.ParseMediaType(ct)
	if mt == "" {
		mt, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	final := resp.Request.URL
	p := Page{URL: final.String()}
	switch {
	case mt == "text/html" || mt == "application/xhtml+xml":
		r, err := charset.NewReader(bytes.NewReader(body), ct)
		if err != nil {
			r = bytes.NewReader(body)
		}
		p.Title, p.Text = Extract(r, final)
	case strings.HasPrefix(mt, "text/") || mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		mt == "application/xml" || strings.HasSuffix(mt, "+xml"):
		p.Text = strings.ToValidUTF8(string(body), "")
	default:
		return Page{}, fmt.Errorf("cannot read %s content", mt)
	}
	return p, nil
}

// Extract returns the title and main text of an HTML page: the readability
// article when one is found, otherwise the text of the whole body.
func Extract(r io.Reader, pageURL *url.URL) (title, text string) {
	raw, _ := io.ReadAll(r)
	art, err := readability.FromReader(bytes.NewReader(raw), pageURL)
	if err == nil && art.Node != nil {
		text = NodeText(art.Node)
		title = strings.TrimSpace(art.Title)
	}
	if len(text) >= 200 {
		return title, text
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return title, text
	}
	if title == "" {
		title = strings.TrimSpace(textOf(find(doc, "title")))
	}
	body := find(doc, "body")
	if body == nil {
		body = doc
	}
	if t := NodeText(body); len(t) > len(text) {
		text = t
	}
	return title, text
}

var skipTags = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true,
	"iframe": true, "head": true, "nav": true, "footer": true, "button": true}

var blockTags = map[string]bool{"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "section": true, "article": true, "header": true, "pre": true,
	"blockquote": true, "table": true, "ul": true, "ol": true, "dd": true, "dt": true, "hr": true, "main": true}

// NodeText renders n as plain text: one line per block element, table cells
// separated by " | ", whitespace collapsed, blank lines squeezed.
func NodeText(n *html.Node) string {
	var b strings.Builder
	pre := 0
	flat := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			if pre > 0 {
				b.WriteString(n.Data)
			} else {
				b.WriteString(flat.Replace(n.Data)) // source line breaks inside a paragraph are not text breaks
			}
			return
		case html.ElementNode:
			if skipTags[n.Data] {
				return
			}
			if n.Data == "td" || n.Data == "th" {
				b.WriteString(" | ")
			}
			if blockTags[n.Data] {
				b.WriteString("\n")
			}
		}
		if n.Data == "pre" {
			pre++
			defer func() { pre-- }()
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[n.Data] {
			b.WriteString("\n")
		}
	}
	walk(n)
	var out []string
	blank := false
	for _, line := range strings.Split(b.String(), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		line = strings.TrimPrefix(line, "| ")
		if line == "" || line == "|" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func find(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, tag); f != nil {
			return f
		}
	}
	return nil
}

func textOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// truncate cuts s to max characters (runes) with a note.
func truncate(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n\n[Content truncated...]"
}
