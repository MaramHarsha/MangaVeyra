-- Outfit pose references belong to the variant active when they were kept.
-- Legacy references remain unassigned; never guess a costume association.
ALTER TABLE character_refs ADD COLUMN outfit_id TEXT REFERENCES character_outfits(id) ON DELETE SET NULL;
CREATE INDEX character_refs_outfit_idx ON character_refs(character_id, outfit_id, order_idx);
