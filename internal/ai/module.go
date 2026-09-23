package ai

import (
	"context"
	"os"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/setup"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Module is the ai feature module.
type Module struct {
	d   *core.Deps
	svc *Service
	ui  *web.UI
}

// New creates the module and provides its services: *ai.Service (routing and
// execution for other modules), ai.ComputeMeter, web.ComputeExtender (top bar
// and dashboard compute card), setup.ModelStep (first-run step 3) and
// jobs.ManifestLookup (manifests handed to workers).
func New(d *core.Deps) *Module {
	s := NewService(d)
	m := &Module{d: d, svc: s}
	core.Provide(d, s)
	core.Provide[ComputeMeter](d, s)
	core.Provide[web.ComputeExtender](d, m)
	core.Provide[setup.ModelStep](d, &modelStep{m: m})
	core.Provide[jobs.ManifestLookup](d, s.ManifestJSON)
	return m
}

// Service returns the AI service.
func (m *Module) Service() *Service { return m.svc }

// Name implements core.Module.
func (m *Module) Name() string { return "ai" }

// Init loads the catalogue and installs the queue hooks (router fallback,
// spend guard, GPU time accounting).
func (m *Module) Init(ctx context.Context) error {
	m.ui = web.From(m.d)
	if err := m.svc.ReloadCatalog(ctx); err != nil {
		return err
	}
	m.d.Jobs.SetFailover(m.svc.Failover)
	m.d.Jobs.AddValidator(m.svc.spendGuard)
	m.d.Jobs.OnFinish(m.svc.onFinish)
	return nil
}

// Start reloads manifests when files in <data>/manifests change.
func (m *Module) Start(ctx context.Context) error {
	dir := m.d.Cfg.ManifestDir()
	last := dirStamp(dir)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if s := dirStamp(dir); s != last {
				last = s
				if err := m.svc.ReloadCatalog(ctx); err != nil {
					m.d.Log.Warn("ai: reload manifests", "err", err)
				}
			}
		}
	}
}

// dirStamp fingerprints a directory by names, sizes and modification times.
func dirStamp(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	out := ""
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			out += e.Name() + info.ModTime().UTC().String() + string(rune(info.Size()%1000))
		}
	}
	return out
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	g := r.Group("/models", core.RequireUser)
	r.Page("GET /models", m.modelsPage, core.RequireUser)
	g.Page("POST /models/{id}/toggle", m.toggleModel, core.RequireRole(core.RoleAdmin))
	g.Page("POST /changes/ack", m.ackChanges, core.RequireRole(core.RoleAdmin))
	g.Page("POST /local-only", m.setLocalOnly, core.RequireRole(core.RoleAdmin))

	g.Page("GET /routing", m.routingPage)
	g.Page("POST /routing/mode", m.setMode)
	g.Page("POST /routing/guards", m.saveGuards, core.RequireRole(core.RoleAdmin))
	g.Page("POST /routing/pins", m.addPin)
	g.Page("POST /routing/pins/{id}/delete", m.deletePin)

	g.Page("GET /providers/new", m.chooser)
	g.Page("GET /providers/new/{step}", m.wizard)
	g.Page("POST /providers/new/connect", m.connect)
	g.Page("POST /providers/new/mapping", m.saveMapping)
	g.Page("POST /providers/new/test", m.runTest)
	g.Page("POST /providers/new/describe", m.describe)
	g.Page("POST /providers/new/route", m.finishRoute)
	g.Page("GET /providers/{id}", m.providerPage)
	g.Page("POST /providers/{id}/toggle", m.toggleProvider)
	g.Page("POST /providers/{id}/delete", m.deleteProviderPage)
	g.Page("POST /providers/{id}/approve", m.approveProvider, core.RequireRole(core.RoleAdmin))

	adm := r.Group("/models", core.RequireRole(core.RoleAdmin), core.RequireSession)
	adm.Page("GET /workers/new", m.workersPage)
	adm.Page("POST /workers/new", m.createWorkerToken)
	adm.Page("POST /workers/tokens/{id}/revoke", m.revokeWorkerToken)
	g.Page("GET /local", m.localPage)
	adm.Page("POST /local", m.saveLocal)

	api := r.Group("/api/v1/ai", core.RequireUser)
	api.API("GET /models", m.apiModels)
	api.API("GET /providers", m.apiProviders)
	api.API("POST /providers", m.apiCreateProvider, core.RequireHuman)
	api.API("PATCH /providers/{id}", m.apiUpdateProvider, core.RequireHuman)
	api.API("DELETE /providers/{id}", m.apiDeleteProvider, core.RequireHuman)
	api.API("POST /providers/{id}/test", m.apiTestProvider, core.RequireHuman)
	api.API("POST /route", m.apiRoute)
	api.API("POST /estimate", m.apiEstimate)
	api.API("GET /decisions", m.apiDecisions)
	api.API("GET /meter", m.apiMeter)
	api.API("GET /pins", m.apiPins)
	api.API("POST /pins", m.apiAddPin, core.RequireHuman)
	api.API("DELETE /pins/{id}", m.apiDeletePin, core.RequireHuman)
	api.API("POST /feedback", m.apiFeedback)
}
