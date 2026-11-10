-- +goose Up
-- disabled_at locks a user out (sessions and API keys) without deleting them;
-- integrations such as the Google Workspace sync set and clear it.
ALTER TABLE "user" ADD COLUMN disabled_at timestamptz;

-- One row per integration run (scheduled, admin button or CLI), dry runs included.
CREATE TABLE connector_run (
    id          bigserial PRIMARY KEY,
    connector   text NOT NULL,
    trigger     text NOT NULL,
    dry_run     boolean NOT NULL DEFAULT false,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    error       text NOT NULL DEFAULT '',
    report      jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX connector_run_connector_idx ON connector_run (connector, id DESC);

-- +goose Down
DROP TABLE connector_run;
ALTER TABLE "user" DROP COLUMN disabled_at;
