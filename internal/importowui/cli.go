package importowui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/db"
)

// Usage documents the migrate subcommands.
const Usage = `Usage:
  kera migrate from-openwebui --dsn <source postgres url> --uploads-dir <dir> [flags]
      --dry-run              import inside a transaction that is rolled back; copy no files
      --only <list>          comma list of: ` + "users,groups,grants,chats,folders,files,knowledge,memories,prompts,feedback" + `
      --since <date>         only chats and files updated at or after (YYYY-MM-DD or RFC 3339)
      --prune-missing        delete imported rows that are gone from the source (never rows made in Kera)
      --no-files             keep file rows but do not copy blobs (--uploads-dir not needed)
      --export-presets <dir> write each model's system prompt to <dir>/<id>.md
      --report-config        print retrieval/web/task/ui/permission/tool-server settings, secrets removed
  kera migrate verify --dsn <source postgres url>
      compare source and target counts and check imported API keys; exits 1 on mismatch

The target is KERA_DATABASE_URL (migrated first); files go to KERA_DATA_DIR/uploads.
`

// ErrMismatch is returned by verify when source and target differ.
var ErrMismatch = errors.New("verify found mismatches")

// Command runs "kera migrate <args>".
func Command(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing subcommand\n\n" + Usage)
	}
	switch args[0] {
	case "from-openwebui":
		return fromCmd(ctx, args[1:], out)
	case "verify":
		return verifyCmd(ctx, args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, Usage)
		return nil
	}
	return fmt.Errorf("unknown migrate command %q\n\n%s", args[0], Usage)
}

func fromCmd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migrate from-openwebui", flag.ContinueOnError)
	dsn := fs.String("dsn", "", "source Postgres URL")
	opt := Options{Log: slog.Default(), Trigger: "cli"}
	fs.StringVar(&opt.UploadsDir, "uploads-dir", "", "source uploads directory (read only)")
	fs.BoolVar(&opt.DryRun, "dry-run", false, "roll back instead of committing")
	only := fs.String("only", "", "comma list of entities")
	since := fs.String("since", "", "chats and files updated at or after this date")
	fs.BoolVar(&opt.PruneMissing, "prune-missing", false, "delete imported rows gone from the source")
	fs.BoolVar(&opt.NoFiles, "no-files", false, "skip blob copies")
	presets := fs.String("export-presets", "", "directory for model system prompts")
	reportCfg := fs.Bool("report-config", false, "print selected source settings without secrets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var err error
	if opt.Only, err = parseOnly(*only); err != nil {
		return err
	}
	if opt.Since, err = parseSince(*since); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("--dsn is required")
	}
	if opt.UploadsDir == "" && !opt.NoFiles {
		return errors.New("--uploads-dir is required (or pass --no-files)")
	}
	src, dst, env, err := open(ctx, *dsn)
	if err != nil {
		return err
	}
	defer src.Close()
	defer dst.Close()
	opt.DataDir = env.DataDir
	unlock, err := Lock(ctx, dst)
	if err != nil {
		return err
	}
	defer unlock()
	rep, err := Run(ctx, src, dst, SourceName(*dsn), opt)
	if rep != nil {
		PrintReport(out, rep, opt.DryRun)
	}
	if err != nil {
		return err
	}
	if opt.DryRun {
		fmt.Fprintln(out, "dry run: nothing was imported")
	}
	if *presets != "" {
		n, err := ExportPresets(ctx, src, *presets)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "exported %d preset prompts to %s\n", n, *presets)
	}
	if *reportCfg {
		fmt.Fprintln(out, "\nsource settings (secrets removed):")
		return ReportConfig(ctx, src, out)
	}
	return nil
}

func verifyCmd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migrate verify", flag.ContinueOnError)
	dsn := fs.String("dsn", "", "source Postgres URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("--dsn is required")
	}
	src, dst, _, err := open(ctx, *dsn)
	if err != nil {
		return err
	}
	defer src.Close()
	defer dst.Close()
	v, err := Verify(ctx, src, dst)
	if err != nil {
		return err
	}
	PrintVerification(out, v)
	if !v.OK() {
		return ErrMismatch
	}
	return nil
}

// open connects to the source (read-only sessions) and the migrated target.
func open(ctx context.Context, dsn string) (*pgxpool.Pool, *pgxpool.Pool, config.Env, error) {
	env, err := config.Load(false)
	if err != nil {
		return nil, nil, env, err
	}
	if dsn == env.DatabaseURL {
		return nil, nil, env, errors.New("--dsn points at the Kera database itself")
	}
	src, err := openSource(ctx, dsn)
	if err != nil {
		return nil, nil, env, err
	}
	dst, err := db.Open(ctx, env.DatabaseURL)
	if err != nil {
		src.Close()
		return nil, nil, env, err
	}
	if _, err := db.Migrate(ctx, dst); err != nil {
		src.Close()
		dst.Close()
		return nil, nil, env, err
	}
	return src, dst, env, nil
}

// openSource connects to the source database with read-only sessions.
func openSource(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse source dsn: %w", err) // pgx keeps the password out of this error
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	src, err := pgxpool.NewWithConfig(ctx, cfg)
	if err == nil {
		err = src.Ping(ctx)
	}
	if err != nil {
		if src != nil {
			src.Close()
		}
		return nil, fmt.Errorf("connect source: %w", err)
	}
	return src, nil
}

func parseOnly(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	valid := map[string]bool{}
	for _, e := range Entities {
		valid[e] = true
	}
	var out []string
	for _, e := range strings.Split(s, ",") {
		e = strings.TrimSpace(e)
		if !valid[e] {
			return nil, fmt.Errorf("--only: unknown entity %q (want %s)", e, strings.Join(Entities, ","))
		}
		out = append(out, e)
	}
	return out, nil
}

func parseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--since: want YYYY-MM-DD or RFC 3339, got %q", s)
	}
	return t, nil
}
