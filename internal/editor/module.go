package editor

import (
	"context"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Module is the editor feature module.
type Module struct {
	d       *core.Deps
	studio  studio.Service
	pages   pagestore.Service
	letter  lettering.Engine
	gen     gen.Service
	control gen.ControlService
	assets  *assets.Service
	ui      *web.UI
	// generating serialises one user's generation requests per page so a
	// repeated submission reuses the active job (see activeGeneration).
	generating keyedMutex
}

// New creates the module.
func New(d *core.Deps) *Module { return &Module{d: d} }

// Name implements core.Module.
func (m *Module) Name() string { return "editor" }

func (m *Module) Init(context.Context) error {
	m.studio = core.MustUse[studio.Service](m.d)
	m.pages = core.MustUse[pagestore.Service](m.d)
	m.letter = core.MustUse[lettering.Engine](m.d)
	m.gen = core.MustUse[gen.Service](m.d)
	m.control = core.MustUse[gen.ControlService](m.d)
	m.assets = core.MustUse[*assets.Service](m.d)
	m.ui = core.MustUse[*web.UI](m.d)
	return nil
}

func (m *Module) Routes(r *core.Router) {
	r.Page("GET /studio/p/{project}/chapters/{chapter}/edit", m.editorPage, core.RequireSession)
	r.Page("GET /studio/p/{project}/chapters/{chapter}/pages/{page}/history", m.pageHistory, core.RequireSession)
	base := "/api/v1/editor/p/{project}/chapters/{chapter}"
	r.API("GET "+base+"/pages/{page}/quote", m.controlQuote, core.RequireSession)
	r.API("POST "+base+"/lock/quote", m.repairQuote, core.RequireSession)
	r.API("GET "+base+"/lock/{character}", m.lockStatus, core.RequireSession)
	r.API("POST "+base+"/lock/repair", m.repairCharacter, core.RequireSession)
	r.API("POST "+base+"/pages/{page}/regenerate", m.generatePage, core.RequireSession)
	r.API("POST "+base+"/pages/{page}/sketch", m.uploadSketch, core.RequireSession)
	r.API("GET "+base+"/pages/{page}/versions/{version}", m.versionPage, core.RequireSession)
	r.API("GET "+base+"/pages/{page}/proof", m.pageProof, core.RequireSession)
	r.API("GET "+base, m.bootstrap, core.RequireSession)
	r.API("POST "+base+"/pages", m.createPage, core.RequireSession)
	r.API("POST "+base+"/pages/reorder", m.reorderPages, core.RequireSession)
	r.API("GET "+base+"/pages/{page}", m.getPage, core.RequireSession)
	r.API("PUT "+base+"/pages/{page}", m.savePage, core.RequireSession)
	r.API("DELETE "+base+"/pages/{page}", m.deletePage, core.RequireSession)
	r.API("POST "+base+"/pages/{page}/lock", m.setLock, core.RequireSession)
	r.API("POST "+base+"/pages/{page}/lettering", m.layoutLettering, core.RequireSession)
	r.API("GET "+base+"/pages/{page}/history", m.history, core.RequireSession)
	r.API("POST "+base+"/pages/{page}/revert", m.revert, core.RequireSession)
	r.API("POST "+base+"/pages/{page}/generate", m.generate, core.RequireSession)
	r.API("POST "+base+"/pages/{page}/masks", m.uploadMask, core.RequireSession)
	r.API("GET "+base+"/queue", m.queue, core.RequireSession)
	r.API("GET "+base+"/pages/{page}/layouts", m.pageLayouts, core.RequireSession)
	r.API("GET "+base+"/pages/{page}/thumb", m.thumb, core.RequireSession)
	r.API("GET "+base+"/pages/{page}/route", m.route, core.RequireSession)
	r.API("GET "+base+"/pages/{page}/jobs/{job}", m.job, core.RequireSession)
	r.API("POST "+base+"/pages/{page}/jobs/{job}/apply", m.apply, core.RequireSession)
}
