-- 0001 core: accounts, sessions, tokens, settings, audit log, assets.
-- Portable SQL (SQLite + Postgres). Ids are lowercase ULIDs (TEXT), times are UTC,
-- JSON columns are TEXT holding JSON, BLOB is rewritten to BYTEA for Postgres.

CREATE TABLE users (
    id                  TEXT PRIMARY KEY,
    username            TEXT NOT NULL,
    email               TEXT,
    email_verified_at   TIMESTAMP,
    display_name        TEXT NOT NULL DEFAULT '',
    password_hash       TEXT NOT NULL DEFAULT '',
    role                TEXT NOT NULL DEFAULT 'reader' CHECK (role IN ('admin', 'creator', 'reader')),
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'pending')),
    avatar_key          TEXT NOT NULL DEFAULT '',
    banner_key          TEXT NOT NULL DEFAULT '',
    bio                 TEXT NOT NULL DEFAULT '',
    links               TEXT NOT NULL DEFAULT '[]',
    totp_secret_enc     TEXT NOT NULL DEFAULT '',
    totp_enabled        BOOLEAN NOT NULL DEFAULT FALSE,
    totp_recovery       TEXT NOT NULL DEFAULT '[]',
    totp_last_step      BIGINT NOT NULL DEFAULT 0,
    locale              TEXT NOT NULL DEFAULT '',
    timezone            TEXT NOT NULL DEFAULT '',
    onboarding          TEXT NOT NULL DEFAULT '{}',
    xp                  BIGINT NOT NULL DEFAULT 0,
    level               INTEGER NOT NULL DEFAULT 1,
    invite_id           TEXT,
    suspended_reason    TEXT NOT NULL DEFAULT '',
    last_login_at       TIMESTAMP,
    password_changed_at TIMESTAMP,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX users_username_uq ON users (username);
CREATE UNIQUE INDEX users_email_uq ON users (email);
CREATE INDEX users_role_idx ON users (role, status);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL,
    remember     BOOLEAN NOT NULL DEFAULT FALSE,
    ip           TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX sessions_token_uq ON sessions (token_hash);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE invites (
    id         TEXT PRIMARY KEY,
    code       TEXT NOT NULL,
    email      TEXT NOT NULL DEFAULT '',
    role       TEXT NOT NULL DEFAULT 'creator' CHECK (role IN ('admin', 'creator', 'reader')),
    note       TEXT NOT NULL DEFAULT '',
    max_uses   INTEGER NOT NULL DEFAULT 1,
    uses       INTEGER NOT NULL DEFAULT 0,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    expires_at TIMESTAMP,
    revoked_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX invites_code_uq ON invites (code);

CREATE TABLE password_resets (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL,
    created_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    requested_ip TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   TIMESTAMP NOT NULL,
    used_at      TIMESTAMP
);
CREATE UNIQUE INDEX password_resets_token_uq ON password_resets (token_hash);
CREATE INDEX password_resets_user_idx ON password_resets (user_id);

CREATE TABLE api_tokens (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('agent', 'worker', 'api')),
    name             TEXT NOT NULL DEFAULT '',
    prefix           TEXT NOT NULL DEFAULT '',
    token_hash       TEXT NOT NULL,
    scopes           TEXT NOT NULL DEFAULT '[]',
    project_ids      TEXT NOT NULL DEFAULT '[]',
    approve_over_usd DOUBLE PRECISION,
    last_used_at     TIMESTAMP,
    last_used_ip     TEXT NOT NULL DEFAULT '',
    expires_at       TIMESTAMP,
    revoked_at       TIMESTAMP,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX api_tokens_hash_uq ON api_tokens (token_hash);
CREATE INDEX api_tokens_user_idx ON api_tokens (user_id, kind);

CREATE TABLE instance_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE user_prefs (
    user_id    TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    prefs      TEXT NOT NULL DEFAULT '{}',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE audit_log (
    id          TEXT PRIMARY KEY,
    actor_id    TEXT REFERENCES users (id) ON DELETE SET NULL,
    actor_kind  TEXT NOT NULL DEFAULT 'user' CHECK (actor_kind IN ('user', 'agent', 'worker', 'system')),
    token_id    TEXT,
    action      TEXT NOT NULL,
    target_kind TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    project_id  TEXT,
    ip          TEXT NOT NULL DEFAULT '',
    data        TEXT NOT NULL DEFAULT '{}',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX audit_log_created_idx ON audit_log (created_at);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, created_at);
CREATE INDEX audit_log_target_idx ON audit_log (target_kind, target_id);
CREATE INDEX audit_log_project_idx ON audit_log (project_id, created_at);

-- Stored files (media store key + metadata). project_id is a soft reference
-- (projects reference assets for covers, so no FK cycle here).
CREATE TABLE assets (
    id           TEXT PRIMARY KEY,
    key          TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    bytes        BIGINT NOT NULL DEFAULT 0,
    width        INTEGER NOT NULL DEFAULT 0,
    height       INTEGER NOT NULL DEFAULT 0,
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    sha256       TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL DEFAULT 'upload',
    source       TEXT NOT NULL DEFAULT 'upload' CHECK (source IN ('upload', 'ai', 'worker', 'import', 'system', 'world', 'pack')),
    owner_id     TEXT REFERENCES users (id) ON DELETE SET NULL,
    project_id   TEXT,
    meta         TEXT NOT NULL DEFAULT '{}',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX assets_key_uq ON assets (key);
CREATE INDEX assets_owner_idx ON assets (owner_id, created_at);
CREATE INDEX assets_project_idx ON assets (project_id, kind);
CREATE INDEX assets_sha_idx ON assets (sha256);
