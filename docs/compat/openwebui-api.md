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
