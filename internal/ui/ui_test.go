package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/testdb"
)

const testYAML = `
site: {name: Site Name, default_models: [assistant], model_order: [assistant, other]}
brand:
  name: Example
  tagline: Ask anything
  logo: {light: /brand/logo.png, dark: /brand/logo.png}
  mark: /brand/mark.png
  favicon: /brand/icon.svg
  favicon_alt: [/brand/mark.png]
  pwa_icons: [/brand/mark.png]
  splash: {logo: /brand/mark.png, logo_dark: /brand/missing.png, wordmark: EXAMPLE, style: orbit}
  accent: {base: "#1e7f8c", bright: "#3dafc4"}
  grey: {hue: 215, chroma: 0.012}
  background: aurora
  login: {wordmark: EXAMPLE, style: glass-card}
  default_theme: dark
  tool_display:
    search_web: {label: web search, icon: search, kind: search, plural: web searches}
suggestions:
  - {title: [Plan, a trip], content: Plan a trip}
integrations:
  - {id: sheets, kind: oauthToolServer, tool_server_id: "mcp:sheets", label: Sheets, auto_enable: true, show_in_plus_menu: true}
chat: {memory: {enable: true}}
`

// 1x1 transparent PNG
var pngPixel = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

const shell = `<!doctype html><html><head><meta charset="utf-8"><!--kera:head--></head><body><div id="root"></div></body></html>`

func setup(t *testing.T) (http.Handler, string) {
	t.Helper()
	db := testdb.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	brand := filepath.Join(dir, "brand")
	must(t, os.Mkdir(brand, 0o755))
	must(t, os.WriteFile(filepath.Join(brand, "mark.png"), pngPixel, 0o644))
	must(t, os.WriteFile(filepath.Join(brand, "logo.png"), pngPixel, 0o644))
	must(t, os.WriteFile(filepath.Join(brand, "icon.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("nope"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(testYAML), 0o644))

	keys, err := secret.Derive(strings.Repeat("s", 32))
	must(t, err)
	st := settings.New(db)
	f, err := apply.Load(filepath.Join(dir, "config.yaml"))
	must(t, err)
	_, err = apply.Apply(ctx, db, keys, st, f, apply.Options{})
	must(t, err)

	env := config.Env{ConfigFile: filepath.Join(dir, "config.yaml")}
	srv, err := api.New(env, db)
	must(t, err)
	(&UI{settings: st, assets: &assets{dir: brandDir(env.ConfigFile, "")}, tmpl: []byte(shell)}).routes(srv)
	return srv.Handler(), brand
}

func sha8(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:8]
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestConfigMapping(t *testing.T) {
	h, _ := setup(t)
	w := get(h, "/api/v1/ui/config")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var c map[string]any
	must(t, json.Unmarshal(w.Body.Bytes(), &c))
	brand := c["brand"].(map[string]any)
	v := "?v=" + sha8(pngPixel)
	checks := map[string]any{
		"name": "Example", "tagline": "Ask anything", "background": "aurora", "defaultTheme": "dark",
		"mark": "/brand/mark.png" + v, "appleTouchIcon": nil,
	}
	for k, want := range checks {
		if brand[k] != want {
			t.Errorf("brand.%s = %v, want %v", k, brand[k], want)
		}
	}
	sp := brand["splash"].(map[string]any)
	if sp["logo"] != "/brand/mark.png"+v || sp["logoDark"] != "/brand/missing.png" || sp["wordmark"] != "EXAMPLE" {
		t.Errorf("splash = %v", sp)
	}
	if !strings.HasPrefix(brand["favicon"].(string), "/brand/icon.svg?v=") {
		t.Errorf("favicon = %v", brand["favicon"])
	}
	if brand["login"].(map[string]any)["style"] != "glass-card" || brand["accent"].(map[string]any)["bright"] != "#3dafc4" {
		t.Errorf("login/accent = %v", brand)
	}
	f := c["features"].(map[string]any)
	if f["memory"] != true || f["spend"] != false || f["temporaryChat"] != true {
		t.Errorf("features = %v", f)
	}
	if got := c["defaultModels"].([]any); len(got) != 1 || got[0] != "assistant" {
		t.Errorf("defaultModels = %v", got)
	}
	if td := c["toolDisplay"].(map[string]any)["search_web"].(map[string]any); td["plural"] != "web searches" {
		t.Errorf("toolDisplay = %v", td)
	}
	if in := c["integrations"].([]any)[0].(map[string]any); in["toolServerId"] != "mcp:sheets" || in["autoEnable"] != true {
		t.Errorf("integrations = %v", in)
	}
	if ps := c["promptSuggestions"].([]any)[0].(map[string]any); ps["content"] != "Plan a trip" {
		t.Errorf("promptSuggestions = %v", ps)
	}
}

func TestIndexInjection(t *testing.T) {
	h, _ := setup(t)
	w := get(h, "/c/some-chat")
	body := w.Body.String()
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("cache-control = %q", w.Header().Get("Cache-Control"))
	}
	for _, want := range []string{
		"<title>Example</title>",
		`<link rel="icon" href="/brand/icon.svg?v=`,
		`type="image/svg+xml" sizes="any">`,
		`<link rel="icon" href="/brand/mark.png?v=`,
		`<link rel="manifest" href="/manifest.json">`,
		"--accent:#1e7f8c;--accent-bright:#3dafc4",
		"--gray-50:oklch(0.985 0.003 215)", "--gray-950:oklch(0.16 0.008 215)",
		".kera-splash{",
		`(t!=="light"&&t!=="dark"&&t!=="system")t=t&&t.indexOf("dark")>-1?"dark":"dark"`,
		"window.__UI__={",
		`<div id="root"><div class="kera-splash kera-splash-orbit" data-splash`,
		`<div class="kera-splash-word">EXAMPLE</div></div></div>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index lacks %q", want)
		}
	}
}

func TestIndexEscaping(t *testing.T) {
	cfg := &Config{Brand: apply.Brand{Name: `</script><b>"x"`, Tagline: "a\u2028b", CustomCSS: "a{}</style><script>",
		Splash: &apply.Splash{Logo: `/brand/a.png"onerror=x`, Wordmark: "<i>W</i>"}}}
	out := string(renderIndex([]byte(shell), cfg))
	head, _, _ := strings.Cut(out, "<body>")
	if strings.Count(out, "</script>") != 2 { // theme + __UI__ scripts only
		t.Errorf("unexpected </script> in %s", out)
	}
	if _, script, _ := strings.Cut(out, "window.__UI__="); strings.Contains(script, "\u2028") {
		t.Error("raw U+2028 in the __UI__ script")
	}
	if strings.Count(head, "</style>") != 1 {
		t.Errorf("custom css closed the style element: %s", head)
	}
	for _, want := range []string{"<title>&lt;/script&gt;&lt;b&gt;&#34;x&#34;</title>", `\u003c/script\u003e`, `\u2028`,
		`src="/brand/a.png&#34;onerror=x"`, "&lt;i&gt;W&lt;/i&gt;"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	// no brand: no splash, no tokens, root left empty for React
	plain := string(renderIndex([]byte(shell), &Config{Brand: apply.Brand{Name: "Kera"}}))
	if !strings.Contains(plain, `<div id="root"></div>`) || strings.Contains(plain, "--accent:") {
		t.Errorf("unbranded shell: %s", plain)
	}
}

func TestTokens(t *testing.T) {
	scale := []string{"#f00", "#e00", "#d00", "#c00", "#b00", "#a00", "#900", "#800", "#700", "#600", "#500"}
	for _, tc := range []struct {
		b    apply.Brand
		want []string
	}{
		{apply.Brand{}, nil},
		{apply.Brand{Grey: &apply.Grey{Hue: 260, Chroma: 0.02}}, []string{"--gray-50:oklch(0.985 0.005 260)", "--gray-300:oklch(0.87 0.02 260)", "--gray-900:oklch(0.215 0.0167 260)"}},
		{apply.Brand{Grey: &apply.Grey{Scale: scale}}, []string{"--gray-50:#f00", "--gray-950:#500"}},
		{apply.Brand{Accent: &apply.Accent{Base: "red"}}, []string{":root{--accent:red}"}},
		// rows apply did not validate still cannot break out of the <style>
		{apply.Brand{Accent: &apply.Accent{Base: "red}</style><script>", Bright: "#3dafc4"}}, []string{":root{--accent-bright:#3dafc4}"}},
	} {
		got := tokens(&tc.b)
		if tc.want == nil && got != "" {
			t.Errorf("tokens(empty) = %q", got)
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("tokens = %q, lacks %q", got, w)
			}
		}
	}
}

func TestBrandAssets(t *testing.T) {
	h, brand := setup(t)
	v := sha8(pngPixel)
	for _, tc := range []struct {
		path, status, cache string
	}{
		{"/brand/mark.png?v=" + v, "200", "public, max-age=31536000, immutable"},
		{"/brand/mark.png?v=deadbeef", "200", "no-cache"},
		{"/brand/mark.png", "200", "no-cache"},
		{"/brand/icon.svg", "200", "no-cache"},
		{"/brand/..%2fsecret.txt", "404", ""},
		{"/brand/../secret.txt", "307", ""}, // the mux redirects to the cleaned /secret.txt
		{"/brand/%2e%2e%2fconfig.yaml", "404", ""},
		{"/brand/secret.txt", "404", ""},
		{"/brand/nope.png", "404", ""},
	} {
		w := get(h, tc.path)
		if got := w.Result().Status[:3]; got != tc.status {
			t.Errorf("%s: status %s, want %s", tc.path, got, tc.status)
			continue
		}
		if tc.cache != "" && w.Header().Get("Cache-Control") != tc.cache {
			t.Errorf("%s: cache %q, want %q", tc.path, w.Header().Get("Cache-Control"), tc.cache)
		}
	}
	if w := get(h, "/brand/icon.svg"); w.Header().Get("Content-Security-Policy") != "sandbox" || w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("svg headers = %v", w.Header())
	}
	// a symlink out of the brand dir is refused
	must(t, os.Symlink(filepath.Join(filepath.Dir(brand), "secret.txt"), filepath.Join(brand, "link.png")))
	if w := get(h, "/brand/link.png"); w.Code != 404 {
		t.Errorf("symlink escape: status %d", w.Code)
	}
}

func TestManifest(t *testing.T) {
	h, _ := setup(t)
	w := get(h, "/manifest.json")
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("cache = %q", w.Header().Get("Cache-Control"))
	}
	var m struct {
		Name  string         `json:"name"`
		Theme string         `json:"theme_color"`
		Icons []manifestIcon `json:"icons"`
	}
	must(t, json.Unmarshal(w.Body.Bytes(), &m))
	if m.Name != "Example" || m.Theme != "#1e7f8c" || len(m.Icons) != 1 || m.Icons[0].Sizes != "1x1" || m.Icons[0].Type != "image/png" {
		t.Errorf("manifest = %+v", m)
	}
}

func TestBrandDir(t *testing.T) {
	t.Setenv("KERA_BRAND_DIR", "")
	if d := brandDir("/etc/kera/config.yaml", "/data"); d != "/etc/kera/brand" {
		t.Errorf("with config: %s", d)
	}
	if d := brandDir("", "/data"); d != "/data/brand" {
		t.Errorf("without config: %s", d)
	}
	t.Setenv("KERA_BRAND_DIR", "/srv/brand")
	if d := brandDir("/etc/kera/config.yaml", "/data"); d != "/srv/brand" {
		t.Errorf("env: %s", d)
	}
}

func TestServeCache(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ext"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ext", "a.zip"), []byte("PK\x03\x04zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	get := func(rel string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		serveCache(rec, httptest.NewRequest("GET", "/cache/"+rel, nil), root, rel)
		return rec
	}
	if rec := get("ext/a.zip"); rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), `filename="a.zip"`) || rec.Body.String() != "PK\x03\x04zip" {
		t.Fatalf("zip: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	for _, bad := range []string{"../secret.txt", "ext/../../secret.txt", "ext", "", ".hidden", "ext/missing.zip"} {
		if rec := get(bad); rec.Code != 404 {
			t.Errorf("%q: %d", bad, rec.Code)
		}
	}
}

// tool_servers[].display.tools reach toolDisplay as "<server>_<fn>" and the
// bare name, each only when nothing named it before (ui.tool_display wins).
func TestToolServerDisplayMerged(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	st := settings.New(db)
	must(t, st.Set(ctx, "ui.tool_display", map[string]any{"search_web": map[string]any{"label": "web search"},
		"beta_search_kb": map[string]any{"label": "admin beta"}}, true))
	for _, r := range [][3]string{
		{"alpha", "0", `{"label":"KB","tools":{"search_kb":{"label":"KB search","kind":"search"},"search_web":{"label":"server web"}}}`},
		{"beta", "1", `{"tools":{"search_kb":{"label":"beta search"}}}`},
	} {
		_, err := db.Exec(ctx, `INSERT INTO tool_server (id, kind, url, sort, display) VALUES ($1, 'openapi', 'http://x', $2::int, $3::jsonb)`, r[0], r[1], r[2])
		must(t, err)
	}
	_, err := db.Exec(ctx, `INSERT INTO tool_server (id, kind, url, enabled, display) VALUES ('off', 'mcp', 'http://x', false, '{"tools":{"gone":{"label":"no"}}}')`)
	must(t, err)
	cfg, _, err := (&UI{settings: st, db: db, assets: &assets{dir: t.TempDir()}, tmpl: []byte(shell)}).current(ctx)
	must(t, err)
	td := cfg.ToolDisplay
	for k, want := range map[string]string{"search_web": "web search", "alpha_search_web": "server web", "search_kb": "KB search",
		"alpha_search_kb": "KB search", "beta_search_kb": "admin beta"} {
		if td[k].Label != want {
			t.Errorf("%s = %+v, want %q", k, td[k], want)
		}
	}
	if _, ok := td["gone"]; ok || td["search_kb"].Kind != "search" {
		t.Errorf("toolDisplay %+v", td)
	}
}
