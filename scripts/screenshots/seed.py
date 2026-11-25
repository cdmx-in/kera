#!/usr/bin/env python3
"""Fills a SCRATCH Kera with the demo workspace of demo.py.

  seed.py setup      runners, stored secrets, pipelines with scripts, stage titles, environment and timetables
  seed.py history [name...]   backdated runs (SQL on the scratch database), so the history and health strip look lived in
  seed.py chats      a few sidebar chat titles
  seed.py run NAME [--nowait] / wait NAME RUN   run a pipeline now, wait for a run
  seed.py shift      moves the newest real run of each pipeline to its last timetable slot

Environment: KERA_URL, DEMO_EMAIL, DEMO_PASSWORD, DEMO_DIR (runner tokens are written there), DEMO_PSQL (a command
that runs psql against the scratch database, default: docker exec -i kera-test-db psql -U kera -d kera_shots).
Never point it at a real Kera: `history` and `shift` write to the database directly."""

import http.cookiejar
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import demo  # noqa: E402

URL = os.environ.get("KERA_URL", "http://127.0.0.1:18097").rstrip("/")
OUT = os.environ.get("DEMO_DIR", "/tmp/kera-demo")
PSQL = os.environ.get("DEMO_PSQL", "docker exec -i kera-test-db psql -U kera -d kera_shots -v ON_ERROR_STOP=1 -q").split()
VERSION = os.environ.get("DEMO_VERSION", "dev")

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def call(method: str, path: str, body=None):
    req = urllib.request.Request(URL + path, method=method, data=None if body is None else json.dumps(body).encode(), headers={"content-type": "application/json"})
    try:
        with opener.open(req) as r:
            raw = r.read()
    except urllib.error.HTTPError as e:
        raise SystemExit(f"{method} {path}: {e.code} {e.read()[:300]!r}")
    return json.loads(raw) if raw else None


def login():
    call("POST", "/api/v1/auths/signin", {"email": os.environ.get("DEMO_EMAIL", "admin@example.com"), "password": os.environ["DEMO_PASSWORD"]})


def sql(text: str) -> str:
    r = subprocess.run(PSQL, input=text, capture_output=True, text=True)
    if r.returncode:
        raise SystemExit("psql: " + r.stderr[:500])
    return r.stdout


def lit(s) -> str:
    return "NULL" if s is None else "'" + str(s).replace("'", "''") + "'"


def stamp(log: str, start, end) -> str:
    """Log lines the way the runner stores them: a UTC time in front, spread over the step."""
    lines = [x for x in log.split("\n") if x]
    span = (end - start).total_seconds()
    return "".join(f"{(start + timedelta(seconds=span * i / max(1, len(lines)))).strftime('%H:%M:%S')} {x}\n" for i, x in enumerate(lines))


def setup():
    os.makedirs(OUT, exist_ok=True)
    login()
    for name in demo.RUNNERS:
        tok = call("POST", "/api/v1/admin/pipeline-runners", {"name": name})["token"]
        path = os.path.join(OUT, name + ".token")
        with open(path, "w") as f:
            f.write(tok)
        os.chmod(path, 0o600)
    for name, about in demo.SECRETS:
        call("POST", "/api/v1/admin/secrets", {"name": name, "description": about, "value": demo.secret_value()})
    for p in demo.PIPELINES:
        n = p["name"]
        call("POST", "/api/v1/admin/pipelines", {"name": n, "title": p["title"]})
        call("PUT", f"/api/v1/admin/pipelines/{n}/schedule", {"run_mode": "kera", "scheduled": False, "frequency": p["frequency"], "at_minute": p["at"], "timezone": "UTC"})
        call("PUT", f"/api/v1/admin/pipelines/{n}/script", {"files": p["files"], "entry": p["entry"], "note": "First version"})
        call("PUT", f"/api/v1/admin/pipelines/{n}/stages", {"stages": [{"name": k, "title": t, "about": a} for k, t, a, *_ in p["steps"]]})
        for var, value, kind in p["env"]:
            if kind == "plain":
                call("PUT", f"/api/v1/admin/pipelines/{n}/env", {"name": var, "value": value, "secret": False})
            elif kind == "secret":
                call("PUT", f"/api/v1/admin/pipelines/{n}/env", {"name": var, "value": demo.secret_value(), "secret": True})
            else:
                call("PUT", f"/api/v1/admin/pipelines/{n}/env", {"name": var, "secret_ref": kind})
    print("setup done")


def schedule_on():
    login()
    for p in demo.PIPELINES:
        call("PUT", f"/api/v1/admin/pipelines/{p['name']}/schedule", {"scheduled": True})


def history():
    now = datetime.now(timezone.utc)
    q = ["BEGIN;"]
    only = sys.argv[2:]  # pipeline names; none means all
    for pi, p in enumerate(demo.PIPELINES):
        if only and p["name"] not in only:
            continue
        runs = demo.plan(p, now, seed=pi + 1)
        for ri, (t, status, total, wait, fail_at, text) in enumerate(runs):
            start = t + timedelta(milliseconds=ri % 7 * 90)
            claimed = start + timedelta(seconds=wait)
            where = "runner-1" if ri % 3 else "runner-2"
            scale = max(0.2, (total - 2) / sum(s[3] for s in p["steps"]))
            steps, cur = [], claimed + timedelta(seconds=5)
            for si, (key, _t, _a, secs, log, counts) in enumerate(p["steps"]):
                if fail_at is not None and si > fail_at:
                    steps.append((si, key, "skipped", None, None, "", {}, ""))
                    continue
                d = secs * scale * (0.5 if fail_at == si else 1)
                fin = cur + timedelta(seconds=d)
                st, err, lg = "ok", "", log
                if fail_at == si:
                    st, err, lg = "failed", text, "\n".join(log.split("\n")[:2]) + "\n" + text
                if status == "warning" and p.get("warn") and p["warn"][0] == key:
                    st, err, lg = "warning", p["warn"][1], log + "\n" + p["warn"][1]
                lg = stamp(lg, cur, fin)
                steps.append((si, key, st, cur, fin, err, counts if st != "failed" else {}, lg))
                cur = fin
            end = (cur if fail_at is None else cur) + timedelta(seconds=1)
            run_err = text if status == "failed" else ""
            src = json.dumps({"host": where, "user": where, "dir": f"/work/{p['name']}", "command": p["entry"], "client": "kera runner " + VERSION})
            q.append(f"""WITH r AS (INSERT INTO pipeline_run (pipeline_id, trigger, status, client_done, error, started_at, active_at, finished_at, queued, claimed_at, runner_id, script_version, source)
  SELECT p.id, 'schedule', {lit(status)}, true, {lit(run_err)}, {lit(start.isoformat())}, {lit(end.isoformat())}, {lit(end.isoformat())}, false, {lit(claimed.isoformat())},
  (SELECT id FROM pipeline_runner WHERE name = {lit(where)}), 1, {lit(src)}::jsonb FROM pipeline p WHERE p.name = {lit(p['name'])} RETURNING id)
INSERT INTO pipeline_step (run_id, seq, kera, key, name, status, started_at, finished_at, error, counts, log)
  SELECT r.id, v.seq, false, v.key, v.key, v.status, v.started_at::timestamptz, v.finished_at::timestamptz, v.error, v.counts::jsonb, v.log FROM r, (VALUES """
                     + ",".join(f"({s[0]}, {lit(s[1])}, {lit(s[2])}, {lit(s[3].isoformat() if s[3] else None)}, {lit(s[4].isoformat() if s[4] else None)}, {lit(s[5])}, {lit(json.dumps(s[6]))}, {lit(s[7])})" for s in steps)
                     + ") AS v(seq, key, status, started_at, finished_at, error, counts, log);")
    q.append("COMMIT;")
    sql("\n".join(q))
    print("history done")


def chats():
    """A few chat titles for the sidebar, so it does not read "No chats yet"."""
    titles = [("Draft the release notes", 3), ("Summarise the onboarding guide", 20), ("Explain the refund policy", 30), ("Plan the Q4 roadmap", 60 * 30), ("Rewrite the status email", 60 * 52), ("Translate the help centre intro", 60 * 24 * 5)]
    q = ["BEGIN;"]
    for i, (t, minutes) in enumerate(titles):
        q.append(f"INSERT INTO chat (id, user_id, title, created_at, updated_at) SELECT 'demo-chat-{i}', id, {lit(t)}, now() - interval '{minutes} minutes', now() - interval '{minutes} minutes' FROM \"user\" ORDER BY created_at LIMIT 1;")
    q.append("COMMIT;")
    sql("\n".join(q))


# the log lines start with the UTC time they were written; moving a run moves them too
SHIFT_LOG = r"""coalesce((SELECT string_agg(CASE WHEN l ~ '^[0-9][0-9]:[0-9][0-9]:[0-9][0-9] ' THEN to_char(substr(l, 1, 8)::time + d.by, 'HH24:MI:SS') || substr(l, 9) ELSE l END, E'\n' ORDER BY n)
  FROM regexp_split_to_table(s.log, E'\n') WITH ORDINALITY AS t(l, n)), '')"""


def start_run(name: str) -> int:
    return call("POST", f"/api/v1/admin/pipelines/{name}/run", {})["id"]


def wait_run(name: str, run: int):
    import time

    while call("GET", f"/api/v1/admin/pipelines/{name}/runs/{run}").get("finished_at") is None:
        time.sleep(2)


def run():
    """seed.py run NAME [--nowait]: Run now, and wait for the run to end unless asked not to. Prints the run id."""
    login()
    rid = start_run(sys.argv[2])
    if "--nowait" not in sys.argv:
        wait_run(sys.argv[2], rid)
    print(rid)


def wait():
    login()
    wait_run(sys.argv[2], int(sys.argv[3]))


def shift():
    """Move the newest finished real run of each pipeline back to its last timetable slot (UTC), keeping its own durations."""
    now = datetime.now(timezone.utc)
    only = sys.argv[2:]  # pipeline names; none means all
    q = ["BEGIN;"]
    for p in demo.PIPELINES:
        if only and p["name"] not in only:
            continue
        slot = demo.slots(p, now)[-1]
        q.append(f"""WITH last AS (SELECT r.id, r.started_at FROM pipeline_run r JOIN pipeline p ON p.id = r.pipeline_id WHERE p.name = {lit(p['name'])} AND r.finished_at IS NOT NULL ORDER BY r.id DESC LIMIT 1),
d AS (SELECT id, {lit(slot.isoformat())}::timestamptz - started_at AS by FROM last),
u1 AS (UPDATE pipeline_run r SET started_at = r.started_at + d.by, active_at = r.active_at + d.by, finished_at = r.finished_at + d.by, claimed_at = r.claimed_at + d.by, trigger = 'schedule' FROM d WHERE r.id = d.id)
UPDATE pipeline_step s SET started_at = s.started_at + d.by, finished_at = s.finished_at + d.by, log = {SHIFT_LOG} FROM d WHERE s.run_id = d.id;""")
    # the setup happened two weeks ago, and a stored secret was last used by the newest run that references it
    q.append("""UPDATE secret SET created_at = now() - interval '14 days', updated_at = now() - interval '12 days';
UPDATE pipeline SET created_at = now() - interval '14 days', updated_at = now() - interval '14 days';
UPDATE pipeline_env SET updated_at = now() - interval '12 days';
UPDATE pipeline_runner SET created_at = now() - interval '14 days';
UPDATE pipeline_script SET created_at = now() - interval '14 days';
UPDATE secret s SET last_used_at = (SELECT max(r.claimed_at) FROM pipeline_run r JOIN pipeline_env e ON e.pipeline_id = r.pipeline_id WHERE e.secret_id = s.id);""")
    q.append("COMMIT;")
    sql("\n".join(q))
    print("shift done")


if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    {"setup": setup, "history": history, "shift": shift, "schedule-on": schedule_on, "chats": chats, "run": run, "wait": wait}.get(cmd, lambda: sys.exit(__doc__))()
