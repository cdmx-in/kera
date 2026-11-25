# kera-rerank

Optional reranker sidecar for Kera's knowledge search: a sentence-transformers
cross-encoder on CPU behind the Cohere/Jina-style `/rerank` contract, so TEI,
vLLM or LiteLLM can replace it without changes in Kera.

```
POST /rerank {"query": "...", "documents": ["...", {"text": "..."}], "top_n": 6}
  -> {"model": "...", "results": [{"index": 1, "relevance_score": 0.93}, ...]}   best first
GET  /health -> {"status": "ok", "model": "...", "loaded": false}
```

Environment: `RERANK_MODEL` (default `BAAI/bge-reranker-v2-m3`), `RERANK_MAX_LENGTH` (512),
`RERANK_MAX_DOCS` (200), `RERANK_BATCH` (16), `OMP_NUM_THREADS` (4). The model is downloaded
on the first request into `/models`; mount a volume there to keep it across restarts.

```
docker build -t kera-rerank sidecars/rerank
docker run -d -p 127.0.0.1:8124:8000 -v kera-rerank-models:/models kera-rerank
```

Point Kera at it with `rag.reranker.url: http://kera-rerank:8000` (and `rag.reranker.model`).
A hosted reranker works the same way: LiteLLM's `/rerank` (any rerank model it serves, e.g. Cohere),
Cohere or TEI, with `rag.reranker.api_key` sent as `Authorization: Bearer`. Chats on local models
do not send passages to `rag.reranker.url` unless `rag.reranker.local_runs: allow`; set
`rag.reranker.local_url` to give them an on-prem reranker such as this sidecar.

Suggested settings when the sidecar serves local-model chats:

```yaml
rag:
  top_k: 30               # candidates sent to the reranker; the CPU cost grows with this
  top_k_rerank: 8         # passages kept (the default)
  min_results: 3          # a threshold never leaves fewer than this (the default)
  relevance_threshold: 0  # rank, do not cut; raise it only after measuring recall
  reranker:
    local_url: http://kera-rerank:8000
    model: BAAI/bge-reranker-v2-m3
```

A search reranks `top_k` candidates in one request, so its latency is the sidecar's time for that many
passages of up to `RERANK_MAX_LENGTH` tokens. Time it on your hardware before turning it on (`curl -w
'%{time_total}\n'` on one `POST /rerank` with 30 passages of about 1,000 characters); lower `top_k` if it
is too slow. When the sidecar fails, searches fall back to the fused order for five minutes.

Tests: `pip install -r requirements.txt -r requirements-dev.txt && pytest` (they use a fake
model, so no download is needed).
