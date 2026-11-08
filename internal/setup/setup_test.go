package setup_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/setup"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func post(c *testutil.Client, path string, v url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

func contains(t *testing.T, r *testutil.Response, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(string(r.Body), p) {
			t.Fatalf("response %d lacks %q:\n%.2500s", r.StatusCode, p, r.Body)
		}
	}
}

func location(t *testing.T, r *testutil.Response, want string) {
	t.Helper()
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != want {
		t.Fatalf("want redirect to %s, got %d %q\n%.1500s", want, r.StatusCode, r.Header.Get("Location"), r.Body)
	}
}

func TestWizardEndToEnd(t *testing.T) { testWizard(t, "sqlite") }

// TestWizardEndToEndPostgres runs the wizard on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestWizardEndToEndPostgres(t *testing.T) { testWizard(t, "postgres") }

func testWizard(t *testing.T, dialect string) {
	ta := testutil.NewTestApp(t, testutil.WithoutAdmin(), testutil.WithDialect(dialect))
	ctx := context.Background()
	c := ta.NewClient()

	location(t, c.Get("/studio"), "/setup")
	location(t, c.Get("/setup"), "/setup/admin")
	page := c.Get("/setup/admin")
	contains(t, page, "Your server,", "Create the admin account", "Telemetry: off", `aria-current="step"`)

	mismatch := post(c, "/setup/admin", url.Values{"username": {"jerry"}, "password": {"salt-road-lighthouse"}, "confirm": {"nope"}})
	if mismatch.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch: %d", mismatch.StatusCode)
	}
	if r := post(c, "/setup/admin", url.Values{"username": {"a"}, "password": {"salt-road-lighthouse"}, "confirm": {"salt-road-lighthouse"}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad username: %d", r.StatusCode)
	}
	location(t, post(c, "/setup/admin", url.Values{"display_name": {"Jerry Okafor"}, "username": {"jerry"}, "email": {"jerry@example.org"},
		"password": {"salt-road-lighthouse"}, "confirm": {"salt-road-lighthouse"}}), "/setup/instance")
	if ok, _ := ta.Deps.Auth.HasAdmin(ctx); !ok {
		t.Fatal("admin not created")
	}
	// Nobody else can create a second first admin.
	other := ta.NewClient()
	if r := post(other, "/setup/admin", url.Values{"username": {"mallory"}, "password": {"salt-road-lighthouse"}, "confirm": {"salt-road-lighthouse"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("second admin: %d", r.StatusCode)
	}
	if r := other.Get("/setup/instance"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous step 2: %d", r.StatusCode)
	}

	contains(t, c.Get("/setup/instance"), "Name your server", `name="mode"`, "Invite only", "Telemetry")
	bad := post(c, "/setup/instance", url.Values{"name": {"Salt Studio"}, "public_url": {"ftp://x"}, "mode": {"public"}, "sign_up": {"open"}})
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad url: %d", bad.StatusCode)
	}
	location(t, post(c, "/setup/instance", url.Values{"name": {"Salt Studio"}, "public_url": {"https://studio.example.org/"}, "mode": {"public"},
		"sign_up": {"open"}}), "/setup/models")
	inst := ta.Deps.Settings.MustInstance(ctx)
	if inst.Name != "Salt Studio" || inst.PublicURL != "https://studio.example.org" || inst.Mode != settings.ModePublic || inst.SignUp != settings.SignUpOpen || inst.Telemetry {
		t.Fatalf("instance: %+v", inst)
	}

	models := c.Get("/setup/models")
	contains(t, models, "Connect at least one image model", "No GPU worker connected yet", "OpenRouter",
		"One key for 30+ image models and text models", `name="local_only"`)
	// Pages this server does not serve render as Soon controls, never as 404 links.
	if body := string(models.Body); strings.Contains(body, `href="/models/workers/new"`) || strings.Contains(body, `href="/models/local`) {
		t.Fatal("setup models step links to a page the server does not serve")
	}
	location(t, post(c, "/setup/models", url.Values{"local_only": {"on"}}), "/setup/finish")
	inst = ta.Deps.Settings.MustInstance(ctx)
	if !inst.LocalOnly || inst.RoutingMode != settings.RoutingLocalOnly {
		t.Fatalf("local only: %+v", inst)
	}
	localModels := c.Get("/setup/models")
	contains(t, localModels, "OpenRouter", "Connect", "disabled")
	if strings.Contains(string(localModels.Body), `href="/models/providers/new?kind=openrouter`) {
		t.Fatal("local-only provider control remains navigable")
	}
	// HTMX auto-save of the toggle.
	c.Header.Set("HX-Request", "true")
	r := post(c, "/setup/models", url.Values{"autosave": {"1"}})
	c.Header.Del("HX-Request")
	if r.StatusCode != http.StatusNoContent || !strings.Contains(r.Header.Get("HX-Trigger"), "mv:toast") {
		t.Fatalf("autosave: %d %v", r.StatusCode, r.Header)
	}
	if ta.Deps.Settings.MustInstance(ctx).LocalOnly {
		t.Fatal("local only should be off again")
	}

	fin := c.Get("/setup/finish")
	contains(t, fin, "Check &amp; finish", "Database", dialect, "Image workers", "Telemetry", "Off. Nothing is sent anywhere.")
	location(t, post(c, "/setup/finish", nil), "/studio")
	if ta.Deps.Settings.MustInstance(ctx).SetupCompletedAt == nil {
		t.Fatal("setup not completed")
	}
	location(t, c.Get("/setup"), "/studio")
	if r := c.Get("/studio"); r.StatusCode != http.StatusOK {
		t.Fatalf("studio after setup: %d", r.StatusCode)
	}
}

func TestStepsNeedAnAdmin(t *testing.T) {
	ta := testutil.NewTestApp(t)
	_, member := ta.UserClient(t, auth.RoleCreator)
	if r := member.Get("/setup/instance"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member: %d", r.StatusCode)
	}
	location(t, member.Get("/setup"), "/studio")
	location(t, ta.NewClient().Get("/setup/admin"), "/setup")
}

func TestModelsStepDetectsGPUAndUsesHook(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	if _, err := ta.Deps.Jobs.RegisterWorker(ctx, "env", "10.0.0.9", jobs.Registration{Name: "Studio PC",
		GPUs: []jobs.GPU{{Name: "NVIDIA RTX 4090", VRAMTotalMB: 24576, VRAMFreeMB: 20000}}, MaxConcurrent: 1}); err != nil {
		t.Fatal(err)
	}
	c := ta.Login(t, "admin")
	r := c.Get("/setup/models")
	contains(t, r, "NVIDIA RTX 4090 · 24 GB detected", "Qwen-Image Edit, FLUX.2 [klein], Illustrious XL · 41 GB download",
		"Install recommended", "OpenRouter", "Connect")
	location(t, post(c, "/models/providers", url.Values{"name": {"Setup OpenRouter"}, "key": {"sk-or-v1-setup-test"},
		"scope": {"server"}}), "/models")
	r = c.Get("/setup/models")
	contains(t, r, "OpenRouter", "Connected")
}

func TestRecommendForAndURLValidation(t *testing.T) {
	if setup.RecommendFor(nil) != nil {
		t.Fatal("no GPU, no recommendation")
	}
	if r := setup.RecommendFor([]jobs.GPU{{VRAMTotalMB: 12 * 1024}}); r == nil || len(r.Models) != 2 {
		t.Fatalf("12 GB: %+v", r)
	}
	for in, ok := range map[string]bool{"": true, "https://a.example": true, "http://10.0.0.2:8080/": true,
		"ftp://a": false, "https://u:p@a.example": false, "https://a.example/?x=1": false, "a.example": false} {
		if _, got := setup.ValidatePublicURL(in); got != ok {
			t.Errorf("ValidatePublicURL(%q) = %v", in, got)
		}
	}
}
