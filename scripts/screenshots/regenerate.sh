#!/usr/bin/env bash
# Rebuilds the demo workspace on a SCRATCH Kera (never a real one) and retakes the pipeline screenshots in docs/images.
#
#   scripts/screenshots/regenerate.sh [data|shots|clean|all]     (default: all)
#
# Needs: a Kera binary with the web app built in (make build; KERA_BIN names it), Docker with a runner image
# (docker build -f sidecars/runner/Dockerfile -t kera-runner:local sidecars/runner), the test Postgres (make testdb),
# python3, the agent-browser CLI and ImageMagick (convert). The scratch Kera listens on 127.0.0.1 only and has its own
# database, which `clean` drops. Everything shown is invented: see demo.py. Run it where nothing else uses the port
# and container names below. The whole run takes about 12 minutes (the demo scripts really run).
# Known quirk: the hourly pipeline fires by itself at minute 5; if that falls inside the run, a real extra ticket-digest
# run shows in its history (harmless, but the queued screenshot then has a run just above it). Start again if it bothers you.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
work=${DEMO_WORK:-/tmp/kera-demo}
port=${DEMO_PORT:-18097}
db=${DEMO_DB:-kera_shots}
pg=${DEMO_PG_CONTAINER:-kera-test-db}
bin=${KERA_BIN:-$root/kera}
image=${DEMO_RUNNER_IMAGE:-kera-runner:local}
session=${DEMO_BROWSER_SESSION:-kera-demo}
images=${DEMO_IMAGES:-$root/docs/images}

mkdir -p "$work"
export KERA_URL=http://127.0.0.1:$port KERA_LISTEN=127.0.0.1:$port KERA_DATA_DIR=$work/data
export KERA_DATABASE_URL="postgres://kera:kera-test@127.0.0.1:55432/$db?sslmode=disable"
export DEMO_DIR=$work DEMO_EMAIL=admin@example.com TZ=UTC
export DEMO_VERSION=${DEMO_VERSION:-$("$bin" version)}
export DEMO_PSQL="docker exec -i $pg psql -U kera -d $db -v ON_ERROR_STOP=1 -q"
# a throwaway key and password, kept only for this scratch instance
if [ ! -f "$work/secret" ]; then
  head -c 32 /dev/urandom | base64 > "$work/secret"
  head -c 18 /dev/urandom | base64 | tr -d '/+=' > "$work/pass"
fi
export KERA_SECRET_KEY=$(cat "$work/secret") DEMO_PASSWORD=$(cat "$work/pass")

seed() { python3 "$here/seed.py" "$@"; }
ab() { agent-browser --session "$session" "$@" >/dev/null; }

# the scratch server is found by its port, never by name: a production Kera on the same host may match a name
serving_pid() { ss -ltnp | awk -v p=":$port" '$4 ~ p"$"' | sed -n 's/.*pid=\([0-9]*\).*/\1/p' | head -1; }

clean() {
  agent-browser --session "$session" close >/dev/null 2>&1 || true
  docker rm -f demo-runner-1 demo-runner-2 >/dev/null 2>&1 || true
  local pid
  pid=$(serving_pid || true)
  if [ -n "$pid" ] && [ "$(readlink "/proc/$pid/cwd")" = "$work" ]; then kill "$pid"; sleep 1; fi
  docker exec "$pg" psql -U kera -d postgres -c "DROP DATABASE IF EXISTS $db" >/dev/null
}

data() {
  clean
  docker exec "$pg" psql -U kera -d postgres -c "CREATE DATABASE $db" >/dev/null
  rm -rf "$work/data"; mkdir -p "$work/data"
  "$bin" db migrate >/dev/null
  printf '%s\n' "$DEMO_PASSWORD" | "$bin" user add --admin --password-stdin admin@example.com >/dev/null
  (cd "$work" && setsid nohup "$bin" serve > "$work/serve.log" 2>&1 &)
  until curl -sf -o /dev/null "$KERA_URL/"; do sleep 1; done
  seed setup
  seed chats
  for n in 1 2; do
    # the hostname is what the run page shows as the machine
    docker run -d --name demo-runner-$n --hostname runner-$n --network host -e KERA_URL -e "KERA_RUNNER_TOKEN=$(cat "$work/runner-$n.token")" \
      -v "$bin:/usr/local/bin/kera:ro" "$image" >/dev/null
  done
  sleep 6
  # each pipeline: backdated history, one real run, and that run moves to its timetable slot; run ids stay together per pipeline
  for p in nightly-report ticket-digest invoice-check; do
    seed history "$p"; seed run "$p" >/dev/null; seed shift "$p"
  done
  seed history docs-sync
  # the newest docs-sync run waits for a free runner: the runners are paused for a moment after it is queued
  docker pause demo-runner-1 demo-runner-2 >/dev/null
  id=$(seed run docs-sync --nowait)
  sleep 42
  docker unpause demo-runner-1 demo-runner-2 >/dev/null
  seed wait docs-sync "$id"; seed shift docs-sync
  seed history kb-refresh
  seed schedule-on
  seed run kb-refresh --nowait > "$work/kb-run"   # left running, for the "running" shots
  date +%s > "$work/kb-started"
}

# shot NAME WIDTH HEIGHT URL [CROP_HEIGHT_PX] [WAIT_SECONDS]: a retina screenshot of a page, cut to CROP_HEIGHT_PX (device pixels,
# to leave out empty space below the content), halved to WIDTH css pixels (a 390 px wide phone is kept at 780) and
# reduced to 256 colours so each file stays small. No other tool is needed.
shot() {
  local name=$1 w=$2 h=$3 url=$4 crop=${5:-} wait=${6:-4} out
  out=$((w > 400 ? w : w * 2))
  ab set viewport "$w" "$h" 2
  ab open "$url"
  sleep "$wait"
  agent-browser --session "$session" screenshot "$work/raw.png" > /dev/null
  [ -n "$crop" ] && convert "$work/raw.png" -crop "$((w * 2))x$crop+0+0" +repage "$work/raw.png"
  convert "$work/raw.png" -filter Lanczos -resize "${out}x" -colors 256 -dither None -strip "$images/$name.png"
}

latest() { psql_ -tAc "SELECT max(r.id) FROM pipeline_run r JOIN pipeline p ON p.id = r.pipeline_id WHERE p.name = '$1'"; }
psql_() { docker exec -i "$pg" psql -U kera -d "$db" -v ON_ERROR_STOP=1 -q "$@"; }

shots() {
  local base=$KERA_URL/admin/pipelines kb docs bad digest
  mkdir -p "$images"
  ab set viewport 1440 700 2
  ab open "$KERA_URL/auth"; sleep 2
  ab fill 'input[type=email]' "$DEMO_EMAIL"; ab fill 'input[type=password]' "$DEMO_PASSWORD"; ab press Enter; sleep 3
  # the "running" shots want kb-refresh about two minutes in, on its embed step
  until [ $(( $(date +%s) - $(cat "$work/kb-started") )) -ge 125 ]; do sleep 2; done
  kb=$(cat "$work/kb-run")
  shot pipelines-overview-light 1440 700 "$base"
  shot pipeline-run-live-light 1440 1330 "$base/kb-refresh/$kb"
  docs=$(latest docs-sync); bad=$(latest invoice-check); digest=$(latest ticket-digest)
  shot pipeline-run-ok-light 1440 1140 "$base/docs-sync/$docs"
  ab set media dark; sleep 1
  agent-browser --session "$session" screenshot "$work/raw.png" > /dev/null
  convert "$work/raw.png" -filter Lanczos -resize 1440x -colors 256 -dither None -strip "$images/pipeline-run-ok-dark.png"
  ab set media light
  shot pipeline-run-failed-light 1440 1430 "$base/invoice-check/$bad"
  shot pipeline-env-light 1440 740 "$base/docs-sync?tab=env"
  shot pipeline-runners-light 1440 420 "$base?runners"
  shot pipeline-schedule-light 1440 1000 "$base/docs-sync/edit" 1872
  shot pipeline-run-mobile-light 390 1500 "$base/docs-sync/$docs" 2240
  # the notebook: run every cell in a live session on a runner, then look at the cells and their output
  ab set viewport 1440 1560 2; ab open "$base/ticket-digest?tab=script"; sleep 4
  ab find role button click --name "Run all"
  sleep 45
  agent-browser --session "$session" screenshot "$work/raw.png" > /dev/null
  convert "$work/raw.png" -crop 2880x2860+0+0 +repage -filter Lanczos -resize 1440x -colors 256 -dither None -strip "$images/pipeline-notebook-light.png"
  # the stored secrets, once kb-refresh has finished and moved to its slot (so "last used" reads sensibly)
  seed wait kb-refresh "$kb"; seed shift kb-refresh
  shot pipeline-secrets-light 1440 800 "$base?secrets"
  # a run nobody has picked up: both runners stopped, one run queued, removed again afterwards
  docker stop demo-runner-1 demo-runner-2 > /dev/null
  id=$(seed run ticket-digest --nowait)
  shot pipeline-run-queued-light 1440 1000 "$base/ticket-digest/$id" 1760 25
  psql_ -c "DELETE FROM pipeline_run WHERE id = $id" > /dev/null
  docker start demo-runner-1 demo-runner-2 > /dev/null
}

case "${1:-all}" in
  clean) clean ;;
  data) data ;;
  shots) shots ;;
  all) data; shots; clean ;;
  *) sed -n 2,4p "$0"; exit 1 ;;
esac
