package ai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// fakeModelList is a local OpenAI-compatible server that only lists models.
func fakeModelList(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "qwen3:8b"}}})
	}))
	t.Cleanup(server.Close)
	return server
}

// A default install (no MV_ALLOW_PRIVATE_PROVIDER_URLS) must connect the
// pre-filled local endpoint from the UI: the admin allows private networks
// with a visible switch, and every later request re-checks the live setting.
func TestCustomProviderPrivateEndpointsAreAnAdminSettingByDefaultOff(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			var calls atomic.Int64
			endpoint := fakeModelList(t, &calls)
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			admin := ta.Login(t, ta.Admin.Username)

			page := mainOf(t, admin.Get("/models/providers/new/connect"))
			mustContain(t, page, `value="http://localhost:11434/v1"`, "Allow private-network endpoints on this server",
				`id="custom-allow-private" name="allow_private" value="true" checked`, "Link-local and cloud metadata addresses stay blocked")
			mustNotContain(t, page, "An admin must enable private provider URLs")

			// Switched off by the admin: a clear error on the endpoint field.
			refused := postModelsForm(admin, "/models/providers/new/connect", url.Values{"name": {"Harbour local"}, "endpoint": {endpoint.URL + "/v1"}, "model": {"qwen3:8b"}, "scope": {"server"}})
			body := string(refused.Body)
			if refused.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="custom-endpoint-error"`) || !strings.Contains(body, "Turn on “Allow private-network endpoints”") {
				t.Fatalf("private endpoint without the switch = %d %.3000s", refused.StatusCode, body)
			}
			if calls.Load() != 0 {
				t.Fatal("a refused endpoint was contacted")
			}
			// Link-local and metadata addresses stay blocked whatever the switch says.
			metadata := postModelsForm(admin, "/models/providers/new/connect", url.Values{"name": {"Metadata"}, "endpoint": {"http://169.254.169.254/v1"}, "model": {"qwen3:8b"}, "scope": {"server"}, "allow_private": {"true"}})
			if metadata.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(metadata.Body), "Link-local and cloud metadata addresses are always blocked.") {
				t.Fatalf("metadata endpoint = %d", metadata.StatusCode)
			}

			allowed := postModelsForm(admin, "/models/providers/new/connect", url.Values{"name": {"Harbour local"}, "endpoint": {endpoint.URL + "/v1"}, "model": {"qwen3:8b"}, "scope": {"server"}, "allow_private": {"true"}})
			if allowed.StatusCode != http.StatusSeeOther || calls.Load() != 1 {
				t.Fatalf("allowed connect = %d calls=%d %.2000s", allowed.StatusCode, calls.Load(), allowed.Body)
			}
			id := providerIDByName(t, ta, "Harbour local")
			mustContain(t, mainOf(t, admin.Get("/models/providers/new/test?id="+id)), `<dd class="mv-aicustom__status--ok">Success</dd>`)
			mustNotContain(t, mainOf(t, admin.Get("/models/providers/new/connect")), "custom-allow-private")

			// Turning the setting off applies at once: the next test never dials.
			if err := ta.Deps.Settings.SetKey(context.Background(), "ai.private_endpoints", map[string]bool{"allow": false}, ta.Admin.ID); err != nil {
				t.Fatal(err)
			}
			retest := postModelsForm(admin, "/models/providers/new/test", url.Values{"id": {id}})
			if retest.StatusCode != http.StatusSeeOther || calls.Load() != 1 {
				t.Fatalf("retest after switching off = %d calls=%d", retest.StatusCode, calls.Load())
			}
			mustContain(t, mainOf(t, admin.Get("/models/providers/new/test?id="+id)), `<dd class="mv-aicustom__status--error">Failed</dd>`, "Test again")

			// Non-admins cannot switch it on; they are told to ask an admin.
			updateModelsInstance(t, ta, func(inst *settings.Instance) { inst.UsersMayAddProviders = true })
			_, creator := ta.UserClient(t, auth.RoleCreator)
			ask := mainOf(t, creator.Get("/models/providers/new/connect"))
			mustContain(t, ask, "Private-network endpoints are off", "Ask an admin to allow private-network endpoints in Model routing")
			mustNotContain(t, ask, "custom-allow-private")
			denied := postModelsForm(creator, "/models/providers/new/connect", url.Values{"name": {"Mine"}, "endpoint": {endpoint.URL + "/v1"}, "model": {"qwen3:8b"}, "scope": {"user"}, "allow_private": {"true"}})
			if denied.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(denied.Body), `id="custom-endpoint-error"`) || calls.Load() != 1 {
				t.Fatalf("creator enabled private endpoints: %d calls=%d", denied.StatusCode, calls.Load())
			}
			var stored int
			if err := ta.Deps.DB.Get(context.Background(), &stored, `SELECT COUNT(*) FROM instance_settings WHERE key = 'ai.private_endpoints' AND value LIKE '%false%'`); err != nil || stored != 1 {
				t.Fatalf("private endpoint setting = %d %v", stored, err)
			}
		})
	}
}

// An empty Model ID is reported on the Model ID field, not on the endpoint.
// While members may not add their own providers, a creator sees both G2 add
// actions disabled with the same reason, and reaching G12 directly shows the
// designed "ask an admin" state instead of a bare 403 page.
func TestCustomProviderCreatorWithoutPersonalProvidersIsAskedToAskAnAdmin(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		updateModelsInstance(t, ta, func(inst *settings.Instance) { inst.UsersMayAddProviders = false })
		_, creator := ta.UserClient(t, auth.RoleCreator)
		models := mainOf(t, creator.Get("/models"))
		mustNotContain(t, models, `href="/models/providers/custom"`)
		if strings.Count(models, `data-tip="Only an admin can add providers on this server."`) != 2 {
			t.Fatalf("G2 add actions are not both disabled with the reason: %.1200s", models)
		}
		r := creator.Get("/models/providers/new/connect")
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("G12 for a creator = %d", r.StatusCode)
		}
		r.StatusCode = http.StatusOK // the designed page, served with 403
		page := mainOf(t, r)
		mustContain(t, page, "Ask an admin to add this provider", "Add a custom provider", `href="/models"`)
		mustNotContain(t, page, `name="endpoint"`, "<form")
	})
}

func TestCustomProviderEmptyModelErrorIsOnModelField(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		r := postModelsForm(admin, "/models/providers/new/connect", url.Values{"name": {"Ollama"}, "endpoint": {"http://localhost:11434/v1"}, "model": {""}, "scope": {"server"}, "allow_private": {"true"}})
		body := string(r.Body)
		if r.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("empty model = %d", r.StatusCode)
		}
		mustContain(t, body, `id="custom-model-error"`, "Enter the exact ID of a model installed on the server")
		mustNotContain(t, body, `id="custom-endpoint-error"`, "Use an allowed local endpoint")
	})
}
