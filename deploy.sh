#!/usr/bin/env bash
# Deploy Kera from this checkout: test, build the image, recreate the container, apply the config.
#   ./deploy.sh            full deploy (refuses uncommitted changes)
#   ./deploy.sh --skip-tests
#   ./deploy.sh --no-apply  skip `kera apply` (code-only change)
#   ./deploy.sh --dirty     deploy uncommitted changes anyway (the version says +dirty)
# Rollback: docker tag kera:prev kera:local && docker compose up -d --no-deps kera
#   (from the compose directory; migrations only go forward, so check the release's migrations first)
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd .. && pwd)   # docker-compose.yml lives one level up
tests=1 apply=1 dirty=0
for a in "$@"; do
  case $a in
    --skip-tests) tests=0 ;;
    --no-apply) apply=0 ;;
    --dirty) dirty=1 ;;
    *) echo "unknown flag: $a" >&2; exit 2 ;;
  esac
done

if [ $dirty = 0 ] && [ -n "$(git status --porcelain)" ]; then
  echo "uncommitted changes; commit them or pass --dirty" >&2
  exit 1
fi
version=$(git describe --tags --always --dirty)

if [ $tests = 1 ]; then
  make test   # vet, race tests incl. database tests (make testdb), typecheck, vitest
fi

cd "$ROOT"
if docker image inspect kera:local >/dev/null 2>&1; then
  docker tag kera:local kera:prev
fi
docker compose build --build-arg VERSION="$version" kera
docker compose up -d --no-deps kera

# wait until the new container is ready: database reachable, migrations applied (up to 90 s)
for _ in $(seq 45); do
  docker exec kera kera health >/dev/null 2>&1 && break
  sleep 2
done
docker exec kera kera health
docker exec kera kera version

if [ $apply = 1 ]; then
  docker exec kera kera apply -f /config/config.yaml
fi
echo "deployed: $version (previous image kept as kera:prev)"
