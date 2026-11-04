// Package chats stores conversations: chats, the message tree, folders,
// tags and shares. The chat engine writes through Store; http.go serves the
// Open WebUI-compatible /api/v1/chats and /api/v1/folders routes.
package chats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/ids"
)

// ErrNotFound is returned for chats, messages and folders that do not exist
// or belong to someone else (the API never reveals which).
var ErrNotFound = api.Errf(http.StatusNotFound, "not_found", "Chat not found")

// Chat is one chat row.
type Chat struct {
	ID               string
	UserID           string
	Title            string
	FolderID         *string
	Pinned           bool
	Archived         bool
	ShareID          *string
	CurrentMessageID *string
	Locality         *string
	ModelIDs         []string
	Summary          string
	Meta             map[string]any
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Message is one message row. Parts, Files, Sources and PendingCalls hold
// JSON in the streamed shape.
type Message struct {
	ID             string
	ChatID         string
	ParentID       *string
	Role           string
	ModelID        string
	Content        string
	Parts          json.RawMessage
	Files          json.RawMessage
	Sources        json.RawMessage
	PendingCalls   json.RawMessage
	FollowUps      json.RawMessage
	Usage          json.RawMessage
	Status         string
	Error          string
	ContextSummary string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Store is the chat persistence layer.
type Store struct {
	DB   *pgxpool.Pool
	live LiveFunc
}

const chatCols = `id, user_id, title, folder_id, pinned, archived, share_id, current_message_id, locality,
	model_ids, summary, meta, created_at, updated_at`

func scanChat(row pgx.Row) (Chat, error) {
	var c Chat
	err := row.Scan(&c.ID, &c.UserID, &c.Title, &c.FolderID, &c.Pinned, &c.Archived, &c.ShareID, &c.CurrentMessageID,
		&c.Locality, &c.ModelIDs, &c.Summary, &c.Meta, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Get returns chat id when userID owns it ("" skips the owner check).
func (s *Store) Get(ctx context.Context, userID, id string) (Chat, error) {
	c, err := scanChat(s.DB.QueryRow(ctx, `SELECT `+chatCols+` FROM chat WHERE id = $1 AND ($2 = '' OR user_id = $2)`, id, userID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return c, fmt.Errorf("get chat: %w", err)
	}
	return c, err
}

// Create inserts a chat. A blank id gets a new one.
func (s *Store) Create(ctx context.Context, c *Chat) error {
	if c.ID == "" {
		c.ID = ids.New()
	}
	if c.Title == "" {
		c.Title = "New Chat"
	}
	if c.Meta == nil {
		c.Meta = map[string]any{}
	}
	if c.ModelIDs == nil {
		c.ModelIDs = []string{}
	}
	err := s.DB.QueryRow(ctx, `INSERT INTO chat (id, user_id, title, folder_id, pinned, model_ids, meta)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, updated_at`,
		c.ID, c.UserID, c.Title, c.FolderID, c.Pinned, c.ModelIDs, c.Meta).Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create chat: %w", err)
	}
	return nil
}

// Delete removes a chat (messages, tags and file links cascade).
func (s *Store) Delete(ctx context.Context, userID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM chat WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete chat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Update applies a SQL SET fragment to the caller's chat and returns the row.
// set must only reference $3.. placeholders filled by args.
func (s *Store) update(ctx context.Context, userID, id, set string, args ...any) (Chat, error) {
	c, err := scanChat(s.DB.QueryRow(ctx, `UPDATE chat SET `+set+`, updated_at = now() WHERE id = $1 AND user_id = $2
		RETURNING `+chatCols, append([]any{id, userID}, args...)...))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return c, fmt.Errorf("update chat: %w", err)
	}
	return c, err
}

// SetTitle renames a chat.
func (s *Store) SetTitle(ctx context.Context, userID, id, title string) (Chat, error) {
	return s.update(ctx, userID, id, `title = $3`, title)
}

// SetCurrent moves the active leaf (the message must belong to the chat).
func (s *Store) SetCurrent(ctx context.Context, chatID, messageID string) error {
	_, err := s.DB.Exec(ctx, `UPDATE chat SET current_message_id = $2, updated_at = now()
		WHERE id = $1 AND EXISTS (SELECT 1 FROM message WHERE id = $2 AND chat_id = $1)`, chatID, messageID)
	if err != nil {
		return fmt.Errorf("set current message: %w", err)
	}
	return nil
}

// SetLocality records where a chat's replies were produced. A local reply
// locks the chat to local models; a cloud reply only fills an empty value.
func (s *Store) SetLocality(ctx context.Context, chatID string, local bool) error {
	q := `UPDATE chat SET locality = 'local' WHERE id = $1`
	if !local {
		q = `UPDATE chat SET locality = 'cloud' WHERE id = $1 AND locality IS NULL`
	}
	if _, err := s.DB.Exec(ctx, q, chatID); err != nil {
		return fmt.Errorf("set locality: %w", err)
	}
	return nil
}

// AddModel appends a model id to chat.model_ids once.
func (s *Store) AddModel(ctx context.Context, chatID, modelID string) error {
	_, err := s.DB.Exec(ctx, `UPDATE chat SET model_ids = array_append(model_ids, $2)
		WHERE id = $1 AND NOT ($2 = ANY(model_ids))`, chatID, modelID)
	return err
}

// ---- messages -------------------------------------------------------------------

const msgCols = `id, chat_id, parent_id, role, model_id, content, parts, files, sources, pending_calls, follow_ups,
	usage, status, error, context_summary, created_at, updated_at`

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.ChatID, &m.ParentID, &m.Role, &m.ModelID, &m.Content, &m.Parts, &m.Files, &m.Sources,
		&m.PendingCalls, &m.FollowUps, &m.Usage, &m.Status, &m.Error, &m.ContextSummary, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func collectMessages(rows pgx.Rows, err error) ([]Message, error) {
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Messages returns every message of a chat, oldest first.
func (s *Store) Messages(ctx context.Context, chatID string) ([]Message, error) {
	return collectMessages(s.DB.Query(ctx, `SELECT `+msgCols+` FROM message WHERE chat_id = $1 ORDER BY created_at, id`, chatID))
}

// Message returns one message of a chat.
func (s *Store) Message(ctx context.Context, chatID, id string) (Message, error) {
	m, err := scanMessage(s.DB.QueryRow(ctx, `SELECT `+msgCols+` FROM message WHERE id = $1 AND chat_id = $2`, id, chatID))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, api.Errf(http.StatusNotFound, "not_found", "Message not found")
	}
	return m, err
}

// Branch returns the path from the root to leafID, root first.
func (s *Store) Branch(ctx context.Context, chatID, leafID string) ([]Message, error) {
	return collectMessages(s.DB.Query(ctx, `WITH RECURSIVE up AS (
			SELECT `+msgCols+`, 0 AS depth FROM message WHERE id = $2 AND chat_id = $1
			UNION ALL
			SELECT m.id, m.chat_id, m.parent_id, m.role, m.model_id, m.content, m.parts, m.files, m.sources, m.pending_calls,
				m.follow_ups, m.usage, m.status, m.error, m.context_summary, m.created_at, m.updated_at, up.depth + 1
			FROM message m JOIN up ON m.id = up.parent_id WHERE m.chat_id = $1 AND up.depth < 10000)
		SELECT `+msgCols+` FROM up ORDER BY depth DESC`, chatID, leafID))
}

// InsertMessage adds a message; an existing id in the same chat is left as is.
func (s *Store) InsertMessage(ctx context.Context, m *Message) error {
	if m.ID == "" {
		m.ID = ids.New()
	}
	if m.Status == "" {
		m.Status = "done"
	}
	tag, err := s.DB.Exec(ctx, `INSERT INTO message (id, chat_id, parent_id, role, model_id, content, parts, files, sources,
			follow_ups, usage, status, error)
		VALUES ($1, $2, $3, $4, $5, $6, coalesce($7, '[]'::jsonb), coalesce($8, '[]'::jsonb), coalesce($9, '[]'::jsonb),
			coalesce($10, '[]'::jsonb), coalesce($11, '{}'::jsonb), $12, $13)
		ON CONFLICT (id) DO NOTHING`,
		m.ID, m.ChatID, m.ParentID, m.Role, m.ModelID, m.Content, nullJSON(m.Parts), nullJSON(m.Files), nullJSON(m.Sources),
		nullJSON(m.FollowUps), nullJSON(m.Usage), m.Status, m.Error)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var chat string
		if err := s.DB.QueryRow(ctx, `SELECT chat_id FROM message WHERE id = $1`, m.ID).Scan(&chat); err != nil {
			return fmt.Errorf("check message: %w", err)
		}
		if chat != m.ChatID {
			return api.Errf(http.StatusConflict, "message_conflict", "message id belongs to another chat")
		}
	}
	return nil
}

// Progress is the state of an assistant message the engine writes back.
type Progress struct {
	Content        string
	Parts          any
	Files          any
	Sources        any
	PendingCalls   any
	FollowUps      any
	Usage          any
	Status         string
	Error          string
	ContextSummary *string // nil leaves it unchanged
}

// SaveProgress writes an assistant message's streamed state.
func (s *Store) SaveProgress(ctx context.Context, messageID string, p Progress) error {
	_, err := s.DB.Exec(ctx, `UPDATE message SET content = $2, parts = $3, files = $4, sources = $5, pending_calls = $6,
			follow_ups = $7, usage = $8, status = $9, error = $10, context_summary = coalesce($11, context_summary),
			updated_at = now()
		WHERE id = $1`, messageID, p.Content, jsonOr(p.Parts, "[]"), jsonOr(p.Files, "[]"), jsonOr(p.Sources, "[]"),
		jsonOr(p.PendingCalls, "[]"), jsonOr(p.FollowUps, "[]"), jsonOr(p.Usage, "{}"), p.Status, p.Error, p.ContextSummary)
	if err != nil {
		return fmt.Errorf("save message %s: %w", messageID, err)
	}
	return nil
}

// SetContextSummary marks messageID as the last message a compaction summary covers.
func (s *Store) SetContextSummary(ctx context.Context, chatID, messageID, summary string) error {
	_, err := s.DB.Exec(ctx, `UPDATE message SET context_summary = $3 WHERE id = $2 AND chat_id = $1`, chatID, messageID, summary)
	if err == nil {
		_, err = s.DB.Exec(ctx, `UPDATE chat SET summary = $2 WHERE id = $1`, chatID, summary)
	}
	if err != nil {
		return fmt.Errorf("save summary: %w", err)
	}
	return nil
}

// EditMessage replaces a message's text.
func (s *Store) EditMessage(ctx context.Context, userID, chatID, id, content string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE message m SET content = $4,
			parts = CASE WHEN jsonb_array_length(m.parts) = 0 THEN m.parts
				ELSE (SELECT coalesce(jsonb_agg(p), '[]') FROM jsonb_array_elements(m.parts) p WHERE p->>'type' <> 'text')
					|| jsonb_build_array(jsonb_build_object('type', 'text', 'text', $4::text)) END,
			updated_at = now()
		FROM chat c WHERE m.id = $3 AND m.chat_id = $2 AND c.id = m.chat_id AND c.user_id = $1`, userID, chatID, id, content)
	if err != nil {
		return fmt.Errorf("edit message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteMessage removes a message with its descendants and moves the active
// leaf to the parent when it was inside the removed subtree.
func (s *Store) DeleteMessage(ctx context.Context, userID, chatID, id string) error {
	c, err := s.Get(ctx, userID, chatID)
	if err != nil {
		return err
	}
	m, err := s.Message(ctx, chatID, id)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var removed []string
	rows, err := tx.Query(ctx, `WITH RECURSIVE down AS (SELECT id FROM message WHERE id = $2 AND chat_id = $1
			UNION ALL SELECT m.id FROM message m JOIN down ON m.parent_id = down.id WHERE m.chat_id = $1)
		DELETE FROM message WHERE id IN (SELECT id FROM down) RETURNING id`, chatID, id)
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	if removed, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	if c.CurrentMessageID != nil && contains(removed, *c.CurrentMessageID) {
		if _, err := tx.Exec(ctx, `UPDATE chat SET current_message_id = $2, updated_at = now() WHERE id = $1`, chatID, m.ParentID); err != nil {
			return fmt.Errorf("move current message: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// LinkFiles records that files belong to a chat (gives the owner read access
// through the chat and lets list_chat_files find them).
func (s *Store) LinkFiles(ctx context.Context, chatID, messageID string, fileIDs []string) error {
	for _, f := range fileIDs {
		if _, err := s.DB.Exec(ctx, `INSERT INTO chat_file (chat_id, file_id, message_id)
			SELECT $1, $2, $3 WHERE EXISTS (SELECT 1 FROM file WHERE id = $2) ON CONFLICT DO NOTHING`, chatID, f, messageID); err != nil {
			return fmt.Errorf("link file: %w", err)
		}
	}
	return nil
}

// ---- tags -------------------------------------------------------------------------

// Tags returns a chat's tags.
func (s *Store) Tags(ctx context.Context, chatID string) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT tag FROM chat_tag WHERE chat_id = $1 ORDER BY tag`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// AddTags adds tags (normalised to lower-case, trimmed, max 40 chars).
func (s *Store) AddTags(ctx context.Context, chatID string, tags []string) error {
	for _, t := range tags {
		if t = NormalizeTag(t); t == "" {
			continue
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO chat_tag (chat_id, tag) VALUES ($1, $2) ON CONFLICT DO NOTHING`, chatID, t); err != nil {
			return fmt.Errorf("add tag: %w", err)
		}
	}
	return nil
}

// NormalizeTag lower-cases a tag and turns spaces into underscores.
func NormalizeTag(t string) string {
	t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#")))
	t = strings.Join(strings.Fields(t), "_")
	if r := []rune(t); len(r) > 40 {
		t = string(r[:40])
	}
	return t
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// jsonOr marshals v; nil (or a nil slice/map) becomes the empty literal.
func jsonOr(v any, empty string) []byte {
	if v == nil {
		return []byte(empty)
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return []byte(empty)
	}
	return b
}
