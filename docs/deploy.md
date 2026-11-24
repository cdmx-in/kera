# Run Kera with Docker Compose

## Start

```sh
git clone https://github.com/cdmx-in/kera.git && cd kera/deploy
KERA_ADMIN_EMAIL=you@example.com ./init.sh     # writes .env with random secrets; prints your first password once
docker compose up -d
```

Open http://localhost:8080 and sign in with that email and the printed password. The first start takes about a minute (Postgres sets itself up); `docker compose ps` shows `healthy` when Kera is ready.

The stack has three services: `kera`, its Postgres `kera-db`, and `extract`, which reads PDF, DOCX, PPTX and ODT uploads and OCRs scanned pages ([Document extraction](#document-extraction)). Kera finds `extract` by itself; there is nothing to set up.

Without `KERA_ADMIN_EMAIL`, `./init.sh` writes the same `.env` and prints how to get an admin. Before the first start you can delete `.env` and run it again with the variable. At any time while Kera has no users, you can add `KERA_ADMIN_EMAIL` and `KERA_ADMIN_PASSWORD` (10 or more characters) to `.env` and run `docker compose up -d`. Or run `docker compose exec kera kera user login-link you@example.com` for a one-time sign-in link.

Without `init.sh`: `cp .env.example .env` and fill in `KERA_DB_PASSWORD` and `KERA_SECRET_KEY` (`openssl rand -hex 32`), and for a first admin `KERA_ADMIN_EMAIL` and `KERA_ADMIN_PASSWORD` (10 or more characters; avoid `$` and `#`, which `.env` files treat specially). `.env` holds the key that encrypts the API keys Kera stores, so keep it with your backups and out of git.

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
- A new password signs the user out of every browser, but **API keys keep working**: they are separate credentials. If an account may have been compromised, tick "Also revoke their API keys" in the Set password dialog, run `kera user passwd --revoke-api-keys <email>`, or have the user revoke them under Settings > Account. Nothing deletes keys unless you ask.
- A password an admin sets, and the one from `KERA_ADMIN_PASSWORD`, is not forced to change at first sign-in. Pass it on once and ask the person to change it under Settings > Account (for the first admin: do it yourself after the first sign-in).

On the command line, for scripts or when no admin can get in:

```sh
docker compose exec kera kera user add --name "Ada Lovelace" ada@example.com    # then give them a password:
docker compose exec kera kera user passwd ada@example.com                       # asks twice, nothing is echoed
printf '%s\n' "$PASSWORD" | docker compose exec -T kera kera user passwd --stdin ada@example.com
```

The password is never a command-line argument, so it stays out of the process list and the shell history. Passwords need 10 characters or more (at most 1024 bytes). Beyond length Kera refuses only what attackers try first: the person's email address, one of a short built-in list of very common passwords and base words (`1234567890`, `password123`, `Welcome2024!`, `P@ssw0rd`), a repeated character or a plain run (`aaaaaaaaaa`, `abababababab`, `qwertyuiop`, `abcdefghij`), and the word `kera` or the email's name with digits around it. There are no rules about digits or symbols, and no breach list: a long phrase of unrelated words is the best choice. `KERA_ADMIN_PASSWORD` has to pass the same rules, or Kera refuses to start and says which rule.

**Wrong guesses are slowed down.** Kera counts tries three ways and refuses further tries for 30 seconds, then 1 minute, 2, 4, 8 and at most 15 minutes, telling the person how long to wait:

- 5 tries for one email from one address (a stranger who guesses your admin's email burns only their own allowance and cannot lock the owner out from elsewhere);
- 30 tries for one email from all addresses together (a guesser with many addresses still runs out; the owner is also held by this one, which takes far more than a couple of strangers);
- 20 tries from one address across all emails (spraying).

A good sign-in clears that email-and-address pair, and setting a new password for a user (Admin > Users) clears their locks for every address. The counts are kept in memory, so a restart clears them; `kera user passwd` runs in another process and cannot clear them on a running server. The page never says whether an email exists: unknown email, wrong password, no password and a disabled account all answer "Invalid email or password". `docker compose logs kera` has a line per failed try with the address and the reason. It names the account when the email belongs to one; any other text typed into the email box (people paste passwords there) appears only as a short fingerprint like `email#1a2b3c4d`, so a log never keeps it. The password itself is never logged.

**Behind a reverse proxy**, tries are counted per client address. A proxy on the same host or Docker network (Caddy, nginx, Traefik) shows up from a loopback or private address; Kera then reads `X-Forwarded-For` from the right, skipping proxies, and takes the address that proxy saw (whatever a client wrote itself sits to the left and is ignored). The proxy must set or append that header: Caddy and Traefik do, nginx needs `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`. Kera does **not** believe `CF-Connecting-IP` from such a proxy, because Caddy, nginx and Traefik pass a client-sent copy through untouched.

A peer you list in `KERA_TRUSTED_PROXIES` (addresses or CIDRs, comma separated) is read the same way: Kera takes its `X-Forwarded-For` from the right, skipping other listed proxies, and Cloudflare appends the visitor's address there. `CF-Connecting-IP` is never read, because other CDNs and proxies pass a client-sent copy through. List only proxies that append the address of the connection they received. If Cloudflare reaches Kera directly over the internet (a proxied DNS record pointing at the server), put its ranges in `.env`, copied from https://www.cloudflare.com/ips/ (these were current when this was written):

```
KERA_TRUSTED_PROXIES=173.245.48.0/20,103.21.244.0/22,103.22.200.0/22,103.31.4.0/22,141.101.64.0/18,108.162.192.0/18,190.93.240.0/20,188.114.96.0/20,197.234.240.0/22,198.41.128.0/17,162.158.0.0/15,104.16.0.0/13,104.24.0.0/14,172.64.0.0/13,131.0.72.0/22,2400:cb00::/32,2606:4700::/32,2803:f800::/32,2405:b500::/32,2405:8100::/32,2a06:98c0::/29,2c0f:f248::/32
```

A Cloudflare Tunnel on the same host needs nothing: it connects from loopback and sets `X-Forwarded-For`. With Caddy between Cloudflare and Kera, give Caddy `trusted_proxies` for Cloudflare so it keeps their `X-Forwarded-For` instead of replacing it. A proxy that is not listed and not private is treated as an ordinary visitor, so every person behind it shares one count.

**Turning it on and off.** Admin > Settings > Interface > Password sign-in. On a fresh install with no OAuth sign-in provider the setting has no value and the form is on. Adding an OAuth login provider (in Admin > Settings > OAuth providers, or with `kera apply`) writes the setting as off for you, and removing the provider later leaves it off, so a team on single sign-on never gets a password form it did not ask for, not even if the provider is dropped by mistake. Upgrading a deployment that already has a login provider does the same. To use passwords next to SSO, or to go back to passwords after dropping SSO, switch it on there; in a config file it is `auth: {password: {enabled: true}}`. An explicit choice is never overwritten. Off really refuses password sign-ins at the server, not just in the page. A change made with `kera apply` or directly in the database reaches the sign-in path within 10 seconds (the settings cache).

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

## Document extraction

The `extract` service ([sidecars/extract](../sidecars/extract/README.md)) turns uploads into text for Knowledge collections and chat attachments: PDF, DOCX, PPTX and ODT, and OCR for PDF pages that are scans (English). Kera reads plain text, code, HTML and `.xlsx` files itself. The old binary Word format `.doc` is not read; save it as `.docx`.

**How Kera finds it.** The compose file gives `kera` the variable `KERA_EXTRACTOR_URL=http://extract:8000`. It is the default of Admin > Settings > Retrieval > Extractor URL, so nothing is stored in the database. A value saved there wins over the default (saving it empty switches extraction off), and Reset goes back to the default. Under the field, a card shows the address in effect, whether the service answers, which types it reads and whether OCR is installed; Check again repeats it. An install that has neither the variable nor a saved value, such as one that keeps its old compose file, behaves as before: those uploads fail with "no document extractor configured for .pdf" and what to do.

**Image, memory, disk.** Compose pulls `ghcr.io/cdmx-in/kera-extract` (`KERA_EXTRACT_IMAGE` changes it; amd64 and arm64): about 113 MB to download, 301 MB on disk. The service publishes no port and keeps no data. Idle it uses about 33 MB with its one worker (about 95 MB with two). Reading a 20-page scan peaked at 205 MB, an 80 MB PDF at 390 MB; the compose file caps it at 1 GB (`deploy.resources.limits.memory`), raise that for bigger files. OCR takes about 3 seconds per dense page on one CPU core; a file gets an 8 minute budget, and blank pages left after it stay unread (the file's `ocr_skipped` meta says how many). Uploads are limited by `files.max_mb` (100 MB).

**Before the image is published for your checkout**, or on a platform it does not cover, `docker compose up -d` tries to pull it, prints one `denied` line, and builds it from `sidecars/extract` instead (about a minute, needs internet for apt and pip). `docker compose build extract` does the same on its own. `docker compose pull` stops with an error in that situation; use `docker compose pull --ignore-buildable` then.

**Turn it off.** Set `KERA_EXTRACTOR_URL=` (empty) in `.env`, then `docker compose up -d` and `docker compose stop extract`. `up -d` starts a stopped service again, so to keep it off for good delete the `extract:` block from the compose file, or start the stack with `docker compose up -d --scale extract=0`. Kera then behaves as an install without it.

**More workers.** `WEB_CONCURRENCY` in the `extract` service's `environment` is the number of uvicorn workers (1 in the compose file, about 45 MB each when idle). Raise it when many people upload at once.

**Another OCR language.** The image has English. Build your own on top of it, here with German (`tesseract-ocr-<code>` is the Debian package of each language):

```dockerfile
# deploy/extract-ocr/Dockerfile
FROM ghcr.io/cdmx-in/kera-extract:latest
USER root
RUN apt-get update && apt-get install -y --no-install-recommends tesseract-ocr-deu && rm -rf /var/lib/apt/lists/*
USER 10001
```

```yaml
# deploy/docker-compose.override.yml (Compose reads it by itself)
services:
  extract:
    image: my-kera-extract
    build: ./extract-ocr
    environment:
      OCR_LANGS: eng+deu
```

`docker compose up -d --build extract` builds and starts it. The card under Extractor URL then says "OCR of scans on (eng+deu)".

**Your own extractor.** Point Extractor URL at any service with the same contract (see the sidecar README) and switch the bundled one off as above.

**When an upload fails.** The failed file shows the reason in Workspace > Knowledge and has a Retry button; once the service answers again, Retry extracts the file and indexes it, with no new upload.

| The file says | Cause and fix |
|---|---|
| `no document extractor configured for .pdf` | No address is set. Update to a compose file that has the `extract` service and run `docker compose up -d`, or set Extractor URL. |
| `document extractor not reachable (its host name does not resolve ...)` | The service is stopped or the address is wrong. `docker compose ps extract`, then `docker compose up -d extract`, and check Extractor URL. |
| `document extractor not reachable (connection refused ...)` | The name resolves but nothing listens there: it is still starting, crashed, or the port is wrong. `docker compose logs extract`. |
| `document extractor not reachable (it closed the connection, it may have run out of memory ...)` | The file needed more than the memory cap and the service was killed (it restarts by itself). Raise the limit, or split the file. |
| `document extractor not reachable (no answer in time)` | No answer within 10 minutes: a stuck service or a huge file. `docker compose logs extract`. |
| `extractor: HTTP 422: could not read .pdf file` | The file is damaged or password-protected. |
| `extractor: HTTP 415: unsupported type .doc` | Save the file as `.docx`. |

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

**Plain http and the published port.** The compose file publishes Kera on every network interface, over plain http, so that a LAN or VPS install works the moment it starts. Passwords typed over plain http cross the network readable: that is fine on `localhost` or a LAN you trust, and not anywhere else. For a server other people reach, put HTTPS in front as above and, when the proxy runs on the same host, set `KERA_BIND=127.0.0.1` in `.env` (then `docker compose up -d`) so that Kera listens only to the proxy. Otherwise anyone who can reach port 8080 can bypass the proxy, its HTTPS and its rules, and sign in over plain http. If the proxy runs in the same compose project or on another machine, bind the port to the address the proxy uses, or do not publish it.

## What it needs

Measured idle: Kera about 20 MB of memory (a burst of sign-ins can take up to 4 x 64 MiB for a moment: each password check holds 64 MiB, and at most 4 run at once, fewer on a machine with fewer cores), its Postgres about 50 MB, the `extract` service about 33 MB (more while it reads a file, see [Document extraction](#document-extraction)), LiteLLM about 700 MB, Ollama about 3 GB with `llama3.2:3b` loaded (the model sits in memory). Disk: Kera's database starts around 50 MB; the `extract` image is 301 MB (about 113 MB to download); the `ollama` profile adds 5 GB for the image and 2.3 GB for the models; LiteLLM's image is 1.4 GB.

## Ports, data, backup

- Published: Kera on `8080` on every network interface (change the port with `KERA_PORT`, the interface with `KERA_BIND`) and, with the `litellm` profile, LiteLLM on `127.0.0.1:4000` (`LITELLM_PORT`). The databases, the `extract` service and Ollama are only reachable inside the stack.
- Volumes: `kera_pgdata` (the database), `kera_data` (uploaded files), `litellm_pgdata`, `ollama_models`. Compose prefixes them with the project name, `deploy_` by default.
- `docker compose down` keeps all data. `docker compose down -v` deletes it.

Back up the database and the uploads, and keep `.env`:

```sh
docker compose exec -T kera-db pg_dump -U kera kera | gzip > kera-db.sql.gz
docker run --rm -v deploy_kera_data:/data -v "$PWD":/backup alpine tar czf /backup/kera-data.tgz -C /data .
```

Upgrade with `docker compose pull && docker compose up -d`; database migrations run on start. An install from before the `extract` service existed gets it by updating the checkout (`git pull`) and running `docker compose up -d`: Kera uses it with no setting changed, and files that failed earlier have a Retry button.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `required variable ... is empty` | Run `./init.sh` in `deploy/`, or fill in `.env`. |
| "Invalid email or password" for the right password | The account may have no password yet (Admin > Users, key button; or `kera user passwd`), be disabled, or have been created by a single sign-on provider. `docker compose logs kera` names the reason for each failed try. |
| "Too many attempts" | Wait the time it says (30 seconds to 15 minutes), or set a new password for the user in Admin > Users, which clears their locks. |
| The admin from `KERA_ADMIN_EMAIL` does not exist | It is created only at a start where Kera has no users; `docker compose logs kera` says "first admin created" or "ignored". Use `kera user add`, or `login-link` to get in. |
| Kera stops at start with `KERA_ADMIN_PASSWORD` in the message | The password is shorter than 10 characters, or the email is not an address; fix `.env` and `docker compose up -d`. |
| `KERA_SECRET_KEY must be at least 32 bytes` in `docker compose logs kera` | Use 64 hex characters: `openssl rand -hex 32`. |
| `port is already allocated` | Set `KERA_PORT=8081` (and `KERA_URL=http://localhost:8081`) in `.env`. |
| Sign-in link or login redirects go to the wrong address | Set `KERA_URL` to the address in the browser's address bar. |
| No models in the picker | Admin: Admin > Settings > Connections > Test shows why. Other users: see the note on models above. |
| A PDF, DOCX, PPTX or ODT upload fails, or the card under Admin > Settings > Retrieval > Extractor URL is red | See the table in [Document extraction](#document-extraction): it lists what the file says and what to do. `docker compose logs extract` shows the service's side. |
| `docker compose pull` stops with `denied` for `kera-extract` | The extractor image is not published for your checkout. `docker compose pull --ignore-buildable`, then `docker compose up -d`, which builds it from `sidecars/extract`. |
| A Knowledge upload fails with "model not found" | The embedding model is not pulled: `ollama pull nomic-embed-text` (with the `ollama` profile it is downloaded at first start; watch `docker compose logs -f ollama-pull`). |

Logs: `docker compose logs -f kera`.
