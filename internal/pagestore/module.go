package pagestore

import "github.com/MaramHarsha/MangaVeyra/internal/core"

// Module is the page store module. It has no routes; the editor exposes pages
// over HTTP.
type Module struct {
	d *core.Deps
}

// New creates the module. The implementation provides Service here.
func New(d *core.Deps) *Module { return &Module{d: d} }

// Name implements core.Module.
func (m *Module) Name() string { return "pagestore" }

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {}
