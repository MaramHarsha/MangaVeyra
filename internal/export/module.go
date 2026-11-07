package export

import (
	"context"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

type Module struct {
	d  *core.Deps
	s  *service
	ui *web.UI
}

func New(d *core.Deps) *Module {
	s := &service{db: d.DB, queue: d.Jobs}
	core.Provide[Service](d, s)
	core.Provide[QueueService](d, s)
	return &Module{d: d, s: s}
}
func (*Module) Name() string { return "export" }
func (m *Module) Init(context.Context) error {
	m.s.studio = core.MustUse[studio.Service](m.d)
	m.s.pages = core.MustUse[pagestore.Service](m.d)
	m.s.renderer = core.MustUse[compose.Renderer](m.d)
	m.s.assets = core.MustUse[*assets.Service](m.d)
	m.s.lettering = core.MustUse[lettering.Engine](m.d)
	m.ui = core.MustUse[*web.UI](m.d)
	return nil
}
func (m *Module) JobHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{JobType: m.s.handle}
}
func (m *Module) Routes(r *core.Router) { m.routes(r) }
