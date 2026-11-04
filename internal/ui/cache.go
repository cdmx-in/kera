package ui

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/cdmx-in/kera/internal/app"
)

// mountCache serves downloads that admins drop into <KERA_DATA_DIR>/cache
// (for example a browser extension zip). Signed-in users only, by session
// cookie or API key, so an install command can fetch it with a key.
func mountCache(a *app.App) {
	root := filepath.Join(a.Env.DataDir, "cache")
	a.Server.Handle("GET /cache/{path...}", a.Auth.Required(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveCache(w, r, root, r.PathValue("path"))
	})))
}

func serveCache(w http.ResponseWriter, r *http.Request, root, rel string) {
	rel = path.Clean("/" + rel)[1:]
	if rel == "" || strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") {
		http.NotFound(w, r)
		return
	}
	// os.OpenInRoot refuses "..", absolute paths and symlinks that leave root.
	f, err := os.OpenInRoot(root, rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(path.Base(rel), `"`, "")+`"`)
	if strings.HasSuffix(rel, ".zip") {
		w.Header().Set("Content-Type", "application/zip")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, path.Base(rel), st.ModTime(), f)
}
