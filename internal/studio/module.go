package studio

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Module is the studio feature module.
type Module struct {
	d   *core.Deps
	svc *service
}

// New creates the module.
func New(d *core.Deps) *Module {
	svc := &service{db: d.DB, auth: d.Auth}
	core.Provide[Service](d, svc)
	core.Provide[BibleService](d, svc)
	return &Module{d: d, svc: svc}
}

// Name implements core.Module.
func (m *Module) Name() string { return "studio" }

// Routes implements core.Module. No routes yet.
func (m *Module) Routes(r *core.Router) {}

// Init resolves cross-feature dependencies and installs the one Studio queue
// validator. All job scopes pass through the same authoritative lock checker.
func (m *Module) Init(context.Context) error {
	m.svc.assets = core.MustUse[*assets.Service](m.d)
	m.d.Jobs.AddTxValidator(func(ctx context.Context, tx *db.Tx, p *jobs.EnqueueParams) error {
		return m.svc.CheckScopeWritableTx(ctx, tx, p.Scope)
	})
	return nil
}
