package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/dashboard"
)

func body(r *testutil.Response) string { return string(r.Body) }

func mustContain(t *testing.T, r *testutil.Response, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(body(r), p) {
			t.Fatalf("response %d does not contain %q:\n%.3000s", r.StatusCode, p, r.Body)
		}
	}
}

func TestRootRedirects(t *testing.T) {
	ta := testutil.NewTestApp(t)
	anon := ta.NewClient()
	if r := anon.Get("/"); r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/login" {
		t.Fatalf("private instance, anonymous: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	if _, err := ta.Deps.Settings.UpdateInstance(context.Background(), "", func(i *settings.Instance) error {
		i.Mode = settings.ModePublic
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if r := anon.Get("/"); r.Header.Get("Location") != "/explore" {
		t.Fatalf("public instance, anonymous: %q", r.Header.Get("Location"))
	}
	c := ta.Login(t, "admin")
	if r := c.Get("/"); r.Header.Get("Location") != "/studio" {
		t.Fatalf("signed in: %q", r.Header.Get("Location"))
	}

	fresh := testutil.NewTestApp(t, testutil.WithoutAdmin())
	if r := fresh.NewClient().Get("/"); r.Header.Get("Location") != "/setup" {
		t.Fatalf("no admin: %q", r.Header.Get("Location"))
	}
}

func TestDashboardEmptyStateAndSource(t *testing.T) {
	ta := testutil.NewTestApp(t)
	_, c := ta.UserClient(t, auth.RoleCreator)
	r := c.Get("/studio")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /studio: %d", r.StatusCode)
	}
	mustContain(t, r, "No projects yet", "シーン…", "Import CBZ / PDF", `class="mv-sidebar"`, `class="mv-bottomnav"`,
		"No GPU worker connected", "AGPL-3.0", `data-theme="dark"`)
	if strings.Contains(body(r), "style=") {
		t.Fatal("pages must not use inline style attributes (CSP)")
	}

	core.Provide[dashboard.Source](ta.Deps, fakeSource{})
	r = c.Get("/studio")
	mustContain(t, r, "Glass Orchard", "Recent projects", "Open editor")
}

type fakeSource struct{}

func (fakeSource) RecentProjects(ctx context.Context, userID string, n int) ([]dashboard.Project, error) {
	return []dashboard.Project{{ID: "p1", Title: "Glass Orchard", Href: "/studio/p/glass-orchard", Meta: "Ch. 3 · published",
		ThumbLabel: "PAGE THUMBNAIL", ChapterLine: "Ch. 3 · 12 of 30 pages", Progress: .4, EditorHref: "/studio/p/glass-orchard/chapters/3/edit",
		EditorLabel: "Open editor", PageLabel: "CH. 3 · PAGE 12"}}, nil
}

func TestComputeMeterShowsWorkerGPU(t *testing.T) {
	ta := testutil.NewTestApp(t)
	if _, err := ta.Deps.Jobs.RegisterWorker(context.Background(), "env", "10.0.0.2", jobs.Registration{Name: "Studio PC",
		GPUs: []jobs.GPU{{Name: "RTX 4090", VRAMTotalMB: 24576, VRAMFreeMB: 7880}}, MaxConcurrent: 1}); err != nil {
		t.Fatal(err)
	}
	c := ta.Login(t, "admin")
	r := c.Get("/studio")
	mustContain(t, r, "RTX 4090 · 16.3 / 24 GB", "Studio PC", "GPU idle", `id="mv-meter"`)
}

func TestErrorPages(t *testing.T) {
	ta := testutil.NewTestApp(t)
	c := ta.Login(t, "admin")
	r := c.Get("/no/such/page")
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("404 page: %d", r.StatusCode)
	}
	mustContain(t, r, "SHIIN…", "This page flew off the panel", `class="mv-state__outline"`)

	api := c.Get("/api/v1/no-such-endpoint")
	if api.StatusCode != http.StatusNotFound || api.ErrorCode() != "not_found" {
		t.Fatalf("API 404 must stay JSON: %d %s", api.StatusCode, api.Body)
	}

	// Maintenance mode: members see GARA GARA, admins still get in.
	if _, err := ta.Deps.Settings.UpdateInstance(context.Background(), "", func(i *settings.Instance) error {
		i.Maintenance = settings.Maintenance{Enabled: true, Message: "Installing 1.0.1."}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, member := ta.UserClient(t, auth.RoleCreator)
	m := member.Get("/studio")
	if m.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("maintenance: %d", m.StatusCode)
	}
	mustContain(t, m, "GARA GARA", "Installing 1.0.1.")
	if r := c.Get("/studio"); r.StatusCode != http.StatusOK {
		t.Fatalf("admin during maintenance: %d", r.StatusCode)
	}
}

func TestStaticAndPWA(t *testing.T) {
	ta := testutil.NewTestApp(t)
	c := ta.NewClient()
	r := c.Get("/static/brand/lockup-color-on-dark-gate-blue.svg")
	if r.StatusCode != http.StatusOK || !strings.Contains(r.Header.Get("Cache-Control"), "max-age") {
		t.Fatalf("static file: %d %q", r.StatusCode, r.Header.Get("Cache-Control"))
	}
	if r := c.Get("/static/"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("directory listing must be off: %d", r.StatusCode)
	}
	m := c.Get("/manifest.webmanifest")
	if m.StatusCode != http.StatusOK || !strings.Contains(m.Header.Get("Content-Type"), "manifest+json") {
		t.Fatalf("manifest: %d %s", m.StatusCode, m.Header.Get("Content-Type"))
	}
	mustContain(t, m, `"display":"standalone"`, "app-icon-maskable-512.png")
	sw := c.Get("/sw.js")
	if sw.Header.Get("Service-Worker-Allowed") != "/" {
		t.Fatalf("sw.js headers: %v", sw.Header)
	}
	off := c.Get("/offline")
	mustContain(t, off, "PUTSUN", "You’re offline")
	if strings.Contains(body(off), "data-user=") {
		t.Fatal("the cached offline page must not carry user data")
	}
	if f := c.Get("/favicon.ico"); f.StatusCode != http.StatusOK || f.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("favicon: %d", f.StatusCode)
	}
}

func TestStyleGuideAccess(t *testing.T) {
	ta := testutil.NewTestApp(t)
	if r := ta.NewClient().Get("/_design"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", r.StatusCode)
	}
	_, member := ta.UserClient(t, auth.RoleCreator)
	if r := member.Get("/_design"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("member: %d", r.StatusCode)
	}
	r := ta.Login(t, "admin").Get("/_design")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("admin: %d", r.StatusCode)
	}
	mustContain(t, r, `data-theme="dark"`, `data-theme="light"`, "Gate motifs", `data-island="demo"`, "mv-rank--s",
		"mv-gen--generating", "mv-modal", "mv-toast--error", "mv-ccard__kanji")

	dev := testutil.NewTestApp(t, testutil.WithEnv("MV_DEV", "1"))
	if r := dev.NewClient().Get("/_design"); r.StatusCode != http.StatusOK {
		t.Fatalf("MV_DEV=1 anonymous: %d", r.StatusCode)
	}
}

func TestFlashSurvivesRedirectOnce(t *testing.T) {
	ta := testutil.NewTestApp(t)
	c := ta.Login(t, "admin")
	// Log out: the flash is shown on the login page, then gone.
	r := c.Do(http.MethodPost, "/logout", strings.NewReader(url.Values{}.Encode()), "application/x-www-form-urlencoded")
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	p := c.Get("/login")
	mustContain(t, p, `data-toast=`, "You are logged out.")
	if again := c.Get("/login"); strings.Contains(body(again), "data-toast=") {
		t.Fatal("flash shown twice")
	}
}

func TestHTMXRequestsGetFragments(t *testing.T) {
	ta := testutil.NewTestApp(t)
	c := ta.Login(t, "admin")
	c.Header.Set("HX-Request", "true")
	r := c.Get("/settings/reading")
	if strings.Contains(body(r), "<html") || !strings.Contains(body(r), "mv-settings__head") {
		t.Fatalf("HTMX request must get the fragment only:\n%.500s", r.Body)
	}
	c.Header.Del("HX-Request")
}

func TestServerHost(t *testing.T) {
	cases := map[string][]string{
		"studio.local":     {"https://studio.local", "", "x"},
		"env.example:8080": {"", "http://env.example:8080", "x"},
		"req.host":         {"", "", "req.host"},
		"localhost":        {"", "", ""},
	}
	for want, in := range cases {
		if got := web.ServerHost(in...); got != want {
			t.Errorf("ServerHost(%q) = %q, want %q", in, got, want)
		}
	}
}
