package ai

import (
	"context"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// G12 step 4 decides in which routing modes auto-routing may pick a custom
// local connection; an unset list keeps the original behaviour (not Best).
func TestLocalTextRoutingModesChosenInTheWizard(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		admin := providerActor{UserID: f.userID, Admin: true}
		local, err := m.providers.Create(ctx, admin, providerCreate{Kind: providerKindLocalText, Name: "Legacy local", BaseURL: "http://localhost:11434/v1", Local: localTextConfig{Model: "qwen3:8b", ContextTokens: 32768, OutputTokens: 4096}, Scope: providerScopeServer})
		if err != nil {
			t.Fatal(err)
		}
		plan := func(mode string) TextPlan {
			t.Helper()
			p, err := m.text.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: f.userID, ProjectID: f.projectID, RoutingMode: mode, Input: testScriptInputJSON(t)})
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		if p := plan(ModeBalanced); p.ProviderID != local.ID {
			t.Fatalf("legacy balanced plan = %+v", p)
		}
		if p := plan(ModeBest); p.ProviderID == local.ID {
			t.Fatal("legacy connection joined Best mode")
		}
		if _, err := d.Exec(ctx, `UPDATE providers SET config = ? WHERE id = ?`, `{"model":"qwen3:8b","context_tokens":32768,"output_tokens":4096,"modes":["best"]}`, local.ID); err != nil {
			t.Fatal(err)
		}
		if p := plan(ModeBest); p.ProviderID != local.ID {
			t.Fatalf("opted-in best plan = %+v", p)
		}
		if p := plan(ModeBalanced); p.ProviderID == local.ID {
			t.Fatal("connection used outside its chosen modes")
		}
		for _, bad := range [][]string{{"local_only"}, {"best", "best"}, {"turbo"}} {
			if validLocalModes(bad) {
				t.Errorf("accepted modes %v", bad)
			}
		}
	})
}
