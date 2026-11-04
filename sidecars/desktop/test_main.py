import os

import pytest
from fastapi.testclient import TestClient

import main

KEY = "test-key"


@pytest.fixture
def c(monkeypatch, tmp_path):
    monkeypatch.setattr(main, "KEY", KEY)
    monkeypatch.setattr(main, "ROOT", str(tmp_path))
    monkeypatch.setattr(main, "SETTLE", 0)
    monkeypatch.setattr(main.Session, "start", lambda s: os.makedirs(s.home, exist_ok=True))
    monkeypatch.setattr(main.Session, "stop", lambda s: None)
    monkeypatch.setattr(main, "grab", lambda s: b"jpeg")
    calls = []

    def xdo(s, args, timeout=20):
        calls.append(args)
        return "Example Domain" if args[0] == "getactivewindow" else ""

    monkeypatch.setattr(main, "xdo", xdo)
    main.sessions.clear()
    client = TestClient(main.app, headers={"Authorization": f"Bearer {KEY}"})
    client.calls = calls
    yield client
    main.sessions.clear()


def call(c, chat="chat1", **body):
    return c.post("/computer", json=body, headers={"X-Kera-Chat-Id": chat})


def req(**kw):
    return main.Computer(**kw)


def test_auth(c):
    assert c.get("/health", headers={"Authorization": ""}).status_code == 200
    assert c.get("/openapi.json", headers={"Authorization": ""}).status_code == 200
    assert c.post("/computer", json={"action": "screenshot"}, headers={"Authorization": ""}).status_code == 401
    assert c.post("/computer", json={"action": "screenshot"}, headers={"Authorization": "Bearer nope"}).status_code == 401


def test_no_key_configured_rejects(c, monkeypatch):
    monkeypatch.setattr(main, "KEY", "")
    assert c.post("/computer", json={"action": "screenshot"}, headers={"Authorization": "Bearer "}).status_code == 401


def test_openapi_tools():
    spec = main.app.openapi()
    ops = {op["operationId"]: op for p in spec["paths"].values() for op in p.values()}
    assert set(ops) == {"computer", "collect_file"}  # debug routes are not tools
    c = ops["computer"]
    assert len(c["summary"]) + 1 + len(c["description"]) <= 1024


def test_safe_id():
    assert main.safe_id("0193-abc_D") == "0193-abc_D"
    h = main.safe_id("../etc")
    assert h.startswith("h") and len(h) == 33 and "/" not in h
    assert main.safe_id("a/b") != main.safe_id("a.b")
    with pytest.raises(main.HTTPException):
        main.safe_id("")


def test_xdo_args():
    assert main.xdo_args(req(action="click", x=10, y=20)) == ["mousemove", "--sync", "10", "20", "click", "1"]
    assert main.xdo_args(req(action="right_click", x=0, y=0))[-1] == "3"
    assert main.xdo_args(req(action="double_click", x=1, y=1))[4:] == ["click", "--repeat", "2", "--delay", "80", "1"]
    drag = main.xdo_args(req(action="drag", x=1, y=2, to_x=3, to_y=4))
    assert drag[:6] == ["mousemove", "--sync", "1", "2", "mousedown", "1"] and drag[-2:] == ["mouseup", "1"]
    assert main.xdo_args(req(action="type", text="-rf hi")) == ["type", "--delay", "12", "--", "-rf hi"]
    assert main.xdo_args(req(action="key", keys="ctrl+l Return")) == ["key", "--delay", "60", "ctrl+l", "Return"]
    assert main.xdo_args(req(action="scroll", dy=-3)) == ["click", "--repeat", "3", "--delay", "40", "4"]
    sc = main.xdo_args(req(action="scroll", x=5, y=6, dx=2))
    assert sc[:4] == ["mousemove", "--sync", "5", "6"] and sc[-1] == "7"
    assert main.xdo_args(req(action="open_url", url="https://x")) is None


@pytest.mark.parametrize("body", [
    {"action": "click", "x": 1280, "y": 10},
    {"action": "click", "x": -1, "y": 10},
    {"action": "click", "x": 5},
    {"action": "drag", "x": 1, "y": 1},
    {"action": "type", "text": "x" * 2001},
    {"action": "type", "text": ""},
    {"action": "key", "keys": "ctrl+l; rm"},
    {"action": "key", "keys": "$(id)"},
    {"action": "key", "keys": ""},
    {"action": "scroll"},
    {"action": "scroll", "dy": 99},
    {"action": "wait", "seconds": 60},
    {"action": "open_url", "url": "file:///etc/passwd"},
    {"action": "open_url", "url": "javascript:alert(1)"},
    {"action": "open_url", "url": "https://a.com/\nx"},
    {"action": "run_command"},
    {"action": "nope"},
])
def test_invalid_args(c, body):
    assert call(c, **body).status_code == 422
    assert not main.sessions  # nothing started for a bad call


def test_open_url_adds_scheme():
    assert main.check(req(action="open_url", url="example.com")) == "https://example.com"


def test_click_flow_and_upload(c, monkeypatch):
    sent = []
    monkeypatch.setattr(main, "upload", lambda h, name, data, mime: sent.append((h["x-kera-callback"], mime)) or "f1")
    r = c.post("/computer", json={"action": "click", "x": 5, "y": 6},
               headers={"X-Kera-Chat-Id": "chat1", "X-Kera-Callback": "http://k/api/v1/runs/r1"})
    body = r.json()
    assert body["images"] == ["f1"] and "active window: Example Domain" in body["content"]
    assert sent == [("http://k/api/v1/runs/r1", "image/jpeg")]
    assert c.calls[0] == ["mousemove", "--sync", "5", "6", "click", "1"]


def test_no_callback_and_done(c):
    body = call(c, action="screenshot").json()
    assert "screenshot not uploaded (no callback)" in body["content"] and "images" not in body
    assert "Done" in call(c, action="done").json()["content"]


def test_missing_chat_id(c):
    assert c.post("/computer", json={"action": "screenshot"}).status_code == 400


def test_busy(c):
    assert call(c, "a", action="screenshot").status_code == 200
    assert call(c, "b", action="screenshot").status_code == 200
    r = call(c, "c", action="screenshot")
    assert r.status_code == 429 and "desktop busy" in r.json()["detail"]
    assert {s.display for s in main.sessions.values()} == {10, 11}
    assert c.delete("/sessions/a").status_code == 200
    assert call(c, "c", action="screenshot").status_code == 200


def test_run_command(c):
    out = call(c, action="run_command", command="echo hi > out.txt; echo out; echo err >&2; exit 3").json()["content"]
    assert out.startswith("exit 3; output:\nout\nerr")
    assert open(os.path.join(main.sessions["chat1"].home, "out.txt")).read() == "hi\n"


def test_run_command_timeout(c, monkeypatch):
    monkeypatch.setattr(main, "CMD_TIMEOUT", 0.3)
    assert "killed after" in call(c, action="run_command", command="sleep 5").json()["content"]


def test_inside(tmp_path):
    home = tmp_path / "s"
    home.mkdir()
    (home / "a.txt").write_text("x")
    os.symlink("/etc/passwd", home / "link")
    assert main.inside(str(home), "a.txt") == str(home / "a.txt")
    assert main.inside(str(home), str(home / "a.txt")) == str(home / "a.txt")
    for bad in ("../x", "/etc/passwd", "link", ".", "sub/../../x"):
        with pytest.raises(main.HTTPException):
            main.inside(str(home), bad)


def test_collect_file(c, monkeypatch):
    assert c.post("/collect_file", json={"path": "a"}, headers={"X-Kera-Chat-Id": "chat1"}).status_code == 404
    call(c, action="screenshot")
    with open(os.path.join(main.sessions["chat1"].home, "r.csv"), "w") as f:
        f.write("a,b\n")
    hdr = {"X-Kera-Chat-Id": "chat1"}
    assert "cannot be uploaded (no callback)" in c.post("/collect_file", json={"path": "r.csv"}, headers=hdr).json()["content"]
    assert c.post("/collect_file", json={"path": "../r.csv"}, headers=hdr).status_code == 422
    assert c.post("/collect_file", json={"path": "missing"}, headers=hdr).status_code == 404
    got = []
    monkeypatch.setattr(main, "upload", lambda h, name, data, mime: got.append((name, data, mime)) or "f9")
    assert c.post("/collect_file", json={"path": "r.csv"}, headers=hdr).json()["files"] == ["f9"]
    assert got == [("r.csv", b"a,b\n", "text/csv")]


def test_upload_and_status_use_callback(monkeypatch):
    seen = []

    class R:
        def raise_for_status(self):
            pass

        def json(self):
            return {"id": "fid", "name": "n"}

    monkeypatch.setattr(main.httpx, "post", lambda url, **kw: seen.append((url, kw)) or R())
    h = {"x-kera-callback": "http://k/api/v1/runs/r", "x-kera-callback-token": "tok"}
    assert main.upload(h, "s.jpg", b"x", "image/jpeg") == "fid"
    main.status(h, "hello", True)
    assert seen[0][0] == "http://k/api/v1/runs/r/files" and seen[0][1]["params"] == {"name": "s.jpg"}
    assert seen[0][1]["headers"] == {"Content-Type": "image/jpeg", "Authorization": "Bearer tok"}
    assert seen[1][0] == "http://k/api/v1/runs/r/events"
    assert seen[1][1]["json"] == {"type": "status", "data": {"description": "hello", "done": True}}
    assert main.upload({}, "s.jpg", b"x", "image/jpeg") is None
