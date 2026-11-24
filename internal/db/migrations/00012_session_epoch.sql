-- +goose Up
-- Sign-in tokens carry the user's session_epoch. Changing or resetting a
-- password adds one, so every token issued before it stops working (see
-- auth.Resolve). Tokens made before this column existed carry no epoch, which
-- reads as 0, so they keep working until the first password change.
ALTER TABLE "user" ADD COLUMN session_epoch bigint NOT NULL DEFAULT 0;

-- Password sign-in is on by default only while no OAuth login provider exists (identity.passwordEnabled).
-- A deployment that already has one gets the setting pinned to false, so removing the provider
-- later cannot switch the password form on by itself. A value an admin set is left alone.
INSERT INTO config (key, value, managed)
SELECT 'auth.password.enabled', 'false', false
WHERE EXISTS (SELECT 1 FROM oauth_provider WHERE options @> '{"login": true}')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
ALTER TABLE "user" DROP COLUMN session_epoch;
