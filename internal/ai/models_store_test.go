package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

func TestListManageableVisibilityIncludesDisabledAndNeverContainsSecrets(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		store := fixture.providers(nil)
		updateInstance(t, fixture.settings, func(inst *settings.Instance) {
			inst.UsersMayAddProviders = true
		})

		server, err := store.Create(ctx, providerActor{UserID: fixture.userID, Admin: true}, providerCreate{
			Name: "Server provider", Key: "server-list-secret", Scope: providerScopeServer,
		})
		if err != nil {
			t.Fatal(err)
		}
		own, err := store.Create(ctx, providerActor{UserID: fixture.userID}, providerCreate{
			Name: "Own provider", Key: "own-list-secret", Scope: providerScopeUser,
		})
		if err != nil {
			t.Fatal(err)
		}
		other, err := store.Create(ctx, providerActor{UserID: fixture.otherID}, providerCreate{
			Name: "Other provider", Key: "other-list-secret", Scope: providerScopeUser,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetEnabled(ctx, providerActor{UserID: fixture.userID}, own.ID, false); err != nil {
			t.Fatal(err)
		}

		adminRows, err := store.ListManageable(ctx, providerActor{UserID: fixture.userID, Admin: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := providerIDs(adminRows); strings.Join(got, ",") != server.ID+","+own.ID {
			t.Fatalf("admin rows = %v; want server and own personal only", got)
		}
		if adminRows[1].Enabled {
			t.Fatal("disabled provider was hidden or returned as enabled")
		}

		creatorRows, err := store.ListManageable(ctx, providerActor{UserID: fixture.userID})
		if err != nil {
			t.Fatal(err)
		}
		if got := providerIDs(creatorRows); len(got) != 1 || got[0] != own.ID {
			t.Fatalf("creator rows = %v; want own personal only", got)
		}
		otherRows, err := store.ListManageable(ctx, providerActor{UserID: fixture.otherID})
		if err != nil {
			t.Fatal(err)
		}
		if got := providerIDs(otherRows); len(got) != 1 || got[0] != other.ID {
			t.Fatalf("other rows = %v; want other personal only", got)
		}

		encoded, err := json.Marshal(append(append(adminRows, creatorRows...), otherRows...))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"server-list-secret", "own-list-secret", "other-list-secret", "key_enc", "base_url", "config"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("manageable list leaked %q: %s", forbidden, encoded)
			}
		}
	})
}

func providerIDs(rows []providerView) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}
