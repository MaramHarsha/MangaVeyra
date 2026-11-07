package pagestore

import (
	"context"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Module is the page store module. It has no routes; the editor exposes pages
// over HTTP.
type Module struct {
	d *core.Deps
	s *store
}

// New creates the module. The implementation provides Service here.
func New(d *core.Deps) *Module {
	s := &store{db: d.DB}
	core.Provide[Service](d, s)
	return &Module{d: d, s: s}
}

func (m *Module) Init(context.Context) error {
	m.s.studio = core.MustUse[studio.Service](m.d)
	m.s.assets = core.MustUse[*assets.Service](m.d)
	return nil
}

// Name implements core.Module.
func (m *Module) Name() string { return "pagestore" }

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {}
