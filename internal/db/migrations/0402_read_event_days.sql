-- Daily aggregates of raw read events older than 90 days (ADR 0012 §8). The
-- analytics roll-up claims each raw day with DELETE … RETURNING and upserts one
-- row per UTC day, series, publication, kind and page. readers counts the
-- distinct reader keys (member user or anonymous daily session hash) that day.
CREATE TABLE read_event_days (
    day            TIMESTAMP NOT NULL,
    series_id      TEXT NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    publication_id TEXT NOT NULL REFERENCES chapter_publications (id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('open', 'page', 'complete', 'drop')),
    page           INTEGER NOT NULL DEFAULT 0,
    events         BIGINT NOT NULL DEFAULT 0,
    readers        BIGINT NOT NULL DEFAULT 0,
    ms             BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (day, series_id, publication_id, kind, page)
);
CREATE INDEX read_event_days_series_idx ON read_event_days (series_id, day);
