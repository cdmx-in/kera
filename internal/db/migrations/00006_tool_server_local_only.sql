-- +goose Up
-- A local-only tool server joins a run only when the model runs on this host
-- (models with local = true), so its data never reaches a hosted provider.
ALTER TABLE tool_server ADD COLUMN local_only boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE tool_server DROP COLUMN local_only;
