-- 0200 ai: routing pins for more scopes (series, job type) and mode pins,
-- per-member routing overrides, manifest change tracking, and richer routing decisions.

-- Pins: scope_kind server | project | series | character | panel_type | job_type.
-- pin_type 'model' forces model_id (optionally provider_id); 'mode' forces a routing mode.
CREATE TABLE routing_pins_v2 (
    id          TEXT PRIMARY KEY,
    scope_kind  TEXT NOT NULL CHECK (scope_kind IN ('server', 'project', 'series', 'character', 'panel_type', 'job_type')),
    scope_id    TEXT NOT NULL DEFAULT '',
    task        TEXT NOT NULL DEFAULT '',
    pin_type    TEXT NOT NULL DEFAULT 'model' CHECK (pin_type IN ('model', 'mode')),
    model_id    TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    mode        TEXT NOT NULL DEFAULT '',
    label       TEXT NOT NULL DEFAULT '',
    created_by  TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO routing_pins_v2 (id, scope_kind, scope_id, task, pin_type, model_id, provider_id, created_by, created_at)
    SELECT id, scope_kind, scope_id, task, 'model', model_id, provider_id, created_by, created_at FROM routing_pins;
DROP TABLE routing_pins;
ALTER TABLE routing_pins_v2 RENAME TO routing_pins;
CREATE UNIQUE INDEX routing_pins_scope_uq ON routing_pins (scope_kind, scope_id, task, pin_type);

-- A member's own routing mode (overrides the instance default; project and series pins win over it).
CREATE TABLE ai_user_prefs (
    user_id      TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    routing_mode TEXT NOT NULL DEFAULT '',
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Last seen fingerprint of every manifest, to list catalogue changes for admins.
CREATE TABLE ai_manifest_state (
    model_id   TEXT PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    source     TEXT NOT NULL DEFAULT '',
    hash       TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ai_manifest_changes (
    id              TEXT PRIMARY KEY,
    model_id        TEXT NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    change          TEXT NOT NULL CHECK (change IN ('added', 'updated', 'removed')),
    source          TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    acknowledged_at TIMESTAMP
);
CREATE INDEX ai_manifest_changes_created_idx ON ai_manifest_changes (created_at);

ALTER TABLE routing_decisions ADD COLUMN label TEXT NOT NULL DEFAULT '';
ALTER TABLE routing_decisions ADD COLUMN need TEXT NOT NULL DEFAULT '{}';
ALTER TABLE routing_decisions ADD COLUMN why TEXT NOT NULL DEFAULT '[]';
ALTER TABLE routing_decisions ADD COLUMN status TEXT NOT NULL DEFAULT '';
