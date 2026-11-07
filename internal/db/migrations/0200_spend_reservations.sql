-- 0200 durable remote-spend reservations.
--
-- Identity columns intentionally have no foreign keys. Deleting a job, user,
-- project or provider must not erase a possible charge or prevent repair of an
-- orphaned reservation.

CREATE TABLE spend_reservations (
    id                 TEXT PRIMARY KEY,
    job_id             TEXT NOT NULL,
    attempt            INTEGER NOT NULL CHECK (attempt > 0),
    call_seq           INTEGER NOT NULL CHECK (call_seq > 0),
    purpose            TEXT NOT NULL CHECK (purpose IN ('initial', 'repair', 'retry', 'failover')),

    user_id            TEXT NOT NULL,
    project_id         TEXT NOT NULL DEFAULT '',
    provider_id        TEXT NOT NULL,
    model_id           TEXT NOT NULL,
    provider_scope     TEXT NOT NULL CHECK (provider_scope IN ('server', 'user')),

    month              TEXT NOT NULL CHECK (
        LENGTH(month) = 7
        AND SUBSTR(month, 5, 1) = '-'
        AND SUBSTR(month, 1, 1) BETWEEN '0' AND '9'
        AND SUBSTR(month, 2, 1) BETWEEN '0' AND '9'
        AND SUBSTR(month, 3, 1) BETWEEN '0' AND '9'
        AND SUBSTR(month, 4, 1) BETWEEN '0' AND '9'
        AND SUBSTR(month, 6, 1) BETWEEN '0' AND '9'
        AND SUBSTR(month, 7, 1) BETWEEN '0' AND '9'
        AND SUBSTR(month, 6, 2) BETWEEN '01' AND '12'
    ),
    estimate_microusd  BIGINT NOT NULL CHECK (estimate_microusd >= 0),
    actual_microusd    BIGINT CHECK (actual_microusd IS NULL OR actual_microusd >= 0),

    state              TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved', 'reconciled', 'repaired')),
    outcome            TEXT NOT NULL DEFAULT 'pending' CHECK (outcome IN ('pending', 'success', 'failure', 'canceled', 'unknown')),
    actual_source      TEXT NOT NULL DEFAULT 'pending' CHECK (actual_source IN ('pending', 'reported', 'estimate')),

    expires_at         TIMESTAMP NOT NULL,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reconciled_at      TIMESTAMP,

    UNIQUE (job_id, attempt, call_seq),

    CHECK (
        (
            state = 'reserved'
            AND outcome = 'pending'
            AND actual_source = 'pending'
            AND actual_microusd IS NULL
            AND reconciled_at IS NULL
        )
        OR
        (
            state = 'reconciled'
            AND outcome IN ('success', 'failure', 'canceled')
            AND actual_source IN ('reported', 'estimate')
            AND actual_microusd IS NOT NULL
            AND reconciled_at IS NOT NULL
        )
        OR
        (
            state = 'repaired'
            AND outcome = 'unknown'
            AND actual_source = 'estimate'
            AND actual_microusd IS NOT NULL
            AND reconciled_at IS NOT NULL
        )
    )
);

CREATE INDEX spend_reservations_job_seq_idx
    ON spend_reservations (job_id, call_seq);

CREATE INDEX spend_reservations_month_state_idx
    ON spend_reservations (month, state);

CREATE INDEX spend_reservations_user_month_state_idx
    ON spend_reservations (user_id, month, state, provider_scope);

CREATE INDEX spend_reservations_stale_idx
    ON spend_reservations (state, expires_at);
