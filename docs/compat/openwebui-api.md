# Open WebUI compatibility contract

Kera serves a subset of the Open WebUI HTTP API so existing scripts and clients keep working after migrating. This document is written from observed request and response shapes, in our own words. Implement compatibility routes from this file, never from Open WebUI source code (see CONTRIBUTING.md).

Status legend: **planned**, **done**.

## Authentication

Every route accepts `Authorization: Bearer <token>` where the token is either a session token or a personal API key starting with `sk-`. Browser sessions also send the `kera_session` cookie; a POST/PUT/PATCH/DELETE authenticated only by that cookie is accepted from the same origin only (CSRF), so scripts should use the header. Errors use `{"detail": "<message>"}` with the HTTP status.

## OpenAI-compatible

| Route | Status | Notes |
|---|---|---|
| `GET /api/models` | done | Also `GET /api/v1/models` and `GET /v1/models`. `{"data": [model...]}`. Each model has `id`, `name`, `object: "model"`, `created`, `owned_by` (`ollama` or `openai`), `connection_type` (`local` or `external`), `local`, `hidden`, `kind` (`chat`, `embedding` or `rerank`; Kera addition), `features` (enabled feature ids), `defaultFeatures`, `toolIds` (`server:<id>`, `builtin:<name>`), `suggestionPrompts`, `profileImageUrl`, `capabilities`, and `info: {id, base_model_id, name, meta: {description, capabilities, hidden, suggestion_prompts, knowledge, toolIds, builtinTools, profile_image_url}, params}`. Only models the caller may use. Hidden models are listed for API-key callers and with `?include_hidden=true`. Embedding and rerank models (from Ollama `/api/show` capabilities, LiteLLM `/model/info` mode, else `embed`/`rerank` in the name) are left out unless `?kinds=all`, and a chat with one fails with `model_not_chat`. Ordered by the `site.model_order` setting, then name. |
| `POST /api/chat/completions` | done | Also `/api/v1/chat/completions` and `/v1/chat/completions`. OpenAI request body (`model`, `messages` with string or `text`/`image_url` content, `stream`, generation options, `tools`). Extensions: `chat_id`, `id` (assistant message id), `parent_id`, `user_message: {id, parentId, content, files}` (the web app's new turn), `files: [{type: "file"\|"collection", id}]`, `features: {web_search, code_interpreter, memory}` (absent = the model's defaults), `tool_ids` (`server:<id>` or bare ids; absent = the model's servers), `temporary`, `stream_format: "events"`. The model preset's prompt (with variables), parameters (request values win), knowledge, tools and hooks apply for every caller, with or without `chat_id`; the server runs the tool loop and returns the final answer. When the body carries `tools`, only those are offered and tool calls are returned to the caller (`finish_reason: "tool_calls"`). `stream: false` -> `{id, object: "chat.completion", choices: [{message: {role, content, reasoning_content?, tool_calls?}, finish_reason}], usage, sources (nested citations), kera: {run_id, message_id, chat_id, tool_calls, sources, files, parts}}`; content is the text of the last model round. `stream: true` -> `chat.completion.chunk` events (`delta.content`, `delta.reasoning_content`, final `tool_calls` for passthrough, a usage chunk, `data: [DONE]`); progress rides along as chunks with `choices: []` and `kera: {type, data}`. `stream_format: "events"` -> the native dialect (see below). Errors before the run: `{detail, code, error: {message, code}}` with the HTTP status (`404 model_not_found`, `409 local_lock`, `409 busy`). A run continues when the client disconnects. |
| `GET /api/v1/chats/{id}/messages/{mid}/stream?after=<seq>` | done | Kera extension: re-attach to a running answer (also `Last-Event-ID`). `after=0` starts with one `snapshot` of the state so far. A finished or lost run answers a `snapshot` of the stored message and `done`. Temporary chats use the `temp:<id>` chat id from the first event. |
| `POST /api/v1/chats/{id}/messages/{mid}/cancel` | done | stops the run; the message is kept with status `stopped`. |
| `POST /api/v1/chats/{id}/messages/{mid}/calls/{callId}` | done | `{value}` answers an `ask_user` (`[{question, answers[]}]`) or `confirm` (`true`/`false`); 404 when already answered. Owner only. |
| `GET /api/v1/chats/{id}/runs` | done | `[{run_id, message_id, model, seq, done, created_at}]` of live and recent (10 min) runs. |
| `POST /api/v1/runs/{run}/events`, `POST /api/v1/runs/{run}/ask` | done | callbacks for tool and hook servers, `Authorization: Bearer <run token>` (15 min, from `X-Kera-Callback-Token` / the hook envelope). `events`: `{type: status\|sources\|files\|block, data}`. `ask`: `{kind: confirm\|choice\|input, title, message, questions?, timeout_ms}` long-polls up to 240 s -> `{answer}` or `{answer: null, reason}`. |

Native event dialect (`text/event-stream`, `id:` = seq, `: ping` every 15 s, `X-Accel-Buffering: no`): every `data:` is JSON with `type`, `seq` (1, 2, ... per message) and `messageId` (plus `chatId` on the first event of a temporary run). Types: `delta {text}`, `reasoning {text}` / `{done, durationMs}`, `tool_call {callId, name, server, args}`, `tool_result {callId, status: ok\|error, preview, files?}`, `status {description, done, id?, action?, hidden?, query?, urls?, count?, callId?}`, `sources {sources: [{id, name, title, url, fileId, page, snippet, score}]}`, `files {files: [{id, name, size, contentType, type}]}`, `block {block}`, `snapshot {parts, files, sources, status, pendingCalls}`, `ask_user {callId, questions}`, `confirm {callId, title, message}`, `compaction {state, summary?, before?, after?}`, `title {title}`, `follow_ups {follow_ups}`, `usage {usage}`, `error {code, message}`, `done {status?: "stopped"}`.

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
| `GET /api/v1/knowledge/` | done | `{items: [...], total}`: collections the caller can read (owner, admin or a read/write grant), newest first. Each item: `{id, user_id, name, description, meta, created_at, updated_at, write_access, embedding_model: {id, connection, model, dim} \| null, fts_config, reindexing, status: {files, chunks, pending, processing, completed, failed}}`. |
| `POST /api/v1/knowledge/create` | done | `{name, description}` (other fields such as `access_control` are ignored; new collections are private). Needs `workspace.knowledge`. Returns the collection. |
| `GET /api/v1/knowledge/{id}` | done | the collection plus `files` (the `/files` items). 404 when the caller cannot read it. |
| `GET /api/v1/knowledge/{id}/files` | done | `{items: [{id, user_id, filename, meta, size, chunks, data: {status, file_status, error?}, created_at, updated_at}], total}`. `data.status` is the indexing state (`pending` -> `processing` -> `completed` or `failed`); `file_status` is the extraction state. |
| `POST /api/v1/knowledge/{id}/file/add` | done | `{file_id}`; a file still being extracted is indexed when extraction completes. Adding a file again re-queues it only when it failed. Needs write access to the collection and read access to the file. Returns the collection with `files`. |
| `POST /api/v1/knowledge/{id}/file/remove` | done | `{file_id}`; drops the link and its chunks, the file itself stays (delete it with `DELETE /api/v1/files/{id}`). |
| `POST /api/v1/knowledge/{id}/file/update` | done | `{file_id}`; indexes the file again. |
| `POST /api/v1/knowledge/{id}/update` | done | `{name?, description?}`. |
| `DELETE /api/v1/knowledge/{id}/delete` | done | removes the collection, its chunks and grants; returns `true`. |
| `POST /api/v1/knowledge/{id}/reindex?embedding_model=&connection=` | done | Kera extension: re-embeds every file. With `embedding_model` the collection moves to that model (on `connection`, default `rag.embedding.connection`); the old chunks answer queries until every file is done; moving to another model (or naming a `connection`) is admin-only (403). Without `embedding_model` a move in progress is cancelled. `{status: true, queued}`. |
| `GET/PUT /api/v1/knowledge/{id}/grants` | done | Kera extension: the collection's grants `[{principal, permission, managed}]` (`user:<id>`, `user:*`, `group:<id>`; read or write). PUT replaces them; owner, admins and write-grant holders. |
| `POST /api/v1/retrieval/query/collection` | done | `{collection_names, query, k, k_reranker?}` (ids or names) -> `{documents: [[text...]], metadatas: [[{name, source, file_id, knowledge_id, page?, headers?}]], distances: [[score...]]}`, one inner list, best first. Hybrid vector + full-text search fused by weighted reciprocal rank, then the reranker when `rag.reranker.url` is set; scores are reranker relevance (0-1) or fusion scores. `k_reranker`, else `k`, else `rag.top_k_rerank` results. Also served at `/query/collections`. |
| `GET /api/v1/retrieval/embedding` | done | `{embedding_engine, embedding_model, embedding_batch_size}` (engine = connection id). |

## Prompts

| Route | Status | Notes |
|---|---|---|
| `GET /api/v1/prompts/` | done | prompts the caller can read: own first, then shared, by command. `[{id, user_id, command, title, name, content, created_at, updated_at, timestamp, write_access}]`; `command` has no leading `/`. |
| `POST /api/v1/prompts/create` | done | `{command, title \| name, content}`; a leading `/` is dropped, commands are 1-64 of `a-z 0-9 - _` and unique per user (409). Needs `workspace.prompts`. |
| `GET /api/v1/prompts/command/{command}` | done | the caller's own prompt with that command, else a shared one. |
| `GET /api/v1/prompts/id/{id}` | done | 404 when the caller cannot read it. |
| `POST /api/v1/prompts/id/{id}/update` | done | partial `{command?, title?, content?}`; owner, admins or a write grant. |
| `DELETE /api/v1/prompts/id/{id}/delete` | done | also removes its grants. |
| `GET/PUT /api/v1/prompts/id/{id}/grants` | done | same shape as the knowledge grants. |

## Chats, models, users, feedback

| Route | Status | Notes |
|---|---|---|
| `POST /api/v1/chats/new` | done | ChatForm `{chat: {title, models, tags, history: {currentId, messages: {id: {id, parentId, role, content, model, timestamp, done}}}}, folder_id?}`; the messages are stored (an assistant entry with `done: false` waits for a completion with that `id`). Returns the chat object. The client's chat id is ignored. |
| `GET /api/v1/chats/{id}` | done | `{id, user_id, title, folder_id, share_id, archived, pinned, created_at, updated_at, tags, meta: {tags}, chat: {id, title, models, tags, history: {currentId, messages: {id: {id, parentId, childrenIds, role, content, model, timestamp, createdAt (ms), done, status, error?, files, sources, statusHistory, usage, followUps, parts, pendingCalls, seq?}}}, messages: [active branch]}}`. A streaming message carries the live state and `seq` (resume from it). Imported tool traces and nested citations are rendered in the web app's shape. |
| `POST /api/v1/chats/{id}` | done | `{chat: {title?, pinned?, history: {currentId}?}}`; returns the chat. |
| `DELETE /api/v1/chats/{id}` | done | `true`; needs `chat.delete`. |
| `GET /api/v1/chats/?page=n` | done | also `/chats/list`: `[{id, title, updated_at, created_at, pinned, archived, folder_id}]`, newest first, 60 per page (no page = all), archived left out; `?folder_id=`, `?tag=`. |
| `GET /api/v1/chats/pinned`, `/archived`, `/folder/{id}`, `/search?text=&page=` | done | same list shape. Search matches every word as a prefix in titles and message text; `tag:<name>` words filter by tag. |
| `POST /api/v1/chats/{id}/pin`, `/archive` | done | toggle; return the list entry. `GET /chats/{id}/pinned` -> bool. |
| `POST /api/v1/chats/{id}/folder` | done | `{folder_id}` (null = top level). |
| `POST /api/v1/chats/{id}/share`, `DELETE ...` | done | snapshot of the chat now -> `{id, share_id, chat_id, title}` (sharing again refreshes the snapshot); `GET /api/v1/chats/share/{share_id}` returns it in the chat shape to any signed-in user (`/s/<share_id>`). |
| `GET/POST/DELETE /api/v1/chats/{id}/tags`, `DELETE .../tags/all`, `GET /chats/all/tags`, `POST /chats/tags` | done | tags `{name}` are lower-case with `_` for spaces; lists are `[{id, name, user_id}]`; `POST /chats/tags {name}` lists chats with that tag. |
| `POST /api/v1/chats/{id}/messages/{mid}`, `DELETE ...` | done | `{content}` edits a message; delete removes it and its replies (the active leaf moves to its parent). Both return the chat. |
| `GET/POST /api/v1/folders/`, `GET/DELETE /folders/{id}`, `POST /folders/{id}/update`, `/update/parent`, `/update/expanded` | done | `{id, user_id, parent_id, name, is_expanded, created_at, updated_at}`; deleting a folder keeps its chats at the top level. |
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
| `GET /api/v1/models/model?id=` | done | `{id, user_id, base_model_id, connection_id, name, params, meta: {description, profile_image_url, toolIds, builtinTools, disabledTools, knowledge: [{id, name?, mode}] (`kera apply` stores collections by name; an entry needs an id or a name), capabilities, suggestion_prompts, hidden, features, defaultFeatures, fileContext, hooks, sort}, is_active, managed, created_at, updated_at}`. `params.system` is the system prompt. A discovered model without a row returns a fresh row. 404 when the caller cannot read it. |
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
| `POST /api/v1/evaluations/feedback` | done | signed-in users; `{type, data: {rating, model_id, comment}, meta, snapshot}` (`type` defaults to `rating`) stored under the caller -> `{id, user_id, version, type, data, meta, snapshot, created_at, updated_at}`. The list and leaderboard routes are not built. |
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
| `GET /api/v1/configs/settings` | done | admin; every known setting (built-in defaults plus every key `kera apply` writes) sorted by key: `[{key, value, default, managed, secret?, set}]`. Secret keys (`code_interpreter.token`, `spend.source.dsn`) never carry `value`; `set` says one is stored. |
| `POST /api/v1/configs/settings` | done | admin; `{key: value, ...}` written in one transaction. Unknown keys and values whose JSON type differs from the default's (or a fraction for an integer key) are rejected with 400 and nothing is written. `null` resets a key to its default; for a secret key `""` does too. The `managed` flag is kept, so the next `kera apply` still overwrites a key it owns. Returns the GET list. |
| `GET /api/v1/configs/tool_servers` | done | admin; `[{id, kind, name, description, url, spec_path, auth: {type, header, provider, has_key}, enabled, name_filter, disabled_functions, max_result_chars, auto_enable_when_connected, display, sort, managed}]`. Keys are never returned. |
| `POST /api/v1/configs/tool_servers` | done | admin; upserts one server by `id` (`auth.key` absent keeps the stored key, `""` clears it; `bearer`/`header` need one; `user_oauth` needs an existing `auth.provider`; `display` absent keeps the stored one). The row becomes `managed: false`. Grants go through `/api/v1/grants/tool_server/{id}`. |
| `DELETE /api/v1/configs/tool_servers/{id}` | done | admin; also drops the server's grants. |
| `GET/POST /api/v1/configs/hook_servers`, `DELETE .../{id}` | done | admin; `{id, name, url, has_key, events: inlet\|outlet, priority, scope: global\|models, model_ids, config, forward_oauth, timeout_ms, on_error: skip\|abort, enabled, managed}`; POST takes `key` (write-only, same rules as tool servers). |
| `GET/POST /api/v1/configs/oauth_providers`, `DELETE .../{id}` | done | admin; `{id, name, login, issuer, authorization_endpoint, token_endpoint, revocation_endpoint, userinfo_endpoint, client_id, has_secret, scope, pkce, send_resource, managed, redirect_uri}`; POST takes `client_secret` (write-only). `redirect_uri` is `<KERA_URL>/oauth/<id>/callback` for login providers and `<KERA_URL>/oauth/clients/<id>/callback` for tool-server providers. DELETE answers 409 `in_use` while a tool server uses the provider. |
| `GET /api/v1/analytics/summary\|daily\|models\|users?start_date=&end_date=` | done | admin; whole UTC days, end inclusive, default the last 30 days, at most 366. One event per assistant reply (the user from its chat, tokens from `usage` rows of that message, else from the message's own usage, so imported history counts) plus `usage` rows without a stored message. `summary` = `{start_date, end_date, users_total, active_users, messages, chats, models, prompt_tokens, completion_tokens}`; `daily` = `[{day, messages, users, prompt_tokens, completion_tokens}]` for every day; `models` = `[{model, messages, users, chats, prompt_tokens, completion_tokens}]`; `users` = `[{user_id, name, email, messages, chats, prompt_tokens, completion_tokens, last_at}]`. |
| `GET /api/v1/analytics/spend?start_date=&end_date=` | done | admin; both dates required. `{start_date, end_date, rows: [{end_user, model, day, spend, prompt_tokens, completion_tokens, requests}], users: [{id, name, email, last_active_at}]}`: LiteLLM spend logs summed per end user, model and UTC day (`end_user` `""` for unattributed calls), read through a read-only 2-connection pool on `spend.source.dsn` when `spend.source.kind` is `litellm_postgres`. 503 `spend_not_configured`, 502 `spend_source_failed`. |
| `GET /api/v1/admin/sync/openwebui` | done | admin; `{enabled, at, timezone, next_run?, running, runs: [{id, source, trigger, since?, started_at, finished_at, error, totals: {inserted, existing, skipped, pruned}, stats}]}` for the last 10 `migration_run` rows (CLI runs included). `enabled` when `KERA_OWUI_SYNC_DSN` is set. |
| `POST /api/v1/admin/sync/openwebui` | done | admin; starts an import now (same options as the nightly run) -> 202 `{started: true}`; 409 `sync_running` while an import holds the lock (scheduler, button or CLI); 503 `sync_not_configured`. |
| `GET /api/v1/favicon?domain=` | done | signed-in users; the site's icon (`/favicon.ico`, else the home page's `<link rel=icon>`), raster images or SVG of at most 32 KB, private-network targets refused, in-memory cache (24 h, misses 1 h); 404 when none, 400 for anything but a hostname. |
## Tool servers and per-user OAuth

| Route | Status | Notes |
|---|---|---|
| `GET /api/v1/tools/` | done | `[{id, name, kind: "openapi" \| "mcp" \| "builtin", description, authenticated, auth_type: "none" \| "bearer" \| "header" \| "user_oauth", provider?, auto_enable?, connected_at?, functions: [names], error?}]`: the tool servers the caller may read (admins: all enabled ones) plus the built-in tool sets. `authenticated` is true unless the server needs the caller's own OAuth grant and the caller has not connected it; `connected_at` (epoch seconds) is set when they have. `functions` honours the server's name filter and disabled list; a server that cannot be reached within 8 s lists none and sets `error`. |
| `GET /oauth/clients/{provider}/authorize?next=` | done | Kera extension, browser only. Needs a session (otherwise redirects to `/auth?redirect=...`). Redirects to the provider with PKCE S256 and the provider's scopes; Google also gets `access_type=offline` and `prompt=consent` (so a refresh token is issued). The redirect URI is `<KERA_URL>/oauth/clients/{provider}/callback`. State, verifier, user and `next` travel in an encrypted 10-minute cookie. |
| `GET /oauth/clients/{provider}/callback` | done | exchanges the code, stores the token encrypted per user and provider, redirects to `next` (a same-origin path) or `/?tools=<tool server id>`. Failures redirect with `?oauth_error=<code>` (`invalid_state`, `session_mismatch`, `provider_denied`, `unknown_provider`, `exchange_failed`, `server_error`). |
| `DELETE /api/v1/oauth/clients/{provider}/session` | done | revokes the grant at the provider (RFC 7009: refresh token, else access token) and deletes it; `{ok: true}`, also when nothing was connected or the provider row is gone. A 4xx from the provider (token already invalid, or a revocation that can never succeed) still deletes it. When the provider answers 5xx or is unreachable the grant is kept and the answer is 409 `{detail, code: "revoke_failed"}` (never 5xx, which a CDN may rewrite). |

Tool calls carry `X-Kera-User-Id`, `X-Kera-User-Email`, `X-Kera-Chat-Id`, `X-Kera-Message-Id`, `X-Kera-Files` (comma-separated file ids), `X-Kera-Callback` and `X-Kera-Callback-Token` headers (OpenAPI and MCP alike), plus `Authorization: Bearer` with the server's key or the user's OAuth access token (refreshed when it expires within 5 minutes; a refresh the provider rejects deletes the grant). A `user_oauth` server may also set `auth.header` + `auth.key`: that header carries a static service key next to the user's token. `X-Kera-Callback` is `<KERA_CALLBACK_URL or KERA_URL>/api/v1/runs/<run>`; set `KERA_CALLBACK_URL` to an internal address when the public URL sits behind a proxy that cuts long requests (the `ask` long-poll lasts up to 240 s). An OpenAPI result that is a JSON object with `content` gives the model only `content` and the chat its `parts` (UI blocks), `files` and `sources`; anything else goes to the model as is. Results longer than the server's `max_result_chars` (default setting `tools.max_result_chars`) are cut with a note. MCP servers are spoken to over Streamable HTTP, protocol revisions 2024-11-05 to 2025-11-25 (initialize, sessions, SSE responses, elicitation in form mode, progress and log notifications).

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

Re-runs upsert in place and never overwrite Kera's own state: rows `kera apply` manages (groups, grants it adopted) are neither updated nor pruned, source grants on resources whose access the config owns (managed models, anything with a managed grant) are skipped, and `knowledge.meta` is merged so Kera's keys (`reindex_to`, `errors`) survive.

Nightly sync (shadow period): with `KERA_OWUI_SYNC_DSN` set, `kera serve` runs the import every day at `KERA_OWUI_SYNC_AT` (HH:MM in `site.timezone`, default `01:30`) with `--prune-missing`, `--since` = start of the last successful full run from the same source (not dry, no `--only`, with blobs) minus 1 h, and blobs from `KERA_OWUI_SYNC_UPLOADS` (default `/owui/uploads`). Imports run one at a time under a Postgres advisory lock shared with `kera migrate from-openwebui`; every run is a `migration_run` row (`flags.trigger` = `schedule`, `admin` or `cli`).
