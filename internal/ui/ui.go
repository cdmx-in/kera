// Package ui serves what the web app needs before it can call the API: the
// public UI config (also inlined into index.html as window.__UI__), brand
// tokens and splash in the app shell, brand images and the PWA manifest.
package ui

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	TemporaryChat   bool `json:"temporaryChat"`
	CodeInterpreter bool `json:"codeInterpreter"`
	WebSearch       bool `json:"webSearch"`
	APIKeys         bool `json:"apiKeys"`
}

// cacheFor bounds how stale the shell may be after an admin change.
const cacheFor = 10 * time.Second

// UI renders and caches the config and the app shell.
type UI struct {
	settings *settings.Store
	assets   *assets
	tmpl     []byte

	mu    sync.Mutex
	until time.Time
	cfg   *Config
	index []byte
}

// Mount registers the routes and takes over index.html rendering.
func Mount(a *app.App) error {
	u := &UI{settings: a.Settings, assets: &assets{dir: brandDir(a.Env.ConfigFile, a.Env.DataDir)}, tmpl: a.Server.IndexTemplate()}
	u.routes(a.Server)
	mountCache(a)
	return nil
}

func (u *UI) routes(s *api.Server) {
	s.HandleFunc("GET /api/v1/ui/config", u.handleConfig)
	s.HandleFunc("GET /manifest.json", u.handleManifest)
	s.HandleFunc("GET /brand/{file}", u.assets.serve)
	s.SetIndexRenderer(u.renderIndex)
}

// brandDir is KERA_BRAND_DIR, else brand/ next to KERA_CONFIG, else <data>/brand.
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

// current returns the cached config and shell, rebuilding them every cacheFor.
func (u *UI) current(ctx context.Context) (*Config, []byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cfg != nil && time.Now().Before(u.until) {
		return u.cfg, u.index, nil
	}
	cfg, err := load(ctx, u.settings)
	if err != nil {
		return nil, nil, err
	}
	u.assets.version(&cfg.Brand)
	u.cfg, u.index, u.until = cfg, renderIndex(u.tmpl, cfg), time.Now().Add(cacheFor)
	return u.cfg, u.index, nil
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
	// ponytail: temporary chats and API keys have no deployment-wide switch,
	// only per-user permissions; add settings keys if a deployment needs one.
	cfg.Features = Features{Spend: spend != "", Memory: memory, TemporaryChat: true,
		CodeInterpreter: interpreter != "", WebSearch: search != "", APIKeys: true}
	return cfg, nil
}
