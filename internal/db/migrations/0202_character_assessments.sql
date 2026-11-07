-- Immutable input identity for Character Lock evidence. A score belongs to one
-- character/reference fingerprint and one raster asset, never to a mutable page.
CREATE TABLE character_assessments (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 character_id TEXT NOT NULL REFERENCES characters(id) ON DELETE CASCADE,
 asset_id TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
 reference_hash TEXT NOT NULL,
 method TEXT NOT NULL,
 identity_score DOUBLE PRECISION NOT NULL,
 outfit_score DOUBLE PRECISION,
 job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 created_at TIMESTAMP NOT NULL,
 UNIQUE(character_id, asset_id, reference_hash)
);
CREATE INDEX character_assessments_project ON character_assessments(project_id,character_id,reference_hash);
