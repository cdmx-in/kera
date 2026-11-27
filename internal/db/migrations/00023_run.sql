-- +goose Up
-- Live agent runs of stored chats (internal/engine/durable.go, docs/durable-runs.md). One row per run that has not
-- ended; the row is deleted when the run ends (message, usage and run_step keep the outcome). It is a table of its own
-- because the lease is renewed every few seconds, which on message would churn its text index. Nothing in it is
-- encrypted. checkpoint holds the run's working state, conversation and tool-result text included, so it must never
-- be logged; deleting the message (also through its chat or user) deletes the row.
CREATE TABLE run (
    id               text        PRIMARY KEY,          -- the same id as run_step.run_id
    message_id       text        NOT NULL UNIQUE REFERENCES message (id) ON DELETE CASCADE,
    chat_id          text        NOT NULL,
    user_id          text        NOT NULL,
    origin           text        NOT NULL DEFAULT '',  -- '' | schedule:<task> | test:<user>
    request          jsonb       NOT NULL,             -- what is needed to build the run again, without messages
    state            text        NOT NULL CHECK (state IN ('queued', 'running', 'waiting')),
    lease_owner      text,
    lease_until      timestamptz,
    attempt          integer     NOT NULL DEFAULT 1,   -- fences writes and numbers the events of each attempt
    recoveries       integer     NOT NULL DEFAULT 0,   -- claims of an expired lease
    cancel_requested boolean     NOT NULL DEFAULT false,
    wait_key         text,                             -- call id of the open question
    wake_at          timestamptz,
    answer           jsonb,                            -- JSON null: the question timed out
    deadline         timestamptz,                      -- wall-clock limit of an unattended run
    checkpoint       bytea,                            -- JSON; bytea because jsonb refuses NUL, which tool output can hold
    created_at       timestamptz NOT NULL DEFAULT now()
);

-- the janitor's lookup of replies that are marked as being written
CREATE INDEX message_streaming ON message (updated_at) WHERE status = 'streaming';

-- +goose Down
DROP INDEX message_streaming;
DROP TABLE run;
