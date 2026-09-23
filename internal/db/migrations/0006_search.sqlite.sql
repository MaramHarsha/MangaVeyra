-- 0006 search (SQLite): FTS5 index over public series metadata, kept in sync by triggers.
-- Query: SELECT s.* FROM series_fts f JOIN series s ON s.rowid = f.rowid WHERE series_fts MATCH ? ORDER BY rank.

CREATE VIRTUAL TABLE series_fts USING fts5(
    title, synopsis, tags, genres,
    content = 'series', content_rowid = 'rowid', tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER series_fts_ai AFTER INSERT ON series BEGIN
    INSERT INTO series_fts (rowid, title, synopsis, tags, genres)
    VALUES (new.rowid, new.title, new.synopsis, new.tags, new.genres);
END;

CREATE TRIGGER series_fts_ad AFTER DELETE ON series BEGIN
    INSERT INTO series_fts (series_fts, rowid, title, synopsis, tags, genres)
    VALUES ('delete', old.rowid, old.title, old.synopsis, old.tags, old.genres);
END;

CREATE TRIGGER series_fts_au AFTER UPDATE OF title, synopsis, tags, genres ON series BEGIN
    INSERT INTO series_fts (series_fts, rowid, title, synopsis, tags, genres)
    VALUES ('delete', old.rowid, old.title, old.synopsis, old.tags, old.genres);
    INSERT INTO series_fts (rowid, title, synopsis, tags, genres)
    VALUES (new.rowid, new.title, new.synopsis, new.tags, new.genres);
END;
