-- 0002 studio: projects, story structure, characters, pages, panels, layers,
-- lettering, templates, versions, review comments, continuity, glossaries.

CREATE TABLE projects (
    id             TEXT PRIMARY KEY,
    owner_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title          TEXT NOT NULL,
    slug           TEXT NOT NULL,
    premise        TEXT NOT NULL DEFAULT '',
    logline        TEXT NOT NULL DEFAULT '',
    genres         TEXT NOT NULL DEFAULT '[]',
    tags           TEXT NOT NULL DEFAULT '[]',
    style          TEXT NOT NULL DEFAULT 'bw_screentone',
    format         TEXT NOT NULL DEFAULT 'manga_rtl' CHECK (format IN ('manga_rtl', 'comic_ltr', 'webtoon')),
    page_size      TEXT NOT NULL DEFAULT 'b5',
    page_width     INTEGER NOT NULL DEFAULT 1600,
    page_height    INTEGER NOT NULL DEFAULT 2400,
    language       TEXT NOT NULL DEFAULT 'en',
    status         TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
    cover_asset_id TEXT REFERENCES assets (id) ON DELETE SET NULL,
    template_id    TEXT,
    settings       TEXT NOT NULL DEFAULT '{}',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at    TIMESTAMP
);
CREATE UNIQUE INDEX projects_owner_slug_uq ON projects (owner_id, slug);
CREATE INDEX projects_updated_idx ON projects (owner_id, updated_at);

CREATE TABLE project_members (
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('owner', 'writer', 'artist', 'letterer', 'editor', 'viewer')),
    scope      TEXT NOT NULL DEFAULT '{}',
    invited_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX project_members_user_idx ON project_members (user_id);

CREATE TABLE arcs (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    title      TEXT NOT NULL,
    summary    TEXT NOT NULL DEFAULT '',
    color      TEXT NOT NULL DEFAULT '',
    order_idx  INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX arcs_project_idx ON arcs (project_id, order_idx);

CREATE TABLE chapters (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    arc_id     TEXT REFERENCES arcs (id) ON DELETE SET NULL,
    number     INTEGER NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'scripting', 'drawing', 'lettering', 'review', 'ready', 'published')),
    locked     BOOLEAN NOT NULL DEFAULT FALSE,
    ai_label   TEXT NOT NULL DEFAULT '{}',
    settings   TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX chapters_project_number_uq ON chapters (project_id, number);

CREATE TABLE characters (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    kanji               TEXT NOT NULL DEFAULT '',
    role                TEXT NOT NULL DEFAULT '',
    role_line           TEXT NOT NULL DEFAULT '',
    age                 TEXT NOT NULL DEFAULT '',
    look                TEXT NOT NULL DEFAULT '',
    personality         TEXT NOT NULL DEFAULT '',
    speech_style        TEXT NOT NULL DEFAULT '',
    backstory           TEXT NOT NULL DEFAULT '',
    voice               TEXT NOT NULL DEFAULT '{}',
    color               TEXT NOT NULL DEFAULT '',
    category            TEXT NOT NULL DEFAULT 'supporting' CHECK (category IN ('main', 'supporting', 'extra')),
    lock_status         TEXT NOT NULL DEFAULT 'none' CHECK (lock_status IN ('none', 'training', 'ready', 'failed')),
    lock_score          DOUBLE PRECISION,
    trigger_word        TEXT NOT NULL DEFAULT '',
    lora_asset_id       TEXT REFERENCES assets (id) ON DELETE SET NULL,
    portrait_asset_id   TEXT REFERENCES assets (id) ON DELETE SET NULL,
    pack_source         TEXT NOT NULL DEFAULT '{}',
    order_idx           INTEGER NOT NULL DEFAULT 0,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX characters_project_idx ON characters (project_id, order_idx);

CREATE TABLE character_refs (
    id           TEXT PRIMARY KEY,
    character_id TEXT NOT NULL REFERENCES characters (id) ON DELETE CASCADE,
    asset_id     TEXT NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    kind         TEXT NOT NULL DEFAULT 'other' CHECK (kind IN ('front', 'side', 'back', 'face', 'expression', 'outfit', 'sheet', 'other')),
    label        TEXT NOT NULL DEFAULT '',
    order_idx    INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX character_refs_character_idx ON character_refs (character_id, order_idx);

CREATE TABLE character_outfits (
    id           TEXT PRIMARY KEY,
    character_id TEXT NOT NULL REFERENCES characters (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    asset_id     TEXT REFERENCES assets (id) ON DELETE SET NULL,
    is_default   BOOLEAN NOT NULL DEFAULT FALSE,
    order_idx    INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX character_outfits_character_idx ON character_outfits (character_id, order_idx);

CREATE TABLE character_relations (
    id           TEXT PRIMARY KEY,
    character_id TEXT NOT NULL REFERENCES characters (id) ON DELETE CASCADE,
    other_id     TEXT NOT NULL REFERENCES characters (id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    note         TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX character_relations_uq ON character_relations (character_id, other_id, kind);

CREATE TABLE setting_items (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('location', 'prop', 'style')),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    tags        TEXT NOT NULL DEFAULT '[]',
    asset_id    TEXT REFERENCES assets (id) ON DELETE SET NULL,
    refs        TEXT NOT NULL DEFAULT '[]',
    data        TEXT NOT NULL DEFAULT '{}',
    order_idx   INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX setting_items_project_idx ON setting_items (project_id, kind, order_idx);

CREATE TABLE scenes (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    chapter_id  TEXT NOT NULL REFERENCES chapters (id) ON DELETE CASCADE,
    order_idx   INTEGER NOT NULL DEFAULT 0,
    title       TEXT NOT NULL DEFAULT '',
    summary     TEXT NOT NULL DEFAULT '',
    location_id TEXT REFERENCES setting_items (id) ON DELETE SET NULL,
    time_of_day TEXT NOT NULL DEFAULT '',
    beats       TEXT NOT NULL DEFAULT '[]',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX scenes_chapter_idx ON scenes (chapter_id, order_idx);

-- Panel-by-panel script. label is the human numbering, e.g. "7.3" (page 7, panel 3).
CREATE TABLE script_panels (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    chapter_id    TEXT NOT NULL REFERENCES chapters (id) ON DELETE CASCADE,
    scene_id      TEXT REFERENCES scenes (id) ON DELETE CASCADE,
    page_number   INTEGER NOT NULL DEFAULT 0,
    order_idx     INTEGER NOT NULL DEFAULT 0,
    label         TEXT NOT NULL DEFAULT '',
    camera        TEXT NOT NULL DEFAULT '',
    shot          TEXT NOT NULL DEFAULT '',
    emotion       TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL DEFAULT '',
    dialogue      TEXT NOT NULL DEFAULT '[]',
    sfx           TEXT NOT NULL DEFAULT '[]',
    character_ids TEXT NOT NULL DEFAULT '[]',
    notes         TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX script_panels_chapter_idx ON script_panels (chapter_id, page_number, order_idx);
CREATE INDEX script_panels_scene_idx ON script_panels (scene_id, order_idx);

CREATE TABLE pages (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    chapter_id     TEXT NOT NULL REFERENCES chapters (id) ON DELETE CASCADE,
    number         INTEGER NOT NULL,
    layout_id      TEXT NOT NULL DEFAULT '',
    size           TEXT NOT NULL DEFAULT '',
    width          INTEGER NOT NULL DEFAULT 1600,
    height         INTEGER NOT NULL DEFAULT 2400,
    status         TEXT NOT NULL DEFAULT 'draft',
    locked         BOOLEAN NOT NULL DEFAULT FALSE,
    locked_by      TEXT REFERENCES users (id) ON DELETE SET NULL,
    locked_at      TIMESTAMP,
    version        INTEGER NOT NULL DEFAULT 1,
    branch_id      TEXT,
    thumb_asset_id TEXT REFERENCES assets (id) ON DELETE SET NULL,
    art_asset_id   TEXT REFERENCES assets (id) ON DELETE SET NULL,
    notes          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX pages_chapter_idx ON pages (chapter_id, number);
CREATE INDEX pages_project_idx ON pages (project_id);

-- points: polygon in page percent, e.g. [[0,0],[100,0],[100,40],[0,35]].
CREATE TABLE panels (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    page_id         TEXT NOT NULL REFERENCES pages (id) ON DELETE CASCADE,
    order_idx       INTEGER NOT NULL DEFAULT 0,
    points          TEXT NOT NULL DEFAULT '[]',
    border          TEXT NOT NULL DEFAULT '{}',
    script_panel_id TEXT REFERENCES script_panels (id) ON DELETE SET NULL,
    prompt          TEXT NOT NULL DEFAULT '',
    prompt_source   TEXT NOT NULL DEFAULT 'script' CHECK (prompt_source IN ('script', 'user', 'agent')),
    camera          TEXT NOT NULL DEFAULT '',
    shot            TEXT NOT NULL DEFAULT '',
    pose            TEXT NOT NULL DEFAULT '{}',
    style           TEXT NOT NULL DEFAULT '',
    locked          BOOLEAN NOT NULL DEFAULT FALSE,
    locked_by       TEXT REFERENCES users (id) ON DELETE SET NULL,
    locked_at       TIMESTAMP,
    alt_text        TEXT NOT NULL DEFAULT '',
    review_status   TEXT NOT NULL DEFAULT 'none' CHECK (review_status IN ('none', 'open', 'needs_changes', 'done', 'approved')),
    living          TEXT NOT NULL DEFAULT '{}',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX panels_page_idx ON panels (page_id, order_idx);

CREATE TABLE layers (
    id            TEXT PRIMARY KEY,
    panel_id      TEXT NOT NULL REFERENCES panels (id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('background', 'character', 'fx', 'lettering')),
    name          TEXT NOT NULL DEFAULT '',
    character_id  TEXT REFERENCES characters (id) ON DELETE SET NULL,
    asset_id      TEXT REFERENCES assets (id) ON DELETE SET NULL,
    mask_asset_id TEXT REFERENCES assets (id) ON DELETE SET NULL,
    order_idx     INTEGER NOT NULL DEFAULT 0,
    visible       BOOLEAN NOT NULL DEFAULT TRUE,
    locked        BOOLEAN NOT NULL DEFAULT FALSE,
    opacity       DOUBLE PRECISION NOT NULL DEFAULT 1,
    blend         TEXT NOT NULL DEFAULT 'normal',
    transform     TEXT NOT NULL DEFAULT '{}',
    provenance    TEXT NOT NULL DEFAULT 'human' CHECK (provenance IN ('ai', 'human', 'mixed')),
    model_id      TEXT NOT NULL DEFAULT '',
    provider_id   TEXT NOT NULL DEFAULT '',
    job_id        TEXT,
    seed          BIGINT,
    prompt        TEXT NOT NULL DEFAULT '',
    lock_score    DOUBLE PRECISION,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX layers_panel_idx ON layers (panel_id, order_idx);
CREATE INDEX layers_character_idx ON layers (character_id);

-- geometry: {x, y, w, h} in page percent; tail: {x, y, curve}; style: font/size/stroke overrides.
CREATE TABLE bubbles (
    id                   TEXT PRIMARY KEY,
    page_id              TEXT NOT NULL REFERENCES pages (id) ON DELETE CASCADE,
    panel_id             TEXT REFERENCES panels (id) ON DELETE SET NULL,
    kind                 TEXT NOT NULL DEFAULT 'speech' CHECK (kind IN ('speech', 'thought', 'shout', 'whisper', 'caption', 'sfx')),
    speaker_character_id TEXT REFERENCES characters (id) ON DELETE SET NULL,
    script_panel_id      TEXT REFERENCES script_panels (id) ON DELETE SET NULL,
    order_idx            INTEGER NOT NULL DEFAULT 0,
    geometry             TEXT NOT NULL DEFAULT '{}',
    tail                 TEXT NOT NULL DEFAULT '{}',
    style                TEXT NOT NULL DEFAULT '{}',
    vertical             BOOLEAN NOT NULL DEFAULT FALSE,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX bubbles_page_idx ON bubbles (page_id, order_idx);
CREATE INDEX bubbles_panel_idx ON bubbles (panel_id);

CREATE TABLE bubble_texts (
    id         TEXT PRIMARY KEY,
    bubble_id  TEXT NOT NULL REFERENCES bubbles (id) ON DELETE CASCADE,
    lang       TEXT NOT NULL,
    text       TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'machine', 'reviewed', 'approved', 'overflow')),
    source     TEXT NOT NULL DEFAULT 'human' CHECK (source IN ('human', 'ai')),
    fit        TEXT NOT NULL DEFAULT '{}',
    updated_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX bubble_texts_uq ON bubble_texts (bubble_id, lang);

CREATE TABLE templates (
    id          TEXT PRIMARY KEY,
    owner_id    TEXT REFERENCES users (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('story', 'layout')),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    tags        TEXT NOT NULL DEFAULT '[]',
    data        TEXT NOT NULL DEFAULT '{}',
    builtin     BOOLEAN NOT NULL DEFAULT FALSE,
    shared      BOOLEAN NOT NULL DEFAULT FALSE,
    pack_source TEXT NOT NULL DEFAULT '{}',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX templates_kind_idx ON templates (kind, builtin);
CREATE INDEX templates_owner_idx ON templates (owner_id);

CREATE TABLE branches (
    id               TEXT PRIMARY KEY,
    page_id          TEXT NOT NULL REFERENCES pages (id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    parent_branch_id TEXT REFERENCES branches (id) ON DELETE SET NULL,
    head_version_id  TEXT,
    created_by       TEXT REFERENCES users (id) ON DELETE SET NULL,
    merged_into_id   TEXT REFERENCES branches (id) ON DELETE SET NULL,
    merged_at        TIMESTAMP,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX branches_page_name_uq ON branches (page_id, name);

-- Page snapshots. snapshot is the full page document JSON (panels, layers, bubbles).
CREATE TABLE versions (
    id         TEXT PRIMARY KEY,
    page_id    TEXT NOT NULL REFERENCES pages (id) ON DELETE CASCADE,
    branch_id  TEXT REFERENCES branches (id) ON DELETE CASCADE,
    parent_id  TEXT REFERENCES versions (id) ON DELETE SET NULL,
    number     INTEGER NOT NULL DEFAULT 1,
    actor_kind TEXT NOT NULL DEFAULT 'user' CHECK (actor_kind IN ('user', 'agent', 'system')),
    actor_id   TEXT REFERENCES users (id) ON DELETE SET NULL,
    token_id   TEXT,
    job_id     TEXT,
    action     TEXT NOT NULL DEFAULT '',
    scope      TEXT NOT NULL DEFAULT '{}',
    summary    TEXT NOT NULL DEFAULT '',
    snapshot   TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX versions_page_idx ON versions (page_id, created_at);
CREATE INDEX versions_branch_idx ON versions (branch_id, created_at);

-- Review comments (internal) and reader comments (public). anchor pins a
-- comment to a point/panel: {"panel_id": "...", "x": 42.1, "y": 10.5}.
CREATE TABLE comments (
    id          TEXT PRIMARY KEY,
    project_id  TEXT REFERENCES projects (id) ON DELETE CASCADE,
    series_id   TEXT,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('chapter', 'page', 'panel', 'series')),
    target_id   TEXT NOT NULL,
    anchor      TEXT NOT NULL DEFAULT '{}',
    visibility  TEXT NOT NULL DEFAULT 'internal' CHECK (visibility IN ('internal', 'public')),
    author_id   TEXT REFERENCES users (id) ON DELETE SET NULL,
    parent_id   TEXT REFERENCES comments (id) ON DELETE CASCADE,
    body        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'needs_changes', 'done', 'approved')),
    resolved_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    resolved_at TIMESTAMP,
    edited_at   TIMESTAMP,
    deleted_at  TIMESTAMP,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX comments_target_idx ON comments (target_kind, target_id, created_at);
CREATE INDEX comments_project_idx ON comments (project_id, status);
CREATE INDEX comments_parent_idx ON comments (parent_id);
CREATE INDEX comments_series_idx ON comments (series_id, created_at);

CREATE TABLE continuity_issues (
    id           TEXT PRIMARY KEY,
    project_id   TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    chapter_id   TEXT REFERENCES chapters (id) ON DELETE CASCADE,
    scene_id     TEXT REFERENCES scenes (id) ON DELETE SET NULL,
    page_id      TEXT REFERENCES pages (id) ON DELETE SET NULL,
    panel_id     TEXT REFERENCES panels (id) ON DELETE SET NULL,
    character_id TEXT REFERENCES characters (id) ON DELETE SET NULL,
    kind         TEXT NOT NULL DEFAULT 'other' CHECK (kind IN ('outfit', 'prop', 'injury', 'time_of_day', 'location', 'character', 'other')),
    severity     TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'error')),
    message      TEXT NOT NULL,
    evidence     TEXT NOT NULL DEFAULT '{}',
    status       TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'dismissed', 'fixed')),
    job_id       TEXT,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at  TIMESTAMP
);
CREATE INDEX continuity_issues_chapter_idx ON continuity_issues (chapter_id, status);

CREATE TABLE glossaries (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    lang           TEXT NOT NULL,
    source_text    TEXT NOT NULL,
    target_text    TEXT NOT NULL DEFAULT '',
    kind           TEXT NOT NULL DEFAULT 'term' CHECK (kind IN ('name', 'term', 'sfx', 'place')),
    note           TEXT NOT NULL DEFAULT '',
    case_sensitive BOOLEAN NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX glossaries_uq ON glossaries (project_id, lang, source_text);
