#!/usr/bin/env python3
"""Records docs/images/durable-restart.gif: a Cowork task on a SCRATCH Kera (never a real one) while the server is
stopped with SIGTERM in the middle of a write and started again.

  python3 scripts/screenshots/restart_clip.py

Needs: a Kera binary with the web app built in (make build; KERA_BIN names it), the test Postgres (make testdb),
Python with Pillow, the agent-browser CLI and the DejaVu fonts (DEJAVU_DIR). The work folder (frames, logs) is made
under CLIP_WORK, or the system temp folder. Kera and the fake model of
restart_fake.py listen on 127.0.0.1 only. The scratch database is created here and dropped at the end. Kera runs as a
child of this script, so the SIGTERM can reach no other process. Takes about a minute.

Each frame is the browser as it was, over a panel that lists what had happened by then: what this script did, lines
of the scratch Kera's log, and the requests the fake tool server received. The clip runs in real time. The data is
made up and the model is a script.
"""
import json
import os
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import urllib.request
from datetime import datetime
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
BIN = os.environ.get("KERA_BIN", str(ROOT / "kera"))
OUT = os.environ.get("CLIP_OUT", str(ROOT / "docs/images/durable-restart.gif"))
FONTS = Path(os.environ.get("DEJAVU_DIR", "/usr/share/fonts"))
PG, DB = os.environ.get("DEMO_PG_CONTAINER", "kera-test-db"), "kera_restart_clip"
PORT, FAKE_PORT = 18473, 18474
URL = f"http://127.0.0.1:{PORT}"
EMAIL = "alex@example.com"
GOAL = "Prepare the Acme 2.4 release: check the changes, open a ticket, check the build and announce it."
W, TOP, PANEL = 860, 470, 214

CONFIG = f"""
site: {{default_models: [release-assistant]}}
tasks: {{title: false, tags: false, follow_ups: false, query: false}}
connections:
  - {{id: demo, kind: openai, url: "http://127.0.0.1:{FAKE_PORT}/v1", api_key: not-a-real-key}}
tool_servers:
  - {{id: releases, kind: openapi, name: Release tools, url: "http://127.0.0.1:{FAKE_PORT}", spec: /openapi.json}}
models:
  - {{id: release-assistant, name: Release assistant, base: demo-model, description: Prepares Acme releases, tools: {{servers: [releases]}}}}
"""


def ab(*args, check=True):
    r = subprocess.run(["agent-browser", "--session", "kera-restart-clip", *args], capture_output=True, text=True, timeout=60)
    if check and r.returncode:
        raise SystemExit(f"agent-browser {args[0]}: {r.stderr or r.stdout}")


def psql(sql):
    subprocess.run(["docker", "exec", PG, "psql", "-U", "kera", "-d", "postgres", "-qc", sql], check=True)


def up(port=PORT, path="/"):
    try:
        urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=0.5).read()
        return True
    except OSError:
        return False


def wait_for(pred, seconds, what):
    deadline = time.time() + seconds
    while not pred():
        if time.time() > deadline:
            raise SystemExit("timed out waiting for " + what)
        time.sleep(0.1)


def lines(path):
    out = []
    for line in path.read_text().splitlines():
        try:
            out.append(json.loads(line))
        except ValueError:
            pass
    return out


def record(work, env):
    """Runs the task and the restart; returns the captured frames [(time, file)]."""
    events, log = work / "events.jsonl", work / "kera.log"
    frames, stop = [], threading.Event()

    def note(kind, text):
        with open(work / "shell.jsonl", "a") as f:
            f.write(json.dumps({"t": time.time(), "src": "shell", "kind": kind, "text": text}) + "\n")

    def serve():
        with open(log, "a") as f:
            p = subprocess.Popen([BIN, "serve"], env=env, cwd=work, stdout=f, stderr=f)
        servers.append(p)
        wait_for(lambda: p.poll() is not None or up(), 30, "the scratch Kera")
        if p.poll() is not None:
            raise SystemExit(f"the scratch Kera did not start, see {log}")
        return p

    def capture():
        n = 0
        while not stop.is_set():
            t, path = time.time(), work / f"frame-{n:04d}.png"
            ab("screenshot", str(path), check=False)
            if path.exists():
                frames.append(((t + time.time()) / 2, path))
                n += 1
            time.sleep(max(0, 0.4 - (time.time() - t)))

    servers, camera = [], None
    try:
        kera = serve()
        ab("set", "viewport", str(W), str(TOP), "1")
        ab("set", "media", "light")
        ab("open", URL + "/auth")
        time.sleep(2)
        ab("fill", "input[type=email]", EMAIL)
        ab("fill", "input[type=password]", env["DEMO_PASSWORD"])
        ab("press", "Enter")
        time.sleep(2)
        ab("find", "role", "button", "click", "--name", "Close sidebar", check=False)  # already closed on a narrow page
        time.sleep(1)
        ab("open", URL + "/cowork/new?q=" + urllib.parse.quote(GOAL))
        time.sleep(2)
        ab("find", "role", "textbox", "fill", "Acme 2.4 release", "--name", "Name")
        for _ in range(4):  # goal -> assistant -> apps -> schedule -> review
            ab("find", "role", "button", "click", "--name", "Next", "--exact")
            time.sleep(0.8)

        camera = threading.Thread(target=capture)
        camera.start()
        time.sleep(1.2)
        note("task", "Start task")
        ab("find", "role", "button", "click", "--name", "Start task")

        wait_for(lambda: any("WRITE: ticket" in e["text"] for e in lines(events)), 30, "the ticket write")
        time.sleep(1.8)
        note("term", f"kill -TERM {kera.pid}   (stop the server)")
        kera.send_signal(signal.SIGTERM)
        code = kera.wait(timeout=40)
        note("exited", f"Kera process {kera.pid} has exited (code {code})")
        time.sleep(5)
        note("start", "kera serve   (start it again)")
        serve()
        wait_for(lambda: any(e["kind"] == "done" for e in lines(events)), 90, "the final report")
        time.sleep(6)
    finally:
        stop.set()
        if camera:
            camera.join(timeout=70)
        ab("close", check=False)
        for p in servers:
            if p.poll() is None:
                p.send_signal(signal.SIGTERM)
                p.wait(timeout=40)
    return frames


def compose(work, frames):
    """Writes the GIF and checks that the tool server received each write one time."""
    ev = lines(work / "shell.jsonl") + lines(work / "events.jsonl")
    for o in lines(work / "kera.log"):
        text = {"kera listening": "listening", "run released for the next start": "run released for the next start",
                "taking over a run": f"taking over a run (attempt {o.get('attempt')})"}.get(o.get("msg"))
        if text:
            when = datetime.fromisoformat(o["time"][:26] + "+00:00").timestamp()
            ev.append({"t": when, "src": "kera", "kind": o["msg"], "text": text})
    ev.sort(key=lambda e: e["t"])
    writes = [e["text"] for e in ev if e["kind"] == "write"]
    took_over = any(e["kind"] == "taking over a run" for e in ev)
    if len(writes) != 2 or len(set(writes)) != 2 or not took_over:
        raise SystemExit(f"not the story the clip tells (writes: {writes}, run taken over: {took_over}); see {work}")

    def first(kind, src):
        return next(e["t"] for e in ev if e["kind"] == kind and e["src"] == src)

    t0 = first("task", "shell")
    ticket = next(e["t"] for e in ev if "WRITE: ticket" in e["text"])
    back = [e["t"] for e in ev if e["kind"] == "kera listening"][-1]
    done = first("done", "model") + 0.8  # the page has drawn the end of the report
    green, amber, red = "#238636", "#9e6a03", "#da3633"
    phases = [  # from this moment: caption, status, colour
        (0, "A made-up release task, ready to start", "KERA RUNNING", green),
        (t0, "1  A task is running", "KERA RUNNING", green),
        (ticket, "1  It has just made a write: the ticket exists, the call is still open", "KERA RUNNING", green),
        (first("term", "shell"), "2  Stop signal (SIGTERM): Kera lets the open call finish and records it", "KERA STOPPING", amber),
        (first("exited", "shell"), "3  The server process is gone", "KERA STOPPED", red),
        (back, "4  Started again: the same task goes on where it was", "KERA RUNNING", green),
        (done, "5  Finished. The ticket was not created a second time", "KERA RUNNING", green),
    ]

    def font(path, size):
        return ImageFont.truetype(str(FONTS / path), size)

    bold = "dejavu-sans-fonts/DejaVuSans-Bold.ttf"
    sans = "dejavu-sans-fonts/DejaVuSans.ttf"
    mono = font("dejavu-sans-mono-fonts/DejaVuSansMono.ttf", 12)
    colours = {"shell": "#d2a8ff", "tools": "#7ee787", "kera": "#79c0ff"}

    def panel(t):
        caption, status, colour = [p[1:] for p in phases if p[0] <= t][-1]
        im = Image.new("RGB", (W, PANEL), "#0d1117")
        d = ImageDraw.Draw(im)
        d.rectangle([0, 0, W, 38], fill=colour)
        d.text((14, 10), caption, font=font(bold, 15), fill="#ffffff")
        d.text((W - 14 - d.textlength(status, font=font(bold, 12)), 13), status, font=font(bold, 12), fill="#ffffff")
        seen = [e for e in ev if e["t"] <= t]
        count = {k: sum("WRITE: " + k in e["text"] for e in seen) for k in ("ticket", "announcement")}
        d.text((14, 48), "Writes received by the tool server:", font=font(sans, 13), fill="#8b949e")
        d.text((290, 46), f"ticket ×{count['ticket']}     announcement ×{count['announcement']}", font=font(bold, 15), fill="#e6edf3")
        d.line([14, 72, W - 14, 72], fill="#30363d")
        # the model's "final report sent" only times the last caption
        for i, e in enumerate([e for e in seen if e["t"] >= t0 and e["src"] in colours][-6:]):
            y = 80 + i * 18
            d.text((14, y), f"{e['t'] - t0:5.1f}s", font=mono, fill="#6e7681")
            d.text((72, y), e["src"], font=mono, fill=colours[e["src"]])
            d.text((122, y), e["text"], font=mono, fill="#ffffff" if "WRITE" in e["text"] or e["src"] == "shell" else "#c9d1d9")
        d.text((14, PANEL - 19), "Real recording, real time. Made-up data; the model is a script. Panel: the recorder's actions, Kera's log, the tool server.",
               font=font(sans, 10.5), fill="#8b949e")
        return im

    frames = [f for f in frames if t0 - 1.3 <= f[0] <= done + 4]  # a second before the click; the end is held below
    shots = []
    for t, path in frames:
        im = Image.new("RGB", (W, TOP + PANEL), "#ffffff")
        im.paste(Image.open(path).convert("RGB"), (0, 0))
        im.paste(panel(t), (0, TOP))
        shots.append(im)

    # one palette for all frames (no flicker, small differences), taken from moments that show every colour
    sample = [shots[int(len(shots) * k)] for k in (0, 0.2, 0.3, 0.4, 0.62, 0.99)]
    sheet = Image.new("RGB", (W, (TOP + PANEL) * len(sample)))
    for i, s in enumerate(sample):
        sheet.paste(s, (0, i * (TOP + PANEL)))
    palette = sheet.quantize(colors=96, method=Image.Quantize.MEDIANCUT)
    out, durations = [], []
    for i, im in enumerate(shots):
        p = im.quantize(palette=palette, dither=Image.Dither.NONE)
        following = frames[i + 1][0] if i + 1 < len(frames) else frames[i][0] + 4.5
        ms = max(20, round((following - frames[i][0]) * 100) * 10)
        if out and p.tobytes() == out[-1].tobytes():
            durations[-1] += ms
        else:
            out.append(p)
            durations.append(ms)
    out[0].save(OUT, save_all=True, append_images=out[1:], duration=durations, loop=0, optimize=False, disposal=1)

    print(f"{OUT}: {os.path.getsize(OUT) // 1024} KB, {len(out)} frames, {sum(durations) / 1000:.1f} s")
    for e in ev:
        if e["t"] >= t0:
            print(f"  {e['t'] - t0:6.2f}s  {e['src']:6} {e['text']}")


def main():
    for port in (PORT, FAKE_PORT):
        if up(port):
            raise SystemExit(f"something already listens on port {port}")
    work = Path(tempfile.mkdtemp(prefix="kera-restart-clip-", dir=os.environ.get("CLIP_WORK")))
    password = secrets.token_urlsafe(18)
    # a clean environment: nothing of the caller's (a real Kera's settings, say) reaches the scratch server
    env = {"PATH": os.environ["PATH"], "HOME": str(work), "TZ": "UTC", "KERA_URL": URL, "KERA_LISTEN": f"127.0.0.1:{PORT}",
           "KERA_DATA_DIR": str(work / "data"), "KERA_SECRET_KEY": secrets.token_hex(32),
           "KERA_DATABASE_URL": f"postgres://kera:kera-test@127.0.0.1:55432/{DB}?sslmode=disable"}
    (work / "config.yaml").write_text(CONFIG)
    (work / "data").mkdir()
    (work / "events.jsonl").touch()
    psql(f"CREATE DATABASE {DB}")  # fails if it exists, and then nothing below drops it
    fake = None
    try:
        for args, stdin in ((["db", "migrate"], ""), (["user", "add", "--admin", "--password-stdin", EMAIL], password + "\n"),
                            (["apply", "-f", "config.yaml"], "")):
            r = subprocess.run([BIN, *args], env=env, cwd=work, input=stdin, capture_output=True, text=True)
            if r.returncode:
                raise SystemExit(f"kera {args[0]}: {r.stderr or r.stdout}")
        fake = subprocess.Popen([sys.executable, str(HERE / "restart_fake.py"), str(FAKE_PORT), str(work / "events.jsonl")])
        wait_for(lambda: up(FAKE_PORT, "/v1/models"), 10, "the fake model")
        frames = record(work, {**env, "DEMO_PASSWORD": password})
        compose(work, frames)
    finally:
        if fake:
            fake.terminate()
            fake.wait(timeout=10)
        psql(f"DROP DATABASE IF EXISTS {DB} WITH (FORCE)")
        shutil.rmtree(work / "data", ignore_errors=True)
        print("work folder (frames and logs):", work)


if __name__ == "__main__":
    main()
