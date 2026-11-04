-- +goose Up
-- origin marks rows copied by `kera migrate from-openwebui`, so --prune-missing
-- can remove rows that disappeared from the source without touching rows
-- created in Kera. The tables in 00001 that carry origin already are left alone.
ALTER TABLE api_key        ADD COLUMN origin text NOT NULL DEFAULT 'kera';
ALTER TABLE "group"        ADD COLUMN origin text NOT NULL DEFAULT 'kera';
ALTER TABLE group_member   ADD COLUMN origin text NOT NULL DEFAULT 'kera';
ALTER TABLE access_grant   ADD COLUMN origin text NOT NULL DEFAULT 'kera';
ALTER TABLE message        ADD COLUMN origin text NOT NULL DEFAULT 'kera';
ALTER TABLE shared_chat    ADD COLUMN origin text NOT NULL DEFAULT 'kera';
ALTER TABLE chat_file      ADD COLUMN origin text NOT NULL DEFAULT 'kera';
ALTER TABLE knowledge_file ADD COLUMN origin text NOT NULL DEFAULT 'kera';
ALTER TABLE memory         ADD COLUMN origin text NOT NULL DEFAULT 'kera';

-- +goose Down
ALTER TABLE api_key        DROP COLUMN origin;
ALTER TABLE "group"        DROP COLUMN origin;
ALTER TABLE group_member   DROP COLUMN origin;
ALTER TABLE access_grant   DROP COLUMN origin;
ALTER TABLE message        DROP COLUMN origin;
ALTER TABLE shared_chat    DROP COLUMN origin;
ALTER TABLE chat_file      DROP COLUMN origin;
ALTER TABLE knowledge_file DROP COLUMN origin;
ALTER TABLE memory         DROP COLUMN origin;
