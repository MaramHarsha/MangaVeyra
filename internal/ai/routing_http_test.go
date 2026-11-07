package ai_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// Routing controls are read-only for people who cannot change them, so no
// control on G11 leads to a 403 when used.
func TestRoutingIsReadOnlyForPeopleWhoCannotChangeIt(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		_, reader := ta.UserClient(t, auth.RoleReader)
		body := mainOf(t, reader.Get("/models/routing"))
		mustContain(t, body, "Only an admin can change the routing mode and guards.", `id="licence-guard" name="licence_guard" value="true" checked disabled`,
			`id="routing-limit-on" name="limit_on" value="true" checked disabled`, "Monthly remote spend limit · $20")
		mustNotContain(t, body, `action="/models/routing/pins"`, "data-autosave", "Save routing", `type="number"`)

		owner, ownerClient := ta.UserClient(t, auth.RoleCreator)
		if strings.Contains(mainOf(t, ownerClient.Get("/models/routing")), `action="/models/routing/pins"`) {
			t.Fatal("a creator without projects can open the pin form")
		}
		svc := core.MustUse[studio.Service](ta.Deps)
		project, err := svc.CreateProject(context.Background(), studio.Actor{UserID: owner.ID, SiteRole: auth.RoleCreator}, studio.NewProject{Title: "Lantern Harbour", Premise: "A courier crosses the flooded city.", Style: "seinen_bw", Format: "manga_rtl", Language: "en", PageSize: "b5", PageWidth: 1600, PageHeight: 2400})
		if err != nil {
			t.Fatal(err)
		}
		mustContain(t, mainOf(t, ownerClient.Get("/models/routing")), `action="/models/routing/pins"`)
		// A pin for someone else's project re-renders the page with a reason.
		other := postModelsForm(reader, "/models/routing/pins", url.Values{"scope": {"project"}, "scope_id": {project.ID}, "task": {"panel.generate"}, "model": {"placeholder-art"}})
		if other.StatusCode != http.StatusForbidden || !strings.Contains(string(other.Body), "You can pin models only for projects and characters you own.") {
			t.Fatalf("foreign pin = %d", other.StatusCode)
		}
		saved := postModelsForm(ownerClient, "/models/routing/pins", url.Values{"scope": {"project"}, "scope_id": {project.ID}, "task": {"panel.generate"}, "model": {"placeholder-art"}})
		if saved.StatusCode != http.StatusSeeOther {
			t.Fatalf("own pin = %d %s", saved.StatusCode, saved.Body)
		}
		mustContain(t, mainOf(t, ownerClient.Get("/models/routing")), "Panel art", "Placeholder art")

		// The designed guard copy, editable by the admin.
		admin := mainOf(t, ta.Login(t, ta.Admin.Username).Get("/models/routing"))
		mustContain(t, admin, "Monthly remote spend limit · $", `id="routing-limit" name="limit" type="number"`, `value="20"`, "data-autosave", "Save routing")
		mustNotContain(t, admin, "USD on server keys per user", "Only an admin can change")
	})
}

// The private-network guard on G11 is the live setting G12 uses. It defaults
// to MV_ALLOW_PRIVATE_PROVIDER_URLS and an unrelated routing save never
// freezes that default into a stored value.
func TestRoutingPrivateEndpointGuardIsTheLiveSetting(t *testing.T) {
	ta := testutil.NewTestApp(t, testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true"))
	admin := ta.Login(t, ta.Admin.Username)
	mustContain(t, mainOf(t, admin.Get("/models/routing")), "Private-network endpoints (local servers such as Ollama)",
		`id="private-endpoints" name="private_endpoints" value="true" checked`)
	keep := postModelsForm(admin, "/models/routing", url.Values{"mode": {"fastest"}, "private_endpoints": {"true"}, "limit_on": {"true"}, "limit": {"12.5"}})
	if keep.StatusCode != http.StatusSeeOther {
		t.Fatalf("routing save = %d %s", keep.StatusCode, keep.Body)
	}
	var stored int
	if err := ta.Deps.DB.Get(context.Background(), &stored, `SELECT COUNT(*) FROM instance_settings WHERE key = 'ai.private_endpoints'`); err != nil || stored != 0 {
		t.Fatalf("an unchanged guard was stored: %d %v", stored, err)
	}
	inst, err := ta.Deps.Settings.Instance(context.Background())
	if err != nil || inst.RoutingMode != "fastest" || inst.Limits.MonthlyRemoteSpendUSD != 12.5 || inst.Content.LicenceGuard {
		t.Fatalf("routing settings = %+v %v", inst, err)
	}
	off := postModelsForm(admin, "/models/routing", url.Values{"mode": {"fastest"}})
	if off.StatusCode != http.StatusSeeOther {
		t.Fatalf("routing save = %d", off.StatusCode)
	}
	if inst, _ := ta.Deps.Settings.Instance(context.Background()); inst.Limits.MonthlyRemoteSpendUSD != 0 {
		t.Fatalf("switching the spend limit off left %v", inst.Limits.MonthlyRemoteSpendUSD)
	}
	mustContain(t, mainOf(t, admin.Get("/models/providers/new/connect")), `id="custom-allow-private"`)
	invalid := postModelsForm(admin, "/models/routing", url.Values{"mode": {"fastest"}, "limit_on": {"true"}, "limit": {"-3"}})
	if invalid.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(invalid.Body), "Enter a monthly remote spend limit above $0") {
		t.Fatalf("invalid limit = %d", invalid.StatusCode)
	}
}
