-- +goose Up
-- Skills: a SKILL.md (frontmatter name + description, then instructions) and
-- the text files next to it, imported from a zip or a directory. Chats see
-- only the list; the model loads a skill's text when it needs it.
CREATE TABLE skill (
    id          text PRIMARY KEY,
    name        text        NOT NULL UNIQUE,
    description text        NOT NULL DEFAULT '',
    content     text        NOT NULL,
    resources   jsonb       NOT NULL DEFAULT '{}', -- relative path -> file text
    enabled     boolean     NOT NULL DEFAULT true,
    lazy        boolean     NOT NULL DEFAULT true, -- false: the whole SKILL.md rides along in every chat
    managed     boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE access_grant DROP CONSTRAINT access_grant_resource_type_check;
ALTER TABLE access_grant ADD CONSTRAINT access_grant_resource_type_check
    CHECK (resource_type IN ('model', 'tool_server', 'knowledge', 'prompt', 'chat', 'folder', 'skill'));

-- +goose Down
DELETE FROM access_grant WHERE resource_type = 'skill';
ALTER TABLE access_grant DROP CONSTRAINT access_grant_resource_type_check;
ALTER TABLE access_grant ADD CONSTRAINT access_grant_resource_type_check
    CHECK (resource_type IN ('model', 'tool_server', 'knowledge', 'prompt', 'chat', 'folder'));
DROP TABLE skill;
