-- +goose Up
-- A cowork chat is a task: the assistant works on a goal on its own. Task
-- chats are listed on their own page, never in the chat sidebar.
ALTER TABLE chat ADD COLUMN mode text NOT NULL DEFAULT 'chat' CHECK (mode IN ('chat', 'cowork'));

-- +goose Down
ALTER TABLE chat DROP COLUMN mode;
