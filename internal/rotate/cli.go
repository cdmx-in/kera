package rotate

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/db"
	"github.com/cdmx-in/kera/internal/secret"
)

// CLIUsage is the help text of `kera secrets`.
const CLIUsage = `  kera secrets check      open every stored secret with the current key and print counts; exit 1 if any fails
  kera secrets rotate --old-key-file FILE --new-key-file FILE [--dry-run]
                          re-encrypt every stored secret from the old key to the new one (stop the server first)
`

// Command runs `kera secrets ...`.
func Command(ctx context.Context, args []string, out io.Writer) error {
	usage := errors.New("usage:\n" + CLIUsage)
	if len(args) == 0 {
		return usage
	}
	fs := flag.NewFlagSet("secrets "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var oldFile, newFile string
	var dry bool
	switch args[0] {
	case "check":
	case "rotate":
		fs.StringVar(&oldFile, "old-key-file", "", "file holding the key the data is encrypted with now")
		fs.StringVar(&newFile, "new-key-file", "", "file holding the key to encrypt with")
		fs.BoolVar(&dry, "dry-run", false, "open everything with the old key, report, change nothing")
	default:
		return usage
	}
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	if fs.NArg() > 0 || args[0] == "rotate" && (oldFile == "" || newFile == "") {
		return usage
	}

	if args[0] == "check" {
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
		return Check(ctx, pool, keys, out)
	}

	// rotate takes two keys, so the keys come from the flags and only the database address from the environment
	oldKey, err := config.ReadKeyFile("--old-key-file", oldFile)
	if err != nil {
		return err
	}
	newKey, err := config.ReadKeyFile("--new-key-file", newFile)
	if err != nil {
		return err
	}
	if oldKey == newKey {
		return errors.New("--old-key-file and --new-key-file hold the same key: nothing to rotate")
	}
	oldKeys, err := secret.Derive(oldKey)
	if err != nil {
		return err
	}
	newKeys, err := secret.Derive(newKey)
	if err != nil {
		return err
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
	return Rotate(ctx, pool, oldKeys, newKeys, dry, out)
}
