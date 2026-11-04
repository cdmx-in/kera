package ui

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"image"
	_ "image/gif" // DecodeConfig for manifest icon sizes
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/apply"
)

//go:embed splash.css
var splashCSS string

const (
	headMarker = "<!--kera:head-->"
	rootEmpty  = `<div id="root"></div>`
)

// greyLadder is the lightness and relative chroma of --gray-50..950; it must
// match the defaults in web/src/styles/app.css.
var greyLadder = [11]struct {
	step   string
	l, rel float64
}{
	{"50", 0.985, 0.25}, {"100", 0.967, 0.5}, {"200", 0.925, 0.75}, {"300", 0.87, 1}, {"400", 0.71, 1},
	{"500", 0.555, 1}, {"600", 0.45, 1}, {"700", 0.375, 1}, {"800", 0.28, 1}, {"900", 0.215, 0.833}, {"950", 0.16, 0.667},
}

// tokens returns the :root custom properties the brand overrides ("" = none).
func tokens(b *apply.Brand) string {
	var p []string
	if a := b.Accent; a != nil {
		if apply.CSSColor(a.Base) {
			p = append(p, "--accent:"+a.Base)
		}
		if apply.CSSColor(a.Bright) {
			p = append(p, "--accent-bright:"+a.Bright)
		}
	}
	if g := b.Grey; g != nil {
		for i, s := range greyLadder {
			switch {
			case len(g.Scale) == len(greyLadder) && apply.CSSColor(g.Scale[i]):
				p = append(p, "--gray-"+s.step+":"+g.Scale[i])
			case g.Chroma > 0 || g.Hue != 0:
				p = append(p, fmt.Sprintf("--gray-%s:oklch(%s %s %s)", s.step, num(s.l), num(chroma4(g.Chroma*s.rel)), num(g.Hue)))
			}
		}
	}
	if len(p) == 0 {
		return ""
	}
	return ":root{" + strings.Join(p, ";") + "}"
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// chroma4 rounds a chroma to 4 decimals.
func chroma4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

// themeScript sets data-theme and the "dark" class before first paint from
// localStorage.theme (the key the app writes) or the brand default.
func themeScript(def string) string {
	if def == "" {
		def = "system"
	}
	d, _ := json.Marshal(def)
	return `(function(){var e=document.documentElement,t;try{t=localStorage.getItem("theme")}catch(x){}` +
		`if(t!=="light"&&t!=="dark"&&t!=="system")t=t&&t.indexOf("dark")>-1?"dark":` + string(d) + `;` +
		`if(t==="light"||t==="dark")e.dataset.theme=t;` +
		`e.classList.toggle("dark",t==="dark"||(t!=="light"&&matchMedia("(prefers-color-scheme: dark)").matches))})()`
}

// scriptJSON encodes v for a <script> body. encoding/json already escapes
// <, >, & and U+2028/2029, so "</script>" cannot end the element early.
func scriptJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// styleText keeps admin CSS from closing the <style> element.
func styleText(s string) string { return strings.ReplaceAll(s, "</", `<\/`) }

// iconType guesses a MIME type from a URL's extension (query ignored).
func iconType(u string) string {
	u, _, _ = strings.Cut(u, "?")
	return imageTypes[strings.ToLower(path.Ext(u))]
}

func head(cfg *Config) string {
	b := &cfg.Brand
	var h strings.Builder
	attr := html.EscapeString
	fmt.Fprintf(&h, "<title>%s</title>\n", attr(b.Name))
	if b.Tagline != "" {
		fmt.Fprintf(&h, `<meta name="description" content="%s">`+"\n", attr(b.Tagline))
	}
	for _, u := range append([]string{b.Favicon}, b.FaviconAlt...) {
		if u == "" {
			continue
		}
		sizes := ""
		if t := iconType(u); t == "image/svg+xml" || t == "image/x-icon" {
			sizes = ` sizes="any"`
		}
		fmt.Fprintf(&h, `<link rel="icon" href="%s" type="%s"%s>`+"\n", attr(u), attr(iconType(u)), sizes)
	}
	if b.AppleTouchIcon != "" {
		fmt.Fprintf(&h, `<link rel="apple-touch-icon" href="%s">`+"\n", attr(b.AppleTouchIcon))
	}
	h.WriteString(`<link rel="manifest" href="/manifest.json">` + "\n")
	h.WriteString(`<meta name="theme-color" content="#ffffff" media="(prefers-color-scheme: light)">` + "\n")
	fmt.Fprintf(&h, `<meta name="theme-color" content="%s" media="(prefers-color-scheme: dark)">`+"\n", attr(darkBackground(b)))
	h.WriteString("<style>" + tokens(b))
	if b.Splash != nil {
		h.WriteString(splashCSS)
	}
	if b.CustomCSS != "" {
		h.WriteString(styleText(b.CustomCSS))
	}
	h.WriteString("</style>\n")
	h.WriteString("<script>" + themeScript(b.DefaultTheme) + "</script>\n")
	h.WriteString("<script>window.__UI__=" + scriptJSON(cfg) + "</script>\n")
	return h.String()
}

// darkBackground is --gray-950 of the brand (the dark app background).
func darkBackground(b *apply.Brand) string {
	g := b.Grey
	switch {
	case g == nil:
		return "oklch(0.16 0.008 215)"
	case len(g.Scale) == len(greyLadder):
		return g.Scale[len(g.Scale)-1]
	}
	last := greyLadder[len(greyLadder)-1]
	return fmt.Sprintf("oklch(%s %s %s)", num(last.l), num(chroma4(g.Chroma*last.rel)), num(g.Hue))
}

// splash is the markup React's first render replaces. The web app's
// BrandSplash component renders the same classes to continue it while the
// first data loads.
func splash(b *apply.Brand) string {
	s := b.Splash
	if s == nil {
		return ""
	}
	style := s.Style
	if style == "" {
		style = "orbit"
	}
	attr := html.EscapeString
	var h strings.Builder
	fmt.Fprintf(&h, `<div class="kera-splash kera-splash-%s" data-splash role="status" aria-label="Loading %s"><div class="kera-splash-stage">`, attr(style), attr(b.Name))
	light, dark := s.Logo, s.LogoDark
	if dark == "" {
		dark = light
	}
	if light != "" {
		fmt.Fprintf(&h, `<img class="kera-splash-logo kera-splash-light" src="%s" alt="">`, attr(light))
		fmt.Fprintf(&h, `<img class="kera-splash-logo kera-splash-dark" src="%s" alt="">`, attr(dark))
	}
	h.WriteString(`</div>`)
	if s.Wordmark != "" {
		fmt.Fprintf(&h, `<div class="kera-splash-word">%s</div>`, attr(s.Wordmark))
	}
	h.WriteString(`</div>`)
	return h.String()
}

// renderIndex injects the brand into the built index.html.
func renderIndex(tmpl []byte, cfg *Config) []byte {
	out := bytes.Replace(tmpl, []byte(headMarker), []byte(head(cfg)), 1)
	if sp := splash(&cfg.Brand); sp != "" {
		out = bytes.Replace(out, []byte(rootEmpty), []byte(`<div id="root">`+sp+`</div>`), 1)
	}
	return out
}

type manifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type,omitempty"`
	Purpose string `json:"purpose"`
}

func (u *UI) handleManifest(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := u.current(r.Context())
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	b := cfg.Brand
	srcs := b.PWAIcons
	if len(srcs) == 0 {
		srcs = []string{b.Mark, b.Favicon}
	}
	icons := []manifestIcon{}
	for _, s := range srcs {
		if s != "" {
			icons = append(icons, manifestIcon{Src: s, Sizes: u.assets.size(s), Type: iconType(s), Purpose: "any"})
		}
	}
	theme := "#ffffff"
	if b.Accent != nil && b.Accent.Base != "" {
		theme = b.Accent.Base
	}
	m := map[string]any{"name": b.Name, "short_name": b.Name, "start_url": "/", "scope": "/", "display": "standalone",
		"background_color": "#ffffff", "theme_color": theme, "icons": icons}
	if b.Tagline != "" {
		m["description"] = b.Tagline
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(m)
}

// size is "WxH" for a raster brand image, "any" otherwise.
func (a *assets) size(u string) string {
	name, ok := strings.CutPrefix(u, brandPrefix)
	if !ok {
		return "any"
	}
	name, _, _ = strings.Cut(name, "?")
	f, _, err := a.open(name)
	if err != nil {
		return "any"
	}
	defer f.Close()
	c, _, err := image.DecodeConfig(f)
	if err != nil {
		return "any"
	}
	return fmt.Sprintf("%dx%d", c.Width, c.Height)
}
