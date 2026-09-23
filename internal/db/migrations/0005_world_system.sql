-- 0005 World link, packs, plugins, backups, updates, imports, collaborative page documents.

-- scope 'server' (user_id NULL) is the instance link; scope 'user' links one member's World account.
CREATE TABLE world_links (
    id               TEXT PRIMARY KEY,
    scope            TEXT NOT NULL CHECK (scope IN ('server', 'user')),
    user_id          TEXT REFERENCES users (id) ON DELETE CASCADE,
    world_account_id TEXT NOT NULL DEFAULT '',
    handle           TEXT NOT NULL DEFAULT '',
    display_name     TEXT NOT NULL DEFAULT '',
    token_enc        TEXT NOT NULL DEFAULT '',
    refresh_enc      TEXT NOT NULL DEFAULT '',
    scopes           TEXT NOT NULL DEFAULT '[]',
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'linked', 'revoked')),
    device_code_enc  TEXT NOT NULL DEFAULT '',
    linked_at        TIMESTAMP,
    expires_at       TIMESTAMP,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX world_links_user_idx ON world_links (scope, user_id);

-- Singleton row (id = 'server'): the per-server signing key used for World submissions.
CREATE TABLE world_server (
    id              TEXT PRIMARY KEY,
    server_id       TEXT NOT NULL DEFAULT '',
    key_alg         TEXT NOT NULL DEFAULT 'ed25519',
    public_key      TEXT NOT NULL DEFAULT '',
    private_key_enc TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'unlinked' CHECK (status IN ('unlinked', 'pending', 'linked', 'revoked')),
    linked_at       TIMESTAMP,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE world_submissions (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    item_kind     TEXT NOT NULL CHECK (item_kind IN ('character', 'template', 'style', 'series', 'plugin')),
    item_id       TEXT NOT NULL,
    world_item_id TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'uploading', 'checks', 'in_review', 'changes_requested', 'approved', 'rejected', 'appealed', 'withdrawn')),
    manifest      TEXT NOT NULL DEFAULT '{}',
    checks        TEXT NOT NULL DEFAULT '{}',
    review_notes  TEXT NOT NULL DEFAULT '',
    submitted_at  TIMESTAMP,
    decided_at    TIMESTAMP,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX world_submissions_user_idx ON world_submissions (user_id, created_at);

-- Items the member owns on World (free or acquired there). No prices are stored or shown.
CREATE TABLE world_library (
    id                TEXT PRIMARY KEY,
    user_id           TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    world_item_id     TEXT NOT NULL,
    kind              TEXT NOT NULL,
    title             TEXT NOT NULL DEFAULT '',
    creator_handle    TEXT NOT NULL DEFAULT '',
    ownership         TEXT NOT NULL DEFAULT 'free' CHECK (ownership IN ('free', 'owned')),
    licence           TEXT NOT NULL DEFAULT '{}',
    licence_token_enc TEXT NOT NULL DEFAULT '',
    imported_at       TIMESTAMP,
    imported_ref      TEXT NOT NULL DEFAULT '{}',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX world_library_uq ON world_library (user_id, world_item_id);

CREATE TABLE packs (
    id           TEXT PRIMARY KEY,
    owner_id     TEXT REFERENCES users (id) ON DELETE SET NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('character', 'style', 'template', 'mixed')),
    name         TEXT NOT NULL,
    version      TEXT NOT NULL DEFAULT '',
    author       TEXT NOT NULL DEFAULT '',
    licence      TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL DEFAULT 'file' CHECK (source IN ('file', 'world', 'builtin', 'export')),
    manifest     TEXT NOT NULL DEFAULT '{}',
    signature    TEXT NOT NULL DEFAULT '',
    signer       TEXT NOT NULL DEFAULT '',
    verified     BOOLEAN NOT NULL DEFAULT FALSE,
    asset_id     TEXT REFERENCES assets (id) ON DELETE SET NULL,
    sha256       TEXT NOT NULL DEFAULT '',
    installed_at TIMESTAMP,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX packs_owner_idx ON packs (owner_id, kind);

CREATE TABLE plugins (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    version      TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL DEFAULT 'model',
    manifest     TEXT NOT NULL DEFAULT '{}',
    permissions  TEXT NOT NULL DEFAULT '[]',
    enabled      BOOLEAN NOT NULL DEFAULT FALSE,
    source       TEXT NOT NULL DEFAULT 'file',
    signature    TEXT NOT NULL DEFAULT '',
    verified     BOOLEAN NOT NULL DEFAULT FALSE,
    installed_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    installed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE backups (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('scheduled', 'manual', 'pre_update', 'export', 'user_export')),
    status      TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed', 'restored', 'deleted')),
    path        TEXT NOT NULL DEFAULT '',
    bytes       BIGINT NOT NULL DEFAULT 0,
    sha256      TEXT NOT NULL DEFAULT '',
    includes    TEXT NOT NULL DEFAULT '{}',
    app_version TEXT NOT NULL DEFAULT '',
    schema      TEXT NOT NULL DEFAULT '',
    user_id     TEXT REFERENCES users (id) ON DELETE CASCADE,
    created_by  TEXT REFERENCES users (id) ON DELETE SET NULL,
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMP,
    expires_at  TIMESTAMP
);
CREATE INDEX backups_created_idx ON backups (kind, created_at);

CREATE TABLE updates (
    id           TEXT PRIMARY KEY,
    from_version TEXT NOT NULL,
    to_version   TEXT NOT NULL,
    channel      TEXT NOT NULL DEFAULT 'stable',
    status       TEXT NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'downloading', 'verifying', 'backing_up', 'migrating', 'health_check', 'done', 'failed', 'rolled_back')),
    changelog    TEXT NOT NULL DEFAULT '',
    download_url TEXT NOT NULL DEFAULT '',
    signature    TEXT NOT NULL DEFAULT '',
    backup_id    TEXT REFERENCES backups (id) ON DELETE SET NULL,
    steps        TEXT NOT NULL DEFAULT '[]',
    error        TEXT NOT NULL DEFAULT '',
    started_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    started_at   TIMESTAMP,
    finished_at  TIMESTAMP,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX updates_created_idx ON updates (created_at);

CREATE TABLE imports (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects (id) ON DELETE SET NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('cbz', 'cbr', 'pdf', 'images', 'mvpack', 'mvbackup')),
    filename   TEXT NOT NULL DEFAULT '',
    asset_id   TEXT REFERENCES assets (id) ON DELETE SET NULL,
    status     TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed', 'canceled')),
    progress   DOUBLE PRECISION NOT NULL DEFAULT 0,
    job_id     TEXT REFERENCES jobs (id) ON DELETE SET NULL,
    pages      INTEGER NOT NULL DEFAULT 0,
    result     TEXT NOT NULL DEFAULT '{}',
    error      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX imports_user_idx ON imports (user_id, created_at);

-- Yjs update log per page document (see internal/ws). A 'snapshot' row
-- replaces every older row of the same page when a client compacts.
CREATE TABLE page_doc_updates (
    id         TEXT PRIMARY KEY,
    page_id    TEXT NOT NULL REFERENCES pages (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL DEFAULT 'update' CHECK (kind IN ('update', 'snapshot')),
    data       BLOB NOT NULL,
    user_id    TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX page_doc_updates_page_idx ON page_doc_updates (page_id, id);
