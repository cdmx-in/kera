-- +goose Up
-- Sign-in tokens carry the user's session_epoch. Changing or resetting a
-- password adds one, so every token issued before it stops working (see
-- auth.Resolve). Tokens made before this column existed carry no epoch, which
-- reads as 0, so they keep working until the first password change.
ALTER TABLE "user" ADD COLUMN session_epoch bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE "user" DROP COLUMN session_epoch;
