package ai_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func forEachSpendDialect(t *testing.T, fn func(*testing.T, *testutil.TestApp)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			fn(t, testutil.NewTestApp(t, testutil.WithDialect(dialect)))
		})
	}
}

type spendFixture struct {
	UserID     string
	ProjectID  string
	JobID      string
	ProviderID string
	ModelID    string
}

func seedSpendFixture(t *testing.T, ta *testutil.TestApp, providerScope string) spendFixture {
	t.Helper()
	ctx := context.Background()
	user := ta.CreateUser(t, "", auth.RoleCreator)
	projectID, jobID, providerID := db.NewID(), db.NewID(), db.NewID()
	if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO projects (id, owner_id, title, slug) VALUES (?, ?, ?, ?)`,
		projectID, user.ID, "Low Tide Radio", "low-tide-"+projectID); err != nil {
		t.Fatal(err)
	}
	var owner any
	if providerScope == "user" {
		owner = user.ID
	}
	if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO providers (id, kind, name, key_enc, scope, owner_id)
		VALUES (?, 'openrouter', 'OpenRouter test', 'sealed-test-key', ?, ?)`, providerID, providerScope, owner); err != nil {
		t.Fatal(err)
	}
	modelID := "openrouter/test-text"
	if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO jobs (
		id, type, scope, scope_kind, project_id, user_id, state, executor, model_id, provider_id, attempts
	) VALUES (?, 'script.generate', '{}', 'chapter', ?, ?, 'running', 'go', ?, ?, 1)`,
		jobID, projectID, user.ID, modelID, providerID); err != nil {
		t.Fatal(err)
	}
	return spendFixture{UserID: user.ID, ProjectID: projectID, JobID: jobID, ProviderID: providerID, ModelID: modelID}
}

func insertProvider(t *testing.T, ta *testutil.TestApp, scope, ownerID string, enabled bool, key string) string {
	t.Helper()
	id := db.NewID()
	if _, err := ta.Deps.DB.Exec(context.Background(), `INSERT INTO providers
		(id, kind, name, key_enc, enabled, scope, owner_id) VALUES (?, 'openrouter', 'Extra provider', ?, ?, ?, ?)`,
		id, key, enabled, scope, db.NullString(ownerID)); err != nil {
		t.Fatal(err)
	}
	return id
}

func setSpendLimits(t *testing.T, ta *testutil.TestApp, user, server float64) {
	t.Helper()
	if _, err := ta.Deps.Settings.UpdateInstance(context.Background(), "", func(inst *settings.Instance) error {
		inst.Limits.MonthlyRemoteSpendUSD = user
		inst.Limits.ServerMonthlyRemoteSpendUSD = server
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func openSecondDatabase(t *testing.T, ta *testutil.TestApp) (*db.DB, string) {
	t.Helper()
	dialect, dsn, err := ta.Deps.Cfg.Database()
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(context.Background(), dialect, dsn, db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, dialect
}

func holdSpendLock(t *testing.T, d *db.DB, dialect string) (chan struct{}, <-chan error) {
	t.Helper()
	acquired := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- d.WithTx(context.Background(), func(tx *db.Tx) error {
			if dialect == "postgres" {
				if _, err := tx.Exec(context.Background(), `SELECT pg_advisory_xact_lock(?)`, int64(7_468_326_821)); err != nil {
					return err
				}
			}
			close(acquired)
			<-release
			return nil
		})
	}()
	select {
	case <-acquired:
	case err := <-done:
		t.Fatalf("acquire blocking spend lock: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out acquiring blocking spend lock")
	}
	return release, done
}

func holdSpendLockThenUpdateSetting(t *testing.T, d *db.DB, dialect, key, value string) (chan struct{}, <-chan error) {
	t.Helper()
	acquired := make(chan struct{})
	commit := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- d.WithTx(context.Background(), func(tx *db.Tx) error {
			if dialect == "postgres" {
				if _, err := tx.Exec(context.Background(), `SELECT pg_advisory_xact_lock(?)`, int64(7_468_326_821)); err != nil {
					return err
				}
			}
			close(acquired)
			<-commit
			_, err := tx.Exec(context.Background(), `INSERT INTO instance_settings (key, value, updated_at)
				VALUES (?, ?, ?) ON CONFLICT (key) DO UPDATE
				SET value = excluded.value, updated_at = excluded.updated_at`, key, value, db.Now())
			return err
		})
	}()
	select {
	case <-acquired:
	case err := <-done:
		t.Fatalf("acquire blocking spend lock: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out acquiring blocking spend lock")
	}
	return commit, done
}

func waitForDatabaseUse(t *testing.T, d *db.DB) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var inUseSince time.Time
	for {
		if d.X.Stats().InUse > 0 {
			if inUseSince.IsZero() {
				inUseSince = time.Now()
			}
			// The preliminary idempotency lookup is brief. Sustained use means
			// Begin/pg_advisory_xact_lock is blocked behind the held spend lock.
			if time.Since(inUseSince) >= 100*time.Millisecond {
				return
			}
		} else {
			inUseSince = time.Time{}
		}
		if time.Now().After(deadline) {
			t.Fatal("reservation did not reach database lock contention")
		}
		time.Sleep(time.Millisecond)
	}
}

func reserveParams(f spendFixture, id, purpose string, estimate float64, expires time.Time) ai.ReserveRemoteCallParams {
	return ai.ReserveRemoteCallParams{
		ReservationID: id,
		JobID:         f.JobID,
		Attempt:       1,
		Purpose:       purpose,
		ProviderID:    f.ProviderID,
		ModelID:       f.ModelID,
		EstimateUSD:   estimate,
		ExpiresAt:     expires,
	}
}

func TestSpendReservationLifecycleAndIdempotency(t *testing.T) {
	forEachSpendDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixed := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
		setSpendLimits(t, ta, 10, 20)
		f := seedSpendFixture(t, ta, "server")
		store := ai.NewSpendStore(ta.Deps.DB, ta.Deps.Settings)
		store.Now = func() time.Time { return fixed }

		initial := reserveParams(f, db.NewID(), ai.SpendPurposeInitial, 0.40, fixed.Add(time.Minute))
		reserved, err := store.ReserveRemoteCall(ctx, initial)
		if err != nil {
			t.Fatal(err)
		}
		if !reserved.Dispatch || reserved.State != ai.SpendStateReserved || reserved.CallSeq != 1 ||
			reserved.EstimateMicroUSD != 400_000 || reserved.Month != "2026-11" {
			t.Fatalf("unexpected reservation: %+v", reserved)
		}
		duplicate, err := store.ReserveRemoteCall(ctx, initial)
		if err != nil || duplicate.CallSeq != reserved.CallSeq || !duplicate.Dispatch {
			t.Fatalf("idempotent reserve: %+v, %v", duplicate, err)
		}
		conflict := initial
		conflict.ModelID = "different-model"
		if _, err := store.ReserveRemoteCall(ctx, conflict); !errors.Is(err, ai.ErrReservationConflict) {
			t.Fatalf("conflicting reserve error = %v", err)
		}

		actual := 0.25
		closed, err := store.ReconcileRemoteCall(ctx, ai.ReconcileRemoteCallParams{
			ReservationID: initial.ReservationID, Outcome: ai.SpendOutcomeSuccess, ActualUSD: &actual,
		})
		if err != nil || closed.Dispatch || closed.State != ai.SpendStateReconciled ||
			closed.ActualMicroUSD == nil || *closed.ActualMicroUSD != 250_000 || closed.ActualSource != ai.SpendActualReported {
			t.Fatalf("reconcile: %+v, %v", closed, err)
		}
		if _, err := store.ReconcileRemoteCall(ctx, ai.ReconcileRemoteCallParams{
			ReservationID: initial.ReservationID, Outcome: ai.SpendOutcomeSuccess, ActualUSD: &actual,
		}); err != nil {
			t.Fatalf("duplicate reconcile: %v", err)
		}
		different := 0.30
		if _, err := store.ReconcileRemoteCall(ctx, ai.ReconcileRemoteCallParams{
			ReservationID: initial.ReservationID, Outcome: ai.SpendOutcomeSuccess, ActualUSD: &different,
		}); !errors.Is(err, ai.ErrReservationConflict) {
			t.Fatalf("conflicting reconcile error = %v", err)
		}
		closedAgain, err := store.ReserveRemoteCall(ctx, initial)
		if err != nil || closedAgain.Dispatch {
			t.Fatalf("closed reserve permitted dispatch: %+v, %v", closedAgain, err)
		}

		repairCall := reserveParams(f, db.NewID(), ai.SpendPurposeRepair, 0.20, fixed.Add(time.Minute))
		repairReservation, err := store.ReserveRemoteCall(ctx, repairCall)
		if err != nil || repairReservation.CallSeq != 2 {
			t.Fatalf("repair reservation: %+v, %v", repairReservation, err)
		}
		unknown, err := store.ReconcileRemoteCall(ctx, ai.ReconcileRemoteCallParams{
			ReservationID: repairCall.ReservationID, Outcome: ai.SpendOutcomeFailure,
		})
		if err != nil || unknown.ActualMicroUSD == nil || *unknown.ActualMicroUSD != 200_000 ||
			unknown.ActualSource != ai.SpendActualEstimate {
			t.Fatalf("unknown reconciliation: %+v, %v", unknown, err)
		}

		// Exercise the real public queue transition: Fail makes the job
		// terminal, Retry starts a new round, and Claim takes the next attempt
		// number (attempts never repeat, so an old lease cannot match).
		if failed, err := ta.Deps.Jobs.Fail(ctx, f.JobID, "", jobs.JobError{
			Code: "upstream", Message: "provider failed", Retryable: false,
		}, jobs.Cost{}); err != nil || failed.State != jobs.StateFailed {
			t.Fatalf("queue fail: %+v, %v", failed, err)
		}
		if retried, err := ta.Deps.Jobs.Retry(ctx, f.JobID); err != nil || retried.Attempts != 1 || retried.AttemptBase != 1 ||
			retried.State != jobs.StateQueued {
			t.Fatalf("queue retry: %+v, %v", retried, err)
		}
		claimed, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"script.generate"}})
		if err != nil || claimed == nil || claimed.ID != f.JobID || claimed.Attempts != 2 || claimed.RoundAttempt() != 1 {
			t.Fatalf("queue reclaim: %+v, %v", claimed, err)
		}
		// The previous round's attempt number no longer holds the job.
		stale := reserveParams(f, db.NewID(), ai.SpendPurposeRetry, 0.05, fixed.Add(time.Minute))
		if _, err := store.ReserveRemoteCall(ctx, stale); !errors.Is(err, ai.ErrJobAttemptNotRunning) {
			t.Fatalf("stale-attempt reservation after retry = %v", err)
		}
		retryCall := reserveParams(f, db.NewID(), ai.SpendPurposeRetry, 0.05, fixed.Add(time.Minute))
		retryCall.Attempt = claimed.Attempts
		retryReservation, err := store.ReserveRemoteCall(ctx, retryCall)
		if err != nil || retryReservation.CallSeq != 3 {
			t.Fatalf("manual retry reservation: %+v, %v", retryReservation, err)
		}
		aboveEstimate := 0.10
		if _, err := store.ReconcileRemoteCall(ctx, ai.ReconcileRemoteCallParams{
			ReservationID: retryCall.ReservationID, Outcome: ai.SpendOutcomeSuccess, ActualUSD: &aboveEstimate,
		}); err != nil {
			t.Fatal(err)
		}
		canceledCall := reserveParams(f, db.NewID(), ai.SpendPurposeFailover, 0.15, fixed.Add(time.Minute))
		canceledCall.Attempt = claimed.Attempts
		canceledReservation, err := store.ReserveRemoteCall(ctx, canceledCall)
		if err != nil || canceledReservation.CallSeq != 4 {
			t.Fatalf("canceled-call reservation: %+v, %v", canceledReservation, err)
		}
		reportedZero := 0.0
		canceled, err := store.ReconcileRemoteCall(ctx, ai.ReconcileRemoteCallParams{
			ReservationID: canceledCall.ReservationID, Outcome: ai.SpendOutcomeCanceled, ActualUSD: &reportedZero,
		})
		if err != nil || canceled.Outcome != ai.SpendOutcomeCanceled || canceled.ActualMicroUSD == nil ||
			*canceled.ActualMicroUSD != 0 || canceled.ActualSource != ai.SpendActualReported {
			t.Fatalf("reported-zero cancellation: %+v, %v", canceled, err)
		}

		usage, err := store.MonthlyRemoteUsage(ctx, f.UserID, "2026-11")
		if err != nil || usage.UserMicroUSD != 550_000 || usage.ServerMicroUSD != 550_000 || usage.ReservedCount != 0 {
			t.Fatalf("usage = %+v, %v", usage, err)
		}
		var ledgerRows int
		if err := ta.Deps.DB.Get(ctx, &ledgerRows, `SELECT COUNT(*) FROM spend_ledger WHERE id IN (?, ?, ?, ?)`,
			initial.ReservationID, repairCall.ReservationID, retryCall.ReservationID, canceledCall.ReservationID); err != nil || ledgerRows != 4 {
			t.Fatalf("ledger rows = %d, %v", ledgerRows, err)
		}
		if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(inst *settings.Instance) error {
			inst.LocalOnly = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if closedAfterPolicyChange, err := store.ReserveRemoteCall(ctx, initial); err != nil || closedAfterPolicyChange.Dispatch {
			t.Fatalf("closed idempotency after policy change: %+v, %v", closedAfterPolicyChange, err)
		}
	})
}

func TestSpendCapsScopeLegacyAndAmounts(t *testing.T) {
	forEachSpendDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixed := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
		setSpendLimits(t, ta, 0.30, 0.50)
		f := seedSpendFixture(t, ta, "server")
		store := ai.NewSpendStore(ta.Deps.DB, ta.Deps.Settings)
		store.Now = func() time.Time { return fixed }

		// Legacy positive usage counts; a corrupt negative entry cannot create
		// headroom. Its missing provider-scope snapshot is charged to the user.
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO spend_ledger
			(id, user_id, project_id, job_id, provider_id, model_id, kind, usd, month)
			VALUES (?, ?, ?, ?, ?, ?, 'remote', 0.20, '2026-08')`,
			db.NewID(), f.UserID, f.ProjectID, f.JobID, f.ProviderID, f.ModelID); err != nil {
			t.Fatal(err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO spend_ledger
			(id, user_id, kind, usd, month) VALUES (?, ?, 'remote', -99, '2026-08')`, db.NewID(), f.UserID); err != nil {
			t.Fatal(err)
		}

		serverCall := reserveParams(f, db.NewID(), ai.SpendPurposeInitial, 0.10, fixed.Add(time.Minute))
		if _, err := store.ReserveRemoteCall(ctx, serverCall); err != nil {
			t.Fatalf("exact user boundary rejected: %v", err)
		}
		known := 0.10
		if _, err := store.ReconcileRemoteCall(ctx, ai.ReconcileRemoteCallParams{
			ReservationID: serverCall.ReservationID, Outcome: ai.SpendOutcomeSuccess, ActualUSD: &known,
		}); err != nil {
			t.Fatal(err)
		}
		oneMicro := reserveParams(f, db.NewID(), ai.SpendPurposeRetry, 0.0000001, fixed.Add(time.Minute))
		if _, err := store.ReserveRemoteCall(ctx, oneMicro); !errors.Is(err, ai.ErrUserRemoteSpendLimit) {
			t.Fatalf("one-micro user overflow error = %v", err)
		}

		// User-owned keys bypass the per-user server-key cap but still consume
		// the whole-server cap.
		userProvider := insertProvider(t, ta, "user", f.UserID, true, "sealed-user-key")
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET provider_id = ? WHERE id = ?`, userProvider, f.JobID); err != nil {
			t.Fatal(err)
		}
		f.ProviderID = userProvider
		userCall := reserveParams(f, db.NewID(), ai.SpendPurposeFailover, 0.20, fixed.Add(time.Minute))
		if _, err := store.ReserveRemoteCall(ctx, userCall); err != nil {
			t.Fatalf("user-scoped call at server boundary: %v", err)
		}
		serverOverflow := userCall
		serverOverflow.ReservationID = db.NewID()
		serverOverflow.EstimateUSD = 0.0000001
		if _, err := store.ReserveRemoteCall(ctx, serverOverflow); !errors.Is(err, ai.ErrServerRemoteSpendLimit) {
			t.Fatalf("one-micro server overflow error = %v", err)
		}

		usage, err := store.MonthlyRemoteUsage(ctx, f.UserID, "2026-08")
		if err != nil || usage.UserMicroUSD != 300_000 || usage.ServerMicroUSD != 500_000 {
			t.Fatalf("policy usage = %+v, %v", usage, err)
		}
		if usage.ReservedCount != 1 {
			t.Fatalf("reserved count = %d, want 1", usage.ReservedCount)
		}

		for name, amount := range map[string]float64{
			"negative":          -1,
			"nan":               math.NaN(),
			"positive infinity": math.Inf(1),
			"overflow":          float64(math.MaxInt64) / 1_000_000,
		} {
			t.Run(name, func(t *testing.T) {
				bad := reserveParams(f, db.NewID(), ai.SpendPurposeInitial, amount, fixed.Add(time.Minute))
				if _, err := store.ReserveRemoteCall(ctx, bad); !errors.Is(err, ai.ErrInvalidSpendAmount) {
					t.Fatalf("invalid amount error = %v", err)
				}
			})
		}
		if _, err := store.MonthlyRemoteUsage(ctx, f.UserID, "2026-99"); !errors.Is(err, ai.ErrInvalidSpendAmount) {
			t.Fatalf("invalid month error = %v", err)
		}
		for i, month := range []string{"2026-00", "2026-13", "20a6-01", "2026-a1"} {
			_, err := ta.Deps.DB.Exec(ctx, `INSERT INTO spend_reservations (
				id, job_id, attempt, call_seq, purpose, user_id, provider_id, model_id, provider_scope,
				month, estimate_microusd, expires_at
			) VALUES (?, ?, 9, ?, 'initial', ?, ?, ?, 'server', ?, 1, ?)`,
				db.NewID(), f.JobID, 100+i, f.UserID, f.ProviderID, f.ModelID, month, fixed.Add(time.Minute))
			if err == nil {
				t.Fatalf("portable month CHECK accepted %q", month)
			}
		}

		// Zero limits are unlimited, even after the accounted usage reached the
		// previous exact boundary.
		setSpendLimits(t, ta, 0, 0)
		unlimited := reserveParams(f, db.NewID(), ai.SpendPurposeRetry, 100, fixed.Add(time.Minute))
		if _, err := store.ReserveRemoteCall(ctx, unlimited); err != nil {
			t.Fatalf("zero/unlimited cap rejected: %v", err)
		}
	})
}

func TestConcurrentSpendReservationsDoNotOvershoot(t *testing.T) {
	forEachSpendDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixed := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
		setSpendLimits(t, ta, 1, 100)
		f := seedSpendFixture(t, ta, "server")
		first := ai.NewSpendStore(ta.Deps.DB, ta.Deps.Settings)
		first.Now = func() time.Time { return fixed }

		dialect, dsn, err := ta.Deps.Cfg.Database()
		if err != nil {
			t.Fatal(err)
		}
		secondDB, err := db.Open(ctx, dialect, dsn, db.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = secondDB.Close() })
		secondSettings := settings.New(secondDB)
		second := ai.NewSpendStore(secondDB, secondSettings)
		second.Now = func() time.Time { return fixed }
		stores := []*ai.SpendStore{first, second}

		const calls = 20
		start := make(chan struct{})
		var success, limited atomic.Int64
		errs := make(chan error, calls)
		var wg sync.WaitGroup
		for i := 0; i < calls; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, err := stores[i%len(stores)].ReserveRemoteCall(ctx,
					reserveParams(f, db.NewID(), ai.SpendPurposeInitial, 0.10, fixed.Add(time.Minute)))
				switch {
				case err == nil:
					success.Add(1)
				case errors.Is(err, ai.ErrUserRemoteSpendLimit):
					limited.Add(1)
				default:
					errs <- err
				}
			}(i)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("concurrent reserve: %v", err)
		}
		if success.Load() != 10 || limited.Load() != 10 {
			t.Fatalf("success=%d limited=%d, want 10/10", success.Load(), limited.Load())
		}
		usage, err := first.MonthlyRemoteUsage(ctx, f.UserID, "2026-07")
		if err != nil || usage.UserMicroUSD != 1_000_000 || usage.ServerMicroUSD != 1_000_000 || usage.ReservedCount != 10 {
			t.Fatalf("concurrent usage = %+v, %v", usage, err)
		}
		var distinct, total int
		if err := ta.Deps.DB.Get(ctx, &distinct, `SELECT COUNT(DISTINCT call_seq) FROM spend_reservations WHERE job_id = ?`, f.JobID); err != nil {
			t.Fatal(err)
		}
		if err := ta.Deps.DB.Get(ctx, &total, `SELECT COUNT(*) FROM spend_reservations WHERE job_id = ?`, f.JobID); err != nil {
			t.Fatal(err)
		}
		if distinct != total || total != 10 {
			t.Fatalf("call sequences distinct=%d total=%d", distinct, total)
		}

		// The same two physical connections now contend across two users for
		// the shared server cap. The first phase already consumed $1.
		setSpendLimits(t, ta, 100, 2)
		secondSettings.Invalidate()
		other := seedSpendFixture(t, ta, "server")
		var serverSuccess, serverLimited atomic.Int64
		serverErrs := make(chan error, calls)
		start = make(chan struct{})
		wg = sync.WaitGroup{}
		for i := 0; i < calls; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				fixture := f
				if i%2 == 1 {
					fixture = other
				}
				_, err := stores[i%len(stores)].ReserveRemoteCall(ctx,
					reserveParams(fixture, db.NewID(), ai.SpendPurposeRetry, 0.10, fixed.Add(time.Minute)))
				switch {
				case err == nil:
					serverSuccess.Add(1)
				case errors.Is(err, ai.ErrServerRemoteSpendLimit):
					serverLimited.Add(1)
				default:
					serverErrs <- err
				}
			}(i)
		}
		close(start)
		wg.Wait()
		close(serverErrs)
		for err := range serverErrs {
			t.Errorf("concurrent server reserve: %v", err)
		}
		if serverSuccess.Load() != 10 || serverLimited.Load() != 10 {
			t.Fatalf("server success=%d limited=%d, want 10/10", serverSuccess.Load(), serverLimited.Load())
		}
		serverUsage, err := first.MonthlyRemoteUsage(ctx, f.UserID, "2026-07")
		if err != nil || serverUsage.ServerMicroUSD != 2_000_000 {
			t.Fatalf("server-concurrent usage = %+v, %v", serverUsage, err)
		}
	})
}

func TestSpendClockSamplesAfterContendedLock(t *testing.T) {
	forEachSpendDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		setSpendLimits(t, ta, 10, 10)
		// Warm the settings cache so the only persistent connection use below
		// is the deliberately contended spend transaction.
		if _, err := ta.Deps.Settings.Instance(ctx); err != nil {
			t.Fatal(err)
		}
		f := seedSpendFixture(t, ta, "server")
		blocker, dialect := openSecondDatabase(t, ta)
		store := ai.NewSpendStore(ta.Deps.DB, ta.Deps.Settings)
		var clockMicros atomic.Int64
		var clockCalls atomic.Int64
		store.Now = func() time.Time {
			clockCalls.Add(1)
			return time.UnixMicro(clockMicros.Load()).UTC()
		}

		runContended := func(p ai.ReserveRemoteCallParams, before, after time.Time) (ai.RemoteReservation, error) {
			t.Helper()
			clockMicros.Store(before.UnixMicro())
			release, blockerDone := holdSpendLock(t, blocker, dialect)
			if inUse := ta.Deps.DB.X.Stats().InUse; inUse != 0 {
				t.Fatalf("main database unexpectedly in use before reserve: %d", inUse)
			}
			started := make(chan struct{})
			result := make(chan struct {
				reservation ai.RemoteReservation
				err         error
			}, 1)
			go func() {
				close(started)
				reservation, err := store.ReserveRemoteCall(ctx, p)
				result <- struct {
					reservation ai.RemoteReservation
					err         error
				}{reservation, err}
			}()
			<-started
			waitForDatabaseUse(t, ta.Deps.DB)
			clockMicros.Store(after.UnixMicro())
			close(release)
			if err := <-blockerDone; err != nil {
				t.Fatal(err)
			}
			got := <-result
			return got.reservation, got.err
		}

		jan := time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)
		feb := time.Date(2026, 2, 1, 0, 0, 1, 0, time.UTC)
		monthCall := reserveParams(f, db.NewID(), ai.SpendPurposeInitial, 0.10, feb.Add(time.Hour))
		reservation, err := runContended(monthCall, jan, feb)
		if err != nil || reservation.Month != "2026-02" {
			t.Fatalf("contended month reservation = %+v, %v", reservation, err)
		}

		beforeExpiry := feb.Add(time.Minute)
		afterExpiry := feb.Add(3 * time.Minute)
		expiring := reserveParams(f, db.NewID(), ai.SpendPurposeRetry, 0.10, feb.Add(2*time.Minute))
		if _, err := runContended(expiring, beforeExpiry, afterExpiry); !errors.Is(err, ai.ErrReservationExpired) {
			t.Fatalf("contended expiry error = %v", err)
		}
		if calls := clockCalls.Load(); calls != 2 {
			t.Fatalf("clock sampled %d times, want once per contended authorization", calls)
		}
	})
}

func TestSpendPolicyRefreshesAfterContendedLock(t *testing.T) {
	limits := func(user, server float64) string {
		return fmt.Sprintf(`{"running_gpu_jobs":2,"running_jobs":4,"storage_quota_mb":20480,`+
			`"monthly_remote_spend_usd":%g,"server_monthly_remote_spend_usd":%g,`+
			`"max_upload_mb":50,"max_projects_per_user":0}`, user, server)
	}
	tests := []struct {
		name          string
		providerScope string
		key           string
		value         string
		want          error
	}{
		{name: "user cap", providerScope: "server", key: "limits", value: limits(0.05, 10), want: ai.ErrUserRemoteSpendLimit},
		{name: "server cap", providerScope: "user", key: "limits", value: limits(10, 0.05), want: ai.ErrServerRemoteSpendLimit},
		{name: "local only", providerScope: "server", key: "local_only", value: "true", want: ai.ErrRemoteDisabled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forEachSpendDialect(t, func(t *testing.T, ta *testutil.TestApp) {
				ctx := context.Background()
				setSpendLimits(t, ta, 10, 10)
				// Keep the old permissive policy cached. Reservation admission must
				// still observe the committed update through its transaction.
				if _, err := ta.Deps.Settings.Instance(ctx); err != nil {
					t.Fatal(err)
				}
				f := seedSpendFixture(t, ta, tc.providerScope)
				blocker, dialect := openSecondDatabase(t, ta)
				store := ai.NewSpendStore(ta.Deps.DB, ta.Deps.Settings)
				fixed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
				store.Now = func() time.Time { return fixed }
				params := reserveParams(f, db.NewID(), ai.SpendPurposeInitial, 0.10, fixed.Add(time.Minute))

				commit, blockerDone := holdSpendLockThenUpdateSetting(t, blocker, dialect, tc.key, tc.value)
				result := make(chan error, 1)
				go func() {
					_, err := store.ReserveRemoteCall(ctx, params)
					result <- err
				}()
				waitForDatabaseUse(t, ta.Deps.DB)
				close(commit)
				if err := <-blockerDone; err != nil {
					t.Fatal(err)
				}
				if err := <-result; !errors.Is(err, tc.want) {
					t.Fatalf("contended policy error = %v, want %v", err, tc.want)
				}
				var rows int
				if err := ta.Deps.DB.Get(ctx, &rows, `SELECT COUNT(*) FROM spend_reservations WHERE id = ?`, params.ReservationID); err != nil || rows != 0 {
					t.Fatalf("rejected policy stored reservation: rows=%d err=%v", rows, err)
				}
			})
		})
	}
}

func TestSpendReserveReconcileRaceAndConcurrentDuplicateID(t *testing.T) {
	forEachSpendDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixed := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
		setSpendLimits(t, ta, 1, 10)
		f := seedSpendFixture(t, ta, "server")
		first := ai.NewSpendStore(ta.Deps.DB, ta.Deps.Settings)
		first.Now = func() time.Time { return fixed }
		secondDB, _ := openSecondDatabase(t, ta)
		second := ai.NewSpendStore(secondDB, settings.New(secondDB))
		second.Now = func() time.Time { return fixed }

		base := reserveParams(f, db.NewID(), ai.SpendPurposeInitial, 0.80, fixed.Add(time.Minute))
		if _, err := first.ReserveRemoteCall(ctx, base); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		reconcileErr := make(chan error, 1)
		reserveErr := make(chan error, 1)
		go func() {
			<-start
			actual := 0.10
			_, err := first.ReconcileRemoteCall(ctx, ai.ReconcileRemoteCallParams{
				ReservationID: base.ReservationID, Outcome: ai.SpendOutcomeFailure, ActualUSD: &actual,
			})
			reconcileErr <- err
		}()
		go func() {
			<-start
			_, err := second.ReserveRemoteCall(ctx,
				reserveParams(f, db.NewID(), ai.SpendPurposeRetry, 1.00, fixed.Add(time.Minute)))
			reserveErr <- err
		}()
		close(start)
		if err := <-reconcileErr; err != nil {
			t.Fatalf("racing reconcile: %v", err)
		}
		if err := <-reserveErr; !errors.Is(err, ai.ErrUserRemoteSpendLimit) {
			t.Fatalf("racing reserve escaped cap: %v", err)
		}

		duplicate := reserveParams(f, db.NewID(), ai.SpendPurposeRetry, 0.20, fixed.Add(time.Minute))
		duplicateStart := make(chan struct{})
		results := make(chan ai.RemoteReservation, 2)
		duplicateErrs := make(chan error, 2)
		for _, store := range []*ai.SpendStore{first, second} {
			go func(store *ai.SpendStore) {
				<-duplicateStart
				reservation, err := store.ReserveRemoteCall(ctx, duplicate)
				results <- reservation
				duplicateErrs <- err
			}(store)
		}
		close(duplicateStart)
		one, two := <-results, <-results
		for range 2 {
			if err := <-duplicateErrs; err != nil {
				t.Fatalf("concurrent duplicate reserve: %v", err)
			}
		}
		if one.ID != duplicate.ReservationID || two.ID != duplicate.ReservationID || one.CallSeq != two.CallSeq || !one.Dispatch || !two.Dispatch {
			t.Fatalf("concurrent duplicate results: %+v / %+v", one, two)
		}
		var rows int
		if err := ta.Deps.DB.Get(ctx, &rows, `SELECT COUNT(*) FROM spend_reservations WHERE id = ?`, duplicate.ReservationID); err != nil || rows != 1 {
			t.Fatalf("duplicate reservation rows=%d err=%v", rows, err)
		}
		usage, err := first.MonthlyRemoteUsage(ctx, f.UserID, "2026-06")
		if err != nil || usage.UserMicroUSD != 300_000 || usage.ServerMicroUSD != 300_000 {
			t.Fatalf("race/duplicate usage = %+v, %v", usage, err)
		}
	})
}

func TestSpendRepairMonthPinningAndDeletedAttribution(t *testing.T) {
	forEachSpendDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		jan := time.Date(2026, 1, 31, 23, 59, 0, 0, time.UTC)
		setSpendLimits(t, ta, 10, 10)
		f := seedSpendFixture(t, ta, "server")
		store := ai.NewSpendStore(ta.Deps.DB, ta.Deps.Settings)
		store.Now = func() time.Time { return jan }

		p := reserveParams(f, db.NewID(), ai.SpendPurposeInitial, 0.75, jan.Add(30*time.Second))
		if _, err := store.ReserveRemoteCall(ctx, p); err != nil {
			t.Fatal(err)
		}
		// User deletion cascades the project, job and owned provider. The raw
		// reservation snapshot must survive and remain repairable.
		if _, err := ta.Deps.DB.Exec(ctx, `DELETE FROM users WHERE id = ?`, f.UserID); err != nil {
			t.Fatal(err)
		}
		dialect, dsn, err := ta.Deps.Cfg.Database()
		if err != nil {
			t.Fatal(err)
		}
		// Simulate a process crash/restart: close the pool that created the
		// reservation, reopen the same database, and repair from a fresh store.
		if err := ta.Deps.DB.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := db.Open(ctx, dialect, dsn, db.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		store = ai.NewSpendStore(reopened, settings.New(reopened))
		feb := jan.Add(2 * time.Minute)
		repaired, err := store.RepairExpiredRemoteCalls(ctx, feb, 10)
		if err != nil || repaired.Examined != 1 || repaired.Repaired != 1 {
			t.Fatalf("repair = %+v, %v", repaired, err)
		}
		again, err := store.RepairExpiredRemoteCalls(ctx, feb, 10)
		if err != nil || again.Examined != 0 || again.Repaired != 0 {
			t.Fatalf("repeat repair = %+v, %v", again, err)
		}
		late := 0.50
		if _, err := store.ReconcileRemoteCall(ctx, ai.ReconcileRemoteCallParams{
			ReservationID: p.ReservationID, Outcome: ai.SpendOutcomeSuccess, ActualUSD: &late,
		}); !errors.Is(err, ai.ErrReservationConflict) {
			t.Fatalf("late reconcile error = %v", err)
		}

		janUsage, err := store.MonthlyRemoteUsage(ctx, f.UserID, "2026-01")
		if err != nil || janUsage.UserMicroUSD != 750_000 || janUsage.ServerMicroUSD != 750_000 || janUsage.ReservedCount != 0 {
			t.Fatalf("January usage = %+v, %v", janUsage, err)
		}
		febUsage, err := store.MonthlyRemoteUsage(ctx, f.UserID, "2026-02")
		if err != nil || febUsage.ServerMicroUSD != 0 {
			t.Fatalf("February usage = %+v, %v", febUsage, err)
		}
		var refs struct {
			UserID sql.NullString `db:"user_id"`
			JobID  sql.NullString `db:"job_id"`
			Month  string         `db:"month"`
			USD    float64        `db:"usd"`
		}
		if err := reopened.Get(ctx, &refs, `SELECT user_id, job_id, month, usd FROM spend_ledger WHERE id = ?`, p.ReservationID); err != nil {
			t.Fatal(err)
		}
		if refs.UserID.Valid || refs.JobID.Valid || refs.Month != "2026-01" || refs.USD != 0.75 {
			t.Fatalf("repaired ledger attribution = %+v", refs)
		}
	})
}

func TestSpendDispatchRevalidatesJobProviderAndSettings(t *testing.T) {
	forEachSpendDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixed := time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC)
		setSpendLimits(t, ta, 10, 10)
		f := seedSpendFixture(t, ta, "server")
		store := ai.NewSpendStore(ta.Deps.DB, ta.Deps.Settings)
		store.Now = func() time.Time { return fixed }
		newParams := func() ai.ReserveRemoteCallParams {
			return reserveParams(f, db.NewID(), ai.SpendPurposeInitial, 0.10, fixed.Add(time.Minute))
		}

		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET state = 'queued' WHERE id = ?`, f.JobID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReserveRemoteCall(ctx, newParams()); !errors.Is(err, ai.ErrJobAttemptNotRunning) {
			t.Fatalf("queued job error = %v", err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET state = 'running', attempts = 2 WHERE id = ?`, f.JobID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReserveRemoteCall(ctx, newParams()); !errors.Is(err, ai.ErrJobAttemptNotRunning) {
			t.Fatalf("wrong attempt error = %v", err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET attempts = 1 WHERE id = ?`, f.JobID); err != nil {
			t.Fatal(err)
		}
		mismatchedProvider := insertProvider(t, ta, "server", "", true, "sealed-other-key")
		providerMismatch := newParams()
		providerMismatch.ProviderID = mismatchedProvider
		if _, err := store.ReserveRemoteCall(ctx, providerMismatch); !errors.Is(err, ai.ErrProviderUnavailable) {
			t.Fatalf("job/provider route mismatch error = %v", err)
		}
		modelMismatch := newParams()
		modelMismatch.ModelID = "openrouter/different-model"
		if _, err := store.ReserveRemoteCall(ctx, modelMismatch); !errors.Is(err, ai.ErrProviderUnavailable) {
			t.Fatalf("job/model route mismatch error = %v", err)
		}
		var mismatchRows int
		if err := ta.Deps.DB.Get(ctx, &mismatchRows, `SELECT COUNT(*) FROM spend_reservations WHERE id IN (?, ?)`,
			providerMismatch.ReservationID, modelMismatch.ReservationID); err != nil || mismatchRows != 0 {
			t.Fatalf("route mismatch reservations = %d, %v", mismatchRows, err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE providers SET enabled = FALSE WHERE id = ?`, f.ProviderID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReserveRemoteCall(ctx, newParams()); !errors.Is(err, ai.ErrProviderUnavailable) {
			t.Fatalf("disabled provider error = %v", err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE providers SET enabled = TRUE, key_enc = '' WHERE id = ?`, f.ProviderID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReserveRemoteCall(ctx, newParams()); !errors.Is(err, ai.ErrProviderUnavailable) {
			t.Fatalf("blank key error = %v", err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE providers SET key_enc = 'sealed-test-key' WHERE id = ?`, f.ProviderID); err != nil {
			t.Fatal(err)
		}
		other := ta.CreateUser(t, "", auth.RoleCreator)
		foreignProvider := insertProvider(t, ta, "user", other.ID, true, "sealed-foreign-key")
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET provider_id = ? WHERE id = ?`, foreignProvider, f.JobID); err != nil {
			t.Fatal(err)
		}
		foreign := newParams()
		foreign.ProviderID = foreignProvider
		if _, err := store.ReserveRemoteCall(ctx, foreign); !errors.Is(err, ai.ErrProviderUnavailable) {
			t.Fatalf("foreign user provider error = %v", err)
		}
		if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(inst *settings.Instance) error {
			inst.LocalOnly = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReserveRemoteCall(ctx, newParams()); !errors.Is(err, ai.ErrRemoteDisabled) {
			t.Fatalf("local-only error = %v", err)
		}
		if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(inst *settings.Instance) error {
			inst.LocalOnly = false
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_at)
			VALUES ('limits', '{broken-json', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			db.Now()); err != nil {
			t.Fatal(err)
		}
		brokenSettings := settings.New(ta.Deps.DB)
		brokenStore := ai.NewSpendStore(ta.Deps.DB, brokenSettings)
		brokenStore.Now = func() time.Time { return fixed }
		broken := newParams()
		if _, err := brokenStore.ReserveRemoteCall(ctx, broken); err == nil {
			t.Fatal("settings decode failure permitted a reservation")
		}
		var stored int
		if err := ta.Deps.DB.Get(ctx, &stored, `SELECT COUNT(*) FROM spend_reservations WHERE id = ?`, broken.ReservationID); err != nil || stored != 0 {
			t.Fatalf("settings failure stored reservation: count=%d err=%v", stored, err)
		}
	})
}
