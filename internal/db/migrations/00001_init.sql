-- +goose Up
-- Kera core schema. Ids are text (uuid v7 from Go; imported ids kept verbatim).
-- Requires PostgreSQL 18 + pgvector >= 0.8.

CREATE EXTENSION IF NOT EXISTS vector;

-- Identity -------------------------------------------------------------------

CREATE TABLE "user" (
    id                text PRIMARY KEY,
    name              text        NOT NULL DEFAULT '',
    email             text        NOT NULL,
    role              text        NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user', 'pending')),
    profile_image_url text        NOT NULL DEFAULT '',
    oauth_sub         text,
    settings          jsonb       NOT NULL DEFAULT '{}',
    info              jsonb       NOT NULL DEFAULT '{}',
    origin            text        NOT NULL DEFAULT 'kera',
    last_active_at    timestamptz NOT NULL DEFAULT now(),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX user_email_lower ON "user" (lower(email));
CREATE UNIQUE INDEX user_oauth_sub ON "user" (oauth_sub) WHERE oauth_sub IS NOT NULL;

CREATE TABLE auth_password (
    user_id text PRIMARY KEY REFERENCES "user" (id) ON DELETE CASCADE,
    hash    text NOT NULL
);

CREATE TABLE api_key (
    id           text PRIMARY KEY,
    user_id      text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    key_hash     text        NOT NULL UNIQUE,
    prefix       text        NOT NULL,
    name         text        NOT NULL DEFAULT '',
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_key_user ON api_key (user_id);

CREATE TABLE oauth_provider (
    id                     text PRIMARY KEY,
    issuer                 text    NOT NULL DEFAULT '',
    authorization_endpoint text    NOT NULL DEFAULT '',
    token_endpoint         text    NOT NULL DEFAULT '',
    revocation_endpoint    text    NOT NULL DEFAULT '',
    userinfo_endpoint      text    NOT NULL DEFAULT '',
    client_id              text    NOT NULL DEFAULT '',
    client_secret_enc      bytea,
    scope                  text    NOT NULL DEFAULT '',
    pkce                   boolean NOT NULL DEFAULT true,
    send_resource          boolean NOT NULL DEFAULT false,
    options                jsonb   NOT NULL DEFAULT '{}',
    managed                boolean NOT NULL DEFAULT false
);

CREATE TABLE oauth_token (
    id         text PRIMARY KEY,
    user_id    text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    provider   text        NOT NULL,
    token_enc  bytea       NOT NULL,
    scope      text        NOT NULL DEFAULT '',
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, provider)
);

CREATE TABLE "group" (
    id          text PRIMARY KEY,
    name        text        NOT NULL UNIQUE,
    description text        NOT NULL DEFAULT '',
    permissions jsonb       NOT NULL DEFAULT '{}',
    managed     boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE group_member (
    group_id text NOT NULL REFERENCES "group" (id) ON DELETE CASCADE,
    user_id  text NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_member_user ON group_member (user_id);

-- principal_id is a user id, a group id, or '*' (every user).
CREATE TABLE access_grant (
    id             text PRIMARY KEY,
    resource_type  text        NOT NULL CHECK (resource_type IN ('model', 'tool_server', 'knowledge', 'prompt', 'chat', 'folder')),
    resource_id    text        NOT NULL,
    principal_type text        NOT NULL CHECK (principal_type IN ('user', 'group')),
    principal_id   text        NOT NULL,
    permission     text        NOT NULL CHECK (permission IN ('read', 'write')),
    managed        boolean     NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (resource_type, resource_id, principal_type, principal_id, permission)
);
CREATE INDEX access_grant_principal ON access_grant (principal_type, principal_id);

-- Configuration ----------------------------------------------------------------

CREATE TABLE config (
    key        text PRIMARY KEY,
    value      jsonb       NOT NULL,
    managed    boolean     NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE connection (
    id          text PRIMARY KEY,
    kind        text    NOT NULL CHECK (kind IN ('ollama', 'openai', 'responses')),
    base_url    text    NOT NULL,
    api_key_enc bytea,
    prefix      text    NOT NULL DEFAULT '',
    model_ids   text[]  NOT NULL DEFAULT '{}',
    enabled     boolean NOT NULL DEFAULT true,
    headers     jsonb   NOT NULL DEFAULT '{}',
    options     jsonb   NOT NULL DEFAULT '{}',
    sort        integer NOT NULL DEFAULT 0,
    managed     boolean NOT NULL DEFAULT false
);

-- A row with connection_id set overrides a discovered base model; a row with
-- base_model_id set is a preset built on another model.
CREATE TABLE model (
    id               text PRIMARY KEY,
    connection_id    text REFERENCES connection (id) ON DELETE SET NULL,
    base_model_id    text,
    name             text        NOT NULL DEFAULT '',
    description      text        NOT NULL DEFAULT '',
    system_prompt    text        NOT NULL DEFAULT '',
    params           jsonb       NOT NULL DEFAULT '{}',
    capabilities     jsonb       NOT NULL DEFAULT '{}',
    features         jsonb       NOT NULL DEFAULT '{}',
    default_features text[]      NOT NULL DEFAULT '{}',
    file_context     text        NOT NULL DEFAULT 'tools' CHECK (file_context IN ('tools', 'full')),
    tools            jsonb       NOT NULL DEFAULT '{}',
    knowledge        jsonb       NOT NULL DEFAULT '[]',
    hooks            text[]      NOT NULL DEFAULT '{}',
    suggestions      jsonb       NOT NULL DEFAULT '[]',
    hidden           boolean     NOT NULL DEFAULT false,
    active           boolean     NOT NULL DEFAULT true,
    sort             integer     NOT NULL DEFAULT 0,
    image_url        text        NOT NULL DEFAULT '',
    owner_id         text,
    managed          boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tool_server (
    id                         text PRIMARY KEY,
    kind                       text    NOT NULL CHECK (kind IN ('openapi', 'mcp')),
    name                       text    NOT NULL DEFAULT '',
    description                text    NOT NULL DEFAULT '',
    url                        text    NOT NULL,
    spec_path                  text    NOT NULL DEFAULT 'openapi.json',
    auth                       jsonb   NOT NULL DEFAULT '{"type":"none"}',
    enabled                    boolean NOT NULL DEFAULT true,
    name_filter                text[]  NOT NULL DEFAULT '{}',
    disabled_functions         text[]  NOT NULL DEFAULT '{}',
    max_result_chars           integer NOT NULL DEFAULT 0,
    auto_enable_when_connected boolean NOT NULL DEFAULT false,
    display                    jsonb   NOT NULL DEFAULT '{}',
    sort                       integer NOT NULL DEFAULT 0,
    managed                    boolean NOT NULL DEFAULT false
);

CREATE TABLE hook_server (
    id         text PRIMARY KEY,
    name       text    NOT NULL DEFAULT '',
    url        text    NOT NULL,
    key_enc    bytea,
    events     text[]  NOT NULL DEFAULT '{inlet}',
    priority   integer NOT NULL DEFAULT 0,
    scope      text    NOT NULL DEFAULT 'models' CHECK (scope IN ('global', 'models')),
    model_ids  text[]  NOT NULL DEFAULT '{}',
    config     jsonb   NOT NULL DEFAULT '{}',
    forward_oauth text[] NOT NULL DEFAULT '{}',
    timeout_ms integer NOT NULL DEFAULT 10000,
    on_error   text    NOT NULL DEFAULT 'skip' CHECK (on_error IN ('skip', 'abort')),
    enabled    boolean NOT NULL DEFAULT true,
    managed    boolean NOT NULL DEFAULT false
);

-- Chats --------------------------------------------------------------------------

CREATE TABLE folder (
    id         text PRIMARY KEY,
    user_id    text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    parent_id  text REFERENCES folder (id) ON DELETE CASCADE,
    name       text        NOT NULL,
    expanded   boolean     NOT NULL DEFAULT false,
    origin     text        NOT NULL DEFAULT 'kera',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX folder_user ON folder (user_id);

CREATE TABLE chat (
    id                 text PRIMARY KEY,
    user_id            text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    title              text        NOT NULL DEFAULT 'New Chat',
    folder_id          text REFERENCES folder (id) ON DELETE SET NULL,
    pinned             boolean     NOT NULL DEFAULT false,
    archived           boolean     NOT NULL DEFAULT false,
    share_id           text UNIQUE,
    current_message_id text,
    locality           text CHECK (locality IN ('local', 'cloud')),
    model_ids          text[]      NOT NULL DEFAULT '{}',
    summary            text        NOT NULL DEFAULT '',
    meta               jsonb       NOT NULL DEFAULT '{}',
    origin             text        NOT NULL DEFAULT 'kera',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    title_tsv          tsvector GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED
);
CREATE INDEX chat_user_updated ON chat (user_id, updated_at DESC);
CREATE INDEX chat_user_pinned ON chat (user_id) WHERE pinned;
CREATE INDEX chat_folder ON chat (folder_id) WHERE folder_id IS NOT NULL;
CREATE INDEX chat_title_tsv ON chat USING gin (title_tsv);

-- parts/files/sources/pending_calls are persisted in the streamed shape so a
-- reload renders through the same code as a live stream.
CREATE TABLE message (
    id              text PRIMARY KEY,
    chat_id         text        NOT NULL REFERENCES chat (id) ON DELETE CASCADE,
    parent_id       text,
    role            text        NOT NULL CHECK (role IN ('system', 'user', 'assistant')),
    model_id        text        NOT NULL DEFAULT '',
    content         text        NOT NULL DEFAULT '',
    parts           jsonb       NOT NULL DEFAULT '[]',
    files           jsonb       NOT NULL DEFAULT '[]',
    sources         jsonb       NOT NULL DEFAULT '[]',
    pending_calls   jsonb       NOT NULL DEFAULT '[]',
    follow_ups      jsonb       NOT NULL DEFAULT '[]',
    usage           jsonb       NOT NULL DEFAULT '{}',
    status          text        NOT NULL DEFAULT 'done' CHECK (status IN ('streaming', 'done', 'error', 'stopped')),
    error           text        NOT NULL DEFAULT '',
    context_summary text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    tsv             tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED
);
CREATE INDEX message_chat_created ON message (chat_id, created_at);
CREATE INDEX message_chat_parent ON message (chat_id, parent_id);
CREATE INDEX message_model_created ON message (model_id, created_at) WHERE role = 'assistant';
CREATE INDEX message_tsv ON message USING gin (tsv);

CREATE TABLE chat_tag (
    chat_id text NOT NULL REFERENCES chat (id) ON DELETE CASCADE,
    tag     text NOT NULL,
    PRIMARY KEY (chat_id, tag)
);
CREATE INDEX chat_tag_tag ON chat_tag (tag);

CREATE TABLE shared_chat (
    share_id   text PRIMARY KEY,
    chat_id    text        NOT NULL REFERENCES chat (id) ON DELETE CASCADE,
    user_id    text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    title      text        NOT NULL DEFAULT '',
    snapshot   jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Files and knowledge --------------------------------------------------------------

CREATE TABLE file (
    id           text PRIMARY KEY,
    user_id      text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    filename     text        NOT NULL,
    content_type text        NOT NULL DEFAULT 'application/octet-stream',
    size         bigint      NOT NULL DEFAULT 0,
    sha256       text        NOT NULL DEFAULT '',
    path         text        NOT NULL,
    status       text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    error        text        NOT NULL DEFAULT '',
    text         text,
    meta         jsonb       NOT NULL DEFAULT '{}',
    origin       text        NOT NULL DEFAULT 'kera',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX file_user ON file (user_id, created_at DESC);
CREATE INDEX file_pending ON file (created_at) WHERE status IN ('pending', 'processing');

CREATE TABLE chat_file (
    chat_id    text NOT NULL REFERENCES chat (id) ON DELETE CASCADE,
    file_id    text NOT NULL REFERENCES file (id) ON DELETE CASCADE,
    message_id text,
    PRIMARY KEY (chat_id, file_id)
);

CREATE TABLE embedding_model (
    id            text PRIMARY KEY,
    connection_id text    NOT NULL,
    model         text    NOT NULL,
    dim           integer NOT NULL CHECK (dim > 0),
    batch         integer NOT NULL DEFAULT 32,
    doc_prefix    text    NOT NULL DEFAULT '',
    query_prefix  text    NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (connection_id, model)
);

CREATE TABLE knowledge (
    id                 text PRIMARY KEY,
    user_id            text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    name               text        NOT NULL,
    description        text        NOT NULL DEFAULT '',
    embedding_model_id text REFERENCES embedding_model (id),
    fts_config         regconfig   NOT NULL DEFAULT 'simple',
    meta               jsonb       NOT NULL DEFAULT '{}',
    origin             text        NOT NULL DEFAULT 'kera',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE knowledge_file (
    knowledge_id text        NOT NULL REFERENCES knowledge (id) ON DELETE CASCADE,
    file_id      text        NOT NULL REFERENCES file (id) ON DELETE CASCADE,
    status       text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    chunks       integer     NOT NULL DEFAULT 0,
    added_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (knowledge_id, file_id)
);

-- embedding has no fixed dimension: each embedding model gets its own partial
-- HNSW index (created by the app) over embedding::vector(dim).
CREATE TABLE chunk (
    id                 bigserial PRIMARY KEY,
    knowledge_id       text     NOT NULL REFERENCES knowledge (id) ON DELETE CASCADE,
    file_id            text     NOT NULL REFERENCES file (id) ON DELETE CASCADE,
    idx                integer  NOT NULL,
    text               text     NOT NULL,
    meta               jsonb    NOT NULL DEFAULT '{}',
    fts_config         regconfig NOT NULL DEFAULT 'simple',
    tsv                tsvector GENERATED ALWAYS AS (to_tsvector(fts_config, text)) STORED,
    embedding          vector,
    embedding_model_id text REFERENCES embedding_model (id)
);
CREATE INDEX chunk_knowledge_file ON chunk (knowledge_id, file_id);
CREATE INDEX chunk_tsv ON chunk USING gin (tsv);

CREATE TABLE memory (
    id                 text PRIMARY KEY,
    user_id            text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    content            text        NOT NULL,
    embedding          vector,
    embedding_model_id text REFERENCES embedding_model (id),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX memory_user ON memory (user_id);

-- Misc ------------------------------------------------------------------------------

CREATE TABLE prompt (
    id         text PRIMARY KEY,
    user_id    text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    command    text        NOT NULL,
    title      text        NOT NULL DEFAULT '',
    content    text        NOT NULL DEFAULT '',
    origin     text        NOT NULL DEFAULT 'kera',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, command)
);

CREATE TABLE feedback (
    id         text PRIMARY KEY,
    user_id    text        NOT NULL REFERENCES "user" (id) ON DELETE CASCADE,
    type       text        NOT NULL DEFAULT 'rating',
    data       jsonb       NOT NULL DEFAULT '{}',
    meta       jsonb       NOT NULL DEFAULT '{}',
    snapshot   jsonb       NOT NULL DEFAULT '{}',
    origin     text        NOT NULL DEFAULT 'kera',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE usage (
    id                bigserial PRIMARY KEY,
    user_id           text        NOT NULL,
    model_id          text        NOT NULL,
    chat_id           text,
    message_id        text,
    prompt_tokens     integer     NOT NULL DEFAULT 0,
    completion_tokens integer     NOT NULL DEFAULT 0,
    duration_ms       integer     NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX usage_user_created ON usage (user_id, created_at);
CREATE INDEX usage_model_created ON usage (model_id, created_at);

CREATE TABLE migration_run (
    id          bigserial PRIMARY KEY,
    source      text        NOT NULL,
    flags       jsonb       NOT NULL DEFAULT '{}',
    counts      jsonb       NOT NULL DEFAULT '{}',
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    error       text        NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE IF EXISTS migration_run, usage, feedback, prompt, memory, chunk, knowledge_file, knowledge,
    embedding_model, chat_file, file, shared_chat, chat_tag, message, chat, folder, hook_server,
    tool_server, model, connection, config, access_grant, group_member, "group", oauth_token,
    oauth_provider, api_key, auth_password, "user" CASCADE;
