-- Random salts for anonymous reading-session hashes, one per UTC day (ADR 0012 §8).
-- The first server process to hash a session on a day inserts the day's salt
-- (INSERT … ON CONFLICT (day) DO NOTHING) and every process reads it back, so
-- hashes agree across processes and restarts within the day. Rows of past days
-- are deleted once the day is over, so past hashes cannot be recomputed even
-- with the master key and a visitor's cookie.
CREATE TABLE analytics_day_salts (
    day        TEXT PRIMARY KEY,
    salt       TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
