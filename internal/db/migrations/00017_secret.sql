-- +goose Up
-- The central secret store: a named, encrypted value that pipeline environment variables can point at
-- instead of keeping their own copy. The value is write-only (internal/secret encrypts it; no route reads it
-- back); rotating a secret is one update, and every pipeline that references it gets it from its next run.
CREATE TABLE secret (
    id           text PRIMARY KEY,
    name         text NOT NULL UNIQUE,
    description  text NOT NULL DEFAULT '',
    value_enc    bytea NOT NULL,
    created_by   text REFERENCES "user" (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   text REFERENCES "user" (id) ON DELETE SET NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz);

-- A variable holds its own value or points at a stored secret, never neither. A referenced secret cannot be deleted.
ALTER TABLE pipeline_env
    ADD COLUMN secret_id text REFERENCES secret (id) ON DELETE RESTRICT,
    ALTER COLUMN value_enc DROP NOT NULL,
    ADD CONSTRAINT pipeline_env_value_or_secret CHECK (value_enc IS NOT NULL OR secret_id IS NOT NULL);
CREATE INDEX pipeline_env_secret ON pipeline_env (secret_id) WHERE secret_id IS NOT NULL;

-- +goose Down
-- A variable that only points at a secret has no value of its own to go back to, so it is dropped.
DELETE FROM pipeline_env WHERE value_enc IS NULL;
DROP INDEX pipeline_env_secret;
ALTER TABLE pipeline_env DROP CONSTRAINT pipeline_env_value_or_secret,
    DROP COLUMN secret_id,
    ALTER COLUMN value_enc SET NOT NULL;
DROP TABLE secret;
