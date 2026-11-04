// Package engine runs chat completions: context assembly, hooks, the tool
// loop, streaming, persistence and background tasks. One code path serves
// the web app (native event stream), API keys (OpenAI-compatible output) and
// temporary chats. Runs are detached from the HTTP request that starts them.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/tools"
)

func init() { app.Register("engine", Mount) }

// Engine holds what a run needs.
type Engine struct {
	DB        *pgxpool.Pool
	Keys      *secret.Keys
	Settings  *settings.Store
	Models    *models.Registry
	Files     *files.Service
	Chats     *chats.Store
	PublicURL string
	// CallbackURL is the base of the run callback URLs handed to tool and
	// hook servers (KERA_CALLBACK_URL); empty means PublicURL.
	CallbackURL string
	HTTP        *http.Client // hook servers

	runs *registry
	wg   sync.WaitGroup
}

// Errors returned before a run starts.
var (
	errBusy            = api.Errf(http.StatusConflict, "busy", "this message is already being generated")
	errLocalLock       = api.Errf(http.StatusConflict, "local_lock", "This chat used a local model, so it stays on local models.")
	errCoworkForbidden = api.Errf(http.StatusForbidden, "forbidden", "Permission required: features.cowork")
	errCancelled       = errors.New("cancelled by the user")
)

// New builds an engine; Mount uses the app's shared services.
func New(db *pgxpool.Pool, keys *secret.Keys, st *settings.Store, reg *models.Registry, fs *files.Service, cs *chats.Store, publicURL string) *Engine {
	return &Engine{DB: db, Keys: keys, Settings: st, Models: reg, Files: fs, Chats: cs, PublicURL: publicURL,
		HTTP: &http.Client{}, runs: newRegistry()}
}

// Mount registers the completion, stream and callback routes and the built-in tools.
func Mount(a *app.App) error {
	e := New(a.DB, a.Keys, a.Settings, models.For(a), files.For(a), chats.For(a), a.Env.PublicURL)
	e.CallbackURL = a.Env.CallbackURL
	e.Chats.SetLive(func(id string) (chats.Live, bool) {
		if r := e.runs.byMessage(id); r != nil {
			return r.Live(), true
		}
		return chats.Live{}, false
	})
	// ponytail: single instance, so a message still "streaming" at start-up lost its run.
	if _, err := a.DB.Exec(context.Background(), `UPDATE message SET status = 'error',
		error = 'The server restarted while this answer was being written.' WHERE status = 'streaming'`); err != nil {
		return err
	}
	tools.Register(&filesTools{e: e})
	tools.Register(&coreTools{e: e})
	go e.janitor()
	e.routes(a)
	return nil
}

func (e *Engine) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		e.runs.sweep()
	}
}

// Wait blocks until background run goroutines finish (tests).
func (e *Engine) Wait() { e.wg.Wait() }

// setting reads a runtime setting into dst, logging decode problems.
func (e *Engine) setting(ctx context.Context, key string, dst any) {
	if err := e.Settings.Get(ctx, key, dst); err != nil {
		slog.Warn("setting", "key", key, "err", err)
	}
}

// userProfile is what prompt variables and hooks need about the user.
type userProfile struct {
	GroupNames []string
	Language   string
	Timezone   string
	Location   string
	Memory     bool // opted in to memory
}

func (e *Engine) profile(ctx context.Context, u *auth.User) userProfile {
	p := userProfile{Language: "en-US", Location: "Unknown"}
	e.setting(ctx, "site.timezone", &p.Timezone)
	var raw []byte
	if err := e.DB.QueryRow(ctx, `SELECT settings FROM "user" WHERE id = $1`, u.ID).Scan(&raw); err == nil {
		var s struct {
			Timezone string `json:"timezone"`
			UI       struct {
				Language string `json:"language"`
				Timezone string `json:"timezone"`
				Location string `json:"location"`
				Memory   bool   `json:"memory"`
			} `json:"ui"`
		}
		_ = json.Unmarshal(raw, &s)
		p.Language = coalesce(s.UI.Language, p.Language)
		p.Timezone = coalesce(s.UI.Timezone, s.Timezone, p.Timezone)
		p.Location = coalesce(s.UI.Location, p.Location)
		p.Memory = s.UI.Memory
	}
	if len(u.Groups) > 0 {
		rows, err := e.DB.Query(ctx, `SELECT name FROM "group" WHERE id = ANY($1) ORDER BY name`, u.Groups)
		if err == nil {
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					p.GroupNames = append(p.GroupNames, n)
				}
			}
			rows.Close()
		}
	}
	return p
}

// callbackURL is where a tool or hook server reaches the run's callbacks.
func (e *Engine) callbackURL(runID string) string {
	base := e.CallbackURL
	if base == "" {
		base = e.PublicURL
	}
	return base + "/api/v1/runs/" + runID
}
