// Package activityui renders the designed G9 queue and G4 notification center.
// Persistence and state transitions remain in jobs and notify, which cannot
// import core or web without a cycle.
package activityui

import (
	"context"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

type Module struct {
	d     *core.Deps
	ui    *web.UI
	names ai.RouteNamer
}

func New(d *core.Deps) *Module { return &Module{d: d} }
func (m *Module) Name() string { return "activityui" }

func (m *Module) Init(context.Context) error {
	m.ui = web.From(m.d)
	m.names = core.MustUse[ai.RouteNamer](m.d)
	return nil
}

func (m *Module) Routes(r *core.Router) {
	r.Page("GET /queue", m.queuePage, core.RequireSession)
	r.Page("GET /queue/status", m.queueStatus, core.RequireSession)
	r.Page("POST /queue/jobs/{id}/cancel", m.cancelJob, core.RequireSession)
	r.Page("POST /queue/workers/{id}/{action}", m.workerAction, core.RequireSession, core.RequireRole(core.RoleAdmin))
	r.Page("GET /notifications", m.notificationsPage, core.RequireSession)
	r.Page("POST /notifications/read", m.readAll, core.RequireSession)
	r.Page("POST /notifications/{id}/open", m.openNotification, core.RequireSession)
}

func parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	return r.ParseForm()
}
