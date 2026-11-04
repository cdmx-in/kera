package files

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xuri/excelize/v2"
	"golang.org/x/net/html"
)

// extTypes covers what mime.TypeByExtension lacks in minimal images.
var extTypes = map[string]string{
	".txt": "text/plain; charset=utf-8", ".md": "text/markdown; charset=utf-8", ".markdown": "text/markdown; charset=utf-8",
	".csv": "text/csv; charset=utf-8", ".tsv": "text/tab-separated-values; charset=utf-8",
	".log": "text/plain; charset=utf-8", ".yaml": "application/yaml", ".yml": "application/yaml",
	".json": "application/json", ".xml": "application/xml", ".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xlsm": "application/vnd.ms-excel.sheet.macroEnabled.12",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".doc":  "application/msword", ".odt": "application/vnd.oasis.opendocument.text",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".svg": "image/svg+xml",
}

// DetectType picks a content type from the extension, then from the bytes.
func DetectType(name string, head []byte) string {
	ext := strings.ToLower(filepath.Ext(name))
	if t, ok := extTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return http.DetectContentType(head)
}

// textExts are read as UTF-8 text.
var textExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`txt md markdown csv tsv json jsonl xml yaml yml log ini toml cfg conf env properties
		go py js mjs cjs ts tsx jsx java kt kts scala c h cc cpp hpp cs rb rs php sh bash zsh ps1 bat sql r lua pl swift
		m dart css scss less vue svelte gradle tf hcl proto graphql rst tex org adoc srt vtt diff patch`) {
		textExts["."+e] = true
	}
}

// docExts go to the extractor sidecar.
var docExts = map[string]bool{".pdf": true, ".docx": true, ".pptx": true, ".doc": true, ".odt": true}

func ext(f File) string { return strings.ToLower(filepath.Ext(f.Filename)) }

func isImage(f File) bool {
	return strings.HasPrefix(f.ContentType, "image/") && !strings.HasPrefix(f.ContentType, "image/svg")
}

// extract returns the file's text and meta keys to merge.
func (s *Service) extract(ctx context.Context, f File) (string, map[string]any, error) {
	e := ext(f)
	switch {
	case isImage(f):
		return "", map[string]any{}, nil // vision models read images directly
	case e == ".html" || e == ".htm" || e == ".xhtml":
		b, err := os.ReadFile(s.abs(f))
		if err != nil {
			return "", nil, fmt.Errorf("read file: %w", err)
		}
		return clean(htmlText(b)), map[string]any{}, nil
	case e == ".xlsx" || e == ".xlsm":
		return s.xlsxText(f)
	case docExts[e]:
		return s.sidecar(ctx, f, e)
	}
	b, err := os.ReadFile(s.abs(f))
	if err != nil {
		return "", nil, fmt.Errorf("read file: %w", err)
	}
	if textExts[e] || strings.HasPrefix(http.DetectContentType(b), "text/") {
		return clean(string(b)), map[string]any{}, nil
	}
	if e == "" {
		e = f.ContentType
	}
	return "", nil, fmt.Errorf("unsupported file type %s", e)
}

// clean makes text storable: valid UTF-8, no BOM, no NUL (Postgres text rejects it).
func clean(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.TrimPrefix(s, "\uFEFF")
	return strings.ReplaceAll(s, "\x00", "")
}

var blockTags = map[string]bool{
	"p": true, "div": true, "li": true, "ul": true, "ol": true, "tr": true, "table": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "section": true, "article": true,
	"header": true, "footer": true, "blockquote": true, "pre": true, "hr": true, "title": true, "dt": true, "dd": true,
	"main": true, "nav": true, "aside": true, "figure": true, "figcaption": true, "form": true,
}

var skipTags = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true, "iframe": true}

var (
	spaceRun = regexp.MustCompile(`[ \t\f\r\v]+`)
	blankRun = regexp.MustCompile(`\n\s*\n(\s*\n)+`)
)

// htmlText keeps visible text with line breaks at block elements.
func htmlText(b []byte) string {
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return string(b)
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skipTags[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			sb.WriteString(spaceRun.ReplaceAllString(strings.ReplaceAll(n.Data, "\n", " "), " "))
		}
		if n.Type == html.ElementNode && n.Data == "br" {
			sb.WriteByte('\n')
			return
		}
		block := n.Type == html.ElementNode && blockTags[n.Data]
		if block {
			sb.WriteByte('\n')
		}
		if n.Type == html.ElementNode && (n.Data == "td" || n.Data == "th") {
			sb.WriteByte('\t')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if block {
			sb.WriteByte('\n')
		}
	}
	walk(doc)
	lines := strings.Split(sb.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	out := blankRun.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(out)
}

// xlsxLimits caps how far a workbook may inflate (excelize defaults to 16 GB).
var xlsxLimits = excelize.Options{UnzipSizeLimit: 1 << 30}

// xlsxText renders one "## Sheet: <name>" section per sheet with CSV rows of display values.
func (s *Service) xlsxText(f File) (string, map[string]any, error) {
	x, err := excelize.OpenFile(s.abs(f), xlsxLimits)
	if err != nil {
		return "", nil, fmt.Errorf("open workbook: %w", err)
	}
	defer x.Close()
	var sb strings.Builder
	sheets := x.GetSheetList()
	for i, name := range sheets {
		rows, err := x.GetRows(name)
		if err != nil {
			return "", nil, fmt.Errorf("read sheet %q: %w", name, err)
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "## Sheet: %s\n\n", name)
		w := csv.NewWriter(&sb)
		for _, r := range rows {
			_ = w.Write(r)
		}
		w.Flush()
	}
	return clean(sb.String()), map[string]any{"sheets": sheets}, nil
}

type extractResp struct {
	Pages []struct {
		N    int    `json:"n"`
		Text string `json:"text"`
	} `json:"pages"`
	Meta   map[string]any `json:"meta"`
	Detail any            `json:"detail"`
}

// sidecar posts the file to {rag.extractor.url}/extract.
func (s *Service) sidecar(ctx context.Context, f File, e string) (string, map[string]any, error) {
	var base string
	_ = s.settings.Get(ctx, "rag.extractor.url", &base)
	base = strings.TrimRight(base, "/")
	if base == "" {
		return "", nil, fmt.Errorf("no document extractor configured for %s", e)
	}
	src, err := os.Open(s.abs(f))
	if err != nil {
		return "", nil, fmt.Errorf("read file: %w", err)
	}
	defer src.Close()
	pr, pw := io.Pipe()
	defer pr.Close() // unblocks the writer if the request fails early
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", f.Filename)
		if err == nil {
			_, err = io.Copy(part, src)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/extract", pr)
	if err != nil {
		return "", nil, fmt.Errorf("extractor request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := s.client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("extractor: %w", err)
	}
	defer resp.Body.Close()
	var out extractResp
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err := json.Unmarshal(body, &out); err != nil && resp.StatusCode < 300 {
		return "", nil, fmt.Errorf("extractor: bad response: %w", err)
	}
	if resp.StatusCode >= 300 {
		msg := fmt.Sprint(out.Detail)
		if out.Detail == nil {
			msg = strings.TrimSpace(string(body[:min(len(body), 300)]))
		}
		return "", nil, fmt.Errorf("extractor: HTTP %d: %s", resp.StatusCode, msg)
	}
	texts := make([]string, 0, len(out.Pages))
	pages := make([]map[string]any, 0, len(out.Pages))
	for _, p := range out.Pages {
		t := clean(p.Text)
		texts = append(texts, t)
		pages = append(pages, map[string]any{"n": p.N, "chars": len([]rune(t))})
	}
	meta := map[string]any{"pages": pages}
	if t, ok := out.Meta["title"].(string); ok && t != "" {
		meta["title"] = t
	}
	return strings.Join(texts, "\n\n"), meta, nil
}
