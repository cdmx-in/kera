-- +goose Up
-- The audit log: who did what, when, to what (internal/audit). The app only appends to it; the retention
-- purge (audit.retention_days) is the one thing that deletes rows. actor_id has no foreign key, so the history
-- outlives a deleted user, and actor_label keeps the email or name the actor had at the time. meta holds names
-- and flags (the settings that changed, a variable's name), never a secret, token, password or message.
CREATE TABLE audit_event (
    id           text        PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT now(),
    actor_id     text,
    actor_label  text        NOT NULL DEFAULT '',
    actor_kind   text        NOT NULL CHECK (actor_kind IN ('user', 'api_key', 'service', 'system', 'anonymous')),
    action       text        NOT NULL,
    target_type  text        NOT NULL DEFAULT '',
    target_id    text        NOT NULL DEFAULT '',
    target_label text        NOT NULL DEFAULT '',
    outcome      text        NOT NULL DEFAULT 'ok' CHECK (outcome IN ('ok', 'denied', 'failed')),
    ip           text        NOT NULL DEFAULT '',
    user_agent   text        NOT NULL DEFAULT '',
    meta         jsonb       NOT NULL DEFAULT '{}');
CREATE INDEX audit_event_at ON audit_event (at DESC, id DESC);
CREATE INDEX audit_event_actor ON audit_event (actor_id, at DESC) WHERE actor_id IS NOT NULL;
CREATE INDEX audit_event_action ON audit_event (action text_pattern_ops, at DESC);
CREATE INDEX audit_event_target ON audit_event (target_type, target_id, at DESC);

-- +goose Down
DROP TABLE audit_event;
