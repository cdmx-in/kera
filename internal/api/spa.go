package api

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/web"
)

// spa serves the embedded Vite build: hashed /assets/* are immutable,
// index.html is never cached (so a deploy is visible on the next load, even
// behind a CDN), unknown paths fall back to index.html for client routing.
type spa struct {
	files fs.FS
	index []byte
}

const placeholderIndex = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Kera</title></head><body><p>Kera is running. The web app has not been built: run <code>make web</code>.</p></body></html>`

func newSPA(_ config.Env) (*spa, error) {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return nil, err
	}
	idx, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		idx = []byte(placeholderIndex)
	}
	return &spa{files: dist, index: idx}, nil
}

// SetIndex replaces the rendered index (brand injection happens at startup).
func (s *Server) SetIndex(b []byte) { s.spa.index = b }

// IndexTemplate returns the raw built index.html for rendering.
func (s *Server) IndexTemplate() []byte { return bytes.Clone(s.spa.index) }

func (s *spa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// API and OAuth paths never fall through to the app shell.
	if strings.HasPrefix(p, "api/") || strings.HasPrefix(p, "oauth/") || p == "api" {
		JSON(w, http.StatusNotFound, map[string]any{"detail": "not found", "code": "not_found"})
		return
	}
	if p != "" && p != "index.html" {
		if st, err := fs.Stat(s.files, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, s.files, p)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(s.index)
}
