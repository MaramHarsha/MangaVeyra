package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/activityui"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// Job notifications (and their live toasts) carry a human summary in the
// recipient's language, project · what was made · duration, never the job
// type; a parent's worker child adds no notification of its own.
func TestJobNotificationsCarryAHumanSummary(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			q := ta.Deps.Jobs
			clock := testutil.NewClock(time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC))
			q.Now = clock.Now
			for _, lang := range []string{"en", "ja"} {
				u := ta.CreateUser(t, "summary-"+lang, auth.RoleCreator)
				if _, err := ta.Deps.DB.Exec(ctx, `UPDATE users SET locale = ? WHERE id = ?`, lang, u.ID); err != nil {
					t.Fatal(err)
				}
				project := db.NewID()
				if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO projects (id, owner_id, title, slug) VALUES (?, ?, 'Tidebreak Courier', ?)`, project, u.ID, "tidebreak-"+project); err != nil {
					t.Fatal(err)
				}
				done, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "page.generate", UserID: u.ID, Scope: jobs.Scope{Kind: jobs.ScopeNone}, Batch: true, MaxAttempts: 1})
				if err != nil {
					t.Fatal(err)
				}
				// The scope is stored directly: this test is about the notification, not page validation.
				if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET scope = ?, scope_kind = 'project', project_id = ? WHERE id = ?`,
					db.MustJSON(jobs.Scope{Kind: jobs.ScopeProject, ProjectID: project}), project, done.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{done.Type}}); err != nil {
					t.Fatal(err)
				}
				child, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "character.score", Executor: jobs.ExecutorWorker, UserID: u.ID,
					Scope: jobs.Scope{Kind: jobs.ScopeNone}, ParentID: done.ID, MaxAttempts: 1})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{child.Type}}); err != nil {
					t.Fatal(err)
				}
				if _, err := q.Fail(ctx, child.ID, "", jobs.JobError{Code: "provider_error", Message: "upstream said: invalid key sk-live-SECRET"}, jobs.Cost{}); err != nil {
					t.Fatal(err)
				}
				clock.Advance(38 * time.Second)
				if _, err := q.Complete(ctx, done.ID, "", nil, jobs.Cost{}); err != nil {
					t.Fatal(err)
				}
				notices, err := ta.Deps.Notify.List(ctx, u.ID, notifyListAll())
				if err != nil || len(notices) != 1 {
					t.Fatalf("%s: want only the parent's notification: %+v %v", lang, notices, err)
				}
				want := map[string]string{"en": "Tidebreak Courier · Page art · 38 s", "ja": "Tidebreak Courier · ページの絵 · 38秒"}[lang]
				n := notices[0]
				if n.Kind != "job_done" || n.Body != want || n.Data["body"] != activityui.JobSummaryBody || strings.Contains(n.Body, "page.generate") {
					t.Fatalf("%s: notification body = %q (%+v), want %q", lang, n.Body, n.Data, want)
				}
			}
		})
	}
}
