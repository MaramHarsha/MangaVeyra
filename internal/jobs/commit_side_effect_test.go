package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func commitHarness(t *testing.T) (*testutil.TestApp, *jobs.Queue, *jobs.Pool) {
	t.Helper()
	ta := testutil.NewTestApp(t)
	if _, err := ta.Deps.DB.Exec(context.Background(), `CREATE TABLE commit_effects (job_id TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	q := ta.Deps.Jobs
	return ta, q, jobs.NewPool(q, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func effectCount(t *testing.T, ta *testutil.TestApp, id string) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM commit_effects WHERE job_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	return n
}

func kindCount(t *testing.T, ta *testutil.TestApp, id, kind string) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND kind = ?`, id, kind); err != nil {
		t.Fatal(err)
	}
	return n
}

type donePanicPublisher struct{}

func (donePanicPublisher) Publish(_ string, typ string, _ any) {
	if typ == "job.done" {
		panic("publisher failed")
	}
}

func TestCommitSideEffectAtomicSuccess(t *testing.T) {
	ta, q, pool := commitHarness(t)
	ctx := context.Background()
	j, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "commit.success", Scope: jobs.Scope{Kind: jobs.ScopeNone}})
	if err != nil {
		t.Fatal(err)
	}
	q.SetPublisher(donePanicPublisher{})
	var hooks atomic.Int32
	var hookSawWrite atomic.Bool
	q.OnFinish(func(ctx context.Context, got *jobs.Job) {
		if got.ID != j.ID {
			return
		}
		hooks.Add(1)
		var n int
		if ta.Deps.DB.Get(ctx, &n, `SELECT COUNT(*) FROM commit_effects WHERE job_id = ?`, j.ID) == nil && n == 1 {
			hookSawWrite.Store(true)
		}
	})
	pool.Register("commit.success", func(ctx context.Context, run *jobs.Run) error {
		run.SetOutput(map[string]string{"value": "wrong"})
		run.AddActual(jobs.Cost{USD: 1.25})
		return run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
			if _, err := tx.Exec(ctx, `INSERT INTO commit_effects (job_id, value) VALUES (?, ?)`, run.Job.ID, "saved"); err != nil {
				return nil, err
			}
			return map[string]string{"value": "committed"}, nil
		})
	})
	if ok, err := pool.RunOnce(ctx); err != nil || !ok {
		t.Fatalf("RunOnce() = %v, %v", ok, err)
	}
	got, _ := q.Get(ctx, j.ID)
	var out map[string]string
	if err := got.DecodeOutput(&out); err != nil {
		t.Fatal(err)
	}
	if got.State != jobs.StateDone || out["value"] != "committed" || got.Actual.USD != 1.25 || effectCount(t, ta, j.ID) != 1 || kindCount(t, ta, j.ID, "done") != 1 {
		t.Fatalf("job = %+v, output = %#v", got, out)
	}
	if hooks.Load() != 1 || !hookSawWrite.Load() {
		t.Fatalf("hooks = %d, saw write = %v", hooks.Load(), hookSawWrite.Load())
	}
}

func TestCommitSideEffectRollback(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(error) (any, error)
		want string
	}{
		{"callback error", func(want error) (any, error) { return nil, want }, "rejected"},
		{"marshal error", func(error) (any, error) { return make(chan int), nil }, "unsupported type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta, q, pool := commitHarness(t)
			ctx := context.Background()
			want := errors.New("rejected")
			errCh := make(chan error, 1)
			pool.Register("commit.rollback", func(ctx context.Context, run *jobs.Run) error {
				err := run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
					if _, err := tx.Exec(ctx, `INSERT INTO commit_effects (job_id, value) VALUES (?, ?)`, run.Job.ID, "rollback"); err != nil {
						return nil, err
					}
					return tc.fn(want)
				})
				errCh <- err
				return err
			})
			j, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "commit.rollback", Scope: jobs.Scope{Kind: jobs.ScopeNone}})
			if ok, err := pool.RunOnce(ctx); err != nil || !ok {
				t.Fatalf("RunOnce() = %v, %v", ok, err)
			}
			commitErr := <-errCh
			if commitErr == nil || !strings.Contains(commitErr.Error(), tc.want) {
				t.Fatalf("CommitSideEffect() error = %v", commitErr)
			}
			got, _ := q.Get(ctx, j.ID)
			if got.State != jobs.StateFailed || effectCount(t, ta, j.ID) != 0 || kindCount(t, ta, j.ID, "done") != 0 {
				t.Fatalf("job = %+v", got)
			}
		})
	}
}

func TestCommitSideEffectStaleAttemptCallbackNotCalled(t *testing.T) {
	ta, q, pool := commitHarness(t)
	ctx := context.Background()
	q.Now = ta.Clock.Now
	started, resume, firstDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	staleErr := make(chan error, 1)
	var attempt, staleCalls atomic.Int32
	pool.Register("commit.stale", func(ctx context.Context, run *jobs.Run) error {
		if attempt.Add(1) == 1 {
			close(started)
			<-resume
			err := run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
				staleCalls.Add(1)
				_, err := tx.Exec(ctx, `INSERT INTO commit_effects (job_id, value) VALUES (?, ?)`, run.Job.ID, "attempt-1")
				return nil, err
			})
			staleErr <- err
			return err
		}
		return run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
			if _, err := tx.Exec(ctx, `INSERT INTO commit_effects (job_id, value) VALUES (?, ?)`, run.Job.ID, "attempt-2"); err != nil {
				return nil, err
			}
			return map[string]int{"attempt": 2}, nil
		})
	})
	j, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "commit.stale", Scope: jobs.Scope{Kind: jobs.ScopeNone}, MaxAttempts: 2})
	go func() { _, _ = pool.RunOnce(ctx); close(firstDone) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first attempt did not start")
	}
	ta.Clock.Advance(2 * time.Minute)
	if n, err := q.RequeueExpired(ctx); err != nil || n != 1 {
		t.Fatalf("RequeueExpired() = %d, %v", n, err)
	}
	ta.Clock.Advance(10 * time.Second)
	if ok, err := pool.RunOnce(ctx); err != nil || !ok {
		t.Fatalf("second RunOnce() = %v, %v", ok, err)
	}
	close(resume)
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stale attempt did not return")
	}
	if err := <-staleErr; !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale error = %v", err)
	}
	var value string
	if err := ta.Deps.DB.Get(ctx, &value, `SELECT value FROM commit_effects WHERE job_id = ?`, j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := q.Get(ctx, j.ID)
	if staleCalls.Load() != 0 || got.State != jobs.StateDone || got.Attempts != 2 || value != "attempt-2" || kindCount(t, ta, j.ID, "done") != 1 {
		t.Fatalf("job = %+v, value = %q, stale calls = %d", got, value, staleCalls.Load())
	}
}

func TestCommitSideEffectLateErrorAndDoubleCommit(t *testing.T) {
	for _, lateErr := range []bool{false, true} {
		t.Run(map[bool]string{false: "double", true: "late error"}[lateErr], func(t *testing.T) {
			ta, q, pool := commitHarness(t)
			ctx := context.Background()
			secondErr := make(chan error, 1)
			var secondCalls atomic.Int32
			pool.Register("commit.terminal", func(ctx context.Context, run *jobs.Run) error {
				if err := run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
					_, err := tx.Exec(ctx, `INSERT INTO commit_effects (job_id, value) VALUES (?, ?)`, run.Job.ID, "first")
					return map[string]int{"commit": 1}, err
				}); err != nil {
					return err
				}
				if lateErr {
					return errors.New("late handler error")
				}
				err := run.CommitSideEffect(ctx, func(context.Context, *db.Tx) (any, error) {
					secondCalls.Add(1)
					return nil, nil
				})
				secondErr <- err
				return nil
			})
			j, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "commit.terminal", Scope: jobs.Scope{Kind: jobs.ScopeNone}})
			if ok, err := pool.RunOnce(ctx); err != nil || !ok {
				t.Fatalf("RunOnce() = %v, %v", ok, err)
			}
			if !lateErr {
				if err := <-secondErr; !errors.Is(err, jobs.ErrAlreadyCommitted) {
					t.Fatalf("second error = %v", err)
				}
			}
			got, _ := q.Get(ctx, j.ID)
			if got.State != jobs.StateDone || effectCount(t, ta, j.ID) != 1 || secondCalls.Load() != 0 || kindCount(t, ta, j.ID, "done") != 1 || kindCount(t, ta, j.ID, "failed") != 0 {
				t.Fatalf("job = %+v, second calls = %d", got, secondCalls.Load())
			}
		})
	}
}

func TestCommitSideEffectCancellationSkipsCallback(t *testing.T) {
	ta, q, pool := commitHarness(t)
	ctx := context.Background()
	started, proceed, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	errCh := make(chan error, 1)
	var calls atomic.Int32
	pool.Register("commit.canceled", func(ctx context.Context, run *jobs.Run) error {
		close(started)
		<-proceed
		err := run.CommitSideEffect(context.WithoutCancel(ctx), func(context.Context, *db.Tx) (any, error) {
			calls.Add(1)
			return nil, nil
		})
		errCh <- err
		return err
	})
	j, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "commit.canceled", Scope: jobs.Scope{Kind: jobs.ScopeNone}})
	go func() { _, _ = pool.RunOnce(ctx); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
	if _, err := q.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return")
	}
	if err := <-errCh; !errors.Is(err, jobs.ErrCanceled) {
		t.Fatalf("commit error = %v", err)
	}
	got, _ := q.Get(ctx, j.ID)
	if got.State != jobs.StateCanceled || calls.Load() != 0 || effectCount(t, ta, j.ID) != 0 || kindCount(t, ta, j.ID, "done") != 0 {
		t.Fatalf("job = %+v, callback calls = %d", got, calls.Load())
	}
}
