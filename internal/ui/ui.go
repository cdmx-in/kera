// Package ui serves what the web app needs before it can call the API: the
// public UI config (also inlined into index.html as window.__UI__), brand
// tokens and splash in the app shell, brand images and the PWA manifest.
package ui

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/settings"
)

func init() { app.Register("ui", Mount) }

// Config is GET /api/v1/ui/config and window.__UI__.
type Config struct {
	Brand             apply.Brand                  `json:"brand"`
	Features          Features                     `json:"features"`
	DefaultModels     []string                     `json:"defaultModels"`
	ModelOrder        []string                     `json:"modelOrder"`
	PromptSuggestions []apply.Suggestion           `json:"promptSuggestions"`
	ToolDisplay       map[string]apply.ToolDisplay `json:"toolDisplay"`
	Integrations      []apply.Integration          `json:"integrations"`
}

// Features says what this deployment offers at all; per-user permissions
// still decide who may use it.
type Features struct {
	Spend           bool `json:"spend"`
	Memory          bool `json:"memory"`
	CodeInterpreter bool `json:"codeInterpreter"`
	WebSearch       bool `json:"webSearch"`
}

// cacheFor bounds how stale the shell may be after an admin change.
const cacheFor = 10 * time.Second

// UI renders and caches the config and the app shell.
type UI struct {
	settings   *settings.Store
	db         *pgxpool.Pool // tool_server display names; nil in tests that need none
	assets     *assets
	tmpl       []byte
	configFile string // KERA_CONFIG, for "reset to the config file"

	mu    sync.Mutex
	until time.Time
	gen   uint64 // settings.Gen() the cache was built at
	cfg   *Config
	index []byte
}

// Mount registers the routes and takes over index.html rendering.
func Mount(a *app.App) error {
	u := &UI{settings: a.Settings, db: a.DB, tmpl: a.Server.IndexTemplate(), configFile: a.Env.ConfigFile,
		assets: &assets{upload: filepath.Join(a.Env.DataDir, "brand"), dir: brandDir(a.Env.ConfigFile, a.Env.DataDir)}}
	u.routes(a.Server)
	u.adminRoutes(a.Server, func(f http.HandlerFunc) http.Handler { return a.Auth.Admin(f) })
	mountCache(a)
	return nil
}

func (u *UI) routes(s *api.Server) {
	s.HandleFunc("GET /api/v1/ui/config", u.handleConfig)
	s.HandleFunc("GET /manifest.json", u.handleManifest)
	s.HandleFunc("GET /brand/{file}", u.assets.serve)
	s.SetIndexRenderer(u.renderIndex)
}

// brandDir is the read-only brand dir: KERA_BRAND_DIR, else brand/ next to
// KERA_CONFIG, else <data>/brand (then the same as the uploads dir).
func brandDir(configFile, dataDir string) string {
	if d := os.Getenv("KERA_BRAND_DIR"); d != "" {
		return d
	}
	if configFile != "" {
		return filepath.Join(filepath.Dir(configFile), "brand")
	}
	return filepath.Join(dataDir, "brand")
}

func (u *UI) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := u.current(r.Context())
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, cfg)
}

func (u *UI) renderIndex(r *http.Request) []byte {
	_, idx, err := u.current(r.Context())
	if err != nil {
		slog.Error("render index", "err", err)
		return renderIndex(u.tmpl, &Config{Brand: apply.Brand{Name: "Kera"}})
	}
	return idx
}

// current returns the cached config and shell, rebuilding them every cacheFor
// and right after an admin save in this process.
func (u *UI) current(ctx context.Context) (*Config, []byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	gen := u.settings.Gen()
	if u.cfg != nil && time.Now().Before(u.until) && gen == u.gen {
		return u.cfg, u.index, nil
	}
	cfg, err := load(ctx, u.settings)
	if err != nil {
		return nil, nil, err
	}
	if u.db != nil {
		if cfg.ToolDisplay == nil { // ui.tool_display stored as null
			cfg.ToolDisplay = map[string]apply.ToolDisplay{}
		}
		if err := mergeServerDisplay(ctx, u.db, cfg.ToolDisplay); err != nil {
			slog.Warn("ui: tool server display names", "err", err)
		}
	}
	u.assets.version(&cfg.Brand)
	u.cfg, u.index, u.until, u.gen = cfg, renderIndex(u.tmpl, cfg), time.Now().Add(cacheFor), gen
	return u.cfg, u.index, nil
}

// expire makes the next request rebuild the config (an asset changed).
func (u *UI) expire() {
	u.mu.Lock()
	u.until = time.Time{}
	u.mu.Unlock()
}

// load maps the settings `kera apply` (or the admin UI) wrote to Config.
func load(ctx context.Context, st *settings.Store) (*Config, error) {
	cfg := &Config{DefaultModels: []string{}, ModelOrder: []string{}, PromptSuggestions: []apply.Suggestion{},
		ToolDisplay: map[string]apply.ToolDisplay{}, Integrations: []apply.Integration{}}
	var siteName, spend, interpreter, search string
	var memory bool
	errs := []error{
		st.Get(ctx, "ui.brand", &cfg.Brand),
		st.Get(ctx, "site.name", &siteName),
		st.Get(ctx, "site.default_models", &cfg.DefaultModels),
		st.Get(ctx, "site.model_order", &cfg.ModelOrder),
		st.Get(ctx, "site.suggestions", &cfg.PromptSuggestions),
		st.Get(ctx, "ui.tool_display", &cfg.ToolDisplay),
		st.Get(ctx, "ui.integrations", &cfg.Integrations),
		st.Get(ctx, "spend.source.kind", &spend),
		st.Get(ctx, "chat.memory.enable", &memory),
		st.Get(ctx, "code_interpreter.engine", &interpreter),
		st.Get(ctx, "web.search.engine", &search),
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if cfg.Brand.Name == "" {
		cfg.Brand.Name = siteName
	}
	if cfg.Brand.Name == "" {
		cfg.Brand.Name = "Kera"
	}
	// temporary chats and API keys are per-user permissions (features.temporary_chat,
	// features.api_keys), read from /api/v1/auths/, not deployment switches
	cfg.Features = Features{Spend: spend != "", Memory: memory, CodeInterpreter: interpreter != "", WebSearch: search != ""}
	return cfg, nil
}

// mergeServerDisplay adds tool_servers[].display.tools to the tool display
// map as "<server id>_<function>" (the web app tries it for a server's steps
// first) and as the bare function name, each only when ui.tool_display or an
// earlier server (sort order) has not named it: the admin's names win.
func mergeServerDisplay(ctx context.Context, db *pgxpool.Pool, m map[string]apply.ToolDisplay) error {
	rows, err := db.Query(ctx, `SELECT id, display FROM tool_server WHERE enabled ORDER BY sort, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var d apply.ToolServerDisplay
		if err := rows.Scan(&id, &d); err != nil {
			return err
		}
		for _, name := range slices.Sorted(maps.Keys(d.Tools)) {
			for _, k := range []string{id + "_" + name, name} {
				if _, ok := m[k]; !ok {
					m[k] = d.Tools[name]
				}
			}
		}
	}
	return rows.Err()
}
