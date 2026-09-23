-- 0003 AI and jobs: providers, model manifests, routing, spend, workers, job queue.

-- key_enc is sealed with the secrets box and never returned by the API.
CREATE TABLE providers (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL,
    base_url   TEXT NOT NULL DEFAULT '',
    key_enc    TEXT NOT NULL DEFAULT '',
    key_hint   TEXT NOT NULL DEFAULT '',
    config     TEXT NOT NULL DEFAULT '{}',
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    scope      TEXT NOT NULL DEFAULT 'server' CHECK (scope IN ('server', 'user')),
    owner_id   TEXT REFERENCES users (id) ON DELETE CASCADE,
    last_test  TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX providers_scope_idx ON providers (scope, owner_id);

-- Custom connections and overrides of the embedded manifest catalogue.
CREATE TABLE models (
    id          TEXT PRIMARY KEY,
    source      TEXT NOT NULL DEFAULT 'custom' CHECK (source IN ('builtin', 'file', 'custom', 'plugin')),
    manifest    TEXT NOT NULL DEFAULT '{}',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    provider_id TEXT REFERENCES providers (id) ON DELETE CASCADE,
    owner_id    TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- scope_kind: server | project | character | panel_type; scope_id '' for server-wide pins.
CREATE TABLE routing_pins (
    id          TEXT PRIMARY KEY,
    scope_kind  TEXT NOT NULL CHECK (scope_kind IN ('server', 'project', 'character', 'panel_type')),
    scope_id    TEXT NOT NULL DEFAULT '',
    task        TEXT NOT NULL,
    model_id    TEXT NOT NULL,
    provider_id TEXT NOT NULL DEFAULT '',
    created_by  TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX routing_pins_uq ON routing_pins (scope_kind, scope_id, task);

CREATE TABLE model_stats (
    model_id    TEXT NOT NULL,
    provider_id TEXT NOT NULL DEFAULT '',
    task        TEXT NOT NULL DEFAULT '',
    runs        BIGINT NOT NULL DEFAULT 0,
    failures    BIGINT NOT NULL DEFAULT 0,
    keeps       BIGINT NOT NULL DEFAULT 0,
    rerolls     BIGINT NOT NULL DEFAULT 0,
    total_secs  DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_usd   DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (model_id, provider_id, task)
);

-- token_key is the api_tokens id, or 'env' for the MV_WORKER_TOKEN shared secret.
CREATE TABLE workers (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    token_key      TEXT NOT NULL,
    version        TEXT NOT NULL DEFAULT '',
    backends       TEXT NOT NULL DEFAULT '[]',
    gpus           TEXT NOT NULL DEFAULT '[]',
    job_types      TEXT NOT NULL DEFAULT '[]',
    models         TEXT NOT NULL DEFAULT '[]',
    max_concurrent INTEGER NOT NULL DEFAULT 1,
    status         TEXT NOT NULL DEFAULT 'online' CHECK (status IN ('online', 'offline', 'draining')),
    ip             TEXT NOT NULL DEFAULT '',
    last_seen_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    registered_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX workers_token_name_uq ON workers (token_key, name);
CREATE INDEX workers_status_idx ON workers (status, last_seen_at);

-- The job queue (see internal/jobs). scope holds the full scope JSON; the
-- scope ids are denormalised for filtering and lock checks.
CREATE TABLE jobs (
    id               TEXT PRIMARY KEY,
    type             TEXT NOT NULL,
    scope            TEXT NOT NULL DEFAULT '{}',
    scope_kind       TEXT NOT NULL DEFAULT '',
    project_id       TEXT REFERENCES projects (id) ON DELETE CASCADE,
    chapter_id       TEXT,
    page_id          TEXT,
    panel_id         TEXT,
    layer_id         TEXT,
    user_id          TEXT REFERENCES users (id) ON DELETE SET NULL,
    state            TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed', 'canceled')),
    progress         DOUBLE PRECISION NOT NULL DEFAULT 0,
    step             INTEGER NOT NULL DEFAULT 0,
    steps            INTEGER NOT NULL DEFAULT 0,
    message          TEXT NOT NULL DEFAULT '',
    executor         TEXT NOT NULL DEFAULT 'go' CHECK (executor IN ('go', 'worker')),
    model_id         TEXT NOT NULL DEFAULT '',
    provider_id      TEXT NOT NULL DEFAULT '',
    decision         TEXT NOT NULL DEFAULT '{}',
    input            TEXT NOT NULL DEFAULT '{}',
    files            TEXT NOT NULL DEFAULT '{}',
    output           TEXT NOT NULL DEFAULT '{}',
    error            TEXT NOT NULL DEFAULT '{}',
    estimate         TEXT NOT NULL DEFAULT '{}',
    actual           TEXT NOT NULL DEFAULT '{}',
    attempts         INTEGER NOT NULL DEFAULT 0,
    max_attempts     INTEGER NOT NULL DEFAULT 3,
    parent_id        TEXT REFERENCES jobs (id) ON DELETE SET NULL,
    priority         INTEGER NOT NULL DEFAULT 0,
    batch            BOOLEAN NOT NULL DEFAULT FALSE,
    worker_id        TEXT REFERENCES workers (id) ON DELETE SET NULL,
    lease_expires_at TIMESTAMP,
    run_after        TIMESTAMP,
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at       TIMESTAMP,
    finished_at      TIMESTAMP
);
CREATE INDEX jobs_claim_idx ON jobs (state, executor, priority, created_at);
CREATE INDEX jobs_user_idx ON jobs (user_id, state);
CREATE INDEX jobs_project_idx ON jobs (project_id, created_at);
CREATE INDEX jobs_page_idx ON jobs (page_id, state);
CREATE INDEX jobs_worker_idx ON jobs (worker_id, state);
CREATE INDEX jobs_parent_idx ON jobs (parent_id);
CREATE INDEX jobs_lease_idx ON jobs (state, lease_expires_at);

CREATE TABLE job_events (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    message    TEXT NOT NULL DEFAULT '',
    data       TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX job_events_job_idx ON job_events (job_id, id);

CREATE TABLE routing_decisions (
    id                 TEXT PRIMARY KEY,
    job_id             TEXT REFERENCES jobs (id) ON DELETE CASCADE,
    user_id            TEXT REFERENCES users (id) ON DELETE SET NULL,
    project_id         TEXT,
    task               TEXT NOT NULL,
    mode               TEXT NOT NULL DEFAULT 'balanced',
    chosen_model_id    TEXT NOT NULL DEFAULT '',
    chosen_provider_id TEXT NOT NULL DEFAULT '',
    candidates         TEXT NOT NULL DEFAULT '[]',
    explanation        TEXT NOT NULL DEFAULT '',
    overridden         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX routing_decisions_job_idx ON routing_decisions (job_id);
CREATE INDEX routing_decisions_created_idx ON routing_decisions (created_at);

-- Remote spend on the users' own keys and local GPU time; month is 'YYYY-MM' (UTC).
CREATE TABLE spend_ledger (
    id          TEXT PRIMARY KEY,
    user_id     TEXT REFERENCES users (id) ON DELETE SET NULL,
    project_id  TEXT,
    job_id      TEXT REFERENCES jobs (id) ON DELETE SET NULL,
    provider_id TEXT NOT NULL DEFAULT '',
    model_id    TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL DEFAULT 'remote' CHECK (kind IN ('remote', 'gpu')),
    usd         DOUBLE PRECISION NOT NULL DEFAULT 0,
    gpu_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    month       TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX spend_ledger_user_month_idx ON spend_ledger (user_id, month);
CREATE INDEX spend_ledger_provider_month_idx ON spend_ledger (provider_id, month);
CREATE INDEX spend_ledger_month_idx ON spend_ledger (month);
