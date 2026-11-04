package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/db"
	"github.com/cdmx-in/kera/internal/secret"
)

// applyCmd: kera apply -f <file> [--dry-run] [--prune]
func applyCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	file := fs.String("f", os.Getenv("KERA_CONFIG"), "config file (default $KERA_CONFIG)")
	dry := fs.Bool("dry-run", false, "print the plan and change nothing")
	prune := fs.Bool("prune", false, "delete managed rows of declared kinds that the file no longer lists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("usage: kera apply -f <file> [--dry-run] [--prune]")
	}
	f, err := apply.Load(*file)
	if err != nil {
		return err
	}
	pool, keys, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	res, err := apply.Apply(ctx, pool, keys, nil, f, apply.Options{DryRun: *dry, Prune: *prune})
	if res != nil {
		res.Print(os.Stdout)
	}
	return err
}

// exportCmd: kera export [-o file]
func exportCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	out := fs.String("o", "", "write to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, keys, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	cfg, placeholders, err := apply.Export(ctx, pool, keys)
	if err != nil {
		return err
	}
	b, err := apply.Marshal(cfg)
	if err != nil {
		return err
	}
	for _, p := range placeholders {
		fmt.Fprintln(os.Stderr, "secret exported as", p)
	}
	if *out == "" {
		_, err = os.Stdout.Write(b)
		return err
	}
	return os.WriteFile(*out, b, 0o600)
}

// openStore connects, applies schema migrations and derives the secret keys.
func openStore(ctx context.Context) (*pgxpool.Pool, *secret.Keys, error) {
	env, err := config.Load(true)
	if err != nil {
		return nil, nil, err
	}
	keys, err := secret.Derive(env.SecretKey)
	if err != nil {
		return nil, nil, err
	}
	pool, err := db.Open(ctx, env.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if _, err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, nil, err
	}
	return pool, keys, nil
}
