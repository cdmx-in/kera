// Package db owns the Postgres pool and the embedded schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects and pings. The pool is the only handle the app uses.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse KERA_DATABASE_URL: %w", err)
	}
	if cfg.MaxConns < 10 {
		cfg.MaxConns = 10
	}
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func provider(pool *pgxpool.Pool) (*goose.Provider, *sql.DB, error) {
	sqldb := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, sqldb, mustSub(migrations, "migrations"))
	if err != nil {
		sqldb.Close()
		return nil, nil, fmt.Errorf("migration provider: %w", err)
	}
	return p, sqldb, nil
}

// Migrate applies every pending migration.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	p, sqldb, err := provider(pool)
	if err != nil {
		return nil, err
	}
	defer sqldb.Close()
	res, err := p.Up(ctx)
	var applied []string
	for _, r := range res {
		applied = append(applied, r.Source.Path)
	}
	if err != nil {
		return applied, fmt.Errorf("migrate: %w", err)
	}
	return applied, nil
}

// Status reports (version, applied) for every known migration.
func Status(ctx context.Context, pool *pgxpool.Pool) ([]goose.MigrationStatus, error) {
	p, sqldb, err := provider(pool)
	if err != nil {
		return nil, err
	}
	defer sqldb.Close()
	st, err := p.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]goose.MigrationStatus, 0, len(st))
	for _, s := range st {
		out = append(out, *s)
	}
	return out, nil
}

// Pending reports whether migrations remain (used by /ready).
func Pending(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	p, sqldb, err := provider(pool)
	if err != nil {
		return false, err
	}
	defer sqldb.Close()
	return p.HasPending(ctx)
}
