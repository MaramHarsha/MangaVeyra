package compose

import (
	"context"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
)

type Module struct {
	d *core.Deps
	r *renderer
}

func New(d *core.Deps) *Module {
	r := &renderer{}
	core.Provide[Renderer](d, r)
	return &Module{d: d, r: r}
}
func (*Module) Name() string        { return "compose" }
func (*Module) Routes(*core.Router) {}
func (m *Module) Init(context.Context) error {
	m.r.assets = core.MustUse[*assets.Service](m.d)
	m.r.lettering = core.MustUse[lettering.Engine](m.d)
	return nil
}
