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
			if response.StatusCode != http.StatusSeeOther || !strings.Contains(response.Header.Get("Location"), "/test?id=") {
				t.Fatalf("connect=%d %s", response.StatusCode, response.Body)
			}
			id := providerIDByName(t, ta, "Original local writer")
			// "Next: test" runs the connection check, so step 2 opens on its
			// designed result: Status Success · Time · Cost and "Next: describe".
			if calls.Load() != 1 {
				t.Fatalf("Next: test made %d model-list requests", calls.Load())
			}
			body := mainOf(t, admin.Get("/models/providers/new/test?id="+id))
			// Once a draft exists the designed type switch only shows its type.
			mustContain(t, body, "Step 2 of 4", `<span class="is-current">OpenAI-compatible</span>`, `<span aria-disabled="true">ComfyUI workflow</span>`,
				`<span aria-disabled="true">Hugging Face</span>`, "MODEL LIST · “qwen3:8b” installed",
				`<dd class="mv-aicustom__status--ok">Success</dd>`, "<dt>Time</dt>", "<dt>Cost</dt>", "<dd>Local</dd>", "Next: describe",
				`href="/models/providers/new/connect?id=`+id+`"`)
			mustNotContain(t, body, "custom-sealed-secret", "Capability", "Test and continue", "Checks the installed model list", "mv-soon")
			var sealed string
			if err := ta.Deps.DB.Get(t.Context(), &sealed, `SELECT key_enc FROM providers WHERE id = ?`, id); err != nil || sealed == "" || strings.Contains(sealed, "custom-sealed-secret") {
				t.Fatalf("key not sealed: %v", err)
			}
			// Back on step 2 returns to step 1 with the draft, not to /models.
			back := mainOf(t, admin.Get("/models/providers/new/connect?id="+id))
			mustContain(t, back, "Step 1 of 4", `value="Original local writer"`, `value="`+endpoint.URL+`/v1"`, `value="qwen3:8b"`, "A key is saved", `name="id" value="`+id+`"`)
			mustNotContain(t, back, "custom-sealed-secret")
			jump := postModelsForm(admin, "/models/providers/new/route", url.Values{"id": {id}})
			if jump.StatusCode != http.StatusForbidden {
				t.Fatalf("skipped testing: %d", jump.StatusCode)
			}
			// Re-saving step 1 updates the same draft (blank key keeps it) and retests.
			resaved := postModelsForm(admin, "/models/providers/new/connect", url.Values{"id": {id}, "name": {"Original local writer"}, "endpoint": {endpoint.URL + "/v1"}, "model": {"qwen3:8b"}, "scope": {"server"}})
			if resaved.StatusCode != http.StatusSeeOther || calls.Load() != 2 {
				t.Fatalf("resave=%d calls=%d %s", resaved.StatusCode, calls.Load(), resaved.Body)
			}
			var drafts int
			if err := ta.Deps.DB.Get(t.Context(), &drafts, `SELECT COUNT(*) FROM providers WHERE name = ? AND key_enc = ?`, "Original local writer", sealed); err != nil || drafts != 1 {
				t.Fatalf("resave duplicated the draft or dropped its key: %d %v", drafts, err)
			}
			next := postModelsForm(admin, "/models/providers/new/test", url.Values{"id": {id}, "action": {"next"}})
			if next.StatusCode != http.StatusSeeOther || !strings.Contains(next.Header.Get("Location"), "/describe") {
				t.Fatalf("next=%d %s", next.StatusCode, next.Body)
			}
			if calls.Load() != 2 {
				t.Fatalf("Next: describe re-ran the test: %d", calls.Load())
			}
			described := postModelsForm(admin, "/models/providers/new/describe", url.Values{"id": {id}, "name": {"Original local writer"}, "context": {"32768"}, "output": {"4096"}})
			if described.StatusCode != http.StatusSeeOther {
				t.Fatalf("describe=%d %s", described.StatusCode, described.Body)
			}
			// Step 4 offers the routing modes, defaulting to every mode but Best.
			route := mainOf(t, admin.Get("/models/providers/new/route?id="+id))
			mustContain(t, route, "Step 4 of 4", "Auto-routing may use it in", `name="modes" value="balanced" checked`,
				`name="modes" value="fastest" checked`, `name="modes" value="free_only" checked`, "Everyone on this server", "Connect provider")
			mustNotContain(t, route, `name="modes" value="best" checked`, `name="modes" value="local_only"`)
			// The chips follow the G11 order: Local only (disabled) before Free only.
			if local, free := strings.Index(route, ">Local only<"), strings.Index(route, `value="free_only"`); local < 0 || free < 0 || local > free {
				t.Fatalf("step 4 chip order: local=%d free=%d", local, free)
			}
			none := postModelsForm(admin, "/models/providers/new/route", url.Values{"id": {id}})
			if none.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(none.Body), "Choose at least one routing mode.") {
				t.Fatalf("route without modes=%d", none.StatusCode)
			}
			routed := postModelsForm(admin, "/models/providers/new/route", url.Values{"id": {id}, "modes": {"best", "balanced", "local_only"}})
			if routed.StatusCode != http.StatusSeeOther {
				t.Fatalf("route=%d %s", routed.StatusCode, routed.Body)
			}
			var enabled bool
			var config string
			if err := ta.Deps.DB.Get(t.Context(), &enabled, `SELECT enabled FROM providers WHERE id = ?`, id); err != nil || !enabled {
				t.Fatalf("not enabled %v", err)
			}
			if err := ta.Deps.DB.Get(t.Context(), &config, `SELECT config FROM providers WHERE id = ?`, id); err != nil || !strings.Contains(config, `"modes":["balanced","best"]`) {
				t.Fatalf("routing modes not stored: %s %v", config, err)
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
			for _, path := range []string{"/models/providers/new/not-a-step", "/models/providers/new/huggingface", "/models/providers/new/comfyui"} {
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
			mustNotContain(t, body, "aiimage.", "@ui.")
		}
	})
}

func TestFalConnectionCanBeReenabledFromImageCatalogue(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		created := postModelsForm(admin, "/models/providers", url.Values{"kind": {"fal"}, "name": {"Harbour image connection"}, "key": {"fal-fixture-secret"}, "scope": {"server"}})
		if created.StatusCode != http.StatusSeeOther {
			t.Fatalf("fal create=%d %s", created.StatusCode, created.Body)
		}
		id := providerIDByName(t, ta, "Harbour image connection")
		disabled := postModelsForm(admin, "/models/providers/"+id+"/enabled?cap=image", url.Values{"enabled": {"false"}})
		if disabled.StatusCode != http.StatusSeeOther || disabled.Header.Get("Location") != "/models?cap=image" {
			t.Fatalf("disable=%d %s", disabled.StatusCode, disabled.Body)
		}
		body := mainOf(t, admin.Get("/models?cap=image"))
		mustContain(t, body, "Harbour image connection", "Disabled", "/models/providers/"+id+"/enabled?cap=image")
		mustNotContain(t, body, "fal-fixture-secret")
		enabled := postModelsForm(admin, "/models/providers/"+id+"/enabled?cap=image", url.Values{"enabled": {"true"}})
		if enabled.StatusCode != http.StatusSeeOther {
			t.Fatalf("enable=%d", enabled.StatusCode)
		}
	})
}
