// Package skills keeps skills: a SKILL.md (frontmatter name and description,
// then instructions) plus the text files beside it. Admins import them from
// a zip or a directory (CLI, admin console or `kera apply`), grant them like
// any resource, and chats reach them through the "builtin:skills" tool set:
// the model sees the list and loads a skill's text only when it needs it.
package skills

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

// DB is what the store needs: a pool or a transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrNotFound means no skill has that id or name (or the user cannot read it).
var ErrNotFound = errors.New("skill not found")

// Import outcomes.
const (
	Created   = "created"
	Updated   = "updated"
	Unchanged = "unchanged"
)

// Row is a stored skill without its text.
type Row struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	Lazy        bool      `json:"lazy"`
	Managed     bool      `json:"managed"`
	Files       int       `json:"files"`
	CreatedAt   time.Time `json:"-"`
	UpdatedAt   time.Time `json:"-"`
}

const rowCols = `id, name, description, enabled, lazy, managed,
	(SELECT count(*) FROM jsonb_object_keys(resources))::int, created_at, updated_at`

func scanRows(rows pgx.Rows, err error) ([]Row, error) {
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Row, error) {
		var s Row
		return s, r.Scan(&s.ID, &s.Name, &s.Description, &s.Enabled, &s.Lazy, &s.Managed, &s.Files, &s.CreatedAt, &s.UpdatedAt)
	})
}

// Upsert stores s by name: new names are created, changed text is updated,
// enabled/lazy/grants are left as they are. managed marks rows from `kera
// apply` (a later manual import never clears it).
func Upsert(ctx context.Context, db DB, s Skill, managed bool) (id, outcome string, err error) {
	if s.Resources == nil {
		s.Resources = map[string]string{}
	}
	cur, err := Get(ctx, db, s.Name)
	switch {
	case errors.Is(err, ErrNotFound):
		id = ids.New()
		_, err = db.Exec(ctx, `INSERT INTO skill (id, name, description, content, resources, managed)
			VALUES ($1, $2, $3, $4, $5, $6)`, id, s.Name, s.Description, s.Content, s.Resources, managed)
		return id, Created, wrap(err, "insert skill %s", s.Name)
	case err != nil:
		return "", "", err
	}
	if cur.Digest() == s.Digest() && (cur.Managed || !managed) {
		return cur.ID, Unchanged, nil
	}
	_, err = db.Exec(ctx, `UPDATE skill SET description = $2, content = $3, resources = $4, managed = managed OR $5,
		updated_at = now() WHERE id = $1`, cur.ID, s.Description, s.Content, s.Resources, managed)
	return cur.ID, Updated, wrap(err, "update skill %s", s.Name)
}

// Stored is a skill with its text as the database holds it.
type Stored struct {
	Skill
	ID      string
	Enabled bool
	Lazy    bool
	Managed bool
}

// Get loads one skill by name or id.
func Get(ctx context.Context, db DB, nameOrID string) (*Stored, error) {
	s := &Stored{}
	err := db.QueryRow(ctx, `SELECT id, name, description, content, resources, enabled, lazy, managed FROM skill
		WHERE name = $1 OR id = $1 ORDER BY name = $1 DESC LIMIT 1`, nameOrID).
		Scan(&s.ID, &s.Name, &s.Description, &s.Content, &s.Resources, &s.Enabled, &s.Lazy, &s.Managed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load skill: %w", err)
	}
	return s, nil
}

// All lists every skill (admin view).
func All(ctx context.Context, db DB) ([]Row, error) {
	return scanRows(db.Query(ctx, `SELECT `+rowCols+` FROM skill ORDER BY name`))
}

// Visible lists the enabled skills u may read.
func Visible(ctx context.Context, db *pgxpool.Pool, u *auth.User) ([]Row, error) {
	ok, all, err := access.IDs(ctx, db, u, access.Skill, access.Read)
	if err != nil {
		return nil, err
	}
	if ok == nil {
		ok = []string{}
	}
	return scanRows(db.Query(ctx, `SELECT `+rowCols+` FROM skill WHERE enabled AND ($1 OR id = ANY($2)) ORDER BY name`, all, ok))
}

// Open loads an enabled skill by exact name when u may read it.
func Open(ctx context.Context, db *pgxpool.Pool, u *auth.User, name string) (*Stored, error) {
	s, err := Get(ctx, db, name)
	if err != nil {
		return nil, err
	}
	if !s.Enabled || s.Name != name {
		return nil, ErrNotFound
	}
	if ok, err := access.Can(ctx, db, u, access.Skill, s.ID, access.Read); err != nil || !ok {
		return nil, cmp.Or(err, ErrNotFound)
	}
	return s, nil
}

// Delete removes a skill and its grants.
func Delete(ctx context.Context, db DB, id string) error {
	tag, err := db.Exec(ctx, `WITH g AS (DELETE FROM access_grant WHERE resource_type = 'skill' AND resource_id = $1)
		DELETE FROM skill WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}
