-- +goose Up
-- The run trace: one row per model round, tool call, retrieval and compaction of a chat or task run.
-- It holds sizes, counts, timings and flags, never prompt text, tool arguments or tool results, so it
-- stores nothing that needs encrypting. No foreign keys: rows outlive the chat and are purged by age
-- (setting trace.retention_days). Admin-only reads (internal/admin/trace.go).
CREATE TABLE run_step (
    id                bigserial   PRIMARY KEY,
    run_id            text        NOT NULL,
    message_id        text        NOT NULL,
    user_id           text        NOT NULL,
    model_id          text        NOT NULL,
    seq               integer     NOT NULL,
    round             integer     NOT NULL DEFAULT 0,
    kind              text        NOT NULL CHECK (kind IN ('model', 'tool', 'retrieval', 'compaction')),
    name              text        NOT NULL DEFAULT '',
    started_at        timestamptz NOT NULL,
    duration_ms       integer     NOT NULL DEFAULT 0,
    prompt_tokens     integer,
    cached_tokens     integer,
    completion_tokens integer,
    finish            text        NOT NULL DEFAULT '',
    error             text        NOT NULL DEFAULT '',
    detail            jsonb       NOT NULL DEFAULT '{}'
);
CREATE INDEX run_step_message ON run_step (message_id, seq);
CREATE INDEX run_step_run ON run_step (run_id, seq);
CREATE INDEX run_step_started ON run_step (started_at);

-- +goose Down
DROP TABLE run_step;
