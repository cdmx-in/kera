-- +goose Up
-- Keyword index of chunk headings and file names. A chunk's own tsv covers its text only, so a word that
-- appears only in the section heading or the file name cannot be found by keyword. chunk_head holds one
-- weighted tsvector per chunk: the file name and the heading breadcrumb, weight A.
--
-- It is a table of its own, not a change to chunk.tsv, on purpose: changing the generated expression of
-- chunk.tsv rewrites the whole chunk table under an ACCESS EXCLUSIVE lock and rebuilds every index on it
-- (including the HNSW indexes), so search and ingest stand still for the length of the rewrite. Here the
-- migration only creates an empty table (the foreign key takes a brief SHARE ROW EXCLUSIVE lock on chunk).
-- Rows are written when a file is indexed; existing chunks are filled in without locks and without touching
-- the chunk rows by `kera reindex --heads`. Searches work with a partly filled table: a chunk without a
-- row just has no heading match.
CREATE TABLE chunk_head (
    chunk_id bigint   PRIMARY KEY REFERENCES chunk (id) ON DELETE CASCADE,
    tsv      tsvector NOT NULL
);
CREATE INDEX chunk_head_tsv ON chunk_head USING gin (tsv);

-- +goose Down
DROP TABLE chunk_head;
