-- 0100 studio: columns and tables the studio module adds on top of the base
-- schema (0002): project visibility and routing defaults, wizard drafts,
-- scene prose, script references to setting items, Character Lock progress,
-- outfit chapter ranges and the script assistant's proposals.

ALTER TABLE projects ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private';
ALTER TABLE projects ADD COLUMN routing_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN source TEXT NOT NULL DEFAULT 'studio';
ALTER TABLE projects ADD COLUMN chapters_planned INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN pages_per_chapter TEXT NOT NULL DEFAULT '';

ALTER TABLE scenes ADD COLUMN prose TEXT NOT NULL DEFAULT '';

ALTER TABLE script_panels ADD COLUMN setting_ids TEXT NOT NULL DEFAULT '[]';

ALTER TABLE characters ADD COLUMN style TEXT NOT NULL DEFAULT '';
ALTER TABLE characters ADD COLUMN lock_job_id TEXT;
ALTER TABLE characters ADD COLUMN lock_info TEXT NOT NULL DEFAULT '{}';
ALTER TABLE characters ADD COLUMN sheet_job_id TEXT;

ALTER TABLE character_outfits ADD COLUMN chapters TEXT NOT NULL DEFAULT '';

-- New-project wizard drafts (B3), autosaved per step. data holds the answers.
CREATE TABLE project_drafts (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    step       TEXT NOT NULL DEFAULT 'premise',
    data       TEXT NOT NULL DEFAULT '{}',
    project_id TEXT REFERENCES projects (id) ON DELETE SET NULL,
    job_id     TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX project_drafts_user_idx ON project_drafts (user_id, updated_at);

-- Script assistant requests (C1) and the ScriptDiff each one proposed.
CREATE TABLE script_assists (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    chapter_id TEXT NOT NULL REFERENCES chapters (id) ON DELETE CASCADE,
    scene_id   TEXT REFERENCES scenes (id) ON DELETE SET NULL,
    user_id    TEXT REFERENCES users (id) ON DELETE SET NULL,
    request    TEXT NOT NULL,
    job_id     TEXT,
    status     TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'applied', 'dismissed', 'failed')),
    diff       TEXT NOT NULL DEFAULT '{}',
    error      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX script_assists_chapter_idx ON script_assists (chapter_id, created_at);
CREATE INDEX script_assists_job_idx ON script_assists (job_id);

CREATE INDEX pages_updated_idx ON pages (project_id, updated_at);
CREATE INDEX layers_character_score_idx ON layers (character_id, lock_score);
