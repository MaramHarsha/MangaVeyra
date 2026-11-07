package ai_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
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
		if page.StatusCode != http.StatusOK || !strings.Contains(string(page.Body), "placeholder-text") ||
			!strings.Contains(string(page.Body), `href="/models/providers/new?kind=openrouter"`) {
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
			!strings.Contains(string(page.Body), "<span>OpenAI</span>") {
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
		if !strings.Contains(body, "OpenAI · Studio OpenAI") || !strings.Contains(body, "gpt-6-luna") ||
			!strings.Contains(body, "ti-brand-openai") || strings.Contains(body, secret) {
			t.Fatalf("OpenAI row missing or leaked its key")
		}
	})
}

func mustContain(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("page lacks %q", w)
		}
	}
}

func mustNotContain(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(body, w) {
			t.Errorf("page contains %q", w)
		}
	}
}

// mustAppearInOrder checks that each string occurs after the previous one.
func mustAppearInOrder(t *testing.T, body string, want ...string) {
	t.Helper()
	at := 0
	for _, w := range want {
		i := strings.Index(body[at:], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order (searching from %d)", w, at)
		}
		at += i + len(w)
	}
}

// mainOf returns the page's <main> element (skipping the app chrome).
func mainOf(t *testing.T, r *testutil.Response) string {
	t.Helper()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %.1500s", r.StatusCode, r.Body)
	}
	body := string(r.Body)
	start, end := strings.Index(body, "<main"), strings.Index(body, "</main>")
	if start < 0 || end < start {
		t.Fatalf("no <main> in %.1500s", body)
	}
	return body[start:end]
}

var (
	hrefRe = regexp.MustCompile(`href="(/[^"#]*)"`)
	formRe = regexp.MustCompile(`<form[^>]*>`)
	attrRe = regexp.MustCompile(`\b(method|action)="([^"]*)"`)
)

// assertEveryTargetIsServed fails when a link or form in body leads to a
// path the app does not serve (ADR 0004 §7: no link to a 404). Callers pass
// the page content; the app chrome is owned and tested by the web layer.
func assertEveryTargetIsServed(t *testing.T, ta *testutil.TestApp, body string) {
	t.Helper()
	for _, m := range hrefRe.FindAllStringSubmatch(body, -1) {
		path := strings.SplitN(strings.ReplaceAll(m[1], "&amp;", "&"), "?", 2)[0]
		if !ta.Router.Serves(http.MethodGet, path) {
			t.Errorf("link to unserved path %q", m[1])
		}
	}
	for _, form := range formRe.FindAllString(body, -1) {
		method, action := http.MethodGet, ""
		for _, a := range attrRe.FindAllStringSubmatch(form, -1) {
			if a[1] == "method" {
				method = strings.ToUpper(a[2])
			} else {
				action = a[2]
			}
		}
		if method == "DIALOG" || action == "" {
			continue
		}
		if !ta.Router.Serves(method, strings.SplitN(action, "?", 2)[0]) {
			t.Errorf("form %s %q is not served", method, action)
		}
	}
}

func TestModelsPageMatchesDesignedLayout(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		r := admin.Get("/models")
		body := mainOf(t, r)
		mustContain(t, string(r.Body), `<span class="mv-topbar__title" aria-hidden="true">Models</span>`,
			`<a class="mv-iconlink mv-topbar__back" href="/settings"`)

		// Header: designed title, subtitle with the server name and both actions.
		mustContain(t, body, `<h1 class="mv-pagehead__title">Models &amp; providers</h1>`,
			"Everything auto-routing can use on 127.0.0.1")
		mustAppearInOrder(t, body, "Add custom", "Add provider")

		// Summary tiles in design order with real status.
		mustAppearInOrder(t, body, `<ul class="mv-models__tiles`,
			"<strong>Local worker</strong> <span>No worker connected</span>", "mv-models-tile__dot--off",
			"<strong>OpenRouter</strong> <span>Not connected</span>", "mv-models-tile__dot--off",
			`<li class="mv-models-tile is-soon"`, "<strong>fal</strong>", `<span class="mv-soon">Soon</span>`,
			`<li class="mv-models-tile is-soon"`, "<strong>Ollama</strong>", `<span class="mv-soon">Soon</span>`, "</ul>")

		// Capability tabs: text is the default and the only populated one.
		mustAppearInOrder(t, body, `<nav class="mv-seg" aria-label="Capabilities">`,
			`href="/models?cap=image">Image 0</a>`, `href="/models?cap=text" aria-current="true">Text 1</a>`,
			`href="/models?cap=video">Video 0</a>`, `href="/models?cap=voice">Voice 0</a>`, `href="/models?cap=ocr">OCR 0</a>`)

		// The designed table with the built-in placeholder row.
		mustAppearInOrder(t, body, `<table class="mv-table mv-models-table">`,
			">Model</th>", ">Provider</th>", ">Router uses it for</th>", ">Status</th>", ">Cost</th>", ">On</th>",
			`id="provider-placeholder"`, "<strong>placeholder-text</strong>", "Built-in", "Scripts · fallback",
			`<span class="mv-badge mv-badge--success">Ready</span>`, "None",
			`id="enabled-placeholder" name="" value="on" checked disabled`)
		mustNotContain(t, body, "Local only is enabled")
		assertEveryTargetIsServed(t, ta, body)
	})
}

func TestModelsPageSoonItemsNeverLink(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		body := mainOf(t, ta.Login(t, ta.Admin.Username).Get("/models"))
		for _, path := range []string{"/models/providers/custom", "/models/providers/new/fal", "/models/providers/new/google",
			"/models/providers/new/xai", "/models/providers/new/ollama", "/models/routing"} {
			if ta.Router.Serves(http.MethodGet, path) {
				t.Fatalf("%s is served now; update the Soon expectations", path)
			}
			mustNotContain(t, body, `href="`+path)
		}
		// "Add custom" is a disabled button with the Soon tag.
		mustContain(t, body, `<a class="mv-btn mv-btn--quiet is-soon" aria-disabled="true"`,
			`Add custom<span class="mv-soon">Soon</span>`)
		// "Add provider" lists OpenRouter and OpenAI as links, the rest as Soon.
		mustAppearInOrder(t, body, `<details class="mv-dropdown mv-dropdown--end mv-models__add">`,
			`href="/models/providers/new?kind=openrouter" role="menuitem"`, "<span>OpenRouter</span>",
			`class="mv-menu__item is-soon"`, "<span>fal</span>", `<span class="mv-soon">Soon</span>`,
			`href="/models/providers/new?kind=openai" role="menuitem"`, "<span>OpenAI</span>",
			`class="mv-menu__item is-soon"`, "<span>Google</span>", `<span class="mv-soon">Soon</span>`,
			`class="mv-menu__item is-soon"`, "<span>xAI</span>", `<span class="mv-soon">Soon</span>`,
			`class="mv-menu__item is-soon"`, "<span>Ollama</span>", `<span class="mv-soon">Soon</span>`, "</details>")

		// Empty capability tabs use the designed empty state with a Soon tag.
		for capability, title := range map[string]string{"image": "No image models yet", "video": "No video models yet",
			"voice": "No voice models yet", "ocr": "No OCR models yet"} {
			tab := mainOf(t, ta.Login(t, ta.Admin.Username).Get("/models?cap="+capability))
			mustContain(t, tab, `href="/models?cap=`+capability+`" aria-current="true"`, "シーン…", title,
				"This version routes text only.", `<span class="mv-soon">Soon</span>`)
			mustNotContain(t, tab, "<table", "placeholder-text")
		}
		// Unknown tabs fall back to text.
		mustContain(t, mainOf(t, ta.Login(t, ta.Admin.Username).Get("/models?cap=bogus")),
			`href="/models?cap=text" aria-current="true">Text 1</a>`, "placeholder-text")
	})
}

func TestModelsPageRowsActionsAndSecrets(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		orSecret, oaSecret := "or-table-secret-never-render-1234", "sk-openai-table-secret-never-render-5678"
		for _, form := range []url.Values{
			{"kind": {"openrouter"}, "name": {"Studio OR"}, "key": {orSecret}, "scope": {"server"}},
			{"kind": {"openai"}, "name": {"Studio OpenAI"}, "key": {oaSecret}, "scope": {"server"}},
		} {
			if got := postModelsForm(admin, "/models/providers", form); got.StatusCode != http.StatusSeeOther {
				t.Fatalf("create %v = %d: %.1000s", form["name"], got.StatusCode, got.Body)
			}
		}
		orID, oaID := providerIDByName(t, ta, "Studio OR"), providerIDByName(t, ta, "Studio OpenAI")
		if _, err := ta.Deps.DB.Exec(context.Background(), `UPDATE providers SET last_test = ? WHERE id = ?`,
			`{"state":"ok","request_id":"req-safe-1"}`, orID); err != nil {
			t.Fatal(err)
		}

		r := admin.Get("/models")
		body := mainOf(t, r)
		mustContain(t, body, `href="/models?cap=text" aria-current="true">Text 3</a>`,
			"<strong>OpenRouter</strong> <span>1 text model</span>", "mv-models-tile__dot--on")
		mustAppearInOrder(t, body, `id="provider-placeholder"`,
			`<tbody class="mv-models-table__group" id="provider-`+orID+`">`, "<strong>openai/gpt-4.1-mini</strong>",
			"OpenRouter · Studio OR", "Server", "Scripts", `title="Last check request ID: req-safe-1"`,
			`<span class="mv-badge mv-badge--success">Ready</span>`, "≤ $0.45 / call",
			`<tbody class="mv-models-table__group" id="provider-`+oaID+`">`, "<strong>gpt-6-luna</strong>",
			"OpenAI · Studio OpenAI", "Scripts", `<span class="mv-badge mv-badge--muted">Not checked</span>`, "≤ $0.04 / call")

		// The ON switch posts to the existing enabled route; the checkbox comes
		// first so its value wins over the hidden "false".
		enabledPath := "/models/providers/" + orID + "/enabled"
		mustAppearInOrder(t, body, `<form class="mv-models-table__toggle" method="post" action="`+enabledPath+`"`,
			`hx-post="`+enabledPath+`" hx-trigger="change" hx-target="#provider-`+orID+`" hx-swap="outerHTML"`,
			`id="enabled-`+orID+`" name="enabled" value="true" checked aria-label="Use Studio OR"`,
			`<input type="hidden" name="enabled" value="false">`, "<noscript>", ">Save</button>", "</noscript>")
		// Row actions: test connection (existing POST) and the replace-key dialog (existing POST).
		mustAppearInOrder(t, body, `<span class="mv-sr-only">Actions for Studio OR</span>`,
			`<form method="post" action="/models/providers/`+orID+`/test">`,
			`hx-post="/models/providers/`+orID+`/test"`, "Test connection",
			`href="#replace-key-`+orID+`" role="menuitem" data-dialog-open="replace-key-`+orID+`"`, "Replace key",
			`<dialog class="mv-modal mv-models-keydlg" id="replace-key-`+orID+`"`,
			`<form method="post" action="/models/providers/`+orID+`/key"`, `name="key"`, `type="password"`)
		assertEveryTargetIsServed(t, ta, body)

		// No key or ciphertext reaches the page.
		var orCipher, oaCipher string
		if err := ta.Deps.DB.Get(context.Background(), &orCipher, `SELECT key_enc FROM providers WHERE id = ?`, orID); err != nil {
			t.Fatal(err)
		}
		if err := ta.Deps.DB.Get(context.Background(), &oaCipher, `SELECT key_enc FROM providers WHERE id = ?`, oaID); err != nil {
			t.Fatal(err)
		}
		mustNotContain(t, string(r.Body), orSecret, oaSecret, orCipher, oaCipher, "key_enc")

		// Without JavaScript the switch submits checkbox-then-hidden values.
		off := postModelsForm(admin, enabledPath, url.Values{"enabled": {"false"}})
		if off.StatusCode != http.StatusSeeOther || off.Header.Get("Location") != "/models" {
			t.Fatalf("switch off = %d %q", off.StatusCode, off.Header.Get("Location"))
		}
		mustContain(t, mainOf(t, admin.Get("/models")), `id="enabled-`+orID+`" name="enabled" value="true" aria-label`,
			`<span class="mv-badge mv-badge--muted">Disabled</span>`)
		on := postModelsForm(admin, enabledPath, url.Values{"enabled": {"true", "false"}})
		if on.StatusCode != http.StatusSeeOther {
			t.Fatalf("switch on = %d", on.StatusCode)
		}
		var enabled bool
		if err := ta.Deps.DB.Get(context.Background(), &enabled, `SELECT enabled FROM providers WHERE id = ?`, orID); err != nil {
			t.Fatal(err)
		}
		if !enabled {
			t.Fatal("checkbox value did not win over the hidden false")
		}

		// HTMX swaps exactly this connection's rows and keeps the result visible.
		admin.Header.Set("HX-Request", "true")
		swapped := postModelsForm(admin, enabledPath, url.Values{"enabled": {"false"}})
		admin.Header.Del("HX-Request")
		frag := string(swapped.Body)
		if swapped.StatusCode != http.StatusOK || !strings.HasPrefix(frag, `<tbody class="mv-models-table__group" id="provider-`+orID+`">`) ||
			strings.Contains(frag, "<html") {
			t.Fatalf("HTMX toggle = %d: %.1500s", swapped.StatusCode, frag)
		}
		mustAppearInOrder(t, frag, `<span class="mv-badge mv-badge--muted">Disabled</span>`,
			`<tr class="mv-models-table__notice">`, "Provider disabled", "</tbody>")
		mustNotContain(t, frag, orSecret, orCipher)

		newSecret := "or-replaced-secret-never-render-9999"
		admin.Header.Set("HX-Request", "true")
		replaced := postModelsForm(admin, "/models/providers/"+orID+"/key", url.Values{"key": {newSecret}})
		invalid := postModelsForm(admin, "/models/providers/"+orID+"/key", url.Values{"key": {" "}})
		admin.Header.Del("HX-Request")
		mustContain(t, string(replaced.Body), `id="provider-`+orID+`"`, "Provider key replaced",
			`<span class="mv-badge mv-badge--muted">Disabled</span>`)
		if invalid.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("HTMX empty key = %d", invalid.StatusCode)
		}
		mustContain(t, string(invalid.Body), `id="provider-`+orID+`"`, "Enter a valid API key")
		var replacedCipher string
		if err := ta.Deps.DB.Get(context.Background(), &replacedCipher, `SELECT key_enc FROM providers WHERE id = ?`, orID); err != nil {
			t.Fatal(err)
		}
		for _, b := range []string{string(replaced.Body), string(invalid.Body), string(admin.Get("/models").Body)} {
			mustNotContain(t, b, newSecret, replacedCipher, orCipher)
		}
	})
}

func TestModelsPageLocalOnlyWorkerAndPermissions(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		if _, err := ta.Deps.Jobs.RegisterWorker(ctx, "env", "10.0.0.9", jobs.Registration{Name: "Studio PC",
			GPUs: []jobs.GPU{{Name: "NVIDIA RTX 4090", VRAMTotalMB: 24576, VRAMFreeMB: 20000}}, MaxConcurrent: 1}); err != nil {
			t.Fatal(err)
		}
		admin := ta.Login(t, ta.Admin.Username)
		if got := postModelsForm(admin, "/models/providers", url.Values{"name": {"Guarded OR"}, "key": {"or-guarded-secret"},
			"scope": {"server"}}); got.StatusCode != http.StatusSeeOther {
			t.Fatalf("create = %d", got.StatusCode)
		}
		id := providerIDByName(t, ta, "Guarded OR")
		updateModelsInstance(t, ta, func(inst *settings.Instance) {
			inst.LocalOnly = true
			inst.RoutingMode = settings.RoutingLocalOnly
			inst.Limits.MonthlyRemoteSpendUSD = 0
		})

		body := mainOf(t, admin.Get("/models"))
		mustContain(t, body, "<strong>Local worker</strong> <span>NVIDIA RTX 4090</span>",
			"<strong>OpenRouter</strong> <span>Paused by Local only</span>", "Local only is enabled")
		mustAppearInOrder(t, body, "<strong>Local worker</strong>", "mv-models-tile__dot--on", "<strong>OpenRouter</strong>",
			"mv-models-tile__dot--off")
		// Testing is disabled while Local only is on.
		mustContain(t, body, `role="menuitem" disabled hx-post="/models/providers/`+id+`/test"`)

		// A crafted HTMX test still makes no request and shows its result in the row.
		admin.Header.Set("HX-Request", "true")
		tested := postModelsForm(admin, "/models/providers/"+id+"/test", url.Values{})
		admin.Header.Del("HX-Request")
		mustContain(t, string(tested.Body), `id="provider-`+id+`"`, `<tr class="mv-models-table__notice">`,
			"Connection check failed")

		// A creator who may not add providers sees no add links.
		_, creator := ta.UserClient(t, auth.RoleCreator)
		creatorBody := mainOf(t, creator.Get("/models"))
		mustContain(t, creatorBody, `data-tip="Only an admin can add providers on this server."`, "Add provider",
			`href="/models?cap=text" aria-current="true">Text 1</a>`)
		mustNotContain(t, creatorBody, `href="/models/providers/new`, "Guarded OR")
	})
}

func TestSetupStepThreeShowsDesignedProviderRows(t *testing.T) {
	ta := testutil.NewTestApp(t)
	admin := ta.Login(t, ta.Admin.Username)
	r := admin.Get("/setup/models")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("setup models = %d", r.StatusCode)
	}
	body := string(r.Body)
	mustAppearInOrder(t, body, `<div class="mv-providers">`,
		"OpenRouter", `href="/models/providers/new?kind=openrouter&amp;next=/setup/models"`,
		">fal<", "Fast hosted open models", `is-soon`, `<span class="mv-soon">Soon</span>`,
		"OpenAI · Google · xAI", "Direct vendor keys", `href="/models/providers/new?kind=openai&amp;next=/setup/models"`,
		"Ollama · local text model", "For script, translation and routing", `is-soon`, `<span class="mv-soon">Soon</span>`,
		"Custom connection", "OpenAI-compatible, ComfyUI workflow, any HTTP API", `is-soon`, `<span class="mv-soon">Soon</span>`)
	mustNotContain(t, body, `href="/models/providers/new/fal`, `href="/models/providers/new/ollama`, `href="/models/providers/custom`)

	if got := postModelsForm(admin, "/models/providers", url.Values{"kind": {"openai"}, "name": {"Setup vendor"},
		"key": {"sk-setup-vendor-secret"}, "scope": {"server"}, "next": {"/setup/models"}}); got.StatusCode != http.StatusSeeOther ||
		got.Header.Get("Location") != "/setup/models" {
		t.Fatalf("vendor connect = %d %q", got.StatusCode, got.Header.Get("Location"))
	}
	mustAppearInOrder(t, string(admin.Get("/setup/models").Body), "OpenAI · Google · xAI", "Connected", "Ollama · local text model")
}
