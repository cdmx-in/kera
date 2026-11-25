-- +goose Up
-- Where a run came from (host, command, folder, client), reported by the script or the kera CLI when it opens
-- the run. Kera never starts scripts, so this is how the admin pages can say where one lives.
ALTER TABLE pipeline_run ADD COLUMN source jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE pipeline_run DROP COLUMN source;
