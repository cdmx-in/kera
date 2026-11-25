# Kera-run pipelines

A [pipeline](pipelines.md) normally reports runs that something else starts (cron, CI). A **Kera-run** pipeline moves that something into Kera: the script, its environment and its timetable are stored in Kera and edited in **Admin > Pipelines**, Kera queues a run when the timetable says so, and a **runner** executes it. The run, its steps, logs and errors look the same as for any pipeline.

Kera itself never executes a script: its image has no shell and no Docker socket. The runner is a separate container (or any machine with the `kera` binary) that only makes outgoing calls to Kera. The Compose stack in `deploy/` already runs one, so Kera-run pipelines work as soon as it is up.

- [How it works](#how-it-works)
- [Set one up](#set-one-up)
- [The script and its files](#the-script-and-its-files)
- [Environment and secrets](#environment-and-secrets)
- [Notebook and checks](#notebook-and-checks)
- [Timetable](#timetable)
- [Cancel, limits and failures](#cancel-limits-and-failures)
- [Alerts](#alerts)
- [Security](#security)
- [Moving a cron job over](#moving-a-cron-job-over)

## How it works

1. The minute ticker finds a Kera-run pipeline whose slot has come, claims the slot (a compare-and-set, so two servers never queue it twice) and opens a run in the **queued** state, pinned to the newest script version. **Run now** does the same by hand.
2. A runner asks `POST /api/v1/runner/claim` (long poll, up to 25 s). Kera hands it the oldest queued run: script files, entry file, arguments, the decrypted environment and a **job token** that is valid for that run only, while it is open.
3. The runner writes the files to `/work/<pipeline>/.kera-script/`, starts the entry file with the pipeline's workspace `/work/<pipeline>` as its working directory, and reports through the normal run API (`${KERA_MARK}step` markers, `kera pipeline push`, the heartbeat) exactly as a script under `kera pipeline run` would.
4. The run closes like any other. A queued run that no runner claims within 10 minutes fails with "No runner picked this up". A slot that comes while the previous run is still open is skipped; slots missed while Kera was down run once.

## Set one up

1. **Runner.** Nothing to do with the Compose stack in `deploy/` ([Pipelines run by Kera](deploy.md#pipelines-run-by-kera)): its `runner` service signs in with a token Kera makes itself and shows as `builtin` under Admin > Pipelines > **Runners**. For another machine, or a runner that needs other mounts: Runners > New runner, copy the `kr-` token (shown once) and start the container from `sidecars/runner/` (see `docker-compose.example.yml`) with `KERA_URL`, `KERA_RUNNER_TOKEN` and a volume for `/work`. Mount only what the scripts must read or change.
2. **Pipeline.** Create it as usual; in its edit page choose **How it runs: Run by Kera**.
3. **Script.** Open the pipeline's **Script** tab, add files, pick the entry file (`.sh` or `.py`), save. Add variables on the **Environment** tab.
4. **Timetable** in the edit page, then **Run now** to try it.

## The script and its files

- Every save is a new **version**; the newest is live and a run records the version it used. The page shows the history with a diff and can restore an old version as a new one. The last 50 versions are kept.
- Up to 200 text files, 1 MiB each, 8 MiB together. Names are relative paths (`scripts/fetch.py`); no `..`, no absolute paths.
- The entry is started with `bash` (`.sh`, `.bash`) or `python3` (`.py`); the arguments are split on spaces.
- Working directory: the pipeline's **workspace**, `/work/<pipeline>`. It persists between runs, so put state there (databases, the previous snapshot). The script's own files are in `$KERA_SCRIPT_DIR`; `dirname "$0"` also works.
- A `requirements.txt` among the files is installed into a virtual environment on the first run and reused by every later run of the same file contents.
- Set by the runner: `KERA_URL`, `KERA_PIPELINE`, `KERA_PIPELINE_TOKEN` (the job token), `KERA_RUN`, `KERA_MARK`, `KERA_SCRIPT_DIR`, `HOME` (the workspace), `PATH`, `TZ`, `LANG`. Nothing else of the runner's own environment reaches the script.
- Because `KERA_MARK` is set, a script that wraps itself with `kera pipeline run` when `KERA_MARK` is empty (the pattern in [pipelines.md](pipelines.md#wrap-a-script)) runs unchanged.

## Environment and secrets

Variables are stored encrypted (AES-256-GCM, from `KERA_SECRET_KEY`). A **secret** variable is write-only: the page shows that it is set, never its value, and the API never returns it. A plain variable can be read back. The names the runner sets itself (`KERA_URL`, `KERA_PIPELINE`, `KERA_PIPELINE_TOKEN`, `KERA_RUN`, `KERA_MARK`, `KERA_SCRIPT_DIR`, `KERA_TEST`) are reserved; other names, such as `KERA_API_KEY`, are ordinary variables.

### Stored secrets

A secret that several pipelines need (an API token, a database password) can live once in **Admin > Pipelines > Secrets** and be referenced by name instead of copied into each pipeline. On a pipeline's Environment tab, choose **Stored secret** for a new variable and pick the name; the row then shows `stored: <name>` and links to the Secrets page. Over the API the variable is `PUT .../env` with `{"name": "API_KEY", "secret_ref": "<secret name>"}` in place of `value` (exactly one of the two).

- A stored value is write-only, like a secret variable: no page or route returns it. **Update value** on the Secrets page (or `PUT /api/v1/admin/secrets/{name}`) rotates it, and every pipeline that references it gets the new value from its next run; a run that has started keeps what it was given.
- A secret in use cannot be deleted: Kera answers `409` and lists the pipelines and variables that use it. Each secret also shows when it was last handed to a run.
- A variable that references a stored secret is always masked in run logs, whatever its own Secret box would say.

The value reaches the runner when it claims a run. The runner masks every secret value it knows in the run log and in its own output, in addition to the server's filter for key shapes.

## Notebook and checks

The Script tab works like a notebook. The script is still **one file** (the entry); its cells are separated by header lines that are plain comments:

```bash
#%% fetch data
curl -fsS "$SOURCE_URL" -o data.json
#%% build
python3 "$KERA_SCRIPT_DIR/build.py"
```

Text before the first header is a first, unnamed cell. When Kera runs the script, the runner turns each header into a step marker, **one line for one line** (so line numbers in errors still match the editor): `fetch data` becomes the step `fetch-data` (step names are one word), and an empty title becomes `cell-2`. Because the headers are comments the same file also runs without the runner, just without step reporting.

**Run a cell** runs it in a live **session** on a runner: one shell (`bash`) or Python process per session, in a scratch folder, so variables, imports, the working folder and files carry from cell to cell. The cell's output streams back under it. Everything a real run has is there (the stored environment with secrets masked in the output, `KERA_URL`, `KERA_SCRIPT_DIR`), plus `KERA_TEST=1`.

- **Nothing real changes.** The session is backed by a **dry-run** pipeline run, so `kera pipeline push` inside a cell lists and plans but never uploads or removes anything. By default the cells work in a scratch folder, not the pipeline's workspace; ticking **Use the real workspace** lets them read (and change) the real files, with a warning.
- **Stop** interrupts a Python cell and keeps the session's variables. A shell cannot be interrupted without ending the cell's remaining commands, so Stop restarts the shell and its variables are lost.
- **Run all** runs every cell in order and stops at the first one that fails.
- A session closes when you leave the page, after 20 minutes without a cell, or after 3 hours. It does not hold up scheduled runs.
- Test sessions appear in the run list as **Notebook** runs; they are never the pipeline's "last run" and do not count for Late or alerts.

**Checks** run on the runner, so they use the same tools as the script: `bash -n` and ShellCheck for `.sh` files, a Python syntax parse for `.py` files. The page runs them a moment after you stop typing and marks the problem lines.

## Timetable

Hourly, daily, weekdays or weekly, at a time in an IANA time zone (`Asia/Kolkata`); wall-clock based, so daylight-saving changes keep the hour. One timetable per pipeline: a script that does something different on Mondays checks the weekday itself (`[ "$(date +%u)" = 1 ]`). The **Late** flag follows the timetable.

## Cancel, limits and failures

- **Cancel** on a queued run closes it at once. On a running run it asks the runner, which stops the script and everything it started (the process group), then reports "Cancelled by an admin."
- A script is stopped after 3 hours, like every run. No report from the script for 30 minutes fails the run.
- If Kera cannot build the job (the script version or pipeline is gone) the run fails with the reason.

## Alerts

Set the setting `pipelines.alert_url` to an Alertmanager `/api/v2/alerts` URL. Every minute Kera posts one `KBPipelineUnhealthy` alert (labels `severity=warning`, `job=<pipeline>`) for each enabled pipeline whose last run failed or that is late, and one resolve when it recovers. This replaces a cron probe of the health URL. It covers every pipeline, including ones started from outside.

## Security

- Script, environment and runner routes are admin-only. A script is **code execution on the runner by design**: give the runner container only the mounts, networks and capabilities its scripts need; it has no Docker socket and publishes no port. The image runs as an unprivileged user (uid 10002), and the built-in runner has only `/work` and `/tmp` to write to.
- Runner tokens (`kr-`) are stored as hashes and can be rotated. The built-in runner's token sits in a file the runner reads (`KERA_RUNNER_TOKEN_FILE`, mounted read-only), which its scripts can read too, as they could read a token in its environment: a runner is as trusted as the people who write its scripts.
- Changes to scripts are recorded in the version history (who, when, note); environment changes are logged without values.

## Moving a cron job over

1. Create the pipeline (and a runner, if you do not use the built-in one); paste the script files; copy the variables from the env files the cron job sourced (only the ones the script uses).
2. Point the paths at what the runner mounts (a workspace under `/work/<pipeline>`, read-only mounts for repositories).
3. **Run now** and compare with the cron run; keep the cron line until two runs from Kera are good, then remove it.
