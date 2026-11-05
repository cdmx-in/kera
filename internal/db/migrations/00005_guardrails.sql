-- +goose Up
-- Guardrail audit: what was redacted, blocked or logged. Counts only, never
-- the matched text.
CREATE TABLE guardrail_event (
    id          bigserial   PRIMARY KEY,
    user_id     text,
    via_api_key boolean     NOT NULL DEFAULT false,
    model_id    text        NOT NULL DEFAULT '',
    rule        text        NOT NULL,
    action      text        NOT NULL,
    count       integer     NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX guardrail_event_created ON guardrail_event (created_at);

-- +goose Down
DROP TABLE guardrail_event;
