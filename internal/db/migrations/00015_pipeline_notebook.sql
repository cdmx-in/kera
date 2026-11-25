-- +goose Up
-- Script notebook (internal/pipeline/session.go): an interactive session on a runner for trying the cells of a
-- script, a dry-run pipeline run behind it (so `kera pipeline push` in a cell can only plan), and one-shot syntax checks.

-- A dry run changes nothing, so it does not take the one-open-run lock: a notebook session beside a scheduled run is fine.
DROP INDEX pipeline_run_open;
CREATE UNIQUE INDEX pipeline_run_open ON pipeline_run (pipeline_id) WHERE finished_at IS NULL AND NOT dry_run;

CREATE TABLE pipeline_session (
    id          text PRIMARY KEY,
    pipeline_id text NOT NULL REFERENCES pipeline (id) ON DELETE CASCADE,
    user_id     text REFERENCES "user" (id) ON DELETE SET NULL,
    run_id      bigint,                                    -- the dry run behind it (no FK: runs are trimmed)
    runner_id   text,                                      -- the runner that took it
    lang        text NOT NULL CHECK (lang IN ('bash', 'python')),
    entry       text NOT NULL,
    files       jsonb NOT NULL,                            -- the editor's files when it started
    real        boolean NOT NULL DEFAULT false,            -- run in the pipeline's real workspace instead of a scratch folder
    status      text NOT NULL DEFAULT 'starting' CHECK (status IN ('starting', 'ready', 'closing', 'closed')),
    error       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    active_at   timestamptz NOT NULL DEFAULT now(),
    closed_at   timestamptz);
CREATE INDEX pipeline_session_pipeline ON pipeline_session (pipeline_id, created_at DESC);

CREATE TABLE pipeline_cell (
    id          bigserial PRIMARY KEY,
    session_id  text NOT NULL REFERENCES pipeline_session (id) ON DELETE CASCADE,
    name        text NOT NULL DEFAULT '',
    code        text NOT NULL,
    status      text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'ok', 'failed', 'stopped')),
    output      text NOT NULL DEFAULT '',
    exit_code   integer,
    stop        boolean NOT NULL DEFAULT false,            -- an admin asked to interrupt it
    stop_sent   boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now());
CREATE INDEX pipeline_cell_session ON pipeline_cell (session_id, id);

CREATE TABLE pipeline_check (
    id          text PRIMARY KEY,
    pipeline_id text NOT NULL REFERENCES pipeline (id) ON DELETE CASCADE,
    files       jsonb NOT NULL,
    entry       text NOT NULL,
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'claimed', 'done')),
    result      jsonb,
    created_at  timestamptz NOT NULL DEFAULT now());

-- +goose Down
DROP TABLE pipeline_check;
DROP TABLE pipeline_cell;
DROP TABLE pipeline_session;
DROP INDEX pipeline_run_open;
CREATE UNIQUE INDEX pipeline_run_open ON pipeline_run (pipeline_id) WHERE finished_at IS NULL;
