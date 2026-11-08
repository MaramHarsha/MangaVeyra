package publish

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Module is the publish feature module.
type Module struct {
	d   *core.Deps
	svc *service
	ui  *web.UI
}

// New creates the module and provides publish.Service.
func New(d *core.Deps) *Module {
	svc := &service{db: d.DB, settings: d.Settings, now: db.Now, deps: d, log: d.Log, auth: d.Auth, i18n: d.I18n,
		jobs: d.Jobs, notify: d.Notify}
	core.Provide[Service](d, svc)
	if d.Jobs != nil {
		// Publish input carries synopsis and alt-text drafts: never echo it in job views.
		d.Jobs.MarkPrivateInput(JobType)
	}
	return &Module{d: d, svc: svc}
}

// Name implements core.Module.
func (m *Module) Name() string { return "publish" }

// Init resolves the services the read and write sides depend on.
func (m *Module) Init(context.Context) error {
	m.svc.studio = core.MustUse[studio.Service](m.d)
	m.svc.assets = core.MustUse[*assets.Service](m.d)
	m.svc.pages = core.MustUse[pagestore.Service](m.d)
	m.svc.renderer = core.MustUse[compose.Renderer](m.d)
	m.svc.lettering = core.MustUse[lettering.Engine](m.d)
	if c, ok := core.Use[gen.CoverService](m.d); ok {
		m.svc.cover = c
	}
	m.ui, _ = core.Use[*web.UI](m.d)
	return nil
}

// JobHandlers implements core.JobHandlers.
func (m *Module) JobHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{JobType: m.svc.handlePublish}
}
