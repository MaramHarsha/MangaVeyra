package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

const (
	microdollarsPerDollar = int64(1_000_000)
	pgSpendLock           = int64(7_468_326_821)

	SpendPurposeInitial  = "initial"
	SpendPurposeRepair   = "repair"
	SpendPurposeRetry    = "retry"
	SpendPurposeFailover = "failover"

	SpendStateReserved   = "reserved"
	SpendStateReconciled = "reconciled"
	SpendStateRepaired   = "repaired"

	SpendOutcomePending  = "pending"
	SpendOutcomeSuccess  = "success"
	SpendOutcomeFailure  = "failure"
	SpendOutcomeCanceled = "canceled"
	SpendOutcomeUnknown  = "unknown"

	SpendActualPending  = "pending"
	SpendActualReported = "reported"
	SpendActualEstimate = "estimate"
)

// Spend errors are deliberately free of provider responses, keys and prompts.
var (
	ErrInvalidSpendAmount     = errors.New("ai: invalid remote spend amount")
	ErrRemoteDisabled         = errors.New("ai: remote access is disabled")
	ErrReservationNotFound    = errors.New("ai: spend reservation not found")
	ErrReservationConflict    = errors.New("ai: spend reservation conflicts with an existing call")
	ErrReservationExpired     = errors.New("ai: spend reservation has expired")
	ErrJobAttemptNotRunning   = errors.New("ai: job attempt is not running")
	ErrProviderUnavailable    = errors.New("ai: remote provider is unavailable")
	ErrUserRemoteSpendLimit   = errors.New("ai: monthly user remote-spend limit exceeded")
	ErrServerRemoteSpendLimit = errors.New("ai: monthly server remote-spend limit exceeded")
)

// ReserveRemoteCallParams describes one billable outbound call. ReservationID
// is allocated once by the caller and reused after an uncertain database
// result. CallSeq is assigned by SpendStore and is monotonic across the job.
type ReserveRemoteCallParams struct {
	ReservationID string
	JobID         string
	Attempt       int
	Purpose       string
	ProviderID    string
	ModelID       string
	EstimateUSD   float64
	ExpiresAt     time.Time
}

// RemoteReservation is the durable state of one outbound remote call.
type RemoteReservation struct {
	ID               string
	JobID            string
	Attempt          int
	CallSeq          int64
	Purpose          string
	UserID           string
	ProjectID        string
	ProviderID       string
	ModelID          string
	ProviderScope    string
	Month            string
	EstimateMicroUSD int64
	ActualMicroUSD   *int64
	State            string
	Outcome          string
	ActualSource     string
	ExpiresAt        time.Time
	CreatedAt        time.Time
	ReconciledAt     *time.Time
	// Dispatch is true only while this exact reservation is open and the
	// current job/provider checks still allow the caller to make the request.
	Dispatch bool
}

// ReconcileRemoteCallParams closes one reservation. ActualUSD nil means the
// amount is unknown and the estimate is charged; a pointer to zero means the
// provider definitely did not bill the call.
type ReconcileRemoteCallParams struct {
	ReservationID string
	Outcome       string
	ActualUSD     *float64
}

// RepairResult describes one bounded expired-reservation sweep.
type RepairResult struct {
	Examined int
	Repaired int
}

// MonthlyRemoteUsage is cap-accounted usage. UserMicroUSD applies the current
// per-user policy (server-scope providers plus conservative legacy entries),
// while ServerMicroUSD includes every remote provider scope.
type MonthlyRemoteUsage struct {
	Month          string
	UserMicroUSD   int64
	ServerMicroUSD int64
	ReservedCount  int
}

// SpendStore owns durable remote-spend reservations and reconciliation.
type SpendStore struct {
	db       *db.DB
	settings *settings.Service

	// Now is replaceable by tests. Production uses db.Now.
	Now func() time.Time
}

// NewSpendStore creates a durable remote-spend store.
func NewSpendStore(d *db.DB, st *settings.Service) *SpendStore {
	return &SpendStore{db: d, settings: st, Now: db.Now}
}

func (s *SpendStore) now() time.Time {
	if s.Now == nil {
		return db.Now()
	}
	return s.Now().UTC().Truncate(time.Microsecond)
}

// ReserveRemoteCall atomically reserves an estimate against the monthly caps.
// It never holds a transaction while the caller performs network I/O.
func (s *SpendStore) ReserveRemoteCall(ctx context.Context, p ReserveRemoteCallParams) (RemoteReservation, error) {
	var zero RemoteReservation
	if s == nil || s.db == nil || s.settings == nil {
		return zero, errors.New("ai: spend store is not configured")
	}
	if err := validateReserveParams(p); err != nil {
		return zero, err
	}
	estimate, err := usdToMicrodollars(p.EstimateUSD)
	if err != nil {
		return zero, err
	}
	expires := p.ExpiresAt.UTC().Truncate(time.Microsecond)
	// Closed rows are immutable, so this lock-free read is sufficient to make
	// an identical retry idempotent even if local-only or limits changed after
	// the original call. Open/new rows are re-read under the spend lock below.
	if existing, found, err := reservationByID(ctx, s.db, p.ReservationID); err != nil {
		return zero, err
	} else if found {
		if !sameReservePayload(existing, p, estimate, expires) {
			return zero, ErrReservationConflict
		}
		if existing.State != SpendStateReserved {
			return existing.remote(false), nil
		}
	}
	var out RemoteReservation
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := lockSpend(ctx, tx); err != nil {
			return err
		}
		// Dispatch authorization linearizes at the spend lock. In particular,
		// policy, month attribution, and expiry must reflect state after lock
		// contention, not a snapshot taken when the caller entered this method.
		now := s.now()

		existing, found, err := reservationByID(ctx, tx, p.ReservationID)
		if err != nil {
			return err
		}
		if found {
			if !sameReservePayload(existing, p, estimate, expires) {
				return ErrReservationConflict
			}
			if existing.State != SpendStateReserved {
				out = existing.remote(false)
				return nil
			}
			if !expires.After(now) {
				return ErrReservationExpired
			}
		}

		// Remote policy is authoritative at the same spend-lock
		// linearization point as usage. Bypass the settings cache so a cap
		// reduction or local-only update committed while this caller waited
		// cannot be admitted under the previous snapshot.
		inst, err := s.settings.InstanceTx(ctx, tx)
		if err != nil {
			return fmt.Errorf("ai: read settings for remote spend: %w", err)
		}
		if err := inst.Validate(); err != nil {
			return fmt.Errorf("ai: validate settings for remote spend: %w", err)
		}
		if inst.LocalOnly {
			return ErrRemoteDisabled
		}
		userLimit, err := usdToMicrodollars(inst.Limits.MonthlyRemoteSpendUSD)
		if err != nil {
			return fmt.Errorf("%w: user limit", err)
		}
		serverLimit, err := usdToMicrodollars(inst.Limits.ServerMonthlyRemoteSpendUSD)
		if err != nil {
			return fmt.Errorf("%w: server limit", err)
		}

		attr, err := resolveAttribution(ctx, tx, p.JobID, p.Attempt, p.ProviderID, p.ModelID)
		if err != nil {
			return err
		}
		if found {
			if existing.UserID != attr.UserID || existing.ProjectID != attr.ProjectID || existing.ProviderScope != attr.ProviderScope {
				return ErrReservationConflict
			}
			out = existing.remote(true)
			return nil
		}
		if !expires.After(now) {
			return ErrReservationExpired
		}

		month := now.Format("2006-01")
		usage, err := monthlyUsageTx(ctx, tx, attr.UserID, month)
		if err != nil {
			return err
		}
		if attr.ProviderScope == "server" && userLimit > 0 {
			total, err := checkedAdd(usage.UserMicroUSD, estimate)
			if err != nil {
				return err
			}
			if total > userLimit {
				return ErrUserRemoteSpendLimit
			}
		}
		if serverLimit > 0 {
			total, err := checkedAdd(usage.ServerMicroUSD, estimate)
			if err != nil {
				return err
			}
			if total > serverLimit {
				return ErrServerRemoteSpendLimit
			}
		}

		var callSeq int64
		if err := tx.Get(ctx, &callSeq, `SELECT COALESCE(MAX(call_seq), 0) + 1 FROM spend_reservations WHERE job_id = ?`, p.JobID); err != nil {
			return fmt.Errorf("ai: allocate spend call sequence: %w", err)
		}
		if callSeq <= 0 {
			return fmt.Errorf("%w: call sequence overflow", ErrInvalidSpendAmount)
		}
		_, err = tx.Exec(ctx, `INSERT INTO spend_reservations (
			id, job_id, attempt, call_seq, purpose, user_id, project_id, provider_id, model_id, provider_scope,
			month, estimate_microusd, state, outcome, actual_source, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'reserved', 'pending', 'pending', ?, ?)`,
			p.ReservationID, p.JobID, p.Attempt, callSeq, p.Purpose, attr.UserID, attr.ProjectID, p.ProviderID,
			p.ModelID, attr.ProviderScope, month, estimate, expires, now)
		if err != nil {
			if db.IsUniqueViolation(err) {
				return ErrReservationConflict
			}
			return fmt.Errorf("ai: insert spend reservation: %w", err)
		}
		row, _, err := reservationByID(ctx, tx, p.ReservationID)
		if err != nil {
			return err
		}
		out = row.remote(true)
		return nil
	})
	if err != nil {
		return zero, err
	}
	return out, nil
}

// ReconcileRemoteCall records the actual (or conservatively estimated) usage
// and inserts the immutable spend-ledger row in the same transaction.
func (s *SpendStore) ReconcileRemoteCall(ctx context.Context, p ReconcileRemoteCallParams) (RemoteReservation, error) {
	var zero RemoteReservation
	if s == nil || s.db == nil {
		return zero, errors.New("ai: spend store is not configured")
	}
	if strings.TrimSpace(p.ReservationID) == "" || !validOutcome(p.Outcome) {
		return zero, ErrReservationConflict
	}
	var reported *int64
	if p.ActualUSD != nil {
		v, err := usdToMicrodollars(*p.ActualUSD)
		if err != nil {
			return zero, err
		}
		reported = &v
	}

	var out RemoteReservation
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := lockSpend(ctx, tx); err != nil {
			return err
		}
		row, found, err := reservationByID(ctx, tx, p.ReservationID)
		if err != nil {
			return err
		}
		if !found {
			return ErrReservationNotFound
		}
		actual, source := row.EstimateMicroUSD, SpendActualEstimate
		if reported != nil {
			actual, source = *reported, SpendActualReported
		}
		if row.State != SpendStateReserved {
			if row.State == SpendStateReconciled && row.Outcome == p.Outcome && row.ActualSource == source &&
				row.ActualMicroUSD.Valid && row.ActualMicroUSD.Int64 == actual {
				out = row.remote(false)
				return nil
			}
			return ErrReservationConflict
		}

		now := s.now()
		res, err := tx.Exec(ctx, `UPDATE spend_reservations
			SET state = 'reconciled', outcome = ?, actual_source = ?, actual_microusd = ?, reconciled_at = ?
			WHERE id = ? AND state = 'reserved'`, p.Outcome, source, actual, now, row.ID)
		if err != nil {
			return fmt.Errorf("ai: reconcile spend reservation: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrReservationConflict
		}
		if err := insertLedger(ctx, tx, row, actual, now); err != nil {
			return err
		}
		closed, _, err := reservationByID(ctx, tx, row.ID)
		if err != nil {
			return err
		}
		out = closed.remote(false)
		return nil
	})
	if err != nil {
		return zero, err
	}
	return out, nil
}

// RepairExpiredRemoteCalls closes at most limit orphaned reservations at their
// estimates. Call it at startup before dispatch and periodically thereafter.
func (s *SpendStore) RepairExpiredRemoteCalls(ctx context.Context, now time.Time, limit int) (RepairResult, error) {
	var result RepairResult
	if s == nil || s.db == nil {
		return result, errors.New("ai: spend store is not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	now = now.UTC().Truncate(time.Microsecond)
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := lockSpend(ctx, tx); err != nil {
			return err
		}
		var ids []string
		if err := tx.Select(ctx, &ids, `SELECT id FROM spend_reservations
			WHERE state = 'reserved' AND expires_at <= ? ORDER BY expires_at, id LIMIT ?`, now, limit); err != nil {
			return fmt.Errorf("ai: find expired spend reservations: %w", err)
		}
		result.Examined = len(ids)
		for _, id := range ids {
			row, found, err := reservationByID(ctx, tx, id)
			if err != nil {
				return err
			}
			if !found || row.State != SpendStateReserved || row.ExpiresAt.After(now) {
				continue
			}
			res, err := tx.Exec(ctx, `UPDATE spend_reservations
				SET state = 'repaired', outcome = 'unknown', actual_source = 'estimate',
					actual_microusd = estimate_microusd, reconciled_at = ?
				WHERE id = ? AND state = 'reserved'`, now, id)
			if err != nil {
				return fmt.Errorf("ai: repair spend reservation: %w", err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				continue
			}
			if err := insertLedger(ctx, tx, row, row.EstimateMicroUSD, now); err != nil {
				return err
			}
			result.Repaired++
		}
		return nil
	})
	if err != nil {
		return RepairResult{}, err
	}
	return result, nil
}

// MonthlyRemoteUsage returns one consistent cap-accounting snapshot.
func (s *SpendStore) MonthlyRemoteUsage(ctx context.Context, userID, month string) (MonthlyRemoteUsage, error) {
	var zero MonthlyRemoteUsage
	if s == nil || s.db == nil {
		return zero, errors.New("ai: spend store is not configured")
	}
	if !validMonth(month) {
		return zero, fmt.Errorf("%w: invalid month", ErrInvalidSpendAmount)
	}
	var out MonthlyRemoteUsage
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := lockSpend(ctx, tx); err != nil {
			return err
		}
		var err error
		out, err = monthlyUsageTx(ctx, tx, userID, month)
		return err
	})
	if err != nil {
		return zero, err
	}
	return out, nil
}

type attribution struct {
	UserID        string
	ProjectID     string
	ProviderScope string
}

func resolveAttribution(ctx context.Context, tx *db.Tx, jobID string, attempt int, providerID, modelID string) (attribution, error) {
	var out attribution
	var job struct {
		UserID          sql.NullString `db:"user_id"`
		ProjectID       sql.NullString `db:"project_id"`
		State           string         `db:"state"`
		Attempts        int            `db:"attempts"`
		CancelRequested bool           `db:"cancel_requested"`
		ProviderID      string         `db:"provider_id"`
		ModelID         string         `db:"model_id"`
	}
	query := `SELECT user_id, project_id, state, attempts, cancel_requested, provider_id, model_id FROM jobs WHERE id = ?`
	if tx.Dialect() == db.Postgres {
		query += ` FOR UPDATE`
	}
	if err := tx.Get(ctx, &job, query, jobID); err != nil {
		if db.IsNotFound(err) {
			return out, ErrJobAttemptNotRunning
		}
		return out, fmt.Errorf("ai: read job for spend reservation: %w", err)
	}
	if job.State != "running" || job.Attempts != attempt || job.CancelRequested || !job.UserID.Valid || job.UserID.String == "" {
		return out, ErrJobAttemptNotRunning
	}
	// The durable job route is authoritative. A caller cannot reserve one
	// provider/model and dispatch another by forging an untrusted TextPlan.
	if job.ProviderID != providerID || job.ModelID != modelID {
		return out, ErrProviderUnavailable
	}

	var provider struct {
		Enabled bool           `db:"enabled"`
		Scope   string         `db:"scope"`
		OwnerID sql.NullString `db:"owner_id"`
		KeyEnc  string         `db:"key_enc"`
	}
	query = `SELECT enabled, scope, owner_id, key_enc FROM providers WHERE id = ?`
	if tx.Dialect() == db.Postgres {
		query += ` FOR UPDATE`
	}
	if err := tx.Get(ctx, &provider, query, providerID); err != nil {
		if db.IsNotFound(err) {
			return out, ErrProviderUnavailable
		}
		return out, fmt.Errorf("ai: read provider for spend reservation: %w", err)
	}
	if !provider.Enabled || strings.TrimSpace(provider.KeyEnc) == "" {
		return out, ErrProviderUnavailable
	}
	switch provider.Scope {
	case "server":
		if provider.OwnerID.Valid {
			return out, ErrProviderUnavailable
		}
	case "user":
		if !provider.OwnerID.Valid || provider.OwnerID.String != job.UserID.String {
			return out, ErrProviderUnavailable
		}
	default:
		return out, ErrProviderUnavailable
	}
	return attribution{UserID: job.UserID.String, ProjectID: job.ProjectID.String, ProviderScope: provider.Scope}, nil
}

func validateReserveParams(p ReserveRemoteCallParams) error {
	if strings.TrimSpace(p.ReservationID) == "" || strings.TrimSpace(p.JobID) == "" || p.Attempt <= 0 ||
		strings.TrimSpace(p.ProviderID) == "" || strings.TrimSpace(p.ModelID) == "" || p.ExpiresAt.IsZero() {
		return ErrReservationConflict
	}
	switch p.Purpose {
	case SpendPurposeInitial, SpendPurposeRepair, SpendPurposeRetry, SpendPurposeFailover:
		return nil
	default:
		return ErrReservationConflict
	}
}

func validOutcome(v string) bool {
	return v == SpendOutcomeSuccess || v == SpendOutcomeFailure || v == SpendOutcomeCanceled
}

func validMonth(v string) bool {
	parsed, err := time.Parse("2006-01", v)
	return err == nil && parsed.Format("2006-01") == v
}

func usdToMicrodollars(v float64) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, ErrInvalidSpendAmount
	}
	scaled := v * float64(microdollarsPerDollar)
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled >= float64(math.MaxInt64) {
		return 0, ErrInvalidSpendAmount
	}
	if v > 0 && scaled == 0 {
		return 1, nil
	}
	return int64(math.Ceil(scaled)), nil
}

func checkedAdd(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, ErrInvalidSpendAmount
	}
	return a + b, nil
}

func lockSpend(ctx context.Context, tx *db.Tx) error {
	if tx.Dialect() != db.Postgres {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(?)`, pgSpendLock); err != nil {
		return fmt.Errorf("ai: lock remote spend: %w", err)
	}
	return nil
}

type reservationRow struct {
	ID               string        `db:"id"`
	JobID            string        `db:"job_id"`
	Attempt          int           `db:"attempt"`
	CallSeq          int64         `db:"call_seq"`
	Purpose          string        `db:"purpose"`
	UserID           string        `db:"user_id"`
	ProjectID        string        `db:"project_id"`
	ProviderID       string        `db:"provider_id"`
	ModelID          string        `db:"model_id"`
	ProviderScope    string        `db:"provider_scope"`
	Month            string        `db:"month"`
	EstimateMicroUSD int64         `db:"estimate_microusd"`
	ActualMicroUSD   sql.NullInt64 `db:"actual_microusd"`
	State            string        `db:"state"`
	Outcome          string        `db:"outcome"`
	ActualSource     string        `db:"actual_source"`
	ExpiresAt        time.Time     `db:"expires_at"`
	CreatedAt        time.Time     `db:"created_at"`
	ReconciledAt     sql.NullTime  `db:"reconciled_at"`
}

const reservationColumns = `id, job_id, attempt, call_seq, purpose, user_id, project_id, provider_id, model_id,
	provider_scope, month, estimate_microusd, actual_microusd, state, outcome, actual_source,
	expires_at, created_at, reconciled_at`

func reservationByID(ctx context.Context, q db.Querier, id string) (reservationRow, bool, error) {
	var row reservationRow
	if err := q.Get(ctx, &row, `SELECT `+reservationColumns+` FROM spend_reservations WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return row, false, nil
		}
		return row, false, fmt.Errorf("ai: read spend reservation: %w", err)
	}
	row.ExpiresAt = row.ExpiresAt.UTC()
	row.CreatedAt = row.CreatedAt.UTC()
	if row.ReconciledAt.Valid {
		row.ReconciledAt.Time = row.ReconciledAt.Time.UTC()
	}
	return row, true, nil
}

func sameReservePayload(row reservationRow, p ReserveRemoteCallParams, estimate int64, expires time.Time) bool {
	return row.JobID == p.JobID && row.Attempt == p.Attempt && row.Purpose == p.Purpose &&
		row.ProviderID == p.ProviderID && row.ModelID == p.ModelID && row.EstimateMicroUSD == estimate &&
		row.ExpiresAt.Equal(expires)
}

func (r reservationRow) remote(dispatch bool) RemoteReservation {
	var actual *int64
	if r.ActualMicroUSD.Valid {
		v := r.ActualMicroUSD.Int64
		actual = &v
	}
	var reconciled *time.Time
	if r.ReconciledAt.Valid {
		v := r.ReconciledAt.Time.UTC()
		reconciled = &v
	}
	return RemoteReservation{
		ID: r.ID, JobID: r.JobID, Attempt: r.Attempt, CallSeq: r.CallSeq, Purpose: r.Purpose,
		UserID: r.UserID, ProjectID: r.ProjectID, ProviderID: r.ProviderID, ModelID: r.ModelID,
		ProviderScope: r.ProviderScope, Month: r.Month, EstimateMicroUSD: r.EstimateMicroUSD,
		ActualMicroUSD: actual, State: r.State, Outcome: r.Outcome, ActualSource: r.ActualSource,
		ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt, ReconciledAt: reconciled, Dispatch: dispatch,
	}
}

func insertLedger(ctx context.Context, tx *db.Tx, row reservationRow, actual int64, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO spend_ledger (
		id, user_id, project_id, job_id, provider_id, model_id, kind, usd, gpu_seconds, month, created_at
	) VALUES (
		?, (SELECT id FROM users WHERE id = ?), ?, (SELECT id FROM jobs WHERE id = ?),
		?, ?, 'remote', ?, 0, ?, ?
	)`, row.ID, row.UserID, db.NullString(row.ProjectID), row.JobID, row.ProviderID, row.ModelID,
		float64(actual)/float64(microdollarsPerDollar), row.Month, now)
	if err != nil {
		return fmt.Errorf("ai: insert remote spend ledger: %w", err)
	}
	return nil
}

type reservationUsageRow struct {
	UserID        string        `db:"user_id"`
	ProviderScope string        `db:"provider_scope"`
	State         string        `db:"state"`
	Estimate      int64         `db:"estimate_microusd"`
	Actual        sql.NullInt64 `db:"actual_microusd"`
}

type legacyUsageRow struct {
	UserID sql.NullString `db:"user_id"`
	USD    float64        `db:"usd"`
}

func monthlyUsageTx(ctx context.Context, tx *db.Tx, userID, month string) (MonthlyRemoteUsage, error) {
	out := MonthlyRemoteUsage{Month: month}
	var reservations []reservationUsageRow
	if err := tx.Select(ctx, &reservations, `SELECT user_id, provider_scope, state, estimate_microusd, actual_microusd
		FROM spend_reservations WHERE month = ?`, month); err != nil {
		return out, fmt.Errorf("ai: read monthly spend reservations: %w", err)
	}
	for _, row := range reservations {
		amount := row.Estimate
		if row.State == SpendStateReserved {
			out.ReservedCount++
		} else {
			if !row.Actual.Valid {
				return MonthlyRemoteUsage{}, ErrInvalidSpendAmount
			}
			amount = row.Actual.Int64
		}
		var err error
		out.ServerMicroUSD, err = checkedAdd(out.ServerMicroUSD, amount)
		if err != nil {
			return MonthlyRemoteUsage{}, err
		}
		if userID != "" && row.UserID == userID && row.ProviderScope == "server" {
			out.UserMicroUSD, err = checkedAdd(out.UserMicroUSD, amount)
			if err != nil {
				return MonthlyRemoteUsage{}, err
			}
		}
	}

	// Rows whose id is not a reservation pre-date this state machine (or belong
	// to another insert-only ledger producer). Their provider scope is not
	// durable, so user accounting includes them conservatively.
	var legacy []legacyUsageRow
	if err := tx.Select(ctx, &legacy, `SELECT l.user_id, l.usd FROM spend_ledger l
		LEFT JOIN spend_reservations r ON r.id = l.id
		WHERE l.kind = 'remote' AND l.month = ? AND r.id IS NULL`, month); err != nil {
		return MonthlyRemoteUsage{}, fmt.Errorf("ai: read legacy monthly spend: %w", err)
	}
	for _, row := range legacy {
		usd := row.USD
		if usd < 0 {
			usd = 0
		}
		amount, err := usdToMicrodollars(usd)
		if err != nil {
			return MonthlyRemoteUsage{}, err
		}
		out.ServerMicroUSD, err = checkedAdd(out.ServerMicroUSD, amount)
		if err != nil {
			return MonthlyRemoteUsage{}, err
		}
		if userID != "" && row.UserID.Valid && row.UserID.String == userID {
			out.UserMicroUSD, err = checkedAdd(out.UserMicroUSD, amount)
			if err != nil {
				return MonthlyRemoteUsage{}, err
			}
		}
	}
	return out, nil
}
