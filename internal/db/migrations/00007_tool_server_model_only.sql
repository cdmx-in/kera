-- +goose Up
-- A model-only tool server joins a run only through a model that lists it
-- (models.tools.servers): it is left out of the chat Tools picker and a
-- request's own tool_ids cannot add it, so its data stays with the assistants
-- built for it. Grants still decide who may use those assistants' tools.
ALTER TABLE tool_server ADD COLUMN model_only boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE tool_server DROP COLUMN model_only;
