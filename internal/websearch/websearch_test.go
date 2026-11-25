package websearch

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
	"github.com/cdmx-in/kera/internal/tools"
)

func TestBlocked(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "172.16.0.1": true, "172.31.0.1": true, "169.254.169.254": true,
		"100.64.0.1": true, "0.0.0.0": true, "::1": true, "fd00::1": true, "fe80::1": true, "::ffff:127.0.0.1": true,
		"8.8.8.8": false, "1.1.1.1": false, "2606:4700:4700::1111": false,
	} {
		if got := Blocked(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Blocked(%s) = %v", ip, got)
		}
	}
}

const article = `<html><head><title>Fallback title</title><script>var x=1</script></head><body>
<nav>Home | About</nav>
<article><h1>Real headline</h1>
<p>The first paragraph of a long article explains the topic in enough words that readability treats it as the main content of the page.</p>
<p>The second paragraph continues with more details, numbers like 42 and 7, and a conclusion that also counts toward the article length.</p>
<table><tr><th>Name</th><th>Price</th></tr><tr><td>Widget</td><td>10</td></tr></table>
</article><footer>Copyright</footer></body></html>`

func TestFetchAndGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, article)
		case "/short":
			fmt.Fprint(w, `<html><head><title>Tiny</title></head><body><div>Just one line.</div><script>x()</script></body></html>`)
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"a":1}`)
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, "%PDF-1.4")
		case "/redirect":
			http.Redirect(w, r, "/page", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if _, err := Fetch(ctx, NewClient(5*time.Second, false), srv.URL+"/page"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback not blocked: %v", err)
	}
	any := NewClient(5*time.Second, true)
	p, err := Fetch(ctx, any, srv.URL+"/redirect")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p.URL, "/page") || !strings.Contains(p.Text, "first paragraph") || !strings.Contains(p.Text, "Widget | 10") ||
		strings.Contains(p.Text, "var x") || strings.Contains(p.Text, "Copyright") {
		t.Fatalf("page = %+v", p)
	}
	if !strings.Contains(p.Text, "\n") {
		t.Fatal("paragraphs not separated")
	}
	p, err = Fetch(ctx, any, srv.URL+"/short")
	if err != nil || p.Title != "Tiny" || p.Text != "Just one line." {
		t.Fatalf("fallback = %+v %v", p, err)
	}
	if p, err = Fetch(ctx, any, srv.URL+"/json"); err != nil || p.Text != `{"a":1}` {
		t.Fatalf("json = %+v %v", p, err)
	}
	if _, err = Fetch(ctx, any, srv.URL+"/pdf"); err == nil {
		t.Fatal("pdf accepted")
	}
	if _, err = Fetch(ctx, any, srv.URL+"/missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404: %v", err)
	}
	if _, err = Fetch(ctx, any, "file:///etc/passwd"); err == nil {
		t.Fatal("file URL accepted")
	}
	if s := truncate("héllo world", 5); s != "héllo\n\n[Content truncated...]" {
		t.Fatalf("truncate = %q", s)
	}
}

const ddgHTML = `<div class="result results_links result--ad"><a class="result__a" href="https://duckduckgo.com/y.js?ad=1">Ad</a><a class="result__snippet">buy</a></div>
<div class="result"><h2><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&amp;rut=x">Example <b>A</b></a></h2>
<a class="result__snippet" href="#">Snippet <b>A</b></a></div>
<div class="result"><a class="result__a" href="https://example.org/b">B</a><div class="result__snippet">Snippet B</div></div>`

const ddgLite = `<table><tr><td><a rel="nofollow" href="https://lite.example/1" class='result-link'>Lite one</a></td></tr>
<tr><td class='result-snippet'>first</td></tr><tr><td><a href="https://lite.example/2" class='result-link'>Lite two</a></td></tr>
<tr><td class='result-snippet'>second</td></tr></table>`

func TestParseDDG(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(ddgHTML))
	hits := parseDDG(doc, "result__a", "result__snippet")
	want := []Hit{{"Example A", "https://example.com/a", "Snippet A"}, {"B", "https://example.org/b", "Snippet B"}}
	if fmt.Sprint(hits) != fmt.Sprint(want) {
		t.Fatalf("hits = %+v", hits)
	}
	doc, _ = html.Parse(strings.NewReader(ddgLite))
	if hits := parseDDG(doc, "result-link", "result-snippet"); len(hits) != 2 || hits[1].Snippet != "second" {
		t.Fatalf("lite = %+v", hits)
	}
}

func fakeEngines(t *testing.T) (*Engines, *atomic.Int32) {
	var htmlCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/html/":
			htmlCalls.Add(1)
			if r.UserAgent() != UserAgent || r.FormValue("q") == "" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `<div class="anomaly-modal">are you a robot?</div>`) // challenge page: no results
		case "/lite/":
			fmt.Fprint(w, ddgLite)
		case "/search": // searxng
			if r.URL.Query().Get("format") != "json" {
				http.Error(w, "format", 400)
				return
			}
			fmt.Fprint(w, `{"results":[{"url":"https://s.example/1","title":"S1","content":"c1"},{"url":"https://s.example/2","title":"S2","content":"c2"}]}`)
		case "/brave":
			if r.Header.Get("X-Subscription-Token") != "bk" {
				http.Error(w, "key", 401)
				return
			}
			fmt.Fprint(w, `{"web":{"results":[{"title":"<strong>Brave</strong> hit","url":"https://b.example","description":"about <strong>it</strong>"}]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return &Engines{HTTP: srv.Client(), DDGHTML: srv.URL + "/html/", DDGLite: srv.URL + "/lite/", BraveAPI: srv.URL + "/brave"}, &htmlCalls
}

func TestEngines(t *testing.T) {
	e, htmlCalls := fakeEngines(t)
	ctx := context.Background()
	hits, err := e.Search(ctx, EngineConfig{Engine: "duckduckgo"}, "kera", 1)
	if err != nil || len(hits) != 1 || hits[0].Title != "Lite one" || htmlCalls.Load() != 1 {
		t.Fatalf("ddg fallback = %+v %v", hits, err)
	}
	base := strings.TrimSuffix(e.DDGHTML, "/html/")
	hits, err = e.Search(ctx, EngineConfig{Engine: "searxng", URL: base}, "kera", 5)
	if err != nil || len(hits) != 2 || hits[1].Snippet != "c2" {
		t.Fatalf("searxng = %+v %v", hits, err)
	}
	hits, err = e.Search(ctx, EngineConfig{Engine: "brave", APIKey: "bk"}, "kera", 5)
	if err != nil || len(hits) != 1 || hits[0].Title != "Brave hit" || hits[0].Snippet != "about it" {
		t.Fatalf("brave = %+v %v", hits, err)
	}
	if _, err := e.Search(ctx, EngineConfig{Engine: "brave", APIKey: "wrong"}, "kera", 5); err == nil {
		t.Fatal("bad brave key accepted")
	}
	if _, err := e.Search(ctx, EngineConfig{Engine: "bing"}, "kera", 5); err == nil {
		t.Fatal("unknown engine accepted")
	}
}

func TestSearchSkipsHitsWithoutAHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The shape one SearXNG engine in the pinned release returns.
		fmt.Fprint(w, `{"results":[`+
			`{"url":"http:///go/v1/redirect","title":"engine artifact","content":"no host"},`+
			`{"url":"https://example.com/real","title":"Real","content":"has a host"}]}`)
	}))
	t.Cleanup(srv.Close)
	e := &Engines{HTTP: srv.Client()}
	hits, err := e.Search(context.Background(), EngineConfig{Engine: "searxng", URL: srv.URL}, "kera", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Link != "https://example.com/real" {
		t.Fatalf("hostless hit kept: %+v", hits)
	}
	// The filter is not SearXNG-specific: a relative or scheme-less link is
	// unusable whichever engine produced it.
	for _, link := range []string{"", "/relative", "example.com/no-scheme", "http:///nohost", "mailto:a@b"} {
		if hasHost(link) {
			t.Errorf("%q accepted as a usable link", link)
		}
	}
	if !hasHost("https://example.com/ok") {
		t.Error("an https URL with a host was rejected")
	}
}

func TestBudgets(t *testing.T) {
	b := &budgets{m: map[string]*budget{}}
	for i := range 3 {
		if !b.take("r1", "search", 3) {
			t.Fatalf("call %d refused", i)
		}
	}
	if b.take("r1", "search", 3) {
		t.Fatal("4th call allowed")
	}
	if !b.take("r1", "fetch", 3) || !b.take("r2", "search", 3) || !b.take("r1", "search", 0) {
		t.Fatal("budgets are not per run and kind")
	}
}

func TestProvider(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	keys, _ := secret.Derive(strings.Repeat("k", 32))
	st := settings.New(pool)
	p := New(st, keys)
	p.Engines, _ = fakeEngines(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, article) }))
	defer page.Close()
	for k, v := range map[string]any{"web.search.max_calls_per_turn": 1, "web.fetch.max_calls_per_turn": 2, "web.fetch.max_chars": 50} {
		if err := st.Set(ctx, k, v, true); err != nil {
			t.Fatal(err)
		}
	}
	u := &auth.User{ID: "u", Permissions: auth.Permissions{"features": map[string]any{"web_search": true}}}
	if specs, _ := p.Tools(ctx, u); len(specs) != 2 {
		t.Fatalf("specs = %v", specs)
	}
	if specs, _ := p.Tools(ctx, &auth.User{ID: "x"}); specs != nil {
		t.Fatal("tools without permission")
	}
	var events []tools.Event
	call := func(name string, args any) tools.Result {
		b, _ := json.Marshal(args)
		res, err := p.Execute(ctx, tools.Call{Name: name, Args: b, User: u, RunID: "run",
			Emit: func(e tools.Event) { events = append(events, e) }})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := call("search_web", map[string]any{"query": "kera"})
	if res.IsError || len(res.Sources) != 2 || !strings.Contains(res.Content, "lite.example/2") {
		t.Fatalf("search = %+v", res)
	}
	last := events[len(events)-1].Data.(map[string]any)
	if last["action"] != "web_search" || last["done"] != true || last["count"] != 2 || last["description"] != "Searched 2 sites" {
		t.Fatalf("status = %v", last)
	}
	if res := call("search_web", map[string]any{"query": "again"}); res.Content != BudgetSpent {
		t.Fatalf("budget = %+v", res)
	}
	res = call("fetch_url", map[string]any{"url": page.URL})
	if !res.IsError || !strings.Contains(res.Content, "private network") {
		t.Fatalf("private fetch = %+v", res)
	}
	if err := st.Set(ctx, "web.fetch.allow_private", true, false); err != nil {
		t.Fatal(err)
	}
	res = call("fetch_url", map[string]any{"url": page.URL})
	if res.IsError || !strings.Contains(res.Content, "[Content truncated...]") || len(res.Sources) != 1 || res.Sources[0].URL == "" {
		t.Fatalf("fetch = %+v", res)
	}
	if res := call("fetch_url", map[string]any{"url": page.URL}); res.Content != BudgetSpent {
		t.Fatalf("fetch budget = %+v", res)
	}
}

type rewrite struct{ target *url.URL }

func (r rewrite) RoundTrip(orig *http.Request) (*http.Response, error) {
	req := orig.Clone(orig.Context())
	req.Header.Set("X-Host", req.URL.Host)
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	resp, err := http.DefaultTransport.RoundTrip(req)
	if resp != nil {
		resp.Request = orig
	}
	return resp, err
}

func TestFavicons(t *testing.T) {
	ico := append([]byte{0, 0, 1, 0, 1, 0}, make([]byte, 100)...)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		host := r.Header.Get("X-Host")
		switch {
		case (host == "ico.example" || host == "late.example") && r.URL.Path == "/favicon.ico":
			_, _ = w.Write(ico)
		case host == "linked.example" && r.URL.Path == "/":
			fmt.Fprint(w, `<html><head><link rel="shortcut icon" href="/static/i.png"></head></html>`)
		case host == "linked.example" && r.URL.Path == "/static/i.png":
			_, _ = w.Write(png1x1())
		case host == "big.example" && r.URL.Path == "/favicon.ico":
			_, _ = w.Write(append(ico, make([]byte, faviconMaxBytes)...))
		case host == "bighome.example" && r.URL.Path == "/":
			fmt.Fprint(w, `<html><head><link rel="icon" href="/i.png"></head><body>`+strings.Repeat("x", 600<<10))
		case host == "bighome.example" && r.URL.Path == "/i.png":
			_, _ = w.Write(png1x1())
		case host == "html.example":
			fmt.Fprint(w, "<html>not an image</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	f := NewFavicons(&http.Client{Transport: rewrite{target}})
	now := time.Now()
	f.now = func() time.Time { return now }
	get := func(domain string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/favicon?domain="+url.QueryEscape(domain), nil))
		return rec
	}
	if rec := get("ico.example"); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/x-icon" || rec.Body.Len() != len(ico) {
		t.Fatalf("ico = %d %s", rec.Code, rec.Header())
	}
	n := hits.Load()
	get("www.ico.example") // same key, cached
	if hits.Load() != n {
		t.Fatal("cache miss on a cached domain")
	}
	if rec := get("linked.example"); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("linked = %d", rec.Code)
	}
	if rec := get("bighome.example"); rec.Code != 200 {
		t.Fatalf("icon linked from a large home page = %d", rec.Code)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel() // the browser left; the fetch still completes and is cached
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequestWithContext(gone, "GET", "/api/v1/favicon?domain=late.example", nil))
	if rec.Code != 200 {
		t.Fatalf("cancelled request = %d", rec.Code)
	}
	for _, d := range []string{"big.example", "html.example"} {
		if rec := get(d); rec.Code != 404 {
			t.Fatalf("%s = %d", d, rec.Code)
		}
	}
	n = hits.Load()
	get("html.example") // misses are cached too
	if hits.Load() != n {
		t.Fatal("negative result not cached")
	}
	now = now.Add(2 * time.Hour)
	get("html.example") // misses expire after an hour
	if hits.Load() == n {
		t.Fatal("expired miss not refetched")
	}
	n = hits.Load()
	now = now.Add(23 * time.Hour)
	get("ico.example")
	if hits.Load() == n {
		t.Fatal("expired entry not refetched")
	}
	for _, bad := range []string{"", "localhost", "a/b.com", "evil.com:8080", "-x.com", strings.Repeat("a", 64) + ".com"} {
		if rec := get(bad); rec.Code != 400 {
			t.Errorf("domain %q = %d", bad, rec.Code)
		}
	}
	// LRU bound
	for i := range faviconEntries + 5 {
		f.mu.Lock()
		f.byKey[fmt.Sprint(i)] = f.order.PushFront(&icon{domain: fmt.Sprint(i), at: now})
		f.mu.Unlock()
	}
	get("fresh.example")
	if f.order.Len() > faviconEntries || len(f.byKey) != f.order.Len() {
		t.Fatalf("lru size %d/%d", f.order.Len(), len(f.byKey))
	}
	// the production client refuses private targets
	g := NewFavicons(NewClient(time.Second, false))
	if ct, _ := g.fetch(context.Background(), "localhost.localdomain"); ct != "" {
		t.Fatal("private target fetched")
	}
}

func png1x1() []byte {
	b, _ := hex.DecodeString("89504e470d0a1a0a0000000d4948445200000001000000010806000000")
	return b
}
