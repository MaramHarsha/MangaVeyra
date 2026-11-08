package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Text limits of the admin forms.
const (
	maxServerRules  = 2000
	maxDescription  = 500
	maxMaintenance  = 500
	maxPublicURLLen = 300
)

var ratingValues = []string{adminapi.RatingAll, adminapi.RatingTeen, adminapi.RatingMature, adminapi.RatingAdult}

// ---- content rules -----------------------------------------------------------

type rulesView struct {
	Content    settings.Content
	Ratings    []ui.Option
	ServerText string
	Words      string
	WordCount  int
	WordsOpen  bool
	WordsError string
	RulesError string
}

func (m *Module) rulesPage(w http.ResponseWriter, r *http.Request) error {
	return m.renderRules(w, r, http.StatusOK, m.rulesView(r.Context()))
}

func (m *Module) rulesView(ctx context.Context) rulesView {
	inst := m.d.Settings.MustInstance(ctx)
	v := rulesView{Content: inst.Content, ServerText: inst.Rules, Words: strings.Join(inst.Content.BlockedWords, "\n"),
		WordCount: len(inst.Content.BlockedWords)}
	for _, r := range ratingValues {
		v.Ratings = append(v.Ratings, ui.Option{Value: r, Label: i18n.T(ctx, "admin.rating."+r), Disabled: !adminapi.RatingAllowed(inst.Content, r)})
	}
	return v
}

func (m *Module) renderRules(w http.ResponseWriter, r *http.Request, status int, v rulesView) error {
	f, err := m.frame(r.Context(), r, tabRules)
	if err != nil {
		return err
	}
	return m.render(w, r, status, f, RulesTab(v))
}

// POST /admin/rules: the Content card (allow mature, allow adult, default
// rating, licence guard) and the sign-up rules text.
func (m *Module) saveRules(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	part := web.FormValue(r, "part")
	text := strings.TrimSpace(strings.ReplaceAll(web.FormValue(r, "rules"), "\r\n", "\n"))
	if part == "server_rules" && utf8.RuneCountInString(text) > maxServerRules {
		v := m.rulesView(ctx)
		v.ServerText, v.RulesError = text, i18n.T(ctx, "admin.rules.error.rules_long")
		return m.renderRules(w, r, http.StatusUnprocessableEntity, v)
	}
	inst, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error {
		if part == "server_rules" {
			i.Rules = text
			return nil
		}
		c := &i.Content
		c.AllowMature, c.AllowAdult, c.LicenceGuard = web.FormBool(r, "allow_mature"), web.FormBool(r, "allow_adult"), web.FormBool(r, "licence_guard")
		if rating := web.FormValue(r, "default_age_rating"); rating != "" {
			c.DefaultAgeRating = rating
		}
		// A default the rules no longer allow falls back to all ages.
		c.DefaultAgeRating = adminapi.DefaultRating(*c)
		// Every chapter is labelled at publish (product rule 5); there is no off switch.
		c.RequireAILabels = true
		return nil
	})
	if err != nil {
		return core.ErrBadRequest("content rules")
	}
	if part == "server_rules" {
		m.audit(ctx, "admin.settings.server_rules", "instance", "rules", nil)
	} else {
		c := inst.Content
		m.audit(ctx, "admin.settings.content", "instance", "content", map[string]any{"allow_mature": c.AllowMature,
			"allow_adult": c.AllowAdult, "default_age_rating": c.DefaultAgeRating, "licence_guard": c.LicenceGuard})
	}
	return done(w, r, "/admin/rules")
}

// POST /admin/rules/words: the blocked words list.
func (m *Module) saveWords(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	raw := web.FormValue(r, "words")
	words, err := adminapi.ParseBlockedWords(raw)
	if err != nil {
		v := m.rulesView(ctx)
		v.Words, v.WordsOpen = raw, true
		v.WordsError = i18n.T(ctx, "admin.rules.error.words_many", "count", adminapi.MaxBlockedWords)
		if errors.Is(err, adminapi.ErrWordTooLong) {
			v.WordsError = i18n.T(ctx, "admin.rules.error.word_long", "count", adminapi.MaxBlockedWordRunes)
		}
		return m.renderRules(w, r, http.StatusUnprocessableEntity, v)
	}
	if _, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error {
		i.Content.BlockedWords = words
		return nil
	}); err != nil {
		return err
	}
	m.audit(ctx, "admin.settings.blocked_words", "instance", "content", map[string]any{"count": len(words)})
	return done(w, r, "/admin/rules")
}

// ---- server ------------------------------------------------------------------

type serviceRow struct{ Label, Value string }

type serverView struct {
	Name, Description, PublicURL, Mode string
	Errors                             map[string]string
	Services                           []serviceRow
	Maintenance                        settings.Maintenance
	Dialog                             bool
	MaintError                         string
	MaintDraft                         string
}

func (m *Module) serverPage(w http.ResponseWriter, r *http.Request) error {
	v := m.serverView(r.Context())
	v.Dialog = r.URL.Query().Get("dialog") == "maintenance"
	return m.renderServer(w, r, http.StatusOK, v)
}

func (m *Module) renderServer(w http.ResponseWriter, r *http.Request, status int, v serverView) error {
	f, err := m.frame(r.Context(), r, tabServer)
	if err != nil {
		return err
	}
	return m.render(w, r, status, f, ServerTab(v))
}

func (m *Module) serverView(ctx context.Context) serverView {
	inst := m.d.Settings.MustInstance(ctx)
	v := serverView{Name: inst.Name, Description: inst.Description, PublicURL: inst.PublicURL, Mode: inst.Mode,
		Errors: map[string]string{}, Maintenance: inst.Maintenance, MaintDraft: inst.Maintenance.Message}
	v.Services = m.services(ctx)
	return v
}

// services is the Services card: real state only.
func (m *Module) services(ctx context.Context) []serviceRow {
	unknown := "—"
	rows := []serviceRow{{i18n.T(ctx, "admin.server.database"), m.databaseStatus(ctx)}}

	storage := unknown
	var used int64
	if err := m.d.DB.Get(ctx, &used, `SELECT CAST(COALESCE(SUM(bytes), 0) AS BIGINT) FROM assets`); err == nil {
		storage = formatBytes(used)
		if c := m.storageCapacity(); c > 0 {
			storage = i18n.T(ctx, "admin.stat.storage_of", "used", storage, "total", formatBytes(c))
		}
	}
	backend := i18n.T(ctx, "admin.server.storage_local")
	if m.d.Cfg.Storage == "s3" {
		backend = i18n.T(ctx, "admin.server.storage_s3")
	}
	rows = append(rows, serviceRow{i18n.T(ctx, "admin.server.storage"), backend + " · " + storage})

	email := i18n.T(ctx, "admin.server.email_off")
	if m.d.Notify.EmailEnabled() {
		email = i18n.T(ctx, "admin.server.email_on")
	}
	rows = append(rows, serviceRow{i18n.T(ctx, "admin.server.email"), email})
	rows = append(rows, serviceRow{i18n.T(ctx, "admin.server.worker"), m.workerStatus(ctx)})

	queue := unknown
	if st, err := m.d.Jobs.Stats(ctx, ""); err == nil {
		queue = i18n.T(ctx, "admin.server.queue_value", "queued", st.Queued, "running", st.Running, "failed", st.FailedLastHour)
	}
	rows = append(rows, serviceRow{i18n.T(ctx, "admin.server.queue"), queue})
	rows = append(rows, serviceRow{i18n.T(ctx, "admin.server.version"), m.d.Version.Version})
	rows = append(rows, serviceRow{i18n.T(ctx, "admin.server.uptime"), duration(ctx, time.Since(m.started))})
	return rows
}

func (m *Module) databaseStatus(ctx context.Context) string {
	var name, version string
	var size int64
	var err error
	if m.d.DB.Dialect() == db.Postgres {
		name = "PostgreSQL"
		if err = m.d.DB.Get(ctx, &version, `SHOW server_version`); err == nil {
			err = m.d.DB.Get(ctx, &size, `SELECT pg_database_size(current_database())`)
		}
		if i := strings.IndexByte(version, ' '); i > 0 {
			version = version[:i]
		}
	} else {
		name = "SQLite"
		var pages, pageSize int64
		if err = m.d.DB.Get(ctx, &version, `SELECT sqlite_version()`); err == nil {
			if err = m.d.DB.Get(ctx, &pages, `PRAGMA page_count`); err == nil {
				err = m.d.DB.Get(ctx, &pageSize, `PRAGMA page_size`)
			}
		}
		size = pages * pageSize
	}
	if err != nil {
		return i18n.T(ctx, "admin.server.database_down", "name", name)
	}
	return i18n.T(ctx, "admin.server.database_ok", "name", strings.TrimSpace(name+" "+version), "size", formatBytes(size))
}

func (m *Module) workerStatus(ctx context.Context) string {
	list, err := m.d.Jobs.ListWorkers(ctx)
	if err != nil {
		return "—"
	}
	var online []jobs.Worker
	for _, w := range list {
		if w.Status == jobs.WorkerOnline {
			online = append(online, w)
		}
	}
	if len(online) == 0 {
		return i18n.T(ctx, "admin.server.worker_none")
	}
	name := online[0].Name
	if g := online[0].GPUs.V; len(g) > 0 && g[0].Name != "" {
		name = g[0].Name
	}
	if len(online) == 1 {
		return i18n.T(ctx, "admin.server.worker_one", "name", name)
	}
	return i18n.T(ctx, "admin.server.worker_many", "count", len(online), "name", name)
}

// POST /admin/server: the Instance card.
func (m *Module) saveServer(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	v := m.serverView(ctx)
	v.Name, v.Description = web.FormValue(r, "name"), strings.ReplaceAll(web.FormValue(r, "description"), "\r\n", "\n")
	v.PublicURL, v.Mode = strings.TrimRight(web.FormValue(r, "public_url"), "/"), web.FormValue(r, "mode")
	if v.Name == "" || utf8.RuneCountInString(v.Name) > 80 {
		v.Errors["name"] = i18n.T(ctx, "admin.server.error.name")
	}
	if utf8.RuneCountInString(v.Description) > maxDescription {
		v.Errors["description"] = i18n.T(ctx, "admin.server.error.description")
	}
	if v.PublicURL != "" && !validPublicURL(v.PublicURL) {
		v.Errors["public_url"] = i18n.T(ctx, "admin.server.error.public_url")
	}
	if v.Mode != settings.ModePersonal && v.Mode != settings.ModePrivate && v.Mode != settings.ModePublic {
		v.Errors["mode"] = i18n.T(ctx, "admin.server.error.mode")
	}
	if len(v.Errors) > 0 {
		return m.renderServer(w, r, http.StatusUnprocessableEntity, v)
	}
	if _, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error {
		i.Name, i.Description, i.PublicURL, i.Mode = v.Name, v.Description, v.PublicURL, v.Mode
		return nil
	}); err != nil {
		// The stored name is limited to 80 bytes, which a long non-Latin name can exceed.
		v.Errors["name"] = i18n.T(ctx, "admin.server.error.name")
		return m.renderServer(w, r, http.StatusUnprocessableEntity, v)
	}
	m.audit(ctx, "admin.settings.instance", "instance", "instance", map[string]any{"name": v.Name, "public_url": v.PublicURL, "mode": v.Mode})
	return done(w, r, "/admin/server")
}

// validPublicURL accepts an absolute http(s) URL with a host and no query or fragment.
func validPublicURL(raw string) bool {
	if len(raw) > maxPublicURLLen {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(raw, " \t\n")
}

// POST /admin/maintenance (enabled = 1 | 0, message)
func (m *Module) saveMaintenance(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	msg := strings.TrimSpace(strings.ReplaceAll(web.FormValue(r, "message"), "\r\n", "\n"))
	on := web.FormValue(r, "enabled") == "1"
	if utf8.RuneCountInString(msg) > maxMaintenance {
		v := m.serverView(ctx)
		v.Dialog, v.MaintDraft, v.MaintError = true, msg, i18n.T(ctx, "admin.maintenance.error.long")
		return m.renderServer(w, r, http.StatusUnprocessableEntity, v)
	}
	if _, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error {
		i.Maintenance = settings.Maintenance{Enabled: on, Message: msg}
		return nil
	}); err != nil {
		return err
	}
	action := "admin.maintenance.off"
	if on {
		action = "admin.maintenance.on"
	}
	m.audit(ctx, action, "instance", "maintenance", map[string]any{"message": msg})
	return done(w, r, "/admin/server")
}
