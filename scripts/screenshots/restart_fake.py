"""For restart_clip.py: a scripted OpenAI-compatible model and a made-up "release tools" OpenAPI server in one process.

The model is a script (it picks the next step from how many tool results it has); everything else in the clip is Kera.
Every write the tool server receives is appended to the events file when it takes effect: that file is the ground truth.

  python3 restart_fake.py PORT EVENTS_FILE
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT, EVENTS = int(sys.argv[1]), sys.argv[2]


def event(kind, text, src="tools"):
    with open(EVENTS, "a") as f:
        f.write(json.dumps({"t": time.time(), "src": src, "kind": kind, "text": text}) + "\n")


# (text before the call, tool name, arguments); after the last one comes the report
STEPS = [
    ("I will start with what changed in 2.4.", "list_merged_changes", {}),
    ("Next I will open the release ticket.", "create_release_ticket", {"title": "Release 2.4 checklist"}),
    ("Now the build status.", "get_build_status", {}),
    ("The build is green, so I will post the announcement.", "post_announcement", {"channel": "#releases", "text": "Acme 2.4 is ready to ship."}),
]
REPORT = """**Acme 2.4 is ready.**

- 3 changes merged: CSV export, faster search, a fix for time zones
- Release ticket **REL-118** opened
- Build **#2041** passed
- Announcement posted to **#releases**"""


def op(name, summary, body=None):
    o = {"operationId": name, "summary": summary, "responses": {"200": {"description": "ok"}}}
    if body:
        schema = {"type": "object", "properties": {k: {"type": "string"} for k in body}, "required": body}
        o["requestBody"] = {"required": True, "content": {"application/json": {"schema": schema}}}
    return o


# Kera calls a GET again after a restart (it only reads); it never repeats a POST that may have run.
SPEC = {"openapi": "3.0.0", "info": {"title": "Release tools", "version": "1"}, "paths": {
    "/changes": {"get": op("list_merged_changes", "List the changes merged for the release")},
    "/tickets": {"post": op("create_release_ticket", "Open a release ticket", ["title"])},
    "/build": {"get": op("get_build_status", "Status of the release build")},
    "/announcements": {"post": op("post_announcement", "Post an announcement", ["channel", "text"])},
}}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send(self, obj, code=200):
        raw = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path == "/v1/models":
            return self.send({"object": "list", "data": [{"id": "demo-model", "object": "model"}]})
        if self.path == "/openapi.json":
            return self.send(SPEC)
        if self.path == "/changes":
            time.sleep(2)
            event("read", "GET  /changes        read")
            return self.send({"merged": ["CSV export", "Faster search", "Fix: time zones in reports"]})
        if self.path == "/build":
            time.sleep(3)
            event("read", "GET  /build          read")
            return self.send({"build": 2041, "status": "passed"})
        self.send({"error": "not found"}, 404)

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers.get("content-length") or 0)) or b"{}")
        if self.path == "/tickets":
            # the write takes effect now; the answer comes later, so the restart lands while the call is open
            event("write", "POST /tickets        WRITE: ticket REL-118 created")
            time.sleep(7)
            event("answered", "POST /tickets        answered")
            return self.send({"ticket": "REL-118", "created": True})
        if self.path == "/announcements":
            time.sleep(2)
            event("write", "POST /announcements  WRITE: announcement posted")
            return self.send({"posted": True, "channel": data.get("channel")})
        if self.path == "/v1/chat/completions":
            return self.chat(data)
        self.send({"error": "not found"}, 404)

    def chat(self, req):
        names = [t["function"]["name"] for t in req.get("tools") or []]
        call, text = None, "Release 2.4"  # background tasks offer no tools
        if any(n.endswith("create_release_ticket") for n in names):
            done = sum(m.get("role") == "tool" for m in req.get("messages", []))
            if done < len(STEPS):
                text, want, args = STEPS[done]
                call = (next(n for n in names if n.endswith(want)), args, f"call_{done + 1}")
            else:
                text = REPORT
        usage = {"prompt_tokens": 900, "completion_tokens": 60, "total_tokens": 960}
        if not req.get("stream"):
            choice = {"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": text}}
            return self.send({"id": "x", "object": "chat.completion", "model": "demo-model", "choices": [choice], "usage": usage})
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.end_headers()

        def chunk(delta, finish=None, **extra):
            o = {"id": "x", "object": "chat.completion.chunk", "model": "demo-model",
                 "choices": [{"index": 0, "delta": delta, "finish_reason": finish}], **extra}
            self.wfile.write(f"data: {json.dumps(o)}\n\n".encode())
            self.wfile.flush()

        try:
            time.sleep(0.6)
            words = text.split(" ")
            for i, word in enumerate(words):
                chunk({"content": word + (" " if i < len(words) - 1 else "")})
                time.sleep(0.07)
            if call:
                chunk({"tool_calls": [{"index": 0, "id": call[2], "type": "function",
                                       "function": {"name": call[0], "arguments": json.dumps(call[1])}}]})
            chunk({}, "tool_calls" if call else "stop", usage=usage)
            self.wfile.write(b"data: [DONE]\n\n")
            if text == REPORT:
                event("done", "final report sent", src="model")
        except (BrokenPipeError, ConnectionResetError):
            pass  # Kera dropped the reply (a restart): it asks again


ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
