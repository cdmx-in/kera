# Open WebUI compatibility contract

Kera serves a subset of the Open WebUI HTTP API so existing scripts and clients keep working after migrating. This document is written from observed request and response shapes, in our own words. Implement compatibility routes from this file, never from Open WebUI source code (see CONTRIBUTING.md).

Status legend: **planned**, **done**.

## Authentication

Every route accepts `Authorization: Bearer <token>` where the token is either a session token or a personal API key starting with `sk-`. Browser sessions also send the `kera_session` cookie; a POST/PUT/PATCH/DELETE authenticated only by that cookie is accepted from the same origin only (CSRF), so scripts should use the header. Errors use `{"detail": "<message>"}` with the HTTP status.

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
