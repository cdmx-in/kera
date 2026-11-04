package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/cdmx-in/kera/internal/apply"
)

// assets serves brand images from one directory. Every URL the server emits
// carries ?v=<content hash>, so a matching request may be cached forever and
// a replaced file is picked up by the next shell render.
type assets struct{ dir string }

var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp",
	".gif": "image/gif", ".ico": "image/x-icon", ".svg": "image/svg+xml",
}

const brandPrefix = "/brand/"

// open returns the named image, or an error for anything that is not a plain
// image file directly inside dir (os.OpenInRoot also refuses symlink escapes).
func (a *assets) open(name string) (*os.File, string, error) {
	typ, ok := imageTypes[strings.ToLower(path.Ext(name))]
	if !ok || name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return nil, "", os.ErrNotExist
	}
	f, err := os.OpenInRoot(a.dir, name)
	if err != nil {
		return nil, "", err
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, "", os.ErrNotExist
	}
	return f, typ, nil
}

// hash is the first 8 hex digits of the file's sha256, "" when unreadable.
func (a *assets) hash(name string) string {
	f, _, err := a.open(name)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// url adds ?v=<hash> to a /brand/ URL whose file exists; other URLs pass through.
func (a *assets) url(u string) string {
	name, ok := strings.CutPrefix(u, brandPrefix)
	if !ok || strings.Contains(name, "?") {
		return u
	}
	if v := a.hash(name); v != "" {
		return u + "?v=" + v
	}
	return u
}

// version rewrites every image URL in b.
func (a *assets) version(b *apply.Brand) {
	if b.Logo != nil {
		l := *b.Logo
		l.Light, l.Dark = a.url(l.Light), a.url(l.Dark)
		b.Logo = &l
	}
	if b.Splash != nil {
		s := *b.Splash
		s.Logo, s.LogoDark = a.url(s.Logo), a.url(s.LogoDark)
		b.Splash = &s
	}
	b.Mark, b.Favicon, b.AppleTouchIcon = a.url(b.Mark), a.url(b.Favicon), a.url(b.AppleTouchIcon)
	b.FaviconAlt = a.urls(b.FaviconAlt)
	b.PWAIcons = a.urls(b.PWAIcons)
}

func (a *assets) urls(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, u := range in {
		out[i] = a.url(u)
	}
	return out
}

func (a *assets) serve(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	f, typ, err := a.open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("X-Content-Type-Options", "nosniff")
	if typ == "image/svg+xml" {
		// an SVG opened directly must not run script on our origin
		h.Set("Content-Security-Policy", "sandbox")
	}
	if v := r.URL.Query().Get("v"); v != "" && v == a.hash(name) {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, st.ModTime(), f)
}
