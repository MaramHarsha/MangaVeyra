package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Review fixes for ADR 0013 §9: ComfyUI and generic HTTP connections are a
// server the admin names (never a member's personal connection), and a
// custom connection runs only after a passed test for what it is now.

func testHTTPConfig() httpConfig {
	return httpConfig{Request: json.RawMessage(`{"prompt":"{{prompt}}"}`), Mode: httpModeSync, ImagePath: "images.0",
		ImageKind: httpBase64, CheckURL: "/health", Image: defaultImageSpec}
}

// countingServer answers every request with 200 {} and counts them.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.Close)
	return s, &calls
}

// insertPersonalServer stores an enabled personal ComfyUI or HTTP connection
// the way a member could save one before this rule.
func insertPersonalServer(t *testing.T, d *db.DB, ownerID, kind, base string, cfg any) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := db.NewID()
	if _, err := d.Exec(context.Background(), `INSERT INTO providers (id, kind, name, base_url, key_enc, key_hint, config, enabled, scope, owner_id, last_test, created_at, updated_at)
		VALUES (?, ?, ?, ?, '', '', ?, TRUE, 'user', ?, '{}', ?, ?)`, id, kind, "Personal "+kind, base, string(raw), ownerID, db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	return id
}

// passCustomTest records a passed test for the connection as it is now.
func passCustomTest(t *testing.T, store *providerStore, id string) {
	t.Helper()
	r, err := providerByID(context.Background(), store.db, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.recordCustomTest(context.Background(), dispatchProvider{ID: r.ID, Kind: r.Kind, BaseURL: r.BaseURL, KeyEnc: r.KeyEnc, Config: r.Config}, "ok"); err != nil {
		t.Fatal(err)
	}
}

func TestReviewComfyUIAndHTTPConnectionsAreAdminServerConnections(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &routeDoer{})
		allowPrivate(t, m, true)
		updateInstance(t, f.settings, func(inst *settings.Instance) { inst.UsersMayAddProviders = true })
		member, admin := providerActor{UserID: f.userID}, providerActor{UserID: f.otherID, Admin: true}
		server, calls := countingServer(t)
		for _, tc := range []struct {
			kind, base string
			cfg        any
		}{
			{providerKindHTTP, server.URL + "/containers/create", testHTTPConfig()},
			{providerKindComfyUI, server.URL, testComfyConfig(t)},
		} {
			in := providerCreate{Kind: tc.kind, Name: tc.kind + " server", BaseURL: tc.base, Config: tc.cfg}
			for _, c := range []struct {
				actor providerActor
				scope string
			}{{member, providerScopeUser}, {member, providerScopeServer}, {admin, providerScopeUser}} {
				in.Scope = c.scope
				if _, err := m.providers.Create(ctx, c.actor, in); !errors.Is(err, errProviderForbidden) {
					t.Fatalf("%s created by %+v as %s: %v", tc.kind, c.actor, c.scope, err)
				}
			}
			in.Scope = providerScopeServer
			shared, err := m.providers.Create(ctx, admin, in)
			if err != nil {
				t.Fatalf("admin %s: %v", tc.kind, err)
			}
			// Members use the admin's server through routing like any server
			// connection.
			passCustomTest(t, m.providers, shared.ID)
			if _, err := m.providers.Candidate(ctx, f.userID, shared.ID); err != nil {
				t.Fatalf("member cannot route to the server %s: %v", tc.kind, err)
			}

			// A personal one saved before this rule never runs, tests, turns
			// on or reopens in the wizard.
			personal := insertPersonalServer(t, d, f.userID, tc.kind, tc.base, tc.cfg)
			passCustomTest(t, m.providers, personal)
			candidates, err := m.providers.Candidates(ctx, f.userID)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range candidates {
				if c.ID == personal {
					t.Fatalf("personal %s is a routing candidate", tc.kind)
				}
			}
			if _, err := m.providers.Candidate(ctx, f.userID, personal); err == nil {
				t.Fatalf("personal %s candidate", tc.kind)
			}
			if _, err := m.providers.ResolveDispatch(ctx, f.userID, personal); err == nil {
				t.Fatalf("personal %s dispatch", tc.kind)
			}
			if _, err := m.providers.SetEnabled(ctx, member, personal, false); err != nil {
				t.Fatalf("turning a personal %s off: %v", tc.kind, err)
			}
			if _, err := m.providers.SetEnabled(ctx, member, personal, true); err == nil {
				t.Fatalf("personal %s turned on", tc.kind)
			}
			before := calls.Load()
			if _, err := m.providerAPI.TestConnection(ctx, member, personal); err == nil || calls.Load() != before {
				t.Fatalf("personal %s tested: %v calls %d→%d", tc.kind, err, before, calls.Load())
			}
			if _, err := m.customRecord(ctx, member, personal); err == nil {
				t.Fatalf("personal %s reopened in G12", tc.kind)
			}
			if err := m.providers.Delete(ctx, member, personal); err != nil {
				t.Fatalf("deleting a personal %s: %v", tc.kind, err)
			}
		}
	})
}

func TestReviewCustomConnectionsRunOnlyAfterAPassedTest(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &routeDoer{})
		allowPrivate(t, m, true)
		admin := providerActor{UserID: f.userID, Admin: true}
		var healthy atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" && healthy.Load() {
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			http.Error(w, "down", http.StatusServiceUnavailable)
		}))
		t.Cleanup(server.Close)
		view, err := m.providers.Create(ctx, admin, providerCreate{Kind: providerKindHTTP, Name: "Image server", BaseURL: server.URL + "/generate",
			Config: testHTTPConfig(), Scope: providerScopeServer, Draft: true})
		if err != nil {
			t.Fatal(err)
		}
		routes := func() bool {
			_, err := m.providers.Candidate(ctx, f.userID, view.ID)
			plan := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1}})
			return err == nil && plan.ProviderID == view.ID
		}

		// An untested draft and a draft whose test failed cannot be turned on.
		if _, err := m.providers.SetEnabled(ctx, admin, view.ID, true); !errors.Is(err, errProviderUntested) {
			t.Fatalf("untested draft enabled: %v", err)
		}
		if _, err := m.providerAPI.TestConnection(ctx, admin, view.ID); err == nil {
			t.Fatal("a failing server passed the test")
		}
		if _, err := m.providers.SetEnabled(ctx, admin, view.ID, true); !errors.Is(err, errProviderUntested) {
			t.Fatalf("failed draft enabled: %v", err)
		}

		// A passed test (G2 Test connection works on a disabled connection).
		healthy.Store(true)
		if _, err := m.providerAPI.TestConnection(ctx, admin, view.ID); err != nil {
			t.Fatalf("test: %v", err)
		}
		if _, err := m.providers.SetEnabled(ctx, admin, view.ID, true); err != nil {
			t.Fatalf("tested connection: %v", err)
		}
		if !routes() {
			t.Fatal("a tested connection does not route")
		}

		// A failed re-test stops routing and dispatch until it passes again.
		healthy.Store(false)
		if _, err := m.providerAPI.TestConnection(ctx, admin, view.ID); err == nil {
			t.Fatal("failing re-test passed")
		}
		if routes() {
			t.Fatal("a connection whose last test failed still routes")
		}
		if _, err := m.providers.ResolveDispatch(ctx, f.userID, view.ID); err == nil {
			t.Fatal("a connection whose last test failed still dispatches")
		}
		healthy.Store(true)
		if _, err := m.providerAPI.TestConnection(ctx, admin, view.ID); err != nil || !routes() {
			t.Fatalf("re-tested connection: %v", err)
		}

		// A change the test did not cover (here written straight to the row,
		// as a change racing a running test would land) leaves it untested.
		if _, err := d.Exec(ctx, `UPDATE providers SET base_url = ? WHERE id = ?`, server.URL+"/other", view.ID); err != nil {
			t.Fatal(err)
		}
		if routes() {
			t.Fatal("a changed endpoint routes on the old test")
		}
		if v, err := m.providers.GetManageable(ctx, admin, view.ID); err != nil || v.LastTest.State != "" {
			t.Fatalf("G2 shows the old test as current: %+v %v", v.LastTest, err)
		}
		if _, err := m.providers.SetEnabled(ctx, admin, view.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := m.providers.SetEnabled(ctx, admin, view.ID, true); !errors.Is(err, errProviderUntested) {
			t.Fatalf("changed endpoint enabled on the old test: %v", err)
		}
		// Routing modes and image limits are not what the test covers.
		passCustomTest(t, m.providers, view.ID)
		cfg := testHTTPConfig()
		cfg.Modes, cfg.Image.MaxWidth = []string{ModeBalanced}, 2048
		if err := m.providers.updateCustomConfig(ctx, view.ID, providerKindHTTP, server.URL+"/other", "", cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := m.providers.SetEnabled(ctx, admin, view.ID, true); err != nil {
			t.Fatalf("described connection: %v", err)
		}

		// ComfyUI: a workflow with a paid partner node fails its test, so it
		// can never be turned on.
		fake := &fakeComfy{png: phase4PNG(t, 64, 64), partner: true}
		comfy := httptest.NewServer(fake.handler(t))
		t.Cleanup(comfy.Close)
		draft, err := m.providers.Create(ctx, admin, providerCreate{Kind: providerKindComfyUI, Name: "Partner", BaseURL: comfy.URL,
			Config: testComfyConfig(t), Scope: providerScopeServer, Draft: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.providerAPI.TestConnection(ctx, admin, draft.ID); err == nil {
			t.Fatal("partner node workflow passed")
		}
		if _, err := m.providers.SetEnabled(ctx, admin, draft.ID, true); !errors.Is(err, errProviderUntested) {
			t.Fatalf("partner node workflow enabled: %v", err)
		}
	})
}
