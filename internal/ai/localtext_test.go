package ai

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

func TestLocalTextConnectionOptionalKeyAndValidatedScript(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		p, err := m.providers.Create(ctx, providerActor{UserID: f.userID, Admin: true}, providerCreate{Kind: providerKindLocalText, Name: "Local test", BaseURL: "http://localhost:11434/v1", Local: localTextConfig{Model: "qwen3:8b", ContextTokens: 32768, OutputTokens: 4096}, Scope: providerScopeServer})
		if err != nil {
			t.Fatal(err)
		}
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){func(*http.Request) (*http.Response, error) {
			return completionResponse(testScriptDocument(t, testScriptInput()), nil), nil
		}}}
		m.text.localDoer = fake
		plan, err := m.text.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: f.userID, ProjectID: f.projectID, PinProviderID: p.ID, Input: testScriptInputJSON(t)})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Remote || plan.ModelID != "qwen3:8b" || plan.Estimate.USD != 0 {
			t.Fatalf("local plan=%+v", plan)
		}
		id := f.seedRunningJob(t, p.ID, 1)
		if _, err := d.Exec(ctx, `UPDATE jobs SET model_id = ? WHERE id = ?`, plan.ModelID, id); err != nil {
			t.Fatal(err)
		}
		result, err := m.text.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: f.userID, ProjectID: f.projectID, JobID: id, Attempt: 1, Input: testScriptInputJSON(t)})
		if err != nil {
			t.Fatal(err)
		}
		if result.ModelID != plan.ModelID || len(result.Document) == 0 {
			t.Fatal("missing local script")
		}
		request := fake.requests()[0]
		if request.Authorization != "" || request.URL != "http://localhost:11434/v1/chat/completions" || !strings.Contains(string(request.Body), `"max_tokens":4096`) {
			t.Fatalf("local request=%+v", request)
		}
		var count int
		if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM spend_reservations WHERE job_id = ?`, id); err != nil || count != 0 {
			t.Fatalf("local call reserved remote spending: %d %v", count, err)
		}
		// A second process commits local-only while this service retains its old
		// settings cache. The local adapter has no reservation transaction to
		// perform this refresh on its behalf.
		if _, err := f.settings.Instance(ctx); err != nil {
			t.Fatal(err)
		}
		updateInstance(t, settings.New(d), func(i *settings.Instance) { i.LocalOnly = true })
		_, err = m.text.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: f.userID, ProjectID: f.projectID, JobID: id, Attempt: 1, Input: testScriptInputJSON(t)})
		requireGenerateCode(t, err, CodeLocalOnly)
		if fake.count() != 1 {
			t.Fatal("local-only dispatched custom HTTP")
		}
	})
}

func TestLocalTextCannotForgeRemoteRouteOrLeakURLs(t *testing.T) {
	for _, base := range []string{"http://user:secret@localhost/v1", "http://localhost/v1?key=secret", "http://localhost/v1#secret", "file:///v1"} {
		if validateLocalTextConfig(base, localTextConfig{"qwen3:8b", 32768, 4096}) == nil {
			t.Fatalf("accepted %s", base)
		}
	}
}
