package gen_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestEnqueueScriptAssemblesPremiseAdaptPinsAndEffectiveDefault(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "private fallback\nwith exact spacing  ", 2)
		fake := &fakeTextService{}
		fixture.useText(fake)

		premiseJob, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise", PagesTarget: 8,
			PinProviderID: ai.PlaceholderID, PinModelID: ai.PlaceholderText,
		})
		if err != nil {
			t.Fatalf("enqueue premise: %v", err)
		}
		if premiseJob.MaxAttempts != gateAScriptMaxAttempts || premiseJob.Estimate != (jobs.Cost{}) {
			t.Fatalf("premise job retry/cost = %d/%+v", premiseJob.MaxAttempts, premiseJob.Estimate)
		}
		var premisePayload storedScriptPayload
		if err := json.Unmarshal(premiseJob.Input, &premisePayload); err != nil {
			t.Fatal(err)
		}
		if premisePayload.Version != gateAScriptPayloadVersion ||
			!premisePayload.ExpectedUpdatedAt.Equal(fixture.chapter.UpdatedAt) ||
			premisePayload.RoutingMode != ai.ModeBalanced || premisePayload.Remote || premisePayload.TokenID != "" {
			t.Fatalf("stored effective route = %+v", premisePayload)
		}
		if premisePayload.Input.Premise != fixture.project.Premise || premisePayload.Input.SourceText != "" ||
			premisePayload.Input.ChapterNumber != fixture.chapter.Number || premisePayload.Input.ChapterTitle != fixture.chapter.Title ||
			len(premisePayload.Input.Characters) != 2 || premisePayload.Input.Characters[0].ID != fixture.characters[0].ID ||
			premisePayload.Input.Characters[1].ID != fixture.characters[1].ID {
			t.Fatalf("premise input was not the deterministic Studio snapshot: %+v", premisePayload.Input)
		}

		// Only one script job may be active per chapter; each variant below
		// is inspected and then canceled so the next one can be planned.
		cancelQueued := func(job *jobs.Job) {
			t.Helper()
			if canceled, err := ta.Deps.Jobs.Cancel(ctx, job.ID); err != nil || canceled.State != jobs.StateCanceled {
				t.Fatalf("cancel queued script job: %+v, %v", canceled, err)
			}
		}
		if _, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise", PagesTarget: 8,
		}); !errors.Is(err, gen.ErrScriptJobActive) {
			t.Fatalf("second active script job = %v, want ErrScriptJobActive", err)
		}
		cancelQueued(premiseJob)

		explicit := "  explicit source\n\tkeeps bytes ✓  "
		explicitJob, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "adapt", SourceText: explicit,
			RoutingMode: ai.ModeLocalOnly,
		})
		if err != nil {
			t.Fatalf("enqueue explicit adaptation: %v", err)
		}
		cancelQueued(explicitJob)
		_, err = fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "adapt",
		})
		if err != nil {
			t.Fatalf("enqueue fallback adaptation: %v", err)
		}

		// The rejected duplicate was planned before its enqueue guard ran.
		requests := fake.planRequests()
		if len(requests) != 4 {
			t.Fatalf("PlanText calls = %d, want 4", len(requests))
		}
		requests = append(requests[:1], requests[2:]...)
		if requests[0].RoutingMode != "" || requests[0].PinProviderID != ai.PlaceholderID || requests[0].PinModelID != ai.PlaceholderText {
			t.Fatalf("default mode or pins changed before PlanText: %+v", requests[0])
		}
		var explicitInput, fallbackInput ai.ScriptInput
		if err := json.Unmarshal(requests[1].Input, &explicitInput); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(requests[2].Input, &fallbackInput); err != nil {
			t.Fatal(err)
		}
		if explicitInput.SourceText != explicit {
			t.Fatalf("explicit source changed: %q", explicitInput.SourceText)
		}
		if fallbackInput.SourceText != "private fallback\nwith exact spacing  " {
			t.Fatalf("fallback source changed: %q", fallbackInput.SourceText)
		}
		if requests[1].RoutingMode != ai.ModeLocalOnly || requests[2].RoutingMode != "" {
			t.Fatalf("routing requests changed: %q, %q", requests[1].RoutingMode, requests[2].RoutingMode)
		}

		for _, job := range []*jobs.Job{premiseJob} {
			events, err := ta.Deps.Jobs.Events(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if strings.Contains(event.Message+string(event.Data), explicit) || strings.Contains(event.Message+string(event.Data), "private fallback") {
					t.Fatal("private source leaked into a job event")
				}
			}
		}
	})
}

func TestEnqueueScriptRechecksTokenScopesApprovalAndRevocationWithoutJobs(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "", 0)
		providerID := db.NewID()
		fake := &fakeTextService{planFn: func(context.Context, ai.TextPlanRequest) (ai.TextPlan, error) {
			return remoteTextPlan(ai.ModeBalanced, providerID, 0.25), nil
		}}
		fixture.useText(fake)

		jobCount := func() int {
			var count int
			if err := ta.Deps.DB.Get(ctx, &count, `SELECT COUNT(*) FROM jobs`); err != nil {
				t.Fatal(err)
			}
			return count
		}
		makeToken := func(name string, scopes []string, projects []string, threshold *float64) *auth.TokenInfo {
			_, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: fixture.owner.ID,
				Kind: auth.TokenAgent, Name: name, Scopes: scopes, ProjectIDs: projects, ApproveOverUSD: threshold})
			if err != nil {
				t.Fatalf("CreateToken(%s): %v", name, err)
			}
			return info
		}
		enqueue := func(token *auth.TokenInfo) (*jobs.Job, error) {
			return fixture.gen.EnqueueScript(ctx, studio.Actor{UserID: fixture.owner.ID, TokenID: token.ID, SiteRole: auth.RoleAdmin},
				gen.ScriptRequest{ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise"})
		}

		before := jobCount()
		missingGenerate := makeToken("missing-generate", []string{"projects:write"}, nil, nil)
		if _, err := enqueue(missingGenerate); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("missing generate scope: %v", err)
		}
		wrongProject := makeToken("wrong-project", []string{"projects:write", "generate"}, []string{db.NewID()}, nil)
		if _, err := enqueue(wrongProject); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("wrong project scope: %v", err)
		}
		low := 0.74
		lowApproval := makeToken("low-approval", []string{"projects:write", "generate"}, []string{fixture.project.ID}, &low)
		if _, err := enqueue(lowApproval); generateErrorCode(err) != ai.CodeApproval {
			t.Fatalf("low approval threshold: %v", err)
		}
		if got := jobCount(); got != before {
			t.Fatalf("failed checks created %d jobs", got-before)
		}

		equal := 0.75
		approved := makeToken("equal-approval", []string{"projects:write", "generate"}, []string{fixture.project.ID}, &equal)
		job, err := enqueue(approved)
		if err != nil {
			t.Fatalf("equal threshold should pass: %v", err)
		}
		if job.Estimate.USD != 0.75 || job.MaxAttempts != gateAScriptMaxAttempts {
			t.Fatalf("full action estimate = %+v with %d attempts", job.Estimate, job.MaxAttempts)
		}
		var payload storedScriptPayload
		if err := json.Unmarshal(job.Input, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.TokenID != approved.ID || strings.Contains(string(job.Input), "mva_") {
			t.Fatalf("job stored wrong token identity: %s", job.Input)
		}

		revoked := makeToken("revoked-after-plan", []string{"projects:write", "generate"}, []string{fixture.project.ID}, nil)
		fake.planFn = func(context.Context, ai.TextPlanRequest) (ai.TextPlan, error) {
			if err := ta.Deps.Auth.RevokeToken(ctx, fixture.owner.ID, revoked.ID, false); err != nil {
				t.Fatalf("RevokeToken: %v", err)
			}
			return remoteTextPlan(ai.ModeBalanced, providerID, 0.25), nil
		}
		before = jobCount()
		if _, err := enqueue(revoked); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("revoked token final recheck: %v", err)
		}
		if got := jobCount(); got != before {
			t.Fatalf("revoked token created %d jobs", got-before)
		}
	})
}

func TestEnqueueRejectsUntrustedPlanMatrix(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		fixture := newGenFixture(t, ta, "", 0)
		base := placeholderTextPlan(ai.ModeBalanced)
		cases := map[string]func(*ai.TextPlan){
			"task":           func(p *ai.TextPlan) { p.Task = "other" },
			"schema":         func(p *ai.TextPlan) { p.Schema = "other" },
			"effective mode": func(p *ai.TextPlan) { p.RoutingMode = "" },
			"provider pair":  func(p *ai.TextPlan) { p.ProviderID = db.NewID() },
			"model pair":     func(p *ai.TextPlan) { p.ModelID = "remote" },
			"remote flag":    func(p *ai.TextPlan) { p.Remote = true },
			"decision array": func(p *ai.TextPlan) { p.Decision = json.RawMessage(`[]`) },
			"decision dup":   func(p *ai.TextPlan) { p.Decision = json.RawMessage(`{"mode":1,"mode":2}`) },
			"negative cost":  func(p *ai.TextPlan) { p.Estimate.USD = -1 },
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				plan := base
				plan.Decision = append(json.RawMessage(nil), base.Decision...)
				mutate(&plan)
				fixture.useText(&fakeTextService{planFn: func(context.Context, ai.TextPlanRequest) (ai.TextPlan, error) {
					return plan, nil
				}})
				var before int
				_ = ta.Deps.DB.Get(context.Background(), &before, `SELECT COUNT(*) FROM jobs`)
				if _, err := fixture.gen.EnqueueScript(context.Background(), fixture.actor, gen.ScriptRequest{
					ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
				}); generateErrorCode(err) != ai.CodeUpstream {
					t.Fatalf("untrusted plan error = %v", err)
				}
				var after int
				_ = ta.Deps.DB.Get(context.Background(), &after, `SELECT COUNT(*) FROM jobs`)
				if after != before {
					t.Fatal("untrusted plan was enqueued")
				}
			})
		}
	})
}

func generateErrorCode(err error) string {
	var generateErr *ai.GenerateError
	if errors.As(err, &generateErr) {
		return generateErr.Code
	}
	return ""
}
