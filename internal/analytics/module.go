package analytics

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Module is the analytics feature module: it provides Recorder, serves the
// reader island's event endpoint and the F2 screen, and rolls raw events
// older than 90 days into daily aggregates.
type Module struct {
	d       *core.Deps
	rec     *recorder
	ui      *web.UI
	publish publish.Service
}

// New creates the module and provides analytics.Recorder.
func New(d *core.Deps) *Module {
	var key []byte
	if d.Secrets != nil {
		key = d.Secrets.Derive(sessionPurpose)
	} else {
		// Without a master key the hashes are still unlinkable; they only stop
		// matching across restarts.
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	rec := newRecorder(d.DB, key)
	core.Provide[Recorder](d, rec)
	return &Module{d: d, rec: rec}
}

// Name implements core.Module.
func (m *Module) Name() string { return "analytics" }

// Init resolves the publish read side and the web layer.
func (m *Module) Init(context.Context) error {
	m.publish = core.MustUse[publish.Service](m.d)
	m.rec.publish = m.publish
	m.ui = web.From(m.d)
	return nil
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	r.API("POST /api/v1/analytics/events", m.postEvents)
}

// Start rolls up old raw events a minute after start and then every six hours.
func (m *Module) Start(ctx context.Context) error {
	return m.rollUpLoop(ctx, time.Minute, 6*time.Hour)
}
