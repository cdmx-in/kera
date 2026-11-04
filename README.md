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
- **Migration from Open WebUI.** `kera migrate from-openwebui` imports users, chats, files and knowledge.

## Quick start (development)

```sh
make testdb                       # Postgres 18 + pgvector on 127.0.0.1:55432
export KERA_DATABASE_URL=postgres://kera:kera-test@127.0.0.1:55432/kera?sslmode=disable
export KERA_SECRET_KEY=$(openssl rand -hex 32)
make build && ./kera serve        # http://localhost:8080
```

Run the tests with `make test` (database tests use the `testdb` container).

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
