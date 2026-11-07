package gen_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestScriptJobPlaceholderPersistsAtomicallyWithSafeOutput(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		secretSource := "PRIVATE-SOURCE-DO-NOT-LOG-" + db.NewID()
		fixture := newGenFixture(t, ta, secretSource, 1)
		fake := &fakeTextService{generateFn: func(_ context.Context, request ai.TextGenerateRequest) (ai.TextResult, error) {
			if request.JobID == "" || request.Attempt != 1 || request.ActorID != fixture.owner.ID ||
				request.ProjectID != fixture.project.ID || request.Plan.RoutingMode != ai.ModeBalanced {
				t.Fatalf("GenerateText identity/fence = %+v", request)
			}
			var input ai.ScriptInput
			if err := json.Unmarshal(request.Input, &input); err != nil {
				t.Fatal(err)
			}
			if input.SourceText != secretSource {
				t.Fatalf("source bytes changed before generation: %q", input.SourceText)
			}
			return ai.TextResult{Document: generatedScriptJSON(fixture.characters[0].ID, "Rin reaches the gate."),
				ProviderID: request.Plan.ProviderID, ModelID: request.Plan.ModelID}, nil
		}}
		fixture.useText(fake)
		job, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "adapt",
		})
		if err != nil {
			t.Fatalf("EnqueueScript: %v", err)
		}
		ran, err := ta.Deps.Executor.RunOnce(ctx)
		if err != nil || !ran {
			t.Fatalf("RunOnce = %v, %v", ran, err)
		}
		stored, err := ta.Deps.Jobs.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.State != jobs.StateDone || stored.Attempts != 1 || stored.Error != nil {
			t.Fatalf("stored job = %+v", stored)
		}
		var output storedScriptOutput
		if err := stored.DecodeOutput(&output); err != nil {
			t.Fatal(err)
		}
		if output.ChapterID != fixture.chapter.ID || output.Scenes != 1 || output.Panels != 1 || output.Pages != 1 ||
			output.Provider != ai.PlaceholderID || output.Model != ai.PlaceholderText {
			t.Fatalf("safe output = %+v", output)
		}
		if strings.Contains(string(stored.Output), secretSource) || strings.Contains(string(stored.Output), "Rin reaches the gate") {
			t.Fatalf("job output leaked source/document: %s", stored.Output)
		}
		doc, err := fixture.studio.GetScript(ctx, fixture.chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := doc.Scenes[0].Panels[0].Action; got != "Rin reaches the gate." {
			t.Fatalf("persisted action = %q", got)
		}
		events, err := ta.Deps.Jobs.Events(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			serialized := event.Message + string(event.Data)
			if strings.Contains(serialized, secretSource) || strings.Contains(serialized, "Rin reaches the gate") {
				t.Fatalf("event leaked private input/output: %s", serialized)
			}
		}
	})
}

func TestScriptJobRechecksMembershipTokenLockAndCharacterOwnership(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		cases := []struct {
			name      string
			setup     func(*testing.T, *genFixture) studio.Actor
			mutate    func(*testing.T, *genFixture)
			errorCode string
			remote    bool
		}{
			{
				name:  "membership removed",
				setup: func(_ *testing.T, fixture *genFixture) studio.Actor { return fixture.actor },
				mutate: func(t *testing.T, fixture *genFixture) {
					if _, err := ta.Deps.DB.Exec(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`,
						fixture.project.ID, fixture.owner.ID); err != nil {
						t.Fatal(err)
					}
				},
				errorCode: jobs.ErrCodeBadInput,
			},
			{
				name: "token revoked",
				setup: func(t *testing.T, fixture *genFixture) studio.Actor {
					_, token, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: fixture.owner.ID,
						Kind: auth.TokenAgent, Name: "queued-script", Scopes: []string{"projects:write", "generate"},
						ProjectIDs: []string{fixture.project.ID}})
					if err != nil {
						t.Fatal(err)
					}
					return studio.Actor{UserID: fixture.owner.ID, TokenID: token.ID}
				},
				mutate: func(t *testing.T, fixture *genFixture) {
					var payload storedScriptPayload
					jobsForProject, err := ta.Deps.Jobs.List(ctx, jobs.Filter{ProjectID: fixture.project.ID, Limit: 1})
					if err != nil || len(jobsForProject) != 1 || json.Unmarshal(jobsForProject[0].Input, &payload) != nil {
						t.Fatalf("read token payload: %v", err)
					}
					if err := ta.Deps.Auth.RevokeToken(ctx, fixture.owner.ID, payload.TokenID, false); err != nil {
						t.Fatal(err)
					}
				},
				errorCode: jobs.ErrCodeBadInput,
			},
			{
				name: "token approval lowered",
				setup: func(t *testing.T, fixture *genFixture) studio.Actor {
					_, token, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: fixture.owner.ID,
						Kind: auth.TokenAgent, Name: "approval-change", Scopes: []string{"projects:write", "generate"},
						ProjectIDs: []string{fixture.project.ID}})
					if err != nil {
						t.Fatal(err)
					}
					return studio.Actor{UserID: fixture.owner.ID, TokenID: token.ID}
				},
				mutate: func(t *testing.T, fixture *genFixture) {
					jobsForProject, err := ta.Deps.Jobs.List(ctx, jobs.Filter{ProjectID: fixture.project.ID, Limit: 1})
					if err != nil || len(jobsForProject) != 1 {
						t.Fatalf("read approval payload: %v", err)
					}
					var payload storedScriptPayload
					if err := json.Unmarshal(jobsForProject[0].Input, &payload); err != nil {
						t.Fatal(err)
					}
					if _, err := ta.Deps.DB.Exec(ctx, `UPDATE api_tokens SET approve_over_usd = 0 WHERE id = ?`, payload.TokenID); err != nil {
						t.Fatal(err)
					}
				},
				errorCode: ai.CodeApproval,
				remote:    true,
			},
			{
				name:  "chapter locked",
				setup: func(_ *testing.T, fixture *genFixture) studio.Actor { return fixture.actor },
				mutate: func(t *testing.T, fixture *genFixture) {
					if _, err := ta.Deps.DB.Exec(ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, fixture.chapter.ID); err != nil {
						t.Fatal(err)
					}
				},
				errorCode: jobs.ErrCodeBadInput,
			},
			{
				name:  "character deleted",
				setup: func(_ *testing.T, fixture *genFixture) studio.Actor { return fixture.actor },
				mutate: func(t *testing.T, fixture *genFixture) {
					if _, err := ta.Deps.DB.Exec(ctx, `DELETE FROM characters WHERE id = ?`, fixture.characters[0].ID); err != nil {
						t.Fatal(err)
					}
				},
				errorCode: jobs.ErrCodeBadInput,
			},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				characterCount := 0
				if testCase.name == "character deleted" {
					characterCount = 1
				}
				fixture := newGenFixture(t, ta, "", characterCount)
				fake := &fakeTextService{}
				if testCase.remote {
					providerID := db.NewID()
					fake.planFn = func(context.Context, ai.TextPlanRequest) (ai.TextPlan, error) {
						return remoteTextPlan(ai.ModeBalanced, providerID, 0.25), nil
					}
				}
				fixture.useText(fake)
				actor := testCase.setup(t, fixture)
				job, err := fixture.gen.EnqueueScript(ctx, actor, gen.ScriptRequest{
					ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
				})
				if err != nil {
					t.Fatalf("EnqueueScript: %v", err)
				}
				testCase.mutate(t, fixture)
				ran, err := ta.Deps.Executor.RunOnce(ctx)
				if err != nil || !ran {
					t.Fatalf("RunOnce = %v, %v", ran, err)
				}
				stored, err := ta.Deps.Jobs.Get(ctx, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.State != jobs.StateFailed || stored.Error == nil || stored.Error.Code != testCase.errorCode {
					t.Fatalf("post-enqueue recheck job = %+v", stored)
				}
				if len(fake.generateRequests()) != 0 {
					t.Fatal("AI was called before live rechecks failed")
				}
			})
		}
	})
}

func TestScriptJobSanitizesAIErrorsAndPreservesActual(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		fixture := newGenFixture(t, ta, "", 0)
		providerID := db.NewID()
		secret := "sk-upstream-secret-" + db.NewID()
		fake := &fakeTextService{
			planFn: func(context.Context, ai.TextPlanRequest) (ai.TextPlan, error) {
				return remoteTextPlan(ai.ModeBalanced, providerID, 0.25), nil
			},
			generateFn: func(context.Context, ai.TextGenerateRequest) (ai.TextResult, error) {
				return ai.TextResult{}, &ai.GenerateError{Code: ai.CodeProviderRefuse,
					SafeMessage: "The provider declined this request.", Actual: jobs.Cost{USD: 0.125}, Err: errors.New(secret)}
			},
		}
		fixture.useText(fake)
		job, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
			ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
		})
		if err != nil {
			t.Fatal(err)
		}
		if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
			t.Fatalf("RunOnce = %v, %v", ran, err)
		}
		stored, err := ta.Deps.Jobs.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.State != jobs.StateFailed || stored.Error == nil || stored.Error.Code != ai.CodeProviderRefuse ||
			stored.Error.Message != "The provider declined this request." || stored.Actual.USD != 0.125 {
			t.Fatalf("safe failure/actual = %+v", stored)
		}
		serialized, _ := json.Marshal(stored)
		events, _ := ta.Deps.Jobs.Events(ctx, job.ID)
		eventJSON, _ := json.Marshal(events)
		if strings.Contains(string(serialized), secret) || strings.Contains(string(eventJSON), secret) {
			t.Fatal("raw AI error leaked into job state or events")
		}
	})
}

type rollbackStudio struct {
	studio.Service
	err error
}

func (s rollbackStudio) ReplaceGeneratedScriptTx(ctx context.Context, tx *db.Tx, input studio.ReplaceScriptInput) (studio.ReplaceScriptResult, error) {
	result, err := s.Service.ReplaceGeneratedScriptTx(ctx, tx, input)
	if err != nil {
		return result, err
	}
	return result, s.err
}

func TestScriptJobRollbackAndCancellationPreserveExistingScript(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		t.Run("callback rollback", func(t *testing.T) {
			fixture := newGenFixture(t, ta, "", 0)
			seedScript(t, fixture, "Existing script stays.")
			fake := &fakeTextService{generateFn: func(_ context.Context, request ai.TextGenerateRequest) (ai.TextResult, error) {
				return ai.TextResult{Document: generatedScriptJSON("", "Replacement must roll back."),
					ProviderID: request.Plan.ProviderID, ModelID: request.Plan.ModelID}, nil
			}}
			fixture.useText(fake)
			gen.SetStudioServiceForTest(fixture.gen, rollbackStudio{Service: fixture.studio, err: errors.New("injected commit failure")})
			job, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
				ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
			})
			if err != nil {
				t.Fatal(err)
			}
			if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
				t.Fatalf("RunOnce = %v, %v", ran, err)
			}
			stored, _ := ta.Deps.Jobs.Get(ctx, job.ID)
			if stored.State != jobs.StateFailed || stored.Error == nil || stored.Error.Code != jobs.ErrCodeInternal {
				t.Fatalf("rollback job = %+v", stored)
			}
			assertScriptAction(t, fixture, "Existing script stays.")
		})

		t.Run("running cancellation", func(t *testing.T) {
			fixture := newGenFixture(t, ta, "", 0)
			seedScript(t, fixture, "Existing canceled script stays.")
			started := make(chan struct{})
			fake := &fakeTextService{generateFn: func(ctx context.Context, _ ai.TextGenerateRequest) (ai.TextResult, error) {
				close(started)
				<-ctx.Done()
				return ai.TextResult{}, &ai.GenerateError{Code: ai.CodeTimeout, SafeMessage: "The provider timed out.",
					Retryable: true, Actual: jobs.Cost{USD: 0.125}, Err: ctx.Err()}
			}}
			fixture.useText(fake)
			job, err := fixture.gen.EnqueueScript(ctx, fixture.actor, gen.ScriptRequest{
				ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID, Mode: "premise",
			})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				_, _ = ta.Deps.Executor.RunOnce(ctx)
				close(done)
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("generation did not start")
			}
			if _, err := ta.Deps.Jobs.Cancel(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("canceled generation did not stop")
			}
			stored, _ := ta.Deps.Jobs.Get(ctx, job.ID)
			if stored.State != jobs.StateCanceled || stored.Error == nil || stored.Error.Code != jobs.ErrCodeCanceled {
				t.Fatalf("canceled job = %+v", stored)
			}
			if stored.Actual.USD != 0.125 {
				t.Fatalf("canceled GenerateError actual = %+v", stored.Actual)
			}
			assertScriptAction(t, fixture, "Existing canceled script stays.")
		})
	})
}

func seedScript(t *testing.T, fixture *genFixture, action string) {
	t.Helper()
	err := fixture.ta.Deps.DB.WithTx(context.Background(), func(tx *db.Tx) error {
		_, err := fixture.studio.ReplaceGeneratedScriptTx(context.Background(), tx, studio.ReplaceScriptInput{
			Actor: fixture.actor, ProjectID: fixture.project.ID, ChapterID: fixture.chapter.ID,
			Doc: studioScript("", action), JobID: db.NewID(), ProviderID: ai.PlaceholderID,
			ModelID: ai.PlaceholderText, Provenance: "ai",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed script: %v", err)
	}
}

func assertScriptAction(t *testing.T, fixture *genFixture, want string) {
	t.Helper()
	doc, err := fixture.studio.GetScript(context.Background(), fixture.chapter.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Scenes) != 1 || len(doc.Scenes[0].Panels) != 1 || doc.Scenes[0].Panels[0].Action != want {
		t.Fatalf("script after failed job = %+v, want action %q", doc, want)
	}
}
