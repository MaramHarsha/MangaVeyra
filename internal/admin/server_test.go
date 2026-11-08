package admin_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// The Instance card saves name, description, public URL and mode; the
// Services card shows the real database, storage, email, workers and queue.
func TestServerTab(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		page := e.admin.Get("/admin/server")
		wantStatus(t, "server tab", page, http.StatusOK)
		dialect := "SQLite"
		if e.ta.Deps.DB.Dialect() == "postgres" {
			dialect = "PostgreSQL"
		}
		wantBody(t, "services", page, dialect+" ", " · OK · ", "Local disk · ", "Not set up", "None online", "0 queued · 0 running",
			"Off · nothing leaves the server", `id="telemetry" name="" value="on" disabled`)
		// Backups (G10) and updates (G14) belong to other modules: Soon until they exist.
		wantBody(t, "maintenance card", page, "Backups", "Update settings", "View logs")

		form := url.Values{"name": {"Harbour Ghosts"}, "description": {"A small group making manga."}, "public_url": {"https://studio.example.org/"}, "mode": {"public"}}
		wantSaved(t, "instance", postForm(e.admin, "/admin/server", form), "/admin/server")
		inst := e.instance()
		if inst.Name != "Harbour Ghosts" || inst.Description != "A small group making manga." || inst.PublicURL != "https://studio.example.org" || inst.Mode != settings.ModePublic {
			t.Fatalf("instance %+v", inst)
		}
		if e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'admin.settings.instance'`) != 1 {
			t.Fatal("instance change not audited")
		}
		for field, value := range map[string]string{"name": "", "public_url": "ftp://studio.example.org", "mode": "secret"} {
			bad := url.Values{"name": {"Harbour Ghosts"}, "public_url": {""}, "mode": {"private"}}
			bad.Set(field, value)
			r := postForm(e.admin, "/admin/server", bad)
			wantStatus(t, "bad "+field, r, http.StatusUnprocessableEntity)
		}
		if e.instance().Mode != settings.ModePublic {
			t.Fatal("a refused form changed the mode")
		}
	})
}

// Maintenance mode: members get the G6 page with the admin's message, admins
// still get in, and turning it off brings members back.
func TestMaintenanceToggle(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		_, memberC := e.user(auth.RoleCreator)
		dialog := e.admin.Get("/admin/server?dialog=maintenance")
		wantBody(t, "maintenance dialog", dialog, `id="maintenance-dialog"`, `mv-modal--static`, `name="enabled" value="1"`, "Turn on maintenance mode")

		long := postForm(e.admin, "/admin/maintenance", url.Values{"enabled": {"1"}, "message": {strings.Repeat("x", 501)}})
		wantStatus(t, "long message", long, http.StatusUnprocessableEntity)

		msg := "Moving to a bigger disk. Back in ten minutes."
		wantSaved(t, "on", postForm(e.admin, "/admin/maintenance", url.Values{"enabled": {"1"}, "message": {msg}}), "/admin/server")
		inst := e.instance()
		if !inst.Maintenance.Enabled || inst.Maintenance.Message != msg {
			t.Fatalf("maintenance %+v", inst.Maintenance)
		}
		r := memberC.Get("/studio")
		wantStatus(t, "member during maintenance", r, http.StatusServiceUnavailable)
		wantBody(t, "G6", r, msg, "GARA GARA")
		wantStatus(t, "admin during maintenance", e.admin.Get("/admin/server"), http.StatusOK)
		wantBody(t, "admin sees the state", e.admin.Get("/admin/server"), "Maintenance mode is on", "Turn off maintenance mode")

		wantSaved(t, "off", postForm(e.admin, "/admin/maintenance", url.Values{"enabled": {"0"}, "message": {msg}}), "/admin/server")
		if e.instance().Maintenance.Enabled {
			t.Fatal("maintenance still on")
		}
		wantStatus(t, "member after maintenance", memberC.Get("/studio"), http.StatusOK)
		if e.count(`SELECT COUNT(*) FROM audit_log WHERE action IN ('admin.maintenance.on', 'admin.maintenance.off')`) != 2 {
			t.Fatal("maintenance changes not audited")
		}
	})
}

// Every G5 form works without JavaScript: a plain POST answers 303 back to its
// tab, and auto-saving cards keep a Save button for browsers without it.
func TestNoJavaScriptForms(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		for path, parts := range map[string][]string{
			"/admin/users":  {`action="/admin/signup"`, `action="/admin/limits"`, `class="mv-nojs-only`, `data-autosave`, `data-autosubmit`, `href="/admin/users?dialog=invite#invite-dialog"`},
			"/admin/rules":  {`action="/admin/rules"`, `action="/admin/rules/words"`, `<details class="mv-admin__words"`, `class="mv-nojs-only"`},
			"/admin/server": {`action="/admin/server"`, `href="/admin/server?dialog=maintenance#maintenance-dialog"`, `class="mv-nojs-only"`},
		} {
			wantBody(t, path, e.admin.Get(path), parts...)
		}
		r := postForm(e.admin, "/admin/signup", url.Values{"sign_up": {"invite"}})
		if r.Header.Get("HX-Redirect") != "" {
			t.Fatal("plain form answered with an HTMX redirect")
		}
		wantSaved(t, "plain form", r, "/admin/users")
		wantBody(t, "saved toast", e.admin.Get(r.Header.Get("Location")), "Saved")
	})
}
