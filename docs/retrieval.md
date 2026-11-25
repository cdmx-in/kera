# How knowledge search works

This page describes what happens when a chat or a tool searches a knowledge collection, which settings change it, and what an upgrade needs. Settings live in Admin > Settings > Retrieval and in `kera apply` under `rag:`.

## From question to passages

1. **Queries.** The first turn of a chat searches with the user's message. Later turns ask the task model (`tasks.query`) for one to three keyword queries. The `query_knowledge_files` tool sends the model's own query.
2. **Three rankings per query**, over the collections the user can read, each limited to `rag.top_k` (30) candidates:
   - vector search over the chunk embeddings;
   - keyword search over the chunk text;
   - keyword search over the file name and the section headings (the heading path of the chunk).
3. **One fusion.** All rankings of all queries are merged by weighted reciprocal rank fusion (k = 60). A passage that is second for two queries beats one that is first for only one. `rag.hybrid.bm25_weight` sets keyword against vector (0.5), `rag.hybrid.head_weight` the heading ranking (0.25; 0 switches it off), `rag.fuse_queries` (on) the joint ranking of several queries (off: each query keeps its own top results and the lists are joined).
4. **Reranking** when a reranker is configured (`rag.reranker.url`; for chats on local models `rag.reranker.local_url`, see [sidecars/rerank](../sidecars/rerank/README.md)). The reranker judges the candidates against the first query. `rag.relevance_threshold` drops low scores but `rag.min_results` (3) keeps the best few anyway, so a threshold ranks and never empties the answer. `rag.top_k_rerank` (8) passages are kept. Without a reranker the fused order is used.
5. **Neighbours.** Each kept chunk is widened to the `rag.neighbors` (1) chunks before and after it, in the same file. Runs of adjacent chunks become one passage, the overlap two chunks share is written once, a passage is cut at `rag.passage_max_chars` (3000), and files come in the order of their best hit with the passages of a file in document order. `rag.neighbors: 0` returns the lone chunk.

Citations: the passages of a run (the system prompt and every tool result) share one numbering, so `[3]` is the same passage wherever the model read it and in the sources list of the answer. A passage found again by a later search keeps its number.

## What an upgrade needs

| Change | Needed |
|---|---|
| Reranker, query fusion, neighbours, citation numbers | Nothing: they are settings and code. |
| Heading and file-name keyword search (migration `00019_chunk_head`) | The migration creates an empty table and takes only a brief lock. Chunks indexed before it have no heading row, so they match by text only until you run `kera reindex --heads`. It fills the rows in batches without touching the chunk table, can be stopped and repeated, and does not re-embed. New and re-indexed files get their rows when they are indexed. |
| Embedding prefixes | A new embedding model gets the prefixes of its model card when Kera knows the model (`nomic-embed-text`, `mxbai-embed-large`, `e5`), or `rag.embedding.doc_prefix` / `query_prefix`. A model row that exists is not changed: set the two prefix settings and run `kera reindex --all`, which writes them to the model row and re-embeds every chunk with them. Vectors made without a prefix do not match a prefixed query, so do not set a prefix without reindexing. Memory shares the model row, so memories embedded earlier are searched with the new query prefix until they are embedded again. |

Why the heading index is a table of its own: changing the generated column `chunk.tsv` to include headings (`ALTER TABLE ... ALTER COLUMN tsv SET EXPRESSION`, PostgreSQL 17 and later) rewrites the whole chunk table under an exclusive lock and rebuilds every index on it, including the vector indexes. On a large collection that stops search and indexing for as long as the rewrite takes.

## Checking a change on your own questions

Keep a set of questions with the facts a good answer must contain, load your documents into a scratch collection, and compare before and after on the same set: the share of required facts found in the top 5, 10 and 30 passages, and the reciprocal rank of the first passage that has one. Differences smaller than the spread between two identical runs are noise; with a few dozen questions that spread is several points. Each setting above can be switched back on its own (`rag.neighbors: 0`, `rag.hybrid.head_weight: 0`, `rag.fuse_queries: false`, `rag.min_results: 0`), so one change can be measured at a time. `POST /api/v1/retrieval/query/collection` returns `chunk` and `chunk_end` with every passage for this.

Not measured by the tests in this repository: whether each step improves answers on a real collection. The tests use a tiny synthetic corpus and a hash-based embedder to show that each step changes the ranking or the returned text as intended.
