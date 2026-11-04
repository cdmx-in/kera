package importowui

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (im *importer) importMemories(ctx context.Context) error {
	st := im.rep.Stat("memories")
	return im.sourceRows(ctx, `SELECT id, user_id, coalesce(content, ''), created_at, updated_at FROM memory ORDER BY id`, nil,
		func(r pgx.Rows) error {
			var id, userID, content string
			var created, updated *int64
			if err := r.Scan(&id, &userID, &content, &created, &updated); err != nil {
				return err
			}
			st.Source++
			uid, ok := im.users[userID]
			if !ok {
				st.skip("owner missing", id)
				return nil
			}
			// Embeddings are not copied; Kera's memory indexer fills them.
			return im.put(ctx, st, id, `INSERT INTO memory (id, user_id, content, origin, created_at, updated_at)
				VALUES ($1, $2, $3, 'openwebui', $4, $5)
				ON CONFLICT (id) DO UPDATE SET user_id = EXCLUDED.user_id, content = EXCLUDED.content,
					embedding = CASE WHEN memory.content = EXCLUDED.content THEN memory.embedding END,
					embedding_model_id = CASE WHEN memory.content = EXCLUDED.content THEN memory.embedding_model_id END,
					updated_at = EXCLUDED.updated_at
				WHERE memory.origin = 'openwebui' AND memory.updated_at <= EXCLUDED.updated_at
				RETURNING (xmax = 0)`, id, uid, content, ts(created), ts(updated))
		})
}

func (im *importer) importPrompts(ctx context.Context) error {
	st := im.rep.Stat("prompts")
	return im.sourceRows(ctx, `SELECT id, user_id, coalesce(command, ''), coalesce(name, ''), coalesce(content, ''), created_at, updated_at
		FROM prompt ORDER BY id`, nil, func(r pgx.Rows) error {
		var id, userID, command, name, content string
		var created, updated *int64
		if err := r.Scan(&id, &userID, &command, &name, &content, &created, &updated); err != nil {
			return err
		}
		st.Source++
		uid, ok := im.users[userID]
		if !ok {
			st.skip("owner missing", id)
			return nil
		}
		var taken bool
		if err := im.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM prompt WHERE user_id = $1 AND command = $2 AND id <> $3)`,
			uid, command, id).Scan(&taken); err != nil {
			return fmt.Errorf("check prompt command: %w", err)
		}
		if taken {
			st.skip("command taken by another prompt", id)
			return nil
		}
		return im.put(ctx, st, id, `INSERT INTO prompt (id, user_id, command, title, content, origin, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'openwebui', $6, $7)
			ON CONFLICT (id) DO UPDATE SET user_id = EXCLUDED.user_id, command = EXCLUDED.command, title = EXCLUDED.title,
				content = EXCLUDED.content, updated_at = EXCLUDED.updated_at
			WHERE prompt.origin = 'openwebui' AND prompt.updated_at <= EXCLUDED.updated_at
			RETURNING (xmax = 0)`, id, uid, command, name, content, ts(created), ts(updated))
	})
}

func (im *importer) importFeedback(ctx context.Context) error {
	st := im.rep.Stat("feedback")
	return im.sourceRows(ctx, `SELECT id, user_id, coalesce(type, 'rating'), data, meta, snapshot, created_at, updated_at
		FROM feedback ORDER BY id`, nil, func(r pgx.Rows) error {
		var id, userID, typ string
		var data, meta, snap []byte
		var created, updated *int64
		if err := r.Scan(&id, &userID, &typ, &data, &meta, &snap, &created, &updated); err != nil {
			return err
		}
		st.Source++
		uid, ok := im.users[userID]
		if !ok {
			st.skip("owner missing", id)
			return nil
		}
		return im.put(ctx, st, id, `INSERT INTO feedback (id, user_id, type, data, meta, snapshot, origin, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'openwebui', $7, $8)
			ON CONFLICT (id) DO UPDATE SET user_id = EXCLUDED.user_id, type = EXCLUDED.type, data = EXCLUDED.data,
				meta = EXCLUDED.meta, snapshot = EXCLUDED.snapshot, updated_at = EXCLUDED.updated_at
			WHERE feedback.origin = 'openwebui' AND feedback.updated_at <= EXCLUDED.updated_at
			RETURNING (xmax = 0)`, id, uid, typ, jsonObject(data), jsonObject(meta), jsonObject(snap), ts(created), ts(updated))
	})
}
