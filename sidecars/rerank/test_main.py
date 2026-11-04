from fastapi.testclient import TestClient

import main


class FakeEncoder:
    """Scores a document by how many query words it contains."""

    def predict(self, pairs, batch_size=16, show_progress_bar=False):
        return [sum(w in d.lower() for w in q.lower().split()) / 10 for q, d in pairs]


def client(monkeypatch):
    monkeypatch.setattr(main, "_model", FakeEncoder())
    return TestClient(main.app)


def test_rerank_orders_and_cuts(monkeypatch):
    c = client(monkeypatch)
    r = c.post("/rerank", json={"model": "x", "query": "payout status", "documents": ["fees", "payout status list", {"text": "a payout"}], "top_n": 2})
    assert r.status_code == 200
    body = r.json()
    assert [x["index"] for x in body["results"]] == [1, 2]
    assert body["results"][0]["relevance_score"] == 0.2
    assert "document" not in body["results"][0]


def test_return_documents_and_empty(monkeypatch):
    c = client(monkeypatch)
    r = c.post("/rerank", json={"query": "a", "documents": ["a"], "return_documents": True})
    assert r.json()["results"][0]["document"] == {"text": "a"}
    assert c.post("/rerank", json={"query": "a", "documents": []}).json()["results"] == []


def test_bad_documents(monkeypatch):
    c = client(monkeypatch)
    assert c.post("/rerank", json={"query": "a", "documents": [{"nope": 1}]}).status_code == 422
    assert c.post("/rerank", json={"query": "a", "documents": ["x"] * (main.MAX_DOCS + 1)}).status_code == 413


def test_health(monkeypatch):
    c = client(monkeypatch)
    assert c.get("/health").json() == {"status": "ok", "model": main.MODEL, "loaded": True}
