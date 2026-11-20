# Kera

[![ci](https://github.com/cdmx-in/kera/actions/workflows/ci.yml/badge.svg)](https://github.com/cdmx-in/kera/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

A self-hosted AI chat for teams, built for Ollama and any OpenAI-compatible model. It ships as one small Go binary with the web app built in, and Postgres is the only service it needs.

Kera runs CodeMax's own internal AI workspace in production every day.

## Why Kera

- **Small and fast.** One static binary, a container image under 100 MB, idle memory under 100 MB.
- **One dependency.** Postgres 18 with pgvector holds users, chats, file metadata, vectors and full-text indexes. You don't need Redis, a vector database or a queue.
- **Local first.** Ollama models are first-class. Any OpenAI-compatible endpoint works too, including the Responses API.
- **Plugins in any language.** Hooks and tools are HTTP services (OpenAPI or MCP), not code loaded into the server.

## Features

- **Chat.** Streaming Markdown, maths, Mermaid diagrams, token counts, thumbs up/down, Markdown export, folders, archive, temporary chats, slash-command prompts, and dictation that runs on the device.
- **Team management.** Google or any OIDC sign-in, groups, per-resource access grants, personal API keys, and an OpenAI-compatible API.
- **Assistants.** Model presets with their own prompt, tools, knowledge and skills, managed in a workspace or as config (`kera apply -f config.yaml`).
- **Knowledge.** Hybrid search (vectors plus full text) with optional reranking. Uploads are parsed by an extraction sidecar, and scanned PDFs are OCR'd.
- **Tools.** OpenAPI and MCP tool servers, with per-user OAuth for services like Google Sheets, Gmail and Docs. Built-in web search (SearXNG, Brave or DuckDuckGo).
- **Code interpreter.** Runs Python, Node.js, TypeScript, Bash, PHP, Ruby, Go, Java and R in a Jupyter sandbox, and returns the files and images the code creates.
- **Cowork.** A task mode in which the model drives a sandboxed Linux desktop (browser and shell). You watch it live and can take over at any time.
- **Memory.** Automatic memory capture, which users manage in Settings.
- **Guardrails.** Personal-data redaction or blocking, prompt-injection and blocklist checks, and optional moderation on every model call, with an audit trail (see below).
- **Integrations.** Scheduled directory sync from Google Workspace: users come in, departments become groups, and suspended or deleted users are disabled (see [docs/integrations.md](docs/integrations.md)).
- **Branding.** Your own name, logos and colours, set from the admin console.

## Quick start

With Docker:

```sh
cd deploy
cp .env.example .env              # set KERA_SECRET_KEY (openssl rand -hex 32) and KERA_DB_PASSWORD
docker compose up -d
docker compose exec kera kera user login-link you@example.com   # open the printed link
```

On an empty install, `login-link` creates that first user as admin. After that, add a Google or OIDC sign-in provider in Admin > Settings. To let your account sign in through it, turn on `auth.oauth.merge_by_email`.

The compose file expects Ollama on the host at port 11434. Set `OLLAMA_BASE_URL` to point it elsewhere, or add OpenAI-compatible connections in Admin > Settings.

From source (Go 1.26, Node 24, pnpm):

```sh
make testdb                       # Postgres 18 + pgvector on 127.0.0.1:55432
export KERA_DATABASE_URL=postgres://kera:kera-test@127.0.0.1:55432/kera?sslmode=disable
export KERA_SECRET_KEY=$(openssl rand -hex 32)
make build && ./kera serve        # http://localhost:8080
```

Run the tests with `make test`. The database tests use the `testdb` container.

## Guardrails

Every call to a model server (chats, background tasks, memory, embeddings) goes through one guard. Its settings live under `guardrails.*`, set in Admin > Settings > Guardrails or in the `guardrails:` section of a config file:

| Key | Modes | What it checks |
|---|---|---|
| `guardrails.pii` | off, log, redact, block | Email, phone, card (Luhn), PAN, Aadhaar (Verhoeff check digit), IBAN (mod 97) and SSN in messages, tool results and embedding texts. Redacted values become `[card]`, `[email]`, ... |
| `guardrails.output_pii` | off, log, redact | The same detectors on the model's reply. A short hold-back catches values split across stream chunks. |
| `guardrails.injection` | off, log, block | Phrases that try to override the system prompt. |
| `guardrails.blocklist` | list of regexes | Case-insensitive; a match in a user message always blocks. |
| `guardrails.moderation` / `.model` | off, log, block | Classifies the latest user message with a moderation model on the same connection (`/v1/moderations`). |
| `guardrails.local_exempt` | bool | Skips every check for local (Ollama) models. |

A blocked call fails with HTTP 400 and the code `guardrail_blocked`. Each event records the rule, action, count, user and model, never the matched text. Events are listed in the admin console and in the `guardrail_event` table. A model preset can override any key with `features: {guardrails: {pii: off}}`. For custom checks, use a hook server.

## Layout

| Path | What |
|---|---|
| `cmd/kera` | the binary: `serve`, `db`, `migrate`, `apply`, `health`, ... |
| `internal/` | server packages, one per feature |
| `web/` | React + Vite app, embedded into the binary |
| `sidecars/` | optional helper services: document extraction, reranking, Cowork desktop |
| `deploy/` | Docker Compose example |
| `docs/` | documentation |

## Contributing

Issues and pull requests are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) first (DCO sign-off, clean-room rule). Report security problems privately as described in [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Built and maintained by [CodeMax IT Solutions Pvt. Ltd.](https://cdmx.in)
