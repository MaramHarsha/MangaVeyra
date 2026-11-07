package ai

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

func requireGenerateCode(t *testing.T, err error, code string) *GenerateError {
	t.Helper()
	var generated *GenerateError
	if !errors.As(err, &generated) || generated.Code != code {
		t.Fatalf("error = %T %v, want GenerateError code %q", err, err, code)
	}
	return generated
}

func TestTextRoutingModesPinsAndApprovalCeiling(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		server := fixture.createProvider(t, providers, providerScopeServer, "server-key")
		user := fixture.createProvider(t, providers, providerScopeUser, "user-key")
		service := newTextService(d, fixture.settings, providers, newOpenRouterClient(&queuedDoer{}),
			NewSpendStore(d, fixture.settings), nil)
		base := TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: fixture.userID,
			ProjectID: fixture.projectID, Input: testScriptInputJSON(t)}

		plan, err := service.PlanText(ctx, base)
		if err != nil || !plan.Remote || plan.RoutingMode != ModeBalanced || plan.ProviderID != user.ID ||
			plan.ModelID != openRouterModel || math.Abs(plan.Estimate.USD-openRouterPlanUSD) > 1e-12 {
			t.Fatalf("default plan = %+v, %v", plan, err)
		}
		for _, mode := range []string{ModeBalanced, ModeBest, ModeFastest} {
			req := base
			req.RoutingMode = mode
			got, err := service.PlanText(ctx, req)
			if err != nil || !got.Remote || got.RoutingMode != mode || got.ProviderID != user.ID {
				t.Errorf("mode %q plan = %+v, %v", mode, got, err)
			}
		}
		pinned := base
		pinned.PinProviderID, pinned.PinModelID = server.ID, openRouterModel
		if got, err := service.PlanText(ctx, pinned); err != nil || got.ProviderID != server.ID {
			t.Fatalf("remote pin plan = %+v, %v", got, err)
		}
		placeholder := base
		placeholder.PinProviderID, placeholder.PinModelID = PlaceholderID, PlaceholderText
		if got, err := service.PlanText(ctx, placeholder); err != nil || got.Remote || got.ProviderID != PlaceholderID {
			t.Fatalf("placeholder pin plan = %+v, %v", got, err)
		}
		for _, mode := range []string{ModeLocalOnly, ModeFreeOnly} {
			req := base
			req.RoutingMode = mode
			got, err := service.PlanText(ctx, req)
			if err != nil || got.Remote || got.RoutingMode != mode {
				t.Fatalf("forced local mode %q = %+v, %v", mode, got, err)
			}
			req.PinProviderID, req.PinModelID = server.ID, openRouterModel
			_, err = service.PlanText(ctx, req)
			if mode == ModeLocalOnly {
				requireGenerateCode(t, err, CodeLocalOnly)
			} else {
				requireGenerateCode(t, err, CodeNoProvider)
			}
		}
		invalid := base
		invalid.RoutingMode = "cheapest"
		_, err = service.PlanText(ctx, invalid)
		requireGenerateCode(t, err, CodeBadInput)

		updateInstance(t, fixture.settings, func(inst *settings.Instance) { inst.LocalOnly = true })
		local, err := service.PlanText(ctx, base)
		if err != nil || local.Remote || local.RoutingMode != ModeBalanced {
			t.Fatalf("global local-only default plan = %+v, %v", local, err)
		}
		updateInstance(t, fixture.settings, func(inst *settings.Instance) { inst.LocalOnly = false })

		if _, err := d.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES ('routing_mode', '{')
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`); err != nil {
			t.Fatal(err)
		}
		fixture.settings.Invalidate()
		failedClosed, err := service.PlanText(ctx, base)
		if err != nil || failedClosed.Remote || failedClosed.RoutingMode != ModeLocalOnly {
			t.Fatalf("settings failure plan = %+v, %v", failedClosed, err)
		}
		if _, err := d.Exec(ctx, `DELETE FROM instance_settings WHERE key = 'routing_mode'`); err != nil {
			t.Fatal(err)
		}
		fixture.settings.Invalidate()

		oversized := testScriptInput()
		oversized.Mode, oversized.Premise = "adapt", ""
		oversized.SourceText = strings.Repeat("x", maxScriptSourceBytes)
		oversizedRaw, _ := json.Marshal(oversized)
		autoLarge := base
		autoLarge.Input = oversizedRaw
		if got, err := service.PlanText(ctx, autoLarge); err != nil || got.Remote {
			t.Fatalf("automatic oversized plan = %+v, %v", got, err)
		}
		autoLarge.PinProviderID, autoLarge.PinModelID = server.ID, openRouterModel
		_, err = service.PlanText(ctx, autoLarge)
		requireGenerateCode(t, err, CodeBadInput)
	})
}
