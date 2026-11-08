package gen_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// C3's drift line is scoped to the chapter its Fix link opens in D12: the
// lowest-numbered chapter with drift and only that chapter's appearances.
func TestLockStatusScopesTheDriftLineToTheFixChapter(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps, pages := controlPages(t, ta, 1)
		second, err := f.studio.CreateChapter(ctx, f.actor, f.project.ID, "The Second Tide")
		if err != nil {
			t.Fatal(err)
		}
		var copies []pagedoc.Page
		for n := range 2 {
			p := pages[0]
			p.ID, p.ChapterID, p.Number = db.NewID(), second.ID, n+1
			p.Panels = []pagedoc.Panel{pages[0].Panels[0]}
			p.Panels[0].ID = db.NewID()
			p.Panels[0].Layers = append([]pagedoc.Layer(nil), pages[0].Panels[0].Layers...)
			for i := range p.Panels[0].Layers {
				p.Panels[0].Layers[i].ID = db.NewID()
			}
			p.Bubbles = nil
			copies = append(copies, p)
		}
		if err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := ps.CreatePagesTx(ctx, tx, second.ID, copies, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "create_page"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		control := core.MustUse[gen.ControlService](ta.Deps)
		c := f.characters[0]
		status, err := control.LockStatus(ctx, f.actor, f.project.ID, c.ID)
		if err != nil || status.ScoringWorker {
			t.Fatalf("status without a worker: %+v %v", status, err)
		}
		job, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "test.assessment_fixture", Scope: jobs.Scope{Kind: jobs.ScopeProject, ProjectID: f.project.ID}, UserID: f.actor.UserID, Executor: jobs.ExecutorWorker, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		// Every appearance shares one raster asset here, so one fixture
		// measurement below the threshold marks all three as drifted.
		if _, err = ta.Deps.DB.Exec(ctx, `INSERT INTO character_assessments(id,project_id,character_id,asset_id,reference_hash,method,identity_score,outfit_score,job_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, db.NewID(), f.project.ID, c.ID, pages[0].Panels[0].Layers[1].Asset.ID, status.ReferenceFingerprint, "dinov2-small-v1:"+strings.Repeat("a", 64), .8, .85, job.ID, db.Now()); err != nil {
			t.Fatal(err)
		}
		lockWorkerSetup(t, ta, jobs.Registration{Name: "Scoring fixture", Backends: []string{"vision"}, JobTypes: []string{"character.score"}, Models: []string{dinoModel}})
		status, err = control.LockStatus(ctx, f.actor, f.project.ID, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status.DriftTotal != 3 || status.DriftChapterID != f.chapter.ID || status.DriftChapterNumber != f.chapter.Number || status.DriftChapterTotal != 1 || !status.ScoringWorker {
			t.Fatalf("drift scope = total %d, chapter %s/%d with %d, worker %v", status.DriftTotal, status.DriftChapterID, status.DriftChapterNumber, status.DriftChapterTotal, status.ScoringWorker)
		}
		chapter, err := control.LockChapterStatus(ctx, f.actor, f.project.ID, c.ID, second.ID)
		if err != nil || chapter.DriftTotal != 2 || chapter.DriftChapterTotal != 2 || chapter.DriftChapterNumber != second.Number {
			t.Fatalf("chapter status = %+v %v", chapter, err)
		}
	})
}
