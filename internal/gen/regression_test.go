package gen_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// Concurrent submits for one chapter enqueue exactly one script job; the rest
// get ErrScriptJobActive instead of paying for duplicate generations.
func TestConcurrentEnqueueCreatesOneScriptJob(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "", 0)
		const submits = 16
		var (
			wg             sync.WaitGroup
			created, dupes atomic.Int32
			unexpected     = make(chan error, submits)
		)
		start := make(chan struct{})
		for range submits {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
					ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
				})
				switch {
				case err == nil:
					created.Add(1)
				case errors.Is(err, gen.ErrScriptJobActive):
					dupes.Add(1)
				default:
					unexpected <- err
				}
			}()
		}
		close(start)
		wg.Wait()
		close(unexpected)
		for err := range unexpected {
			t.Errorf("unexpected enqueue error: %v", err)
		}
		var stored int
		if err := ta.Deps.DB.Get(ctx, &stored, `SELECT COUNT(*) FROM jobs WHERE chapter_id = ? AND type = ?`,
			fixture.chapter.ID, gen.JobScript); err != nil {
			t.Fatal(err)
		}
		if created.Load() != 1 || dupes.Load() != submits-1 || stored != 1 {
			t.Fatalf("created=%d duplicates=%d stored=%d, want 1/%d/1", created.Load(), dupes.Load(), stored, submits-1)
		}
	})
}

// A handler from before a manual retry must not persist or complete the job:
// Retry keeps attempt numbers unique, so the old lease no longer matches.
func TestRetriedScriptJobRejectsStaleAttempt(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "", 0)
		clock := testutil.NewClock(time.Now())
		ta.Deps.Jobs.Now = clock.Now
		var calls atomic.Int32
		staleStarted, releaseStale := make(chan struct{}), make(chan struct{})
		freshStarted, releaseFresh := make(chan struct{}), make(chan struct{})
		fixture.useText(&fakeTextService{generateFn: func(_ context.Context, r ai.TextGenerateRequest) (ai.TextResult, error) {
			action, started, release := "Fresh retried run.", freshStarted, releaseFresh
			if calls.Add(1) == 1 {
				action, started, release = "STALE first run.", staleStarted, releaseStale
			}
			close(started)
			<-release
			return ai.TextResult{Document: generatedScriptJSON("", action), ProviderID: r.Plan.ProviderID, ModelID: r.Plan.ModelID}, nil
		}})
		job, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{ProjectID: fixture.project.ID,
			ChapterID: fixture.chapter.ID, Mode: "premise"})
		if err != nil {
			t.Fatal(err)
		}
		staleDone := make(chan struct{})
		go func() { _, _ = ta.Deps.Executor.RunOnce(ctx); close(staleDone) }()
		<-staleStarted
		// Cancellation requested by another app instance (its in-process hook
		// does not reach this pool), then the lease expires.
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET cancel_requested = TRUE WHERE id = ?`, job.ID); err != nil {
			t.Fatal(err)
		}
		clock.Advance(2 * time.Minute)
		if n, err := ta.Deps.Jobs.RequeueExpired(ctx); err != nil || n != 1 {
			t.Fatalf("RequeueExpired = %d, %v", n, err)
		}
		if _, err := ta.Deps.Jobs.Retry(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		freshDone := make(chan struct{})
		go func() { _, _ = ta.Deps.Executor.RunOnce(ctx); close(freshDone) }()
		<-freshStarted
		close(releaseStale) // the stale handler resumes while the retried attempt runs
		<-staleDone
		if mid, err := ta.Deps.Jobs.Get(ctx, job.ID); err != nil || mid.State != jobs.StateRunning {
			t.Fatalf("stale handler changed the retried job: %+v, %v", mid, err)
		}
		if doc, err := fixture.studio.GetScript(ctx, fixture.chapter.ID); err != nil || len(doc.Scenes) != 0 {
			t.Fatalf("stale handler persisted a script: %+v, %v", doc, err)
		}
		close(releaseFresh)
		<-freshDone
		final, err := ta.Deps.Jobs.Get(ctx, job.ID)
		if err != nil || final.State != jobs.StateDone || final.Attempts != 2 || final.RoundAttempt() != 1 {
			t.Fatalf("final job = %+v, %v", final, err)
		}
		assertScriptAction(t, fixture, "Fresh retried run.")
	})
}

// Plan 0001 bounds panels per document, so a generated scene may be empty; the
// chapter must stay editable afterwards.
func TestGeneratedEmptySceneStaysEditable(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "", 0)
		var doc map[string]any
		if err := json.Unmarshal(generatedScriptJSON("", "Panel one."), &doc); err != nil {
			t.Fatal(err)
		}
		doc["scenes"] = append(doc["scenes"].([]any), map[string]any{"key": "scene-empty", "title": "Quiet beat",
			"summary": "", "time_of_day": "", "panels": []any{}})
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		fixture.useText(&fakeTextService{generateFn: func(_ context.Context, r ai.TextGenerateRequest) (ai.TextResult, error) {
			return ai.TextResult{Document: raw, ProviderID: r.Plan.ProviderID, ModelID: r.Plan.ModelID}, nil
		}})
		job, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{ProjectID: fixture.project.ID,
			ChapterID: fixture.chapter.ID, Mode: "premise"})
		if err != nil {
			t.Fatal(err)
		}
		if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
			t.Fatalf("RunOnce = %v, %v", ran, err)
		}
		if stored, err := ta.Deps.Jobs.Get(ctx, job.ID); err != nil || stored.State != jobs.StateDone {
			t.Fatalf("job = %+v, %v", stored, err)
		}
		current, err := fixture.studio.GetScript(ctx, fixture.chapter.ID)
		if err != nil || len(current.Scenes) != 2 || len(current.Scenes[1].Panels) != 0 {
			t.Fatalf("stored script = %+v, %v", current, err)
		}
		chapter, err := fixture.studio.GetChapter(ctx, fixture.chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		current.Scenes[0].Panels[0].Action = "Edited by a human."
		current.Scenes[1].Title = "Quiet beat, revised"
		if _, err := fixture.studio.EditScript(ctx, studio.EditScriptInput{Actor: fixture.actor, ProjectID: fixture.project.ID,
			ChapterID: fixture.chapter.ID, Doc: current, ExpectedUpdatedAt: chapter.UpdatedAt}); err != nil {
			t.Fatalf("edit script with an empty scene: %v", err)
		}
		edited, err := fixture.studio.GetScript(ctx, fixture.chapter.ID)
		if err != nil || len(edited.Scenes) != 2 || edited.Scenes[0].Panels[0].Action != "Edited by a human." ||
			edited.Scenes[1].Title != "Quiet beat, revised" || len(edited.Scenes[1].Panels) != 0 {
			t.Fatalf("edited script = %+v, %v", edited, err)
		}
	})
}

// A caller-validated revision binds the enqueue: once the chapter changed
// (here: a script was saved), the request is refused before any job exists.
func TestEnqueueScriptRejectsStaleExpectedRevision(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "", 0)
		validated := fixture.chapter.UpdatedAt
		first, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{ProjectID: fixture.project.ID,
			ChapterID: fixture.chapter.ID, Mode: "premise", ExpectedUpdatedAt: validated})
		if err != nil {
			t.Fatal(err)
		}
		if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
			t.Fatalf("RunOnce = %v, %v", ran, err)
		}
		if done, err := ta.Deps.Jobs.Get(ctx, first.ID); err != nil || done.State != jobs.StateDone {
			t.Fatalf("first job = %+v, %v", done, err)
		}
		_, err = fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{ProjectID: fixture.project.ID,
			ChapterID: fixture.chapter.ID, Mode: "premise", ExpectedUpdatedAt: validated})
		if !errors.Is(err, studio.ErrConflict) {
			t.Fatalf("enqueue against a stale revision = %v, want studio.ErrConflict", err)
		}
		var count int
		if err := ta.Deps.DB.Get(ctx, &count, `SELECT COUNT(*) FROM jobs WHERE chapter_id = ?`, fixture.chapter.ID); err != nil || count != 1 {
			t.Fatalf("jobs for chapter = %d, %v", count, err)
		}
	})
}
