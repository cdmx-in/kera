package knowledge

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/db"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/models"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
)

// CLIUsage is the help text of `kera reindex`.
const CLIUsage = `  kera reindex [--all | --knowledge ID|NAME] [--embedding-model M [--connection C]] [--pending]
                          (re)index knowledge collections now; --pending only indexes files not indexed yet
  kera reindex --heads    fill the heading and file-name keyword index of chunks indexed before it existed
                          (no re-embedding, no lock on the chunk table; safe to repeat or stop)
`

// Command runs `kera reindex`. It indexes in this process; a running server
// may take some of the queued files (rows are claimed atomically).
func Command(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reindex", flag.ContinueOnError)
	all := fs.Bool("all", false, "every collection")
	kid := fs.String("knowledge", "", "one collection (id or name)")
	model := fs.String("embedding-model", "", "move to this embedding model (default: keep each collection's model)")
	conn := fs.String("connection", "", "connection of --embedding-model (default rag.embedding.connection)")
	pending := fs.Bool("pending", false, "only index files that are not indexed yet")
	heads := fs.Bool("heads", false, "fill the heading and file-name keyword index of existing chunks")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *all == (*kid != "") && !*pending && !*heads {
		return errors.New("usage: kera reindex --all | --knowledge ID|NAME | --pending | --heads")
	}
	env, err := config.Load(true)
	if err != nil {
		return err
	}
	keys, err := secret.Derive(env.SecretKey)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	st := settings.New(pool)
	reg := models.New(pool, keys, st, env.OllamaURL)
	s := New(pool, st, files.New(pool, st, env.DataDir), reg, reg)
	if *all || *kid != "" {
		if err := s.queueCLI(ctx, out, *all, *kid, *conn, *model); err != nil {
			return err
		}
	}
	if *heads {
		return s.backfillHeads(ctx, out)
	}
	return s.drain(ctx, out)
}

func (s *Service) queueCLI(ctx context.Context, out io.Writer, all bool, ref, conn, model string) error {
	rows, err := s.db.Query(ctx, `SELECT id, name FROM knowledge WHERE $1 OR id = $2 OR name = $2 ORDER BY name`, all, ref)
	if err != nil {
		return fmt.Errorf("list knowledge: %w", err)
	}
	type kn struct{ id, name string }
	ks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (kn, error) {
		var k kn
		return k, r.Scan(&k.id, &k.name)
	})
	if err != nil {
		return err
	}
	if len(ks) == 0 {
		return fmt.Errorf("no knowledge collection matches %q", ref)
	}
	for _, k := range ks {
		n, err := s.Reindex(ctx, k.id, conn, model)
		if err != nil {
			return fmt.Errorf("%s: %w", k.name, err)
		}
		fmt.Fprintf(out, "queued %d files of %q\n", n, k.name)
	}
	return nil
}

// drain indexes pending files until none is ready, then summarises.
func (s *Service) drain(ctx context.Context, out io.Writer) error {
	done, failed := 0, 0
	for ctx.Err() == nil {
		r, err := s.step(ctx)
		if err != nil {
			return err
		}
		if r == nil {
			break
		}
		var name string
		_ = s.db.QueryRow(ctx, `SELECT filename FROM file WHERE id = $1`, r.FileID).Scan(&name)
		if r.Err != nil {
			failed++
			fmt.Fprintf(out, "  failed  %s: %v\n", name, r.Err)
			continue
		}
		done++
		fmt.Fprintf(out, "  indexed %s (%d chunks)\n", name, r.Chunks)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var waiting, busy int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kf.status = 'pending'), count(*) FILTER (WHERE kf.status = 'processing')
		FROM knowledge_file kf`).Scan(&waiting, &busy); err != nil {
		return err
	}
	fmt.Fprintf(out, "indexed %d files, %d failed", done, failed)
	if waiting > 0 {
		fmt.Fprintf(out, "; %d wait for text extraction (done by kera serve)", waiting)
	}
	if busy > 0 {
		fmt.Fprintf(out, "; %d are being indexed by a running server", busy)
	}
	fmt.Fprintln(out)
	return nil
}
