# Pipelines

A pipeline keeps knowledge collections equal to a folder that lives somewhere else (a docs repository, a nightly export, a fetcher's output folder) and shows every run as a flow of steps with logs and errors.

Kera never starts, stops or retries your script. Your cron, CI or scheduler starts it; the script (or the `kera pipeline` command) reports to Kera with the pipeline's token. Kera records the run, uploads and indexes the files, and shows the result in **Admin > Pipelines**.

- [Concepts](#concepts)
- [Quick start: push a folder](#quick-start-push-a-folder)
- [Wrap a script](#wrap-a-script)
- [The marker protocol](#the-marker-protocol)
- [The HTTP API for scripts](#the-http-api-for-scripts)
- [The admin API](#the-admin-api)
- [How the sync works](#how-the-sync-works)
- [Schedules, lateness and the health URL](#schedules-lateness-and-the-health-url)
- [Recipes](#recipes)
- [Limits](#limits)
- [Troubleshooting](#troubleshooting)

## Concepts

| Term | Meaning |
|---|---|
| Pipeline | A name (`docs-sync`), the collections it may write (its **targets**), an owner, an optional expected interval, and one **token**. |
| Token | `kp-` plus 40 characters. It is the only credential the token routes accept. It is shown once, when the pipeline is created or the token is rotated; Kera stores only its hash. |
| Run | One execution. A pipeline has at most one open run. A run is `running`, then `ok`, `warning` or `failed`. |
| Script step | A named stage the script reports: `fetch`, `convert`, `upload`. Steps are shown in the order they first appear. |
| Collection step | One per target collection, created by Kera when the run starts (pending) and filled by Kera while it syncs. It is keyed by the collection id and shown by the collection name. A script cannot write one. |
| Owner | The user the uploaded files belong to. It is the admin who created the pipeline until an admin changes it. A user who owns a pipeline cannot be deleted until its owner is changed. |

A pipeline owns the files it uploaded (`knowledge_file.origin = pipeline:<id>`). Files that people added by hand, or another pipeline's files, are never replaced or removed by it. See [How the sync works](#how-the-sync-works).

## Quick start: push a folder

1. **Admin > Pipelines > New**: name it (`docs-sync`), choose or create the collection, copy the token.
2. Put the connection in an env file (`kera.env`):

   ```
   KERA_URL=https://kera.example.com
   KERA_PIPELINE=docs-sync
   KERA_PIPELINE_TOKEN=kp-...
   ```

3. Run it, from cron, CI or by hand:

   ```sh
   docker run --rm --env-file kera.env -v "$PWD/docs:/docs:ro" \
     ghcr.io/cdmx-in/kera pipeline push --collection "Product docs" --dir /docs
   ```

   or, with a `kera` binary: `kera pipeline push --collection "Product docs" --dir ./docs`.

   When you opened Kera on `localhost`, a container cannot reach it there: the command the token dialog prints adds `--add-host host.docker.internal:host-gateway -e KERA_URL=http://host.docker.internal:PORT` to reach the host. Use the same if you write the command yourself.

`kera pipeline push --collection NAME --dir DIR [--wait 15m] [--dry-run]`:

- walks the folder; skips names that start with a dot, symbolic links and files whose type Kera cannot read (it lists what it skipped); folders are flattened: `guides/setup.md` becomes `guides--setup.md`. Two files that flatten to the same name are an error;
- computes each file's SHA-256 and lists the folder to Kera; Kera answers which files are new or changed; the command uploads those, lists again, then waits for indexing (`--wait`, default 15 minutes);
- outside a wrapped run it opens its own run and closes it, and exits `0` when the run is `ok` and `1` when it is `warning` or `failed` (a file could not be indexed, or the removal of files was refused);
- `--dry-run` shows what would be uploaded, adopted and removed, and changes nothing. The run is recorded with a "Dry run" badge;
- exit `75` when the pipeline already has an open run (the message says which one and since when), so a cron job can skip and try again later.

Retries: a transport error or a 5xx answer is tried three times with a growing pause.

## Wrap a script

If a script fetches and converts files before they are pushed, run it under the wrapper, so each stage shows up as a step with its log. The wrapper runs your script, so it needs a `kera` binary on the machine where the script lives; the Docker image has no shell to run it in. Copy the static binary out of the image:

```sh
id=$(docker create ghcr.io/cdmx-in/kera) && docker cp "$id:/usr/local/bin/kera" ./kera && docker rm "$id"
```

Then:

```sh
KERA_URL=https://kera.example.com KERA_PIPELINE=docs-sync KERA_PIPELINE_TOKEN=kp-... \
  kera pipeline run -- ./refresh.sh
```

`kera pipeline run [--name NAME] -- COMMAND [ARGS...]`:

- opens a run, prints its link, runs the command with `KERA_RUN` and `KERA_MARK` set, prints the link again at the end;
- reads the command's stdout and stderr on **one** pipe, so lines keep the order they were written, and copies them to its own stdout;
- turns [marker lines](#the-marker-protocol) into steps and sends everything else to the current step's log (output before the first marker goes to a step called `start`);
- sends a report every 5 seconds or 16 KiB, a heartbeat every 60 seconds, and everything that is left before it closes the run;
- exits with the command's exit code.

If the command exits non-zero, the step it was in fails with "exit code N", and the run's error names every failed step ("Failed at step fetch: kept the previous files. Failed at step upload: exit code 1."). A script that keeps its failures for the end (`fail` markers, then a deferred `exit 1`) prints `${KERA_MARK}end` just before that exit: the current step then ends `ok`, and only the steps that failed are blamed ("Failed at step fetch: kept the previous files."). Without `end`, Kera cannot tell a deferred exit from a crash after the soft failure, so it blames the step that was running as well.

If Kera cannot be reached when the run starts (or the token is wrong), the wrapper warns on stderr and **runs the command unreported**: a Kera outage never stops the real work. Only a `409 run_active` skips the run, with exit `75`.

Inside a wrapped run, `kera pipeline push` does not open a run of its own. It exits `0` on file-level failures and refused removals (they are on the collection step and in the run's status) and non-zero only on a transport or authentication error, so one unreadable file never stops the other collections under `set -e`. A listing that Kera refuses (a collection the pipeline does not write to, a collection that is changing embedding model) is reported as a failed step through a `fail` marker and the command still exits `0`.

A script that is itself wrapped by the same file can guard against loops:

```sh
if [ -z "${KERA_MARK:-}" ] && [ -n "${KERA_PIPELINE:-}" ] && command -v kera >/dev/null; then
  exec kera pipeline run -- bash "$0" "$@"
fi
M="${KERA_MARK:-::}"        # without the wrapper the markers are plain log lines
step() { echo "${M}step $1"; }
```

`bash "$0"` (not just `"$0"`) keeps the script starting when it was run as `bash refresh.sh`, whose name has no slash and so is not searched in the current folder, or when it has no execute bit.

## The marker protocol

A marker is a line that starts with `$KERA_MARK`, a random value the wrapper generates for each run (`::kera-` and 32 hex digits, `::`). Text your script fetched cannot forge a marker because it does not know the value. A line that starts with another `::kera-...::` prefix, or that carries the mark in the middle, is an ordinary log line.

| Line | Effect |
|---|---|
| `${KERA_MARK}steps a b c` | Declares steps, shown as pending until they start. Steps that never start are shown as skipped when the run closes. |
| `${KERA_MARK}step NAME` | Starts `NAME` (one word, up to 100 characters). The step before it ends `ok` unless a marker already ended it. A step that is already closed is **not** re-entered: the line is ignored. |
| `${KERA_MARK}warn TEXT` | The current step gets a warning with that text; the script goes on. |
| `${KERA_MARK}fail TEXT` | The current step fails with that text; the script goes on. The run will be `failed`, and files are not removed from collections in this run. |
| `${KERA_MARK}skip TEXT` | The current step is skipped, with the text as the reason. |
| `${KERA_MARK}count key=N` | Sets a number on the current step (`key` starts with a letter, up to 40 characters; at most 20 keys per step). |
| `${KERA_MARK}end` | The script is done and only keeps its verdict for its exit code (`exit 1` after `fail` markers). The current step ends `ok`; a non-zero exit then blames only the steps that failed. |
| anything else | A log line of the current step. |

The wrapper shows markers on its own stdout as `[step] NAME`, `[warn] TEXT`, `[fail] TEXT` and `[skip] TEXT`; `steps`, `count` and `end` lines are not shown.

Statuses never go down: a step marked `fail` stays failed, a warning can become a failure, a failed step cannot become `ok`.

## The HTTP API for scripts

Any language can report a run. Every route needs `Authorization: Bearer kp-...` and the pipeline's name in the path; the token must belong to that pipeline (otherwise `401`). A pipeline that is switched off answers `403 pipeline_disabled`. No user session or API key works on these routes, and a `kp-` token works on no other route.

Errors are `{"detail": "...", "code": "..."}`. Times are RFC 3339 in UTC. The server stamps the times; the script never sends one.

| Route | Purpose |
|---|---|
| `POST /api/v1/pipelines/{name}/runs` | Open a run |
| `POST /api/v1/pipelines/{name}/runs/{run}/steps` | Report steps and logs; an empty list is a heartbeat |
| `POST /api/v1/pipelines/{name}/runs/{run}/sync` | List a collection's files; get what to upload |
| `PUT /api/v1/pipelines/{name}/runs/{run}/files` | Upload one file |
| `POST /api/v1/pipelines/{name}/runs/{run}/finish` | The script is done |
| `GET /api/v1/pipelines/{name}/runs/{run}` | The run and its steps |
| `GET /api/v1/pipelines/{name}/health` | `200` or `503`, for monitoring |

### Open a run

```sh
curl -sS -X POST "$KERA_URL/api/v1/pipelines/docs-sync/runs" \
  -H "Authorization: Bearer $KERA_PIPELINE_TOKEN" -H 'Content-Type: application/json' \
  -d '{"steps": ["fetch", "convert", "upload"]}'
```

Body (all optional): `{"trigger": "script" | "push", "steps": ["fetch", ...], "dry_run": false}`. A dry run lists, plans and writes nothing: uploads are refused (`400 dry_run`), nothing is adopted or removed, and the one-shot flags stay on.

`201`:

```json
{"run": 12, "url": "https://kera.example.com/admin/pipelines/docs-sync/12", "version": "v1.4.0",
 "dry_run": false, "collections": [{"id": "0192...", "name": "Product docs"}]}
```

`409` when the pipeline already has an open run:

```json
{"detail": "this pipeline already has a run in progress (run 11, started 2026-03-10 06:00, last report 06:02 UTC)",
 "code": "run_active", "run": 11, "trigger": "script", "started_at": "...", "active_at": "..."}
```

Starting a run trims the pipeline to its newest 50 runs.

### Report steps

`POST .../runs/{run}/steps`, body at most 1 MiB:

```json
{"steps": [
  {"name": "fetch", "status": "running"},
  {"name": "fetch", "log": "downloaded 25 files\n", "counts": {"files": 25}},
  {"name": "fetch", "status": "ok"}
]}
```

Each item is `{name, status?, error?, counts?, log?}`, applied in order. `status` is `pending`, `running`, `ok`, `warning`, `failed` or `skipped`. A new step without a status starts running; a pending step that gets a log starts running. A closed step (`ok`, `warning`, `failed`, `skipped`) only moves up to a worse verdict. `error` is the text shown for a warning or failure. `log` is text; each line is stored with the server's time (`06:14:09 downloaded 25 files`). Reply: `{"ok": true, "ignored": 0}`; `ignored` counts items the server dropped (a name that is empty or over 100 bytes, an unknown status, a 101st step). `409 run_closed` once the run is finished or closed.

The script cannot write a collection step: a step is a script step, keyed by the name you send, even when the name is a collection's.

An empty body, `{"steps": []}`, is a heartbeat: it tells Kera the script is alive.

### List a collection and upload

```sh
curl -sS -X POST "$KERA_URL/api/v1/pipelines/docs-sync/runs/12/sync" \
  -H "Authorization: Bearer $KERA_PIPELINE_TOKEN" -H 'Content-Type: application/json' \
  -d '{"collection": "Product docs", "complete": true,
       "files": [{"name": "setup.md", "sha256": "9f86d0...", "size": 1832}]}'
```

- `collection` is a target's id or its name (when exactly one target has that name). Any other collection is `403 not_a_target`; the API never creates a collection.
- `files` is the whole folder, at most 20,000 files, each `{name, sha256, size}`. A name is a single file name of at most 200 bytes, with no directories (`a--b.md`, not `a/b.md`) and no control characters (`400 bad_name`); `sha256` is 64 hex digits.
- `complete: true` says the list is the whole folder; only then can files that are no longer listed be removed when the run closes. List a collection once per run; a second call with the same files is fine (that is how `push` checks what is left after the uploads).
- Reply: `{"upload": [...], "unchanged": [...], "received": [...], "adopted": [...], "ambiguous": [...], "stale": [...], "refused": "..."}`. Upload the names in `upload`. `received` are already staged for indexing. `stale` are files of this pipeline that were not listed. `refused` (when present) says the removal of the stale files would be refused. `409 reindexing` while the collection moves to another embedding model.

Upload:

```sh
curl -sS -X PUT "$KERA_URL/api/v1/pipelines/docs-sync/runs/12/files?collection=Product%20docs&name=setup.md&sha256=9f86d0..." \
  -H "Authorization: Bearer $KERA_PIPELINE_TOKEN" --data-binary @docs/setup.md
```

The body is the file's bytes. The server checks the SHA-256 (`400 checksum_mismatch`), the size against the upload limit (`413`) and the name. Reply: `{"name": "setup.md", "changed": true, "replaced": false}`.

### Finish

`POST .../runs/{run}/finish` with `{"error": "..."}` (optional). The script is done: steps still running end `ok`, or `failed` when an error is given. Kera then waits for the uploaded files to be indexed and closes the run. An `error` makes the run `failed` and skips removing files. Finishing twice is fine. The reply is the run, as below.

### Read a run

`GET .../runs/{run}` (also the reply of `finish`):

```json
{"id": 12, "pipeline": "docs-sync", "trigger": "script", "dry_run": false,
 "status": "running", "client_done": true, "error": "",
 "started_at": "...", "active_at": "...", "finished_at": null,
 "counts": {"listed": 25, "unchanged": 22, "uploaded": 3, "indexed": 25, "failed": 0, "removed": 0},
 "steps": [
   {"seq": 1, "kera": false, "key": "fetch", "name": "fetch", "status": "ok",
    "started_at": "...", "finished_at": "...", "error": "", "counts": {"files": 25}, "log_dropped": false},
   {"seq": 1000, "kera": true, "key": "0192...", "name": "Product docs", "status": "running",
    "started_at": "...", "finished_at": null, "error": "", "counts": {"listed": 25, "waiting": 3}, "log_dropped": false}
 ]}
```

Reading a run also moves it on: counts are refreshed, and a run whose script is done closes once its files are indexed. `status` is `running` until the run closes. Logs are not included; admins read a step's log from the admin API.

### Health

`GET /api/v1/pipelines/{name}/health` needs the pipeline's token. `200` or `503` with:

```json
{"status": "ok", "detail": "", "last_ok_at": "2026-03-10T06:14:09Z"}
```

`status` is `ok`, `warning` (the last run finished with a warning), `never_run`, or, with `503`, `failed` (the last closed run failed; `detail` has its error) or `late`. Dry runs are ignored. See [Schedules](#schedules-lateness-and-the-health-url).

## The admin API

All routes need an admin. Times are RFC 3339 in UTC.

| Route | Purpose |
|---|---|
| `GET /api/v1/admin/pipelines` | List pipelines |
| `POST /api/v1/admin/pipelines` | Create one; the token is in the reply, once |
| `GET /api/v1/admin/pipelines/{name}` | One pipeline |
| `PUT /api/v1/admin/pipelines/{name}` | Change it (only the fields sent) |
| `DELETE /api/v1/admin/pipelines/{name}` | Delete it; its collections and files stay |
| `POST /api/v1/admin/pipelines/{name}/token` | Rotate the token: `{"token": "kp-...", "token_prefix": "kp-abcdefg"}` |
| `POST /api/v1/admin/pipelines/{name}/once` | Set the one-shot flags |
| `GET /api/v1/admin/pipelines/{name}/runs?before=&limit=` | Runs, newest first |
| `GET /api/v1/admin/pipelines/{name}/runs/{run}` | One run with its steps (no logs) |
| `GET /api/v1/admin/pipelines/{name}/runs/{run}/steps/{seq}` | One step with its log |
| `POST /api/v1/admin/pipelines/{name}/runs/{run}/close` | Close a stuck run as failed |

A pipeline:

```json
{"id": "0192...", "name": "docs-sync", "title": "Product docs",
 "owner": {"id": "...", "name": "Ada", "email": "ada@example.com"},
 "targets": [{"id": "0192...", "name": "Product docs", "missing": false}],
 "every_minutes": 1440, "token_prefix": "kp-abcdefg",
 "adopt": false, "allow_mass_delete": false, "enabled": true, "managed": false,
 "created_at": "...", "updated_at": "...",
 "late": false, "running": false, "last_ok_at": "...",
 "last_run": {"id": 12, "pipeline": "docs-sync", "trigger": "script", "dry_run": false, "status": "ok",
              "client_done": true, "error": "", "started_at": "...", "active_at": "...", "finished_at": "...",
              "counts": {"listed": 25, "unchanged": 25}}}
```

- `last_run` is `null` until the first run. A target whose collection was deleted has `missing: true` and an empty name.
- State for a list: off when `enabled` is false; running when `running`; otherwise late when `late`, else the `last_run.status`; never run when `last_run` is `null`.
- `late` is true when `every_minutes > 0` and the newest ok or warning run that synced every target (dry runs and runs that touched no collection do not count) finished more than `every_minutes + max(15 minutes, 10 percent)` ago; a pipeline that never had one counts from its creation.

**Create**: `POST` body

```json
{"name": "docs-sync", "title": "Product docs", "targets": ["<collection id or exact name>"],
 "new_collections": ["Product docs"], "every_minutes": 1440, "enabled": true}
```

`name` is 1 to 40 lowercase letters, digits and dashes. `targets` are existing collections (id, or an exact name when only one collection has it); `new_collections` are created, owned by the admin who sent the request. Two targets may not share a name (`400 duplicate_target_name`) because a script names its collection. The owner is the admin who sent the request. Reply `201 {"pipeline": {...}, "token": "kp-..."}`. Form errors are `400` with `field`.

**Update**: `PUT` takes any of `title`, `owner_id`, `targets`, `new_collections`, `every_minutes`, `enabled`. The name never changes. Changing `owner_id` moves the files the pipeline uploaded and the target collections the old owner held to the new owner, in one transaction. A collection removed from `targets` keeps its files as they are (still marked as this pipeline's); a run that is already open keeps the targets it started with.

**Once**: `POST` body `{"adopt": true, "mass_delete": true}` (either may be left out, `false` clears). `adopt` lets the next run take over same-named files that no pipeline owns. `mass_delete` lets the next run remove files that would otherwise be [refused](#how-the-sync-works). Both are cleared by the next run that ends `ok` or `warning`; failed runs and dry runs leave them.

**Runs**: `GET .../runs?before=<run id>&limit=30` returns `{"runs": [<run without steps>], "more": true}`; `limit` is at most 100. `counts` in a run is the collection steps' counts added up.

**A step with its log**: the step as in a run, plus `"log": "06:14:09 downloaded 25 files\n..."`. The log is the last 256 KiB; `log_dropped` says it was cut.

**Close**: for a run whose script died. The run fails with "Closed by an admin at HH:MM UTC.", a step that was still running fails with that reason, steps that never started are skipped, and nothing is removed from the collections.

Collection step counts: `listed`, `unchanged`, `adopted`, `uploaded`, `waiting`, `indexed`, `failed`, `removed` (and `refused` when a removal was refused). Its log has `+ name` for a new file, `~ name` for a replaced one, `- name` for a removed one and `FAILED name: reason`.

## How the sync works

1. **Plan.** The script lists the folder. Kera compares each file's SHA-256 with the file it holds under that name and answers `upload`, `unchanged`, `received` (the same content is already waiting for indexing). With a pending `adopt`, a same-named file that no pipeline owns is taken over instead of duplicated; two such files make the name `ambiguous` and it is not adopted.
2. **Upload.** Each upload is staged beside the old version, hidden from search, the file list and the chat tools. When the new version is indexed, the old file and its chunks are swapped out in one database transaction: a search sees the old document or the new one, never both and never neither. A file that cannot be read or indexed keeps its old version, fails its collection step and is offered again by the next run. A new file that yields no text also fails.
3. **Wait.** When the script finishes, the run waits for the files of this run to be indexed. The run is closed by whoever looks next (the script's own `finish`, the web page, the minute ticker).
4. **Remove.** Only if the listing was `complete`, the script reported no error and **no script step failed**, files this pipeline owns that this run did not list are removed. The removal is refused (the collection step gets a warning; nothing is removed) when the listing was empty, or when more than `max(5, half)` of the pipeline's files would go, unless an admin allowed it once.
5. **Close.** Steps that never started are skipped, the worst step decides the run's status, and the one-shot flags clear.

Consequences worth knowing:

- Replace is atomic per document, not per run. A crashed run can leave some documents new and some old; none is ever missing.
- A replaced document gets a new file id, so a chat citation of the old version stops opening.
- Names are flat. A rename is a delete and an add.
- One indexing worker serves the whole server, so a large first sync delays other uploads.
- Deleting a pipeline keeps the collections and the files; the files go back to "added by Kera".

## Schedules, lateness and the health URL

Kera does not run anything on a timer. **Expected every** (`every_minutes`) only tells Kera how often a good run should happen, so it can say **Late** and answer `503` on the health URL. `0` means no expectation.

A minute ticker keeps runs from hanging:

- a run whose script has not reported for 30 minutes, or has been running for 3 hours, is failed ("The script stopped reporting at 06:14 UTC."); the step that was running fails with the same text and steps that never started are skipped;
- a run whose script is done but whose files make no indexing progress for 30 minutes is failed, and the collection step names the files that were still waiting;
- other finished runs are closed as soon as their files are indexed.

Kera sends no alerts. Probe the health URL from something you already run:

```sh
curl -fsS -H "Authorization: Bearer $KERA_PIPELINE_TOKEN" "$KERA_URL/api/v1/pipelines/docs-sync/health" >/dev/null \
  || echo "docs-sync is late or failed" # post to your alerting here
```

## Recipes

**Cron** (every night at 02:30):

```
30 2 * * *  docker run --rm --env-file /etc/kera/docs-sync.env -v /srv/docs:/docs:ro ghcr.io/cdmx-in/kera pipeline push --collection "Product docs" --dir /docs
```

Set **Expected every** to `1440`. Exit code `75` means the previous run was still going; cron may ignore it.

**CI** (a GitHub Actions job after a docs change):

```yaml
publish-docs:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - run: |
        docker run --rm \
          -e KERA_URL -e KERA_PIPELINE -e KERA_PIPELINE_TOKEN \
          -v "$PWD/docs:/docs:ro" \
          ghcr.io/cdmx-in/kera pipeline push --collection "Product docs" --dir /docs
      env:
        KERA_URL: https://kera.example.com
        KERA_PIPELINE: docs-sync
        KERA_PIPELINE_TOKEN: ${{ secrets.KERA_PIPELINE_TOKEN }}
```

**A script with its own fetching**: wrap it with `kera pipeline run`, print step markers, call `kera pipeline push` once per collection. The `kera` binary must be where the script runs; it may be older than the server (the command warns).

**First migration of a folder that was uploaded by hand**: tick **Adopt once** for the pipeline, run `kera pipeline push ... --dry-run` and read the plan ("adopt N, upload 0, remove 0"), run it for real, then look for files nobody owns:

```sql
SELECT f.filename FROM knowledge_file kf JOIN file f ON f.id = kf.file_id
WHERE kf.knowledge_id = ANY(<target ids>) AND kf.origin NOT LIKE 'pipeline:%';
```

## Limits

| What | Limit |
|---|---|
| One steps report | 1 MiB |
| Script steps per run | 100 (more are ignored) |
| Step name | 100 bytes |
| Error text | 2,000 characters |
| Log per step | last 256 KiB (`log_dropped`) |
| Log per run | 4 MiB (more is dropped) |
| Runs kept per pipeline | 50, trimmed when a run starts |
| Files in one listing | 20,000 |
| File name | one name, 200 bytes, no directories |
| File size | the server's upload limit (`files.max_mb`) |
| Idle script | 30 minutes without a report |
| Script phase | 3 hours |
| Indexing without progress | 30 minutes |

Logs and error texts are passed through a secret filter (keys, tokens, passwords in `key=value` pairs, private keys, URLs with credentials) and lose control characters before they are stored. The filter is a set of regular expressions: it catches common shapes, not every secret. Run views are admin-only.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `401` on every call | The token does not belong to the pipeline named in the URL, was rotated, or is not a `kp-` token. User sessions and API keys never work on these routes. |
| `403 pipeline_disabled` | The pipeline is switched off in the admin page. |
| `403 not_a_target` | The collection is not one of the pipeline's targets. Add it in the pipeline's form, or fix the name. |
| `409 run_active`, exit code 75 | A run is open. Wait for it, or close it in the admin page if its script died (it is closed by itself after 30 minutes without a report). |
| `409 reindexing` | The collection is moving to another embedding model. Sync again when that has finished. |
| A run is "failed" and the script exited 0 | A file could not be indexed (the collection step says which and why; the old version stays live), or a step was marked `fail`. |
| The run is "warning": removal refused | More than half of the files (or all of them) are missing from the listing. If that is right, tick **Allow removing once** and run again; if not, check the folder. |
| Files were not removed | The run failed, a script step failed, or the listing was not `complete`. Removal waits for a clean run. |
| "no text could be extracted" | The file has no text (a scan, an empty file). Add a text layer or remove it. |
| The same file is uploaded every run | Its content changes every run (a build date, a random id). Kera compares the SHA-256 of the bytes. |
| A same-named file added by hand appears twice | The pipeline does not own it. Use **Adopt once** to take it over, or delete the extra by hand. |
| Everything shows "late" | The expected interval is shorter than the schedule, or no run synced every target recently; a run that touches no collection does not count. |
| `kera pipeline run` prints "not reporting this run" | It could not reach Kera or the token is wrong; the command ran anyway. The message says why. |
| "the server is version X, this command is Y" | Update the `kera` binary or image; old commands keep working against newer servers for the calls above. |
