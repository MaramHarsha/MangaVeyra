# ADR 0003: Hard remote-spend reservations

- Status: Accepted for milestone 0001; implementation pending
- Date: 2026-11-07
- Related plan: `docs/plans/0001-script-vertical-slice.md`

## Context

Checking a monthly limit before an HTTP request is not sufficient: concurrent
requests can all observe the same remaining budget, and a process crash can lose
the eventual usage record. Remote calls also include repairs, retries, and
failovers, each of which can spend independently.

The existing `spend_ledger` remains the immutable actual-usage history. Hard cap
enforcement needs a durable, idempotent reservation before every outbound call.

## Decision

Migration `0200_spend_reservations.sql` will add one portable table. Monetary
enforcement uses non-negative integer microdollars, converted with a conservative
ceiling. Reservation, reconciliation, and repair transactions are serialized by:

- PostgreSQL: `SELECT pg_advisory_xact_lock(7468326821)`.
- SQLite: the existing `db.DB.WithTx`, which uses `BEGIN IMMEDIATE`.

The table records a generated reservation ID, job ID, claimed attempt, a
job-global call sequence, purpose (`initial`, `repair`, `retry`, or `failover`),
snapshot attribution, provider scope, UTC reservation month, estimate, optional
actual amount, state, outcome, expiry, and timestamps. Its state machine is:

- `reserved`: estimate counts against the cap.
- `reconciled`: reported actual counts against the cap.
- `repaired`: estimate counts as the conservative actual after an orphan expires.

There are no foreign keys from reservations to mutable jobs, users, projects, or
providers. Deletion must neither erase a possible charge nor prevent repair.
Identity and ownership are nevertheless re-resolved and validated when reserving.

`UNIQUE (job_id, attempt, call_seq)` is retained for diagnosis, but `call_seq` is
monotonic across the whole job. It does not reset with an attempt because manual
`Queue.Retry` currently resets the attempt counter.

## Service contract

The shared store exposes four operations:

```go
ReserveRemoteCall(context.Context, ReserveRemoteCallParams) (RemoteReservation, error)
ReconcileRemoteCall(context.Context, ReconcileRemoteCallParams) (RemoteReservation, error)
RepairExpiredRemoteCalls(context.Context, time.Time, int) (RepairResult, error)
MonthlyRemoteUsage(context.Context, string, string) (MonthlyRemoteUsage, error)
```

The caller allocates `ReservationID` once and reuses it after an uncertain database
result. An identical reserve is idempotent; a conflicting payload fails. A closed
reservation returns `Dispatch=false` and never permits a second HTTP call.

Reserve derives the user and project from a running job at the exact attempt,
re-resolves an enabled provider and its ownership/key state, assigns the next
job-global call sequence, and checks user and server caps under the spend lock.
The HTTP request starts only after commit. Each repair, retry, and failover receives
its own reservation.

Reconciliation accepts `nil` actual usage to mean unknown and charges the estimate.
A known zero is distinct. Identical reconciliation is idempotent; conflicting
reconciliation fails. The immutable ledger row uses the reservation ID as its ID,
which supplies one-row-per-call idempotency without changing `spend_ledger`.
Closed reservation rows are not double-counted with their linked ledger rows;
legacy ledger rows without reservations still count conservatively.

Expired reservations continue counting at estimate. Startup repair, followed by a
periodic bounded sweep, changes them to `repaired`, charges the estimate, and inserts
the deterministic ledger row. Late reconciliation cannot rewrite repaired usage.

## Cap policy

- Equality with a cap is allowed; exceeding it is rejected.
- Zero means unlimited.
- Negative, non-finite, overflowing, or invalid amounts fail closed.
- The per-user cap applies to server-scope provider calls, matching current settings.
- The server cap applies to all remote calls, including user-owned providers.
- Reconciliation may record an actual amount above the estimate and then blocks
  later reservations. Providers therefore require a genuine upper-bound estimate
  for a cap to be mathematically hard before the call.
- Reservation month is UTC at reservation time and does not change across month end.

`settings.Instance.Validate` must also reject a negative
`ServerMonthlyRemoteSpendUSD`; it currently validates only the user limit.

## Required verification

Run the same tests on SQLite and PostgreSQL, using two database handles for races:

1. concurrent reservations cannot overshoot user or server caps;
2. exact-boundary, microdollar rounding, invalid-number, and unlimited cases;
3. reserve and reconcile idempotency plus conflict detection;
4. reconciliation below/above estimate and unknown usage at estimate;
5. distinct initial, repair, retry, failover, and manual-retry calls;
6. reserve-versus-reconcile races cannot undercount;
7. crash/reopen, expiry repair, repeated repair, and late results;
8. month rollover and deletion of live attribution rows;
9. legacy ledger inclusion without linked-row double counting; and
10. server-scope and user-scope cap policy.

## Consequences and residual risks

The approach adds a short global spend critical section but keeps network I/O out
of it. It prevents concurrent overspend relative to estimates and fails closed
through crashes. Settings are cached for three seconds today, so multi-process limit
reductions need tighter invalidation before the deployment model expands beyond a
single application process.
