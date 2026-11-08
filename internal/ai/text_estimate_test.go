package ai

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Review fixes: a queued script never reserves more than the estimate it was
// approved with (a Hugging Face tariff can change after a re-test), and a
// Hugging Face text model whose name matches a built-in route still runs on
// its own connection.

func TestReviewQueuedTextNeverReservesMoreThanItsApprovedEstimate(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		in := testScriptInput()
		var chats atomic.Int64
		doer := (&routeDoer{}).on(http.MethodPost, hfRouterBase+"/chat/completions", func(*http.Request, []byte) (*http.Response, error) {
			chats.Add(1)
			return chatOK(string(testScriptDocument(t, in)), 3000, 1000), nil
		}).on(http.MethodPost, openRouterChatURL, func(*http.Request, []byte) (*http.Response, error) {
			chats.Add(1)
			return chatOK(string(testScriptDocument(t, in)), 3000, 1000), nil
		})
		m, f := imageTestModule(t, d, doer)
		m.text.hfDoer = doer
		cfg := testHFTextConfig()
		view := f.createConfiguredProvider(t, m.providers, providerKindHuggingFace, hfRouterBase, "hf_token", cfg)
		base := TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: f.userID, ProjectID: f.projectID, Input: testScriptInputJSON(t)}
		approved, err := m.text.PlanText(ctx, base)
		if err != nil || approved.ProviderID != view.ID || !approved.Remote {
			t.Fatalf("plan = %+v %v", approved, err)
		}
		reservations := func(jobID string) int {
			var n int
			if err := d.Get(ctx, &n, `SELECT COUNT(*) FROM spend_reservations WHERE job_id = ?`, jobID); err != nil {
				t.Fatal(err)
			}
			return n
		}

		// A re-test froze a ten times higher listed tariff after the job was
		// queued: the queued plan needs approval again and nothing is sent.
		raised := cfg
		raised.InputNano, raised.OutputNano = 9000, 18000
		p, err := m.providers.ResolveDispatch(ctx, f.userID, view.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.providers.updateCustomConfig(ctx, view.ID, providerKindHuggingFace, hfRouterBase, p.KeyEnc, raised); err != nil {
			t.Fatal(err)
		}
		job := f.seedRunningModelJob(t, view.ID, approved.ModelID)
		_, err = m.text.GenerateText(ctx, TextGenerateRequest{Plan: approved, ActorID: f.userID, ProjectID: f.projectID, JobID: job, Attempt: 1, Input: testScriptInputJSON(t)})
		if !isCode(err, CodeApproval) || chats.Load() != 0 || reservations(job) != 0 {
			t.Fatalf("raised tariff = %v, %d calls, %d reservations", err, chats.Load(), reservations(job))
		}

		// A plan made at the new tariff (approved again) runs.
		current, err := m.text.PlanText(ctx, base)
		if err != nil || current.Estimate.USD <= approved.Estimate.USD {
			t.Fatalf("new plan = %+v %v", current, err)
		}
		job = f.seedRunningModelJob(t, view.ID, current.ModelID)
		if _, err := m.text.GenerateText(ctx, TextGenerateRequest{Plan: current, ActorID: f.userID, ProjectID: f.projectID, JobID: job, Attempt: 1, Input: testScriptInputJSON(t)}); err != nil || chats.Load() != 1 {
			t.Fatalf("approved new plan = %v calls %d", err, chats.Load())
		}
		// A lower tariff than approved runs on the plan approved earlier.
		if err := m.providers.updateCustomConfig(ctx, view.ID, providerKindHuggingFace, hfRouterBase, p.KeyEnc, cfg); err != nil {
			t.Fatal(err)
		}
		job = f.seedRunningModelJob(t, view.ID, current.ModelID)
		if _, err := m.text.GenerateText(ctx, TextGenerateRequest{Plan: current, ActorID: f.userID, ProjectID: f.projectID, JobID: job, Attempt: 1, Input: testScriptInputJSON(t)}); err != nil || chats.Load() != 2 {
			t.Fatalf("cheaper than approved = %v calls %d", err, chats.Load())
		}

		// The same rule holds for a frozen route: a plan approved below the
		// route's bound is never dispatched.
		if _, err := d.Exec(ctx, `DELETE FROM providers WHERE id = ?`, view.ID); err != nil {
			t.Fatal(err)
		}
		or := f.createProvider(t, m.providers, providerScopeServer, "or-key")
		low := TextPlan{Task: TaskScript, Schema: SchemaScriptV1, RoutingMode: ModeBalanced, ProviderID: or.ID, ModelID: openRouterModel,
			Remote: true, Estimate: jobs.Cost{USD: openRouterPlanUSD / 2}}
		job = f.seedRunningModelJob(t, or.ID, openRouterModel)
		if _, err := m.text.GenerateText(ctx, TextGenerateRequest{Plan: low, ActorID: f.userID, ProjectID: f.projectID, JobID: job, Attempt: 1, Input: testScriptInputJSON(t)}); !isCode(err, CodeApproval) || chats.Load() != 2 || reservations(job) != 0 {
			t.Fatalf("frozen route above its plan = %v calls %d", err, chats.Load())
		}
	})
}

func TestReviewHuggingFaceModelNamedLikeABuiltInRouteRuns(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		in := testScriptInput()
		endpoint := "https://j3z5luu0ooo76jnl.us-east-1.aws.endpoints.huggingface.cloud"
		doer := (&routeDoer{}).on(http.MethodPost, endpoint+"/v1/chat/completions", func(*http.Request, []byte) (*http.Response, error) {
			return chatOK(string(testScriptDocument(t, in)), 3000, 1000), nil
		})
		m, f := imageTestModule(t, d, doer)
		m.text.hfDoer = doer
		// The endpoint serves a model whose name is also the Together route's.
		view := f.createConfiguredProvider(t, m.providers, providerKindHuggingFace, endpoint, "hf_token",
			hfConfig{Endpoint: hfDedicated, Task: hfTaskText, Model: togetherTextModel, ContextTokens: 32768, OutputTokens: 4096})
		plan, err := m.text.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: f.userID, ProjectID: f.projectID, Input: testScriptInputJSON(t)})
		if err != nil || plan.ProviderID != view.ID || plan.ModelID != togetherTextModel {
			t.Fatalf("plan = %+v %v", plan, err)
		}
		job := f.seedRunningModelJob(t, view.ID, plan.ModelID)
		if _, err := m.text.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: f.userID, ProjectID: f.projectID, JobID: job, Attempt: 1, Input: testScriptInputJSON(t)}); err != nil {
			t.Fatalf("dedicated model named like a built-in route: %v", err)
		}
		if reqs := doer.requests(); len(reqs) != 1 || reqs[0].URL != endpoint+"/v1/chat/completions" {
			t.Fatalf("requests = %+v", reqs)
		}
	})
}
