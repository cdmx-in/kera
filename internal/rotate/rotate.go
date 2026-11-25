// Package rotate implements `kera secrets check` and `kera secrets rotate`: they find every value that Kera stored
// encrypted with the key derived from KERA_SECRET_KEY, test that it still opens, and move it to a new key.
//
// Everything stored this way is listed once, as data, in locations. To find locations that a new feature adds,
// search for the callers of secret.Keys Encrypt, EncryptString, Decrypt and DecryptString (and apply.SealToolKey,
// apply.SealSecret) and for bytea columns or "_enc" fields in internal/db/migrations. Ciphertexts that are
// never stored (the short-lived OAuth flow cookies, run tokens, sessions) are signed or sealed per request and
// need nothing here: a new key simply invalidates them.
package rotate

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/secret"
)

// Location is one column that holds ciphertext made by secret.Keys.Encrypt (AES-GCM, nonce||ciphertext).
type Location struct {
	Table, Column string
	// Field is empty when Column is a bytea holding the ciphertext. Otherwise Column is jsonb and the ciphertext
	// is the base64 (standard alphabet) text under the key Field of that object.
	Field string
	// Key lists the text primary-key columns: they find the row again for the update and name it in messages.
	Key []string
	// Where narrows the read; Args are its parameters ($1...).
	Where string
	Args  []any
}

// locations is every stored ciphertext, the whole list of what a key change touches. A NULL column (or an
// empty or missing Field) holds no ciphertext: EncryptString("") is stored as NULL.
var locations = []Location{
	{Table: "connection", Column: "api_key_enc", Key: []string{"id"}},
	{Table: "oauth_provider", Column: "client_secret_enc", Key: []string{"id"}},
	{Table: "oauth_token", Column: "token_enc", Key: []string{"id"}},
	{Table: "hook_server", Column: "key_enc", Key: []string{"id"}},
	{Table: "pipeline_env", Column: "value_enc", Key: []string{"pipeline_id", "name"}}, // an empty value is a non-empty ciphertext
	{Table: "tool_server", Column: "auth", Field: "key_enc", Key: []string{"id"}},      // apply.SealToolKey
	// secret settings, stored as {"enc": "<base64>"} (apply.SealSecret); other config rows are plain JSON
	{Table: "config", Column: "value", Field: "enc", Key: []string{"key"}, Where: "key = ANY($1)", Args: []any{secretSettings()}},
}

func secretSettings() []string {
	var keys []string
	for k, secret := range apply.SettingKeys() {
		if secret {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// Name is how messages call the location: table.column, plus .field for a JSON column.
func (l Location) Name() string {
	if l.Field != "" {
		return l.Table + "." + l.Column + "." + l.Field
	}
	return l.Table + "." + l.Column
}

func (l Location) keyList() string { return strings.Join(l.Key, ", ") }

func (l Location) selectSQL() string {
	ct := l.Column
	if l.Field != "" {
		ct = l.Column + "->>'" + l.Field + "'"
	}
	where := ct + " IS NOT NULL"
	if l.Where != "" {
		where += " AND (" + l.Where + ")"
	}
	return "SELECT ARRAY[" + l.keyList() + "], " + ct + " FROM " + l.Table + " WHERE " + where + " ORDER BY " + l.keyList()
}

func (l Location) updateSQL() string {
	set := l.Column + " = $1"
	if l.Field != "" {
		set = l.Column + " = jsonb_set(" + l.Column + ", '{" + l.Field + "}', to_jsonb($1::text))"
	}
	where := make([]string, len(l.Key))
	for i, k := range l.Key {
		where[i] = fmt.Sprintf("%s = $%d", k, i+2)
	}
	return "UPDATE " + l.Table + " SET " + set + " WHERE " + strings.Join(where, " AND ")
}

// querier is a pool or a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// item is one stored ciphertext. plain is set when it opened, err when it did not.
type item struct {
	loc    *Location
	key    []string
	sealed []byte
	plain  []byte
	err    error
}

func (i item) row() string { return strings.Join(i.key, "/") }

// scan reads every ciphertext of every location and opens it with keys. A row that does not open is an item
// with err set; only a failing query is an error.
func scan(ctx context.Context, q querier, keys *secret.Keys) ([]item, error) {
	var out []item
	for i := range locations {
		l := &locations[i]
		rows, err := q.Query(ctx, l.selectSQL(), l.Args...)
		if err != nil {
			return nil, queryErr(l, err)
		}
		for rows.Next() {
			it := item{loc: l}
			var text string
			if l.Field == "" {
				err = rows.Scan(&it.key, &it.sealed)
			} else if err = rows.Scan(&it.key, &text); err == nil && text != "" {
				it.sealed, it.err = base64.StdEncoding.DecodeString(text)
			}
			if err != nil {
				rows.Close()
				return nil, queryErr(l, err)
			}
			if len(it.sealed) == 0 && it.err == nil { // nothing stored (an empty field is "no key", as in SealToolKey)
				continue
			}
			if it.err == nil {
				it.plain, it.err = keys.Decrypt(it.sealed)
			}
			out = append(out, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, queryErr(l, err)
		}
	}
	return out, nil
}

func queryErr(l *Location, err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "42P01" { // undefined_table
		return fmt.Errorf("read %s: %w (has kera db migrate been run?)", l.Name(), err)
	}
	return fmt.Errorf("read %s: %w", l.Name(), err)
}

type tally struct {
	name       string
	ok, failed int
}

// maxListed caps the failing rows printed per location.
const maxListed = 20

// printTallies prints one line per location and then the failing rows. Row names and counts only, never values.
func printTallies(out io.Writer, items []item, okWord string) (ok, failed int) {
	ts := make([]tally, len(locations))
	for i := range locations {
		ts[i].name = locations[i].Name()
	}
	for _, it := range items {
		t := &ts[slices.IndexFunc(ts, func(t tally) bool { return t.name == it.loc.Name() })]
		if it.err != nil {
			t.failed++
		} else {
			t.ok++
		}
	}
	for _, t := range ts {
		line := fmt.Sprintf("  %-34s %4d %s", t.name, t.ok, okWord)
		if t.failed > 0 {
			line += fmt.Sprintf(", %d FAILED", t.failed)
		}
		fmt.Fprintln(out, line)
		ok, failed = ok+t.ok, failed+t.failed
	}
	listed := map[string]int{}
	for _, it := range items {
		if it.err == nil {
			continue
		}
		if listed[it.loc.Name()]++; listed[it.loc.Name()] <= maxListed {
			fmt.Fprintf(out, "  FAILED %s %s: %v\n", it.loc.Name(), it.row(), it.err)
		} else if listed[it.loc.Name()] == maxListed+1 {
			fmt.Fprintf(out, "  FAILED %s: more rows follow\n", it.loc.Name())
		}
	}
	return ok, failed
}

// Check opens every stored ciphertext with keys, prints the counts and returns an error when any does not open.
func Check(ctx context.Context, db querier, keys *secret.Keys, out io.Writer) error {
	items, err := scan(ctx, db, keys)
	if err != nil {
		return err
	}
	ok, failed := printTallies(out, items, "ok")
	if failed > 0 {
		return fmt.Errorf("%d of %d stored secrets do not open with the current key (is this the key the data was encrypted with?)", failed, ok+failed)
	}
	fmt.Fprintf(out, "all %d stored secrets open with the current key\n", ok)
	return nil
}
