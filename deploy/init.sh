#!/bin/sh
# Writes deploy/.env with random secrets, once. Usage: ./init.sh [ollama] [litellm]
#   ollama   also run a model server in the stack (small chat + embedding model, CPU)
#   litellm  also run the LiteLLM gateway, wired to Kera with spend tracking
# Set KERA_URL first when Kera will not be opened at http://localhost:8080:
#   KERA_URL=https://chat.example.com ./init.sh
set -eu
cd "$(dirname "$0")"
rand() { head -c "$1" /dev/urandom | od -An -vtx1 | tr -d ' \n'; }
profiles=""
for p in "$@"; do
  case "$p" in
    ollama | litellm) profiles="${profiles:+$profiles,}$p" ;;
    *) echo "unknown option '$p' (use: ollama, litellm)" >&2; exit 1 ;;
  esac
done
if [ -e .env ]; then
  echo ".env already exists; leaving it as it is (delete it to start over)."
  exit 0
fi
umask 077
{
  echo "KERA_DB_PASSWORD=$(rand 12)"
  echo "KERA_SECRET_KEY=$(rand 32)"
  echo "KERA_URL=${KERA_URL:-http://localhost:8080}"
  echo "LITELLM_MASTER_KEY=sk-$(rand 16)"
  echo "LITELLM_DB_PASSWORD=$(rand 12)"
  case ",$profiles," in *,ollama,*) echo "OLLAMA_BASE_URL=http://ollama:11434" ;; esac
  if [ -n "$profiles" ]; then echo "COMPOSE_PROFILES=$profiles"; fi
} > .env
echo "Wrote deploy/.env (random secrets). Next: docker compose up -d"
