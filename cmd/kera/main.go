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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/db"
	"github.com/cdmx-in/kera/internal/identity"
	"github.com/cdmx-in/kera/internal/importowui"
	"github.com/cdmx-in/kera/internal/knowledge"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/skills"
	"github.com/cdmx-in/kera/internal/version"
)

const usage = `kera - self-hosted team chat for Ollama and OpenAI-compatible models

Usage:
  kera serve              run the server (applies database migrations first)
  kera db migrate         apply database migrations and exit
  kera db status          list migrations and whether they are applied
  kera apply -f FILE [--dry-run] [--prune]
                          make the database match a declarative YAML config
  kera export [-o FILE]   print the managed config as YAML (secrets as ${...})
  kera migrate from-openwebui|verify  import an Open WebUI database (kera migrate help)
  kera version            print the version
` + knowledge.CLIUsage + skills.CLIUsage + identity.CLIUsage + `
Environment: KERA_DATABASE_URL, KERA_SECRET_KEY, KERA_URL, KERA_DATA_DIR, KERA_LISTEN, OLLAMA_BASE_URL,
KERA_CONFIG (default file for kera apply), KERA_CALLBACK_URL (base URL tool servers call back on; default KERA_URL).
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
	case "user", "apikey":
		return identityCmd(ctx, args)
	case "apply":
		return applyCmd(ctx, args[1:])
	case "export":
		return exportCmd(ctx, args[1:])
	case "migrate":
		return importowui.Command(ctx, args[1:], os.Stdout)
	case "reindex":
		return knowledge.Command(ctx, args[1:], os.Stdout)
	case "skills":
		return skills.Command(ctx, args[1:], os.Stdout)
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

func identityCmd(ctx context.Context, args []string) error {
	env, err := config.Load(false)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if args[0] == "user" && len(args) > 1 && args[1] == "login-link" {
		return loginLink(ctx, pool, args[2:])
	}
	if args[0] == "user" {
		return identity.UserCmd(ctx, pool, args[1:], os.Stdout)
	}
	return identity.APIKeyCmd(ctx, pool, args[1:], os.Stdout, os.Stderr)
}

// loginLink prints a sign-in link for an existing user. It is the way into
// the web app before an OAuth provider is configured for this host (for
// example while running on a LAN address that Google will not redirect to).
func loginLink(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	fs := flag.NewFlagSet("user login-link", flag.ContinueOnError)
	ttl := fs.Duration("ttl", 12*time.Hour, "how long the session lasts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: kera user login-link [--ttl 12h] <email>")
	}
	env, err := config.Load(true)
	if err != nil {
		return err
	}
	keys, err := secret.Derive(env.SecretKey)
	if err != nil {
		return err
	}
	var id, role string
	err = pool.QueryRow(ctx, `SELECT id, role FROM "user" WHERE lower(email) = lower($1)`, fs.Arg(0)).Scan(&id, &role)
	if err != nil {
		return fmt.Errorf("user %s: %w", fs.Arg(0), err)
	}
	if role == "pending" {
		return errors.New("user is pending approval")
	}
	tok, err := auth.IssueToken(keys, id, *ttl)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "This link signs in as that user until it expires. Do not share it.")
	fmt.Printf("%s/auth#token=%s\n", env.PublicURL, tok)
	return nil
}

func logLevel() slog.Level {
	if os.Getenv("KERA_DEBUG") != "" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
