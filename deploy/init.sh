#!/bin/sh
# Writes deploy/.env with random secrets, once. Usage: ./init.sh [ollama] [litellm] [searxng]
# KERA_SECRET_KEY in it encrypts every stored credential: keep it apart from backups of the data. To keep it out of
# the container's environment, Kera also reads KERA_SECRET_KEY_FILE (see docs/deploy.md, The secret key).
#   ollama   also run a model server in the stack (small chat + embedding model, CPU)
#   litellm  also run the LiteLLM gateway, wired to Kera with spend tracking
#   searxng  also run SearXNG for private web search, wired to Kera (no account or API key)
# Name the first admin to get a password for them, printed once at the end:
#   KERA_ADMIN_EMAIL=you@example.com ./init.sh
# Set KERA_URL first when Kera will not be opened at http://localhost:8080:
#   KERA_URL=https://chat.example.com ./init.sh
set -eu
cd "$(dirname "$0")"
rand() { head -c "$1" /dev/urandom | od -An -vtx1 | tr -d ' \n'; }
# 20 random characters without look-alikes (no 0 O 1 l I), in groups of five: kP7mQ-x3Rtn-bW9cD-hN4vZ
password() {
  while :; do
    p=$(head -c 512 /dev/urandom | LC_ALL=C tr -dc 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789' | head -c 20)
    [ "${#p}" -eq 20 ] && break
  done
  printf %s "$p" | fold -w 5 | paste -sd- -
}
profiles=""
for p in "$@"; do
  case "$p" in
    ollama | litellm | searxng) profiles="${profiles:+$profiles,}$p" ;;
    *) echo "unknown option '$p' (use: ollama, litellm, searxng)" >&2; exit 1 ;;
  esac
done
admin="${KERA_ADMIN_EMAIL:-}"
case "$admin" in
  "") ;;
  *[!A-Za-z0-9._%+@-]* | @* | *@ | *@*@*) echo "KERA_ADMIN_EMAIL='$admin' is not a plain email address like you@example.com" >&2; exit 1 ;;
  *@*) ;;
  *) echo "KERA_ADMIN_EMAIL='$admin' is not a plain email address like you@example.com" >&2; exit 1 ;;
esac
if [ -e .env ]; then
  echo ".env already exists; leaving it as it is (delete it to start over)."
  if [ -n "$admin" ]; then
    echo "To create a first admin there, add KERA_ADMIN_EMAIL and KERA_ADMIN_PASSWORD (10 or more characters) to .env; see docs/deploy.md."
  fi
  exit 0
fi
umask 077 # .env is for you alone: it holds the key that encrypts stored secrets
pw=""
if [ -n "$admin" ]; then pw=$(password); fi
{
  echo "KERA_DB_PASSWORD=$(rand 12)"
  echo "KERA_SECRET_KEY=$(rand 32)"
  echo "KERA_URL=${KERA_URL:-http://localhost:8080}"
  echo "LITELLM_MASTER_KEY=sk-$(rand 16)"
  echo "LITELLM_DB_PASSWORD=$(rand 12)"
  case ",$profiles," in *,ollama,*) echo "OLLAMA_BASE_URL=http://ollama:11434" ;; esac
  if [ -n "$profiles" ]; then echo "COMPOSE_PROFILES=$profiles"; fi
  if [ -n "$admin" ]; then
    echo "# Creates the first admin at the first start, only while Kera has no users. Delete both lines afterwards."
    echo "KERA_ADMIN_EMAIL=$admin"
    echo "KERA_ADMIN_PASSWORD=$pw"
  fi
} > .env
echo "Wrote deploy/.env (random secrets, readable only by you). Next: docker compose up -d"
if [ -n "$admin" ]; then
  echo
  echo "First admin:  $admin"
  echo "Password:     $pw"
  echo
  echo "Shown once here. Open the address in KERA_URL and sign in with them; change the password under Settings > Account."
  echo "Both are also in .env until you delete the two KERA_ADMIN_ lines, which Kera uses only at the first start."
  echo "Delete them after the first sign-in; the first password is not forced to change, so change it too."
else
  echo "No admin yet. For one with a password, delete .env and run again before the first start: KERA_ADMIN_EMAIL=you@example.com ./init.sh"
  echo "Or after the first start: docker compose exec kera kera user login-link you@example.com"
fi
case "${KERA_URL:-http://localhost:8080}" in
  https://*) ;;
  *)
    echo
    echo "Heads-up: signing in over plain http is fine on localhost or a trusted LAN. Anywhere else put HTTPS in front (docs/deploy.md), and set KERA_BIND=127.0.0.1 in .env when the proxy runs on this host."
    ;;
esac
