package ai

import (
	"context"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

func TestImageFallbackAcceptsCompletedPageBatchButNeverWorkerChildren(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		f.createProvider(t, m.providers, providerScopeServer, "batch-or")
		if _, err := m.providers.Create(ctx, providerActor{UserID: f.userID, Admin: true}, providerCreate{Kind: providerKindFal, Name: "Batch fal", Key: "batch-fal", Scope: providerScopeServer}); err != nil {
			t.Fatal(err)
		}
		plan, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}})
		if err != nil {
			t.Fatal(err)
		}
		jobID := imageTestJob(t, m, f, plan)
		j, err := m.d.Jobs.Get(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		chapterID := db.NewID()
		if _, err := d.Exec(ctx, `INSERT INTO chapters (id,project_id,number,title) VALUES (?,?,1,'Tide gate')`, chapterID, f.projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `INSERT INTO pages (id,project_id,chapter_id,number) VALUES (?,?,?,1)`, j.Scope.PageID, f.projectID, chapterID); err != nil {
			t.Fatal(err)
		}
		parent, err := m.d.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "pages.from_script", UserID: f.userID, Scope: jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: f.projectID, ChapterID: chapterID}})
		if err != nil {
			t.Fatal(err)
		}
		j.ParentID = parent.ID
		failure := jobs.JobError{Code: CodeTimeout, Retryable: true}
		assertNoFallback := func() {
			t.Helper()
			if next, err := m.image.fallback(ctx, j, plan, failure); err != nil || next != nil {
				t.Fatalf("ineligible job fallback=%+v %v", next, err)
			}
		}
		assertNoFallback() // queued ancestor cannot have committed a page batch
		if _, err := d.Exec(ctx, `UPDATE jobs SET state='done' WHERE id=?`, parent.ID); err != nil {
			t.Fatal(err)
		}
		if next, err := m.image.fallback(ctx, j, plan, failure); err != nil || next == nil || next.Executor != jobs.ExecutorGo {
			t.Fatalf("batch fallback=%+v %v", next, err)
		}
		j.Executor = jobs.ExecutorWorker
		assertNoFallback()
		j.Executor = jobs.ExecutorGo
		if _, err := d.Exec(ctx, `UPDATE jobs SET type='character.sheet' WHERE id=?`, parent.ID); err != nil {
			t.Fatal(err)
		}
		assertNoFallback()
		if _, err := d.Exec(ctx, `UPDATE jobs SET type='pages.from_script' WHERE id=?`, parent.ID); err != nil {
			t.Fatal(err)
		}
		other := db.NewID()
		if _, err := d.Exec(ctx, `INSERT INTO chapters (id,project_id,number,title) VALUES (?,?,2,'Another tide')`, other, f.projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `UPDATE pages SET chapter_id=? WHERE id=?`, other, j.Scope.PageID); err != nil {
			t.Fatal(err)
		}
		assertNoFallback()
	})
}
