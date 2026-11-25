package pipeline

// The built-in runner: with KERA_RUNNER_TOKEN_FILE set (the compose file does), Kera makes a runner token at its
// first start, keeps it in that file and registers the runner "builtin" with it. The runner container reads the
// same file, so a community install has working Kera-run pipelines without anyone creating a token by hand.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

const builtinRunner = "builtin"

var runnerTokenRe = regexp.MustCompile(`^` + runnerPrefix + `[0-9a-f]{40}$`)

// readRunnerToken is the token in the file, or "" when the file is missing or holds something else.
func readRunnerToken(file string) string {
	b, err := os.ReadFile(file)
	if tok := strings.TrimSpace(string(b)); err == nil && runnerTokenRe.MatchString(tok) {
		return tok
	}
	return ""
}

// writeRunnerToken replaces the file in one step (the runner may be reading it). 0644: the volume it lives in is
// shared with the runner container only.
func writeRunnerToken(file, tok string) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // gone after the rename; cleans up after a failure
	_, err = f.WriteString(tok + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(f.Name(), file)
	}
	return err
}

// ensureBuiltinRunner makes the file and the runner row agree on one token. It is safe to run in every Kera
// process at once: an advisory lock keeps one at a time, so two first starts cannot leave the file with one token
// and the row with another. The token is never logged.
func (s *Service) ensureBuiltinRunner(ctx context.Context, file string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kera-builtin-runner'))`); err != nil {
		return err
	}
	tok := readRunnerToken(file)
	if tok == "" {
		if tok, _, _, err = newRunnerToken(); err != nil {
			return err
		}
		if err := writeRunnerToken(file, tok); err != nil {
			return fmt.Errorf("write %s: %w", file, err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO pipeline_runner (id, name, token_hash, token_prefix, created_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name) DO UPDATE SET token_hash = EXCLUDED.token_hash, token_prefix = EXCLUDED.token_prefix
		WHERE pipeline_runner.token_hash <> EXCLUDED.token_hash`, ids.New(), builtinRunner, auth.HashAPIKey(tok), tok[:10], s.now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// startBuiltinRunner is what Mount calls. A failure is logged, not fatal: Kera works without the runner, and the
// log line says what to look at.
func (s *Service) startBuiltinRunner(ctx context.Context) {
	file := s.a.Env.RunnerTokenFile
	if file == "" {
		return
	}
	if err := s.ensureBuiltinRunner(ctx, file); err != nil {
		slog.Error("pipeline: built-in runner not set up; the runner container cannot sign in. "+
			"If the error is a permission one, the volume for "+filepath.Dir(file)+" was probably made by an older image: remove it and start again",
			"file", file, "err", err)
	}
}
