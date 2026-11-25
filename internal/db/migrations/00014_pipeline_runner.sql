-- +goose Up
-- Kera-run pipelines: the script, its environment and its timetable live in Kera; a runner
-- (kera runner, a separate container) pulls queued runs and executes them. Runs started
-- from outside (cron, CI) keep working: run_mode 'external' is the default.
ALTER TABLE pipeline
    ADD COLUMN run_mode    text    NOT NULL DEFAULT 'external' CHECK (run_mode IN ('external', 'kera')),
    ADD COLUMN scheduled   boolean NOT NULL DEFAULT false,      -- the timetable below is on
    ADD COLUMN frequency   text    NOT NULL DEFAULT 'daily' CHECK (frequency IN ('hourly', 'daily', 'weekdays', 'weekly')),
    ADD COLUMN at_minute   integer NOT NULL DEFAULT 0 CHECK (at_minute BETWEEN 0 AND 1439), -- minutes after local midnight (hourly: minute of the hour)
    ADD COLUMN weekday     integer NOT NULL DEFAULT 1 CHECK (weekday BETWEEN 0 AND 6),     -- 0 = Sunday
    ADD COLUMN timezone    text    NOT NULL DEFAULT 'UTC',
    ADD COLUMN next_run_at timestamptz;                         -- claimed with compare-and-set, like scheduled_task

-- Every save is a new version; the highest is live. A run records the version it used.
CREATE TABLE pipeline_script (
    pipeline_id text NOT NULL REFERENCES pipeline (id) ON DELETE CASCADE,
    version     integer NOT NULL,
    files       jsonb NOT NULL,                  -- {"refresh.sh": "...", "scripts/a.py": "..."}
    entry       text NOT NULL,                   -- the file that is started
    args        text NOT NULL DEFAULT '',        -- appended to the entry, split like a shell line
    note        text NOT NULL DEFAULT '',
    created_by  text REFERENCES "user" (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (pipeline_id, version));

-- Environment of the script. A secret is write-only: it is encrypted (internal/secret) and never
-- sent back to the browser; it reaches only the runner that claims a run.
CREATE TABLE pipeline_env (
    pipeline_id text NOT NULL REFERENCES pipeline (id) ON DELETE CASCADE,
    name        text NOT NULL,
    value_enc   bytea NOT NULL,
    secret      boolean NOT NULL DEFAULT true,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (pipeline_id, name));

CREATE TABLE pipeline_runner (
    id           text PRIMARY KEY,
    name         text NOT NULL UNIQUE,
    token_hash   text NOT NULL UNIQUE,           -- sha256 of the "kr-" token
    token_prefix text NOT NULL,
    version      text NOT NULL DEFAULT '',
    last_seen_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now());

-- queued: opened by the timetable or "Run now", waiting for a runner to claim it.
ALTER TABLE pipeline_run
    ADD COLUMN queued           boolean NOT NULL DEFAULT false,
    ADD COLUMN claimed_at       timestamptz,
    ADD COLUMN runner_id        text,
    ADD COLUMN cancel_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN script_version   integer;
CREATE INDEX pipeline_run_queued ON pipeline_run (id) WHERE queued AND finished_at IS NULL;

-- +goose Down
DROP INDEX pipeline_run_queued;
ALTER TABLE pipeline_run DROP COLUMN queued, DROP COLUMN claimed_at, DROP COLUMN runner_id,
    DROP COLUMN cancel_requested, DROP COLUMN script_version;
DROP TABLE pipeline_runner;
DROP TABLE pipeline_env;
DROP TABLE pipeline_script;
ALTER TABLE pipeline DROP COLUMN run_mode, DROP COLUMN scheduled, DROP COLUMN frequency, DROP COLUMN at_minute,
    DROP COLUMN weekday, DROP COLUMN timezone, DROP COLUMN next_run_at;
