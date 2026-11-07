package account

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Preference flag keys stored in settings.UserPrefs.Flags.
const (
	FlagVerticalJP    = "language.vertical_jp"
	FlagWebtoonGuided = "reading.webtoon_guided"
	FlagTapLShape     = "reading.tap_l_shape"
	FlagHideChrome    = "reading.hide_chrome"
	FlagAutoDownload  = "reading.auto_download"
	FlagAltText       = "a11y.alt_text_always"
	FlagColourSafe    = "a11y.colour_safe"
)

func parse(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	return nil
}

func oneOf(v string, allowed ...string) bool { return slices.Contains(allowed, v) }

// ---- appearance ---------------------------------------------------------------------

func (m *Module) saveAppearance(w http.ResponseWriter, r *http.Request) error {
	if err := parse(w, r); err != nil {
		return err
	}
	theme := r.PostFormValue("theme")
	p, err := m.updatePrefs(r.Context(), func(p *settings.UserPrefs) {
		if oneOf(theme, "dark", "light", "system") {
			p.Theme = theme
		}
		p.ReducedMotion = motionValue(web.FormBool(r, "reduced_motion"), p.ReducedMotion)
		p.Textures = web.FormBool(r, "textures")
		p.LivingPanels = web.FormBool(r, "living_panels")
		p.ShowRanksXP = web.FormBool(r, "show_ranks_xp")
		p.HighContrastFocus = web.FormBool(r, "high_contrast_focus")
	})
	if err != nil {
		return err
	}
	if theme != "" {
		http.SetCookie(w, &http.Cookie{Name: core.ThemeCookie, Value: p.Theme, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
	}
	return m.saved(w, r, SecAppearance, &p)
}

// motionValue maps the reduced-motion switch to the stored value: on → "on";
// off → "system" (follow the OS) unless the member had forced full motion.
func motionValue(on bool, cur string) string {
	if on {
		return "on"
	}
	if cur == "off" {
		return "off"
	}
	return "system"
}

// ---- language --------------------------------------------------------------------------

type languageView struct {
	UI            string
	Languages     []langOption
	Reading       []langOption
	Addable       []langOption
	Timezone      string
	Timezones     []string
	AutoTranslate bool
	VerticalJP    bool
}

type langOption struct{ Code, Name string }

// knownReadingLanguages are offered for reading even without an interface catalog.
var knownReadingLanguages = []langOption{
	{"en", "English"}, {"ja", "日本語"}, {"es", "Español"}, {"fr", "Français"}, {"de", "Deutsch"}, {"pt", "Português"},
	{"it", "Italiano"}, {"ko", "한국어"}, {"zh", "中文"}, {"id", "Bahasa Indonesia"}, {"th", "ไทย"}, {"vi", "Tiếng Việt"},
	{"ar", "العربية"}, {"he", "עברית"}, {"ru", "Русский"}, {"pl", "Polski"}, {"tr", "Türkçe"}, {"hi", "हिन्दी"},
}

var timezones = []string{"UTC", "Europe/London", "Europe/Berlin", "Europe/Paris", "Europe/Madrid", "Europe/Warsaw", "Africa/Lagos",
	"Africa/Nairobi", "Asia/Dubai", "Asia/Kolkata", "Asia/Bangkok", "Asia/Jakarta", "Asia/Singapore", "Asia/Shanghai", "Asia/Seoul",
	"Asia/Tokyo", "Australia/Sydney", "Pacific/Auckland", "America/Sao_Paulo", "America/New_York", "America/Chicago",
	"America/Denver", "America/Los_Angeles", "America/Mexico_City"}

func langName(code string) string {
	for _, l := range knownReadingLanguages {
		if l.Code == code {
			return l.Name
		}
	}
	return code
}

func (m *Module) languageView(ctx context.Context, tz string, p settings.UserPrefs) languageView {
	v := languageView{UI: p.Languages.UI, Timezone: tz, AutoTranslate: p.Languages.AutoTranslate, VerticalJP: p.Flag(FlagVerticalJP, false)}
	bundle := m.d.I18n
	if bundle == nil {
		bundle = i18n.Default()
	}
	for _, code := range bundle.Languages() {
		name := bundle.Translate(code, "lang.native")
		if name == "lang.native" {
			name = langName(code)
		}
		v.Languages = append(v.Languages, langOption{code, name})
	}
	for _, code := range p.Languages.Reading {
		v.Reading = append(v.Reading, langOption{code, langName(code)})
	}
	for _, l := range knownReadingLanguages {
		if !slices.Contains(p.Languages.Reading, l.Code) {
			v.Addable = append(v.Addable, l)
		}
	}
	v.Timezones = timezones
	if tz != "" && !slices.Contains(timezones, tz) {
		v.Timezones = append([]string{tz}, timezones...)
	}
	return v
}

func (m *Module) saveLanguage(w http.ResponseWriter, r *http.Request) error {
	if err := parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	before := core.PrefsFrom(ctx).Languages.UI
	uiLang := r.PostFormValue("ui_lang")
	bundle := m.d.I18n
	if bundle == nil {
		bundle = i18n.Default()
	}
	p, err := m.updatePrefs(ctx, func(p *settings.UserPrefs) {
		if uiLang == "" || bundle.Has(uiLang) {
			p.Languages.UI = uiLang
		}
		p.Languages.AutoTranslate = web.FormBool(r, "auto_translate")
		p.Flags[FlagVerticalJP] = web.FormBool(r, "vertical_jp")
		list := p.Languages.Reading
		if add := r.PostFormValue("add_lang"); add != "" && r.PostFormValue("action") == "add" && !slices.Contains(list, add) && len(list) < 12 {
			list = append(list, add)
		}
		if rm := r.PostFormValue("remove"); rm != "" {
			list = slices.DeleteFunc(list, func(s string) bool { return s == rm })
		}
		if up := r.PostFormValue("up"); up != "" {
			if i := slices.Index(list, up); i > 0 {
				list[i-1], list[i] = list[i], list[i-1]
			}
		}
		p.Languages.Reading = list
	})
	if err != nil {
		return err
	}
	if tz := r.PostFormValue("timezone"); tz != u.Timezone && (tz == "" || slices.Contains(timezones, tz)) {
		if _, err := m.d.Auth.UpdateProfile(ctx, u.ID, auth.ProfileUpdate{Timezone: &tz}); err != nil {
			return err
		}
	}
	if uiLang != before {
		// The interface language changes every string: reload the page in it.
		if uiLang != "" {
			http.SetCookie(w, &http.Cookie{Name: core.LangCookie, Value: uiLang, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
		}
		if core.IsHTMX(r) {
			w.Header().Set("HX-Refresh", "true")
			w.WriteHeader(http.StatusNoContent)
			return nil
		}
	}
	if core.IsHTMX(r) {
		// The reading-language list changes shape: send the section back.
		web.Toast(w, "success", i18n.T(ctx, "settings.saved"), "")
		tz := u.Timezone
		if v := r.PostFormValue("timezone"); v == "" || slices.Contains(timezones, v) {
			tz = v
		}
		return web.Fragment(w, r, http.StatusOK, LanguageSection(m.languageView(ctx, tz, p)))
	}
	return m.saved(w, r, SecLanguage, nil)
}

// ---- reading ---------------------------------------------------------------------------

func (m *Module) saveReading(w http.ResponseWriter, r *http.Request) error {
	if err := parse(w, r); err != nil {
		return err
	}
	_, err := m.updatePrefs(r.Context(), func(p *settings.UserPrefs) {
		if v := r.PostFormValue("mode"); oneOf(v, "auto", "paged", "spread", "guided", "webtoon") {
			p.Reading.Mode = v
		}
		if v := r.PostFormValue("direction"); oneOf(v, "auto", "rtl", "ltr") {
			p.Reading.Direction = v
		}
		if v := r.PostFormValue("background"); oneOf(v, "black", "grey", "paper", "white") {
			p.Reading.Background = v
		}
		switch r.PostFormValue("tap_zones") {
		case "edges":
			p.Reading.TapZones, p.Flags[FlagTapLShape] = true, false
		case "l_shape":
			p.Reading.TapZones, p.Flags[FlagTapLShape] = true, true
		case "off":
			p.Reading.TapZones, p.Flags[FlagTapLShape] = false, false
		}
		p.Flags[FlagWebtoonGuided] = r.PostFormValue("webtoon") == "guided"
		p.Flags[FlagHideChrome] = web.FormBool(r, "hide_chrome")
		p.Flags[FlagAutoDownload] = web.FormBool(r, "auto_download")
		p.Reading.ShowPanelComments = web.FormBool(r, "panel_comments")
		p.DataSaver = web.FormBool(r, "data_saver")
		if web.FormBool(r, "human_only") {
			if p.Reading.ProvenanceFilter == "all" {
				p.Reading.ProvenanceFilter = "human_art"
			}
		} else {
			p.Reading.ProvenanceFilter = "all"
		}
	})
	if err != nil {
		return err
	}
	return m.saved(w, r, SecReading, nil)
}

// ---- notifications -------------------------------------------------------------------

// notifyGroup is one row of the notification matrix; it covers one or more kinds.
type notifyGroup struct {
	Key   string
	Kinds []string
	Admin bool
}

// notifyGroups follows the designed notification matrix (G1 · Notifications)
// row for row.
var notifyGroups = []notifyGroup{
	{"generation", []string{notify.KindJobDone, notify.KindJobFailed}, false},
	{"lock", []string{notify.KindLockTraining}, false},
	{"comments", []string{notify.KindComment, notify.KindMention}, false},
	{"review", []string{notify.KindReview}, false},
	{"chapter", []string{notify.KindChapter}, false},
	{"world", []string{notify.KindWorldReview}, false},
	{"update", []string{notify.KindUpdate}, true},
	{"spend", []string{notify.KindSpendLimit}, false},
}

type notificationsView struct {
	Prefs notify.Prefs
	Email bool
	Admin bool
}

func (v notificationsView) on(g notifyGroup, channel string) bool {
	c := v.Prefs.Channel(g.Kinds[0])
	switch channel {
	case "email":
		return c.Email
	case "push":
		return c.Push
	}
	return c.InApp
}

var hours = func() []string {
	out := make([]string, 0, 48)
	for h := 0; h < 24; h++ {
		for _, mm := range []string{"00", "30"} {
			hh := strconv.Itoa(h)
			if h < 10 {
				hh = "0" + hh
			}
			out = append(out, hh+":"+mm)
		}
	}
	return out
}()

func (m *Module) saveNotifications(w http.ResponseWriter, r *http.Request) error {
	if err := parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	p, err := m.d.Notify.Prefs(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, g := range notifyGroups {
		if g.Admin && !u.IsAdmin() {
			continue
		}
		c := notify.Channels{
			InApp: web.FormBool(r, "n."+g.Key+".in_app"),
			Email: web.FormBool(r, "n."+g.Key+".email"),
			Push:  web.FormBool(r, "n."+g.Key+".push"),
		}
		for _, k := range g.Kinds {
			p.Channels[k] = c
		}
	}
	p.QuietHours.Enabled = web.FormBool(r, "quiet")
	if v := r.PostFormValue("quiet_start"); slices.Contains(hours, v) {
		p.QuietHours.Start = v
	}
	if v := r.PostFormValue("quiet_end"); slices.Contains(hours, v) {
		p.QuietHours.End = v
	}
	if p.QuietHours.Timezone == "" {
		p.QuietHours.Timezone = u.Timezone
	}
	if v := r.PostFormValue("digest"); oneOf(v, notify.DigestOff, notify.DigestDaily, notify.DigestWeekly) {
		p.Digest = v
	}
	if err := m.d.Notify.SetPrefs(ctx, u.ID, p); err != nil {
		return core.ErrValidation(map[string]string{"": err.Error()})
	}
	return m.saved(w, r, SecNotifications, nil)
}

// ---- accessibility ---------------------------------------------------------------------

func (m *Module) saveAccessibility(w http.ResponseWriter, r *http.Request) error {
	if err := parse(w, r); err != nil {
		return err
	}
	p, err := m.updatePrefs(r.Context(), func(p *settings.UserPrefs) {
		p.ReducedMotion = motionValue(web.FormBool(r, "reduced_motion"), p.ReducedMotion)
		p.HighContrastFocus = web.FormBool(r, "high_contrast_focus")
		p.UnderlineLinks = web.FormBool(r, "underline_links")
		p.Reading.ReadAloud = web.FormBool(r, "read_aloud")
		p.DyslexiaFont = web.FormBool(r, "dyslexia_font")
		p.Flags[FlagAltText] = web.FormBool(r, "alt_text")
		p.Flags[FlagColourSafe] = web.FormBool(r, "colour_safe")
		if n, err := strconv.Atoi(r.PostFormValue("text_size")); err == nil {
			p.TextSize = layout.NearestTextSize(n)
		}
	})
	if err != nil {
		return err
	}
	return m.saved(w, r, SecAccessibility, &p)
}

// ---- data & export ---------------------------------------------------------------------

type dataView struct {
	QuotaMB int
	Host    string
}
