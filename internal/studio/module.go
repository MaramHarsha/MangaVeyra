package studio

import (
	"context"
	"net/http"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/dashboard"
)

// Module is the studio feature module.
type Module struct {
	d   *core.Deps
	svc *Service
	ui  *web.UI
}

// New creates the module and provides *studio.Service to other modules.
// The studio renders the whole dashboard (GET /studio) itself; a
// dashboard.Source another module provides still feeds its project list.
func New(d *core.Deps) *Module {
	svc := NewService(d)
	core.Provide(d, svc)
	return &Module{d: d, svc: svc}
}

// DashboardSource returns a dashboard.Source backed by the studio's projects
// (for layouts that embed the foundation's dashboard components).
func (m *Module) DashboardSource() dashboard.Source { return dashSource{m} }

// Service returns the studio service.
func (m *Module) Service() *Service { return m.svc }

// Name implements core.Module.
func (m *Module) Name() string { return "studio" }

// Init wires the lock validator, job completion hook and WebSocket topic authorizer.
func (m *Module) Init(ctx context.Context) error {
	m.ui = web.From(m.d)
	if m.d.Jobs != nil {
		m.d.Jobs.AddValidator(m.svc.scopeValidator)
		m.d.Jobs.OnFinish(m.svc.onJobFinished)
	}
	if m.d.Hub != nil {
		m.d.Hub.Authorize("project:", m.authorizeTopic)
	}
	return nil
}

// Start registers the built-in placeholder handlers for studio job types no
// other module registered (all modules' handlers are registered by now).
func (m *Module) Start(ctx context.Context) error {
	if added := m.svc.RegisterFallbackHandlers(m.d.Executor); len(added) > 0 {
		m.svc.log.Info("studio: using built-in placeholder handlers", "types", strings.Join(sortStrings(added), ","))
	}
	<-ctx.Done()
	return nil
}

// authorizeTopic lets project members subscribe to project:{id} (read only).
func (m *Module) authorizeTopic(ctx context.Context, id httpx.Identity, topic string, write bool) error {
	if write || id.User == nil {
		return core.ErrForbidden("")
	}
	a := Actor{Kind: ActorUser, UserID: id.User.ID, Token: id.Token}
	if id.IsAgent() {
		a.Kind = ActorAgent
	}
	if _, err := m.svc.Access(ctx, a, strings.TrimPrefix(topic, "project:")); err != nil {
		return core.ErrForbidden("")
	}
	return nil
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	s := r.Group("", core.RequireUser)
	s.Page("GET /studio", m.dashboard)
	s.Page("GET /studio/projects", m.projectsPage)
	s.Page("GET /studio/characters", m.allCharacters)
	s.Page("GET /studio/setting", m.allSetting)
	s.Page("GET /studio/templates", m.templatesPage)
	s.Page("GET /studio/templates/{id}", m.templatePage)
	s.Page("POST /studio/templates/{id}/delete", m.deleteTemplate)
	s.Page("POST /studio/templates/{id}/apply", m.applyTemplate)

	w := r.Group("/studio/new", core.RequireUser, core.RequireSession)
	r.Page("GET /studio/new", m.wizardStart, core.RequireUser, core.RequireSession)
	w.Page("POST /discard", m.wizardDiscard)
	w.Page("GET /{step}", m.wizardStep)
	w.Page("POST /{step}", m.wizardSave)
	w.Page("GET /status", m.wizardStatus)

	p := r.Group("/studio/p/{project}", core.RequireUser, m.svc.RequireProjectRole())
	p.Page("GET /{$}", m.projectHome)
	p.Page("GET /story", m.storyPage)
	p.Page("GET /story/{chapter}", m.storyPage)
	p.Page("GET /story/{chapter}/{scene}", m.storyPage)
	p.Page("POST /arcs", m.arcCreate)
	p.Page("POST /arcs/{id}", m.arcUpdate)
	p.Page("POST /arcs/{id}/move", m.arcMove)
	p.Page("POST /arcs/{id}/delete", m.arcDelete)
	p.Page("POST /chapters", m.chapterCreate)
	p.Page("POST /chapters/{id}", m.chapterUpdate)
	p.Page("POST /chapters/{id}/move", m.chapterMove)
	p.Page("POST /chapters/{id}/delete", m.chapterDelete)
	p.Page("POST /chapters/{id}/pages", m.pagesFromScript)
	p.Page("POST /scenes", m.sceneCreate)
	p.Page("POST /scenes/{id}", m.sceneUpdate)
	p.Page("POST /scenes/{id}/move", m.sceneMove)
	p.Page("POST /scenes/{id}/delete", m.sceneDelete)
	p.Page("POST /script", m.scriptCreate)
	p.Page("GET /script/{id}", m.scriptRow)
	p.Page("GET /script/{id}/edit", m.scriptEdit)
	p.Page("POST /script/{id}", m.scriptSave)
	p.Page("POST /script/{id}/move", m.scriptMove)
	p.Page("POST /script/{id}/delete", m.scriptDelete)
	p.Page("POST /assist", m.assistRequest)
	p.Page("GET /assist/{id}", m.assistView)
	p.Page("POST /assist/{id}/apply", m.assistApply)
	p.Page("POST /assist/{id}/dismiss", m.assistDismiss)

	p.Page("GET /characters", m.charactersPage)
	p.Page("GET /characters/new", m.characterNew)
	p.Page("POST /characters/new", m.characterCreate)
	p.Page("GET /characters/{id}", m.characterPage)
	p.Page("GET /characters/{id}/edit", m.characterEdit)
	p.Page("POST /characters/{id}", m.characterSave)
	p.Page("POST /characters/{id}/delete", m.characterDelete)
	p.Page("GET /characters/{id}/sheets", m.characterSheets)
	p.Page("POST /characters/{id}/sheets", m.characterGenerate)
	p.Page("POST /characters/{id}/pick", m.characterPick)
	p.Page("POST /characters/{id}/train", m.characterTrain)
	p.Page("POST /characters/{id}/refs", m.characterRefUpload)
	p.Page("POST /characters/{id}/refs/{ref}", m.characterRefUpdate)
	p.Page("POST /characters/{id}/refs/{ref}/delete", m.characterRefDelete)
	p.Page("POST /characters/{id}/outfits", m.outfitSave)
	p.Page("POST /characters/{id}/outfits/{oid}", m.outfitSave)
	p.Page("POST /characters/{id}/outfits/{oid}/delete", m.outfitDelete)
	p.Page("POST /characters/{id}/relations", m.relationAdd)
	p.Page("POST /characters/{id}/relations/{rid}/delete", m.relationDelete)

	p.Page("GET /setting", m.settingPage)
	p.Page("POST /setting", m.settingSave)
	p.Page("POST /setting/{id}", m.settingSave)
	p.Page("POST /setting/{id}/delete", m.settingDelete)

	p.Page("GET /settings", m.projectSettings)
	p.Page("POST /settings", m.projectSettingsSave)
	p.Page("POST /settings/members", m.memberInvite)
	p.Page("POST /settings/members/{user}", m.memberUpdate)
	p.Page("POST /settings/members/{user}/delete", m.memberRemove)
	p.Page("POST /settings/archive", m.projectArchive, core.RequireHuman)
	p.Page("POST /settings/delete", m.projectDelete, core.RequireHuman)
	p.Page("POST /pages/{page}/layout", m.pageLayout)

	m.apiRoutes(r)
}

// htmx reports whether the request came from HTMX (fragment responses).
func htmx(r *http.Request) bool { return core.IsHTMX(r) && r.Header.Get("HX-Boosted") != "true" }
