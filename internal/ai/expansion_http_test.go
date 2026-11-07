package ai_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func TestCustomProviderFourStepsAndRoutingPermissions(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			var calls atomic.Int64
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/v1/models" {
					t.Errorf("unexpected generation request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "qwen3:8b"}}})
			}))
			defer endpoint.Close()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect), testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true"))
			admin := ta.Login(t, ta.Admin.Username)
			for _, path := range []string{"/models/routing", "/models/providers/new/connect"} {
				r := admin.Get(path)
				if r.StatusCode != http.StatusOK {
					t.Fatalf("GET %s = %d %s", path, r.StatusCode, r.Body)
				}
				assertEveryTargetIsServed(t, ta, mainOf(t, r))
			}
			response := postModelsForm(admin, "/models/providers/new/connect", url.Values{"name": {"Original local writer"}, "endpoint": {endpoint.URL + "/v1"}, "model": {"qwen3:8b"}, "key": {"custom-sealed-secret"}, "scope": {"server"}})
			if response.StatusCode != http.StatusSeeOther {
				t.Fatalf("connect=%d %s", response.StatusCode, response.Body)
			}
			id := providerIDByName(t, ta, "Original local writer")
			if calls.Load() != 0 {
				t.Fatal("Connect sent an unsolicited request")
			}
			body := mainOf(t, admin.Get("/models/providers/new/test?id="+id))
			mustContain(t, body, "Step 2 of 4", "Text connection check", "ComfyUI", "Hugging Face", "Soon")
			mustNotContain(t, body, "custom-sealed-secret")
			var sealed string
			if err := ta.Deps.DB.Get(t.Context(), &sealed, `SELECT key_enc FROM providers WHERE id = ?`, id); err != nil || sealed == "" || strings.Contains(sealed, "custom-sealed-secret") {
				t.Fatalf("key not sealed: %v", err)
			}
			jump := postModelsForm(admin, "/models/providers/new/route", url.Values{"id": {id}})
			if jump.StatusCode != http.StatusForbidden {
				t.Fatalf("skipped testing: %d", jump.StatusCode)
			}
			tested := postModelsForm(admin, "/models/providers/new/test", url.Values{"id": {id}})
			if tested.StatusCode != http.StatusSeeOther || !strings.Contains(tested.Header.Get("Location"), "/describe") {
				t.Fatalf("test=%d %s", tested.StatusCode, tested.Body)
			}
			if calls.Load() != 1 {
				t.Fatalf("test calls=%d", calls.Load())
			}
			described := postModelsForm(admin, "/models/providers/new/describe", url.Values{"id": {id}, "name": {"Original local writer"}, "context": {"32768"}, "output": {"4096"}})
			if described.StatusCode != http.StatusSeeOther {
				t.Fatalf("describe=%d %s", described.StatusCode, described.Body)
			}
			routed := postModelsForm(admin, "/models/providers/new/route", url.Values{"id": {id}})
			if routed.StatusCode != http.StatusSeeOther {
				t.Fatalf("route=%d %s", routed.StatusCode, routed.Body)
			}
			var enabled bool
			if err := ta.Deps.DB.Get(t.Context(), &enabled, `SELECT enabled FROM providers WHERE id = ?`, id); err != nil || !enabled {
				t.Fatalf("not enabled %v", err)
			}
			models := mainOf(t, admin.Get("/models?cap=text"))
			mustContain(t, models, "qwen3:8b", "Original local writer")
			mustNotContain(t, models, "custom-sealed-secret")
			_, creator := ta.UserClient(t, auth.RoleCreator)
			denied := postModelsForm(creator, "/models/routing", url.Values{"mode": {"free_only"}, "limit": {"0"}})
			if denied.StatusCode != http.StatusForbidden {
				t.Fatalf("creator changed server routing: %d", denied.StatusCode)
			}
			// Explicit step registration must not turn unimplemented provider names into
			// successful wizard pages through a wildcard route.
			for _, path := range []string{"/models/providers/new/google", "/models/providers/new/xai", "/models/providers/new/not-a-step"} {
				if ta.Router.Serves(http.MethodGet, path) {
					t.Errorf("unexpected route %s", path)
				}
			}
		})
	}
}

func TestImageCatalogueAndRoutingScreensAreCompleteWithoutJavaScript(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		for path, labels := range map[string][]string{"/models?cap=image": {"Placeholder art", "Seedream 4.5", "FLUX.1 schnell", "Region edits", "Model", "Provider"}, "/models/routing": {"Routing", "Recent decisions", "Pins", "Guards", "Balanced", "Free only", "Licence guard"}, "/models/providers/new/connect": {"Step 1 of 4", "1. Connect", "2. Test", "3. Describe", "4. Route", "Local text server", "API key (optional)"}} {
			response := admin.Get(path)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d", path, response.StatusCode)
			}
			body := mainOf(t, response)
			mustContain(t, body, labels...)
			assertEveryTargetIsServed(t, ta, body)
			mustNotContain(t, body, "aiimage.")
		}
	})
}
