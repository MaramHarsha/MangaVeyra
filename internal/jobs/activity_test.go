package jobs_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func TestActivityUsageWindowOwnershipAndChildren(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			owner := ta.CreateUser(t, "harbour-creator", auth.RoleCreator)
			other := ta.CreateUser(t, "orchard-creator", auth.RoleCreator)
			since := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
			makeJob := func(user, parent string, usd, gpu float64, finished *time.Time) *jobs.Job {
				t.Helper()
				j, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "activity.fixture", Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: user, ParentID: parent})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET actual = ?, finished_at = ? WHERE id = ?`, db.MustJSON(jobs.Cost{USD: usd, GPUSeconds: gpu}), finished, j.ID); err != nil {
					t.Fatal(err)
				}
				return j
			}
			parent := makeJob(owner.ID, "", 0.3, 120, &since)
			makeJob(owner.ID, parent.ID, 0.3, 120, &since) // already represented by the parent
			makeJob(other.ID, "", 0.2, 60, &since)
			old := since.Add(-time.Second)
			makeJob(owner.ID, "", 10, 3600, &old)
			makeJob(owner.ID, "", 20, 7200, nil) // unfinished work has no actual in this window
			mine, err := ta.Deps.Jobs.CompletedUsage(ctx, owner.ID, since)
			if err != nil || math.Abs(mine.USD-0.3) > 1e-9 || mine.GPUSeconds != 120 {
				t.Fatalf("member total: %+v %v", mine, err)
			}
			all, err := ta.Deps.Jobs.CompletedUsage(ctx, "", since)
			if err != nil || math.Abs(all.USD-0.5) > 1e-9 || all.GPUSeconds != 180 {
				t.Fatalf("server total: %+v %v", all, err)
			}
		})
	}
}

func TestRecentActivityEventsBoundedAndWithoutPrivatePayloads(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			j, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "activity.fixture", Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: ta.Admin.ID})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 125; i++ {
				if err := ta.Deps.Jobs.Log(ctx, j.ID, fmt.Sprintf("fixture_%03d", i), "private upstream text", map[string]string{"prompt": "private material"}); err != nil {
					t.Fatal(err)
				}
			}
			events, err := ta.Deps.Jobs.RecentEvents(ctx, j.ID, 20)
			if err != nil || len(events) != 20 {
				t.Fatalf("bounded events: %d %v", len(events), err)
			}
			if events[0].Kind != "fixture_105" || events[19].Kind != "fixture_124" {
				t.Fatal("events are not the latest twenty in chronological order")
			}
			for _, ev := range events {
				if ev.Message != "" || ev.RawData != "" || ev.Data != nil {
					t.Fatal("activity events exposed an upstream message or payload")
				}
			}
		})
	}
}
