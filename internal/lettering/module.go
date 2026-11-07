package lettering

import "github.com/MaramHarsha/MangaVeyra/internal/core"

type Module struct{ engine Engine }

func New(d *core.Deps) *Module {
	e, err := NewEngine()
	if err != nil {
		panic(err)
	}
	provide(d, e)
	return &Module{engine: e}
}
func (*Module) Name() string        { return "lettering" }
func (*Module) Routes(*core.Router) {}
