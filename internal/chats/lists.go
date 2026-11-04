package chats

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/ids"
)

// Summary is one row of a chat list.
type Summary struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	UpdatedAt int64   `json:"updated_at"`
	CreatedAt int64   `json:"created_at"`
	Pinned    bool    `json:"pinned"`
	Archived  bool    `json:"archived"`
	FolderID  *string `json:"folder_id"`
}

// ListFilter selects chats of one user.
type ListFilter struct {
	UserID   string
	Archived bool
	Pinned   bool   // only pinned
	FolderID string // only this folder
	Tag      string
	Text     string // full-text over titles and messages
	Page     int    // 1-based; 0 = everything
	PageSize int
}

// List returns chats newest first.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Summary, error) {
	where := []string{"c.user_id = $1", "c.archived = $2"}
	args := []any{f.UserID, f.Archived}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.Pinned {
		where = append(where, "c.pinned")
	}
	if f.FolderID != "" {
		add("c.folder_id = ?", f.FolderID)
	}
	if f.Tag != "" {
		add("EXISTS (SELECT 1 FROM chat_tag t WHERE t.chat_id = c.id AND t.tag = ?)", NormalizeTag(f.Tag))
	}
	if q := PrefixQuery(f.Text); q != "" {
		args = append(args, q, "%"+likeEscape(strings.TrimSpace(f.Text))+"%")
		n := len(args)
		where = append(where, fmt.Sprintf(`(c.title_tsv @@ to_tsquery('simple', $%[1]d) OR c.title ILIKE $%[2]d
			OR EXISTS (SELECT 1 FROM message m WHERE m.chat_id = c.id AND m.tsv @@ to_tsquery('simple', $%[1]d)))`, n-1, n))
	}
	q := `SELECT c.id, c.title, c.updated_at, c.created_at, c.pinned, c.archived, c.folder_id FROM chat c
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY c.updated_at DESC, c.id`
	if f.Page > 0 {
		size := f.PageSize
		if size <= 0 {
			size = 60
		}
		q += fmt.Sprintf(" LIMIT %d OFFSET %d", size, (f.Page-1)*size)
	}
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var c Summary
		var up, cr time.Time
		if err := rows.Scan(&c.ID, &c.Title, &up, &cr, &c.Pinned, &c.Archived, &c.FolderID); err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		c.UpdatedAt, c.CreatedAt = up.Unix(), cr.Unix()
		out = append(out, c)
	}
	return out, rows.Err()
}

// PrefixQuery turns free text into a tsquery where every word matches as a
// prefix ("vect sea" -> "vect:* & sea:*"); "" when there are no words.
func PrefixQuery(text string) string {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		words = append(words, w+":*")
	}
	return strings.Join(words, " & ")
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// UserTags lists every tag the user has used.
func (s *Store) UserTags(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT t.tag FROM chat_tag t JOIN chat c ON c.id = t.chat_id
		WHERE c.user_id = $1 ORDER BY 1`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user tags: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ---- folders ------------------------------------------------------------------------

// Folder is one folder row.
type Folder struct {
	ID         string  `json:"id"`
	UserID     string  `json:"user_id"`
	ParentID   *string `json:"parent_id"`
	Name       string  `json:"name"`
	IsExpanded bool    `json:"is_expanded"`
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
}

const folderCols = `id, user_id, parent_id, name, expanded, created_at, updated_at`

func scanFolder(row pgx.Row) (Folder, error) {
	var f Folder
	var cr, up time.Time
	err := row.Scan(&f.ID, &f.UserID, &f.ParentID, &f.Name, &f.IsExpanded, &cr, &up)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	f.CreatedAt, f.UpdatedAt = cr.Unix(), up.Unix()
	return f, err
}

// Folders lists a user's folders by name.
func (s *Store) Folders(ctx context.Context, userID string) ([]Folder, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+folderCols+` FROM folder WHERE user_id = $1 ORDER BY lower(name), id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer rows.Close()
	out := []Folder{}
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Folder returns one of the user's folders.
func (s *Store) Folder(ctx context.Context, userID, id string) (Folder, error) {
	return scanFolder(s.DB.QueryRow(ctx, `SELECT `+folderCols+` FROM folder WHERE id = $1 AND user_id = $2`, id, userID))
}

// CreateFolder adds a folder; parentID must be the user's folder when set.
func (s *Store) CreateFolder(ctx context.Context, userID, name string, parentID *string) (Folder, error) {
	if parentID != nil && *parentID != "" {
		if _, err := s.Folder(ctx, userID, *parentID); err != nil {
			return Folder{}, err
		}
	} else {
		parentID = nil
	}
	return scanFolder(s.DB.QueryRow(ctx, `INSERT INTO folder (id, user_id, parent_id, name) VALUES ($1, $2, $3, $4)
		RETURNING `+folderCols, ids.New(), userID, parentID, name))
}

// UpdateFolder applies set ($3.. placeholders) to the user's folder.
func (s *Store) updateFolder(ctx context.Context, userID, id, set string, args ...any) (Folder, error) {
	return scanFolder(s.DB.QueryRow(ctx, `UPDATE folder SET `+set+`, updated_at = now() WHERE id = $1 AND user_id = $2
		RETURNING `+folderCols, append([]any{id, userID}, args...)...))
}

// DeleteFolder removes a folder and its sub-folders; their chats move to the top level.
func (s *Store) DeleteFolder(ctx context.Context, userID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM folder WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete folder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
