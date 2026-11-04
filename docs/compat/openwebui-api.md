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
| `GET /api/v1/models/model?id=` | planned | model with `meta.toolIds` and `meta.knowledge`. |
| `POST /api/v1/models/model/update?id=` | planned | partial update of name, description, meta, params. |
| `GET /api/v1/auths/` | planned | current user with permissions. |
| `GET/POST /api/v1/users/default/permissions` | planned | admin. |
| `POST /api/v1/evaluations/feedback` | planned | `{type, data: {rating, model_id, comment}, meta, snapshot}`. |
| `GET /api/version` | done | `{version}`. |
