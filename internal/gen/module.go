package gen

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Module is the gen feature module.
type Module struct {
	d   *core.Deps
	svc *service
}

// New creates the module.
func New(d *core.Deps) *Module {
	svc := &service{queue: d.Jobs, auth: d.Auth, db: d.DB}
	// Script inputs carry the project's private adaptation source.
	if d.Jobs != nil {
		d.Jobs.MarkPrivateInput(JobScript, JobCharacterSheet, JobPanel, JobRegionEdit, JobPagesFromScript, JobLetterPage)
	}
	core.Provide[Service](d, svc)
	return &Module{d: d, svc: svc}
}

// Name implements core.Module.
func (m *Module) Name() string { return "gen" }

// Routes implements core.Module. No routes yet.
func (m *Module) Routes(r *core.Router) {}

// Init resolves feature services after construction and installs fallback only
// for validated, versioned image parents. Script jobs retain their Gate A policy.
func (m *Module) Init(context.Context) error {
	m.svc.text = core.MustUse[ai.TextService](m.d)
	m.svc.studio = core.MustUse[studio.Service](m.d)
	m.svc.images = core.MustUse[ai.ImageService](m.d)
	m.svc.pages = core.MustUse[pagestore.Service](m.d)
	m.svc.assets = core.MustUse[*assets.Service](m.d)
	m.svc.lettering = core.MustUse[lettering.Engine](m.d)
	m.svc.renderer = core.MustUse[compose.Renderer](m.d)
	m.svc.fallback, _ = core.Use[ai.ImageFallback](m.d)
	m.svc.queue.SetFailover(m.svc.failoverImage)
	return nil
}

// JobHandlers implements the Phase 1 durable generation handlers.
func (m *Module) JobHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{JobScript: m.svc.handleScript, JobCharacterSheet: m.svc.handleImage, JobPanel: m.svc.handleImage, JobRegionEdit: m.svc.handleImage, JobPagesFromScript: m.svc.handlePages}
}
