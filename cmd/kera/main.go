// Command kera is the whole server: web app, API and maintenance commands.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/db"
	_ "github.com/cdmx-in/kera/internal/files" // registers the files feature
	"github.com/cdmx-in/kera/internal/version"
)

const usage = `kera - self-hosted team chat for Ollama and OpenAI-compatible models

Usage:
  kera serve              run the server (applies database migrations first)
  kera db migrate         apply database migrations and exit
  kera db status          list migrations and whether they are applied
  kera version            print the version

Environment: KERA_DATABASE_URL, KERA_SECRET_KEY, KERA_URL, KERA_DATA_DIR, KERA_LISTEN, OLLAMA_BASE_URL.
`

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()})))
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Println(version.Version)
		return nil
	case "serve":
		return serve(ctx)
	case "db":
		return dbCmd(ctx, args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
}

func serve(ctx context.Context) error {
	env, err := config.Load(true)
	if err != nil {
		return err
	}
	a, err := app.New(ctx, env)
	if err != nil {
		return err
	}
	defer a.Close()
	return a.Server.ListenAndServe(ctx)
}

func dbCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("db", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: kera db migrate|status")
	}
	env, err := config.Load(false)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	switch fs.Arg(0) {
	case "migrate":
		applied, err := db.Migrate(ctx, pool)
		for _, p := range applied {
			fmt.Println("applied", p)
		}
		if err == nil && len(applied) == 0 {
			fmt.Println("up to date")
		}
		return err
	case "status":
		st, err := db.Status(ctx, pool)
		if err != nil {
			return err
		}
		for _, s := range st {
			fmt.Printf("%-8s %s\n", s.State, s.Source.Path)
		}
		return nil
	}
	return fmt.Errorf("unknown db command %q", fs.Arg(0))
}

func logLevel() slog.Level {
	if os.Getenv("KERA_DEBUG") != "" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
