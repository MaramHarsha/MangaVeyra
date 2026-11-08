-- 0204 cross-process generation de-duplication.
--
-- An in-process mutex plus a scan of active jobs cannot stop two server
-- processes sharing one database from both queueing the same generation (and
-- spending twice). jobs.EnqueueParams.DedupeKey stores a SHA-256 of the job
-- type and the caller's request key here, and at most one active job may
-- hold a key: queued or running, without a pending cancellation. Finished,
-- failed and canceled jobs, and jobs being canceled, never block a new
-- request; a manual retry that would reactivate a held key is refused.
-- Existing rows keep NULL and are unaffected.
ALTER TABLE jobs ADD COLUMN dedupe_key TEXT;
CREATE UNIQUE INDEX jobs_dedupe_active_uq ON jobs (dedupe_key)
    WHERE dedupe_key IS NOT NULL AND state IN ('queued', 'running') AND cancel_requested = FALSE;
