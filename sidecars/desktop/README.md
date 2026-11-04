# kera-desktop

Optional desktop sidecar for Kera: a Linux desktop (Xvfb, openbox, Firefox ESR, a shell) per chat,
served as an OpenAPI tool server. The model sees a screenshot after every action and drives the
desktop with pixel coordinates, keys and text, like a person at a remote screen.

```
POST /computer      (tool "computer")      {action, x, y, to_x, to_y, text, keys, dx, dy, seconds, url, command}
                                             -> {"content": "...; screen 1280x800; active window: ...", "images": [file id]}
POST /collect_file  (tool "collect_file")  {path} -> {"content": "attached out.csv (12 bytes)", "files": [file id]}
GET  /sessions/{chat}/screen.jpg            current screen (debugging); 503 if the X server died
DELETE /sessions/{chat}                     end a desktop now
GET  /sessions/{chat}/view                  {"active": true, "token": "..."} for the live view; 404 without a desktop
GET  /health                                {"status": "ok", "sessions": 1, "max_sessions": 2}
```

Actions: `screenshot`, `click`, `double_click`, `right_click`, `move`, `drag`, `type`, `key`, `scroll`,
`wait`, `open_url`, `run_command`, `done`. Every action but `done` waits briefly and returns a JPEG
screenshot (quality 70). The operating rules for the model (look first, verify each step, ask the user
before logins, payments, sending or deleting, never type secrets it was not given) are in the
`computer` operation description, which Kera passes to the model as the tool description.

- Sessions are keyed by `X-Kera-User-Id` + `X-Kera-Chat-Id` (both required, HTTP 400 without; temporary chat
  ids are chosen by the client, so the user is part of the key). The `/sessions/{chat}` routes need
  `X-Kera-User-Id` too. Each session has one Xvfb display (`:10`, `:11`, ...) with its own MIT cookie
  (`-nolisten tcp`), openbox, and a home `/home/desk/sessions/<chat>` (Firefox profile, `Downloads/`,
  the working directory of `run_command`; the directory name is a hash of user and chat). Firefox starts on the
  first `open_url`.
- Screenshots and files go to Kera through the run callback: `POST {X-Kera-Callback}/files?name=...`
  with `Authorization: Bearer {X-Kera-Callback-Token}`. Status lines go to `{X-Kera-Callback}/events`.
  Without a callback (manual curl) the content says `screenshot not uploaded (no callback)`.
- `run_command` runs `bash -c` in the session home, 60 s timeout, output (stdout and stderr) clipped to
  4000 characters. `collect_file` only reads below the session home (symlinks are resolved first).
- A busy sidecar (`DESKTOP_MAX_SESSIONS` desktops in use by other chats) answers HTTP 429
  `desktop busy: ...`, which Kera shows the model as a tool error.
- A desktop idle for `DESKTOP_IDLE_MINUTES` is ended: every process started for it is killed and its
  home is deleted. Downloads the user needs must be collected before that. An ending desktop (idle, `DELETE`,
  crash) keeps its display and home until its processes are gone: a new call of the same chat waits for that
  (up to 90 s, then HTTP 409).
- If the X server dies, the call answers HTTP 503 `desktop crashed; it will restart on the next call` and the
  desktop is ended.

## Live view and takeover

Every desktop also runs `x11vnc` on `127.0.0.1:5900+display` (localhost only, no VNC password, shared,
`-forever`). One `websockify` on port 6080 serves only the websocket (`/websockify?token=...`, no web files);
its `TokenFile` plugin maps a random per-session token (not the chat id; it changes with every new desktop)
to that session's VNC port. Without a valid token the websocket is closed. The token is read with the
service key and `X-Kera-User-Id` from `GET /sessions/{chat}/view`.

Kera serves it to the chat owner: with `desktop.url` (this API, e.g. `http://kera-desktop:8000`),
`desktop.view_url` (`http://kera-desktop:6080`) and `desktop.key` set, the Cowork task page shows the
desktop with the noVNC client bundled in Kera's web app. Kera proxies only the websocket
(`GET /api/v1/desktop/{chat}/websockify` -> `{view_url}/websockify?token=...`) and adds the token itself; the
browser never sees it. The user can click and type there (for example to sign in) and then tell the assistant
to continue; "End desktop" calls `DELETE /sessions/{chat}`. Kera sends `X-Kera-User-Id` (the chat owner) on
`GET /sessions/{chat}/view` and `DELETE /sessions/{chat}`.

```yaml
desktop:                        # kera apply
  url: http://kera-desktop:8000
  view_url: http://kera-desktop:6080
  key: ${KERA_DESKTOP_KEY}
```

Port 6080 must be reachable only by Kera, like port 8000: anyone who has a token controls that desktop. The edge
proxy in front of Kera must pass websocket upgrades on `/api/v1/desktop/` and keep idle connections open for
minutes.

## Environment

| Variable | Default | |
|---|---|---|
| `KERA_DESKTOP_KEY` | (required) | service key; every route but `/health` and `/openapi.json` needs `Authorization: Bearer <key>`. Without it every call is refused. |
| `DESKTOP_MAX_SESSIONS` | `2` | desktops at the same time; each takes about 400-500 MB RAM and ~250 pids with Firefox open |
| `DESKTOP_IDLE_MINUTES` | `15` | idle time before a desktop is ended |
| `DESKTOP_SCREEN` | `1280x800` | screen size (also written into the tool description) |

## Security

- The desktop browses the open internet and runs any shell command the model sends. Treat it like the
  code sandbox: nothing secret in the container, and the screen content is untrusted. A web page can
  try prompt injection ("ignore your instructions and ..."); the tool description tells the model that
  screen text is data, and asks it to confirm logins, payments, sending and deleting with the user, but
  there is no technical boundary against a model that follows the page.
- Run it on its own Docker network that only Kera joins (not the code sandbox network), with no
  published port (neither 8000 nor 6080). Kera's callback URL must be reachable from it.
- The sidecar serves no page that runs in Kera's origin: Kera bundles the noVNC client and proxies only the
  websocket. Commands in a desktop still run as the same uid as `websockify`, so they can read the token
  files of other desktops and could stop it and listen on 6080 themselves (VNC data, never a page). Per-session
  containers are the upgrade path.
- All desktops run as one uid in one container. A command in one chat's desktop can read another
  chat's home and screen, and can read the service key from `/proc/1/environ` (which only grants the
  same access again; use a key for this sidecar alone). The API process itself is not dumpable, so the
  per-run callback tokens are not readable. Use one container per user if chats need hard isolation.
- Ending a desktop kills every process in a process session or group it started (so `env -u
  KERA_DESKTOP_SESSION` does not escape) and every process carrying its per-desktop mark. A process that
  calls `setsid()` itself and drops the variable still survives; per-session containers are the real fix.
- Firefox keeps its seccomp-bpf content sandbox under `cap_drop: ALL` and `no-new-privileges` (tested:
  content processes run with `Seccomp: 2`; no `MOZ_DISABLE_CONTENT_SANDBOX` needed). The user-namespace
  layer of that sandbox is not available inside Docker. Variables starting with `MOZ_` reach Firefox.
- Fonts cover Latin, Cyrillic, Greek, Arabic, Hebrew, Indic and more (Noto core); CJK text shows as
  boxes unless `fonts-noto-cjk` is added (~100 MB).

## Compose

```yaml
services:
  kera-desktop:
    build: ./sidecars/desktop
    user: "1000"
    environment:
      KERA_DESKTOP_KEY: ${KERA_DESKTOP_KEY}
      DESKTOP_MAX_SESSIONS: "2"
      DESKTOP_IDLE_MINUTES: "15"
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    shm_size: 1g
    mem_limit: 6g
    cpus: 2
    pids_limit: 1024
    tmpfs: [/tmp]
    networks: [kera_desktop]   # no ports: Kera reaches 8000 (API) and 6080 (live view) on this network
    restart: unless-stopped

  kera:
    networks: [default, kera_desktop]   # plus whatever it already has

networks:
  kera_desktop: {}
```

Kera config (`kera apply`), with the same key in Kera's environment:

```yaml
tool_servers:
  - id: desktop
    kind: openapi
    name: Desktop
    url: http://kera-desktop:8000
    auth: {type: bearer, key: "${KERA_DESKTOP_KEY}"}
    grants: [{principal: "group:<pilot group>"}]
```

Give it only to vision-capable models: the screenshots are the model's eyes.

## Tests

```
docker build -t kera-desktop sidecars/desktop
docker run --rm -v "$PWD/sidecars/desktop":/src:ro -w /src -u 0 kera-desktop \
  sh -c "pip install -q -r requirements-dev.txt && python -m pytest -q -p no:cacheprovider"
```

The tests mock xdotool, the X server and the callback. Manual check against a running container:

```
curl -s -H "Authorization: Bearer $KEY" -H "X-Kera-User-Id: me" -H "X-Kera-Chat-Id: smoke" \
  -H "Content-Type: application/json" -d '{"action":"open_url","url":"https://example.com"}' localhost:8000/computer
curl -s -H "Authorization: Bearer $KEY" -H "X-Kera-User-Id: me" localhost:8000/sessions/smoke/screen.jpg -o screen.jpg
```
