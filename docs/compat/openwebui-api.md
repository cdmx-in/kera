# Open WebUI compatibility contract

Kera serves a subset of the Open WebUI HTTP API so existing scripts and clients keep working after migrating. This document is written from observed request and response shapes, in our own words. Implement compatibility routes from this file, never from Open WebUI source code (see CONTRIBUTING.md).

Status legend: **planned**, **done**.

## Authentication

Every route accepts `Authorization: Bearer <token>` where the token is either a session token or a personal API key starting with `sk-`. Browser sessions also send the `kera_session` cookie; state-changing requests from another browser origin (`Sec-Fetch-Site`/`Origin` check) are refused with 403. Errors use `{"detail": "<message>"}` with the HTTP status.

## OpenAI-compatible

| Route | Status | Notes |
|---|---|---|
| `GET /api/models` | done | Also `GET /api/v1/models` and `GET /v1/models`. `{"data": [model...]}`. Each model has `id`, `name`, `object: "model"`, `created`, `owned_by` (`ollama` or `openai`), `connection_type` (`local` or `external`), `local`, `hidden`, `features` (enabled feature ids), `defaultFeatures`, `toolIds` (`server:<id>`, `builtin:<name>`), `suggestionPrompts`, `profileImageUrl`, `capabilities`, and `info: {id, base_model_id, name, meta: {description, capabilities, hidden, suggestion_prompts, knowledge, toolIds, builtinTools, profile_image_url}, params}`. Only models the caller may use. Hidden models are listed for API-key callers and with `?include_hidden=true`. Ordered by the `site.model_order` setting, then name. |
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
| `GET /api/v1/auths/` | planned | current user with permissions. |
| `GET/POST /api/v1/users/default/permissions` | planned | admin. |
| `POST /api/v1/evaluations/feedback` | planned | `{type, data: {rating, model_id, comment}, meta, snapshot}`. |
| `GET /api/version` | done | `{version}`. |
