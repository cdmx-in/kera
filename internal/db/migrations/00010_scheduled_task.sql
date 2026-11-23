-- +goose Up
-- A saved Cowork task the server starts by itself (internal/schedule). Each run is
-- an ordinary cowork chat; last_chat_id points at the newest one. tool_ids,
-- features and files are what the web app would have sent for the same task
-- (features NULL = the assistant's defaults). weekday is 0-6 with 0 = Sunday;
-- at_minute is minutes after local midnight (hourly: the minute of the hour).
CREATE TABLE scheduled_task (
    id           text PRIMARY KEY,
    user_id      text NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    title        text NOT NULL,
    prompt       text NOT NULL,
    model_id     text NOT NULL,
    tool_ids     text[] NOT NULL DEFAULT '{}',
    features     jsonb,
    files        jsonb NOT NULL DEFAULT '[]',
    frequency    text NOT NULL CHECK (frequency IN ('hourly', 'daily', 'weekdays', 'weekly')),
    at_minute    integer NOT NULL CHECK (at_minute BETWEEN 0 AND 1439),
    weekday      integer NOT NULL DEFAULT 0 CHECK (weekday BETWEEN 0 AND 6),
    timezone     text NOT NULL,
    enabled      boolean NOT NULL DEFAULT true,
    notes        text NOT NULL DEFAULT '',
    next_run_at  timestamptz NOT NULL,
    last_run_at  timestamptz,
    last_chat_id text REFERENCES chat (id) ON DELETE SET NULL,
    last_error   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX scheduled_task_due ON scheduled_task (next_run_at) WHERE enabled;
CREATE INDEX scheduled_task_user ON scheduled_task (user_id);
CREATE INDEX scheduled_task_chat ON scheduled_task (last_chat_id);

-- +goose Down
DROP TABLE scheduled_task;
