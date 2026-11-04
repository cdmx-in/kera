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

Tests: `pip install -r requirements.txt -r requirements-dev.txt && pytest` (they use a fake
model, so no download is needed).
