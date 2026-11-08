-- Agent actions and requests (ADR 0013 §7, G13). One row per change an agent
-- made through MCP (script edit, character create or edit, queued generation)
-- and per request that waits for a person: a human-only action (publish,
-- delete, share, unlock) or generation estimated above the token's approval
-- threshold. user_id is the token owner. token_id is a soft reference so a
-- revoked token keeps its labelled history. detail holds the display facts and,
-- for a pending generation, the exact request a person approves; undo holds
-- the before/after field values an undo restores. A row is never deleted when
-- it is undone or decided: status moves done → undone or pending →
-- approved / declined.
CREATE TABLE agent_actions (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_id    TEXT NOT NULL,
    token_name  TEXT NOT NULL DEFAULT '',
    project_id  TEXT REFERENCES projects (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('script_edit', 'character_create', 'character_edit', 'generate', 'request')),
    action      TEXT NOT NULL DEFAULT '',
    target_kind TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL CHECK (status IN ('done', 'undone', 'pending', 'approved', 'declined')),
    detail      TEXT NOT NULL DEFAULT '{}',
    undo        TEXT NOT NULL DEFAULT '{}',
    job_id      TEXT,
    estimate    TEXT NOT NULL DEFAULT '{}',
    decided_by  TEXT REFERENCES users (id) ON DELETE SET NULL,
    decided_at  TIMESTAMP,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX agent_actions_user_idx ON agent_actions (user_id, created_at);
CREATE INDEX agent_actions_project_idx ON agent_actions (project_id, status, created_at);
CREATE INDEX agent_actions_token_idx ON agent_actions (token_id, status, created_at);
