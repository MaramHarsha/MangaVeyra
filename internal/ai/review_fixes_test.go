package ai

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

func TestProviderKeysMustBeVisibleASCII(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		fixture := newAIDBFixture(t, d)
		store := fixture.providers(nil)
		actor := providerActor{UserID: fixture.userID, Admin: true}
		for _, key := range []string{"sk-with space", "sk-line\nbreak", "sk-tab\tkey", "sk-ünicode", "sk-\x00nul"} {
			if _, err := store.Create(context.Background(), actor, providerCreate{Name: "Bad key", Key: key, Scope: providerScopeServer}); !errors.Is(err, errProviderInvalid) {
				t.Errorf("key %q accepted: %v", key, err)
			}
		}
		view := fixture.createProvider(t, store, providerScopeServer, "sk-or-v1-goodkey")
		if _, err := store.ReplaceKey(context.Background(), actor, view.ID, "sk-new\rkey"); !errors.Is(err, errProviderInvalid) {
			t.Fatalf("replacement key with a control character accepted: %v", err)
		}
	})
}

func TestOpenRouterReportedCostIsClampedToTheReservation(t *testing.T) {
	in := testScriptInput()
	doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) {
			return completionResponse(testScriptDocument(t, in), "5000000000000"), nil
		},
	}}
	call, err := newOpenRouterClient(doer).Complete(context.Background(), "sk-or-test", in, scriptAttempt{})
	if err != nil || call.ActualUSD == nil || *call.ActualUSD != openRouterReserveUSD {
		t.Fatalf("absurd reported cost = %v, %v; want the %v reservation", call.ActualUSD, err, openRouterReserveUSD)
	}
}

func TestUSDToMicrodollarsIgnoresFloatNoise(t *testing.T) {
	for _, tc := range []struct {
		usd  float64
		want int64
	}{
		{4.03, 4_030_000}, {8.06, 8_060_000}, {0.29, 290_000}, {100, 100_000_000},
		{0.0000001, 1}, {4.0300003, 4_030_001}, {0.445245, 445_245},
	} {
		got, err := usdToMicrodollars(tc.usd)
		if err != nil || got != tc.want {
			t.Errorf("usdToMicrodollars(%v) = %d, %v; want %d", tc.usd, got, err, tc.want)
		}
	}
	// Every cent amount up to $100 converts exactly.
	for cents := 1; cents <= 10_000; cents++ {
		usd := float64(cents) / 100
		got, err := usdToMicrodollars(usd)
		if err != nil || got != int64(cents)*10_000 {
			t.Fatalf("usdToMicrodollars(%.2f) = %d, %v", usd, got, err)
		}
	}
	if _, err := usdToMicrodollars(math.NaN()); !errors.Is(err, ErrInvalidSpendAmount) {
		t.Fatal("NaN accepted")
	}
}

// The connection check sends the key, so it reads Local only without the
// settings cache: a change committed elsewhere stops it immediately.
func TestConnectionCheckReadsLocalOnlyWithoutTheCache(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		store := fixture.providers(nil)
		view := fixture.createProvider(t, store, providerScopeServer, "sk-or-v1-cachecheck")
		if inst, err := fixture.settings.Instance(ctx); err != nil || inst.LocalOnly {
			t.Fatalf("warm cache = %+v, %v", inst, err)
		}
		// Another instance (its own cache) turns Local only on.
		updateInstance(t, settings.New(d), func(inst *settings.Instance) { inst.LocalOnly = true })
		if cached, _ := fixture.settings.Instance(ctx); cached.LocalOnly {
			t.Skip("settings cache already refreshed; nothing to prove")
		}
		doer := &queuedDoer{}
		service := &providerService{store: store, settings: fixture.settings, client: newOpenRouterClient(doer)}
		if _, err := service.TestConnection(ctx, providerActor{UserID: fixture.userID, Admin: true}, view.ID); err == nil {
			t.Fatal("connection check ran although Local only was on")
		}
		if doer.count() != 0 {
			t.Fatalf("connection check sent %d requests under Local only", doer.count())
		}
	})
}
