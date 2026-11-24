// Package app assembles shared dependencies once and mounts every feature.
// Feature packages expose Mount(*app.App) and are listed in mounts.go, so
// adding a feature touches one line there and nothing else.
package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/db"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
)

// App is what feature packages receive.
type App struct {
	Env      config.Env
	DB       *pgxpool.Pool
	Keys     *secret.Keys
	Settings *settings.Store
	Auth     *auth.Authenticator
	Server   *api.Server
}

// New opens the database, applies migrations and builds the server.
func New(ctx context.Context, env config.Env) (*App, error) {
	pool, err := db.Open(ctx, env.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if _, err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	keys, err := secret.Derive(env.SecretKey)
	if err != nil {
		pool.Close()
		return nil, err
	}
	st := settings.New(pool)
	if env.ExtractorURL != "" {
		// ponytail: one environment default today; a second one is another line here, a table when there are many.
		st.SetDefault("rag.extractor.url", env.ExtractorURL) // a stored value still wins
	}
	srv, err := api.New(env, pool)
	if err != nil {
		pool.Close()
		return nil, err
	}
	a := &App{Env: env, DB: pool, Keys: keys, Settings: st, Server: srv,
		Auth: auth.New(pool, keys, st, strings.HasPrefix(env.PublicURL, "https://"))}
	for _, m := range mounts {
		if err := m.fn(a); err != nil {
			pool.Close()
			return nil, fmt.Errorf("mount %s: %w", m.name, err)
		}
	}
	return a, nil
}

// Close releases resources.
func (a *App) Close() { a.DB.Close() }

type mount struct {
	name string
	fn   func(*App) error
}
