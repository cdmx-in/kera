# Open WebUI compatibility contract

Kera serves a subset of the Open WebUI HTTP API so existing scripts and clients keep working after migrating. This document is written from observed request and response shapes, in our own words. Implement compatibility routes from this file, never from Open WebUI source code (see CONTRIBUTING.md).

Status legend: **planned**, **done**.

## Authentication

Every route accepts `Authorization: Bearer <token>` where the token is either a session token or a personal API key starting with `sk-`. Browser sessions also send the `kera_session` cookie. Errors use `{"detail": "<message>"}` with the HTTP status.

## OpenAI-compatible

| Route | Status | Notes |
|---|---|---|
| `GET /api/models` | planned | `{"data": [model...]}`. Each model has `id`, `name`, `object: "model"`, `created`, `owned_by`, and `info: {meta: {description, capabilities, hidden, suggestion_prompts, knowledge, toolIds}, params}`. Hidden models are listed for API clients. |
| `POST /api/chat/completions` | planned | OpenAI request body. Extensions: `chat_id`, `id` (assistant message id), `parent_id`, `files: [{type: "file"\|"collection", id}]`, `features`, `temporary`. The model preset's prompt, parameters, knowledge and tools apply. When the body carries `tools`, those are passed through to the model and tool calls are returned to the caller. |

## Files and knowledge

| Route | Status | Notes |
|---|---|---|
| `POST /api/v1/files/` | planned | multipart field `file`. Returns `{id, filename, meta: {name, content_type, size}, data: {status}}`. |
| `GET /api/v1/files/{id}` | planned | `data.status` moves `pending` -> `processing` -> `completed` or `failed` (`data.error`). Clients poll this. |
| `GET /api/v1/files/{id}/content` | planned | raw bytes; `?attachment=true` forces download. |
| `DELETE /api/v1/files/{id}` | planned | |
| `GET /api/v1/knowledge/` | planned | `{items: [...], total}`. |
| `POST /api/v1/knowledge/create` | planned | `{name, description}`. |
| `GET /api/v1/knowledge/{id}/files` | planned | `{items: [{id, filename, meta, data: {status}}], total}`. |
| `POST /api/v1/knowledge/{id}/file/add` | planned | `{file_id}`; accepts a file still being processed. |
| `POST /api/v1/knowledge/{id}/file/remove` | planned | `{file_id}`. |
| `POST /api/v1/retrieval/query/collection` | planned | `{collection_names, query, k}` -> `{documents: [[...]], metadatas: [[...]], distances: [[...]]}`. |

## Chats, models, users, feedback

| Route | Status | Notes |
|---|---|---|
| `POST /api/v1/chats/new` | planned | transition support for clients that create a chat, run a completion with `chat_id`, poll and delete. |
| `GET /api/v1/chats/{id}` | planned | `{id, title, chat: {history: {currentId, messages: {id: {id, parentId, childrenIds, role, content, model, timestamp, done, files, sources}}}}}`. |
| `DELETE /api/v1/chats/{id}` | planned | |
| `GET /api/v1/models/model?id=` | planned | model with `meta.toolIds` and `meta.knowledge`. |
| `POST /api/v1/models/model/update?id=` | planned | partial update of name, description, meta, params. |
| `GET /api/v1/auths/` | planned | current user with permissions. |
| `GET/POST /api/v1/users/default/permissions` | planned | admin. |
| `POST /api/v1/evaluations/feedback` | planned | `{type, data: {rating, model_id, comment}, meta, snapshot}`. |
| `GET /api/version` | done | `{version}`. |

## Database import (`kera migrate from-openwebui`)

Observed source schema (0.13, Postgres) and how `internal/importowui` maps it. Status: **done**.

| Source | Meaning | Kera |
|---|---|---|
| `user` | `created_at`/`updated_at`/`last_active_at` epoch seconds; `oauth` = `{"<provider>": {"sub": ...}}`; `settings` JSON; avatar may be an inline `data:` URL | `user` (id kept; merged into an existing user with the same email or OAuth subject; data URLs over 64 KB dropped) |
| `api_key` | plaintext `sk-` key in `key`, optional `expires_at` | `api_key` with `key_hash = sha256(key)` and the 10-char prefix, so the same key keeps working; expired keys skipped |
| `auth`, `oauth_session` | password hashes, encrypted provider tokens | not imported (users sign in again; tool-server OAuth is reconnected) |
| `group`, `group_member` | `permissions` nested booleans | `group` (known keys kept, `features.memories` -> `features.memory`, others reported), `group_member` |
| `access_grant` | `resource_type` in model, knowledge, prompt, tool, shared_chat, ...; `principal_id` `*` = everyone | `access_grant` for model, knowledge, prompt, folder, chat; other types skipped and reported |
| `chat` | `chat.history.messages` = map of message id -> `{parentId, childrenIds, role, content, model, timestamp, done, files, sources, statusHistory, usage, error, followUps, output}`, `chat.history.currentId` = active leaf; `meta.tags` = tag ids | `chat` + one `message` row per entry (`parent_id` tree; `current_message_id` moved down to a leaf if needed), `chat_tag` (the tag's name from the `tag` table) |
| `chat_message` | per-message copy, id `<chat id>-<message id>`; `output` = ordered items (`message`, `reasoning`, `function_call`, `function_call_output`, `web_search_call`, a context-compaction item) | `message.parts` |
| assistant `content` | may embed `<details type="reasoning">` (summary + `> `-quoted text) and `<details type="tool_calls" id name arguments result files>` blocks (attributes HTML-escaped, values JSON-encoded) | blocks become `reasoning`, `tool_call`, `tool_result` parts and are removed from `content`; other `<details>` stay in the text |
| `shared_chat` | `id` equals `chat.share_id`; `chat` = snapshot at share time | `shared_chat` (snapshot converted to `{title, currentId, messages}`) |
| `file` | `path` = `<uploads>/<id>_<name>`; `meta` = `{name, content_type, size, data}`; `data.content` = extracted text | `file` (blob copied to `uploads/<id>/<name>`, sha256; text -> `completed`, else `pending`; missing blob -> `failed`) |
| `knowledge`, `knowledge_file` | collections and their files | `knowledge`, `knowledge_file` as `pending` with 0 chunks (indexed later) |
| `folder`, `chat_file`, `memory`, `prompt`, `feedback` | as named | same tables (memory content only, no vectors) |
| `model` | `params.system` = preset system prompt | not imported; `--export-presets` writes the prompts |
| `config` | one row per dotted key | not imported; `--report-config` prints `rag.* web.* task.* ui.* user.permissions tool_server.connections` with secrets removed |
