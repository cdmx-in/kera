package identity

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/settings"
)

// CLIUsage documents the identity subcommands for `kera help`.
const CLIUsage = `  kera user list                                  list users
  kera user promote <email>                       make a user an admin
  kera user login-link [--ttl 12h] <email>        print a sign-in link (no OAuth needed)
  kera apikey create --user <email> --name <n>    create an API key (printed once)
  kera apikey list --user <email>                 list a user's API keys
`

// UserCmd runs `kera user ...`.
func UserCmd(ctx context.Context, db *pgxpool.Pool, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: kera user list|promote <email>")
	}
	switch args[0] {
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
		fmt.Fprintln(out, args[1], "is now an admin")
		return nil
	}
	return fmt.Errorf("unknown user command %q", args[0])
}

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
		_, key, err := CreateAPIKey(ctx, db, userID, *name)
		if err != nil {
			return err
		}
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
