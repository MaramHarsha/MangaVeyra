// Package web is the MangaVeyra web layer: the embedded frontend bundle and
// its Vite-manifest resolver, the render helpers (full page vs HTMX
// fragment, toasts, flash messages, preference events), the request
// "chrome" every layout needs, the error-page hooks (G6: 404 SHIIN…, 500
// GASHAN!, maintenance GARA GARA, offline PUTSUN), the PWA manifest and
// service worker, the living style guide at /_design and the root redirect.
//
// Feature modules render pages like this:
//
//	u := web.From(d)
//	return u.Page(w, r, http.StatusOK, web.PageOpts{Title: "Projects", Nav: layout.NavProjects}, body)
//
// The component kit lives in internal/web/ui and the frames in internal/web/layout.
package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// current is the most recently constructed web layer; hooks fall back to it
// for requests that did not pass through the web middleware (e.g. 404s).
var current atomic.Pointer[UI]

// UI is the web layer service shared with feature modules (core.Use[*web.UI]).
type UI struct {
	d      *core.Deps
	Assets *Assets
	static http.Handler
	log    *slog.Logger

	mu        sync.Mutex
	fallbacks []fallback
	cc        computeCache
}

type fallback struct {
	pattern string
	h       http.Handler
	mws     []core.Middleware
}

// Module is the web layer's HTTP module. List it first in app wiring so its
// hooks are installed before any other module renders.
type Module struct{ ui *UI }

// New creates the web layer, provides *web.UI to other modules and installs
// the render hooks (layout, HTML error pages, 404).
func New(d *core.Deps) *Module {
	fsys := devStaticFS(d.Cfg.Dev)
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	u := &UI{d: d, Assets: NewAssets(fsys, d.Cfg.Dev, log), static: StaticHandler(fsys, d.Cfg.Dev), log: log}
	core.Provide(d, u)
	current.Store(u)
	core.SetHooks(core.Hooks{
		ErrorPage: errorPageHook,
		NotFound:  http.HandlerFunc(notFoundHook),
		Layout:    layoutHook,
	})
	return &Module{ui: u}
}

// From returns the web layer of d (it panics when the web module is not wired).
func From(d *core.Deps) *UI { return core.MustUse[*UI](d) }

// Name implements core.Module.
func (m *Module) Name() string { return "web" }

// UI returns the web layer service.
func (m *Module) UI() *UI { return m.ui }

// Routes registers static files, the PWA manifest and service worker, the
// offline page, the style guide and the root redirect. The middleware added
// here makes the web layer available to the hooks for every later route.
func (m *Module) Routes(r *core.Router) {
	u := m.ui
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(withUI(req.Context(), u)))
		})
	})
	r.Handle("GET /static/", u.static)
	r.HandleFunc("GET /favicon.ico", u.favicon)
	r.HandleFunc("GET /manifest.webmanifest", u.webManifest)
	r.HandleFunc("GET /sw.js", u.serviceWorker)
	r.Page("GET /offline", u.offline)
	r.Page("GET /_design", u.styleGuide)
	r.Page("GET /{$}", u.root)
	u.Fallback("GET /studio", core.HandlerFunc(u.dashboard), core.RequireUser)
}

// Fallback registers a handler that is mounted only if no other module
// claimed the same pattern (the fallback module mounts them last). It lets
// the foundation ship placeholder pages that feature modules later replace.
func (u *UI) Fallback(pattern string, h http.Handler, mws ...core.Middleware) {
	u.mu.Lock()
	u.fallbacks = append(u.fallbacks, fallback{pattern: pattern, h: h, mws: mws})
	u.mu.Unlock()
}

// FallbackModule mounts the registered fallbacks; list it last in app wiring.
type FallbackModule struct{ d *core.Deps }

// NewFallbacks creates the fallback module.
func NewFallbacks(d *core.Deps) *FallbackModule { return &FallbackModule{d: d} }

// Name implements core.Module.
func (m *FallbackModule) Name() string { return "web.fallbacks" }

// Routes mounts every fallback whose pattern is still free.
func (m *FallbackModule) Routes(r *core.Router) {
	u, ok := core.Use[*UI](m.d)
	if !ok {
		return
	}
	taken := map[string]bool{}
	for _, rt := range r.Routes() {
		taken[rt.Pattern] = true
	}
	u.mu.Lock()
	list := append([]fallback(nil), u.fallbacks...)
	u.mu.Unlock()
	for _, f := range list {
		if taken[f.pattern] {
			continue
		}
		func() {
			defer func() {
				if p := recover(); p != nil {
					u.log.Debug("web: fallback route not mounted (conflict)", "pattern", f.pattern, "err", p)
				}
			}()
			r.Handle(f.pattern, f.h, f.mws...)
		}()
	}
}

// PageOpts describe a page for the layouts.
type PageOpts struct {
	Title       string
	MobileTitle string
	BackURL     string
	// Section highlights Studio / Explore / Library in the top bar.
	Section string
	// Nav highlights a studio sidebar item (layout.Nav*).
	Nav string
	// Tab highlights a mobile bottom nav item (layout.Tab*).
	Tab        string
	NoSidebar  bool
	Transition string
	// Anonymous renders the chrome without the signed-in user (offline page).
	Anonymous bool
}

// Chrome collects the request data every layout needs. w may be nil (then
// queued flash toasts stay queued).
func (u *UI) Chrome(w http.ResponseWriter, r *http.Request, o PageOpts) *layout.Chrome {
	ctx := r.Context()
	lang := i18n.LocaleFrom(ctx)
	c := &layout.Chrome{
		Title: o.Title, MobileTitle: o.MobileTitle, BackURL: o.BackURL,
		Section: o.Section, Nav: o.Nav, Tab: o.Tab, NoSidebar: o.NoSidebar, Transition: o.Transition,
		Theme: core.ThemeFrom(ctx), Lang: lang, Dir: i18n.Dir(lang),
		Version: u.d.Version.Version, Dev: u.d.Cfg.Dev, RequestPath: r.URL.Path,
		Prefs: settings.DefaultUserPrefs(),
	}
	if c.Theme == "" {
		c.Theme = "dark"
	}
	if !o.Anonymous {
		c.User = core.UserFrom(ctx)
		if c.User != nil {
			c.Prefs = core.PrefsFrom(ctx)
		}
	}
	inst := u.d.Settings.MustInstance(ctx)
	c.InstanceName = inst.Name
	c.PublicMode = inst.Mode == settings.ModePublic
	c.LocalOnly = inst.LocalOnly
	c.SignUpOpen = inst.SignUp != settings.SignUpClosed
	c.ServerHost = ServerHost(inst.PublicURL, u.d.Cfg.PublicURL, r.Host)
	ea := u.Assets.Entry(MainEntry)
	c.Assets = layout.Assets{CSS: ea.CSS, Script: ea.Script, Preload: ea.Preload, Fonts: ea.Fonts}
	if q := r.URL.Query().Get("q"); q != "" && r.URL.Path == "/search" {
		c.SearchQuery = q
	}
	if c.User != nil {
		c.Meter = u.meter(ctx)
		if n, err := u.d.Notify.UnreadCount(ctx, c.User.ID); err == nil {
			c.Unread = n
		}
	}
	if w != nil {
		c.Flash = takeFlash(w, r)
	}
	return c
}

// ServerHost picks the host shown in the sidebar footer: the instance's public
// URL, the configured MV_PUBLIC_URL, or the request host.
func ServerHost(candidates ...string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if strings.Contains(c, "://") {
			if pu, err := url.Parse(c); err == nil && pu.Host != "" {
				return pu.Host
			}
			continue
		}
		return c
	}
	return "localhost"
}

// Page renders body inside the app shell (full page), or body alone for HTMX requests.
func (u *UI) Page(w http.ResponseWriter, r *http.Request, status int, o PageOpts, body templ.Component) error {
	if o.Section == "" && o.Nav != "" {
		o.Section = layout.SectionStudio
	}
	c := u.Chrome(w, r, o)
	return Render(w, r, status, withChildren(layout.AppShell(c), body), body)
}

// Plain renders body in the full-screen frame without navigation.
func (u *UI) Plain(w http.ResponseWriter, r *http.Request, status int, o PageOpts, body templ.Component) error {
	c := u.Chrome(w, r, o)
	return Render(w, r, status, withChildren(layout.PlainShell(c), body), body)
}

// withChildren renders frame with body as its children.
func withChildren(frame, body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return frame.Render(templ.WithChildren(ctx, body), w)
	})
}

// Wrap renders frame with body as its children (for layouts other than the app shell).
func Wrap(frame, body templ.Component) templ.Component { return withChildren(frame, body) }

// layoutHook wraps components rendered through core.Render in the app shell.
func layoutHook(r *http.Request, body httpx.Component) httpx.Component {
	u := uiFrom(r.Context())
	if u == nil {
		return body
	}
	c := u.Chrome(nil, r, pageOptsForPath(r.URL.Path))
	return withChildren(layout.AppShell(c), templ.ComponentFunc(body.Render))
}

// pageOptsForPath guesses the navigation highlight from a path.
func pageOptsForPath(p string) PageOpts {
	o := PageOpts{}
	switch {
	case strings.HasPrefix(p, "/studio/projects"):
		o.Section, o.Nav, o.Tab = layout.SectionStudio, layout.NavProjects, layout.TabHome
	case strings.HasPrefix(p, "/studio/characters"):
		o.Section, o.Nav, o.Tab = layout.SectionStudio, layout.NavCharacters, layout.TabHome
	case strings.HasPrefix(p, "/studio/setting"):
		o.Section, o.Nav, o.Tab = layout.SectionStudio, layout.NavSetting, layout.TabHome
	case strings.HasPrefix(p, "/studio/templates"):
		o.Section, o.Nav, o.Tab = layout.SectionStudio, layout.NavTemplates, layout.TabHome
	case strings.HasPrefix(p, "/studio/analytics"):
		o.Section, o.Nav, o.Tab = layout.SectionStudio, layout.NavAnalytics, layout.TabHome
	case strings.HasPrefix(p, "/studio"):
		o.Section, o.Nav, o.Tab = layout.SectionStudio, layout.NavHome, layout.TabHome
	case strings.HasPrefix(p, "/models"), strings.HasPrefix(p, "/queue"):
		o.Section, o.Nav = layout.SectionStudio, layout.NavModels
	case strings.HasPrefix(p, "/agents"), strings.HasPrefix(p, "/plugins"):
		o.Section, o.Nav = layout.SectionStudio, layout.NavAgents
	case strings.HasPrefix(p, "/settings"):
		o.Section, o.Nav, o.Tab = layout.SectionStudio, layout.NavSettings, layout.TabMe
	case strings.HasPrefix(p, "/admin"):
		o.Section, o.Nav = layout.SectionStudio, layout.NavAdmin
	case strings.HasPrefix(p, "/explore"), strings.HasPrefix(p, "/search"), strings.HasPrefix(p, "/s/"):
		o.Section, o.Tab, o.NoSidebar = layout.SectionExplore, layout.TabExplore, true
	case strings.HasPrefix(p, "/library"):
		o.Section, o.Tab, o.NoSidebar = layout.SectionLibrary, layout.TabLibrary, true
	}
	return o
}
