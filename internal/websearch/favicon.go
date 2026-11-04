package websearch

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const (
	faviconMaxBytes = 32 << 10
	faviconTTL      = 24 * time.Hour
	faviconEntries  = 1024
)

var domainRe = regexp.MustCompile(`^(?i)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

// Favicons serves GET /api/v1/favicon?domain=: the site's icon fetched over
// the private-network guard, kept in a small in-memory LRU (misses included).
type Favicons struct {
	client *http.Client
	now    func() time.Time

	mu    sync.Mutex
	order *list.List // front = most recent; values are *icon
	byKey map[string]*list.Element
}

type icon struct {
	domain string
	ctype  string // "" = no icon
	data   []byte
	at     time.Time
}

// NewFavicons returns the handler; c must block private targets.
func NewFavicons(c *http.Client) *Favicons {
	return &Favicons{client: c, now: time.Now, order: list.New(), byKey: map[string]*list.Element{}}
}

func (f *Favicons) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain"))), ".")
	domain = strings.TrimPrefix(domain, "www.")
	if len(domain) > 253 || !domainRe.MatchString(domain) {
		http.Error(w, "invalid domain", http.StatusBadRequest)
		return
	}
	ic := f.get(r.Context(), domain)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if ic.ctype == "" {
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.Error(w, "no icon", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ic.ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	_, _ = w.Write(ic.data)
}

func (f *Favicons) get(ctx context.Context, domain string) *icon {
	f.mu.Lock()
	if el, ok := f.byKey[domain]; ok {
		ic := el.Value.(*icon)
		if f.now().Sub(ic.at) < faviconTTL {
			f.order.MoveToFront(el)
			f.mu.Unlock()
			return ic
		}
		f.order.Remove(el)
		delete(f.byKey, domain)
	}
	f.mu.Unlock()
	// ponytail: concurrent misses for one domain fetch twice; add singleflight if that ever matters.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ic := &icon{domain: domain, at: f.now()}
	ic.ctype, ic.data = f.fetch(ctx, domain)
	f.mu.Lock()
	defer f.mu.Unlock()
	if el, ok := f.byKey[domain]; ok {
		f.order.Remove(el)
	}
	f.byKey[domain] = f.order.PushFront(ic)
	for f.order.Len() > faviconEntries {
		old := f.order.Back()
		f.order.Remove(old)
		delete(f.byKey, old.Value.(*icon).domain)
	}
	return ic
}

// fetch tries /favicon.ico, then the icon the home page links to.
func (f *Favicons) fetch(ctx context.Context, domain string) (string, []byte) {
	base := &url.URL{Scheme: "https", Host: domain, Path: "/"}
	if ct, b, err := f.image(ctx, base.ResolveReference(&url.URL{Path: "/favicon.ico"}).String()); err == nil {
		return ct, b
	}
	href, final := f.linkedIcon(ctx, base.String())
	if href == "" {
		return "", nil
	}
	u, err := final.Parse(href)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", nil
	}
	ct, b, err := f.image(ctx, u.String())
	if err != nil {
		return "", nil
	}
	return ct, b
}

func (f *Favicons) do(ctx context.Context, u string, limit int64) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, errors.New(resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(b)) > limit {
		return nil, nil, errors.New("too large")
	}
	return resp, b, nil
}

// image fetches u and accepts it only when it is an image of at most 32 KB.
func (f *Favicons) image(ctx context.Context, u string) (string, []byte, error) {
	resp, b, err := f.do(ctx, u, faviconMaxBytes)
	if err != nil {
		return "", nil, err
	}
	ct := imageType(resp.Header.Get("Content-Type"), b)
	if ct == "" {
		return "", nil, errors.New("not an image")
	}
	return ct, b, nil
}

// imageType trusts the bytes for raster images; SVG has no magic number, so
// the declared type decides and the CSP sandbox defuses scripts in it.
func imageType(declared string, b []byte) string {
	sniffed := http.DetectContentType(b)
	if strings.HasPrefix(sniffed, "image/") {
		return sniffed
	}
	mt, _, _ := mime.ParseMediaType(declared)
	if mt == "image/svg+xml" && bytes.Contains(b, []byte("<svg")) {
		return mt
	}
	return ""
}

// linkedIcon reads the home page for <link rel="icon" href>.
func (f *Favicons) linkedIcon(ctx context.Context, home string) (string, *url.URL) {
	resp, b, err := f.do(ctx, home, 512<<10)
	if err != nil {
		return "", nil
	}
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return "", nil
	}
	var href string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if href != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "link" {
			for _, rel := range strings.Fields(strings.ToLower(attr(n, "rel"))) {
				if rel == "icon" || rel == "apple-touch-icon" {
					href = attr(n, "href")
					return
				}
			}
		}
		for c := n.FirstChild; c != nil && href == ""; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return href, resp.Request.URL
}
