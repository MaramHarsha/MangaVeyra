package gen

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Module is the gen feature module.
type Module struct {
	d   *core.Deps
	svc *service
}

// New creates the module.
func New(d *core.Deps) *Module {
	svc := &service{queue: d.Jobs, auth: d.Auth}
	// Script inputs carry the project's private adaptation source.
	if d.Jobs != nil {
		d.Jobs.MarkPrivateInput(JobScript)
	}
	core.Provide[Service](d, svc)
	return &Module{d: d, svc: svc}
}

// Name implements core.Module.
func (m *Module) Name() string { return "gen" }

// Routes implements core.Module. No routes yet.
func (m *Module) Routes(r *core.Router) {}

// Init resolves the two Gate A feature services after all modules have been
// constructed. Gen deliberately installs no queue failover policy.
func (m *Module) Init(context.Context) error {
	m.svc.text = core.MustUse[ai.TextService](m.d)
	m.svc.studio = core.MustUse[studio.Service](m.d)
	return nil
}

// JobHandlers implements core.JobHandlers. Later generation handlers remain
// deferred until their owning gates are implemented.
func (m *Module) JobHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{JobScript: m.svc.handleScript}
}
