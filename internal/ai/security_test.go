package ai

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

func TestRemoteTextFailuresAreSanitizedAndReconciled(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		provider := fixture.createProvider(t, providers, providerScopeServer, "never-leak-key")
		plan := TextPlan{Task: TaskScript, Schema: SchemaScriptV1, RoutingMode: ModeBalanced,
			ProviderID: provider.ID, ModelID: openRouterModel, Remote: true, Estimate: jobs.Cost{USD: openRouterPlanUSD}}
		input := testScriptInputJSON(t)

		cases := []struct {
			name    string
			respond func(*http.Request) (*http.Response, error)
			outcome string
		}{
			{"upstream", func(*http.Request) (*http.Response, error) {
				return httpJSON(http.StatusInternalServerError, map[string]any{"error": "RAW-UPSTREAM-SENTINEL"}, nil), nil
			}, SpendOutcomeFailure},
			{"canceled", func(*http.Request) (*http.Response, error) { return nil, context.Canceled }, SpendOutcomeCanceled},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				jobID := fixture.seedRunningJob(t, provider.ID, 1)
				doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){tc.respond}}
				service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer),
					NewSpendStore(d, fixture.settings), nil)
				_, err := service.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
					ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: input})
				generated := requireGenerateCode(t, err, CodeUpstream)
				errText := generated.Error()
				if generated.Unwrap() != nil {
					errText += " " + generated.Unwrap().Error()
				}
				for _, forbidden := range []string{"RAW-UPSTREAM-SENTINEL", "never-leak-key", openRouterChatURL} {
					if strings.Contains(errText, forbidden) {
						t.Fatalf("error leaked %q: %s", forbidden, errText)
					}
				}
				if doer.count() != 1 {
					t.Fatalf("hidden retry made %d calls", doer.count())
				}
				var outcome string
				if err := d.Get(ctx, &outcome, `SELECT outcome FROM spend_reservations WHERE job_id = ?`, jobID); err != nil {
					t.Fatal(err)
				}
				if outcome != tc.outcome {
					t.Fatalf("outcome = %q, want %q", outcome, tc.outcome)
				}
			})
		}
	})
}

func TestRemoteTextDispatchGuardsMakeZeroRequests(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		doer := &queuedDoer{}
		service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer),
			NewSpendStore(d, fixture.settings), nil)
		input := testScriptInputJSON(t)
		generate := func(providerID, jobID, mode string) error {
			_, err := service.GenerateText(ctx, TextGenerateRequest{Plan: TextPlan{Task: TaskScript, Schema: SchemaScriptV1,
				RoutingMode: mode, ProviderID: providerID, ModelID: openRouterModel, Remote: true, Estimate: jobs.Cost{USD: openRouterPlanUSD}}, ActorID: fixture.userID,
				ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: input})
			return err
		}

		server := fixture.createProvider(t, providers, providerScopeServer, "guard-key")
		job := fixture.seedRunningJob(t, server.ID, 1)
		requireGenerateCode(t, generate(server.ID, job, ModeLocalOnly), CodeLocalOnly)
		_, err := service.GenerateText(ctx, TextGenerateRequest{Plan: TextPlan{Task: TaskScript, Schema: SchemaScriptV1,
			RoutingMode: ModeBalanced, ProviderID: server.ID, ModelID: openRouterModel, Remote: false}, ActorID: fixture.userID,
			ProjectID: fixture.projectID, JobID: job, Attempt: 1, Input: input})
		requireGenerateCode(t, err, CodeBadInput)
		_, err = service.GenerateText(ctx, TextGenerateRequest{Plan: TextPlan{Task: TaskScript, Schema: SchemaScriptV1,
			RoutingMode: ModeBalanced, ProviderID: PlaceholderID, ModelID: PlaceholderText, Remote: true}, ActorID: fixture.userID,
			ProjectID: fixture.projectID, Input: input})
		requireGenerateCode(t, err, CodeBadInput)

		otherServer := fixture.createProvider(t, providers, providerScopeServer, "other-route-key")
		requireGenerateCode(t, generate(otherServer.ID, job, ModeBalanced), CodeNoProvider)
		var routeMismatchReservations int
		if err := d.Get(ctx, &routeMismatchReservations, `SELECT COUNT(*) FROM spend_reservations WHERE job_id = ?`, job); err != nil {
			t.Fatal(err)
		}
		if routeMismatchReservations != 0 {
			t.Fatalf("provider route mismatch stored %d reservations", routeMismatchReservations)
		}
		modelMismatchJob := fixture.seedRunningJob(t, server.ID, 1)
		if _, err := d.Exec(ctx, `UPDATE jobs SET model_id = 'openrouter/different-model' WHERE id = ?`, modelMismatchJob); err != nil {
			t.Fatal(err)
		}
		requireGenerateCode(t, generate(server.ID, modelMismatchJob, ModeBalanced), CodeNoProvider)
		if err := d.Get(ctx, &routeMismatchReservations, `SELECT COUNT(*) FROM spend_reservations WHERE job_id = ?`, modelMismatchJob); err != nil {
			t.Fatal(err)
		}
		if routeMismatchReservations != 0 {
			t.Fatalf("model route mismatch stored %d reservations", routeMismatchReservations)
		}

		if _, err := d.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES ('local_only', '{')
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`); err != nil {
			t.Fatal(err)
		}
		fixture.settings.Invalidate()
		requireGenerateCode(t, generate(server.ID, job, ModeBalanced), CodeLocalOnly)
		if _, err := d.Exec(ctx, `DELETE FROM instance_settings WHERE key = 'local_only'`); err != nil {
			t.Fatal(err)
		}
		fixture.settings.Invalidate()

		disabled := fixture.createProvider(t, providers, providerScopeServer, "disabled-key")
		disabledJob := fixture.seedRunningJob(t, disabled.ID, 1)
		if _, err := providers.SetEnabled(ctx, providerActor{UserID: fixture.userID, Admin: true}, disabled.ID, false); err != nil {
			t.Fatal(err)
		}
		requireGenerateCode(t, generate(disabled.ID, disabledJob, ModeBalanced), CodeNoProvider)

		deleted := fixture.createProvider(t, providers, providerScopeServer, "deleted-key")
		deletedJob := fixture.seedRunningJob(t, deleted.ID, 1)
		if err := providers.Delete(ctx, providerActor{UserID: fixture.userID, Admin: true}, deleted.ID); err != nil {
			t.Fatal(err)
		}
		requireGenerateCode(t, generate(deleted.ID, deletedJob, ModeBalanced), CodeNoProvider)

		corrupt := fixture.createProvider(t, providers, providerScopeServer, "corrupt-key")
		corruptJob := fixture.seedRunningJob(t, corrupt.ID, 1)
		if _, err := d.Exec(ctx, `UPDATE providers SET key_enc = 'corrupt' WHERE id = ?`, corrupt.ID); err != nil {
			t.Fatal(err)
		}
		requireGenerateCode(t, generate(corrupt.ID, corruptJob, ModeBalanced), CodeNoProvider)

		other, err := providers.Create(ctx, providerActor{UserID: fixture.otherID, Admin: true}, providerCreate{
			Name: "Other owner", Key: "other-key", Scope: providerScopeUser,
		})
		if err != nil {
			t.Fatal(err)
		}
		otherJob := fixture.seedRunningJob(t, other.ID, 1)
		requireGenerateCode(t, generate(other.ID, otherJob, ModeBalanced), CodeNoProvider)
		if doer.count() != 0 {
			t.Fatalf("dispatch guards made %d HTTP requests", doer.count())
		}
	})
}

func TestRetryHistoryReadFailureFailsClosedBeforeTransport(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		provider := fixture.createProvider(t, providers, providerScopeServer, "history-key")
		jobID := fixture.seedRunningJob(t, provider.ID, 1)
		doer := &queuedDoer{}
		spend := &scriptedSpendStore{userID: fixture.userID, projectID: fixture.projectID}
		service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer), spend, nil)
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		_, err := service.GenerateText(context.Background(), TextGenerateRequest{Plan: TextPlan{Task: TaskScript,
			Schema: SchemaScriptV1, RoutingMode: ModeBalanced, ProviderID: provider.ID, ModelID: openRouterModel, Remote: true},
			ActorID: fixture.userID, ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: testScriptInputJSON(t)})
		requireGenerateCode(t, err, CodeUpstream)
		if doer.count() != 0 || len(spend.reserveCalls) != 0 {
			t.Fatalf("history read failure calls: reserve=%d http=%d", len(spend.reserveCalls), doer.count())
		}
	})
}
