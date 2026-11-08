-- 0400 Phase 3 reader: a signed-in reader confirms once per mature/adult series (ADR 0012).
-- Kept separate from reader_prefs so confirming does not pin a reading mode or direction.
CREATE TABLE age_confirmations (
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    series_id    TEXT NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    confirmed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, series_id)
);
