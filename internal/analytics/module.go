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
	d        *core.Deps
	rec      *recorder
	sessions *sessions
	ui       *web.UI
	publish  publish.Service
}

// New creates the module and provides analytics.Recorder and
// analytics.ReadingSessions.
func New(d *core.Deps) *Module {
	rec := newRecorder(d.DB, derivedKey(d, sessionPurpose))
	s := newSessions(derivedKey(d, sessionCookiePurpose))
	core.Provide[Recorder](d, rec)
	core.Provide[ReadingSessions](d, s)
	return &Module{d: d, rec: rec, sessions: s}
}

// derivedKey derives a purpose key from the server's master key. Without a
// master key it is random: hashes stay unlinkable and cookies are refused
// after a restart (visitors then simply start a new session).
func derivedKey(d *core.Deps, purpose string) []byte {
	if d.Secrets != nil {
		return d.Secrets.Derive(purpose)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return key
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
	r.Page("GET /studio/analytics", m.page, core.RequireSession)
}

// Start rolls up old raw events a minute after start and then every six hours.
func (m *Module) Start(ctx context.Context) error {
	return m.rollUpLoop(ctx, time.Minute, 6*time.Hour)
}
