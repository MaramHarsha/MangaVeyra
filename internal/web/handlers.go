package web

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/errpages"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/styleguide"
)

// root sends visitors where they belong: the setup wizard until an admin
// exists, the studio when signed in, Explore on public instances, else log in.
func (u *UI) root(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	ok, err := u.d.Auth.HasAdmin(ctx)
	if err != nil {
		return err
	}
	switch {
	case !ok:
		core.Redirect(w, r, "/setup")
	case core.UserFrom(ctx) != nil:
		core.Redirect(w, r, "/studio")
	case u.d.Settings.MustInstance(ctx).ExplorePublic():
		core.Redirect(w, r, "/explore")
	default:
		core.Redirect(w, r, "/login")
	}
	return nil
}

func (u *UI) favicon(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(StaticFS(), "icons/favicon-gate-blue-32.png")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

// webManifest serves the PWA manifest with the instance name.
func (u *UI) webManifest(w http.ResponseWriter, r *http.Request) {
	inst := u.d.Settings.MustInstance(r.Context())
	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose,omitempty"`
	}
	m := map[string]any{
		"name":             inst.Name,
		"short_name":       "MangaVeyra",
		"description":      i18n.T(r.Context(), "app.description"),
		"id":               "/",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": "#070A12",
		"theme_color":      "#070A12",
		"icons": []icon{
			{Src: "/static/icons/app-icon-gate-blue-192.png", Sizes: "192x192", Type: "image/png"},
			{Src: "/static/icons/app-icon-gate-blue-512.png", Sizes: "512x512", Type: "image/png"},
			{Src: "/static/icons/app-icon-maskable-192.png", Sizes: "192x192", Type: "image/png", Purpose: "maskable"},
			{Src: "/static/icons/app-icon-maskable-512.png", Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
			{Src: "/static/brand/app-icon-gate-blue.svg", Sizes: "any", Type: "image/svg+xml"},
		},
		"shortcuts": []map[string]string{
			{"name": i18n.T(r.Context(), "nav.new_manga"), "url": "/studio/new"},
			{"name": i18n.T(r.Context(), "nav.library"), "url": "/library"},
		},
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(m)
}

func (u *UI) serviceWorker(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(StaticFS(), "sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(b)
}

// offline is the G6 offline page. It never contains personal data because
// the service worker caches it for everyone on the device.
func (u *UI) offline(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "public, max-age=300")
	return u.Plain(w, r, http.StatusOK, PageOpts{Title: i18n.T(r.Context(), "errors.offline.title"), Anonymous: true}, errpages.Offline())
}

// styleGuide renders every component in both themes (admins, or anyone with MV_DEV=1).
func (u *UI) styleGuide(w http.ResponseWriter, r *http.Request) error {
	usr := core.UserFrom(r.Context())
	if !u.d.Cfg.Dev {
		if usr == nil {
			core.Redirect(w, r, httpx.LoginURL(r))
			return nil
		}
		if !usr.IsAdmin() {
			return core.ErrNotFound()
		}
	}
	w.Header().Set("X-Robots-Tag", "noindex")
	o := PageOpts{Title: i18n.T(r.Context(), "design.title"), Section: "", NoSidebar: true}
	c := u.Chrome(w, r, o)
	return Render(w, r, http.StatusOK, styleguide.Page(c), nil)
}

// errorPageHook renders HTML error pages (G6). HTMX requests get a toast instead.
func errorPageHook(w http.ResponseWriter, r *http.Request, e *httpx.APIError) {
	if core.IsHTMX(r) {
		Toast(w, "error", e.Message, "")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(e.Status)
		_, _ = w.Write([]byte(e.Message))
		return
	}
	u := uiFrom(r.Context())
	if u == nil {
		http.Error(w, e.Message, e.Status)
		return
	}
	ctx := r.Context()
	var body templ.Component
	title := e.Message
	switch {
	case e.Status == http.StatusNotFound:
		body = errpages.NotFound(core.UserFrom(ctx) != nil)
		title = i18n.T(ctx, "errors.404.title")
	case e.Code == httpx.CodeMaintenance:
		inst := u.d.Settings.MustInstance(ctx)
		body = errpages.Maintenance(inst.Name, e.Message)
		title = i18n.T(ctx, "gate.maintenance.title")
	case e.Status >= 500:
		body = errpages.ServerError(e.Message, core.RequestIDFrom(ctx))
		title = i18n.T(ctx, "errors.500.title")
	default:
		body = errpages.Generic(e.Status, errorTitle(r, e), e.Message)
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := u.Plain(w, r, e.Status, PageOpts{Title: title}, body); err != nil {
		u.log.Error("web: render error page", "err", err)
	}
}

func errorTitle(r *http.Request, e *httpx.APIError) string {
	ctx := r.Context()
	switch e.Status {
	case http.StatusUnauthorized:
		return i18n.T(ctx, "errors.401.title")
	case http.StatusForbidden:
		return i18n.T(ctx, "errors.403.title")
	case http.StatusTooManyRequests:
		return i18n.T(ctx, "errors.429.title")
	}
	return i18n.T(ctx, "errors.generic.title")
}

// notFoundHook renders the 404 page for unmatched HTML routes.
func notFoundHook(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, httpx.ErrNotFound())
}
