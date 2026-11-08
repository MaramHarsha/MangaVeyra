package jobs_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// TestDedupeKeyHoldsAcrossServerProcesses enqueues the same keyed work from
// many goroutines on two queues with separate connection pools, as two
// server processes sharing one database would: exactly one job is stored and
// every caller receives it.
func TestDedupeKeyHoldsAcrossServerProcesses(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		queues := []*jobs.Queue{ta.Deps.Jobs, secondInstance(t, ta)}
		user := ta.CreateUser(t, "", auth.RoleCreator)
		const n = 8
		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			ids   = map[string]int{}
			fresh int
			start = make(chan struct{})
			errs  = make(chan error, n)
		)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				j, err := queues[i%2].Enqueue(ctx, jobs.EnqueueParams{Type: "dedupe.race", UserID: user.ID, Scope: none, DedupeKey: "same work",
					// Holding the transaction open widens the window in which both
					// processes have passed the first lookup.
					Guard: func(context.Context, *db.Tx) error { time.Sleep(15 * time.Millisecond); return nil }})
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				ids[j.ID]++
				if !j.Deduplicated {
					fresh++
				}
				mu.Unlock()
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		if len(ids) != 1 || fresh != 1 {
			t.Fatalf("identical concurrent requests stored %d jobs (%d reported new): %v", len(ids), fresh, ids)
		}
		var stored int
		if err := ta.Deps.DB.Get(ctx, &stored, `SELECT COUNT(*) FROM jobs WHERE type = 'dedupe.race'`); err != nil || stored != 1 {
			t.Fatalf("stored jobs = %d %v", stored, err)
		}
	})
}

func TestDedupeKeyReleasesFinishedWorkAndSeparatesKeys(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, q := context.Background(), ta.Deps.Jobs
		rec := &recorder{}
		q.SetPublisher(rec)
		user := ta.CreateUser(t, "", auth.RoleCreator)
		enqueue := func(typ, key string) *jobs.Job {
			t.Helper()
			j, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: typ, UserID: user.ID, Scope: none, DedupeKey: key})
			if err != nil {
				t.Fatal(err)
			}
			return j
		}
		// claim runs the only queued job of typ, which must be want.
		claim := func(typ string, want *jobs.Job) {
			t.Helper()
			j, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{typ}})
			if err != nil || j == nil || j.ID != want.ID {
				t.Fatalf("claim %s: %+v %v", typ, j, err)
			}
		}
		fresh := func(j *jobs.Job, previous *jobs.Job, what string) {
			t.Helper()
			if j.Deduplicated || j.ID == previous.ID {
				t.Fatalf("%s blocked new work", what)
			}
		}

		first := enqueue("dedupe.a", "k")
		again := enqueue("dedupe.a", "k")
		if first.Deduplicated || !again.Deduplicated || again.ID != first.ID {
			t.Fatalf("queued job not reused: %+v %+v", first, again)
		}
		if got := rec.get("job:" + first.ID); len(got) != 1 {
			t.Fatalf("a reused job was published again: %v", got)
		}
		// Running jobs still hold their key.
		claim("dedupe.a", first)
		if j := enqueue("dedupe.a", "k"); j.ID != first.ID || !j.Deduplicated {
			t.Fatal("running job not reused")
		}

		// Done, failed and canceled jobs, and a job being canceled, never block.
		if _, err := q.Complete(ctx, first.ID, "", map[string]any{"ok": true}, jobs.Cost{}); err != nil {
			t.Fatal(err)
		}
		afterDone := enqueue("dedupe.a", "k")
		fresh(afterDone, first, "a finished job")
		claim("dedupe.a", afterDone)
		if _, err := q.Fail(ctx, afterDone.ID, "", jobs.JobError{Code: jobs.ErrCodeBadInput, Message: "x"}, jobs.Cost{}); err != nil {
			t.Fatal(err)
		}
		afterFail := enqueue("dedupe.a", "k")
		fresh(afterFail, afterDone, "a failed job")
		if _, err := q.Cancel(ctx, afterFail.ID); err != nil {
			t.Fatal(err)
		}
		afterCancel := enqueue("dedupe.a", "k")
		fresh(afterCancel, afterFail, "a canceled job")
		claim("dedupe.a", afterCancel)
		if j, err := q.Cancel(ctx, afterCancel.ID); err != nil || !j.CancelRequested || j.State != jobs.StateRunning {
			t.Fatalf("cancel request: %+v %v", j, err)
		}
		fresh(enqueue("dedupe.a", "k"), afterCancel, "a job being canceled")

		// Different keys, job types and unkeyed jobs are different work.
		base := enqueue("dedupe.b", "k")
		seen := map[string]bool{base.ID: true}
		for _, j := range []*jobs.Job{enqueue("dedupe.b", "other"), enqueue("dedupe.c", "k"), enqueue("dedupe.b", ""), enqueue("dedupe.b", "")} {
			if j.Deduplicated || seen[j.ID] {
				t.Fatalf("distinct work was merged: %+v", j)
			}
			seen[j.ID] = true
		}
	})
}

// TestDedupeKeyRetryAndGuards covers the manual retry of a keyed job and the
// rule that an existing active job is returned before any guard runs.
func TestDedupeKeyRetryAndGuards(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, q := context.Background(), ta.Deps.Jobs
		user := ta.CreateUser(t, "", auth.RoleCreator)
		guardErr := errors.New("guard rejects")
		calls := 0
		params := func(reject bool) jobs.EnqueueParams {
			return jobs.EnqueueParams{Type: "dedupe.retry", UserID: user.ID, Scope: none, DedupeKey: "k", Guard: func(context.Context, *db.Tx) error {
				calls++
				if reject {
					return guardErr
				}
				return nil
			}}
		}
		if _, err := q.Enqueue(ctx, params(true)); !errors.Is(err, guardErr) {
			t.Fatalf("guard ignored: %v", err)
		}
		a, err := q.Enqueue(ctx, params(false))
		if err != nil {
			t.Fatal(err)
		}
		if again, err := q.Enqueue(ctx, params(true)); err != nil || again.ID != a.ID || !again.Deduplicated || calls != 2 {
			t.Fatalf("existing job not returned before the guard: %+v %v calls=%d", again, err, calls)
		}
		if _, err := q.Cancel(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		b, err := q.Enqueue(ctx, params(false))
		if err != nil || b.ID == a.ID || b.Deduplicated {
			t.Fatalf("new work after cancel: %+v %v", b, err)
		}
		// Retrying the canceled job would queue the same work twice.
		if _, err := q.Retry(ctx, a.ID); !errors.Is(err, jobs.ErrDuplicate) {
			t.Fatalf("duplicate retry = %v", err)
		}
		if j, _ := q.Get(ctx, a.ID); j.State != jobs.StateCanceled {
			t.Fatalf("refused retry changed the job: %s", j.State)
		}
		if _, err := q.Cancel(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		retried, err := q.Retry(ctx, a.ID)
		if err != nil || retried.State != jobs.StateQueued {
			t.Fatalf("retry after the holder finished: %+v %v", retried, err)
		}
		if again, err := q.Enqueue(ctx, params(false)); err != nil || again.ID != a.ID || !again.Deduplicated {
			t.Fatalf("retried job does not hold its key: %+v %v", again, err)
		}
		long := jobs.EnqueueParams{Type: "dedupe.retry", Scope: none, DedupeKey: strings.Repeat("x", jobs.MaxDedupeKeyBytes+1)}
		if _, err := q.Enqueue(ctx, long); err == nil {
			t.Fatal("oversized key accepted")
		}
	})
}

// TestNoRetryTypesRefuseManualRetry: a job type marked MarkNoRetry carries a
// human decision, so neither its owner nor an admin can queue it again by
// retrying (through the queue or the jobs API); other types still retry.
func TestNoRetryTypesRefuseManualRetry(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, q := context.Background(), ta.Deps.Jobs
		q.MarkNoRetry("noretry.decision")
		owner := ta.CreateUser(t, "", auth.RoleCreator)
		decision, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "noretry.decision", UserID: owner.ID, Scope: none})
		if err != nil {
			t.Fatal(err)
		}
		other, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "noretry.other", UserID: owner.ID, Scope: none})
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range []*jobs.Job{decision, other} {
			if _, err := q.Cancel(ctx, j.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := q.Retry(ctx, decision.ID); !errors.Is(err, jobs.ErrNoRetry) {
			t.Fatalf("Retry(no-retry type) = %v, want ErrNoRetry", err)
		}
		for name, c := range map[string]*testutil.Client{"owner": ta.Login(t, owner.Username), "admin": ta.Login(t, "admin")} {
			if got := c.PostJSON("/api/v1/jobs/"+decision.ID+"/retry", nil); got.StatusCode != http.StatusConflict {
				t.Fatalf("%s API retry of a no-retry job = %d %s", name, got.StatusCode, got.Body)
			}
		}
		if j, _ := q.Get(ctx, decision.ID); j.State != jobs.StateCanceled {
			t.Fatalf("refused retry changed the job: %s", j.State)
		}
		if j, err := q.Retry(ctx, other.ID); err != nil || j.State != jobs.StateQueued {
			t.Fatalf("other types still retry: %+v %v", j, err)
		}
	})
}

// TestDedupeMigrationUpgradesExistingQueues applies 0204 to a database that
// already holds jobs and checks the partial unique index on both dialects.
func TestDedupeMigrationUpgradesExistingQueues(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ctx := context.Background()
			tg := testdb.NewTarget(t, dialect)
			d, err := db.Open(ctx, tg.Dialect, tg.DSN, db.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			before := fstest.MapFS{}
			all := db.Migrations()
			entries, err := fs.ReadDir(all, ".")
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "0204_") {
					continue
				}
				data, err := fs.ReadFile(all, e.Name())
				if err != nil {
					t.Fatal(err)
				}
				before[e.Name()] = &fstest.MapFile{Data: data}
			}
			if _, err := d.MigrateFS(ctx, before); err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{"queued", "running", "done"} {
				if _, err := d.Exec(ctx, `INSERT INTO jobs (id, type, state) VALUES (?, 'legacy', ?)`, db.NewID(), state); err != nil {
					t.Fatal(err)
				}
			}
			applied, err := d.Migrate(ctx)
			if err != nil || len(applied) != 1 || applied[0] != "0204_job_dedupe_key" {
				t.Fatalf("upgrade applied %v: %v", applied, err)
			}
			var legacy int
			if err := d.Get(ctx, &legacy, `SELECT COUNT(*) FROM jobs WHERE type = 'legacy' AND dedupe_key IS NULL`); err != nil || legacy != 3 {
				t.Fatalf("legacy rows = %d %v", legacy, err)
			}
			insert := func(state string, cancel bool) error {
				_, err := d.Exec(ctx, `INSERT INTO jobs (id, type, state, cancel_requested, dedupe_key) VALUES (?, 'keyed', ?, ?, 'digest')`, db.NewID(), state, cancel)
				return err
			}
			// Inactive rows may share a key with each other and with one active row.
			for _, row := range []struct {
				state  string
				cancel bool
			}{{"done", false}, {"failed", false}, {"canceled", false}, {"running", true}, {"queued", false}} {
				if err := insert(row.state, row.cancel); err != nil {
					t.Fatalf("%s: %v", row.state, err)
				}
			}
			for _, state := range []string{"queued", "running"} {
				if err := insert(state, false); !db.IsUniqueViolation(err) {
					t.Fatalf("second active %s row: %v", state, err)
				}
			}
		})
	}
}
