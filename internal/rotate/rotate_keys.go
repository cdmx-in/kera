package rotate

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/secret"
)

// advisoryLock is the pg_advisory_xact_lock key that keeps two rotations from running at once.
const advisoryLock int64 = 0x6b65726173656372

// Reminder is printed after a rotation (not after a dry run).
const Reminder = `Done. What happens next:
  - Start Kera with the new key (KERA_SECRET_KEY, or the file KERA_SECRET_KEY_FILE names). A server that was running
    must be restarted: it caches decrypted values and still signs with the old key.
  - Everyone is signed out: sessions are signed with the key. Tool and hook callbacks issued in the last 15 minutes
    and pipeline runs that are open now lose their callbacks, and fail or time out.
  - Run "kera secrets check" with the new key. Keep that key out of the backup of the data.
`

// Rotate re-encrypts every stored ciphertext from oldKeys to newKeys in one transaction. It first opens everything
// with oldKeys and stops, changing nothing, if any row does not open; after the writes it opens everything with
// newKeys, still inside the transaction, and commits only when each value is what it was. A dry run does the first
// pass and writes nothing. Stop the server first: it would keep writing with the old key.
func Rotate(ctx context.Context, pool *pgxpool.Pool, oldKeys, newKeys *secret.Keys, dryRun bool, out io.Writer) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // after Commit this is a no-op
	var got bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, advisoryLock).Scan(&got); err != nil {
		return err
	}
	if !got {
		return errors.New("another rotation is running (it holds the advisory lock); wait for it to finish")
	}
	// Block writers on the tables we touch until we commit, but give up rather than hang behind a long transaction.
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		return err
	}
	var tables []string
	for _, l := range locations {
		if !slices.Contains(tables, l.Table) {
			tables = append(tables, l.Table)
		}
	}
	slices.Sort(tables) // one order everywhere, so two sessions cannot deadlock
	for _, t := range tables {
		if _, err := tx.Exec(ctx, "LOCK TABLE "+t+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return fmt.Errorf("lock %s (is a long transaction or the server holding it?): %w", t, err)
		}
	}

	items, err := scan(ctx, tx, oldKeys)
	if err != nil {
		return err
	}
	failed := slices.ContainsFunc(items, func(it item) bool { return it.err != nil })
	if failed || dryRun {
		printTallies(out, items, "open with the old key")
	}
	if failed {
		return errors.New("some stored secrets do not open with the old key, so nothing was changed (is --old-key-file the key the server uses now?)")
	}
	if dryRun {
		fmt.Fprintf(out, "dry run: %d stored secrets would be re-encrypted; nothing was changed\n", len(items))
		return nil
	}

	for i := range items {
		it := &items[i]
		sealed, err := newKeys.Encrypt(it.plain)
		if err != nil {
			return err
		}
		var arg any = sealed
		if it.loc.Field != "" {
			arg = base64.StdEncoding.EncodeToString(sealed)
		}
		args := append([]any{arg}, stringsToAny(it.key)...)
		tag, err := tx.Exec(ctx, it.loc.updateSQL(), args...)
		if err != nil {
			return fmt.Errorf("write %s %s: %w", it.loc.Name(), it.row(), err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("write %s %s: %d rows changed, want 1", it.loc.Name(), it.row(), tag.RowsAffected())
		}
	}

	after, err := scan(ctx, tx, newKeys)
	if err != nil {
		return err
	}
	if len(after) != len(items) {
		return fmt.Errorf("verify: %d stored secrets after the rewrite, %d before; nothing was changed", len(after), len(items))
	}
	for i := range after { // same query, same order
		if after[i].err != nil || after[i].loc != items[i].loc || after[i].row() != items[i].row() || !bytes.Equal(after[i].plain, items[i].plain) {
			return fmt.Errorf("verify: %s %s does not come back unchanged; nothing was changed", items[i].loc.Name(), items[i].row())
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	printTallies(out, after, "re-encrypted")
	fmt.Fprintf(out, "%d stored secrets re-encrypted with the new key\n", len(after))
	fmt.Fprint(out, Reminder)
	return nil
}

func stringsToAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
