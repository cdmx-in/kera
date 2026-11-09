#!/usr/bin/env bash
# Deploy Kera from this checkout: test, build the image, recreate the container, apply the config.
#   ./deploy.sh            full deploy
#   ./deploy.sh --skip-tests
#   ./deploy.sh --no-apply  skip `kera apply` (code-only change)
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd .. && pwd)   # docker-compose.yml lives one level up
tests=1 apply=1
for a in "$@"; do
  case $a in
    --skip-tests) tests=0 ;;
    --no-apply) apply=0 ;;
    *) echo "unknown flag: $a" >&2; exit 2 ;;
  esac
done

if [ $tests = 1 ]; then
  go vet ./...
  go test ./...
  (cd web && pnpm exec tsc --noEmit -p . && pnpm exec vitest run)
fi

cd "$ROOT"
docker compose build kera
docker compose up -d --no-deps kera

# wait for the new container to answer (up to 60 s)
for _ in $(seq 30); do
  docker exec kera kera version >/dev/null 2>&1 && break
  sleep 2
done
docker exec kera kera version

if [ $apply = 1 ]; then
  docker exec kera kera apply -f /config/config.yaml
fi
echo "deployed: $(git -C "$ROOT/kera" rev-parse --short HEAD)$(git -C "$ROOT/kera" diff --quiet || echo '+dirty')"
