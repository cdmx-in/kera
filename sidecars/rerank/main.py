"""Kera reranker: a cross-encoder behind a Cohere/Jina-style /rerank endpoint.

POST /rerank {"model"?, "query", "documents": [str | {"text"}], "top_n"?, "return_documents"?}
  -> {"model", "results": [{"index", "relevance_score", "document"?: {"text"}}]} best first
GET /health -> {"status": "ok", "model", "loaded"}

The model (RERANK_MODEL, default BAAI/bge-reranker-v2-m3) runs on CPU and is
downloaded on first use into HF_HOME (mount a volume there to keep it).
"""

import os
import threading

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

MODEL = os.environ.get("RERANK_MODEL", "BAAI/bge-reranker-v2-m3")
MAX_LENGTH = int(os.environ.get("RERANK_MAX_LENGTH", "512"))
MAX_DOCS = int(os.environ.get("RERANK_MAX_DOCS", "200"))
BATCH = int(os.environ.get("RERANK_BATCH", "16"))

app = FastAPI(title="kera-rerank", docs_url=None, redoc_url=None)

_model = None
_lock = threading.Lock()


def load_model():
    from sentence_transformers import CrossEncoder

    return CrossEncoder(MODEL, device="cpu", max_length=MAX_LENGTH)


def model():
    global _model
    with _lock:
        if _model is None:
            _model = load_model()
        return _model


class RerankRequest(BaseModel):
    query: str
    documents: list[str | dict]
    model: str | None = None  # accepted for API compatibility; the served model is fixed
    top_n: int | None = None
    return_documents: bool = False


def doc_text(d):
    if isinstance(d, str):
        return d
    t = d.get("text")
    if not isinstance(t, str):
        raise HTTPException(422, "each document must be a string or {text}")
    return t


@app.post("/rerank")
def rerank(req: RerankRequest):
    if len(req.documents) > MAX_DOCS:
        raise HTTPException(413, f"at most {MAX_DOCS} documents per request")
    texts = [doc_text(d) for d in req.documents]
    if not texts:
        return {"model": MODEL, "results": []}
    scores = model().predict([(req.query, t) for t in texts], batch_size=BATCH, show_progress_bar=False)
    order = sorted(range(len(texts)), key=lambda i: float(scores[i]), reverse=True)
    if req.top_n is not None and req.top_n > 0:
        order = order[: req.top_n]
    results = []
    for i in order:
        r = {"index": i, "relevance_score": float(scores[i])}
        if req.return_documents:
            r["document"] = {"text": texts[i]}
        results.append(r)
    return {"model": MODEL, "results": results}


@app.get("/health")
def health():
    return {"status": "ok", "model": MODEL, "loaded": _model is not None}
