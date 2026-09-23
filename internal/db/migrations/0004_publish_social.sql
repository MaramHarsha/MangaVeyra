-- 0004 publishing, reading, community, notifications, XP.

CREATE TABLE series (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    owner_id        TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    slug            TEXT NOT NULL,
    title           TEXT NOT NULL,
    synopsis        TEXT NOT NULL DEFAULT '',
    genres          TEXT NOT NULL DEFAULT '[]',
    tags            TEXT NOT NULL DEFAULT '[]',
    format          TEXT NOT NULL DEFAULT 'manga_rtl' CHECK (format IN ('manga_rtl', 'comic_ltr', 'webtoon')),
    age_rating      TEXT NOT NULL DEFAULT 'all' CHECK (age_rating IN ('all', 'teen', 'mature', 'adult')),
    licence         TEXT NOT NULL DEFAULT 'all_rights',
    visibility      TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('public', 'unlisted', 'members', 'private')),
    cover_asset_id  TEXT REFERENCES assets (id) ON DELETE SET NULL,
    banner_asset_id TEXT REFERENCES assets (id) ON DELETE SET NULL,
    support_links   TEXT NOT NULL DEFAULT '[]',
    ai_label        TEXT NOT NULL DEFAULT '{}',
    original_lang   TEXT NOT NULL DEFAULT 'en',
    languages       TEXT NOT NULL DEFAULT '[]',
    status          TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'ongoing', 'completed', 'hiatus')),
    follower_count  INTEGER NOT NULL DEFAULT 0,
    view_count      BIGINT NOT NULL DEFAULT 0,
    published_at    TIMESTAMP,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX series_project_uq ON series (project_id);
CREATE UNIQUE INDEX series_slug_uq ON series (slug);
CREATE INDEX series_visibility_idx ON series (visibility, published_at);
CREATE INDEX series_owner_idx ON series (owner_id);

CREATE TABLE chapter_publications (
    id             TEXT PRIMARY KEY,
    series_id      TEXT NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    chapter_id     TEXT NOT NULL REFERENCES chapters (id) ON DELETE CASCADE,
    lang           TEXT NOT NULL,
    number         DOUBLE PRECISION NOT NULL,
    title          TEXT NOT NULL DEFAULT '',
    visibility     TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'unlisted', 'members', 'private')),
    flags          TEXT NOT NULL DEFAULT '{}',
    ai_label       TEXT NOT NULL DEFAULT '{}',
    status         TEXT NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'published', 'unpublished')),
    page_count     INTEGER NOT NULL DEFAULT 0,
    scheduled_at   TIMESTAMP,
    published_at   TIMESTAMP,
    unpublished_at TIMESTAMP,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX chapter_publications_uq ON chapter_publications (chapter_id, lang);
CREATE INDEX chapter_publications_series_idx ON chapter_publications (series_id, lang, number);
CREATE INDEX chapter_publications_status_idx ON chapter_publications (status, scheduled_at);
CREATE INDEX chapter_publications_published_idx ON chapter_publications (published_at);

-- Frozen page images of a publication plus the guided-view geometry and alt text.
CREATE TABLE publication_pages (
    id             TEXT PRIMARY KEY,
    publication_id TEXT NOT NULL REFERENCES chapter_publications (id) ON DELETE CASCADE,
    number         INTEGER NOT NULL,
    asset_id       TEXT REFERENCES assets (id) ON DELETE SET NULL,
    width          INTEGER NOT NULL DEFAULT 0,
    height         INTEGER NOT NULL DEFAULT 0,
    panels         TEXT NOT NULL DEFAULT '[]',
    alt_text       TEXT NOT NULL DEFAULT '',
    living         TEXT NOT NULL DEFAULT '[]',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX publication_pages_uq ON publication_pages (publication_id, number);

CREATE TABLE series_creators (
    series_id   TEXT NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role        TEXT NOT NULL DEFAULT 'owner',
    credit_name TEXT NOT NULL DEFAULT '',
    order_idx   INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (series_id, user_id, role)
);
CREATE INDEX series_creators_user_idx ON series_creators (user_id);

CREATE TABLE follows (
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    series_id  TEXT NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    notify     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, series_id)
);
CREATE INDEX follows_series_idx ON follows (series_id, created_at);

CREATE TABLE user_follows (
    follower_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    followee_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (follower_id, followee_id)
);
CREATE INDEX user_follows_followee_idx ON user_follows (followee_id);

CREATE TABLE read_progress (
    user_id        TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    publication_id TEXT NOT NULL REFERENCES chapter_publications (id) ON DELETE CASCADE,
    series_id      TEXT NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    page           INTEGER NOT NULL DEFAULT 1,
    percent        DOUBLE PRECISION NOT NULL DEFAULT 0,
    completed      BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, publication_id)
);
CREATE INDEX read_progress_history_idx ON read_progress (user_id, updated_at);
CREATE INDEX read_progress_series_idx ON read_progress (user_id, series_id);

CREATE TABLE reader_prefs (
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    series_id  TEXT NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    mode       TEXT NOT NULL DEFAULT 'paged' CHECK (mode IN ('paged', 'spread', 'webtoon', 'guided')),
    direction  TEXT NOT NULL DEFAULT 'rtl' CHECK (direction IN ('rtl', 'ltr', 'vertical')),
    background TEXT NOT NULL DEFAULT 'black',
    zoom       DOUBLE PRECISION NOT NULL DEFAULT 1,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, series_id)
);

CREATE TABLE bookmarks (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    series_id      TEXT NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    publication_id TEXT NOT NULL REFERENCES chapter_publications (id) ON DELETE CASCADE,
    page           INTEGER NOT NULL DEFAULT 1,
    panel_id       TEXT NOT NULL DEFAULT '',
    note           TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX bookmarks_user_idx ON bookmarks (user_id, created_at);

CREATE TABLE downloads (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    publication_id TEXT NOT NULL REFERENCES chapter_publications (id) ON DELETE CASCADE,
    bytes          BIGINT NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'ready' CHECK (status IN ('pending', 'ready', 'expired')),
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at     TIMESTAMP
);
CREATE UNIQUE INDEX downloads_uq ON downloads (user_id, publication_id);

-- Anonymous readers are counted by a salted, daily-rotated session hash, never by IP.
CREATE TABLE read_events (
    id             TEXT PRIMARY KEY,
    series_id      TEXT NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    publication_id TEXT REFERENCES chapter_publications (id) ON DELETE CASCADE,
    user_id        TEXT REFERENCES users (id) ON DELETE SET NULL,
    session_hash   TEXT NOT NULL DEFAULT '',
    kind           TEXT NOT NULL CHECK (kind IN ('open', 'page', 'complete', 'drop')),
    page           INTEGER NOT NULL DEFAULT 0,
    ms             INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX read_events_series_idx ON read_events (series_id, created_at);
CREATE INDEX read_events_publication_idx ON read_events (publication_id, page);

CREATE TABLE reports (
    id          TEXT PRIMARY KEY,
    reporter_id TEXT REFERENCES users (id) ON DELETE SET NULL,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('series', 'chapter', 'page', 'comment', 'user', 'character')),
    target_id   TEXT NOT NULL,
    reason      TEXT NOT NULL CHECK (reason IN ('spam', 'harassment', 'copyright', 'ai_label', 'unmarked_mature', 'illegal', 'other')),
    details     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'actioned', 'dismissed')),
    resolved_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    resolution  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMP
);
CREATE INDEX reports_status_idx ON reports (status, created_at);
CREATE INDEX reports_target_idx ON reports (target_kind, target_id);

CREATE TABLE notifications (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    link       TEXT NOT NULL DEFAULT '',
    actor_id   TEXT REFERENCES users (id) ON DELETE SET NULL,
    data       TEXT NOT NULL DEFAULT '{}',
    group_key  TEXT NOT NULL DEFAULT '',
    read_at    TIMESTAMP,
    emailed_at TIMESTAMP,
    pushed_at  TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX notifications_user_idx ON notifications (user_id, created_at);
CREATE INDEX notifications_unread_idx ON notifications (user_id, read_at);

CREATE TABLE notification_prefs (
    user_id        TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    prefs          TEXT NOT NULL DEFAULT '{}',
    last_digest_at TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE push_subscriptions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint     TEXT NOT NULL,
    p256dh       TEXT NOT NULL,
    auth         TEXT NOT NULL,
    user_agent   TEXT NOT NULL DEFAULT '',
    failures     INTEGER NOT NULL DEFAULT 0,
    last_used_at TIMESTAMP,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX push_subscriptions_endpoint_uq ON push_subscriptions (endpoint);
CREATE INDEX push_subscriptions_user_idx ON push_subscriptions (user_id);

-- ref_kind/ref_id make awards idempotent (one award per user, kind and reference).
CREATE TABLE xp_events (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    amount     INTEGER NOT NULL,
    ref_kind   TEXT NOT NULL DEFAULT '',
    ref_id     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX xp_events_uq ON xp_events (user_id, kind, ref_kind, ref_id);
CREATE INDEX xp_events_user_idx ON xp_events (user_id, created_at);
