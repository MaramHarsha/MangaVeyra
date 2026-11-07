package jobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestEnqueueTxSeesUncommittedScopeAndRollsBackWithIt(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, q := context.Background(), ta.Deps.Jobs
		actor := studio.Actor{UserID: ta.Admin.ID}
		svc := core.MustUse[studio.Service](ta.Deps)
		project, err := svc.CreateProject(ctx, actor, studio.NewProject{Title: "Glass Harbour", Format: "manga_rtl", Language: "en"})
		if err != nil {
			t.Fatal(err)
		}
		chapter, err := svc.CreateChapter(ctx, actor, project.ID, "A Letter in the Rain")
		if err != nil {
			t.Fatal(err)
		}
		rec := &recorder{}
		q.SetPublisher(rec)
		rollback := errors.New("rollback pages and children")
		for _, commit := range []bool{false, true} {
			pageID := db.NewID()
			var child *jobs.Job
			err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO pages (id, project_id, chapter_id, number) VALUES (?, ?, ?, 1)`, pageID, project.ID, chapter.ID); err != nil {
					return err
				}
				var err error
				child, err = q.EnqueueTx(ctx, tx, jobs.EnqueueParams{Type: "panel.test", UserID: actor.UserID,
					Scope: jobs.Scope{Kind: jobs.ScopePage, ProjectID: project.ID, PageID: pageID},
					Guard: func(ctx context.Context, tx *db.Tx) error {
						var count int
						if err := tx.Get(ctx, &count, `SELECT COUNT(*) FROM pages WHERE id = ?`, pageID); err != nil {
							return err
						}
						if count != 1 {
							return rollback
						}
						return nil
					}})
				if err != nil {
					return err
				}
				if len(rec.get("job:"+child.ID)) != 0 {
					t.Error("published before commit")
				}
				if !commit {
					return rollback
				}
				return nil
			})
			if commit && err != nil || !commit && !errors.Is(err, rollback) {
				t.Fatalf("commit=%v: %v", commit, err)
			}
			if child == nil {
				t.Fatal("no transactional child")
			}
			stored, err := q.Get(ctx, child.ID)
			if !commit {
				if !errors.Is(err, jobs.ErrNotFound) {
					t.Fatalf("rolled back job: %v", err)
				}
				var events int
				if err := ta.Deps.DB.Get(ctx, &events, `SELECT COUNT(*) FROM job_events WHERE job_id = ?`, child.ID); err != nil || events != 0 {
					t.Fatalf("events leaked: %d %v", events, err)
				}
				continue
			}
			if err != nil || stored.State != jobs.StateQueued {
				t.Fatalf("stored child: %+v %v", stored, err)
			}
			events, err := q.Events(ctx, child.ID)
			if err != nil || len(events) != 1 || events[0].Kind != "created" {
				t.Fatalf("created event: %+v %v", events, err)
			}
			claimed, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"panel.test"}})
			if err != nil || claimed == nil || claimed.ID != child.ID {
				t.Fatalf("durable child not claimable: %+v %v", claimed, err)
			}
		}
	})
}
