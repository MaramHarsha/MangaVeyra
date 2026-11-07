package gen_test

import (
	"context"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestScriptJobStaleAttemptCannotPersistAfterAttemptTwo(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "", 0)
		seedScript(t, fixture, "Original script.")
		clock := testutil.NewClock(time.Now())
		ta.Deps.Jobs.Now = clock.Now
		attemptOneStarted := make(chan struct{})
		releaseAttemptOne := make(chan struct{})
		fake := &fakeTextService{generateFn: func(_ context.Context, request ai.TextGenerateRequest) (ai.TextResult, error) {
			if request.Plan.Estimate != (jobs.Cost{}) {
				t.Fatalf("placeholder per-attempt estimate = %+v", request.Plan.Estimate)
			}
			switch request.Attempt {
			case 1:
				close(attemptOneStarted)
				<-releaseAttemptOne
				return ai.TextResult{Document: generatedScriptJSON("", "Stale attempt one."),
					ProviderID: request.Plan.ProviderID, ModelID: request.Plan.ModelID}, nil
			case 2:
				return ai.TextResult{Document: generatedScriptJSON("", "Winning attempt two."),
					ProviderID: request.Plan.ProviderID, ModelID: request.Plan.ModelID}, nil
			default:
				t.Fatalf("unexpected attempt %d", request.Attempt)
				return ai.TextResult{}, nil
			}
		}}
		fixture.useText(fake)
		job, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
		})
		if err != nil {
			t.Fatal(err)
		}
		firstDone := make(chan struct{})
		go func() {
			_, _ = ta.Deps.Executor.RunOnce(ctx)
			close(firstDone)
		}()
		select {
		case <-attemptOneStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("attempt one did not start")
		}
		clock.Advance(2 * time.Minute)
		if count, err := ta.Deps.Jobs.RequeueExpired(ctx); err != nil || count != 1 {
			t.Fatalf("RequeueExpired = %d, %v", count, err)
		}
		clock.Advance(10 * time.Second)
		if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
			t.Fatalf("attempt two RunOnce = %v, %v", ran, err)
		}
		assertScriptAction(t, fixture, "Winning attempt two.")
		close(releaseAttemptOne)
		select {
		case <-firstDone:
		case <-time.After(5 * time.Second):
			t.Fatal("stale attempt did not finish")
		}
		stored, err := ta.Deps.Jobs.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.State != jobs.StateDone || stored.Attempts != 2 {
			t.Fatalf("final job = %+v", stored)
		}
		assertScriptAction(t, fixture, "Winning attempt two.")
	})
}

func TestScriptJobsPlannedFromSameRevisionCannotOverwriteWinner(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "", 0)
		calls := 0
		fixture.useText(&fakeTextService{generateFn: func(_ context.Context, request ai.TextGenerateRequest) (ai.TextResult, error) {
			calls++
			action := "First queued script wins."
			if calls == 2 {
				action = "Second queued script must not overwrite."
			}
			return ai.TextResult{Document: generatedScriptJSON("", action),
				ProviderID: request.Plan.ProviderID, ModelID: request.Plan.ModelID}, nil
		}})
		first, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
		})
		if err != nil {
			t.Fatal(err)
		}
		second, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
		})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
				t.Fatalf("RunOnce = %v, %v", ran, err)
			}
		}
		firstStored, err := ta.Deps.Jobs.Get(ctx, first.ID)
		if err != nil {
			t.Fatal(err)
		}
		secondStored, err := ta.Deps.Jobs.Get(ctx, second.ID)
		if err != nil {
			t.Fatal(err)
		}
		if firstStored.State != jobs.StateDone || secondStored.State != jobs.StateFailed ||
			secondStored.Error == nil || secondStored.Error.Code != jobs.ErrCodeBadInput {
			t.Fatalf("same-revision jobs = first=%+v second=%+v", firstStored, secondStored)
		}
		assertScriptAction(t, fixture, "First queued script wins.")
	})
}

func TestScriptJobRestoresPerAttemptEstimateFromActionAggregate(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "", 0)
		providerID := "provider-remote"
		fake := &fakeTextService{
			planFn: func(context.Context, ai.TextPlanRequest) (ai.TextPlan, error) {
				return remoteTextPlan(ai.ModeBalanced, providerID, 0.25), nil
			},
			generateFn: func(_ context.Context, request ai.TextGenerateRequest) (ai.TextResult, error) {
				if request.Plan.Estimate != (jobs.Cost{USD: 0.25}) {
					t.Fatalf("GenerateText estimate = %+v, want per-attempt $0.25", request.Plan.Estimate)
				}
				return ai.TextResult{Document: generatedScriptJSON("", "Remote plan persisted."),
					ProviderID: request.Plan.ProviderID, ModelID: request.Plan.ModelID}, nil
			},
		}
		fixture.useText(fake)
		job, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
		})
		if err != nil {
			t.Fatal(err)
		}
		if job.Estimate != (jobs.Cost{USD: 0.75}) {
			t.Fatalf("queued action estimate = %+v", job.Estimate)
		}
		if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
			t.Fatalf("RunOnce = %v, %v", ran, err)
		}
		stored, _ := ta.Deps.Jobs.Get(ctx, job.ID)
		if stored.State != jobs.StateDone {
			t.Fatalf("job = %+v", stored)
		}
	})
}
