-- +goose Up
-- What a pipeline's stages are called and do. A script only reports step names ("fetch-alf"); the admin pages show the
-- title and description kept here (the name stays the key), so a run reads as sentences instead of slugs.
CREATE TABLE pipeline_stage (
    pipeline_id text NOT NULL REFERENCES pipeline (id) ON DELETE CASCADE,
    name        text NOT NULL,
    title       text NOT NULL DEFAULT '',
    about       text NOT NULL DEFAULT '',
    position    integer NOT NULL DEFAULT 0,
    PRIMARY KEY (pipeline_id, name));

-- +goose Down
DROP TABLE pipeline_stage;
