// Package access answers "may this user read/write this resource?" from the
// access_grant table. Admins may do everything; a grant to principal
// ('user','*') applies to every user; a write grant implies read.
package access

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/ids"
)

// Resource types (match the access_grant CHECK constraint).
const (
	Model      = "model"
	ToolServer = "tool_server"
	Knowledge  = "knowledge"
	Prompt     = "prompt"
	Chat       = "chat"
	Folder     = "folder"
)

// Permissions.
const (
	Read  = "read"
	Write = "write"
)

const principalMatch = `((principal_type = 'user' AND principal_id IN ($1, '*'))
	OR (principal_type = 'group' AND principal_id = ANY($2)))`

func perms(perm string) []string {
	if perm == Read {
		return []string{Read, Write}
	}
	return []string{Write}
}

// Can reports whether u holds perm on (resourceType, id).
func Can(ctx context.Context, db *pgxpool.Pool, u *auth.User, resourceType, id, perm string) (bool, error) {
	if u.IsAdmin() {
		return true, nil
	}
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM access_grant WHERE resource_type = $3 AND resource_id = $4
		AND permission = ANY($5) AND `+principalMatch+`)`,
		u.ID, groups(u), resourceType, id, perms(perm)).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("access check: %w", err)
	}
	return ok, nil
}

// IDs returns every resource id of resourceType on which u holds perm.
// all is true for admins (every id is allowed); ids is then nil.
func IDs(ctx context.Context, db *pgxpool.Pool, u *auth.User, resourceType, perm string) (ids []string, all bool, err error) {
	if u.IsAdmin() {
		return nil, true, nil
	}
	rows, err := db.Query(ctx, `SELECT DISTINCT resource_id FROM access_grant WHERE resource_type = $3
		AND permission = ANY($4) AND `+principalMatch, u.ID, groups(u), resourceType, perms(perm))
	if err != nil {
		return nil, false, fmt.Errorf("access list: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	return ids, false, rows.Err()
}

// Grant is one access_grant row as the API and config files see it.
// Principal is "user:<id>", "user:*" or "group:<id>".
type Grant struct {
	Principal  string `json:"principal" yaml:"principal"`
	Permission string `json:"permission" yaml:"permission"`
}

// Replace sets the grants of one resource. When managedOnly is true only
// rows created by `kera apply` are replaced and hand-made grants survive.
func Replace(ctx context.Context, db *pgxpool.Pool, resourceType, id string, grants []Grant, managed bool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := `DELETE FROM access_grant WHERE resource_type = $1 AND resource_id = $2`
	if managed {
		q += ` AND managed`
	}
	if _, err := tx.Exec(ctx, q, resourceType, id); err != nil {
		return fmt.Errorf("clear grants: %w", err)
	}
	for _, g := range grants {
		ptype, pid, err := ParsePrincipal(g.Principal)
		if err != nil {
			return err
		}
		perm := g.Permission
		if perm == "" {
			perm = Read
		}
		if _, err := tx.Exec(ctx, `INSERT INTO access_grant (id, resource_type, resource_id, principal_type, principal_id, permission, managed)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`, ids.New(), resourceType, id, ptype, pid, perm, managed); err != nil {
			return fmt.Errorf("insert grant: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ParsePrincipal splits "user:<id>" / "group:<id>" / "*" (shorthand for user:*).
func ParsePrincipal(p string) (ptype, pid string, err error) {
	if p == "*" {
		return "user", "*", nil
	}
	for _, t := range []string{"user", "group"} {
		if len(p) > len(t)+1 && p[:len(t)+1] == t+":" {
			return t, p[len(t)+1:], nil
		}
	}
	return "", "", fmt.Errorf("bad principal %q (want user:<id>, user:*, group:<id> or *)", p)
}

func groups(u *auth.User) []string {
	if u.Groups == nil {
		return []string{}
	}
	return u.Groups
}
