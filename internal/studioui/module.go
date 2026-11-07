package studioui

import (
	"context"
	"errors"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/dashboard"
)

// Module is the browser-facing Studio module. Domain persistence stays in
// studio and queued execution stays in gen.
type Module struct {
	d      *core.Deps
	studio studio.Service
	gen    gen.Service
	bible  studio.BibleService
	assets *assets.Service
	ui     *web.UI
}

var _ dashboard.Source = (*Module)(nil)

// New creates the Studio UI module and immediately publishes the dashboard
// adapter. The adapter is only called after Init has resolved its services.
func New(d *core.Deps) *Module {
	m := &Module{d: d}
	core.Provide[dashboard.Source](d, m)
	return m
}

// Name implements core.Module.
func (m *Module) Name() string { return "studioui" }

// Init resolves the contracts provided by Studio, Generation and Web.
func (m *Module) Init(context.Context) error {
	var ok bool
	if m.studio, ok = core.Use[studio.Service](m.d); !ok {
		return errors.New("studioui: studio service unavailable")
	}
	if m.gen, ok = core.Use[gen.Service](m.d); !ok {
		return errors.New("studioui: generation service unavailable")
	}
	if m.ui, ok = core.Use[*web.UI](m.d); !ok {
		return errors.New("studioui: web UI unavailable")
	}
	m.bible = core.MustUse[studio.BibleService](m.d)
	m.assets = core.MustUse[*assets.Service](m.d)
	return nil
}

// Routes mounts the session-only Studio browser screens. Bearer tokens are
// deliberately rejected by core.RequireSession before any handler runs.
func (m *Module) Routes(r *core.Router) {
	m.RegisterBibleRoutes(r)
	r.Page("GET /studio/new", m.newProjectRedirect, core.RequireSession)
	r.Page("GET /studio/new/{step}", m.newProjectPage, core.RequireSession)
	r.Page("POST /studio/new/premise", m.createProject, core.RequireSession)
	r.Page("GET /studio/p/{project}/story", m.projectStory, core.RequireSession)
	r.Page("POST /studio/p/{project}/story/chapters", m.createChapter, core.RequireSession)
	r.Page("GET /studio/p/{project}/story/{chapter}", m.chapterStory, core.RequireSession)
	r.Page("POST /studio/p/{project}/story/{chapter}/generate", m.generateScript, core.RequireSession)
	r.Page("GET /studio/p/{project}/story/{chapter}/jobs/{job}", m.scriptJob, core.RequireSession)
	r.Page("POST /studio/p/{project}/story/{chapter}/jobs/{job}/retry", m.retryScriptJob, core.RequireSession)
	r.Page("POST /studio/p/{project}/story/{chapter}/script", m.editScript, core.RequireSession)
}

func browserActor(r *http.Request) (studio.Actor, error) {
	id := core.IdentityFrom(r.Context())
	if id.User == nil || id.User.ID == "" || id.Token != nil || id.SessionID == "" {
		return studio.Actor{}, studio.ErrForbidden
	}
	return studio.Actor{UserID: id.User.ID, SiteRole: id.User.Role}, nil
}
