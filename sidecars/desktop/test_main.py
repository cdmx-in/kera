import os
import threading
import time

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
    stopped = []
    monkeypatch.setattr(main.Session, "stop", lambda s, others=(): stopped.append(s))
    monkeypatch.setattr(main, "grab", lambda s: b"jpeg")
    calls = []

    def xdo(s, args, timeout=20):
        calls.append(args)
        return "Example Domain" if args[0] == "getactivewindow" else ""

    monkeypatch.setattr(main, "xdo", xdo)
    main.sessions.clear()
    client = TestClient(main.app, headers={"Authorization": f"Bearer {KEY}", "X-Kera-User-Id": "u1"})
    client.calls, client.stopped = calls, stopped
    yield client
    main.sessions.clear()


def call(c, chat="chat1", **body):
    return c.post("/computer", json=body, headers={"X-Kera-Chat-Id": chat})


def sess(chat="chat1", user="u1"):
    return main.sessions[main.session_id({"x-kera-user-id": user, "x-kera-chat-id": chat})]


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


def test_session_id():
    sid = lambda user, chat: main.session_id({"x-kera-user-id": user, "x-kera-chat-id": chat})
    h = sid("u1", "../etc")
    assert h.startswith("s") and len(h) == 33 and h[1:].isalnum()
    assert sid("u1", "temp:x") != sid("u2", "temp:x")
    assert main.session_id({"x-kera-user-id": "u1"}, "temp:x") == sid("u1", "temp:x")
    for user, chat in (("", "c"), ("u", ""), (None, "c")):
        with pytest.raises(main.HTTPException):
            main.session_id({"x-kera-user-id": user, "x-kera-chat-id": chat} if user is not None else {}, None)


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


def test_missing_ids(c):
    assert c.post("/computer", json={"action": "screenshot"}).status_code == 400
    assert call(c, action="screenshot").status_code == 200
    no_user = {"X-Kera-User-Id": ""}
    assert c.post("/computer", json={"action": "screenshot"}, headers={**no_user, "X-Kera-Chat-Id": "chat1"}).status_code == 400
    for r in (c.get("/sessions/chat1/view", headers=no_user), c.get("/sessions/chat1/screen.jpg", headers=no_user),
              c.delete("/sessions/chat1", headers=no_user)):
        assert r.status_code == 400


def test_sessions_per_user(c):
    call(c, "temp:1", action="screenshot")
    other = {"X-Kera-User-Id": "u2"}
    assert c.get("/sessions/temp:1/view", headers=other).status_code == 404  # same chat id, other user
    assert c.delete("/sessions/temp:1", headers=other).status_code == 404
    assert c.post("/computer", json={"action": "screenshot"}, headers={**other, "X-Kera-Chat-Id": "temp:1"}).status_code == 200
    assert len(main.sessions) == 2 and sess("temp:1").home != sess("temp:1", "u2").home


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
    assert open(os.path.join(sess().home, "out.txt")).read() == "hi\n"


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
    with open(os.path.join(sess().home, "r.csv"), "w") as f:
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


def test_view_token(c):
    assert c.get("/sessions/chat1/view").status_code == 404  # never starts a desktop
    assert not main.sessions
    call(c, action="screenshot")
    body = c.get("/sessions/chat1/view").json()
    assert body["active"] is True and len(body["token"]) >= 32 and "chat1" not in body["token"]
    call(c, "chat2", action="screenshot")
    assert c.get("/sessions/chat2/view").json()["token"] != body["token"]
    assert c.get("/sessions/chat1/view", headers={"Authorization": ""}).status_code == 401


def test_session_token_file(monkeypatch, tmp_path):
    monkeypatch.setattr(main, "TOKENS", str(tmp_path / "tokens"))
    monkeypatch.setattr(main, "ROOT", str(tmp_path / "home"))
    spawned = []
    monkeypatch.setattr(main.Session, "spawn", lambda s, name, argv: spawned.append(argv))
    monkeypatch.setattr(main.subprocess, "run", lambda *a, **kw: None)
    exists = os.path.exists
    monkeypatch.setattr(main.os.path, "exists", lambda p: p.startswith("/tmp/.X11-unix/") or exists(p))  # Xvfb up
    s = main.Session("chat1", 12)
    s.start()
    vnc = next(a for a in spawned if "x11vnc" in a)
    assert "-localhost" in vnc and vnc[vnc.index("-rfbport") + 1] == "5912"
    path = tmp_path / "tokens" / "chat1"
    assert path.read_text() == f"{s.token}: 127.0.0.1:5912\n"
    monkeypatch.setattr(main.os, "listdir", lambda p: [])  # no /proc scan
    s.stop()
    assert not path.exists()


def test_end_keeps_session_reserved_until_stopped(c, monkeypatch):
    """DELETE during a running action: a new call of the chat waits for the old stop(); its display stays taken."""
    monkeypatch.setattr(main, "MAX_SESSIONS", 3)
    call(c, action="screenshot")
    old = sess()
    old.lock.acquire()  # a long action is running
    t = threading.Thread(target=lambda: c.delete("/sessions/chat1"))
    t.start()
    while not old.dead:
        time.sleep(0.01)
    assert call(c, "chat2", action="screenshot").status_code == 200
    assert sess("chat2").display != old.display  # not reused while the old Xvfb lives
    assert c.get("/sessions/chat1/view").status_code == 404  # dying: no live view, no collect_file
    assert c.delete("/sessions/chat1").status_code == 404  # already ending
    got = []
    t2 = threading.Thread(target=lambda: got.append(call(c, action="screenshot")))
    t2.start()
    time.sleep(0.2)
    assert not got and c.stopped == []  # waits for the old desktop
    old.lock.release()
    t.join()
    t2.join()
    assert got[0].status_code == 200 and c.stopped == [old]
    new = sess()
    assert new is not old and not new.dead and new.mark != old.mark


def test_dying_session_wait_times_out(c, monkeypatch):
    monkeypatch.setattr(main, "CLOSE_WAIT", 0.1)
    call(c, action="screenshot")
    sess().dead = True
    r = call(c, action="screenshot")
    assert r.status_code == 409 and "still closing" in r.json()["detail"]


def test_reap(c, monkeypatch):
    call(c, "a", action="screenshot")
    call(c, "b", action="screenshot")
    busy = sess("b")
    busy.lock.acquire()
    monkeypatch.setattr(main, "IDLE_MINUTES", -1)  # everything is idle
    main.reap()
    assert list(main.sessions.values()) == [busy] and len(c.stopped) == 1  # a running action is never reaped
    busy.lock.release()
    main.reap()
    assert not main.sessions and not busy.lock.locked()


def test_grab_failure_ends_desktop(c, monkeypatch):
    seen = []
    monkeypatch.setattr(main, "status", lambda h, d, done: seen.append(done))
    call(c, action="screenshot")
    s = sess()

    def broken(s):
        raise OSError("X connection failed")

    monkeypatch.setattr(main, "grab", broken)
    r = call(c, action="run_command", command="true")
    assert r.status_code == 503 and r.json()["detail"] == main.CRASHED
    assert seen == [False, True] and c.stopped == [s] and not main.sessions
    monkeypatch.setattr(main, "grab", lambda s: b"jpeg")
    call(c, action="screenshot")
    monkeypatch.setattr(main, "grab", broken)
    assert c.get("/sessions/chat1/screen.jpg").status_code == 503 and not main.sessions


def test_status_done_on_error(c, monkeypatch):
    seen = []
    monkeypatch.setattr(main, "status", lambda h, d, done: seen.append(done))
    monkeypatch.setattr(main, "MAX_SESSIONS", 0)
    assert call(c, action="run_command", command="true").status_code == 429
    assert seen == [False, True]


def test_collect_file_special(c, monkeypatch):
    call(c, action="screenshot")
    home, hdr = sess().home, {"X-Kera-Chat-Id": "chat1"}
    os.mkfifo(os.path.join(home, "fifo"))
    os.mkdir(os.path.join(home, "sub"))
    with open(os.path.join(home, "big"), "wb") as f:
        f.write(b"x" * 11)
    assert c.post("/collect_file", json={"path": "fifo"}, headers=hdr).status_code == 404  # does not block
    assert c.post("/collect_file", json={"path": "sub"}, headers=hdr).status_code == 404
    monkeypatch.setattr(main, "MAX_FILE", 10)
    assert c.post("/collect_file", json={"path": "big"}, headers=hdr).status_code == 422


def test_stop_kills_escaped_processes_only_of_its_instance(monkeypatch, tmp_path):
    monkeypatch.setattr(main, "ROOT", str(tmp_path))
    monkeypatch.setattr(main.subprocess, "run", lambda *a, **kw: None)  # no xauth

    def alive(pid):
        try:
            with open(f"/proc/{pid}/stat") as f:
                return f.read().rsplit(")", 1)[1].split()[0] not in "ZX"
        except FileNotFoundError:
            return False

    def gone(pid):  # SIGKILL lands asynchronously
        for _ in range(50):
            if not alive(pid):
                return True
            time.sleep(0.02)
        return False

    old, new = main.Session("same", 10), main.Session("same", 10)  # a newer desktop of the same chat
    os.makedirs(old.home)
    out = main.run_command(old, "env -u KERA_DESKTOP_SESSION sleep 100 >/dev/null 2>&1 & echo $!")
    escaped = int(out.split()[-1])
    new.spawn("x", ["sleep", "100"])
    other = new.procs["x"].pid
    old.leaders.add(other)  # a dead leader's pid reused by another desktop's leader
    assert alive(escaped) and alive(other)
    old.stop(others={other})
    assert gone(escaped) and alive(other)
    new.stop()
    assert gone(other)
