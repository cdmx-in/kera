package importowui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/auth"
)

// KeyCheck is the outcome of authenticating every imported API key.
type KeyCheck struct {
	Checked, OK int
	Failed      []string // source key ids that do not resolve to their owner
}

// Verification compares a source with its imported copy.
type Verification struct {
	Report *Report
	Keys   KeyCheck
}

// OK reports whether every expected row is in the target, no extra imported
// rows remain, and every API key resolves to its owner.
func (v Verification) OK() bool {
	for _, s := range v.Report.Stats {
		if s.Inserted > 0 || s.Pruned > 0 {
			return false
		}
	}
	return len(v.Keys.Failed) == 0
}

// Verify replays the whole import inside a transaction that is rolled back:
// a row the replay would insert is missing from the target, a row it would
// prune is extra. Skips are decided by the same code as the import, so
// documented skips never count as mismatches. It then checks that each
// imported API key's hash resolves to the right user.
func Verify(ctx context.Context, src, dst *pgxpool.Pool) (Verification, error) {
	rep, users, err := execute(ctx, src, dst, Options{DryRun: true, NoFiles: true, PruneMissing: true, Log: discardLog()})
	if err != nil {
		return Verification{}, err
	}
	keys, err := checkKeys(ctx, src, dst, users)
	return Verification{Report: rep, Keys: keys}, err
}

func checkKeys(ctx context.Context, src, dst *pgxpool.Pool, users map[string]string) (KeyCheck, error) {
	var kc KeyCheck
	rows, err := src.Query(ctx, `SELECT id, user_id, coalesce(key, '') FROM api_key
		WHERE coalesce(expires_at, 0) = 0 OR expires_at > extract(epoch FROM now()) ORDER BY id`)
	if err != nil {
		return kc, fmt.Errorf("list source keys: %w", err)
	}
	type key struct{ id, user, key string }
	var keys []key
	var k key
	if _, err := pgx.ForEachRow(rows, []any{&k.id, &k.user, &k.key}, func() error { keys = append(keys, k); return nil }); err != nil {
		return kc, fmt.Errorf("list source keys: %w", err)
	}
	for _, k := range keys {
		owner, ok := users[k.user]
		if !ok || !auth.IsAPIKey(k.key) {
			continue // skipped by the import, reported there
		}
		kc.Checked++
		var got string
		err := dst.QueryRow(ctx, `SELECT user_id FROM api_key WHERE key_hash = $1`, auth.HashAPIKey(k.key)).Scan(&got)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return kc, fmt.Errorf("look up key %s: %w", k.id, err)
		}
		if got == owner {
			kc.OK++
		} else {
			kc.Failed = append(kc.Failed, k.id)
		}
	}
	return kc, nil
}

// PrintVerification writes the counts table and the key check.
func PrintVerification(w io.Writer, v Verification) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "entity\tsource\tskipped\texpected\tin target\tmissing\textra\t")
	for _, name := range v.Report.Order {
		s := v.Report.Stats[name]
		exp := s.Source - s.SkippedTotal()
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t\n", name, s.Source, s.SkippedTotal(), exp, s.Existing, s.Inserted, s.Pruned)
	}
	tw.Flush()
	printReasons(w, v.Report, false)
	fmt.Fprintf(w, "\napi keys: %d of %d resolve to their owner\n", v.Keys.OK, v.Keys.Checked)
	if len(v.Keys.Failed) > 0 {
		fmt.Fprintf(w, "keys not resolving: %s\n", strings.Join(v.Keys.Failed, ", "))
	}
	if v.OK() {
		fmt.Fprintln(w, "verify: OK")
	} else {
		fmt.Fprintln(w, "verify: MISMATCH")
	}
}

// PrintReport writes an import report: the counts table, skip reasons and
// notes, and (for dry runs) the first decisions per entity and missing blobs.
func PrintReport(w io.Writer, r *Report, dry bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "entity\tsource\tinserted\texisting\tskipped\tpruned\t")
	for _, name := range r.Order {
		s := r.Stats[name]
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t\n", name, s.Source, s.Inserted, s.Existing, s.SkippedTotal(), s.Pruned)
	}
	tw.Flush()
	printReasons(w, r, true)
	if dry {
		for _, name := range r.Order {
			if d := r.Stats[name].Decisions; len(d) > 0 {
				fmt.Fprintf(w, "\n%s (first %d decisions):\n  %s\n", name, len(d), strings.Join(d, "\n  "))
			}
		}
	}
	if n := len(r.MissingBlobs); n > 0 {
		shown := r.MissingBlobs[:min(n, 50)]
		fmt.Fprintf(w, "\nmissing blobs (%d): %s\n", n, strings.Join(shown, ", "))
	}
}

func printReasons(w io.Writer, r *Report, notes bool) {
	for _, name := range r.Order {
		s := r.Stats[name]
		for _, k := range sortedKeys(s.Skipped) {
			fmt.Fprintf(w, "  %s skipped: %s (%d)\n", name, k, s.Skipped[k])
		}
		for _, k := range sortedKeys(s.Notes) {
			if !notes {
				break
			}
			fmt.Fprintf(w, "  %s note: %s (%d)\n", name, k, s.Notes[k])
		}
	}
}
