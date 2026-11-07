package studioui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// Return the first script's real database snapshot, then commit the job before
// loadStoryView can list it. No scheduling, sleeps, browser reloads or production
// test hooks are needed to reproduce the mixed pre/post-completion reads.
type completingStoryService struct {
	studio.Service
	firstRead func() error
	reads     int
}

func (s *completingStoryService) GetScript(ctx context.Context, chapterID string) (studio.ScriptDoc, error) {
	doc, err := s.Service.GetScript(ctx, chapterID)
	s.reads++
	if err == nil && s.reads == 1 && s.firstRead != nil {
		err = s.firstRead()
	}
	return doc, err
}

type storySnapshotFixture struct {
	module  *Module
	service *completingStoryService
	db      *db.DB
	actor   studio.Actor
	project studio.Project
	chapter studio.Chapter
	job     *jobs.Job
}

func newStorySnapshotFixture(t *testing.T, d *db.DB) storySnapshotFixture {
	t.Helper()
	ctx := context.Background()
	userID := db.NewID()
	if _, err := d.Exec(ctx, `INSERT INTO users (id, username, role) VALUES (?, ?, 'creator')`, userID, "courier-"+userID); err != nil {
		t.Fatal(err)
	}
	deps := &core.Deps{DB: d, Settings: settings.New(d)}
	deps.Jobs = jobs.New(d, deps.Settings, nil, jobs.Options{})
	studio.New(deps)
	svc := core.MustUse[studio.Service](deps)
	actor := studio.Actor{UserID: userID}
	project, err := svc.CreateProject(ctx, actor, studio.NewProject{Title: "Tidebreak Courier", Premise: "A courier follows a harbour lantern.",
		Format: "manga_rtl", PageSize: "b5", Language: "ja", Style: "bw_screentone", PageWidth: 1600, PageHeight: 2400})
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := svc.CreateChapter(ctx, actor, project.ID, "The Lantern Crossing")
	if err != nil {
		t.Fatal(err)
	}
	job, err := deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: gen.JobScript, UserID: userID,
		Scope: jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: project.ID, ChapterID: chapter.ID}})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &completingStoryService{Service: svc}
	return storySnapshotFixture{module: &Module{d: deps, studio: wrapped}, service: wrapped, db: d,
		actor: actor, project: project, chapter: chapter, job: job}
}

func (f storySnapshotFixture) complete(ctx context.Context, withScript, locked, revoke bool) error {
	return f.db.WithTx(ctx, func(tx *db.Tx) error {
		if withScript {
			doc := studio.ScriptDoc{Schema: ai.SchemaScriptV1, ChapterID: f.chapter.ID, ChapterSummary: "A light crosses the tide.",
				Scenes: []studio.ScriptScene{{Key: "lantern-scene", Title: "The Last Lantern", Summary: "The courier departs.",
					TimeOfDay: "dusk", Panels: []studio.ScriptPanel{{Key: "lantern-panel", PageNumber: 1, Camera: "wide",
						Shot: "establishing", Emotion: "hopeful", Action: "The courier steps onto the footbridge.",
						Dialogue: []studio.ScriptDialogue{}, SFX: []studio.ScriptSFX{}, CharacterIDs: []string{}}}}}}
			if _, err := f.service.Service.ReplaceGeneratedScriptTx(ctx, tx, studio.ReplaceScriptInput{Actor: f.actor,
				ProjectID: f.project.ID, ChapterID: f.chapter.ID, Doc: doc, JobID: f.job.ID, ProviderID: ai.PlaceholderID,
				ModelID: ai.PlaceholderText, Provenance: "ai", ExpectedUpdatedAt: f.chapter.UpdatedAt}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE chapters SET locked = ? WHERE id = ?`, locked, f.chapter.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = 'done', progress = 1, finished_at = ? WHERE id = ?`, db.Now(), f.job.ID); err != nil {
			return err
		}
		if revoke {
			_, err := tx.Exec(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, f.project.ID, f.actor.UserID)
			return err
		}
		return nil
	})
}

func TestStoryReloadsManuscriptAndRevisionWhenGenerationCompletesBetweenReads(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		for _, locked := range []bool{false, true} {
			t.Run(map[bool]string{false: "editable", true: "locked"}[locked], func(t *testing.T) {
				ctx := i18n.WithLocale(context.Background(), "ja")
				f := newStorySnapshotFixture(t, d)
				f.service.firstRead = func() error { return f.complete(ctx, true, locked, false) }
				v, err := f.module.loadStoryView(ctx, f.actor, f.project.ID, f.chapter.ID)
				if err != nil {
					t.Fatal(err)
				}
				if f.service.reads != 2 || len(v.Script.Scenes) != 1 || v.Script.Scenes[0].Title != "The Last Lantern" || v.Job != nil {
					t.Fatalf("mixed completion snapshots were rendered: reads=%d, scenes=%d, job=%+v", f.service.reads, len(v.Script.Scenes), v.Job)
				}
				stored, err := f.service.Service.GetChapter(ctx, f.chapter.ID)
				if err != nil || !v.Chapter.UpdatedAt.Equal(stored.UpdatedAt) || !v.Chapter.UpdatedAt.After(f.chapter.UpdatedAt) || v.ReadOnly != locked {
					t.Fatalf("stale chapter revision/lock: view=%+v, stored=%+v, readOnly=%v, err=%v", v.Chapter, stored, v.ReadOnly, err)
				}
				if len(v.Chapters) != 1 || !v.Chapters[0].UpdatedAt.Equal(stored.UpdatedAt) {
					t.Fatalf("chapter tree retained the old revision: %+v", v.Chapters)
				}
				var html bytes.Buffer
				if err := StoryPage(v).Render(ctx, &html); err != nil {
					t.Fatal(err)
				}
				body := html.String()
				if !strings.Contains(body, `data-view-panel="panels"`) || !strings.Contains(body, stored.UpdatedAt.Format(time.RFC3339Nano)) {
					t.Fatal("the response did not contain the persisted manuscript and its fresh form revision")
				}
				if strings.Contains(body, i18n.T(ctx, "studioui.job.done")) || strings.Contains(body, i18n.T(ctx, "studioui.generate.title")) {
					t.Fatal("completed manuscript still shows its ready/empty-generation state")
				}
			})
		}
	})
}

func TestStoryCompletedLegacyEmptyResultReloadsOnlyOnce(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := i18n.WithLocale(context.Background(), "ja")
		f := newStorySnapshotFixture(t, d)
		f.service.firstRead = func() error { return f.complete(ctx, false, false, false) }
		v, err := f.module.loadStoryView(ctx, f.actor, f.project.ID, f.chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		if f.service.reads != 2 || len(v.Script.Scenes) != 0 || v.Job == nil || v.Job.State != "ready" || v.Job.Active {
			t.Fatalf("empty legacy completion was not bounded: reads=%d, scenes=%d, job=%+v", f.service.reads, len(v.Script.Scenes), v.Job)
		}
		if v.Job.Title != i18n.T(ctx, "studioui.job.done") || i18n.LocaleFrom(ctx) != "ja" {
			t.Fatal("completion recovery changed the interface locale")
		}
	})
}

func TestStoryCompletionReloadRechecksReadAuthorization(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		f := newStorySnapshotFixture(t, d)
		f.service.firstRead = func() error { return f.complete(ctx, true, false, true) }
		_, err := f.module.loadStoryView(ctx, f.actor, f.project.ID, f.chapter.ID)
		if !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("revoked membership was accepted while recovering completion: %v", err)
		}
		if f.service.reads != 1 {
			t.Fatalf("new manuscript read after membership was revoked: reads=%d", f.service.reads)
		}
	})
}
