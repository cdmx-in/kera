# kera-desktop

Optional desktop sidecar for Kera: a Linux desktop (Xvfb, openbox, Firefox ESR, a shell) per chat,
served as an OpenAPI tool server. The model sees a screenshot after every action and drives the
desktop with pixel coordinates, keys and text, like a person at a remote screen.

```
POST /computer      (tool "computer")      {action, x, y, to_x, to_y, text, keys, dx, dy, seconds, url, command}
                                             -> {"content": "...; screen 1280x800; active window: ...", "images": [file id]}
POST /collect_file  (tool "collect_file")  {path} -> {"content": "attached out.csv (12 bytes)", "files": [file id]}
GET  /sessions/{chat}/screen.jpg            current screen (debugging)
DELETE /sessions/{chat}                     end a desktop now
GET  /health                                {"status": "ok", "sessions": 1, "max_sessions": 2}
```

Actions: `screenshot`, `click`, `double_click`, `right_click`, `move`, `drag`, `type`, `key`, `scroll`,
`wait`, `open_url`, `run_command`, `done`. Every action but `done` waits briefly and returns a JPEG
screenshot (quality 70). The operating rules for the model (look first, verify each step, ask the user
before logins, payments, sending or deleting, never type secrets it was not given) are in the
`computer` operation description, which Kera passes to the model as the tool description.

- Sessions are keyed by `X-Kera-Chat-Id`: one Xvfb display (`:10`, `:11`, ...) with its own MIT cookie
  (`-nolisten tcp`), openbox, and a home `/home/desk/sessions/<chat>` (Firefox profile, `Downloads/`,
  the working directory of `run_command`). Firefox starts on the first `open_url`.
- Screenshots and files go to Kera through the run callback: `POST {X-Kera-Callback}/files?name=...`
  with `Authorization: Bearer {X-Kera-Callback-Token}`. Status lines go to `{X-Kera-Callback}/events`.
  Without a callback (manual curl) the content says `screenshot not uploaded (no callback)`.
- `run_command` runs `bash -c` in the session home, 60 s timeout, output (stdout and stderr) clipped to
  4000 characters. `collect_file` only reads below the session home (symlinks are resolved first).
- A busy sidecar (`DESKTOP_MAX_SESSIONS` desktops in use by other chats) answers HTTP 429
  `desktop busy: ...`, which Kera shows the model as a tool error.
- A desktop idle for `DESKTOP_IDLE_MINUTES` is ended: every process started for it is killed and its
  home is deleted. Downloads the user needs must be collected before that.

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
  published port. Kera's callback URL must be reachable from it.
- All desktops run as one uid in one container. A command in one chat's desktop can read another
  chat's home and screen, and can read the service key from `/proc/1/environ` (which only grants the
  same access again; use a key for this sidecar alone). The API process itself is not dumpable, so the
  per-run callback tokens are not readable. Use one container per user if chats need hard isolation.
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
    networks: [kera_desktop]
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
curl -s -H "Authorization: Bearer $KEY" -H "X-Kera-Chat-Id: smoke" -H "Content-Type: application/json" \
  -d '{"action":"open_url","url":"https://example.com"}' localhost:8000/computer
curl -s -H "Authorization: Bearer $KEY" localhost:8000/sessions/smoke/screen.jpg -o screen.jpg
```
