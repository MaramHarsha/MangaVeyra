-- 1000 editor collaboration (ADR 0013 §8). An internal review comment may be
-- assigned to one project member, who may then mark it done like its
-- author. Existing rows keep NULL (unassigned). Page approvals are internal
-- comments on the page (status 'approved') and member scopes already live in
-- project_members.scope, so neither needs a schema change.
ALTER TABLE comments ADD COLUMN assignee_id TEXT REFERENCES users (id) ON DELETE SET NULL;
CREATE INDEX comments_assignee_idx ON comments (assignee_id);
