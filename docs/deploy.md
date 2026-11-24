# Run Kera with Docker Compose

## Start

```sh
cd deploy
./init.sh                  # writes .env with random secrets
docker compose up -d
docker compose exec kera kera user login-link you@example.com   # prints a sign-in link
```

Open the link. On an empty install this creates `you@example.com` as the first admin. The first start takes about a minute (Postgres sets itself up); `docker compose ps` shows `healthy` when Kera is ready.

Without `init.sh`: `cp .env.example .env` and fill in `KERA_DB_PASSWORD` and `KERA_SECRET_KEY` (`openssl rand -hex 32`). `.env` holds the key that encrypts the API keys Kera stores, so keep it with your backups and out of git.

## Signing in

There is no password login. The first admin comes from `login-link`, which needs shell access to the server, so nobody can claim the admin role from the network. The link signs you in for 12 hours (`--ttl 720h` for longer; run the command again for a new one).

For a team, add a Google or OIDC provider in Admin > Settings > OAuth providers. Without one, create each person yourself and give them a link:

```sh
docker compose exec kera kera user add --name "Ada Lovelace" ada@example.com
docker compose exec kera kera user login-link ada@example.com
```

New users see no models until you share them: turn on Admin > Settings > Interface > Base models public, or share models one by one in Workspace > Models. (The `litellm` profile turns it on for you.)

## Models

Pick one:

| You have | Do |
|---|---|
| Nothing yet | `./init.sh ollama`. Adds an Ollama server to the stack and downloads a small chat model (`llama3.2:3b`) and an embedding model (`nomic-embed-text`). About 3.4 GB to download for the server image (5 GB on disk) and 2.3 GB for the models, once. It runs on the CPU, so answers are slow; add a GPU as described in `docker-compose.yml`. |
| Ollama on this machine | Nothing: `OLLAMA_BASE_URL` points at `host.docker.internal:11434`. On Linux Ollama listens on `127.0.0.1` only, which containers cannot reach: start it with `OLLAMA_HOST=0.0.0.0` (a systemd override for the `ollama` service) and keep the port closed to the network. Pull an embedding model for Knowledge: `ollama pull nomic-embed-text`. |
| OpenAI, Anthropic, vLLM or a gateway | Add a connection in Admin > Settings > Connections, or run [LiteLLM](litellm.md) with `./init.sh litellm`. |

The profiles add up: `./init.sh ollama litellm`. They are stored as `COMPOSE_PROFILES` in `.env`.

## Config as code

Anything in Admin > Settings can also live in a YAML file. `docker compose exec kera kera export` prints the current configuration (secrets as `${NAME}`). Apply a file, after a dry run:

```sh
docker compose run --rm -v "$PWD/my.yaml:/etc/kera/my.yaml:ro" kera apply -f /etc/kera/my.yaml --dry-run
docker compose run --rm -v "$PWD/my.yaml:/etc/kera/my.yaml:ro" kera apply -f /etc/kera/my.yaml
```

Applying the same file again changes nothing. `deploy/litellm/kera.yaml` is a working example. What a file sets shows as managed by the config file in the admin console.

## Another address, HTTPS

Set `KERA_URL` in `.env` to the address people open (sign-in links and OAuth redirects are built from it), then `docker compose up -d`. Put a reverse proxy in front for HTTPS. With Caddy, which gets the certificate itself:

```
chat.example.com {
    reverse_proxy localhost:8080
}
```

With nginx, answers stream, so turn buffering off for the location: `proxy_pass http://127.0.0.1:8080; proxy_buffering off; proxy_read_timeout 3600s;` and pass the usual `Host` and `X-Forwarded-*` headers.

## Ports, data, backup

- Published: Kera on `8080` (change with `KERA_PORT`) and, with the `litellm` profile, LiteLLM on `127.0.0.1:4000` (`LITELLM_PORT`). The databases and Ollama are only reachable inside the stack.
- Volumes: `kera_pgdata` (the database), `kera_data` (uploaded files), `litellm_pgdata`, `ollama_models`. Compose prefixes them with the project name, `deploy_` by default.
- `docker compose down` keeps all data. `docker compose down -v` deletes it.

Back up the database and the uploads, and keep `.env`:

```sh
docker compose exec -T kera-db pg_dump -U kera kera | gzip > kera-db.sql.gz
docker run --rm -v deploy_kera_data:/data -v "$PWD":/backup alpine tar czf /backup/kera-data.tgz -C /data .
```

Upgrade with `docker compose pull && docker compose up -d`; database migrations run on start.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `required variable ... is empty` | Run `./init.sh` in `deploy/`, or fill in `.env`. |
| `KERA_SECRET_KEY must be at least 32 bytes` in `docker compose logs kera` | Use 64 hex characters: `openssl rand -hex 32`. |
| `port is already allocated` | Set `KERA_PORT=8081` (and `KERA_URL=http://localhost:8081`) in `.env`. |
| Sign-in link or login redirects go to the wrong address | Set `KERA_URL` to the address in the browser's address bar. |
| No models in the picker | Admin: Admin > Settings > Connections > Test shows why. Other users: see Signing in. |
| A Knowledge upload fails with "model not found" | The embedding model is not pulled: `ollama pull nomic-embed-text` (with the `ollama` profile it is downloaded at first start; watch `docker compose logs -f ollama-pull`). |

Logs: `docker compose logs -f kera`.
