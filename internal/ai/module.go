package ai

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/setup"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Module is the ai feature module.
type Module struct {
	d           *core.Deps
	providers   *providerStore
	providerAPI *providerService
	text        *textService
	spend       *SpendStore
	now         func() time.Time
	repairEvery time.Duration
	repairBatch int
	ui          *web.UI
}

// New creates the module.
func New(d *core.Deps) *Module { return newModule(d, newOpenRouterHTTPClient(), db.Now) }

func newModule(d *core.Deps, doer httpDoer, now func() time.Time) *Module {
	if now == nil {
		now = db.Now
	}
	providers := newProviderStore(d.DB, d.Secrets, d.Settings, now)
	client := newOpenRouterClient(doer)
	openai := newOpenAIClient(doer)
	spend := NewSpendStore(d.DB, d.Settings)
	spend.Now = now
	text := newTextService(d.DB, d.Settings, providers, client, spend, now).withClient(providerKindOpenAI, openai)
	m := &Module{d: d, providers: providers, providerAPI: &providerService{store: providers, settings: d.Settings, client: client,
		testers: map[string]remoteKeyTester{providerKindOpenAI: openai}},
		text: text, spend: spend, now: now, repairEvery: time.Minute, repairBatch: 1000}
	core.Provide[TextService](d, text)
	core.Provide[setup.ModelStep](d, m)
	return m
}

// Name implements core.Module.
func (m *Module) Name() string { return "ai" }

// Init repairs one bounded batch before the executor can dispatch remote work.
func (m *Module) Init(ctx context.Context) error {
	if ui, ok := core.Use[*web.UI](m.d); ok {
		m.ui = ui
	}
	_, err := m.spend.RepairExpiredRemoteCalls(ctx, m.timeNow(), m.repairBatch)
	return err
}

// Start periodically closes reservations orphaned by crashes. Network I/O is
// never part of this loop.
func (m *Module) Start(ctx context.Context) error {
	every := m.repairEvery
	if every <= 0 {
		every = time.Minute
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := m.spend.RepairExpiredRemoteCalls(ctx, m.timeNow(), m.repairBatch); err != nil && m.d.Log != nil {
				m.d.Log.WarnContext(ctx, "repair expired remote spend reservations", "err", err)
			}
		}
	}
}

func (m *Module) timeNow() time.Time {
	if m == nil || m.now == nil {
		return db.Now()
	}
	return m.now().UTC().Truncate(time.Microsecond)
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	r.Page("GET /models", m.modelsPage, core.RequireSession)
	r.Page("GET /models/providers/new", m.newProviderPage, core.RequireSession)
	r.Page("POST /models/providers", m.createProvider, core.RequireSession)
	r.Page("POST /models/providers/{id}/test", m.testProvider, core.RequireSession)
	r.Page("POST /models/providers/{id}/enabled", m.setProviderEnabled, core.RequireSession)
	r.Page("POST /models/providers/{id}/key", m.replaceProviderKey, core.RequireSession)
}
