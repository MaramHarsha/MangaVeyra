package packs

import "github.com/MaramHarsha/MangaVeyra/internal/core"

// Module is the packs feature module.
type Module struct {
	d *core.Deps
}

// New creates the module.
func New(d *core.Deps) *Module { return &Module{d: d} }

// Name implements core.Module.
func (m *Module) Name() string { return "packs" }

// Routes implements core.Module. No routes yet.
func (m *Module) Routes(r *core.Router) {}
