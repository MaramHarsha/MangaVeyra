package account_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/account"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func post(c *testutil.Client, path string, v url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

func htmx(c *testutil.Client, path string, v url.Values) *testutil.Response {
	c.Header.Set("HX-Request", "true")
	defer c.Header.Del("HX-Request")
	return post(c, path, v)
}

func contains(t *testing.T, r *testutil.Response, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(string(r.Body), p) {
			t.Fatalf("response %d lacks %q:\n%.2500s", r.StatusCode, p, r.Body)
		}
	}
}

func TestSectionsRender(t *testing.T) {
	ta := testutil.NewTestApp(t)
	_, c := ta.UserClient(t, auth.RoleCreator)
	for path, want := range map[string]string{
		"/settings":               "Your login on",
		"/settings/account":       "Two-step login",
		"/settings/appearance":    "Gate night",
		"/settings/language":      "Reading languages",
		"/settings/reading":       "Tap zones",
		"/settings/notifications": "Generation finished or failed",
		"/settings/accessibility": "Interface text size",
		"/settings/world":         "World is optional",
		"/settings/data":          "Export everything",
	} {
		r := c.Get(path)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", path, r.StatusCode)
		}
		contains(t, r, want, `class="mv-settings__nav"`)
	}
	if r := ta.NewClient().Get("/settings/appearance"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", r.StatusCode)
	}
	contains(t, c.Get("/settings"), "mv-settings--index")
}

func TestAppearanceAppliesInstantly(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u, c := ta.UserClient(t, auth.RoleCreator)
	ctx := context.Background()

	r := htmx(c, "/settings/appearance", url.Values{"theme": {"light"}, "reduced_motion": {"on"}, "textures": {"on"}, "show_ranks_xp": {"on"}})
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("htmx save: %d", r.StatusCode)
	}
	trig := r.Header.Get("HX-Trigger")
	for _, want := range []string{`"mv:prefs"`, `"theme":"light"`, `"motion":"on"`, `"mv:toast"`} {
		if !strings.Contains(trig, want) {
			t.Fatalf("HX-Trigger %s lacks %s", trig, want)
		}
	}
	p, _ := ta.Deps.Settings.UserPrefs(ctx, u.ID)
	if p.Theme != "light" || p.ReducedMotion != "on" || p.LivingPanels || !p.ShowRanksXP {
		t.Fatalf("prefs: %+v", p)
	}
	page := c.Get("/studio")
	contains(t, page, `data-theme="light"`, `data-motion="reduce"`)

	// Plain form post (no JavaScript): redirect back with a flash.
	r = post(c, "/settings/appearance", url.Values{"theme": {"dark"}, "textures": {"on"}})
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/settings/appearance" {
		t.Fatalf("form save: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	contains(t, c.Get("/settings/appearance"), `data-theme="dark"`, "Saved")
}

func TestThemeToggle(t *testing.T) {
	ta := testutil.NewTestApp(t)
	anon := ta.NewClient()
	r := htmx(anon, "/settings/theme", url.Values{"theme": {"light"}})
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("visitor toggle: %d", r.StatusCode)
	}
	contains(t, anon.Get("/login"), `data-theme="light"`)
	if r := htmx(anon, "/settings/theme", url.Values{"theme": {"neon"}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad theme: %d", r.StatusCode)
	}
	u, c := ta.UserClient(t, auth.RoleCreator)
	htmx(c, "/settings/theme", url.Values{"theme": {"system"}})
	if p, _ := ta.Deps.Settings.UserPrefs(context.Background(), u.ID); p.Theme != "system" {
		t.Fatalf("member theme: %s", p.Theme)
	}
}

func TestAccessibilityAndReading(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u, c := ta.UserClient(t, auth.RoleCreator)
	ctx := context.Background()
	htmx(c, "/settings/accessibility", url.Values{"text_size": {"128"}, "high_contrast_focus": {"on"}, "underline_links": {"on"},
		"dyslexia_font": {"on"}, "alt_text": {"on"}, "colour_safe": {"on"}})
	p, _ := ta.Deps.Settings.UserPrefs(ctx, u.ID)
	if p.TextSize != 125 || !p.HighContrastFocus || !p.UnderlineLinks || !p.DyslexiaFont || !p.Flag(account.FlagColourSafe, false) {
		t.Fatalf("a11y prefs: %+v", p)
	}
	contains(t, c.Get("/studio"), `data-text-size="125"`, `data-focus="strong"`, `data-links="underline"`, `data-font="dyslexic"`)

	htmx(c, "/settings/reading", url.Values{"mode": {"spread"}, "webtoon": {"guided"}, "background": {"grey"}, "tap_zones": {"l_shape"},
		"hide_chrome": {"on"}, "human_only": {"on"}, "data_saver": {"on"}})
	p, _ = ta.Deps.Settings.UserPrefs(ctx, u.ID)
	if p.Reading.Mode != "spread" || p.Reading.Background != "grey" || !p.Reading.TapZones || !p.Flag(account.FlagTapLShape, false) ||
		!p.Flag(account.FlagWebtoonGuided, false) || p.Reading.ProvenanceFilter != "human_art" || !p.DataSaver || p.Flag(account.FlagAutoDownload, true) {
		t.Fatalf("reading prefs: %+v", p)
	}
	// The saved values render back.
	contains(t, c.Get("/settings/reading"), `value="spread" checked`, `value="l_shape" checked`, `value="grey" checked`)
}

func TestLanguage(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u, c := ta.UserClient(t, auth.RoleCreator)
	ctx := context.Background()
	r := htmx(c, "/settings/language", url.Values{"action": {"add"}, "add_lang": {"es"}, "timezone": {"Asia/Tokyo"}})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("add: %d", r.StatusCode)
	}
	contains(t, r, "Español", `id="language-section"`)
	htmx(c, "/settings/language", url.Values{"action": {"add"}, "add_lang": {"ja"}, "timezone": {"Asia/Tokyo"}})
	htmx(c, "/settings/language", url.Values{"up": {"ja"}, "timezone": {"Asia/Tokyo"}})
	p, _ := ta.Deps.Settings.UserPrefs(ctx, u.ID)
	if strings.Join(p.Languages.Reading, ",") != "ja,es" {
		t.Fatalf("reading languages: %v", p.Languages.Reading)
	}
	if usr, _ := ta.Deps.Auth.UserByID(ctx, u.ID); usr.Timezone != "Asia/Tokyo" {
		t.Fatalf("timezone: %q", usr.Timezone)
	}
	// Switching the interface language reloads the page in it.
	r = htmx(c, "/settings/language", url.Values{"ui_lang": {"ja"}, "timezone": {"Asia/Tokyo"}})
	if r.Header.Get("HX-Refresh") != "true" {
		t.Fatalf("ui language change must refresh: %v", r.Header)
	}
	contains(t, c.Get("/settings/language"), `lang="ja"`, "表示言語")
}

func TestNotificationMatrix(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u, c := ta.UserClient(t, auth.RoleCreator)
	htmx(c, "/settings/notifications", url.Values{"n.generation.in_app": {"on"}, "n.generation.email": {"on"}, "n.comments.push": {"on"},
		"quiet": {"on"}, "quiet_start": {"23:00"}, "quiet_end": {"08:00"}, "digest": {"daily"}, "n.update.email": {"on"}})
	p, err := ta.Deps.Notify.Prefs(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ch := p.Channel(notify.KindJobFailed); !ch.InApp || !ch.Email || ch.Push {
		t.Fatalf("generation channels: %+v", ch)
	}
	if ch := p.Channel(notify.KindMention); ch.InApp || !ch.Push {
		t.Fatalf("mention channels: %+v", ch)
	}
	if !p.QuietHours.Enabled || p.QuietHours.Start != "23:00" || p.Digest != "daily" {
		t.Fatalf("quiet/digest: %+v", p)
	}
	// Members cannot change the admin-only update row.
	if ch := p.Channel(notify.KindUpdate); !ch.InApp {
		t.Fatalf("update row changed by a member: %+v", ch)
	}
	if strings.Contains(string(c.Get("/settings/notifications").Body), "Server update available") {
		t.Fatal("admin-only row shown to a member")
	}
}

func TestPasswordTwoFactorAndSessions(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u, c := ta.UserClient(t, auth.RoleCreator)
	ctx := context.Background()

	contains(t, c.Get("/settings/account/password"), "Change password")
	if r := post(c, "/settings/account/password", url.Values{"current": {"wrong-one-123"}, "password": {"ninth-bell-tolls"}, "confirm": {"ninth-bell-tolls"}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong current: %d", r.StatusCode)
	}
	if r := post(c, "/settings/account/password", url.Values{"current": {testutil.TestPassword}, "password": {"ninth-bell-tolls"}, "confirm": {"ninth-bell-tolls"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("change: %d %s", r.StatusCode, r.Body)
	}
	if r := c.Get("/settings/account"); r.StatusCode != http.StatusOK {
		t.Fatalf("current session must survive a password change: %d", r.StatusCode)
	}

	page := c.Get("/settings/account/2fa")
	contains(t, page, `src="data:image/png;base64,`, "Setup key", `name="otpauth"`)
	body := string(page.Body)
	i := strings.Index(body, `name="secret" value="`)
	secret := body[i+len(`name="secret" value="`):]
	secret = secret[:strings.Index(secret, `"`)]
	if r := post(c, "/settings/account/2fa", url.Values{"code": {"000000"}, "secret": {secret}}); r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), secret) {
		t.Fatalf("wrong code keeps the same secret: %d", r.StatusCode)
	}
	code, _ := auth.GenerateTOTPCode(secret, time.Now())
	on := post(c, "/settings/account/2fa", url.Values{"code": {code}})
	contains(t, on, "Recovery codes", "Two-step login is on")
	if usr, _ := ta.Deps.Auth.UserByID(ctx, u.ID); !usr.TOTPEnabled {
		t.Fatal("2FA not enabled")
	}
	if r := post(c, "/settings/account/2fa/disable", url.Values{"password": {"ninth-bell-tolls"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable: %d", r.StatusCode)
	}

	// A second session shows up and can be signed out.
	other := ta.NewClient()
	post(other, "/login", url.Values{"identifier": {u.Username}, "password": {"ninth-bell-tolls"}})
	contains(t, c.Get("/settings/account"), "Sign out everywhere else")
	if r := post(c, "/settings/account/sessions/revoke-others", nil); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke others: %d", r.StatusCode)
	}
	if r := other.Get("/studio"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoked session still works: %d", r.StatusCode)
	}
}

func TestProfileAndDelete(t *testing.T) { testProfileAndDelete(t) }

// TestProfileAndDeletePostgres runs the same checks on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestProfileAndDeletePostgres(t *testing.T) { testProfileAndDelete(t, testutil.WithPostgres()) }

func testProfileAndDelete(t *testing.T, opts ...testutil.Option) {
	ta := testutil.NewTestApp(t, opts...)
	u, c := ta.UserClient(t, auth.RoleCreator)
	ctx := context.Background()
	if r := post(c, "/settings/account", url.Values{"display_name": {"Old Wren"}, "email": {u.Email}, "bio": {"Keeps the lighthouse."}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("profile: %d %s", r.StatusCode, r.Body)
	}
	if usr, _ := ta.Deps.Auth.UserByID(ctx, u.ID); usr.DisplayName != "Old Wren" || usr.Bio != "Keeps the lighthouse." {
		t.Fatalf("profile not saved: %+v", usr)
	}
	if r := post(c, "/settings/account", url.Values{"display_name": {""}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty name: %d", r.StatusCode)
	}
	if r := post(c, "/settings/account/delete", url.Values{"confirm": {"someone"}, "password": {testutil.TestPassword}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong confirmation: %d", r.StatusCode)
	}
	if r := post(c, "/settings/account/delete", url.Values{"confirm": {u.Username}, "password": {"not-it-at-all"}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	if r := post(c, "/settings/account/delete", url.Values{"confirm": {u.Username}, "password": {testutil.TestPassword}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if _, err := ta.Deps.Auth.UserByID(ctx, u.ID); err == nil {
		t.Fatal("user still exists")
	}
	// The last admin cannot delete themselves.
	admin := ta.Login(t, "admin")
	r := post(admin, "/settings/account/delete", url.Values{"confirm": {"admin"}, "password": {testutil.TestPassword}})
	contains(t, r, "You are the last admin")
}

func TestWorldPlaceholderYieldsToWorldModule(t *testing.T) {
	ta := testutil.NewTestApp(t)
	c := ta.Login(t, "admin")
	contains(t, c.Get("/settings/world"), "Link it from Admin → World")
	// The fallback is only mounted because no module claimed the route.
	found := false
	for _, rt := range ta.Router.Routes() {
		if rt.Pattern == "GET /settings/world" {
			found = true
		}
	}
	if !found {
		t.Fatal("fallback route not mounted")
	}
	_ = core.RoleAdmin
}

func TestDeviceName(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0":                        "Firefox · Linux",
		"Mozilla/5.0 (Linux; Android 14; Pixel 9) AppleWebKit/537.36 Chrome/128.0 Mobile Safari/537.36": "Chrome · Android",
		"Go-http-client/1.1": "API client",
	} {
		if got := account.DeviceName(ua); got != want {
			t.Errorf("DeviceName(%q) = %q, want %q", ua, got, want)
		}
	}
}
