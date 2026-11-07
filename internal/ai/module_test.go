package ai

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/setup"
)

func TestModuleRegistersTextAndRepairsReservationsAtInitAndStart(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		fixture := newAIDBFixture(t, d)
		base := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
		var nanos atomic.Int64
		nanos.Store(base.UnixNano())
		now := func() time.Time { return time.Unix(0, nanos.Load()).UTC() }
		deps := &core.Deps{DB: d, Settings: fixture.settings, Secrets: fixture.box}
		module := newModule(deps, &queuedDoer{}, now)
		if _, ok := core.Use[TextService](deps); !ok {
			t.Fatal("module did not register TextService")
		}
		if step, ok := core.Use[setup.ModelStep](deps); !ok || step != module {
			t.Fatal("module did not register setup.ModelStep")
		}
		provider := fixture.createProvider(t, module.providers, providerScopeServer, "module-key")

		reserve := func(expiry time.Time) (string, string) {
			jobID := fixture.seedRunningJob(t, provider.ID, 1)
			reservationID := db.NewID()
			got, err := module.spend.ReserveRemoteCall(context.Background(), ReserveRemoteCallParams{
				ReservationID: reservationID, JobID: jobID, Attempt: 1, Purpose: SpendPurposeInitial,
				ProviderID: provider.ID, ModelID: openRouterModel, EstimateUSD: openRouterReserveUSD, ExpiresAt: expiry,
			})
			if err != nil || !got.Dispatch {
				t.Fatalf("reserve = %+v, %v", got, err)
			}
			return reservationID, jobID
		}

		firstID, _ := reserve(base.Add(time.Minute))
		nanos.Store(base.Add(2 * time.Minute).UnixNano())
		if err := module.Init(context.Background()); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := d.Get(context.Background(), &state, `SELECT state FROM spend_reservations WHERE id = ?`, firstID); err != nil {
			t.Fatal(err)
		}
		if state != SpendStateRepaired {
			t.Fatalf("Init state = %q", state)
		}

		nanos.Store(base.Add(3 * time.Minute).UnixNano())
		secondID, _ := reserve(base.Add(4 * time.Minute))
		nanos.Store(base.Add(5 * time.Minute).UnixNano())
		module.repairEvery = 5 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- module.Start(ctx) }()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if err := d.Get(context.Background(), &state, `SELECT state FROM spend_reservations WHERE id = ?`, secondID); err != nil {
				t.Fatal(err)
			}
			if state == SpendStateRepaired {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("periodic repair state = %q", state)
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Start cancellation error = %v", err)
		}
	})
}

func TestSetupModelStepFallsBackSafelyAndRecommendsWithoutNetwork(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		fixture := newAIDBFixture(t, d)
		doer := &queuedDoer{}
		module := newModule(&core.Deps{DB: d, Settings: fixture.settings, Secrets: fixture.box}, doer, nil)

		cards, err := module.Providers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// Without a browser session only the built connection kinds report an
		// error; designed-but-unbuilt rows stay "not connected" so they show Soon.
		assertSetupProviderCards(t, cards, map[string]string{
			providerKindOpenRouter: setup.ProviderError, "vendor": setup.ProviderError,
		})
		if doer.count() != 0 {
			t.Fatal("setup provider status made a network request")
		}

		recommendation, err := module.Recommend(context.Background(), []jobs.GPU{{VRAMTotalMB: 20 * 1024}})
		if err != nil || recommendation == nil || len(recommendation.Models) != 3 {
			t.Fatalf("recommendation = %+v, %v", recommendation, err)
		}
		if doer.count() != 0 {
			t.Fatal("setup recommendation made a network request")
		}
	})
}

// assertSetupProviderCards checks the five provider rows of design G8 step 3:
// design order, i18n keys, icons, connect targets and the expected statuses
// (kinds missing from want must be "not connected").
func assertSetupProviderCards(t *testing.T, cards []setup.ProviderCard, want map[string]string) {
	t.Helper()
	expected := []setup.ProviderCard{
		{Kind: providerKindOpenRouter, NameKey: "setup.provider.openrouter.name", DescKey: "setup.provider.openrouter.desc",
			Icon: "route", Href: "/models/providers/new?kind=openrouter&next=/setup/models"},
		{Kind: "fal", NameKey: "setup.provider.fal.name", DescKey: "setup.provider.fal.desc", Icon: "bolt",
			Href: "/models/providers/new/fal"},
		{Kind: "vendor", NameKey: "setup.provider.vendor.name", DescKey: "setup.provider.vendor.desc", Icon: "brand-openai",
			Href: "/models/providers/new?kind=openai&next=/setup/models"},
		{Kind: "ollama", NameKey: "setup.provider.ollama.name", DescKey: "setup.provider.ollama.desc", Icon: "server-2",
			Href: "/models/providers/new/ollama"},
		{Kind: "custom", NameKey: "setup.provider.custom.name", DescKey: "setup.provider.custom.desc", Icon: "adjustments",
			Href: "/models/providers/custom"},
	}
	if len(cards) != len(expected) {
		t.Fatalf("setup provider cards = %+v, want %d rows", cards, len(expected))
	}
	for i, card := range cards {
		exp := expected[i]
		exp.Status = want[exp.Kind]
		if card != exp {
			t.Errorf("setup provider card %d = %+v, want %+v", i, card, exp)
		}
	}
}

func TestSetupModelStepReportsConnectionStatusPerDesignedRow(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		fixture := newAIDBFixture(t, d)
		doer := &queuedDoer{}
		module := newModule(&core.Deps{DB: d, Settings: fixture.settings, Secrets: fixture.box}, doer, nil)
		ctx := httpx.WithIdentity(context.Background(), httpx.Identity{User: &httpx.User{ID: fixture.userID, Role: httpx.RoleAdmin}})
		admin := providerActor{UserID: fixture.userID, Admin: true}

		cards, err := module.Providers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		assertSetupProviderCards(t, cards, nil)

		openRouter := fixture.createProvider(t, module.providers, providerScopeServer, "setup-openrouter-key")
		openAI, err := module.providers.Create(ctx, admin, providerCreate{Kind: providerKindOpenAI, Name: "Vendor key",
			Key: "setup-openai-key", Scope: providerScopeServer})
		if err != nil {
			t.Fatal(err)
		}
		cards, err = module.Providers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Unchecked enabled connections count as connected.
		assertSetupProviderCards(t, cards, map[string]string{
			providerKindOpenRouter: setup.ProviderConnected, "vendor": setup.ProviderConnected,
		})

		// The vendor row follows OpenAI connections only: a failed OpenAI check
		// marks it as an error while OpenRouter stays connected.
		if _, err := d.Exec(ctx, `UPDATE providers SET last_test = ? WHERE id = ?`, `{"state":"error"}`, openAI.ID); err != nil {
			t.Fatal(err)
		}
		cards, err = module.Providers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		assertSetupProviderCards(t, cards, map[string]string{
			providerKindOpenRouter: setup.ProviderConnected, "vendor": setup.ProviderError,
		})

		// Disabled connections do not count.
		if _, err := module.providers.SetEnabled(ctx, admin, openRouter.ID, false); err != nil {
			t.Fatal(err)
		}
		cards, err = module.Providers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		assertSetupProviderCards(t, cards, map[string]string{"vendor": setup.ProviderError})
		if doer.count() != 0 {
			t.Fatal("setup provider status made a network request")
		}
	})
}
