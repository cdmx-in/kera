package importowui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const chatBatch = 50

func (im *importer) importFolders(ctx context.Context) error {
	st := im.rep.Stat("folders")
	type link struct{ id, parent string }
	var links []link
	err := im.sourceRows(ctx, `SELECT id, coalesce(parent_id, ''), user_id, coalesce(name, ''), coalesce(is_expanded, false),
		created_at, updated_at FROM folder ORDER BY id`, nil, func(r pgx.Rows) error {
		var id, parent, userID, name string
		var expanded bool
		var created, updated *int64
		if err := r.Scan(&id, &parent, &userID, &name, &expanded, &created, &updated); err != nil {
			return err
		}
		st.Source++
		uid, ok := im.users[userID]
		if !ok {
			st.skip("owner missing", id)
			return nil
		}
		if parent != "" {
			links = append(links, link{id, parent})
		}
		return im.put(ctx, st, id, `INSERT INTO folder (id, user_id, name, expanded, origin, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'openwebui', $5, $6)
			ON CONFLICT (id) DO UPDATE SET user_id = EXCLUDED.user_id, name = EXCLUDED.name, expanded = EXCLUDED.expanded,
				updated_at = EXCLUDED.updated_at
			WHERE folder.origin = 'openwebui' AND folder.updated_at <= EXCLUDED.updated_at
			RETURNING (xmax = 0)`, id, uid, name, expanded, ts(created), ts(updated))
	})
	if err != nil {
		return err
	}
	// Parents are linked in a second pass so insert order does not matter.
	for _, l := range links {
		if _, err := im.tx.Exec(ctx, `UPDATE folder SET parent_id = $2 WHERE id = $1 AND origin = 'openwebui'
			AND EXISTS (SELECT 1 FROM folder p WHERE p.id = $2)`, l.id, l.parent); err != nil {
			return fmt.Errorf("link folder %s: %w", l.id, err)
		}
	}
	return nil
}

type srcChat struct {
	id, userID, title, summary string
	shareID, folderID          *string
	archived, pinned           bool
	blob, meta                 []byte
	created, updated           *int64
}

func (im *importer) importChats(ctx context.Context) error {
	folders, err := im.targetIDs(ctx, "folder")
	if err != nil {
		return err
	}
	if im.tagNames, err = im.loadTagNames(ctx); err != nil {
		return err
	}
	var total int
	if err := im.src.QueryRow(ctx, `SELECT count(*) FROM chat WHERE updated_at >= $1`, im.since()).Scan(&total); err != nil {
		return fmt.Errorf("count source chats: %w", err)
	}
	done, last := 0, ""
	for {
		batch, err := im.chatBatch(ctx, last)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		ids := make([]string, len(batch))
		for i, c := range batch {
			ids[i] = c.id
		}
		outputs, err := im.loadOutputs(ctx, ids)
		if err != nil {
			return err
		}
		for _, c := range batch {
			if err := im.importChat(ctx, c, outputs[c.id], folders); err != nil {
				return fmt.Errorf("chat %s: %w", c.id, err)
			}
			done++
			im.progress("chats", done, total)
		}
		last = batch[len(batch)-1].id
	}
	if err := im.importShares(ctx); err != nil {
		return err
	}
	return im.importChatFiles(ctx)
}

// chatBatch reads the next page of source chats by id (keyset pagination).
func (im *importer) chatBatch(ctx context.Context, after string) ([]srcChat, error) {
	var out []srcChat
	err := im.sourceRows(ctx, `SELECT id, user_id, coalesce(title, ''), coalesce(summary, ''), share_id, folder_id,
		coalesce(archived, false), coalesce(pinned, false), chat, meta, created_at, updated_at
		FROM chat WHERE updated_at >= $1 AND id > $2 ORDER BY id LIMIT $3`, []any{im.since(), after, chatBatch},
		func(r pgx.Rows) error {
			var c srcChat
			if err := r.Scan(&c.id, &c.userID, &c.title, &c.summary, &c.shareID, &c.folderID, &c.archived, &c.pinned,
				&c.blob, &c.meta, &c.created, &c.updated); err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	return out, err
}

// loadOutputs returns chat id -> message id -> output items for messages
// stored with a structured output array.
func (im *importer) loadOutputs(ctx context.Context, chatIDs []string) (map[string]map[string][]map[string]any, error) {
	out := map[string]map[string][]map[string]any{}
	err := im.sourceRows(ctx, `SELECT chat_id, id, output FROM chat_message
		WHERE chat_id = ANY($1) AND json_typeof(output) = 'array'`, []any{chatIDs}, func(r pgx.Rows) error {
		var chatID, id string
		var raw []byte
		if err := r.Scan(&chatID, &id, &raw); err != nil {
			return err
		}
		var items []map[string]any
		if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
			return nil
		}
		if out[chatID] == nil {
			out[chatID] = map[string][]map[string]any{}
		}
		out[chatID][strings.TrimPrefix(id, chatID+"-")] = items
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load message outputs: %w", err)
	}
	return out, nil
}

func (im *importer) importChat(ctx context.Context, c srcChat, outputs map[string][]map[string]any, folders map[string]bool) error {
	st, ms := im.rep.Stat("chats"), im.rep.Stat("messages")
	st.Source++
	uid, ok := im.users[c.userID]
	if !ok {
		st.skip("owner missing", c.id)
		return nil
	}
	tree, err := ConvertHistory(c.blob, outputs, ts(c.created))
	if err != nil {
		st.skip("history unreadable", c.id)
		return nil
	}
	ms.Source += len(tree.Messages) + tree.NoRole
	if tree.NoRole > 0 {
		if ms.Skipped == nil {
			ms.Skipped = map[string]int{}
		}
		ms.Skipped["no role"] += tree.NoRole
	}
	for range tree.Dangling {
		ms.note("parent missing, made a root")
	}
	if err := im.rekeyShared(ctx, c.id, &tree, ms); err != nil {
		return err
	}
	var folderID *string
	if c.folderID != nil && *c.folderID != "" {
		if folders[*c.folderID] {
			folderID = c.folderID
		} else {
			st.note("folder missing, chat kept at top level")
		}
	}
	if c.shareID != nil && *c.shareID == "" {
		c.shareID = nil
	}
	var current *string
	if tree.CurrentID != "" {
		current = &tree.CurrentID
	}
	meta := jsonObject(c.meta)
	tags := tagList(meta["tags"], im.tagNames, c.userID)
	delete(meta, "tags")
	outcome, err := im.upsert(ctx, st, c.id, `INSERT INTO chat (id, user_id, title, folder_id, pinned, archived, share_id,
			current_message_id, model_ids, summary, meta, origin, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'openwebui', $12, $13)
		ON CONFLICT (id) DO UPDATE SET user_id = EXCLUDED.user_id, title = EXCLUDED.title, folder_id = EXCLUDED.folder_id,
			pinned = EXCLUDED.pinned, archived = EXCLUDED.archived, share_id = EXCLUDED.share_id,
			current_message_id = EXCLUDED.current_message_id, model_ids = EXCLUDED.model_ids, summary = EXCLUDED.summary,
			meta = EXCLUDED.meta, updated_at = EXCLUDED.updated_at
		WHERE chat.origin = 'openwebui' AND chat.updated_at <= EXCLUDED.updated_at
		RETURNING (xmax = 0)`,
		c.id, uid, c.title, folderID, c.pinned, c.archived, c.shareID, current, chatModels(c.blob), c.summary, meta,
		ts(c.created), ts(c.updated))
	if err != nil {
		return err
	}
	if err := im.writeMessages(ctx, c.id, tree.Messages, ms); err != nil {
		return err
	}
	if outcome == outKept {
		return nil // the Kera copy is newer: leave its tags alone
	}
	return im.writeTags(ctx, c.id, tags)
}

// rekeyShared gives a message a chat-scoped id when its source id already
// belongs to another chat's message (the source allows that when a chat is
// cloned; Kera message ids are global). The choice is stable across re-runs.
func (im *importer) rekeyShared(ctx context.Context, chatID string, tree *Tree, ms *Stat) error {
	ids := make([]string, len(tree.Messages))
	for i, m := range tree.Messages {
		ids[i] = m.ID
	}
	rows, err := im.tx.Query(ctx, `SELECT id FROM message WHERE id = ANY($1) AND chat_id <> $2`, ids, chatID)
	if err != nil {
		return fmt.Errorf("check message ids: %w", err)
	}
	taken, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("check message ids: %w", err)
	}
	if len(taken) == 0 {
		return nil
	}
	re := map[string]string{}
	for _, id := range taken {
		re[id] = chatID + "-" + id
		ms.note("message id used by another chat, re-keyed")
	}
	for i := range tree.Messages {
		m := &tree.Messages[i]
		if n, ok := re[m.ID]; ok {
			m.ID = n
		}
		if n, ok := re[m.ParentID]; ok {
			m.ParentID = n
		}
	}
	if n, ok := re[tree.CurrentID]; ok {
		tree.CurrentID = n
	}
	return nil
}

func (im *importer) writeMessages(ctx context.Context, chatID string, msgs []Message, ms *Stat) error {
	b := &pgx.Batch{}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
		var parent *string
		if m.ParentID != "" {
			parent = &msgs[i].ParentID
		}
		b.Queue(`INSERT INTO message (id, chat_id, parent_id, role, model_id, content, parts, files, sources, follow_ups,
				usage, status, error, context_summary, origin, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'openwebui', $15, $15)
			ON CONFLICT (id) DO UPDATE SET parent_id = EXCLUDED.parent_id, role = EXCLUDED.role, model_id = EXCLUDED.model_id,
				content = EXCLUDED.content, parts = EXCLUDED.parts, files = EXCLUDED.files, sources = EXCLUDED.sources,
				follow_ups = EXCLUDED.follow_ups, usage = EXCLUDED.usage, status = EXCLUDED.status, error = EXCLUDED.error,
				context_summary = EXCLUDED.context_summary
			WHERE message.origin = 'openwebui' AND message.chat_id = EXCLUDED.chat_id AND message.updated_at <= EXCLUDED.updated_at
			RETURNING (xmax = 0)`,
			m.ID, chatID, parent, m.Role, m.Model, m.Content, m.Parts, m.Files, m.Sources, m.FollowUps, m.Usage,
			m.Status, m.Error, m.ContextSummary, m.CreatedAt)
	}
	res := im.tx.SendBatch(ctx, b)
	for _, id := range ids {
		var inserted bool
		if _, err := ms.record(id, inserted, res.QueryRow().Scan(&inserted)); err != nil {
			res.Close()
			return err
		}
	}
	if err := res.Close(); err != nil {
		return fmt.Errorf("write messages: %w", err)
	}
	if !im.opt.PruneMissing {
		return nil
	}
	tag, err := im.tx.Exec(ctx, `DELETE FROM message WHERE chat_id = $1 AND origin = 'openwebui' AND NOT (id = ANY($2))`, chatID, ids)
	if err != nil {
		return fmt.Errorf("prune messages: %w", err)
	}
	ms.Pruned += int(tag.RowsAffected())
	return nil
}

func (im *importer) writeTags(ctx context.Context, chatID string, tags []string) error {
	st := im.rep.Stat("chat_tags")
	for _, t := range tags {
		st.Source++
		if err := im.put(ctx, st, chatID+"/"+t, `INSERT INTO chat_tag (chat_id, tag) VALUES ($1, $2)
			ON CONFLICT DO NOTHING RETURNING true`, chatID, t); err != nil {
			return err
		}
	}
	if !im.opt.PruneMissing {
		return nil
	}
	// ponytail: chat_tag has no origin, so a tag added in Kera to an imported chat is
	// dropped when the source chat changes; add an origin column if that matters.
	tag, err := im.tx.Exec(ctx, `DELETE FROM chat_tag WHERE chat_id = $1 AND NOT (tag = ANY($2))`, chatID, tags)
	if err != nil {
		return fmt.Errorf("prune tags: %w", err)
	}
	st.Pruned += int(tag.RowsAffected())
	return nil
}

// loadTagNames maps "<user id>\x00<tag id>" to the tag's display name: chat
// meta holds normalized tag ids ("data_analysis"), Kera stores the name.
func (im *importer) loadTagNames(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := im.sourceRows(ctx, `SELECT user_id, id, coalesce(name, '') FROM tag`, nil, func(r pgx.Rows) error {
		var user, id, name string
		if err := r.Scan(&user, &id, &name); err != nil {
			return err
		}
		if name != "" {
			out[user+"\x00"+id] = name
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load tag names: %w", err)
	}
	return out, nil
}

func tagList(v any, names map[string]string, user string) []string {
	arr, _ := v.([]any)
	seen := map[string]bool{}
	out := []string{}
	for _, t := range arr {
		s, _ := t.(string)
		if n, ok := names[user+"\x00"+s]; ok {
			s = n
		}
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// chatModels reads the chat-level model list from the blob.
func chatModels(blob []byte) []string {
	var doc struct {
		Models []any `json:"models"`
	}
	_ = json.Unmarshal(blob, &doc)
	out := []string{}
	for _, m := range doc.Models {
		if s, ok := m.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (im *importer) importShares(ctx context.Context) error {
	st := im.rep.Stat("shared_chats")
	chats, err := im.targetIDs(ctx, "chat")
	if err != nil {
		return err
	}
	return im.sourceRows(ctx, `SELECT s.id, coalesce(s.title, ''), s.chat, c.id, c.user_id, s.created_at, s.updated_at
		FROM shared_chat s JOIN chat c ON c.share_id = s.id WHERE c.updated_at >= $1 ORDER BY s.id`, []any{im.since()},
		func(r pgx.Rows) error {
			var id, title, chatID, userID string
			var blob []byte
			var created, updated *int64
			if err := r.Scan(&id, &title, &blob, &chatID, &userID, &created, &updated); err != nil {
				return err
			}
			st.Source++
			uid, ok := im.users[userID]
			if !chats[chatID] || !ok {
				st.skip("chat missing", id)
				return nil
			}
			tree, err := ConvertHistory(blob, nil, ts(created))
			if err != nil {
				st.skip("snapshot unreadable", id)
				return nil
			}
			snap := map[string]any{"title": title, "currentId": tree.CurrentID, "messages": tree.Messages}
			return im.put(ctx, st, id, `INSERT INTO shared_chat (share_id, chat_id, user_id, title, snapshot, origin, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, 'openwebui', $6, $7)
				ON CONFLICT (share_id) DO UPDATE SET chat_id = EXCLUDED.chat_id, user_id = EXCLUDED.user_id, title = EXCLUDED.title,
					snapshot = EXCLUDED.snapshot, updated_at = EXCLUDED.updated_at
				WHERE shared_chat.origin = 'openwebui'
				RETURNING (xmax = 0)`, id, chatID, uid, title, snap, ts(created), ts(updated))
		})
}

func (im *importer) importChatFiles(ctx context.Context) error {
	st := im.rep.Stat("chat_files")
	chats, err := im.targetIDs(ctx, "chat")
	if err != nil {
		return err
	}
	files, err := im.targetIDs(ctx, "file")
	if err != nil {
		return err
	}
	return im.sourceRows(ctx, `SELECT cf.chat_id, cf.file_id, cf.message_id FROM chat_file cf JOIN chat c ON c.id = cf.chat_id
		WHERE c.updated_at >= $1 ORDER BY 1, 2`, []any{im.since()}, func(r pgx.Rows) error {
		var chatID, fileID string
		var msgID *string
		if err := r.Scan(&chatID, &fileID, &msgID); err != nil {
			return err
		}
		st.Source++
		id := chatID + "/" + fileID
		switch {
		case !chats[chatID]:
			st.skip("chat missing", id)
			return nil
		case !files[fileID]:
			st.skip("file missing", id)
			return nil
		}
		return im.put(ctx, st, id, `INSERT INTO chat_file (chat_id, file_id, message_id, origin) VALUES ($1, $2, $3, 'openwebui')
			ON CONFLICT DO NOTHING RETURNING true`, chatID, fileID, msgID)
	})
}
