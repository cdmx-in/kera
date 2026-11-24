# Run Kera with Docker Compose

## Start

```sh
git clone https://github.com/cdmx-in/kera.git && cd kera/deploy
KERA_ADMIN_EMAIL=you@example.com ./init.sh     # writes .env with random secrets; prints your first password once
docker compose up -d
```

Open http://localhost:8080 and sign in with that email and the printed password. The first start takes about a minute (Postgres sets itself up); `docker compose ps` shows `healthy` when Kera is ready.

Without `KERA_ADMIN_EMAIL`, `./init.sh` writes the same `.env` and prints how to get an admin. Before the first start you can delete `.env` and run it again with the variable. At any time while Kera has no users, you can add `KERA_ADMIN_EMAIL` and `KERA_ADMIN_PASSWORD` (10 or more characters) to `.env` and run `docker compose up -d`. Or run `docker compose exec kera kera user login-link you@example.com` for a one-time sign-in link.

Without `init.sh`: `cp .env.example .env` and fill in `KERA_DB_PASSWORD` and `KERA_SECRET_KEY` (`openssl rand -hex 32`), and for a first admin `KERA_ADMIN_EMAIL` and `KERA_ADMIN_PASSWORD` (10 or more characters). `.env` holds the key that encrypts the API keys Kera stores, so keep it with your backups and out of git.

### The password in `.env`

`KERA_ADMIN_EMAIL` and `KERA_ADMIN_PASSWORD` are read once: at a start where Kera finds no users at all, it creates that admin. Whenever any user exists they are ignored (the log says so), so leaving them in changes nothing, but they do leave a working password lying around:

- `.env` is written readable by you alone (mode 600) and git ignores it, but it is plain text, and so is the container's environment (`docker inspect`, and anyone who can run Docker on the host can read it). Backups of `.env` carry it too.
- After you have signed in, delete the two `KERA_ADMIN_` lines from `.env`, run `docker compose up -d` (it recreates the container without them), and change the password under Settings > Account. The printed one then lives only in your terminal's scrollback.
- If the password never reached `.env` the way you wanted, `kera user passwd` sets another (below).

## Signing in

Out of the box people sign in with an email and a password. There is no sign-up page: an admin creates each account, or a person signs in through your identity provider (below).

- Admin > Users > **Add user**: email, name, role and an initial password. *Generate* makes a strong one to pass on; it is shown once and Kera cannot show it again.
- The key button on a user's row sets a new password and signs them out everywhere.
- Everyone changes their own under Settings > Account (the current password is asked for; their other devices are signed out).
- Admins cannot read anyone's password: Kera keeps only a salted argon2id hash (64 MiB, 3 passes), and no screen or API answer shows it.

On the command line, for scripts or when no admin can get in:

```sh
docker compose exec kera kera user add --name "Ada Lovelace" ada@example.com    # then give them a password:
docker compose exec kera kera user passwd ada@example.com                       # asks twice, nothing is echoed
printf '%s\n' "$PASSWORD" | docker compose exec -T kera kera user passwd --stdin ada@example.com
```

The password is never a command-line argument, so it stays out of the process list and the shell history. Passwords need 10 characters or more (at most 1024 bytes) and may not be the person's email address; there are no rules about digits or symbols.

**Wrong guesses are slowed down.** After 5 wrong tries for one email, or 20 from one address, Kera refuses further tries for 30 seconds, then 1 minute, 2, 4, 8 and at most 15 minutes, and tells the person how long to wait. A good sign-in clears the email's count, and setting a new password for a user clears theirs. The counts are kept in memory, so a restart clears them. The page never says whether an email exists: unknown email, wrong password, no password and a disabled account all answer "Invalid email or password". `docker compose logs kera` has a line per failed try with the email, the address and the reason (never the password).

**Behind a reverse proxy**, the address is the one the proxy reports in `CF-Connecting-IP` or `X-Forwarded-For`, believed only when the proxy connects from a private or loopback address (the usual setup: Caddy or nginx on the same host or Docker network). A proxy that connects to Kera over a public address, such as a CDN straight to the origin, must be listed in `KERA_TRUSTED_PROXIES` (addresses or CIDRs, comma separated), otherwise every visitor looks like the CDN and shares one count.

**Turning it off.** Admin > Settings > Interface > Password sign-in. With no value set, it is on while there is no OAuth sign-in provider and off as soon as you add one, so a team that moves to single sign-on gets no password form it did not ask for. The switch overrides that either way; in a config file it is `auth: {password: {enabled: true}}`. Off really refuses password sign-ins at the server, not just in the page.

**Recovery.** `docker compose exec kera kera user login-link you@example.com` prints a one-time sign-in link that works without a password (12 hours; `--ttl 720h` for longer). It needs shell access to the server, which is why it is safe: use it if the admin password is lost, or after switching every other sign-in method off.

For a team with single sign-on, add a Google or OIDC provider in Admin > Settings > OAuth providers. To let an account you made here sign in through it, turn on `auth.oauth.merge_by_email`.

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

## What it needs

Measured idle: Kera about 15 MB of memory, its Postgres about 50 MB, LiteLLM about 700 MB, Ollama about 3 GB with `llama3.2:3b` loaded (the model sits in memory). Disk: Kera's database starts around 50 MB; the `ollama` profile adds 5 GB for the image and 2.3 GB for the models; LiteLLM's image is 1.4 GB.

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
| "Invalid email or password" for the right password | The account may have no password yet (Admin > Users, key button; or `kera user passwd`), be disabled, or have been created by a single sign-on provider. `docker compose logs kera` names the reason for each failed try. |
| "Too many attempts" | Wait the time it says (30 seconds to 15 minutes), or set a new password for the user in Admin > Users, which clears their count. |
| The admin from `KERA_ADMIN_EMAIL` does not exist | It is created only at a start where Kera has no users; `docker compose logs kera` says "first admin created" or "ignored". Use `kera user add`, or `login-link` to get in. |
| Kera stops at start with `KERA_ADMIN_PASSWORD` in the message | The password is shorter than 10 characters, or the email is not an address; fix `.env` and `docker compose up -d`. |
| `KERA_SECRET_KEY must be at least 32 bytes` in `docker compose logs kera` | Use 64 hex characters: `openssl rand -hex 32`. |
| `port is already allocated` | Set `KERA_PORT=8081` (and `KERA_URL=http://localhost:8081`) in `.env`. |
| Sign-in link or login redirects go to the wrong address | Set `KERA_URL` to the address in the browser's address bar. |
| No models in the picker | Admin: Admin > Settings > Connections > Test shows why. Other users: see the note on models above. |
| A Knowledge upload fails with "model not found" | The embedding model is not pulled: `ollama pull nomic-embed-text` (with the `ollama` profile it is downloaded at first start; watch `docker compose logs -f ollama-pull`). |

Logs: `docker compose logs -f kera`.
