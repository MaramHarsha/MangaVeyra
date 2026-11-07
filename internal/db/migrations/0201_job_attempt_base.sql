-- 0201 monotonic job attempts.
--
-- Retry used to reset attempts to 0. A stalled handler from the previous round
-- then held the same (worker_id, attempts) lease identity as the next claim, so
-- it could heartbeat, reserve spend and persist under the new attempt. Retry now
-- keeps attempts monotonic and records where the current round started:
-- attempts - attempt_base is the attempt number within the round, and
-- max_attempts stays the per-round budget.
ALTER TABLE jobs ADD COLUMN attempt_base INTEGER NOT NULL DEFAULT 0;
