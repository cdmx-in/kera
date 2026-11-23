-- +goose Up
-- Pipelines (internal/pipeline): outside scripts keep knowledge collections in
-- sync through a token API and report their steps into a run that the admin
-- page draws as a flow. The sync itself lives in internal/knowledge/sync.go.
CREATE TABLE pipeline (
    id                text PRIMARY KEY,
    name              text NOT NULL UNIQUE,            -- slug: URLs, CLI, YAML
    title             text NOT NULL DEFAULT '',
    user_id           text NOT NULL REFERENCES "user" (id) ON DELETE RESTRICT, -- owns the files it uploads
    targets           text[] NOT NULL DEFAULT '{}',    -- knowledge ids it may write; no FK: a deleted collection stops resolving
    every_minutes     integer NOT NULL DEFAULT 0 CHECK (every_minutes >= 0),   -- a good run is expected this often; 0 = none
    token_hash        text UNIQUE,                     -- sha256 of the "kp-" token
    token_prefix      text NOT NULL DEFAULT '',
    adopt             boolean NOT NULL DEFAULT false,  -- one-shot, admin only
    allow_mass_delete boolean NOT NULL DEFAULT false,  -- one-shot, admin only
    enabled           boolean NOT NULL DEFAULT true,
    managed           boolean NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now());
CREATE INDEX pipeline_user ON pipeline (user_id);

CREATE TABLE pipeline_run (
    id          bigserial PRIMARY KEY,                 -- like connector_run (00008)
    pipeline_id text NOT NULL REFERENCES pipeline (id) ON DELETE CASCADE,
    trigger     text NOT NULL DEFAULT 'script',        -- script | push
    dry_run     boolean NOT NULL DEFAULT false,
    status      text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'ok', 'warning', 'failed')),
    client_done boolean NOT NULL DEFAULT false,        -- the script finished; Kera may still be indexing
    targets     jsonb NOT NULL DEFAULT '{}',           -- knowledge id -> {complete, listed}
    error       text NOT NULL DEFAULT '',
    started_at  timestamptz NOT NULL DEFAULT now(),
    active_at   timestamptz NOT NULL DEFAULT now(),    -- last client call or indexing progress
    finished_at timestamptz);
CREATE INDEX pipeline_run_pipeline ON pipeline_run (pipeline_id, id DESC);
-- the one-open-run lock, across processes
CREATE UNIQUE INDEX pipeline_run_open ON pipeline_run (pipeline_id) WHERE finished_at IS NULL;

CREATE TABLE pipeline_step (
    run_id      bigint NOT NULL REFERENCES pipeline_run (id) ON DELETE CASCADE,
    seq         integer NOT NULL,                      -- order of first appearance
    kera        boolean NOT NULL DEFAULT false,        -- recorded by Kera (one per collection); a script cannot write these
    key         text NOT NULL,                         -- script step: its name; Kera step: the knowledge id (names are not unique)
    name        text NOT NULL,                         -- shown: the step name, or the collection name
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'ok', 'warning', 'failed', 'skipped')),
    started_at  timestamptz,
    finished_at timestamptz,
    error       text NOT NULL DEFAULT '',
    counts      jsonb NOT NULL DEFAULT '{}',
    log         text NOT NULL DEFAULT '',              -- ponytail: tail only; a pipeline_log table if search is needed
    log_dropped boolean NOT NULL DEFAULT false,
    PRIMARY KEY (run_id, seq),
    UNIQUE (run_id, kera, key));

-- replaces: a staged new version. The row is hidden from every reader and
-- swapped in by the ingest worker, in the transaction that stores its chunks,
-- once it is indexed; replaces is the file id of the version it takes over.
-- sync_run: the last run that listed the row. No FK: it is only compared
-- with the open run.
ALTER TABLE knowledge_file
    ADD COLUMN replaces text,
    ADD COLUMN sync_run bigint;

-- +goose Down
-- Staged versions go first, with their file rows: an older binary would
-- serve both versions of a document. ponytail: their blobs stay on disk (SQL
-- cannot delete them); a rollback is rare, and a sweep of uploads/ is the fix.
WITH d AS (DELETE FROM knowledge_file WHERE replaces IS NOT NULL RETURNING file_id)
DELETE FROM file WHERE id IN (SELECT file_id FROM d);
ALTER TABLE knowledge_file DROP COLUMN replaces, DROP COLUMN sync_run;
DROP TABLE pipeline_step;
DROP TABLE pipeline_run;
DROP TABLE pipeline;
