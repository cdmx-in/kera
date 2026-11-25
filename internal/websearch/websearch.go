// Package websearch is the built-in web tool set ("builtin:web"): search_web
// over DuckDuckGo, SearXNG or Brave and fetch_url with readable-text
// extraction, per-turn call budgets and a private-network guard; plus the
// favicon proxy used by the sources row.
package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/tools"
)

// ID is the tool set id.
const ID = "builtin:web"

// BudgetSpent is the tool result once a per-turn budget is used up.
const BudgetSpent = "The budget for this tool in this answer is used up. Answer now from the results you already have."

func init() { app.Register("websearch", Mount) }

// Mount registers the provider and GET /api/v1/favicon.
func Mount(a *app.App) error {
	p := New(a.Settings, a.Keys)
	tools.Register(p)
	fav := NewFavicons(NewClient(10*time.Second, false))
	a.Server.Handle("GET /api/v1/favicon", a.Auth.Required(fav))
	return nil
}

// Provider implements tools.Provider for search_web and fetch_url.
type Provider struct {
	Settings *settings.Store
	Keys     *secret.Keys
	Engines  *Engines
	Public   *http.Client // fetch_url when private targets are blocked
	Any      *http.Client // fetch_url when web.fetch.allow_private is on
	budgets  *budgets
}

// New builds the provider with the public engine endpoints.
func New(st *settings.Store, keys *secret.Keys) *Provider {
	return &Provider{Settings: st, Keys: keys,
		Engines: DefaultEngines(&http.Client{Timeout: 20 * time.Second}),
		Public:  NewClient(20*time.Second, false), Any: NewClient(20*time.Second, true),
		budgets: &budgets{m: map[string]*budget{}}}
}

// ID implements tools.Provider.
func (p *Provider) ID() string { return ID }

var specs = []tools.Spec{{
	Name:        "search_web",
	Description: "Search the public web. Use it for current events, prices, products and anything that may be newer than your training data. Returns titles, links and snippets; read a promising page with fetch_url.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"The search query"},"count":{"type":"integer","description":"Number of results (optional, capped by the server)"}},"required":["query"]}`),
}, {
	Name:        "fetch_url",
	Description: "Fetch a web page and return its main text.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"The http(s) URL to read"}},"required":["url"]}`),
}}

// Tools implements tools.Provider.
func (p *Provider) Tools(ctx context.Context, u *auth.User) ([]tools.Spec, error) {
	if u == nil || !u.Can("features.web_search") {
		return nil, nil
	}
	return specs, nil
}

// Execute implements tools.Provider.
func (p *Provider) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	switch c.Name {
	case "search_web":
		return p.search(ctx, c)
	case "fetch_url":
		return p.fetch(ctx, c)
	}
	return errResult("unknown tool " + c.Name), nil
}

func errResult(msg string) tools.Result {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return tools.Result{Content: string(b), IsError: true}
}

func status(action, desc string, done bool, extra map[string]any) tools.Event {
	d := map[string]any{"action": action, "description": desc, "done": done}
	for k, v := range extra {
		d[k] = v
	}
	return tools.Event{Type: "status", Data: d}
}

func (p *Provider) intSetting(ctx context.Context, key string) int {
	var n int
	_ = p.Settings.Get(ctx, key, &n)
	return n
}

func (p *Provider) search(ctx context.Context, c tools.Call) (tools.Result, error) {
	var args struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(c.Args, &args); err != nil || strings.TrimSpace(args.Query) == "" {
		return errResult("the query argument is required"), nil
	}
	if !p.budgets.take(c.RunID, "search", p.intSetting(ctx, budgetKey(c, "web.search.max_calls_per_turn", "cowork.web.max_searches"))) {
		return tools.Result{Content: BudgetSpent}, nil
	}
	n := max(p.intSetting(ctx, "web.search.result_count"), 1)
	if args.Count > 0 && args.Count < n {
		n = args.Count
	}
	cfg := EngineConfig{}
	_ = p.Settings.Get(ctx, "web.search.engine", &cfg.Engine)
	_ = p.Settings.Get(ctx, "web.search.url", &cfg.URL)
	key, err := apply.ReadSecret(ctx, p.Settings, p.Keys, "web.search.api_key")
	if err != nil {
		return tools.Result{}, err
	}
	cfg.APIKey = key
	c.Emit(status("web_search", "Searching the web", false, map[string]any{"query": args.Query}))
	hits, err := p.Engines.Search(ctx, cfg, args.Query, n)
	if err != nil {
		c.Emit(status("web_search", "Search failed", true, map[string]any{"query": args.Query, "error": true}))
		return errResult("search failed: " + err.Error()), nil
	}
	urls := make([]string, 0, len(hits))
	sources := make([]tools.Source, 0, len(hits))
	for _, h := range hits {
		urls = append(urls, h.Link)
		sources = append(sources, tools.Source{Name: h.Title, URL: h.Link, Snippet: h.Snippet})
	}
	c.Emit(status("web_search", plural(len(hits), "Searched %d site", "Searched %d sites"), true, map[string]any{"query": args.Query, "urls": urls, "count": len(hits)}))
	b, _ := json.Marshal(hits)
	if len(hits) == 0 {
		b = []byte(`{"results":[],"note":"no results; try other words"}`)
	}
	return tools.Result{Content: string(b), Sources: sources}, nil
}

func (p *Provider) fetch(ctx context.Context, c tools.Call) (tools.Result, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(c.Args, &args); err != nil || strings.TrimSpace(args.URL) == "" {
		return errResult("the url argument is required"), nil
	}
	if !p.budgets.take(c.RunID, "fetch", p.intSetting(ctx, budgetKey(c, "web.fetch.max_calls_per_turn", "cowork.web.max_fetches"))) {
		return tools.Result{Content: BudgetSpent}, nil
	}
	host := args.URL
	if u, err := url.Parse(args.URL); err == nil && u.Host != "" {
		host = strings.TrimPrefix(u.Hostname(), "www.")
	}
	extra := map[string]any{"urls": []string{args.URL}}
	c.Emit(status("fetch_url", "Reading "+host, false, extra))
	client := p.Public
	if p.Settings.Bool(ctx, "web.fetch.allow_private") {
		client = p.Any
	}
	page, err := Fetch(ctx, client, args.URL)
	if err != nil {
		c.Emit(status("fetch_url", "Could not read "+host, true, map[string]any{"urls": []string{args.URL}, "error": true}))
		if errors.Is(err, ErrBlocked) {
			return errResult("this address is on a private network and cannot be fetched"), nil
		}
		return errResult(err.Error()), nil
	}
	c.Emit(status("fetch_url", "Read "+host, true, extra))
	text := truncate(page.Text, p.intSetting(ctx, "web.fetch.max_chars"))
	if text == "" {
		text = "(the page has no readable text)"
	}
	head := "URL: " + page.URL + "\n"
	if page.Title != "" {
		head = "Title: " + page.Title + "\n" + head
	}
	name := page.Title
	if name == "" {
		name = host
	}
	return tools.Result{Content: head + "\n" + text, Sources: []tools.Source{{Name: name, URL: page.URL}}}, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf(one, n)
	}
	return fmt.Sprintf(many, n)
}

// budgetKey is the setting that caps this call: a task (many tool rounds) has its own
// budget, a chat reply keeps the per-turn one.
func budgetKey(c tools.Call, chat, task string) string {
	if c.Task {
		return task
	}
	return chat
}

// budgets counts calls per run and kind.
type budgets struct {
	mu sync.Mutex
	m  map[string]*budget
}

type budget struct {
	n  int
	at time.Time
}

// take reports whether one more call fits in limit (limit <= 0 = unlimited).
func (b *budgets) take(runID, kind string, limit int) bool {
	if limit <= 0 || runID == "" {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.m) > 1000 { // ponytail: sweep on growth; runs end within minutes
		for k, v := range b.m {
			if time.Since(v.at) > time.Hour {
				delete(b.m, k)
			}
		}
	}
	key := runID + "|" + kind
	e := b.m[key]
	if e == nil {
		e = &budget{}
		b.m[key] = e
	}
	e.at = time.Now()
	if e.n >= limit {
		return false
	}
	e.n++
	return true
}
