package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// secondInstance opens another connection pool and queue on the same
// database, like a second server process sharing it.
func secondInstance(t *testing.T, ta *testutil.TestApp) *jobs.Queue {
	t.Helper()
	dialect, dsn, err := ta.Deps.Cfg.Database()
	if err != nil {
		t.Fatal(err)
	}
	d2, err := db.Open(context.Background(), dialect, dsn, db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d2.Close() })
	return jobs.New(d2, settings.New(d2), nil, jobs.Options{})
}

// TestConcurrentClaimStress hammers Claim from many goroutines (on two
// "server instances" for Postgres) and checks that no job is ever handed out twice, every
// job runs exactly once and the per-user running limit (fair share) holds.
func TestConcurrentClaimStress(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		const perUserLimit = 2
		if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(i *settings.Instance) error {
			i.Limits.RunningJobs = perUserLimit
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		queues := []*jobs.Queue{ta.Deps.Jobs}
		if ta.Deps.DB.Dialect() == db.Postgres {
			// Postgres deployments may run several server processes on one database.
			// (SQLite is single-process: its writers are serialised in-process.)
			queues = append(queues, secondInstance(t, ta))
		}
		users := make([]string, 5)
		for i := range users {
			users[i] = ta.CreateUser(t, fmt.Sprintf("stress%d", i), auth.RoleCreator).ID
		}
		const perUser = 24
		total := 0
		for i := 0; i < perUser; i++ {
			for _, u := range users {
				if _, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "stress", UserID: u, Scope: none}); err != nil {
					t.Fatal(err)
				}
				total++
			}
		}

		var (
			mu      sync.Mutex
			claimed = map[string]int{}
			running = map[string]int{}
			maxRun  int
			done    atomic.Int64
			errs    = make(chan error, 64)
		)
		deadline := time.Now().Add(90 * time.Second)
		var wg sync.WaitGroup
		for g := 0; g < 16; g++ {
			q := queues[g%len(queues)]
			wg.Add(1)
			go func() {
				defer wg.Done()
				for done.Load() < int64(total) && time.Now().Before(deadline) {
					j, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"stress"}})
					if err != nil {
						errs <- err
						return
					}
					if j == nil {
						time.Sleep(2 * time.Millisecond)
						continue
					}
					mu.Lock()
					claimed[j.ID]++
					running[j.UserID]++
					if running[j.UserID] > maxRun {
						maxRun = running[j.UserID]
					}
					mu.Unlock()
					time.Sleep(time.Millisecond)
					mu.Lock()
					running[j.UserID]--
					mu.Unlock()
					if _, err := q.Complete(ctx, j.ID, "", map[string]any{"ok": true}, jobs.Cost{}); err != nil {
						errs <- fmt.Errorf("complete %s: %w", j.ID, err)
						return
					}
					done.Add(1)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		if int(done.Load()) != total {
			t.Fatalf("finished %d of %d jobs before the deadline", done.Load(), total)
		}
		for id, n := range claimed {
			if n != 1 {
				t.Fatalf("job %s claimed %d times", id, n)
			}
		}
		if len(claimed) != total {
			t.Fatalf("claimed %d distinct jobs, want %d", len(claimed), total)
		}
		if maxRun > perUserLimit {
			t.Fatalf("a user had %d jobs running at once (limit %d)", maxRun, perUserLimit)
		}
		var bad int
		if err := ta.Deps.DB.Get(ctx, &bad, `SELECT COUNT(*) FROM jobs WHERE type = 'stress' AND (state <> 'done' OR attempts <> 1)`); err != nil {
			t.Fatal(err)
		}
		if bad != 0 {
			t.Fatalf("%d jobs not done exactly once", bad)
		}
	})
}

// TestStaleLeaseCannotFinishReclaimedJob covers the lease fence: after a
// lease expires and another runner re-claims the job, the first runner's
// late progress, completion or failure must be rejected (lease lost).
func TestStaleLeaseCannotFinishReclaimedJob(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		q := ta.Deps.Jobs
		clock := testutil.NewClock(time.Now())
		q.Now = clock.Now

		// A Go-executor handler that stalls past its lease.
		started, release := make(chan struct{}), make(chan struct{})
		var runs atomic.Int32
		pool := jobs.NewPool(q, 1, nil)
		pool.Register("slow", func(ctx context.Context, run *jobs.Run) error {
			if runs.Add(1) == 1 {
				close(started)
				<-release
				run.SetOutput(map[string]any{"from": "stale"})
				return nil
			}
			run.SetOutput(map[string]any{"from": "fresh"})
			return nil
		})
		j, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "slow", Scope: none})
		if err != nil {
			t.Fatal(err)
		}
		staleDone := make(chan struct{})
		go func() {
			defer close(staleDone)
			if _, err := pool.RunOnce(ctx); err != nil {
				t.Error(err)
			}
		}()
		<-started
		clock.Advance(5 * time.Minute) // lease (60 s) long gone
		if n, err := q.RequeueExpired(ctx); err != nil || n != 1 {
			t.Fatalf("requeue expired: %d %v", n, err)
		}
		clock.Advance(time.Minute) // past the retry backoff
		if ok, err := pool.RunOnce(ctx); err != nil || !ok {
			t.Fatalf("second run: %v %v", ok, err)
		}
		close(release)
		<-staleDone
		got, _ := q.Get(ctx, j.ID)
		if got.State != jobs.StateDone || string(got.Output) != `{"from":"fresh"}` || got.Attempts != 2 {
			t.Fatalf("stale run overwrote the re-claimed job: state=%s output=%s attempts=%d", got.State, got.Output, got.Attempts)
		}

		// Worker executor: the public API pins the worker id at write time.
		wj, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "w", Scope: none, Executor: jobs.ExecutorWorker})
		wa, err := q.RegisterWorker(ctx, "tok", "", jobs.Registration{Name: "a", JobTypes: []string{"w"}})
		if err != nil {
			t.Fatal(err)
		}
		wb, _ := q.RegisterWorker(ctx, "tok", "", jobs.Registration{Name: "b", JobTypes: []string{"w"}})
		c, _ := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"w"}, WorkerID: wa.ID})
		if c == nil || c.ID != wj.ID {
			t.Fatalf("worker a claim: %+v", c)
		}
		clock.Advance(5 * time.Minute)
		q.RequeueExpired(ctx)
		clock.Advance(time.Minute)
		if c, _ := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"w"}, WorkerID: wb.ID}); c == nil {
			t.Fatal("worker b could not re-claim")
		}
		if _, err := q.Progress(ctx, wj.ID, wa.ID, jobs.ProgressUpdate{Progress: .5}); !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("stale progress: %v", err)
		}
		if _, err := q.Complete(ctx, wj.ID, wa.ID, nil, jobs.Cost{}); !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("stale complete: %v", err)
		}
		if _, err := q.Fail(ctx, wj.ID, wa.ID, jobs.JobError{Code: "internal"}, jobs.Cost{}); !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("stale fail: %v", err)
		}
		if g, _ := q.Get(ctx, wj.ID); g.State != jobs.StateRunning || g.WorkerID != wb.ID {
			t.Fatalf("job must stay with worker b: %+v", g)
		}
	})
}

// TestRequeueExpiredSparesRenewedLease: a heartbeat that lands between the
// expiry sweep's read and its write keeps the job with its runner.
func TestRequeueExpiredSparesRenewedLease(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		q := ta.Deps.Jobs
		clock := testutil.NewClock(time.Now())
		q.Now = clock.Now
		j, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", Scope: none})
		if c, _ := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"t"}}); c == nil {
			t.Fatal("no claim")
		}
		clock.Advance(2 * time.Minute)
		// Simulate the sweep having read the job as expired, then the runner renewing:
		// a sweep that runs after the renewal must not requeue it.
		if err := q.Heartbeat(ctx, j.ID, ""); err != nil {
			t.Fatal(err)
		}
		if n, err := q.RequeueExpired(ctx); err != nil || n != 0 {
			t.Fatalf("requeued a renewed lease: %d %v", n, err)
		}
		if g, _ := q.Get(ctx, j.ID); g.State != jobs.StateRunning {
			t.Fatalf("renewed job lost: %+v", g)
		}
	})
}

// TestCancelRacesClaim: cancel and claim running at the same time either
// cancel a queued job or request cancellation of the running one, never
// leave a claimed job marked canceled while it runs.
func TestCancelRacesClaim(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		q := ta.Deps.Jobs
		for i := 0; i < 30; i++ {
			j, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "race", Scope: none})
			var claimed *jobs.Job
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				claimed, _ = q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"race"}})
			}()
			go func() {
				defer wg.Done()
				if _, err := q.Cancel(ctx, j.ID); err != nil {
					t.Error(err)
				}
			}()
			wg.Wait()
			g, _ := q.Get(ctx, j.ID)
			switch {
			case claimed != nil && g.State == jobs.StateRunning && g.CancelRequested:
				if _, err := q.Fail(ctx, j.ID, "", jobs.JobError{Code: jobs.ErrCodeCanceled}, jobs.Cost{}); err != nil {
					t.Fatal(err)
				}
			case claimed == nil && g.State == jobs.StateCanceled:
			default:
				t.Fatalf("inconsistent: claimed=%v state=%s cancel_requested=%v", claimed != nil, g.State, g.CancelRequested)
			}
		}
	})
}
