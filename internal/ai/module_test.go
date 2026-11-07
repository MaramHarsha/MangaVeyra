package ai

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
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
		if err != nil || len(cards) != 2 || cards[0].Kind != providerKindOpenRouter || cards[1].Kind != providerKindOpenAI ||
			cards[0].Status != setup.ProviderError || cards[1].Status != setup.ProviderError {
			t.Fatalf("provider fallback = %+v, %v", cards, err)
		}
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
