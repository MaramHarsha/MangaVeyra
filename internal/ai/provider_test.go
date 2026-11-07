package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

func TestProviderLifecycleAndSecretBinding(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		fixed := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
		store := fixture.providers(func() time.Time { return fixed })

		if _, err := store.Create(ctx, providerActor{UserID: fixture.userID}, providerCreate{
			Name: "Server", Key: "server-secret", Scope: providerScopeServer,
		}); !errors.Is(err, errProviderForbidden) {
			t.Fatalf("non-admin server create error = %v", err)
		}
		if _, err := store.Create(ctx, providerActor{UserID: fixture.userID}, providerCreate{
			Name: "User", Key: "user-secret", Scope: providerScopeUser,
		}); !errors.Is(err, errProviderForbidden) {
			t.Fatalf("user provider disabled error = %v", err)
		}

		server := fixture.createProvider(t, store, providerScopeServer, "server-secret")
		user := fixture.createProvider(t, store, providerScopeUser, "user-secret")
		if server.OwnerID != "" || user.OwnerID != fixture.userID || server.Kind != providerKindOpenRouter {
			t.Fatalf("scope/owner invariant lost: server=%+v user=%+v", server, user)
		}
		var original string
		if err := d.Get(ctx, &original, `SELECT key_enc FROM providers WHERE id = ?`, server.ID); err != nil {
			t.Fatal(err)
		}
		plaintext, err := fixture.box.OpenString(original, providerKeyAAD(server.ID))
		if err != nil || plaintext != "server-secret" {
			t.Fatalf("open exact AAD = %q, %v", plaintext, err)
		}
		if _, err := fixture.box.OpenString(original, providerKeyAAD(user.ID)); !errors.Is(err, secrets.ErrDecrypt) {
			t.Fatalf("wrong-row AAD error = %v", err)
		}
		encoded, err := json.Marshal(server)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "server-secret") || strings.Contains(string(encoded), original) || strings.Contains(string(encoded), "key_enc") {
			t.Fatalf("provider view leaks secret material: %s", encoded)
		}

		updated, err := store.Update(ctx, providerActor{UserID: fixture.userID, Admin: true}, server.ID,
			providerUpdate{Name: "Renamed", Key: ""})
		if err != nil || updated.Name != "Renamed" || updated.ID != server.ID || updated.Scope != server.Scope {
			t.Fatalf("metadata update = %+v, %v", updated, err)
		}
		var retained string
		_ = d.Get(ctx, &retained, `SELECT key_enc FROM providers WHERE id = ?`, server.ID)
		if retained != original {
			t.Fatal("blank ordinary update changed ciphertext")
		}
		if _, err := store.Update(ctx, providerActor{UserID: fixture.userID, Admin: true}, server.ID,
			providerUpdate{Name: "Bad", Key: "replacement"}); !errors.Is(err, errProviderInvalid) {
			t.Fatalf("ordinary key replacement error = %v", err)
		}
		replaced, err := store.ReplaceKey(ctx, providerActor{UserID: fixture.userID, Admin: true}, server.ID, "server-secret")
		if err != nil || replaced.KeyHint == "" {
			t.Fatalf("replace key = %+v, %v", replaced, err)
		}
		var resealed string
		_ = d.Get(ctx, &resealed, `SELECT key_enc FROM providers WHERE id = ?`, server.ID)
		if resealed == original {
			t.Fatal("explicit replacement reused the prior nonce/ciphertext")
		}

		disabled, err := store.SetEnabled(ctx, providerActor{UserID: fixture.userID, Admin: true}, server.ID, false)
		if err != nil || disabled.Enabled {
			t.Fatalf("disable = %+v, %v", disabled, err)
		}
		var afterDisable string
		_ = d.Get(ctx, &afterDisable, `SELECT key_enc FROM providers WHERE id = ?`, server.ID)
		if afterDisable != resealed {
			t.Fatal("disable erased or changed ciphertext")
		}
		if _, err := store.GetManageable(ctx, providerActor{UserID: fixture.otherID}, user.ID); !errors.Is(err, errProviderForbidden) {
			t.Fatalf("cross-owner manage error = %v", err)
		}

		copyTarget := fixture.createProvider(t, store, providerScopeServer, "other-secret")
		if _, err := d.Exec(ctx, `UPDATE providers SET key_enc = ? WHERE id = ?`, resealed, copyTarget.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolveDispatch(ctx, fixture.userID, copyTarget.ID); !errors.Is(err, errProviderUnavailable) {
			t.Fatalf("copied ciphertext dispatch error = %v", err)
		}

		if err := store.Delete(ctx, providerActor{UserID: fixture.userID, Admin: true}, server.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetManageable(ctx, providerActor{UserID: fixture.userID, Admin: true}, server.ID); !errors.Is(err, errProviderNotFound) {
			t.Fatalf("deleted provider lookup error = %v", err)
		}
	})
}

func TestProviderConnectionUsesKeyEndpointAndGuardsTransport(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		store := fixture.providers(nil)
		provider := fixture.createProvider(t, store, providerScopeServer, "or-test-key")
		doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
			func(*http.Request) (*http.Response, error) {
				return httpJSON(http.StatusOK, map[string]any{"label": "key"}, map[string]string{"X-Request-ID": "req_123.good"}), nil
			},
		}}
		service := &providerService{store: store, settings: fixture.settings, client: newOpenRouterClient(doer)}
		actor := providerActor{UserID: fixture.userID, Admin: true}
		view, err := service.TestConnection(ctx, actor, provider.ID)
		if err != nil || view.LastTest.State != "ok" || view.LastTest.RequestID != "req_123.good" || view.LastTest.Time.IsZero() {
			t.Fatalf("connection result = %+v, %v", view.LastTest, err)
		}
		seen := doer.requests()
		if len(seen) != 1 || seen[0].Method != http.MethodGet || seen[0].URL != openRouterKeyURL ||
			seen[0].Authorization != "Bearer or-test-key" || len(seen[0].Body) != 0 {
			t.Fatalf("unexpected key test request: %+v", seen)
		}
		baseline := doer.count()

		updateInstance(t, fixture.settings, func(inst *settings.Instance) { inst.LocalOnly = true })
		if _, err := service.TestConnection(ctx, actor, provider.ID); !errors.Is(err, errProviderUnavailable) {
			t.Fatalf("local-only connection error = %v", err)
		}
		if doer.count() != baseline {
			t.Fatal("local-only connection made a request")
		}
		updateInstance(t, fixture.settings, func(inst *settings.Instance) { inst.LocalOnly = false })

		if _, err := d.Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES ('local_only', '{')
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`); err != nil {
			t.Fatal(err)
		}
		fixture.settings.Invalidate()
		if _, err := service.TestConnection(ctx, actor, provider.ID); !errors.Is(err, errProviderUnavailable) {
			t.Fatalf("settings decode connection error = %v", err)
		}
		if doer.count() != baseline {
			t.Fatal("settings failure made a request")
		}
		if _, err := d.Exec(ctx, `DELETE FROM instance_settings WHERE key = 'local_only'`); err != nil {
			t.Fatal(err)
		}
		fixture.settings.Invalidate()

		if _, err := store.SetEnabled(ctx, actor, provider.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := service.TestConnection(ctx, actor, provider.ID); !errors.Is(err, errProviderUnavailable) {
			t.Fatalf("disabled connection error = %v", err)
		}
		if doer.count() != baseline {
			t.Fatal("disabled provider made a request")
		}

		userProvider := fixture.createProvider(t, store, providerScopeUser, "user-key")
		if _, err := service.TestConnection(ctx, providerActor{UserID: fixture.otherID}, userProvider.ID); !errors.Is(err, errProviderForbidden) {
			t.Fatalf("cross-owner connection error = %v", err)
		}
		if _, err := d.Exec(ctx, `UPDATE providers SET key_enc = 'tampered' WHERE id = ?`, userProvider.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.TestConnection(ctx, actor, userProvider.ID); !errors.Is(err, errProviderUnavailable) {
			t.Fatalf("corrupt-key connection error = %v", err)
		}
		if doer.count() != baseline {
			t.Fatal("guarded provider made a request")
		}
	})
}
