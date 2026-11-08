package jobs_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// A worker job started by a parent handler is part of that parent: canceling
// the parent stops it, and it cannot be retried on its own after the parent
// finished (nothing would wait for or apply its result).
func TestWorkerChildrenFollowTheirParent(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		q := ta.Deps.Jobs
		owner := ta.CreateUser(t, "child-owner", auth.RoleCreator)
		enqueue := func(typ, executor, parent string) *jobs.Job {
			t.Helper()
			j, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: typ, Executor: executor, UserID: owner.ID, Scope: none, ParentID: parent, MaxAttempts: 1})
			if err != nil {
				t.Fatal(err)
			}
			return j
		}
		parent := enqueue("child.parent", jobs.ExecutorGo, "")
		if _, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{parent.Type}}); err != nil {
			t.Fatal(err)
		}
		queuedChild := enqueue("child.score", jobs.ExecutorWorker, parent.ID)
		runningChild := enqueue("child.score", jobs.ExecutorWorker, parent.ID)
		batchChild := enqueue("child.panel", jobs.ExecutorGo, parent.ID)
		if _, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"child.score"}}); err != nil {
			t.Fatal(err)
		}
		if cur, _ := q.Get(ctx, queuedChild.ID); cur.State == jobs.StateRunning {
			queuedChild, runningChild = runningChild, queuedChild // claim order is by age
		}
		if _, err := q.Cancel(ctx, parent.ID); err != nil {
			t.Fatal(err)
		}
		if cur, _ := q.Get(ctx, parent.ID); cur.State != jobs.StateRunning || !cur.CancelRequested {
			t.Fatalf("parent: %+v", cur)
		}
		if cur, _ := q.Get(ctx, queuedChild.ID); cur.State != jobs.StateCanceled {
			t.Fatalf("queued worker child not canceled with its parent: %s", cur.State)
		}
		if cur, _ := q.Get(ctx, runningChild.ID); cur.State != jobs.StateRunning || !cur.CancelRequested {
			t.Fatalf("running worker child not asked to stop with its parent: %+v", cur)
		}
		if cur, _ := q.Get(ctx, batchChild.ID); cur.State != jobs.StateQueued {
			t.Fatalf("an independent Go child job was canceled: %s", cur.State)
		}
		if _, err := q.Fail(ctx, parent.ID, "", jobs.JobError{Code: jobs.ErrCodeCanceled}, jobs.Cost{}); err != nil {
			t.Fatal(err)
		}

		if _, err := q.Retry(ctx, queuedChild.ID); err == nil || !strings.Contains(err.Error(), "child_job") {
			t.Fatalf("child retry: %v", err)
		}
		session := ta.Login(t, owner.Username)
		got := session.PostJSON("/api/v1/jobs/"+queuedChild.ID+"/retry", nil)
		if got.StatusCode != http.StatusConflict || got.ErrorCode() != "child_job" ||
			!strings.Contains(string(got.Body), "Retry that job instead") || !strings.Contains(string(got.Body), parent.ID) {
			t.Fatalf("API child retry: %d %s", got.StatusCode, got.Body)
		}
		if cur, _ := q.Get(ctx, queuedChild.ID); cur.State != jobs.StateCanceled {
			t.Fatalf("refused retry changed the child: %s", cur.State)
		}
		if got := session.PostJSON("/api/v1/jobs/"+parent.ID+"/retry", nil); got.StatusCode != http.StatusOK {
			t.Fatalf("parent retry: %d %s", got.StatusCode, got.Body)
		}
	})
}
