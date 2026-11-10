# Kera

A self-hosted team chat for Ollama and OpenAI-compatible models, shipped as one small Go binary with the web app built in.

> Status: early development (milestone M0). Not ready for production use.

## Goals

- **Small and fast.** One static binary, a container image under 100 MB, idle memory under 100 MB.
- **Postgres is the only required service.** Postgres 18 with pgvector holds users, chats, files metadata, vectors and full-text indexes.
- **Ollama first, any OpenAI-compatible endpoint second.** Including the OpenAI Responses API.
- **Team features.** Google/OIDC sign-in, groups, per-resource access grants, personal API keys, an OpenAI-compatible API.
- **Server-side tools.** OpenAPI and MCP tool servers, with per-user OAuth for services like Google Sheets.
- **Knowledge.** Hybrid search (vectors plus full text) with optional reranking.
- **Code interpreter.** Runs code in a Jupyter sandbox and returns the files it creates.
- **Plugins in any language.** Hooks and tools are HTTP services, not code loaded into the server.
- **Integrations.** Scheduled directory sync from Google Workspace: users, departments as groups, suspended and deleted users disabled (see `docs/integrations.md`).
- **Guardrails.** Personal data redaction or blocking, prompt-injection and blocklist checks and optional moderation on every model call, with an audit trail (see below).

## Quick start (development)

```sh
make testdb                       # Postgres 18 + pgvector on 127.0.0.1:55432
export KERA_DATABASE_URL=postgres://kera:kera-test@127.0.0.1:55432/kera?sslmode=disable
export KERA_SECRET_KEY=$(openssl rand -hex 32)
make build && ./kera serve        # http://localhost:8080
```

Run the tests with `make test` (database tests use the `testdb` container).

## Guardrails

Every call to a model server (chats, background tasks, memory, embeddings) passes through one guard. Settings live under `guardrails.*` (Admin > Settings > Guardrails, or the `guardrails:` section of a config file):

| Key | Modes | What it checks |
|---|---|---|
| `guardrails.pii` | off, log, redact, block | Email, phone, card (Luhn), PAN, Aadhaar (Verhoeff check digit), IBAN (mod 97) and SSN in messages, tool results and embedding texts. Redacted values become `[card]`, `[email]`, ... |
| `guardrails.output_pii` | off, log, redact | The same detectors on the model's reply, with a short hold-back so values split across stream chunks are caught. |
| `guardrails.injection` | off, log, block | Phrases that try to override the system prompt. |
| `guardrails.blocklist` | list of regexes | Case-insensitive; a match in a user message always blocks. |
| `guardrails.moderation` / `.model` | off, log, block | Classifies the latest user message with a moderation model on the same connection (`/v1/moderations`). |
| `guardrails.local_exempt` | bool | Skip everything for local (Ollama) models. |

A blocked call fails with HTTP 400 and code `guardrail_blocked`. Events (rule, action, count, user, model; never the matched text) are listed in the admin console and the `guardrail_event` table. A model preset overrides any key with `features: {guardrails: {pii: off}}`. Hook servers remain the way to add custom checks.

## Layout

| Path | What |
|---|---|
| `cmd/kera` | the binary: `serve`, `db`, `migrate`, `apply`, ... |
| `internal/` | server packages |
| `web/` | React + Vite app, embedded into the binary |
| `sidecars/` | optional helper services (document extraction, reranking) |
| `deploy/` | Docker Compose example |
| `docs/` | documentation, including the Open WebUI compatibility notes |

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
