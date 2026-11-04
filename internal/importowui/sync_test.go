package importowui

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNextRun(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		now  string // RFC 3339
		loc  *time.Location
		want string
	}{
		{"2026-11-04T00:00:00Z", time.UTC, "2026-11-04T01:30:00Z"},     // later today
		{"2026-11-04T01:30:00Z", time.UTC, "2026-11-05T01:30:00Z"},     // exactly now -> tomorrow
		{"2026-11-04T02:00:00Z", time.UTC, "2026-11-05T01:30:00Z"},     // passed -> tomorrow
		{"2026-11-03T19:00:00Z", kolkata, "2026-11-04T01:30:00+05:30"}, // 00:30 IST -> 01:30 IST same local day
		{"2026-11-03T21:00:00Z", kolkata, "2026-11-05T01:30:00+05:30"}, // 02:30 IST -> next local day
		{"2026-12-31T23:00:00Z", time.UTC, "2027-01-01T01:30:00Z"},     // year rollover
		{"2026-03-07T12:00:00-05:00", ny, "2026-03-08T01:30:00-05:00"}, // day of the spring-forward switch
		{"2026-03-08T12:00:00-04:00", ny, "2026-03-09T01:30:00-04:00"}, // after it: EDT
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := nextRun(now, 1, 30, c.loc); !got.Equal(want) {
			t.Errorf("nextRun(%s, %s) = %s, want %s", c.now, c.loc, got.Format(time.RFC3339), c.want)
		}
	}
	for in, ok := range map[string]bool{"01:30": true, "23:59": true, "24:00": false, "": false, "0130": false} {
		if _, _, err := parseAt(in); (err == nil) != ok {
			t.Errorf("parseAt(%q) err = %v", in, err)
		}
	}
}

func TestLockOneAtATime(t *testing.T) {
	_, dst := fixture(t)
	ctx := context.Background()
	unlock, err := Lock(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(ctx, dst); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock: %v, want ErrBusy", err)
	}
	s := &syncer{db: dst, dsn: "postgres://unused"}
	if err := s.start(ctx, "admin"); !errors.Is(err, ErrBusy) {
		t.Fatalf("start while locked: %v, want ErrBusy", err)
	}
	if s.running.Load() {
		t.Fatal("running flag left set after a refused start")
	}
	unlock()
	unlock2, err := Lock(ctx, dst)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}

func TestLastSince(t *testing.T) {
	_, dst := fixture(t)
	ctx := context.Background()
	if got, err := LastSince(ctx, dst); err != nil || !got.IsZero() {
		t.Fatalf("no runs: %v %v", got, err)
	}
	_, err := dst.Exec(ctx, `INSERT INTO migration_run (source, flags, started_at, finished_at, error) VALUES
		('s', '{"dry_run":false,"only":null}', '2026-11-01T01:30:00Z', '2026-11-01T01:40:00Z', ''),
		('s', '{"dry_run":false,"only":null}', '2026-11-02T01:30:00Z', '2026-11-02T01:31:00Z', 'boom'),
		('s', '{"dry_run":true,"only":null}',  '2026-11-03T01:30:00Z', '2026-11-03T01:31:00Z', ''),
		('s', '{"dry_run":false,"only":["chats"]}', '2026-11-03T02:30:00Z', '2026-11-03T02:31:00Z', ''),
		('s', '{"dry_run":false,"no_files":true}', '2026-11-03T03:30:00Z', '2026-11-03T03:31:00Z', ''),
		('s', '{"dry_run":false,"only":null}', '2026-11-04T01:30:00Z', NULL, '')`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LastSince(ctx, dst)
	want := time.Date(2026, 11, 1, 0, 30, 0, 0, time.UTC)
	if err != nil || !got.Equal(want) {
		t.Fatalf("LastSince = %v %v, want %v (failed, dry, partial, blob-less and unfinished runs ignored)", got, err, want)
	}
}

// A re-import keeps Kera-side knowledge meta (a pending reindex, indexing
// errors) and never touches access that `kera apply` owns.
func TestResyncKeepsKeraState(t *testing.T) {
	src, dst := fixture(t)
	ctx := context.Background()
	// m1 is a config-managed model: the source grant on it must not be copied.
	if _, err := dst.Exec(ctx, `INSERT INTO model (id, name, managed) VALUES ('m1', 'M1', true)`); err != nil {
		t.Fatal(err)
	}
	opt := Options{NoFiles: true, PruneMissing: true, Log: discardLog()}
	rep, err := Run(ctx, src, dst, "test", opt)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stats["grants"].Skipped["access managed by config"] != 1 {
		t.Errorf("grant skips: %v", rep.Stats["grants"].Skipped)
	}
	if n := count(t, dst, `SELECT count(*) FROM access_grant WHERE resource_type = 'model' AND resource_id = 'm1'`); n != 0 {
		t.Errorf("grants on the managed model: %d", n)
	}

	// Kera starts a reindex and records an error; apply adopts the kb grant.
	// The source then changes the collection and drops the grant.
	if _, err := dst.Exec(ctx, `UPDATE knowledge SET meta = meta || '{"reindex_to":"emb-2","errors":{"f2":"no text"}}' WHERE id = 'kb1';
		UPDATE access_grant SET managed = true WHERE id = 'a1';
		UPDATE src.knowledge SET description = 'changed', meta = '{"note":"from source"}', updated_at = 1800000000 WHERE id = 'kb1';
		DELETE FROM src.access_grant WHERE id = 'a1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, src, dst, "test", opt); err != nil {
		t.Fatal(err)
	}
	if n := count(t, dst, `SELECT count(*) FROM knowledge WHERE id = 'kb1' AND description = 'changed'
		AND meta->>'reindex_to' = 'emb-2' AND meta->'errors'->>'f2' = 'no text' AND meta->>'note' = 'from source'`); n != 1 {
		var meta string
		_ = dst.QueryRow(ctx, `SELECT meta::text FROM knowledge WHERE id = 'kb1'`).Scan(&meta)
		t.Errorf("knowledge meta after re-import: %s", meta)
	}
	if n := count(t, dst, `SELECT count(*) FROM access_grant WHERE id = 'a1' AND managed`); n != 1 {
		t.Error("prune deleted a grant that kera apply adopted")
	}
}
