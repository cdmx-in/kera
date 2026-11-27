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
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/memory"
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

	// Memory stores what automatic memory review learns; nil switches the review off.
	Memory *memory.Store

	// LoadUser loads an account with its permissions as they are now, and refuses a disabled or removed one. A resumed
	// run is allowed to go on only by what this returns (Mount: the app's authenticator).
	LoadUser func(ctx context.Context, id string) (*auth.User, error)

	deferMu  sync.Mutex
	deferred map[string]time.Time // runs put back to be resumed later, by the time of the first try

	// drain is cancelled when the engine starts shutting down: no call starts after it, model calls of runs that can
	// be resumed are cut, and held streams close.
	drain      context.Context
	startDrain context.CancelFunc

	// instance names this process as the owner of the leases it holds (durable.go).
	instance string
	kicked   chan struct{}
	lastBeat time.Time // the last successful heartbeat; only the heartbeat's goroutine touches it

	runs     *registry
	wg       sync.WaitGroup
	reviewMu sync.Mutex // held while a memory review runs
}

// Errors returned before a run starts.
var (
	errBusy            = api.Errf(http.StatusConflict, "busy", "this message is already being generated")
	errLocalLock       = api.Errf(http.StatusConflict, "local_lock", "This chat used a local model, so it stays on local models.")
	errCoworkForbidden = api.Errf(http.StatusForbidden, "forbidden", "Permission required: features.cowork")
	errTempForbidden   = api.Errf(http.StatusForbidden, "forbidden", "Permission required: features.temporary_chat")
	errCancelled       = errors.New("cancelled by the user")
	errShutdown        = errors.New("the server is restarting")
	errTimeLimit       = errors.New("the unattended time limit passed")
)

var (
	regMu sync.Mutex
	reg   = map[*app.App]*Engine{}
)

// For returns the engine Mount built for a, for features that start runs on
// their own (scheduled tasks). It is nil until engine.Mount has run.
func For(a *app.App) *Engine {
	regMu.Lock()
	defer regMu.Unlock()
	return reg[a]
}

// timeLimitMsg is the error of an unattended run that ran too long.
const timeLimitMsg = "This scheduled run was stopped because it ran longer than the time limit."

// interruptedMsg is the error a run stopped by a restart is saved with.
const interruptedMsg = "The server restarted while this answer was being written; the text above is what was written until then."

// releaseWait is how long runs that a shutdown cut off get to save or release themselves.
var releaseWait = 5 * time.Second

// Shutdown drains the engine. A run that can be resumed stops between calls: a tool call that is running gets up to
// d to finish and be recorded, no new call starts, and the run's lease is released so that the next process takes it
// over at once. After d it is cut off, and the call is in doubt when the run goes on. Every other run is stopped now
// and saved with interruptedMsg. Without the drain every deploy would leave the calls in flight in doubt.
func (e *Engine) Shutdown(d time.Duration) {
	e.startDrain()
	for _, r := range e.runs.live() {
		if !r.resumable() {
			r.stop(errShutdown)
		}
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return
	case <-time.After(d):
	}
	for _, r := range e.runs.live() {
		r.stop(errShutdown)
	}
	select {
	case <-done:
	case <-time.After(releaseWait):
		slog.Warn("shutdown: runs still saving, giving up", "after", d+releaseWait)
	}
}

// draining reports that the engine is shutting down.
func (e *Engine) draining() bool { return e.drain.Err() != nil }

// cutOnDrain is ctx, cancelled with errShutdown once the engine drains: for work that is better done again after the
// restart than waited for.
func (e *Engine) cutOnDrain(ctx context.Context) (context.Context, func()) {
	c, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(e.drain, func() { cancel(errShutdown) })
	return c, func() { stop(); cancel(nil) }
}

// New builds an engine; Mount uses the app's shared services.
func New(db *pgxpool.Pool, keys *secret.Keys, st *settings.Store, reg *models.Registry, fs *files.Service, cs *chats.Store, publicURL string) *Engine {
	e := &Engine{DB: db, Keys: keys, Settings: st, Models: reg, Files: fs, Chats: cs, PublicURL: publicURL,
		HTTP: &http.Client{}, runs: newRegistry(), instance: ids.New(), kicked: make(chan struct{}, 1), lastBeat: time.Now(),
		LoadUser: auth.New(db, keys, st, false).LoadUser, deferred: map[string]time.Time{}}
	e.drain, e.startDrain = context.WithCancel(context.Background())
	return e
}

// Mount registers the completion, stream and callback routes and the built-in tools.
func Mount(a *app.App) error {
	e := New(a.DB, a.Keys, a.Settings, models.For(a), files.For(a), chats.For(a), a.Env.PublicURL)
	e.CallbackURL = a.Env.CallbackURL
	e.LoadUser = a.Auth.LoadUser
	e.Memory = memory.For(a)
	e.Chats.SetLive(e.live)
	tools.Register(&filesTools{e: e})
	tools.Register(&coreTools{e: e})
	a.Server.OnShutdown(func() { e.Shutdown(20 * time.Second) })
	go e.janitor()
	a.Server.OnStart(func() { go e.durableLoop() }) // not now: a claimed run needs what later features register
	e.routes(a)
	regMu.Lock()
	reg[a] = e
	regMu.Unlock()
	return nil
}

func (e *Engine) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for tick := 0; ; tick++ {
		<-t.C
		e.runs.sweep()
		if n, err := e.failOrphans(context.Background()); err != nil {
			slog.Warn("janitor", "err", err)
		} else if n > 0 {
			slog.Info("failed replies that had no run", "replies", n)
		}
		if tick%60 == 0 { // hourly, the first a minute after start-up
			if n, err := e.purgeTrace(context.Background()); err != nil {
				slog.Warn("purge run trace", "err", err)
			} else if n > 0 {
				slog.Info("purged run trace", "rows", n)
			}
		}
	}
}

// live is the in-flight state of a stored message, from this instance's run. A run that has ended here no longer
// speaks for its message (the stored row does, and another instance may be writing it), and a temporary run never
// does, whatever message id its caller gave it.
func (e *Engine) live(messageID string) (chats.Live, bool) {
	if r := e.runs.byMessage(messageID); r != nil && r.Persisted && !r.Finished() {
		return r.Live(), true
	}
	return chats.Live{}, false
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
