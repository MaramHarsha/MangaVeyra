-- The series page (E5) and creator profile (F1) show a creator-written release
-- note such as "Weekly, Saturday". Empty means no note is shown.
ALTER TABLE series ADD COLUMN schedule TEXT NOT NULL DEFAULT '';
