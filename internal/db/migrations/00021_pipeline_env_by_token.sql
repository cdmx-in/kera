-- +goose Up
-- A script that runs outside Kera may read its pipeline's variables with the pipeline token (GET
-- /api/v1/pipelines/{name}/env). Off unless an admin switches it on: a token that reports runs reads no secrets.
ALTER TABLE pipeline ADD COLUMN env_by_token boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE pipeline DROP COLUMN env_by_token;
