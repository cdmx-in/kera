package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Hit is one search result.
type Hit struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
}

// Engines holds the endpoints of the search engines (overridable in tests).
type Engines struct {
	HTTP     *http.Client
	DDGHTML  string // DuckDuckGo HTML endpoint
	DDGLite  string // DuckDuckGo lite endpoint (fallback)
	BraveAPI string
}

// DefaultEngines are the public endpoints.
func DefaultEngines(c *http.Client) *Engines {
	return &Engines{HTTP: c, DDGHTML: "https://html.duckduckgo.com/html/", DDGLite: "https://lite.duckduckgo.com/lite/",
		BraveAPI: "https://api.search.brave.com/res/v1/web/search"}
}

// EngineConfig is the web.search.* settings an engine needs.
type EngineConfig struct {
	Engine string // duckduckgo | searxng | brave
	URL    string // searxng base URL
	APIKey string // brave subscription token
}

// Search runs query on the configured engine and returns at most n hits.
func (e *Engines) Search(ctx context.Context, cfg EngineConfig, query string, n int) ([]Hit, error) {
	var hits []Hit
	var err error
	switch cfg.Engine {
	case "", "duckduckgo":
		hits, err = e.ddg(ctx, e.DDGHTML, query, "result__a", "result__snippet")
		if err != nil || len(hits) == 0 {
			// the HTML endpoint answers bots with a challenge page now and then
			hits, err = e.ddg(ctx, e.DDGLite, query, "result-link", "result-snippet")
		}
	case "searxng":
		hits, err = e.searxng(ctx, cfg.URL, query)
	case "brave":
		hits, err = e.brave(ctx, cfg.APIKey, query, n)
	default:
		return nil, fmt.Errorf("unknown search engine %q", cfg.Engine)
	}
	if err != nil {
		return nil, err
	}
	// Some engines return hits whose URL has no host ("http:///go/v1/...");
	// Kera cannot open or cite those, so they would only waste a result slot
	// (issue #33).
	usable := hits[:0]
	for _, h := range hits {
		if hasHost(h.Link) {
			usable = append(usable, h)
		}
	}
	hits = usable
	if len(hits) > n {
		hits = hits[:n]
	}
	return hits, nil
}

// hasHost reports whether a hit's link is an absolute http(s) URL with a host.
func hasHost(link string) bool {
	u, err := url.Parse(link)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

func (e *Engines) get(req *http.Request) ([]byte, error) {
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Language", "en;q=0.9")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("search engine answered HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func (e *Engines) ddg(ctx context.Context, endpoint, query, linkClass, snippetClass string) ([]Hit, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(url.Values{"q": {query}, "kl": {""}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", endpoint)
	body, err := e.get(req)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	return parseDDG(doc, linkClass, snippetClass), nil
}

// parseDDG collects result links and the snippet that follows each one.
func parseDDG(doc *html.Node, linkClass, snippetClass string) []Hit {
	var hits []Hit
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch {
			case hasClass(n, linkClass):
				link := ddgTarget(attr(n, "href"))
				if link != "" && !seen[link] {
					seen[link] = true
					hits = append(hits, Hit{Title: textOf(n), Link: link})
				} else {
					hits = append(hits, Hit{}) // placeholder so its snippet is not misattributed
				}
				return
			case hasClass(n, snippetClass):
				if len(hits) > 0 && hits[len(hits)-1].Snippet == "" {
					hits[len(hits)-1].Snippet = textOf(n)
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	out := hits[:0]
	for _, h := range hits {
		if h.Link != "" {
			out = append(out, h)
		}
	}
	return out
}

// ddgTarget unwraps DuckDuckGo's redirect links and drops ads.
func ddgTarget(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") {
		if t := u.Query().Get("uddg"); t != "" && u.Path == "/l/" {
			return t
		}
		return "" // ads (y.js) and internal links
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return href
}

func (e *Engines) searxng(ctx context.Context, base, query string) ([]Hit, error) {
	if base == "" {
		return nil, errors.New("web.search.url is not set for searxng")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(base, "/")+"/search?"+url.Values{"q": {query}, "format": {"json"}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	body, err := e.get(req)
	if err != nil {
		return nil, err
	}
	var r struct {
		Results []struct{ URL, Title, Content string } `json:"results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	hits := make([]Hit, 0, len(r.Results))
	for _, x := range r.Results {
		hits = append(hits, Hit{Title: x.Title, Link: x.URL, Snippet: x.Content})
	}
	return hits, nil
}

func (e *Engines) brave(ctx context.Context, key, query string, n int) ([]Hit, error) {
	if key == "" {
		return nil, errors.New("web.search.api_key is not set for brave")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		e.BraveAPI+"?"+url.Values{"q": {query}, "count": {fmt.Sprint(min(max(n, 1), 20))}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	body, err := e.get(req)
	if err != nil {
		return nil, err
	}
	var r struct {
		Web struct {
			Results []struct{ Title, URL, Description string } `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	hits := make([]Hit, 0, len(r.Web.Results))
	for _, x := range r.Web.Results {
		hits = append(hits, Hit{Title: stripTags(x.Title), Link: x.URL, Snippet: stripTags(x.Description)})
	}
	return hits, nil
}

// stripTags removes the <strong> highlighting Brave puts in snippets.
func stripTags(s string) string {
	doc, err := html.Parse(strings.NewReader("<body>" + s))
	if err != nil {
		return s
	}
	return textOf(doc)
}
