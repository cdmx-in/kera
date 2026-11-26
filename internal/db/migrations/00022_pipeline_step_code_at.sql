-- +goose Up
-- Where in its code a script started a step ("worker.py:42 main()"), as the script reports it. Shown next to the
-- step's title and description; empty when the script does not send it.
ALTER TABLE pipeline_step ADD COLUMN code_at text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE pipeline_step DROP COLUMN code_at;
