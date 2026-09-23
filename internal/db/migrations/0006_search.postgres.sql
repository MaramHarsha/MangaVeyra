-- 0006 search (Postgres): full-text index over public series metadata.
-- Query: WHERE to_tsvector('simple', title || ' ' || synopsis || ' ' || tags || ' ' || genres) @@ websearch_to_tsquery('simple', $1)

CREATE INDEX series_search_idx ON series
    USING GIN (to_tsvector('simple', title || ' ' || synopsis || ' ' || tags || ' ' || genres));
