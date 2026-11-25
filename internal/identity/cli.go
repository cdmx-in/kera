package identity

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/audit"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/settings"
)

// CLIUsage documents the identity subcommands for `kera help`.
const CLIUsage = `  kera user list                                  list users
  kera user add [--name N] [--admin] [--password-stdin] <email>
                                                  create a user; with --password-stdin the first line of stdin is their password
  kera user passwd [--stdin] [--revoke-api-keys] <email>
                                                  set or reset a user's password and sign them out (asks twice without echo; --stdin reads one line, for scripts; API keys stay unless asked)
  kera user promote <email>                       make a user an admin
  kera user login-link [--ttl 12h] <email>        print a sign-in link (no password needed, recovery; creates the first admin on an empty install)
  kera apikey create --user <email> --name <n>    create an API key (printed once)
  kera apikey list --user <email>                 list a user's API keys
`

// UserCmd runs `kera user ...`. in is where passwords are read from.
func UserCmd(ctx context.Context, db *pgxpool.Pool, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: kera user list|add|passwd|promote <email>")
	}
	switch args[0] {
	case "add":
		return addUserCLI(ctx, db, args[1:], in, out)
	case "passwd":
		return passwdCLI(ctx, db, args[1:], in, out)
	case "list":
		return listUsersCLI(ctx, db, out)
	case "promote":
		if len(args) != 2 {
			return errors.New("usage: kera user promote <email>")
		}
		tag, err := db.Exec(ctx, `UPDATE "user" SET role = 'admin', updated_at = now() WHERE lower(email) = lower($1)`, args[1])
		if err != nil {
			return fmt.Errorf("promote: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("no user with email %s (they must sign in once first)", args[1])
		}
		audit.Record(audit.CLI(ctx, db), audit.Event{Action: "user.role_change", TargetType: "user", TargetLabel: strings.ToLower(args[1]), Meta: map[string]any{"to": "admin"}})
		fmt.Fprintln(out, args[1], "is now an admin")
		return nil
	}
	return fmt.Errorf("unknown user command %q", args[0])
}

func addUserCLI(ctx context.Context, db *pgxpool.Pool, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("user add", flag.ContinueOnError)
	name := fs.String("name", "", "display name (default: the part of the email before @)")
	admin := fs.Bool("admin", false, "make the user an admin")
	pwStdin := fs.Bool("password-stdin", false, "read the user's password from the first line of standard input")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || !strings.Contains(fs.Arg(0), "@") {
		return errors.New("usage: kera user add [--name N] [--admin] [--password-stdin] <email>")
	}
	email, err := normalizeEmail(fs.Arg(0))
	if err != nil {
		return err
	}
	role := "user"
	if *admin {
		role = "admin"
	}
	var hash string
	if *pwStdin {
		if f, ok := in.(*os.File); ok && isTerminal(f) {
			return errors.New("--password-stdin reads a pipe, but standard input is a terminal (the password would be echoed): pipe it in, or add the user without it and run kera user passwd")
		}
		pw, err := passwordFromStdin(in)
		if err != nil {
			return err
		}
		if err := auth.CheckPassword(pw, email); err != nil {
			return fmt.Errorf("password: %w", err)
		}
		if hash, err = auth.HashPassword(ctx, pw); err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	id := ids.New()
	tag, err := tx.Exec(ctx, `INSERT INTO "user" (id, name, email, role)
		VALUES ($1, COALESCE(NULLIF($2, ''), split_part($3, '@', 1)), $3, $4) ON CONFLICT DO NOTHING`, id, *name, email, role)
	if err != nil {
		return fmt.Errorf("add user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user %s already exists", email)
	}
	if hash != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO auth_password (user_id, hash) VALUES ($1, $2)`, id, hash); err != nil {
			return fmt.Errorf("store password: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	audit.Record(audit.CLI(ctx, db), audit.Event{Action: "user.create", TargetID: id, TargetLabel: email, Meta: map[string]any{"role": role, "password_set": hash != ""}})
	if hash != "" {
		fmt.Fprintf(out, "added %s as %s with a password; they sign in at the web address with that email and password\n", email, role)
		return nil
	}
	fmt.Fprintf(out, "added %s as %s; give them a password with: kera user passwd %s (or a one-time link: kera user login-link %s)\n", email, role, email, email)
	return nil
}

// passwdCLI sets a user's password and signs them out everywhere. The password
// is typed at a terminal or piped in with --stdin, never given as an argument
// (arguments show up in the process list and the shell history).
func passwdCLI(ctx context.Context, db *pgxpool.Pool, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("user passwd", flag.ContinueOnError)
	stdin := fs.Bool("stdin", false, "read the password from the first line of standard input instead of asking")
	revoke := fs.Bool("revoke-api-keys", false, "also delete the user's API keys (a password change signs sessions out, not keys)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: kera user passwd [--stdin] [--revoke-api-keys] <email>")
	}
	var id, email string
	err := db.QueryRow(ctx, `SELECT id, email FROM "user" WHERE lower(email) = lower($1)`, strings.TrimSpace(fs.Arg(0))).Scan(&id, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no user with email %s (create one with: kera user add --password-stdin %s)", fs.Arg(0), fs.Arg(0))
	}
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	var pw string
	if *stdin {
		if f, ok := in.(*os.File); ok && isTerminal(f) {
			return errors.New("--stdin reads a pipe, but standard input is a terminal: leave --stdin out to be asked (nothing is echoed), or pipe the password in")
		}
		pw, err = passwordFromStdin(in)
	} else {
		pw, err = promptPassword(in, out)
	}
	if err != nil {
		return err
	}
	if err := auth.CheckPassword(pw, email); err != nil {
		return fmt.Errorf("password: %w", err)
	}
	if _, err := storePassword(ctx, db, id, pw, *revoke); err != nil {
		return err
	}
	audit.Record(audit.CLI(ctx, db), audit.Event{Action: "user.password_set", TargetID: id, TargetLabel: email, Meta: map[string]any{"api_keys_revoked": *revoke}})
	fmt.Fprintf(out, "password set for %s; their other sessions are signed out\n", email)
	if *revoke {
		fmt.Fprintln(out, "their API keys are deleted")
	} else {
		fmt.Fprintln(out, "their API keys still work; if the account may be compromised, rerun with --revoke-api-keys")
	}
	// The server counts failed tries in memory and this is another process, so it cannot clear a lock.
	fmt.Fprintln(out, "if they are locked out by too many tries, the lock ends within 15 minutes, when the server restarts, or at once when an admin sets a password in Admin > Users")
	return nil
}

// promptPassword asks twice on the terminal, without echo.
func promptPassword(in io.Reader, out io.Writer) (string, error) {
	f, ok := in.(*os.File)
	if !ok || !isTerminal(f) {
		return "", errors.New("standard input is not a terminal: type the password at a terminal (docker compose exec, without -T), or pipe it in with --stdin")
	}
	first, err := readSecret(f, "New password: ", out)
	if err != nil {
		return "", err
	}
	again, err := readSecret(f, "Repeat it: ", out)
	if err != nil {
		return "", err
	}
	if first != again {
		return "", errors.New("the two passwords differ; nothing was changed")
	}
	return first, nil
}

// passwordFromStdin reads the first line of in (at most 4 KiB), without its line end.
func passwordFromStdin(in io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(in, 4096)).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", errors.New("no password on standard input")
	}
	return trimEOL(line), nil
}

// trimEOL drops one trailing LF or CRLF.
func trimEOL(s string) string { return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r") }

func listUsersCLI(ctx context.Context, db *pgxpool.Pool, out io.Writer) error {
	rows, err := db.Query(ctx, `SELECT email, role, name, last_active_at FROM "user" ORDER BY created_at, id`)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tROLE\tNAME\tLAST ACTIVE")
	for rows.Next() {
		var email, role, name string
		var seen time.Time
		if err := rows.Scan(&email, &role, &name, &seen); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", email, role, name, seen.UTC().Format(time.DateTime))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tw.Flush()
}

// APIKeyCmd runs `kera apikey ...`. warn receives notices that are not output.
func APIKeyCmd(ctx context.Context, db *pgxpool.Pool, args []string, out, warn io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: kera apikey create|list --user <email>")
	}
	fs := flag.NewFlagSet("apikey "+args[0], flag.ContinueOnError)
	fs.SetOutput(warn)
	email := fs.String("user", "", "user email")
	name := fs.String("name", "", "key name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("--user <email> is required")
	}
	var userID string
	err := db.QueryRow(ctx, `SELECT id FROM "user" WHERE lower(email) = lower($1)`, *email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no user with email %s", *email)
	}
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	switch args[0] {
	case "create":
		keyID, key, err := CreateAPIKey(ctx, db, userID, *name)
		if err != nil {
			return err
		}
		audit.Record(audit.CLI(ctx, db), audit.Event{Action: "apikey.create", TargetID: keyID, TargetLabel: *name, Meta: map[string]any{"owner": strings.ToLower(*email)}})
		u, err := auth.New(db, nil, settings.New(db), false).LoadUser(ctx, userID)
		if err == nil && !u.Can("features.api_keys") {
			fmt.Fprintln(warn, "warning: this user lacks the features.api_keys permission, so the key will not authenticate until it is granted")
		}
		fmt.Fprintln(out, key)
		return nil
	case "list":
		keys, err := ListAPIKeys(ctx, db, userID)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tCREATED\tLAST USED")
		for _, k := range keys {
			used := "never"
			if k.LastUsedAt != nil {
				used = time.Unix(*k.LastUsedAt, 0).UTC().Format(time.DateTime)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Prefix, time.Unix(k.CreatedAt, 0).UTC().Format(time.DateTime), used)
		}
		return tw.Flush()
	}
	return fmt.Errorf("unknown apikey command %q", args[0])
}
