package admin

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Tabs of G5, in the design's order.
const (
	tabReports  = "reports"
	tabUsers    = "users"
	tabDisputes = "disputes"
	tabRules    = "rules"
	tabServer   = "server"
)

// Module is the admin feature module.
type Module struct {
	d   *core.Deps
	ui  *web.UI
	mod social.Moderation
	// started is when this server process started (Server tab uptime).
	started time.Time
	now     func() time.Time
}

// New creates the module.
func New(d *core.Deps) *Module { return &Module{d: d, started: time.Now(), now: db.Now} }

// Name implements core.Module.
func (m *Module) Name() string { return "admin" }

// Init resolves the web layer and the social moderation service.
func (m *Module) Init(context.Context) error {
	m.ui = web.From(m.d)
	m.mod, _ = core.Use[social.Moderation](m.d)
	return nil
}

// Routes implements core.Module. Every route is an exact path, so ui.Ready
// never mistakes another module's /admin/... page (backup, update) for one of
// these.
func (m *Module) Routes(r *core.Router) {
	r.Page("GET /admin", m.reportsPage(false), m.guard)
	r.Page("GET /admin/reports", m.reportsPage(false), m.guard)
	r.Page("GET /admin/reports/{id}", m.reportsPage(false), m.guard)
	r.Page("GET /admin/disputes", m.reportsPage(true), m.guard)
	r.Page("GET /admin/disputes/{id}", m.reportsPage(true), m.guard)
	r.Page("POST /admin/reports/{id}/resolve", m.resolveReport, m.guard)
	r.Page("POST /admin/takedowns/restore", m.restoreTakedown, m.guard)

	r.Page("GET /admin/users", m.usersPage, m.guard)
	r.Page("POST /admin/users/{id}/role", m.setRole, m.guard)
	r.Page("POST /admin/users/{id}/suspend", m.setStatus(auth.StatusSuspended), m.guard)
	r.Page("POST /admin/users/{id}/reactivate", m.setStatus(auth.StatusActive), m.guard)
	r.Page("POST /admin/users/{id}/approve", m.setStatus(auth.StatusActive), m.guard)
	r.Page("POST /admin/users/{id}/decline", m.declineUser, m.guard)
	r.Page("POST /admin/users/{id}/reset-link", m.resetLink, m.guard)
	r.Page("POST /admin/users/{id}/delete", m.deleteUser, m.guard)
	r.Page("POST /admin/signup", m.saveSignup, m.guard)
	r.Page("POST /admin/limits", m.saveLimits, m.guard)
	r.Page("POST /admin/invites", m.createInvite, m.guard)
	r.Page("POST /admin/invites/{id}/revoke", m.revokeInvite, m.guard)

	r.Page("GET /admin/rules", m.rulesPage, m.guard)
	r.Page("POST /admin/rules", m.saveRules, m.guard)
	r.Page("POST /admin/rules/words", m.saveWords, m.guard)

	r.Page("GET /admin/server", m.serverPage, m.guard)
	r.Page("POST /admin/server", m.saveServer, m.guard)
	r.Page("POST /admin/maintenance", m.saveMaintenance, m.guard)
}

// guard lets only active site admins with a browser session through. Anyone
// else gets 404, so the panel's existence is never confirmed; an admin's
// bearer token is refused (admin actions need a person at a browser).
func (m *Module) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := core.UserFrom(r.Context())
		if u == nil || !u.IsAdmin() || u.Status != auth.StatusActive {
			core.WriteError(w, r, core.ErrNotFound())
			return
		}
		if id := core.IdentityFrom(r.Context()); id.Token != nil || id.SessionID == "" {
			core.WriteError(w, r, core.ErrForbidden("admin pages need a signed-in browser session; tokens cannot use them"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex")
		next.ServeHTTP(w, r)
	})
}

// frame is what every G5 tab shows above its content.
type frame struct {
	Tab    string
	Title  string
	Sub    string
	Counts counts
	// Update is the header button: "Update to x" when the update module
	// knows a release, otherwise a link to /admin/update (Soon until it exists).
	UpdateLabel string
	UpdateShow  bool
	// StorageCap is the capacity of the local media disk (0 when unknown).
	StorageCap int64
	// Stats are the four stat cards (Reports tab only, as in the design).
	Stats []stat
}

func (m *Module) frame(ctx context.Context, r *http.Request, tab string) (frame, error) {
	inst := m.d.Settings.MustInstance(ctx)
	c, err := loadCounts(ctx, m.d.DB)
	if err != nil {
		return frame{}, err
	}
	host := web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL, r.Host)
	f := frame{Tab: tab, Counts: c, Title: i18n.T(ctx, "admin.title", "host", host),
		Sub:        i18n.T(ctx, "admin.sub", "mode", modeLabel(ctx, inst.Mode), "count", c.Members, "version", m.d.Version.Version),
		UpdateShow: true, UpdateLabel: i18n.T(ctx, "admin.update.link")}
	if n, ok := core.Use[adminapi.UpdateNotice](m.d); ok {
		v, available := n.AvailableUpdate(ctx)
		f.UpdateShow = available
		f.UpdateLabel = i18n.T(ctx, "admin.update.to", "version", v)
	}
	if tab == tabReports {
		f.StorageCap = m.storageCapacity()
	}
	return f, nil
}

func modeLabel(ctx context.Context, mode string) string {
	switch mode {
	case settings.ModePersonal, settings.ModePrivate, settings.ModePublic:
		return i18n.T(ctx, "admin.mode."+mode)
	}
	return mode
}

// render draws a G5 tab inside the app shell. saved=1 (after a plain form
// post or an autosave) shows the saved toast.
func (m *Module) render(w http.ResponseWriter, r *http.Request, status int, f frame, body templ.Component) error {
	ctx := r.Context()
	o := web.PageOpts{Title: f.Title, Section: layout.SectionStudio, Nav: layout.NavAdmin}
	c := m.ui.Chrome(w, r, o)
	if r.URL.Query().Get("saved") == "1" {
		c.Flash = append(c.Flash, layout.Flash{Kind: "success", Title: i18n.T(ctx, "admin.saved")})
	}
	page := Page(f, body)
	return web.Render(w, r, status, web.Wrap(layout.AppShell(c), page), page)
}

// done finishes a successful POST: back to target with the saved toast.
func done(w http.ResponseWriter, r *http.Request, target string) error {
	u, err := url.Parse(target)
	if err != nil {
		u = &url.URL{Path: "/admin"}
	}
	q := u.Query()
	q.Set("saved", "1")
	u.RawQuery = q.Encode()
	core.Redirect(w, r, u.String())
	return nil
}

// tabs are the G5 tab links with their counts.
func tabs(ctx context.Context, f frame) []ui.SegLink {
	label := func(key string, n int) string {
		if n > 0 {
			return i18n.T(ctx, key+"_count", "count", n)
		}
		return i18n.T(ctx, key)
	}
	return []ui.SegLink{
		{Href: "/admin", Label: label("admin.tab.reports", f.Counts.OpenReports), Current: f.Tab == tabReports},
		{Href: "/admin/users", Label: i18n.T(ctx, "admin.tab.users"), Current: f.Tab == tabUsers},
		{Href: "/admin/disputes", Label: label("admin.tab.disputes", f.Counts.Disputes), Current: f.Tab == tabDisputes},
		{Href: "/admin/rules", Label: i18n.T(ctx, "admin.tab.rules"), Current: f.Tab == tabRules},
		{Href: "/admin/server", Label: i18n.T(ctx, "admin.tab.server"), Current: f.Tab == tabServer},
	}
}

// audit records an admin action with the acting admin from the request.
func (m *Module) audit(ctx context.Context, action, kind, id string, data map[string]any) {
	m.d.Auth.Audit(ctx, auth.AuditEntry{Action: action, TargetKind: kind, TargetID: id, Data: data})
}
