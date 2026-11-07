package ai_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func forEachModelsApp(t *testing.T, fn func(*testing.T, *testutil.TestApp)) {
	t.Helper()
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			fn(t, testutil.NewTestApp(t, testutil.WithDialect(dialect)))
		})
	}
}

func postModelsForm(c *testutil.Client, path string, values url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(values.Encode()), "application/x-www-form-urlencoded")
}

func updateModelsInstance(t *testing.T, ta *testutil.TestApp, fn func(*settings.Instance)) {
	t.Helper()
	if _, err := ta.Deps.Settings.UpdateInstance(context.Background(), ta.Admin.ID, func(inst *settings.Instance) error {
		fn(inst)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func providerIDByName(t *testing.T, ta *testutil.TestApp, name string) string {
	t.Helper()
	var id string
	if err := ta.Deps.DB.Get(context.Background(), &id, `SELECT id FROM providers WHERE name = ?`, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestModelsRoutesRequireBrowserSession(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		anon := ta.NewClient().Get("/models")
		if anon.StatusCode != http.StatusSeeOther || !strings.HasPrefix(anon.Header.Get("Location"), "/login?next=") {
			t.Fatalf("anonymous GET = %d %q", anon.StatusCode, anon.Header.Get("Location"))
		}

		raw, _, err := ta.Deps.Auth.CreateToken(context.Background(), auth.TokenInput{
			UserID: ta.Admin.ID, Kind: auth.TokenAPI, Name: "models-test",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := ta.BearerClient(raw).Get("/models"); got.StatusCode != http.StatusForbidden {
			t.Fatalf("bearer GET = %d, want 403: %.1000s", got.StatusCode, got.Body)
		}
	})
}

func TestModelsCreateAuthorizationAndSecretRecovery(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		page := admin.Get("/models")
		if page.StatusCode != http.StatusOK || !strings.Contains(string(page.Body), "Built-in placeholder") ||
			!strings.Contains(string(page.Body), "Connect OpenRouter") {
			t.Fatalf("models page = %d: %.2500s", page.StatusCode, page.Body)
		}

		secret := "or-validation-secret-never-render"
		invalid := postModelsForm(admin, "/models/providers", url.Values{
			"name": {""}, "key": {secret}, "scope": {"server"}, "next": {"https://evil.example/steal"},
		})
		if invalid.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("invalid create = %d: %.2000s", invalid.StatusCode, invalid.Body)
		}
		if strings.Contains(string(invalid.Body), secret) || strings.Contains(string(invalid.Body), "evil.example") {
			t.Fatalf("validation response echoed a secret or unsafe redirect: %.2500s", invalid.Body)
		}

		created := postModelsForm(admin, "/models/providers", url.Values{
			"name": {"Server OpenRouter"}, "key": {"or-server-create-secret"}, "scope": {"server"},
			"next": {"https://evil.example/steal"},
		})
		if created.StatusCode != http.StatusSeeOther || created.Header.Get("Location") != "/models" {
			t.Fatalf("admin create = %d %q: %.1500s", created.StatusCode, created.Header.Get("Location"), created.Body)
		}
		var scope string
		var owner *string
		if err := ta.Deps.DB.QueryRow(context.Background(), `SELECT scope, owner_id FROM providers WHERE name = ?`, "Server OpenRouter").Scan(&scope, &owner); err != nil {
			t.Fatal(err)
		}
		if scope != "server" || owner != nil {
			t.Fatalf("server provider scope/owner = %q/%v", scope, owner)
		}

		creator, creatorClient := ta.UserClient(t, auth.RoleCreator)
		if got := creatorClient.Get("/models/providers/new?kind=openrouter"); got.StatusCode != http.StatusForbidden {
			t.Fatalf("personal form while disabled = %d", got.StatusCode)
		}
		if got := postModelsForm(creatorClient, "/models/providers", url.Values{
			"name": {"Forged server"}, "key": {"or-forged-secret"}, "scope": {"server"},
		}); got.StatusCode != http.StatusForbidden {
			t.Fatalf("forged server scope = %d: %.1000s", got.StatusCode, got.Body)
		}

		updateModelsInstance(t, ta, func(inst *settings.Instance) { inst.UsersMayAddProviders = true })
		personal := postModelsForm(creatorClient, "/models/providers", url.Values{
			"name": {"Personal OpenRouter"}, "key": {"or-personal-create-secret"}, "scope": {"user"},
		})
		if personal.StatusCode != http.StatusSeeOther {
			t.Fatalf("personal create = %d: %.1500s", personal.StatusCode, personal.Body)
		}
		var ownerID string
		if err := ta.Deps.DB.Get(context.Background(), &ownerID, `SELECT owner_id FROM providers WHERE name = ?`, "Personal OpenRouter"); err != nil {
			t.Fatal(err)
		}
		if ownerID != creator.ID {
			t.Fatalf("personal owner = %q, want %q", ownerID, creator.ID)
		}

		forgedAfterEnable := postModelsForm(creatorClient, "/models/providers", url.Values{
			"name": {"Still forged"}, "key": {"or-still-forged-secret"}, "scope": {"server"},
		})
		if forgedAfterEnable.StatusCode != http.StatusForbidden {
			t.Fatalf("forged server scope after enable = %d", forgedAfterEnable.StatusCode)
		}
	})
}

func TestModelsLifecycleIDORAndLocalOnlyTestGuard(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		create := postModelsForm(admin, "/models/providers", url.Values{
			"name": {"Lifecycle OpenRouter"}, "key": {"or-old-lifecycle-secret"}, "scope": {"server"},
		})
		if create.StatusCode != http.StatusSeeOther {
			t.Fatalf("create = %d: %.1500s", create.StatusCode, create.Body)
		}
		id := providerIDByName(t, ta, "Lifecycle OpenRouter")

		disabled := postModelsForm(admin, "/models/providers/"+id+"/enabled", url.Values{"enabled": {"false"}})
		if disabled.StatusCode != http.StatusSeeOther {
			t.Fatalf("disable = %d: %.1500s", disabled.StatusCode, disabled.Body)
		}
		var enabled bool
		if err := ta.Deps.DB.Get(context.Background(), &enabled, `SELECT enabled FROM providers WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if enabled {
			t.Fatal("explicit false did not disable provider")
		}
		if got := postModelsForm(admin, "/models/providers/"+id+"/enabled", url.Values{"enabled": {"toggle"}}); got.StatusCode != http.StatusBadRequest {
			t.Fatalf("ambiguous enable value = %d", got.StatusCode)
		}

		var oldCipher string
		if err := ta.Deps.DB.Get(context.Background(), &oldCipher, `SELECT key_enc FROM providers WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		newSecret := "or-new-lifecycle-secret-never-render"
		replaced := postModelsForm(admin, "/models/providers/"+id+"/key", url.Values{"key": {newSecret}})
		if replaced.StatusCode != http.StatusSeeOther || strings.Contains(string(replaced.Body), newSecret) {
			t.Fatalf("replace = %d: %.1500s", replaced.StatusCode, replaced.Body)
		}
		var newCipher, lastTest string
		if err := ta.Deps.DB.QueryRow(context.Background(), `SELECT key_enc, last_test FROM providers WHERE id = ?`, id).Scan(&newCipher, &lastTest); err != nil {
			t.Fatal(err)
		}
		if newCipher == oldCipher || lastTest != "{}" {
			t.Fatalf("replacement ciphertext/test state = changed:%v last=%q", newCipher != oldCipher, lastTest)
		}

		if got := postModelsForm(admin, "/models/providers/"+id+"/enabled", url.Values{"enabled": {"true"}}); got.StatusCode != http.StatusSeeOther {
			t.Fatalf("enable = %d", got.StatusCode)
		}
		baseline := `{"state":"ok","request_id":"prior-safe-id"}`
		if _, err := ta.Deps.DB.Exec(context.Background(), `UPDATE providers SET last_test = ? WHERE id = ?`, baseline, id); err != nil {
			t.Fatal(err)
		}
		updateModelsInstance(t, ta, func(inst *settings.Instance) {
			inst.LocalOnly = true
			inst.RoutingMode = settings.RoutingLocalOnly
		})
		localPage := admin.Get("/models")
		if localPage.StatusCode != http.StatusOK || !strings.Contains(string(localPage.Body), "Local only is enabled") ||
			!strings.Contains(string(localPage.Body), `disabled`) {
			t.Fatalf("local-only page = %d: %.2500s", localPage.StatusCode, localPage.Body)
		}
		if got := postModelsForm(admin, "/models/providers/"+id+"/test", url.Values{}); got.StatusCode != http.StatusSeeOther {
			t.Fatalf("crafted local-only test = %d: %.1500s", got.StatusCode, got.Body)
		}
		var after string
		if err := ta.Deps.DB.Get(context.Background(), &after, `SELECT last_test FROM providers WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if after != baseline {
			t.Fatalf("local-only test mutated state: %q", after)
		}

		updateModelsInstance(t, ta, func(inst *settings.Instance) { inst.UsersMayAddProviders = true })
		_, ownerClient := ta.UserClient(t, auth.RoleCreator)
		ownerCreate := postModelsForm(ownerClient, "/models/providers", url.Values{
			"name": {"IDOR target"}, "key": {"or-idor-owner-secret"}, "scope": {"user"},
		})
		if ownerCreate.StatusCode != http.StatusSeeOther {
			t.Fatalf("owner create = %d: %.1200s", ownerCreate.StatusCode, ownerCreate.Body)
		}
		personalID := providerIDByName(t, ta, "IDOR target")
		_, attacker := ta.UserClient(t, auth.RoleCreator)
		for _, tc := range []struct {
			path string
			form url.Values
		}{
			{"/models/providers/" + personalID + "/test", url.Values{}},
			{"/models/providers/" + personalID + "/enabled", url.Values{"enabled": {"false"}}},
			{"/models/providers/" + personalID + "/key", url.Values{"key": {"or-attacker-secret"}}},
		} {
			got := postModelsForm(attacker, tc.path, tc.form)
			if got.StatusCode != http.StatusNotFound {
				t.Fatalf("cross-user %s = %d, want 404: %.1000s", tc.path, got.StatusCode, got.Body)
			}
		}
	})
}

func TestModelsOpenAIConnectionFlow(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		page := admin.Get("/models")
		if page.StatusCode != http.StatusOK || !strings.Contains(string(page.Body), `href="/models/providers/new?kind=openai"`) ||
			!strings.Contains(string(page.Body), "Connect OpenAI") {
			t.Fatalf("models page lacks the OpenAI action: %d", page.StatusCode)
		}
		form := admin.Get("/models/providers/new?kind=openai")
		if form.StatusCode != http.StatusOK || !strings.Contains(string(form.Body), `name="kind" value="openai"`) ||
			!strings.Contains(string(form.Body), "storage turned off") {
			t.Fatalf("OpenAI form = %d", form.StatusCode)
		}
		if got := admin.Get("/models/providers/new?kind=custom"); got.StatusCode != http.StatusNotFound {
			t.Fatalf("unknown kind form = %d, want 404", got.StatusCode)
		}
		if got := postModelsForm(admin, "/models/providers", url.Values{"kind": {"custom"}, "name": {"Elsewhere"},
			"key": {"sk-unknown-kind"}, "scope": {"server"}}); got.StatusCode != http.StatusNotFound {
			t.Fatalf("unknown kind create = %d, want 404", got.StatusCode)
		}

		secret := "sk-openai-form-secret-never-render"
		created := postModelsForm(admin, "/models/providers", url.Values{"kind": {"openai"}, "name": {"Studio OpenAI"},
			"key": {secret}, "scope": {"server"}})
		if created.StatusCode != http.StatusSeeOther {
			t.Fatalf("OpenAI create = %d: %.1500s", created.StatusCode, created.Body)
		}
		var kind string
		if err := ta.Deps.DB.Get(context.Background(), &kind, `SELECT kind FROM providers WHERE name = ?`, "Studio OpenAI"); err != nil {
			t.Fatal(err)
		}
		if kind != "openai" {
			t.Fatalf("stored kind = %q", kind)
		}
		listed := admin.Get("/models")
		body := string(listed.Body)
		if !strings.Contains(body, "Studio OpenAI") || !strings.Contains(body, "gpt-6-luna") ||
			!strings.Contains(body, "ti-brand-openai") || strings.Contains(body, secret) {
			t.Fatalf("OpenAI card missing or leaked its key")
		}
	})
}
