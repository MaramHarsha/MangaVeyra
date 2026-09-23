// Package account serves the member settings pages (G1): account (profile,
// password, two-step login, sessions, danger zone), appearance, language,
// reading, notifications, accessibility, World connection (a placeholder
// the World module replaces) and data & export.
//
// Every preference is stored through internal/settings (user_prefs) or the
// notify service; changes apply instantly: forms auto-save with HTMX and the
// response carries an "mv:prefs" event that updates theme, motion and text
// size on <html>. Without JavaScript every form has a Save button.
package account

import (
	"context"
	"maps"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Section keys.
const (
	SecAccount       = "account"
	SecAppearance    = "appearance"
	SecLanguage      = "language"
	SecReading       = "reading"
	SecNotifications = "notifications"
	SecAccessibility = "accessibility"
	SecWorld         = "world"
	SecData          = "data"
)

var sections = []struct{ Key, Icon string }{
	{SecAccount, "user-circle"}, {SecAppearance, "palette"}, {SecLanguage, "language"}, {SecReading, "book"},
	{SecNotifications, "bell"}, {SecAccessibility, "accessible"}, {SecWorld, "world"}, {SecData, "database-export"},
}

// Module is the settings module.
type Module struct {
	d  *core.Deps
	ui *web.UI
}

// New creates the module.
func New(d *core.Deps) *Module { return &Module{d: d} }

// Name implements core.Module.
func (m *Module) Name() string { return "account" }

// Init looks up the web layer and registers the World placeholder (the World
// module replaces it by registering GET /settings/world itself).
func (m *Module) Init(ctx context.Context) error {
	m.ui = web.From(m.d)
	m.ui.Fallback("GET /settings/world", core.HandlerFunc(m.worldPage), core.RequireUser)
	return nil
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	// The theme toggle works for visitors too (cookie) and members (preference).
	r.Page("POST /settings/theme", m.setTheme)

	r.Page("GET /settings", m.index, core.RequireSession)
	g := r.Group("/settings", core.RequireSession)
	g.Page("GET /account", m.section(SecAccount))
	g.Page("POST /account", m.saveProfile)
	g.Page("GET /account/password", m.passwordPage)
	g.Page("POST /account/password", m.changePassword)
	g.Page("GET /account/2fa", m.twoFactorPage)
	g.Page("POST /account/2fa", m.confirmTwoFactor)
	g.Page("POST /account/2fa/disable", m.disableTwoFactor)
	g.Page("POST /account/sessions/{id}/revoke", m.revokeSession)
	g.Page("POST /account/sessions/revoke-others", m.revokeOthers)
	g.Page("POST /account/delete", m.deleteAccount)
	g.Page("GET /appearance", m.section(SecAppearance))
	g.Page("POST /appearance", m.saveAppearance)
	g.Page("GET /language", m.section(SecLanguage))
	g.Page("POST /language", m.saveLanguage)
	g.Page("GET /reading", m.section(SecReading))
	g.Page("POST /reading", m.saveReading)
	g.Page("GET /notifications", m.section(SecNotifications))
	g.Page("POST /notifications", m.saveNotifications)
	g.Page("GET /accessibility", m.section(SecAccessibility))
	g.Page("POST /accessibility", m.saveAccessibility)
	g.Page("GET /data", m.section(SecData))
}

func (m *Module) nav(ctx context.Context) []layout.SettingsNavItem {
	out := make([]layout.SettingsNavItem, 0, len(sections))
	for _, s := range sections {
		out = append(out, layout.SettingsNavItem{Key: s.Key, Href: "/settings/" + s.Key, Label: i18n.T(ctx, "settings.nav."+s.Key), Icon: s.Icon})
	}
	return out
}

// render draws the settings frame with one section's content.
func (m *Module) render(w http.ResponseWriter, r *http.Request, status int, current string, index bool, body templComponent) error {
	ctx := r.Context()
	title := i18n.T(ctx, "settings.title")
	o := web.PageOpts{Title: title, Nav: layout.NavSettings, Section: layout.SectionStudio, Tab: layout.TabMe}
	if !index {
		o.Title = i18n.T(ctx, "settings.nav."+current) + " · " + title
		o.MobileTitle = i18n.T(ctx, "settings.nav."+current)
		o.BackURL = "/settings"
	}
	c := m.ui.Chrome(w, r, o)
	return web.Render(w, r, status, web.Wrap(layout.SettingsLayout(c, title, m.nav(ctx), current, index), body), body)
}

func (m *Module) index(w http.ResponseWriter, r *http.Request) error {
	body, err := m.sectionBody(r, SecAccount)
	if err != nil {
		return err
	}
	return m.render(w, r, http.StatusOK, SecAccount, true, body)
}

func (m *Module) section(key string) core.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		body, err := m.sectionBody(r, key)
		if err != nil {
			return err
		}
		return m.render(w, r, http.StatusOK, key, false, body)
	}
}

// sectionBody builds a section's content from the member's current state.
func (m *Module) sectionBody(r *http.Request, key string) (templComponent, error) {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	prefs := core.PrefsFrom(ctx)
	switch key {
	case SecAccount:
		v, err := m.accountView(r, web.FormErrors{})
		if err != nil {
			return nil, err
		}
		return AccountSection(v), nil
	case SecAppearance:
		return AppearanceSection(prefs), nil
	case SecLanguage:
		return LanguageSection(m.languageView(ctx, u.Timezone, prefs)), nil
	case SecReading:
		return ReadingSection(prefs), nil
	case SecNotifications:
		np, err := m.d.Notify.Prefs(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		return NotificationsSection(notificationsView{Prefs: np, Email: m.d.Notify.EmailEnabled(), Admin: u.IsAdmin()}), nil
	case SecAccessibility:
		return AccessibilitySection(prefs), nil
	case SecData:
		inst := m.d.Settings.MustInstance(ctx)
		return DataSection(dataView{QuotaMB: inst.Limits.StorageQuotaMB, Host: web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL, r.Host)}), nil
	}
	return nil, core.ErrNotFound()
}

// worldPage is the placeholder World connection tab.
func (m *Module) worldPage(w http.ResponseWriter, r *http.Request) error {
	inst := m.d.Settings.MustInstance(r.Context())
	return m.render(w, r, http.StatusOK, SecWorld, false, WorldSection(inst.World.Enabled, inst.LocalOnly, core.UserFrom(r.Context()).IsAdmin()))
}

// updatePrefs applies fn to a copy of the member's preferences (maps cloned) and saves them.
func (m *Module) updatePrefs(ctx context.Context, fn func(p *settings.UserPrefs)) (settings.UserPrefs, error) {
	u := core.UserFrom(ctx)
	return m.d.Settings.UpdateUserPrefs(ctx, u.ID, func(p *settings.UserPrefs) error {
		p.Flags = maps.Clone(p.Flags)
		if p.Flags == nil {
			p.Flags = map[string]bool{}
		}
		fn(p)
		return nil
	})
}

// saved finishes a settings POST: HTMX gets a toast (and a preference event),
// plain forms are redirected back to the section with a flash message.
func (m *Module) saved(w http.ResponseWriter, r *http.Request, section string, prefs *settings.UserPrefs) error {
	ctx := r.Context()
	if prefs != nil {
		web.TriggerPrefs(w, prefsEvent(*prefs))
	}
	if core.IsHTMX(r) {
		web.Toast(w, "success", i18n.T(ctx, "settings.saved"), "")
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	web.Flash(w, r, "success", i18n.T(ctx, "settings.saved"), "")
	core.Redirect(w, r, "/settings/"+section)
	return nil
}

func prefsEvent(p settings.UserPrefs) web.PrefsEvent {
	textures, focus, links, font := p.Textures, p.HighContrastFocus, p.UnderlineLinks, p.DyslexiaFont
	motion := p.ReducedMotion
	return web.PrefsEvent{Theme: p.Theme, Motion: motion, TextSize: p.TextSize, Textures: &textures, StrongFocus: &focus,
		UnderlineLinks: &links, DyslexiaFont: &font}
}

// setTheme stores the theme chosen with the top-bar toggle.
func (m *Module) setTheme(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	theme := r.PostFormValue("theme")
	if theme != "dark" && theme != "light" && theme != "system" {
		return core.ErrValidation(map[string]string{"theme": i18n.T(r.Context(), "error.validation")})
	}
	if u := core.UserFrom(r.Context()); u != nil {
		if _, err := m.updatePrefs(r.Context(), func(p *settings.UserPrefs) { p.Theme = theme }); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{Name: core.ThemeCookie, Value: theme, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil || r.Header.Get("X-Mv-Https") == "1"})
	if core.IsHTMX(r) || core.WantsJSON(r) {
		web.TriggerPrefs(w, web.PrefsEvent{Theme: theme})
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	core.Redirect(w, r, core.SafeRedirectTarget(r.PostFormValue("next"), "/settings/appearance"))
	return nil
}
