package importowui

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/jackc/pgx/v5"
)

// nativeData is true when a user owns rows created in Kera; such users are
// never pruned (their rows would cascade away).
const nativeData = `EXISTS (SELECT 1 FROM chat x WHERE x.user_id = t.id AND x.origin <> 'openwebui')
	OR EXISTS (SELECT 1 FROM file x WHERE x.user_id = t.id AND x.origin <> 'openwebui')
	OR EXISTS (SELECT 1 FROM folder x WHERE x.user_id = t.id AND x.origin <> 'openwebui')
	OR EXISTS (SELECT 1 FROM knowledge x WHERE x.user_id = t.id AND x.origin <> 'openwebui')
	OR EXISTS (SELECT 1 FROM prompt x WHERE x.user_id = t.id AND x.origin <> 'openwebui')
	OR EXISTS (SELECT 1 FROM api_key x WHERE x.user_id = t.id AND x.origin <> 'openwebui')`

// noNative keeps a row whose deletion would cascade to rows created in Kera
// (a chat continued in Kera, a file attached there, ...).
func noNative(table, col string) string {
	return " AND NOT EXISTS (SELECT 1 FROM " + table + " x WHERE x." + col + " = t.id AND x.origin <> 'openwebui')"
}

// prune deletes origin='openwebui' rows whose source row is gone. Rows are
// matched against every source id (not just those changed --since).
// Messages and tags are pruned per chat while chats are written.
func (im *importer) prune(ctx context.Context) error {
	type job struct {
		entity, stat, table, col, src, extra string
	}
	jobs := []job{
		{"users", "users", `"user"`, "id", `SELECT id FROM "user"`, " AND NOT (" + nativeData + ")"},
		{"users", "api_keys", "api_key", "id", `SELECT id FROM api_key WHERE coalesce(expires_at, 0) = 0
			OR expires_at > extract(epoch FROM now())`, ""},
		{"groups", "groups", `"group"`, "id", `SELECT id FROM "group"`, " AND NOT t.managed" + noNative("group_member", "group_id")},
		{"folders", "folders", "folder", "id", `SELECT id FROM folder`, noNative("folder", "parent_id")},
		{"files", "files", "file", "id", `SELECT id FROM file`, noNative("chat_file", "file_id") + noNative("knowledge_file", "file_id")},
		{"chats", "chats", "chat", "id", `SELECT id FROM chat`, noNative("message", "chat_id") + noNative("shared_chat", "chat_id")},
		{"chats", "shared_chats", "shared_chat", "share_id", `SELECT s.id FROM shared_chat s JOIN chat c ON c.share_id = s.id`, ""},
		{"knowledge", "knowledge", "knowledge", "id", `SELECT id FROM knowledge`, noNative("knowledge_file", "knowledge_id")},
		{"grants", "grants", "access_grant", "id", `SELECT id FROM access_grant`, ""},
		{"memories", "memories", "memory", "id", `SELECT id FROM memory`, ""},
		{"prompts", "prompts", "prompt", "id", `SELECT id FROM prompt`, ""},
		{"feedback", "feedback", "feedback", "id", `SELECT id FROM feedback`, ""},
	}
	for _, j := range jobs {
		if !im.opt.wants(j.entity) {
			continue
		}
		keep, err := im.sourceIDs(ctx, j.src)
		if err != nil {
			return err
		}
		rows, err := im.tx.Query(ctx, `DELETE FROM `+j.table+` t WHERE t.origin = 'openwebui' AND NOT (t.`+j.col+` = ANY($1))`+j.extra+
			` RETURNING t.`+j.col, keep)
		if err != nil {
			return fmt.Errorf("prune %s: %w", j.stat, err)
		}
		gone, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("prune %s: %w", j.stat, err)
		}
		im.rep.Stat(j.stat).Pruned += len(gone)
		if j.table == "file" {
			for _, id := range gone {
				if safeName(id) != id {
					continue // never a path outside uploads/
				}
				im.pruneDirs = append(im.pruneDirs, filepath.Join(im.opt.DataDir, "uploads", id))
			}
		}
	}
	pairs := []struct {
		entity, stat, table, a, b, src string
		mapUser                        bool
	}{
		{"groups", "group_members", "group_member", "group_id", "user_id", `SELECT group_id, user_id FROM group_member`, true},
		{"chats", "chat_files", "chat_file", "chat_id", "file_id", `SELECT chat_id, file_id FROM chat_file`, false},
		{"knowledge", "knowledge_files", "knowledge_file", "knowledge_id", "file_id", `SELECT knowledge_id, file_id FROM knowledge_file`, false},
	}
	for _, p := range pairs {
		if !im.opt.wants(p.entity) {
			continue
		}
		var as, bs []string
		err := im.sourceRows(ctx, p.src, nil, func(r pgx.Rows) error {
			var a, b string
			if err := r.Scan(&a, &b); err != nil {
				return err
			}
			if p.mapUser {
				b = im.users[b]
			}
			as, bs = append(as, a), append(bs, b)
			return nil
		})
		if err != nil {
			return err
		}
		tag, err := im.tx.Exec(ctx, `DELETE FROM `+p.table+` t WHERE t.origin = 'openwebui' AND NOT EXISTS (
			SELECT 1 FROM unnest($1::text[], $2::text[]) s(a, b) WHERE s.a = t.`+p.a+` AND s.b = t.`+p.b+`)`, as, bs)
		if err != nil {
			return fmt.Errorf("prune %s: %w", p.stat, err)
		}
		im.rep.Stat(p.stat).Pruned += int(tag.RowsAffected())
	}
	return nil
}
