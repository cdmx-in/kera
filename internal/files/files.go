// Package files stores uploads under KERA_DATA_DIR/uploads/<id>/<name>,
// extracts their text in a small worker pool and serves them back. Other
// features (chat engine, code interpreter, knowledge) use the Service from
// For(a); the HTTP routes live in http.go.
package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/settings"
)

// Statuses of file.status.
const (
	Pending    = "pending"
	Processing = "processing"
	Completed  = "completed"
	Failed     = "failed"
)

var (
	// ErrNotFound means no file row has the id.
	ErrNotFound = errors.New("file not found")
	// ErrTooLarge means the upload exceeds files.max_mb.
	ErrTooLarge = errors.New("file too large")
)

// File is one file row without its extracted text (see Service.Text).
// For paged documents meta.pages is [{n, chars}] and the text is the pages
// joined by "\n\n"; spreadsheets record meta.sheets.
type File struct {
	ID          string
	UserID      string
	Filename    string
	ContentType string
	Size        int64
	SHA256      string
	Path        string // relative to the data dir (absolute for some imported rows)
	Status      string
	Error       string
	Meta        map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Done reports a final status.
func (f File) Done() bool { return f.Status == Completed || f.Status == Failed }

// AccessCheck lets another feature grant read access (e.g. knowledge membership).
type AccessCheck func(ctx context.Context, u *auth.User, fileID string) (bool, error)

// Service owns file storage and processing.
type Service struct {
	db       *pgxpool.Pool
	settings *settings.Store
	dataDir  string
	client   *http.Client

	queue chan string
	stop  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once

	mu     sync.RWMutex
	hooks  []func(context.Context, File)
	checks []AccessCheck
}

// New builds a service; call Start to run the workers.
func New(db *pgxpool.Pool, st *settings.Store, dataDir string) *Service {
	return &Service{db: db, settings: st, dataDir: dataDir,
		client: &http.Client{Timeout: 10 * time.Minute},
		queue:  make(chan string, 1024), stop: make(chan struct{})}
}

// OnProcessed subscribes fn to every file that reaches completed or failed
// through extraction (fn checks File.Status). fn runs on a worker goroutine.
func (s *Service) OnProcessed(fn func(context.Context, File)) {
	s.mu.Lock()
	s.hooks = append(s.hooks, fn)
	s.mu.Unlock()
}

// RegisterAccessCheck adds a check CanRead consults after its own rules.
func (s *Service) RegisterAccessCheck(fn AccessCheck) {
	s.mu.Lock()
	s.checks = append(s.checks, fn)
	s.mu.Unlock()
}

// MaxBytes is the upload limit from files.max_mb (default 100).
func (s *Service) MaxBytes(ctx context.Context) int64 {
	mb := 100
	_ = s.settings.Get(ctx, "files.max_mb", &mb)
	if mb <= 0 {
		mb = 100
	}
	return int64(mb) << 20
}

// Create stores r as a new file owned by userID and queues extraction.
// contentType may be empty; it is then derived from the name and the bytes.
// meta keys are kept; name, content_type and size are always set.
func (s *Service) Create(ctx context.Context, userID, name, contentType string, r io.Reader, meta map[string]any) (File, error) {
	f, err := s.store(ctx, userID, name, contentType, r, meta, true)
	if err != nil {
		return File{}, err
	}
	s.enqueue(f.ID)
	return f, nil
}

// store writes the blob and inserts the row. With process=false the row is
// completed straight away (no extraction); otherwise it stays pending and the
// caller enqueues it.
func (s *Service) store(ctx context.Context, userID, name, contentType string, r io.Reader, meta map[string]any, process bool) (File, error) {
	id := ids.New()
	name = SanitizeName(name)
	rel := filepath.Join("uploads", id, name)
	abs := filepath.Join(s.dataDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return File{}, fmt.Errorf("create upload dir: %w", err)
	}
	size, sum, head, err := writeBlob(abs, r, s.MaxBytes(ctx))
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(abs))
		return File{}, err
	}
	if contentType == "" {
		contentType = DetectType(name, head)
	}
	m := map[string]any{}
	for k, v := range meta {
		m[k] = v
	}
	m["name"], m["content_type"], m["size"] = name, contentType, size
	status := Pending
	if !process {
		status = Completed
	}
	mj, err := json.Marshal(m)
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(abs))
		return File{}, fmt.Errorf("encode meta: %w", err)
	}
	f, err := scanFile(s.db.QueryRow(ctx, `INSERT INTO file (id, user_id, filename, content_type, size, sha256, path, status, meta)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+fileCols,
		id, userID, name, contentType, size, sum, filepath.ToSlash(rel), status, mj))
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(abs))
		return File{}, fmt.Errorf("insert file: %w", err)
	}
	return f, nil
}

// writeBlob copies r to path (at most max bytes) and returns size, sha256 and the first 512 bytes.
func writeBlob(path string, r io.Reader, max int64) (int64, string, []byte, error) {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if err != nil {
		return 0, "", nil, fmt.Errorf("create blob: %w", err)
	}
	defer out.Close()
	h := sha256.New()
	head := &headBuf{}
	n, err := io.Copy(io.MultiWriter(out, h, head), io.LimitReader(r, max+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return 0, "", nil, ErrTooLarge
		}
		return 0, "", nil, fmt.Errorf("write blob: %w", err)
	}
	if n > max {
		return 0, "", nil, ErrTooLarge
	}
	if err := out.Close(); err != nil {
		return 0, "", nil, fmt.Errorf("close blob: %w", err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), head.b, nil
}

type headBuf struct{ b []byte }

func (h *headBuf) Write(p []byte) (int, error) {
	if room := 512 - len(h.b); room > 0 {
		h.b = append(h.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// SanitizeName turns a client-supplied name into a safe single path element:
// no directories, no control characters, at most 200 bytes (extension kept).
func SanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = name[strings.LastIndex(name, "/")+1:]
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, strings.ToValidUTF8(name, ""))
	name = strings.TrimSpace(name)
	name = strings.TrimLeft(name, ".")
	if name == "" {
		return "file"
	}
	const maxLen = 200
	if len(name) <= maxLen {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 20 {
		ext = ""
	}
	base := name[:maxLen-len(ext)]
	for !utf8.ValidString(base) {
		base = base[:len(base)-1]
	}
	return base + ext
}

const fileCols = `id, user_id, filename, content_type, size, sha256, path, status, error, meta, created_at, updated_at`

func scanFile(row pgx.Row) (File, error) {
	var f File
	var meta []byte
	err := row.Scan(&f.ID, &f.UserID, &f.Filename, &f.ContentType, &f.Size, &f.SHA256, &f.Path,
		&f.Status, &f.Error, &meta, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	f.Meta = map[string]any{}
	_ = json.Unmarshal(meta, &f.Meta)
	return f, nil
}

// Get loads a file row.
func (s *Service) Get(ctx context.Context, id string) (File, error) {
	f, err := scanFile(s.db.QueryRow(ctx, `SELECT `+fileCols+` FROM file WHERE id = $1`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return File{}, fmt.Errorf("get file %s: %w", id, err)
	}
	return f, err
}

// List returns files newest first: userID's, or everyone's when userID is "".
func (s *Service) List(ctx context.Context, userID string) ([]File, error) {
	rows, err := s.db.Query(ctx, `SELECT `+fileCols+` FROM file WHERE $1 = '' OR user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// abs resolves a stored path.
func (s *Service) abs(f File) string {
	if filepath.IsAbs(f.Path) {
		return f.Path
	}
	return filepath.Join(s.dataDir, filepath.FromSlash(f.Path))
}

// Open returns the blob for reading (the caller closes it).
func (s *Service) Open(ctx context.Context, id string) (io.ReadSeekCloser, File, error) {
	f, err := s.Get(ctx, id)
	if err != nil {
		return nil, File{}, err
	}
	fh, err := os.Open(s.abs(f))
	if err != nil {
		return nil, File{}, fmt.Errorf("open blob %s: %w", id, err)
	}
	return fh, f, nil
}

// Text returns the extracted text ("" until processing completes).
func (s *Service) Text(ctx context.Context, id string) (string, error) {
	var t *string
	err := s.db.QueryRow(ctx, `SELECT text FROM file WHERE id = $1`, id).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("file text %s: %w", id, err)
	}
	if t == nil {
		return "", nil
	}
	return *t, nil
}

// Wait blocks until the file is completed or failed, ctx ends or timeout passes.
// ponytail: polls the row every 200 ms; an in-process notify list if this ever shows up in profiles.
func (s *Service) Wait(ctx context.Context, id string, timeout time.Duration) (File, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		f, err := s.Get(ctx, id)
		if err != nil {
			return f, err
		}
		if f.Done() {
			return f, nil
		}
		select {
		case <-ctx.Done():
			return f, fmt.Errorf("wait for file %s: %w", id, ctx.Err())
		case <-tick.C:
		}
	}
}

// Delete removes the row and its blob directory.
func (s *Service) Delete(ctx context.Context, id string) error {
	f, err := scanFile(s.db.QueryRow(ctx, `DELETE FROM file WHERE id = $1 RETURNING `+fileCols, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("delete file %s: %w", id, err)
	}
	if err := os.Remove(s.abs(f)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("remove blob", "file", id, "err", err)
	}
	if err := os.RemoveAll(filepath.Join(s.dataDir, "uploads", id)); err != nil {
		slog.Warn("remove blob dir", "file", id, "err", err)
	}
	return nil
}

// CanRead: owner, admin, a chat of u links the file, or a registered check says yes.
func (s *Service) CanRead(ctx context.Context, u *auth.User, fileID string) (bool, error) {
	if u == nil {
		return false, nil
	}
	if u.IsAdmin() {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM file WHERE id = $1 AND user_id = $2)
		OR EXISTS (SELECT 1 FROM chat_file cf JOIN chat c ON c.id = cf.chat_id WHERE cf.file_id = $1 AND c.user_id = $2)`,
		fileID, u.ID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("file access %s: %w", fileID, err)
	}
	if ok {
		return true, nil
	}
	s.mu.RLock()
	checks := append([]AccessCheck(nil), s.checks...)
	s.mu.RUnlock()
	for _, c := range checks {
		if ok, err := c(ctx, u, fileID); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}
