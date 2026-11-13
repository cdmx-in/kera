-- +goose Up
-- Indexes on foreign keys that are filtered or cascaded on by themselves:
-- deleting a file cascades into chunk, chat_file and knowledge_file by
-- file_id; files.CanRead checks chat_file by file_id on every file fetch;
-- deleting a chat cascades into shared_chat; a user's own collections are
-- listed by knowledge.user_id. The existing keys lead with another column.
CREATE INDEX chunk_file ON chunk (file_id);
CREATE INDEX chat_file_file ON chat_file (file_id);
CREATE INDEX knowledge_file_file ON knowledge_file (file_id);
CREATE INDEX shared_chat_chat ON shared_chat (chat_id);
CREATE INDEX knowledge_user ON knowledge (user_id);

-- +goose Down
DROP INDEX knowledge_user;
DROP INDEX shared_chat_chat;
DROP INDEX knowledge_file_file;
DROP INDEX chat_file_file;
DROP INDEX chunk_file;
