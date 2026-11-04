# Open WebUI compatibility contract

Kera serves a subset of the Open WebUI HTTP API so existing scripts and clients keep working after migrating. This document is written from observed request and response shapes, in our own words. Implement compatibility routes from this file, never from Open WebUI source code (see CONTRIBUTING.md).

Status legend: **planned**, **done**.

## Authentication

Every route accepts `Authorization: Bearer <token>` where the token is either a session token or a personal API key starting with `sk-`. Browser sessions also send the `kera_session` cookie; a POST/PUT/PATCH/DELETE authenticated only by that cookie is accepted from the same origin only (CSRF), so scripts should use the header. Errors use `{"detail": "<message>"}` with the HTTP status.

## OpenAI-compatible

| Route | Status | Notes |
|---|---|---|
| `GET /api/models` | done | Also `GET /api/v1/models` and `GET /v1/models`. `{"data": [model...]}`. Each model has `id`, `name`, `object: "model"`, `created`, `owned_by` (`ollama` or `openai`), `connection_type` (`local` or `external`), `local`, `hidden`, `features` (enabled feature ids), `defaultFeatures`, `toolIds` (`server:<id>`, `builtin:<name>`), `suggestionPrompts`, `profileImageUrl`, `capabilities`, and `info: {id, base_model_id, name, meta: {description, capabilities, hidden, suggestion_prompts, knowledge, toolIds, builtinTools, profile_image_url}, params}`. Only models the caller may use. Hidden models are listed for API-key callers and with `?include_hidden=true`. Ordered by the `site.model_order` setting, then name. |
| `POST /api/chat/completions` | planned | OpenAI request body. Extensions: `chat_id`, `id` (assistant message id), `parent_id`, `files: [{type: "file"\|"collection", id}]`, `features`, `temporary`. The model preset's prompt, parameters, knowledge and tools apply. When the body carries `tools`, those are passed through to the model and tool calls are returned to the caller. |

## Files and knowledge

| Route | Status | Notes |
|---|---|---|
| `POST /api/v1/files/` | done | multipart field `file`, optional `metadata` (JSON, stored as `meta.data`). Returns `{id, user_id, hash, filename, meta: {name, content_type, size}, data: {status: "pending"}, created_at, updated_at}` (epoch seconds). `?process=false` stores without extraction (status `completed`). Over the size limit (`files.max_mb`, default 100) -> 413. |
| `GET /api/v1/files/` | done | the caller's files, newest first; admins may add `?all=true`. |
| `GET /api/v1/files/{id}` | done | same object; `data.status` moves `pending` -> `processing` -> `completed` or `failed` (`data.error`). Clients poll this. Files the caller cannot read answer 404. |
| `GET /api/v1/files/{id}/process/status` | done | `{status}`. |
| `GET /api/v1/files/{id}/content` | done | raw bytes with Range support. Inline for images, PDF and plain text, otherwise attachment; `?attachment=true` forces download. `Content-Disposition` carries `filename` plus RFC 6266 `filename*`. HTML, SVG and XML are always attachments with `Content-Security-Policy: sandbox`; `X-Content-Type-Options: nosniff` always. Session cookie or bearer. |
| `GET /api/v1/files/{id}/content/{name}` | done | same as `/content`; the name is ignored. |
| `GET /api/v1/files/{id}/data/content` | done | `{content: <extracted text>}`. |
| `GET /api/v1/files/{id}/preview?sheet=n` | done | Kera extension: `{kind: csv\|xlsx\|text\|pdf\|image\|docx\|other, sheets, sheet, rows: string[][], truncated}`, capped at 2,000 rows x 200 columns (text: first 200 KB, one column per line). |
| `DELETE /api/v1/files/{id}` | done | owner or admin; removes the stored blob. `{message}`. |
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
| `GET /api/v1/auths/` | done | current user `{id, email, name, role, profile_image_url, permissions, groups}` (group ids). A session caller (not an API key) also gets `{token, token_type, expires_at}` and a refreshed `kera_session` cookie. |
| `GET/POST /api/v1/auths/signout` | done | clears the session cookie. |
| `GET /api/v1/auths/api_key` | done | `{api_key: "<prefix>..."}` of the newest key (full keys are never stored); 404 when none. Needs `features.api_keys` (admins always). |
| `POST /api/v1/auths/api_key` | done | `{api_key}`: a new full key, shown once. Rotates the one key these single-key routes manage; named keys stay. |
| `DELETE /api/v1/auths/api_key` | done | deletes every key of the caller; returns `true`. |
| `GET /api/v1/users/?query=&page=` | done | admin; `{users: [{id, name, email, role, profile_image_url, last_active_at, created_at, groups: [{id, name}], api_key_count}], total}`, 30 per page, epoch seconds. |
| `GET /api/v1/users/{id}` | done | admin; one user in the list shape. |
| `POST /api/v1/users/{id}/update` | done | admin; `{name?, role?}`. 403 on your own role, 409 `last_admin` when no admin would remain. |
| `DELETE /api/v1/users/{id}` | done | admin; not yourself. |
| `GET/POST /api/v1/users/default/permissions` | done | admin; the `user.permissions` object (POST replaces and returns it). |
| `GET /api/v1/users/user/settings` | done | the caller's settings object. |
| `POST /api/v1/users/user/settings/update` | done | top-level keys of the body replace the stored ones (shallow merge); returns the result. |
| `GET /api/v1/users/{id}/profile/image` | done | redirect to an http(s) picture, the decoded raster `data:` image, or a generated SVG initial. |
| `GET /api/v1/groups/` | done | admin; `[{id, name, description, permissions, member_count, created_at, updated_at}]`. |
| `POST /api/v1/groups/create` | done | admin; `{name, description, permissions}`; 409 on a duplicate name. |
| `GET /api/v1/groups/id/{id}` | done | admin; the group plus `user_ids`. |
| `POST /api/v1/groups/id/{id}/update` | done | admin; partial `{name?, description?, permissions?}`. |
| `POST /api/v1/groups/id/{id}/users/add` and `/users/remove` | done | admin; `{user_ids}`, unknown ids ignored; returns the group. |
| `DELETE /api/v1/groups/id/{id}/delete` | done | admin; also drops the group's access grants. |
| `GET /api/v1/models/` | done | list of model rows (shape below): all rows for admins, otherwise rows the caller owns or holds a write grant on. |
| `GET /api/v1/models/model?id=` | done | `{id, user_id, base_model_id, connection_id, name, params, meta: {description, profile_image_url, toolIds, builtinTools, disabledTools, knowledge: [{id, mode}], capabilities, suggestion_prompts, hidden, features, defaultFeatures, fileContext, hooks, sort}, is_active, managed, created_at, updated_at}`. `params.system` is the system prompt. A discovered model without a row returns a fresh row. 404 when the caller cannot read it. |
| `POST /api/v1/models/create` | done | body like the row shape, `id` required; 409 when it exists (for non-admins also when the id is a discovered model). Needs `workspace.models` (admins always). The caller becomes the owner. A non-admin's `base_model_id` must be a model they can use (else 400). |
| `POST /api/v1/models/model/update?id=` | done | partial update: fields absent from the body stay as they are, so posting back a fetched row with `meta.description` edited works. `params` replaces the params (its `system` sets the system prompt); `meta.toolIds` sets tool servers and enabled builtins. Needs admin, owner or a write grant (403; 404 when the caller cannot read the model). Creates the row for a discovered model. |
| `POST /api/v1/models/model/toggle?id=` | done | flips `is_active`, returns the row. |
| `DELETE /api/v1/models/model/delete?id=` | done | also `POST` with `?id=` or `{id}`. Removes the row and its grants; returns `true`. |
| `GET /api/v1/configs/connections` | done | admin. `{connections: [{id, kind, base_url, prefix, model_ids, enabled, headers, options, sort, managed, has_key, implicit?}]}`; API keys are never returned. `options.forward_user_headers` maps `email`/`name`/`id`/`role` to header names; `options.web_search` holds Responses web-search options. |
| `POST /api/v1/configs/connections` | done | admin. `{connections: [...]}` upserted by id; an absent `api_key` keeps the stored key, `""` clears it. Connections not listed are kept. |
| `POST /api/v1/configs/connections/{id}/verify` | done | admin. Lists the connection's models live: `{ok: true, models: [...]}`, 502 on failure. |
| `DELETE /api/v1/configs/connections/{id}` | done | admin. |
| `GET /api/v1/memories/` | done | needs `features.memory`. The caller's memories `[{id, user_id, content, created_at, updated_at}]`, newest first, epoch seconds. |
| `POST /api/v1/memories/add` | done | `{content}` (max 4,000 characters) -> the memory. Embedded with `rag.embedding` when configured. |
| `POST /api/v1/memories/query` | done | `{content, k}` -> memories best first with `score` (Kera returns the list shape, not the vector-store result shape). Cosine similarity when `rag.embedding` is configured, full-text otherwise. |
| `POST /api/v1/memories/{id}/update` | done | `{content}` -> the memory; 404 for another user's id. |
| `DELETE /api/v1/memories/{id}` | done | `true`; 404 when missing. |
| `DELETE /api/v1/memories/delete/user` | done | deletes every memory of the caller; `true`. |
| `POST /api/v1/evaluations/feedback` | planned | `{type, data: {rating, model_id, comment}, meta, snapshot}`. |
| `GET /api/version` | done | `{version}`. |

## Kera-only identity routes

| Route | Status | Notes |
|---|---|---|
| `GET /oauth/{provider}/login?next=` | done | redirect to the provider (PKCE S256, state in an encrypted 10-minute cookie). `next` must be a same-origin path, else `/`. |
| `GET /oauth/{provider}/callback` | done | redirects to `/auth#token=<jwt>&next=<path>` and sets `kera_session`; failures go to `/auth?error=<code>` (`invalid_state`, `provider_denied`, `unknown_provider`, `exchange_failed`, `userinfo_failed`, `email_unverified`, `domain_not_allowed`, `email_taken`, `signup_disabled`, `account_pending`, `server_error`). |
| `GET /api/v1/auths/providers` | done | public; `{providers: [{id, name}], login_form: false}`. |
| `GET/POST /api/v1/auths/api_keys` | done | named keys: list `[{id, name, prefix, created_at, last_used_at}]`; POST `{name}` -> `{id, name, key, prefix}` (key shown once). |
| `DELETE /api/v1/auths/api_keys/{id}` | done | one of the caller's keys. |
| `GET/PUT /api/v1/grants/{type}/{id}` | done | admin; `[{principal: "user:<id>" \| "user:*" \| "group:<id>", permission: "read" \| "write"}]`. PUT replaces every grant of the resource, including ones `kera apply` made; the new rows are not managed. |
| `GET /api/v1/favicon?domain=` | done | signed-in users; the site's icon (`/favicon.ico`, else the home page's `<link rel=icon>`), raster images or SVG of at most 32 KB, private-network targets refused, in-memory cache (24 h, misses 1 h); 404 when none, 400 for anything but a hostname. |
## Database import (`kera migrate from-openwebui`)
Observed source schema (0.13, Postgres) and how `internal/importowui` maps it. Status: **done**.
| Source | Meaning | Kera |
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
