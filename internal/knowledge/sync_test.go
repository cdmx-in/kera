package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/retrieval"
	"github.com/cdmx-in/kera/internal/tools"
)

const (
	docFox  = "# Fox\nThe quick brown fox jumps over the lazy dog."
	docJugs = "# Jugs\nPack my box with five dozen liquor jugs."
	docTea  = "# Tea\nBrew the leaves for three minutes in hot water."
)

// syncEnv is a collection a pipeline syncs into. The ingest worker is stopped:
// the tests move the queue by hand with drain.
type syncEnv struct {
	*env
	kid string
}

func newSync(t *testing.T) *syncEnv {
	t.Helper()
	e := setup(t)
	e.s.Close()
	c, err := e.s.CreateFor(context.Background(), "root", "Product docs", "")
	if err != nil {
		t.Fatal(err)
	}
	return &syncEnv{env: e, kid: c.ID}
}

func (e *syncEnv) scope(run int64) SyncScope {
	return SyncScope{Pipeline: "docs-sync", Knowledge: e.kid, Run: run}
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// listing builds a listing from name, content pairs.
func listing(kv ...string) []SyncFile {
	var out []SyncFile
	for i := 0; i < len(kv); i += 2 {
		out = append(out, SyncFile{Name: kv[i], SHA256: sum(kv[i+1]), Size: int64(len(kv[i+1]))})
	}
	return out
}

func (e *syncEnv) plan(run int64, kv ...string) SyncPlanResult {
	e.t.Helper()
	r, err := e.s.SyncPlan(context.Background(), SyncPlanReq{SyncScope: e.scope(run), Files: listing(kv...)})
	if err != nil {
		e.t.Fatalf("plan: %v", err)
	}
	return r
}

// put sends one file and waits until its text is extracted (not indexed).
func (e *syncEnv) put(run int64, name, content string) SyncPutResult {
	e.t.Helper()
	r, err := e.s.SyncPut(context.Background(), SyncPutReq{SyncScope: e.scope(run), Owner: "root", Name: name, SHA256: sum(content), Body: strings.NewReader(content)})
	if err != nil {
		e.t.Fatalf("put %s: %v", name, err)
	}
	if _, err := e.s.files.Wait(context.Background(), r.FileID, 15*time.Second); err != nil {
		e.t.Fatal(err)
	}
	return r
}

// drain indexes every pending row and returns how many it processed.
func (e *syncEnv) drain() int {
	e.t.Helper()
	n := 0
	for {
		did, err := e.s.Step(context.Background())
		if err != nil {
			e.t.Fatalf("step: %v", err)
		}
		if !did {
			return n
		}
		n++
	}
}

// push is a whole run over a folder: plan, put what the plan asks for, index.
func (e *syncEnv) push(run int64, kv ...string) SyncPlanResult {
	e.t.Helper()
	p := e.plan(run, kv...)
	content := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		content[kv[i]] = kv[i+1]
	}
	for _, n := range p.Upload {
		e.put(run, n, content[n])
	}
	e.drain()
	return p
}

func (e *syncEnv) state(run int64) SyncStatus {
	e.t.Helper()
	st, err := e.s.SyncState(context.Background(), e.scope(run))
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *syncEnv) search(q string) []retrieval.Hit {
	e.t.Helper()
	hits, err := e.s.Search(context.Background(), e.users2["root"], []string{e.kid}, q, 5)
	if err != nil {
		e.t.Fatalf("search: %v", err)
	}
	return hits
}

// seen is the text of everything a search returns.
func (e *syncEnv) seen(q string) string {
	var b strings.Builder
	for _, h := range e.search(q) {
		b.WriteString(h.Text + "\n")
	}
	return b.String()
}

func (e *syncEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.s.db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// staged counts the staged rows of the collection.
func (e *syncEnv) staged() int {
	return e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND replaces IS NOT NULL`, e.kid)
}

func (e *syncEnv) fileExists(fid string) bool {
	return e.count(`SELECT count(*) FROM file WHERE id = $1`, fid) == 1
}

func (e *syncEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.s.db.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// snapshot renders everything a sync call may write.
func (e *syncEnv) snapshot() string {
	var s string
	err := e.s.db.QueryRow(context.Background(), `SELECT
		(SELECT coalesce(string_agg(concat_ws('|', file_id, status, origin, replaces, sync_run), ',' ORDER BY file_id), '') FROM knowledge_file WHERE knowledge_id = $1)
		|| '#' || (SELECT count(*) FROM file) || '#' || (SELECT meta::text || updated_at::text FROM knowledge WHERE id = $1)`, e.kid).Scan(&s)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func names(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", label, got, want)
	}
}

func TestSyncPlanClassifies(t *testing.T) {
	e := newSync(t)
	// New: nothing there yet.
	p := e.plan(1, "a.md", docFox, "b.md", docTea)
	names(t, "upload", p.Upload, "a.md", "b.md")
	names(t, "unchanged", p.Unchanged)
	e.push(1, "a.md", docFox, "b.md", docTea)
	if st := e.state(1); st.Indexed != 2 || st.Waiting != 0 || st.Failed != 0 || !st.Settled() {
		t.Fatalf("state after the first run: %+v", st)
	}
	// Unchanged: the same bytes.
	p = e.plan(2, "a.md", docFox, "b.md", docTea)
	names(t, "unchanged", p.Unchanged, "a.md", "b.md")
	names(t, "upload", p.Upload)
	names(t, "stale", p.Stale)
	// Changed: one name has new bytes; a new name appears.
	p = e.plan(3, "a.md", docJugs, "b.md", docTea, "c.md", docFox)
	names(t, "upload", p.Upload, "a.md", "c.md")
	names(t, "unchanged", p.Unchanged, "b.md")
	e.push(3, "a.md", docJugs, "b.md", docTea, "c.md", docFox)
	// Stale: not listed any more.
	p = e.plan(4, "a.md", docJugs)
	names(t, "stale", p.Stale, "b.md", "c.md")
	if p.Refused != "" {
		t.Fatalf("refused %q", p.Refused)
	}
	// A staged version with the same sha is received, not uploaded again.
	e.put(5, "a.md", docFox)
	p = e.plan(5, "a.md", docFox, "b.md", docTea)
	names(t, "received", p.Received, "a.md")
	names(t, "unchanged", p.Unchanged, "b.md")
	names(t, "upload", p.Upload)
	if e.staged() != 1 {
		t.Fatalf("staged %d", e.staged())
	}
	// The source went back to the live content: the staged leftover goes.
	p = e.plan(6, "a.md", docJugs, "b.md", docTea)
	names(t, "unchanged", p.Unchanged, "a.md", "b.md")
	if e.staged() != 0 {
		t.Fatalf("staged leftover kept: %d", e.staged())
	}
	// A staged version of a name that is no longer listed goes too.
	e.put(7, "a.md", docFox)
	e.plan(8, "b.md", docTea)
	if e.staged() != 0 {
		t.Fatalf("staged version of an unlisted name kept: %d", e.staged())
	}
}

func TestSyncPlanChecksTheListing(t *testing.T) {
	e := newSync(t)
	ok := sum("x")
	for name, files := range map[string][]SyncFile{
		"directory":  {{Name: "docs/a.md", SHA256: ok}},
		"dotfile":    {{Name: ".hidden.md", SHA256: ok}},
		"empty name": {{Name: "", SHA256: ok}},
		"short sha":  {{Name: "a.md", SHA256: "abc"}},
		"not hex":    {{Name: "a.md", SHA256: strings.Repeat("z", 64)}},
		"duplicate":  {{Name: "a.md", SHA256: ok}, {Name: "a.md", SHA256: ok}},
	} {
		_, err := e.s.SyncPlan(context.Background(), SyncPlanReq{SyncScope: e.scope(1), Files: files})
		var ae *api.Error
		if !errors.As(err, &ae) || ae.Status != 400 {
			t.Fatalf("%s: err %v, want a 400", name, err)
		}
	}
	// An upper-case sha is the same sha.
	if _, err := e.s.SyncPlan(context.Background(), SyncPlanReq{SyncScope: e.scope(1), Files: []SyncFile{{Name: "a.md", SHA256: strings.ToUpper(ok)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SyncPlan(context.Background(), SyncPlanReq{SyncScope: SyncScope{Pipeline: "p", Knowledge: "nowhere", Run: 1}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing collection: %v", err)
	}
	if _, err := e.s.CreateFor(context.Background(), "root", "  ", ""); err == nil {
		t.Fatal("blank collection name accepted")
	}
}

// Search and Documents keep returning the old text until the worker has
// indexed the new version; then only the new one, and the old version is gone
// from every table (all its chunks, whatever the embedding model).
func TestSyncSwap(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	e.push(1, "a.md", docFox)
	old := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, e.kid)
	var oldID string
	if err := e.s.db.QueryRow(ctx, `SELECT file_id FROM knowledge_file WHERE knowledge_id = $1`, e.kid).Scan(&oldID); err != nil || old != 1 {
		t.Fatalf("base row: %v (%d rows)", err, old)
	}
	// a chunk of another embedding model, as left by a half-done move
	other, err := e.s.ensureModel(ctx, "ollama", "wide")
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO chunk (knowledge_id, file_id, idx, text, embedding_model_id) VALUES ($1, $2, 99, 'stray', $3)`, e.kid, oldID, other.ID)
	// and an indexing error recorded for the old file
	if err := e.s.setFileError(ctx, e.kid, oldID, errors.New("old failure")); err != nil {
		t.Fatal(err)
	}

	e.plan(2, "a.md", docJugs)
	put := e.put(2, "a.md", docJugs)
	if put.Replaces != oldID {
		t.Fatalf("replaces %q, want %q", put.Replaces, oldID)
	}
	if got := e.seen("fox liquor"); !strings.Contains(got, "fox") || strings.Contains(got, "liquor") {
		t.Fatalf("before the swap a search saw %q", got)
	}
	docs, err := e.s.Documents(ctx, e.users2["root"], e.kid)
	if err != nil || len(docs) != 1 || !strings.Contains(docs[0].Text, "fox") || docs[0].FileID != oldID {
		t.Fatalf("documents before the swap: %+v %v", docs, err)
	}
	if st := e.state(2); st.Waiting != 1 || st.Indexed != 0 || st.Settled() {
		t.Fatalf("state with a staged file: %+v", st)
	}

	if n := e.drain(); n != 1 {
		t.Fatalf("drain processed %d rows", n)
	}
	if got := e.seen("fox liquor"); !strings.Contains(got, "liquor") || strings.Contains(got, "fox") {
		t.Fatalf("after the swap a search saw %q", got)
	}
	docs, _ = e.s.Documents(ctx, e.users2["root"], e.kid)
	if len(docs) != 1 || !strings.Contains(docs[0].Text, "liquor") || docs[0].FileID != put.FileID {
		t.Fatalf("documents after the swap: %+v", docs)
	}
	if e.fileExists(oldID) || e.count(`SELECT count(*) FROM chunk WHERE file_id = $1`, oldID) != 0 {
		t.Fatal("the old file or its chunks survived the swap")
	}
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, e.kid) != 1 || e.staged() != 0 {
		t.Fatal("rows after the swap")
	}
	if e.count(`SELECT count(*) FROM knowledge WHERE id = $1 AND meta->'errors' ? $2`, e.kid, oldID) != 0 {
		t.Fatal("the old file's error entry survived the swap")
	}
	if st := e.state(2); st.Indexed != 1 || !st.Settled() {
		t.Fatalf("state after the swap: %+v", st)
	}

	// A put after a finished swap stages against the new base.
	p := e.plan(3, "a.md", docTea)
	names(t, "upload", p.Upload, "a.md")
	if again := e.put(3, "a.md", docTea); again.Replaces != put.FileID {
		t.Fatalf("second put replaces %q, want the swapped-in %q", again.Replaces, put.FileID)
	}
	e.drain()
	if got := e.seen("fox liquor tea"); !strings.Contains(got, "leaves") || strings.Contains(got, "liquor") {
		t.Fatalf("after the second swap a search saw %q", got)
	}
}

// While versions swap, the collection always holds the chunks of exactly one
// version and one live row (one statement, one snapshot), and a search never
// returns both.
// ponytail: a search that straddles the commit can still come back empty for
// that document: Search reads candidates and chunks in three queries, not one
// snapshot. The same holds for any re-index; a snapshot-wide search is the fix.
func TestSyncSwapIsAtomic(t *testing.T) {
	e := newSync(t)
	e.push(1, "a.md", docFox)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	bad := make(chan string, 1)
	fail := func(msg string) {
		select {
		case bad <- msg:
		default:
		}
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			var files, live int
			if err := e.s.db.QueryRow(context.Background(), `SELECT
				(SELECT count(DISTINCT file_id) FROM chunk WHERE knowledge_id = $1),
				(SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND replaces IS NULL)`, e.kid).Scan(&files, &live); err != nil {
				return
			}
			if files != 1 || live != 1 {
				fail("chunks of " + string(rune('0'+files)) + " versions, " + string(rune('0'+live)) + " live rows")
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			hits, err := e.s.Search(context.Background(), e.users2["root"], []string{e.kid}, "fox liquor", 5)
			if err != nil {
				return
			}
			ids := map[string]bool{}
			for _, h := range hits {
				ids[h.FileID] = true
			}
			if len(ids) > 1 {
				fail("a search saw both versions")
				return
			}
		}
	}()
	docs := []string{docJugs, docFox}
	for run := int64(2); run < 14; run++ {
		e.push(run, "a.md", docs[run%2])
	}
	close(stop)
	wg.Wait()
	select {
	case msg := <-bad:
		t.Fatal(msg)
	default:
	}
}

func TestSyncFailedVersionKeepsTheOld(t *testing.T) {
	e := newSync(t)
	e.push(1, "a.md", docFox)
	e.plan(2, "a.md", docJugs)
	staged := e.put(2, "a.md", docJugs)
	e.emb.fail.Store(true)
	e.drain()
	e.emb.fail.Store(false)

	st := e.state(2)
	if st.Failed != 1 || st.Waiting != 0 || st.Indexed != 0 || len(st.Failures) != 1 || !st.Settled() {
		t.Fatalf("state %+v", st)
	}
	f := st.Failures[0]
	if f.Name != "a.md" || !strings.Contains(f.Error, "embedding server down") || !strings.HasSuffix(f.Error, "; the previous version was kept") {
		t.Fatalf("failure %+v", f)
	}
	if got := e.seen("fox liquor"); !strings.Contains(got, "fox") || strings.Contains(got, "liquor") {
		t.Fatalf("the old version stopped serving: %q", got)
	}
	// The next plan lists the name again.
	p := e.plan(3, "a.md", docJugs)
	names(t, "upload", p.Upload, "a.md")
	again := e.put(3, "a.md", docJugs)
	if e.fileExists(staged.FileID) {
		t.Fatal("the failed staged file was not dropped by the new put")
	}
	if e.staged() != 1 {
		t.Fatalf("staged rows %d, want 1", e.staged())
	}
	if e.count(`SELECT count(*) FROM knowledge WHERE id = $1 AND meta->'errors' ? $2`, e.kid, staged.FileID) != 0 {
		t.Fatal("the dropped staged file's error entry survived")
	}
	e.drain()
	if got := e.seen("fox liquor"); !strings.Contains(got, "liquor") {
		t.Fatalf("recovery did not swap: %q", got)
	}
	if st := e.state(3); st.Indexed != 1 || st.Failed != 0 {
		t.Fatalf("state after recovery %+v", st)
	}
	if again.Replaces == "" {
		t.Fatal("the retry lost its base")
	}
}

// A new name that fails has no old version; its error entry goes when a
// retry succeeds.
func TestSyncFailedNewFileThenRetry(t *testing.T) {
	e := newSync(t)
	e.emb.fail.Store(true)
	e.push(1, "a.md", docFox)
	e.emb.fail.Store(false)
	st := e.state(1)
	if st.Failed != 1 || strings.Contains(st.Failures[0].Error, "previous version") {
		t.Fatalf("state %+v", st)
	}
	var failedID string
	if err := e.s.db.QueryRow(context.Background(), `SELECT file_id FROM knowledge_file WHERE knowledge_id = $1`, e.kid).Scan(&failedID); err != nil {
		t.Fatal(err)
	}
	p := e.plan(2, "a.md", docFox)
	names(t, "upload", p.Upload, "a.md") // same sha, but the row failed
	e.push(2, "a.md", docFox)
	if e.fileExists(failedID) || e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, e.kid) != 1 {
		t.Fatal("the failed row was not replaced")
	}
	if e.count(`SELECT count(*) FROM knowledge WHERE id = $1 AND meta->'errors' ? $2`, e.kid, failedID) != 0 {
		t.Fatal("the failed row's error entry survived")
	}
	if st := e.state(2); st.Indexed != 1 || st.Failed != 0 {
		t.Fatalf("state %+v", st)
	}
}

func TestSyncZeroChunksKeepsTheOld(t *testing.T) {
	e := newSync(t)
	e.push(1, "a.md", docFox)
	e.plan(2, "a.md", " \n\n \n")
	e.put(2, "a.md", " \n\n \n")
	e.drain()
	st := e.state(2)
	if st.Failed != 1 || len(st.Failures) != 1 || st.Failures[0].Error != "no text could be extracted; the previous version was kept" {
		t.Fatalf("state %+v", st)
	}
	if got := e.seen("fox"); !strings.Contains(got, "fox") {
		t.Fatalf("the old version stopped serving: %q", got)
	}
	// A new file with no text at all fails too (there is nothing to keep).
	e.push(3, "a.md", docFox, "empty.md", "\n")
	st = e.state(3)
	if st.Failed != 1 || st.Failures[0].Name != "empty.md" || st.Failures[0].Error != "no text could be extracted" || st.Indexed != 1 {
		t.Fatalf("state %+v", st)
	}
}

func TestSyncRePutReplacesTheStagedRow(t *testing.T) {
	e := newSync(t)
	e.push(1, "a.md", docFox)
	e.plan(2, "a.md", docJugs)
	first := e.put(2, "a.md", docJugs)
	second := e.put(2, "a.md", docTea)
	if e.staged() != 1 || e.fileExists(first.FileID) {
		t.Fatalf("staged %d, first file kept %v", e.staged(), e.fileExists(first.FileID))
	}
	if first.Replaces != second.Replaces {
		t.Fatalf("both stage against %q and %q", first.Replaces, second.Replaces)
	}
	e.drain()
	if got := e.seen("fox liquor leaves"); !strings.Contains(got, "leaves") || strings.Contains(got, "liquor") {
		t.Fatalf("search saw %q", got)
	}
}

func TestSyncPutRejectsBadInput(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	files := e.count(`SELECT count(*) FROM file`)
	for name, r := range map[string]SyncPutReq{
		"wrong sha":  {Name: "a.md", SHA256: sum("something else"), Body: strings.NewReader(docFox)},
		"bad name":   {Name: "../a.md", SHA256: sum(docFox), Body: strings.NewReader(docFox)},
		"bad sha":    {Name: "a.md", SHA256: "nope", Body: strings.NewReader(docFox)},
		"no collect": {SyncScope: SyncScope{Pipeline: "docs-sync", Knowledge: "nowhere", Run: 1}, Name: "a.md", SHA256: sum(docFox), Body: strings.NewReader(docFox)},
	} {
		if r.Knowledge == "" {
			r.SyncScope = e.scope(1)
		}
		r.Owner = "root"
		_, err := e.s.SyncPut(ctx, r)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if name == "wrong sha" {
			var ae *api.Error
			if !errors.As(err, &ae) || ae.Code != "checksum_mismatch" {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}
	if n := e.count(`SELECT count(*) FROM file`); n != files {
		t.Fatalf("rejected puts left %d file rows", n-files)
	}
	if n := e.count(`SELECT count(*) FROM knowledge_file`); n != 0 {
		t.Fatalf("rejected puts left %d rows", n)
	}
}

// Prune removes the pipeline's own unlisted rows and nothing else.
func TestSyncPruneSparesOthers(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	e.push(1, "a.md", docFox, "b.md", docTea)
	// a file added by hand, and one owned by another pipeline
	hand := e.upload("root", "hand.md", "# Hand\nAdded in the web app.")
	e.req("root", "POST", "/api/v1/knowledge/"+e.kid+"/file/add", map[string]any{"file_id": hand}, 200, nil)
	other, err := e.s.SyncPut(ctx, SyncPutReq{SyncScope: SyncScope{Pipeline: "other", Knowledge: e.kid, Run: 1}, Owner: "root", Name: "b.md", SHA256: sum(docJugs), Body: strings.NewReader(docJugs)})
	if err != nil {
		t.Fatal(err)
	}
	e.s.files.Wait(ctx, other.FileID, 10*time.Second)
	e.drain()
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, e.kid) != 4 {
		t.Fatal("setup rows")
	}

	// The second run lists only a.md; b.md is stale. A dry run removes nothing.
	p := e.plan(2, "a.md", docFox)
	names(t, "stale", p.Stale, "b.md")
	before := e.snapshot()
	r, err := e.s.SyncPrune(ctx, SyncPruneReq{SyncScope: e.scope(2), Listed: 1, DryRun: true})
	if err != nil || e.snapshot() != before {
		t.Fatalf("dry prune: %+v %v", r, err)
	}
	names(t, "dry stale", r.Stale, "b.md")
	names(t, "dry removed", r.Removed)

	var oldB string
	_ = e.s.db.QueryRow(ctx, `SELECT kf.file_id FROM knowledge_file kf JOIN file f ON f.id = kf.file_id WHERE kf.knowledge_id = $1 AND f.filename = 'b.md' AND kf.origin = 'pipeline:docs-sync'`, e.kid).Scan(&oldB)
	r, err = e.s.SyncPrune(ctx, SyncPruneReq{SyncScope: e.scope(2), Listed: 1})
	if err != nil || r.Refused != "" {
		t.Fatalf("prune: %+v %v", r, err)
	}
	names(t, "removed", r.Removed, "b.md")
	if e.fileExists(oldB) || e.count(`SELECT count(*) FROM chunk WHERE file_id = $1`, oldB) != 0 {
		t.Fatal("the pruned file or its chunks survived")
	}
	// hand.md and the other pipeline's b.md stay, with their chunks
	for _, fid := range []string{hand, other.FileID} {
		if !e.fileExists(fid) || e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND file_id = $2`, e.kid, fid) != 1 ||
			e.count(`SELECT count(*) FROM chunk WHERE file_id = $1`, fid) == 0 {
			t.Fatalf("file %s was pruned", fid)
		}
	}
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, e.kid) != 3 {
		t.Fatal("rows after prune")
	}
}

func TestSyncPruneRefusesAndAllows(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	e.push(1, "a.md", docFox, "b.md", docTea)
	// An empty listing is refused, whatever it removes.
	e.plan(2)
	r, err := e.s.SyncPrune(ctx, SyncPruneReq{SyncScope: e.scope(2), Listed: 0})
	if err != nil || r.Refused == "" || len(r.Removed) != 0 {
		t.Fatalf("empty listing: %+v %v", r, err)
	}
	names(t, "stale", r.Stale, "a.md", "b.md")
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, e.kid) != 2 {
		t.Fatal("a refused prune removed rows")
	}
	// The plan already warns.
	p, err := e.s.SyncPlan(ctx, SyncPlanReq{SyncScope: e.scope(3)})
	if err != nil || p.Refused == "" {
		t.Fatalf("plan of an empty listing: %+v %v", p, err)
	}
	if p, _ = e.s.SyncPlan(ctx, SyncPlanReq{SyncScope: e.scope(3), AllowMassDelete: true}); p.Refused != "" {
		t.Fatalf("allowed plan still refused: %q", p.Refused)
	}
	// Allowed once by the admin: everything goes.
	r, err = e.s.SyncPrune(ctx, SyncPruneReq{SyncScope: e.scope(3), Listed: 0, Allow: true})
	if err != nil || r.Refused != "" {
		t.Fatalf("allowed prune: %+v %v", r, err)
	}
	names(t, "removed", r.Removed, "a.md", "b.md")
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1`, e.kid) != 0 {
		t.Fatal("rows left")
	}
}

// The guard weighs a removal against what the pipeline owned before the run.
// A wrong folder with as many files as the collection holds uploads them first;
// counting those in would make the removal look like half of the files.
func TestSyncPruneWeighsTheFilesOwnedBeforeTheRun(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	var oldKV, newKV []string
	for i := range 10 {
		oldKV = append(oldKV, fmt.Sprintf("old%02d.md", i), fmt.Sprintf("# Old %d\nalpha text %d", i, i))
		newKV = append(newKV, fmt.Sprintf("new%02d.md", i), fmt.Sprintf("# New %d\nbravo text %d", i, i))
	}
	e.push(1, oldKV...)

	first := e.plan(2, newKV...) // run 2 lists ten entirely different names
	if first.Owned != 10 || first.Refused == "" {
		t.Fatalf("first plan: owned %d refused %q", first.Owned, first.Refused)
	}
	for i := 0; i < len(newKV); i += 2 {
		e.put(2, newKV[i], newKV[i+1])
	}
	e.drain()
	if again := e.plan(2, newKV...); again.Owned != 20 {
		t.Fatalf("the second plan counts this run's uploads: owned %d", again.Owned)
	}
	r, err := e.s.SyncPrune(ctx, SyncPruneReq{SyncScope: e.scope(2), Listed: 10, Owned: first.Owned})
	if err != nil || r.Refused == "" || len(r.Removed) != 0 {
		t.Fatalf("prune: %+v %v", r, err)
	}
	if n := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND replaces IS NULL`, e.kid); n != 20 {
		t.Fatalf("%d live files; a refused prune keeps the old ten and the new ten", n)
	}
}

// A swap that is committing locks the staged row and then removes the base row.
// A plan that stamped the base row first and then waited for the staged one
// deadlocked with it.
func TestSyncPlanTakesTheStagedRowBeforeTheBase(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	e.push(1, "a.md", docFox)
	staged := e.put(2, "a.md", docJugs) // staged; the worker is stopped
	tx, err := e.s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM knowledge_file WHERE knowledge_id = $1 AND file_id = $2 FOR UPDATE`, e.kid, staged.FileID); err != nil {
		t.Fatal(err)
	}
	planned := make(chan error, 1)
	go func() {
		_, err := e.s.SyncPlan(ctx, SyncPlanReq{SyncScope: e.scope(3), Files: listing("a.md", docJugs)})
		planned <- err
	}()
	time.Sleep(300 * time.Millisecond) // the plan is now waiting for the staged row
	for _, st := range []struct {
		q   string
		arg string
	}{
		{`DELETE FROM chunk WHERE knowledge_id = $1 AND file_id = $2`, staged.Replaces},
		{`DELETE FROM knowledge_file WHERE knowledge_id = $1 AND file_id = $2`, staged.Replaces}, // the base row: the plan must not hold it
		{`UPDATE knowledge_file SET replaces = NULL, status = 'completed' WHERE knowledge_id = $1 AND file_id = $2`, staged.FileID},
	} {
		if _, err := tx.Exec(ctx, st.q, e.kid, st.arg); err != nil {
			t.Fatalf("the swap's statement %q: %v", st.q, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-planned:
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the plan is stuck")
	}
	if n := e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND file_id = $2 AND replaces IS NULL AND sync_run = 3`, e.kid, staged.FileID); n != 1 {
		t.Fatalf("the swapped-in row was not stamped with the run (%d)", n)
	}
}

// An error for a file that left the collection while it was indexed (a staged
// version dropped by a newer plan) is not worth keeping: nothing would clear it.
func TestSetFileErrorSkipsAFileThatIsGone(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	e.push(1, "a.md", docFox)
	var fid string
	if err := e.s.db.QueryRow(ctx, `SELECT file_id FROM knowledge_file WHERE knowledge_id = $1`, e.kid).Scan(&fid); err != nil {
		t.Fatal(err)
	}
	gone := errors.New("file was removed from the collection while indexing")
	if err := e.s.setFileError(ctx, e.kid, "no-such-file", gone); err != nil {
		t.Fatal(err)
	}
	if err := e.s.setFileError(ctx, e.kid, fid, errors.New("embedding server down")); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM knowledge k, jsonb_object_keys(coalesce(k.meta->'errors', '{}')) key WHERE k.id = $1`, e.kid); n != 1 {
		t.Fatalf("%d error entries; only the file that is still there keeps one", n)
	}
	if n := e.count(`SELECT count(*) FROM knowledge WHERE id = $1 AND meta->'errors' ? $2`, e.kid, fid); n != 1 {
		t.Fatal("the error of a file in the collection is lost")
	}
}

func TestPruneRefused(t *testing.T) {
	for _, c := range []struct {
		name                 string
		owned, stale, listed int
		allow                bool
		refused              bool
	}{
		{"nothing stale", 10, 0, 10, false, false},
		{"nothing at all", 0, 0, 0, false, false},
		{"empty listing", 3, 3, 0, false, true},
		{"empty listing allowed", 3, 3, 0, true, false},
		{"empty listing of one file", 1, 1, 0, false, true},
		{"small collection", 4, 3, 1, false, false},
		{"five of ten", 10, 5, 5, false, false},
		{"six of ten", 10, 6, 4, false, true},
		{"six of ten allowed", 10, 6, 4, true, false},
		{"five of six", 6, 5, 1, false, false},
		{"six of six", 6, 6, 1, false, true},
		{"half of a hundred", 100, 50, 50, false, false},
		{"more than half", 100, 51, 49, false, true},
		{"just over the floor", 8, 6, 2, false, true},
	} {
		got := pruneRefused(c.owned, c.stale, c.listed, c.allow)
		if (got != "") != c.refused {
			t.Errorf("%s: pruneRefused(%d, %d, %d, %v) = %q", c.name, c.owned, c.stale, c.listed, c.allow, got)
		}
	}
	if m := pruneRefused(3, 3, 0, false); !strings.Contains(m, "empty") {
		t.Errorf("message %q does not say why", m)
	}
}

func TestSyncAdopt(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	// a hand-added file with the right bytes, an imported one with no hash,
	// and two hand-added files with one name
	add := func(name, content string) string {
		fid := e.upload("root", name, content)
		e.req("root", "POST", "/api/v1/knowledge/"+e.kid+"/file/add", map[string]any{"file_id": fid}, 200, nil)
		return fid
	}
	guide := add("guide.md", docFox)
	add("twin.md", docTea)
	add("twin.md", docTea)
	e.exec(`INSERT INTO file (id, user_id, filename, path, status, text) VALUES ('imp', 'root', 'policy.md', 'nowhere', 'completed', 'Imported policy text about travel.')`)
	e.exec(`INSERT INTO knowledge_file (knowledge_id, file_id, status, origin) VALUES ($1, 'imp', 'pending', 'openwebui')`, e.kid)
	e.drain()
	origins := func() string {
		var s string
		if err := e.s.db.QueryRow(ctx, `SELECT string_agg(f.filename || '=' || kf.origin, ',' ORDER BY f.filename, kf.file_id) FROM knowledge_file kf JOIN file f ON f.id = kf.file_id WHERE kf.knowledge_id = $1`, e.kid).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	const untouched = "guide.md=kera,policy.md=openwebui,twin.md=kera,twin.md=kera"
	if origins() != untouched {
		t.Fatalf("setup %s", origins())
	}
	req := SyncPlanReq{SyncScope: e.scope(1), Files: listing("guide.md", docFox, "policy.md", "Imported policy text about travel.", "twin.md", docTea)}

	// Without adopt nothing is claimed: every name is new to the pipeline.
	p, err := e.s.SyncPlan(ctx, req)
	if err != nil || len(p.Adopted) != 0 || len(p.Upload) != 3 {
		t.Fatalf("plan without adopt: %+v %v", p, err)
	}
	e.exec(`UPDATE knowledge_file SET sync_run = NULL WHERE knowledge_id = $1`, e.kid)

	// A dry run reports the claims and writes nothing.
	req.Adopt, req.DryRun = true, true
	before := e.snapshot()
	p, err = e.s.SyncPlan(ctx, req)
	if err != nil || e.snapshot() != before {
		t.Fatalf("dry adopt: %+v %v", p, err)
	}
	names(t, "adopted (dry)", p.Adopted, "guide.md", "policy.md")

	req.DryRun = false
	if p, err = e.s.SyncPlan(ctx, req); err != nil {
		t.Fatal(err)
	}
	names(t, "adopted", p.Adopted, "guide.md", "policy.md")
	names(t, "ambiguous", p.Ambiguous, "twin.md")
	names(t, "unchanged", p.Unchanged, "guide.md") // same bytes: nothing to upload
	names(t, "upload", p.Upload, "policy.md", "twin.md")
	if got := origins(); got != "guide.md=pipeline:docs-sync,policy.md=pipeline:docs-sync,twin.md=kera,twin.md=kera" {
		t.Fatalf("origins after adopt: %s", got)
	}
	// The claimed row keeps its file: nothing was copied.
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND file_id = $2 AND origin = 'pipeline:docs-sync'`, e.kid, guide) != 1 {
		t.Fatal("adopted row changed file")
	}
	// A pipeline does not take over rows another pipeline owns.
	e.exec(`UPDATE knowledge_file SET origin = 'pipeline:elsewhere' WHERE knowledge_id = $1 AND file_id = $2`, e.kid, guide)
	req.Adopt = true
	req.Files = listing("guide.md", docFox)
	if p, err = e.s.SyncPlan(ctx, req); err != nil || len(p.Adopted) != 0 {
		t.Fatalf("took another pipeline's row: %+v %v", p, err)
	}
}

func TestSyncStagedRowsAreHidden(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	e.push(1, "a.md", docFox)
	e.plan(2, "a.md", docJugs)
	staged := e.put(2, "a.md", docJugs)
	base := staged.Replaces
	if e.staged() != 1 {
		t.Fatal("not staged")
	}

	st, err := e.s.stats(ctx, []string{e.kid})
	if err != nil || st[e.kid].Files != 1 || st[e.kid].Pending != 0 || st[e.kid].Completed != 1 {
		t.Fatalf("stats %+v %v", st[e.kid], err)
	}
	var list struct {
		Items []fileItem `json:"items"`
		Total int        `json:"total"`
	}
	e.req("root", "GET", "/api/v1/knowledge/"+e.kid+"/files", nil, 200, &list)
	if list.Total != 1 || list.Items[0].ID != base {
		t.Fatalf("file list %+v", list)
	}
	var c coll
	e.req("root", "GET", "/api/v1/knowledge/"+e.kid, nil, 200, &c)
	if len(c.Files) != 1 || c.Status["files"] != 1 || c.Status["pending"] != 0 {
		t.Fatalf("collection %+v", c)
	}
	docs, err := e.s.Documents(ctx, e.users2["root"], e.kid)
	if err != nil || len(docs) != 1 || docs[0].FileID != base {
		t.Fatalf("documents %+v %v", docs, err)
	}

	ts := &toolSet{s: e.s}
	call := func(name, args string) tools.Result {
		r, err := ts.Execute(ctx, tools.Call{Name: name, Args: json.RawMessage(args), User: e.users2["root"], Emit: func(tools.Event) {}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call("list_knowledge", `{}`); !strings.Contains(r.Content, `"files":1`) {
		t.Fatalf("list_knowledge: %s", r.Content)
	}
	if r := call("search_knowledge_files", `{"query":"a.md"}`); !strings.Contains(r.Content, base) || strings.Contains(r.Content, staged.FileID) {
		t.Fatalf("search_knowledge_files: %s", r.Content)
	}
	if r := call("view_knowledge_file", `{"file_id":"`+staged.FileID+`"}`); !r.IsError {
		t.Fatalf("view_knowledge_file shows a staged file: %s", r.Content)
	}
	if r := call("view_knowledge_file", `{"file_id":"`+base+`"}`); r.IsError || !strings.Contains(r.Content, "fox") {
		t.Fatalf("view_knowledge_file of the live file: %s", r.Content)
	}

	// A reader of the collection may open the live file, not the staged one.
	e.req("root", "PUT", "/api/v1/knowledge/"+e.kid+"/grants", []map[string]string{{"principal": "user:bob", "permission": "read"}}, 200, nil)
	bob := e.users2["bob"]
	if ok, err := e.s.canReadFile(ctx, bob, base); err != nil || !ok {
		t.Fatalf("live file: %v %v", ok, err)
	}
	if ok, err := e.s.canReadFile(ctx, bob, staged.FileID); err != nil || ok {
		t.Fatalf("staged file readable: %v %v", ok, err)
	}
	// The indexing queue still sees it: `kera reindex` must wait for it.
	if e.count(`SELECT count(*) FROM knowledge_file WHERE status = 'pending'`) != 1 {
		t.Fatal("the staged row is not queued")
	}
	// After the swap the new file is the visible one.
	e.drain()
	if ok, _ := e.s.canReadFile(ctx, bob, staged.FileID); !ok {
		t.Fatal("the swapped-in file is hidden")
	}
}

func TestSyncDryRunWritesNothing(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	e.push(1, "a.md", docFox, "b.md", docTea)
	e.plan(2, "a.md", docJugs, "b.md", docTea)
	e.put(2, "a.md", docJugs) // a staged leftover for the dry run to want to drop
	hand := e.upload("root", "hand.md", docTea)
	e.req("root", "POST", "/api/v1/knowledge/"+e.kid+"/file/add", map[string]any{"file_id": hand}, 200, nil)
	e.drain()
	e.put(3, "a.md", docFox) // staged again, listing below goes back to it
	before := e.snapshot()
	p, err := e.s.SyncPlan(ctx, SyncPlanReq{SyncScope: e.scope(4), Adopt: true, DryRun: true,
		Files: listing("a.md", docTea, "hand.md", docTea, "new.md", docFox)})
	if err != nil {
		t.Fatal(err)
	}
	names(t, "upload", p.Upload, "a.md", "new.md")
	names(t, "adopted", p.Adopted, "hand.md")
	names(t, "stale", p.Stale, "b.md")
	if _, err := e.s.SyncPrune(ctx, SyncPruneReq{SyncScope: e.scope(4), Listed: 3, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if after := e.snapshot(); after != before {
		t.Fatalf("a dry run wrote:\nbefore %s\nafter  %s", before, after)
	}
}

func TestSyncRefusedWhileMovingModel(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	e.push(1, "a.md", docFox)
	e.exec(`UPDATE knowledge SET meta = meta || jsonb_build_object('reindex_to', 'x') WHERE id = $1`, e.kid)
	_, err := e.s.SyncPlan(ctx, SyncPlanReq{SyncScope: e.scope(2), Files: listing("a.md", docJugs)})
	var ae *api.Error
	if !errors.Is(err, ErrMoving) || !errors.As(err, &ae) || ae.Status != 409 {
		t.Fatalf("plan: %v", err)
	}
	files := e.count(`SELECT count(*) FROM file`)
	if _, err := e.s.SyncPut(ctx, SyncPutReq{SyncScope: e.scope(2), Owner: "root", Name: "a.md", SHA256: sum(docJugs), Body: strings.NewReader(docJugs)}); !errors.Is(err, ErrMoving) {
		t.Fatalf("put: %v", err)
	}
	if e.count(`SELECT count(*) FROM file`) != files || e.staged() != 0 {
		t.Fatal("a refused put left a file behind")
	}
	if st := e.state(1); !st.Moving || st.Settled() {
		t.Fatalf("state %+v", st)
	}
	// Removing files is fine while a move runs.
	if _, err := e.s.SyncPrune(ctx, SyncPruneReq{SyncScope: e.scope(2), Listed: 1}); err != nil {
		t.Fatalf("prune: %v", err)
	}
}

func TestSyncRelease(t *testing.T) {
	e := newSync(t)
	ctx := context.Background()
	e.push(1, "a.md", docFox, "b.md", docTea)
	e.put(2, "a.md", docJugs)
	if e.staged() != 1 {
		t.Fatal("setup")
	}
	var stagedFile string
	_ = e.s.db.QueryRow(ctx, `SELECT file_id FROM knowledge_file WHERE replaces IS NOT NULL`).Scan(&stagedFile)
	if err := e.s.SyncRelease(ctx, "docs-sync"); err != nil {
		t.Fatal(err)
	}
	if e.staged() != 0 || e.fileExists(stagedFile) {
		t.Fatal("the staged version survived")
	}
	if e.count(`SELECT count(*) FROM knowledge_file WHERE knowledge_id = $1 AND origin = 'kera' AND sync_run IS NULL`, e.kid) != 2 {
		t.Fatal("rows were not handed back")
	}
	// Chunks stay: the documents still answer.
	if got := e.seen("fox tea"); !strings.Contains(got, "fox") {
		t.Fatalf("search after release: %q", got)
	}
}
