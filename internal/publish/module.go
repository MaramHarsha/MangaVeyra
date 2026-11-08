package publish

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Module is the publish feature module.
type Module struct {
	d   *core.Deps
	svc *service
}

// New creates the module and provides publish.Service.
func New(d *core.Deps) *Module {
	svc := &service{db: d.DB, settings: d.Settings, now: db.Now}
	core.Provide[Service](d, svc)
	return &Module{d: d, svc: svc}
}

// Name implements core.Module.
func (m *Module) Name() string { return "publish" }

// Init resolves the studio and asset services the read side depends on.
func (m *Module) Init(context.Context) error {
	m.svc.studio = core.MustUse[studio.Service](m.d)
	m.svc.assets = core.MustUse[*assets.Service](m.d)
	return nil
}

// Routes implements core.Module. No routes yet.
func (m *Module) Routes(r *core.Router) {}
