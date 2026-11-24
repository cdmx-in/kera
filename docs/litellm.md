# LiteLLM with Kera

[LiteLLM](https://docs.litellm.ai) is one OpenAI-compatible gateway in front of OpenAI, Anthropic, Ollama, vLLM and more. Put it between Kera and your models and you get one place for keys and model names, plus a spend log that Admin > Spend reads.

The Docker Compose stack in `deploy/` has an opt-in profile that runs LiteLLM and wires it to Kera, so there is nothing to click.

## Start it

```sh
cd deploy
./init.sh litellm          # writes .env with random secrets and turns the profile on
docker compose up -d
```

Add `ollama` (`./init.sh ollama litellm`) if you have no Ollama of your own. The first `up` takes about a minute once the images are downloaded; LiteLLM prepares its database before it reports ready. Then sign in as described in [deploy.md](deploy.md) and chat: the models of `deploy/litellm/config.yaml` are there, and Admin > Spend shows who used what.

Already have a `.env`? Add `LITELLM_MASTER_KEY=sk-<random>`, `LITELLM_DB_PASSWORD=<random letters and digits>` and `COMPOSE_PROFILES=litellm` to it.

## What the profile sets up

| Piece | What it does |
|---|---|
| `litellm` | The gateway, pinned to one release. It reads `deploy/litellm/config.yaml` and listens on `127.0.0.1:4000` (change with `LITELLM_PORT`). |
| `litellm-db` | Its own Postgres for the spend log, in the `litellm_pgdata` volume. |
| `litellm-setup` | Runs `kera apply -f deploy/litellm/kera.yaml` on every `docker compose up`, then exits. Running it again changes nothing. |

`deploy/litellm/kera.yaml` is what the setup step applies:

- a connection `litellm` (`http://litellm:4000/v1`, your master key). Kera asks LiteLLM for each model's kind, so chat and embedding models land in the right lists.
- forwarding of the user's email in the `X-LiteLLM-Customer-Id` header. LiteLLM files every call under that value (`end_user` in its spend log), so Admin > Spend shows one row per person, joined to the names Kera knows. Calls without a user, such as embeddings while indexing, show as "Unattributed".
- the spend source: LiteLLM's database, read only, so Admin > Spend works without any setting.
- the embedding model for Knowledge (`nomic-embed-text` from `config.yaml`).
- every signed-in user may use the LiteLLM models (`base_models_public`).

Edit the file, run `docker compose up -d` again, and the change is applied. These settings show as "Managed by the config file" in the admin console, so change them in the file.

## Choose your models

Edit `deploy/litellm/config.yaml`, then `docker compose restart litellm`. Each `model_name` is the name people see in Kera. The file ships with a small Ollama chat model and an embedding model; the commented examples show OpenAI and Anthropic, with their keys in `deploy/.env` (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`). Mark an embedding model with `model_info: {mode: embedding}`.

If you use your own Ollama instead of the `ollama` profile, use the names from `ollama list`. Ollama on the host must listen on an address the container can reach (see [deploy.md](deploy.md)).

## Spend page

- Figures come from LiteLLM, which writes a row a few seconds after each call. Days are UTC.
- Local models cost nothing, so their rows show requests and tokens with `$0.00`. Add `input_cost_per_token` and `output_cost_per_token` to a model's `litellm_params` to price it.
- "Per model" shows the model LiteLLM called (for example `ollama_chat/llama3.2:1b`).
- Without a spend source the page explains how to set one up. The source is also editable in Admin > Settings > Spend.

## Using a LiteLLM you already run

1. In Admin > Settings > Connections add an OpenAI-compatible connection to it (base URL ending in `/v1`, your key), and under "Forward the user as headers" send the email in `X-LiteLLM-Customer-Id`. LiteLLM treats that header as the end user without further settings. To use another header name, point LiteLLM's `general_settings.user_header_mappings` (older releases: `user_header_name`) at it.
2. In Admin > Settings > Spend choose "LiteLLM database (Postgres)" and give a connection string for LiteLLM's database, for example `postgres://user:password@host:5432/litellm`. Kera only reads it (`default_transaction_read_only`). Give it a read-only database user if you can.
3. For Knowledge, set the embedding connection and model in Admin > Settings > Retrieval.

Or put the same in a config file for `kera apply -f` (see `deploy/litellm/kera.yaml`).

## Good to know

- Models behind LiteLLM count as external for guardrails: `guardrails.local_exempt` and the "On server" badge only apply to Ollama connections that Kera reaches directly.
- The profile replaces the direct Ollama connection that `OLLAMA_BASE_URL` creates; list the models you want in `config.yaml`.
- `docker compose down -v` deletes the spend log along with everything else. `down` alone keeps it.

## Troubleshooting

| Symptom | Check |
|---|---|
| `docker compose up` stops with "dependency failed to start" | `docker compose logs litellm litellm-setup`. An empty `LITELLM_MASTER_KEY` or `LITELLM_DB_PASSWORD` is named in the log; run `./init.sh` or fill them in. |
| No models in Kera | `docker compose logs litellm-setup` should end with "unchanged" or "to create" lines. In Admin > Settings > Connections press Test on the LiteLLM connection. |
| A model answers "connection refused" or "not found" | LiteLLM cannot reach the model server, or the name in `config.yaml` is not pulled (`ollama list`). Check `docker compose logs litellm`. |
| Admin > Spend is empty | Chat once and wait a few seconds. If the page says it cannot read LiteLLM's spend log, LiteLLM has not finished its first start, or the database URL in Admin > Settings > Spend is wrong. |
| Rows show "Unattributed" instead of a person | The connection must forward the email header; see Using a LiteLLM you already run. |
