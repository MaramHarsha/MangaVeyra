package ai

import (
	"context"
	"net/http"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/setup"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Module is the ai feature module.
type Module struct {
	d           *core.Deps
	providers   *providerStore
	providerAPI *providerService
	text        *textService
	image       *imageService
	catalog     *modelCatalog
	spend       *SpendStore
	private     *privateEndpointPolicy
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
	text := newTextService(d.DB, d.Settings, providers, client, spend, now).withClient(providerKindOpenAI, openai).
		withClient(providerKindGoogle, newGoogleTextClient(doer)).
		withClient(providerKindXAI, newXAITextClient(doer)).
		withClient(providerKindTogether, newTogetherTextClient(doer))
	text.hfDoer = doer
	private := &privateEndpointPolicy{fallback: d.Cfg != nil && d.Cfg.AllowPrivateProviderURLs}
	if d.Settings != nil {
		private.keys = d.Settings
	}
	text.localDoer = newLocalTextHTTPClient(private)
	directory := ""
	if d.Cfg != nil {
		directory = d.Cfg.ManifestDir()
	}
	catalog := &modelCatalog{db: d.DB, directory: directory}
	// Image calls may take minutes (OpenAI documents up to two for complex
	// prompts); they get the same guarded transport with a longer client
	// timeout than text, still bounded by postImage's per-call deadline.
	imageDoer := doer
	if c, ok := doer.(*http.Client); ok {
		clone := *c
		clone.Timeout = imageHTTPTimeout
		imageDoer = &clone
	}
	images := &imageService{d: d, providers: providers, catalog: catalog, spend: text, doer: imageDoer,
		localDoer: text.localDoer, hfDoer: imageDoer}
	m := &Module{d: d, image: images, catalog: catalog, providers: providers, providerAPI: &providerService{store: providers, settings: d.Settings, client: client,
		testers: map[string]remoteKeyTester{providerKindOpenAI: openai, providerKindFal: falKeyTester{doer: doer},
			providerKindGoogle: googleKeyTester{doer: doer}, providerKindXAI: xaiKeyTester{doer: doer},
			providerKindTogether: togetherKeyTester{doer: doer}, providerKindReplicate: replicateKeyTester{doer: doer},
			providerKindRunware: runwareKeyTester{doer: doer}},
		localDoer: text.localDoer, hfDoer: doer},
		text: text, spend: spend, private: private, now: now, repairEvery: time.Minute, repairBatch: 1000}
	core.Provide[TextService](d, text)
	core.Provide[ImageService](d, images)
	core.Provide[ImageFallback](d, images.fallback)
	core.Provide[jobs.ManifestLookup](d, catalog.workerManifest)
	if d.Jobs != nil {
		d.Jobs.MarkPrivateInput("image.generate", "image.edit", "image.inpaint", TaskCharacterSheet)
	}
	core.Provide[setup.ModelStep](d, m)
	core.Provide[RouteNamer](d, m)
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
	r.Page("GET /models/routing", m.routingPage, core.RequireSession)
	r.Page("POST /models/routing", m.saveRouting, core.RequireSession)
	r.Page("POST /models/routing/pins", m.savePin, core.RequireSession)
	r.Page("POST /models/routing/pins/{id}/delete", m.deletePin, core.RequireSession)
	for _, step := range customSteps {
		r.Page("GET /models/providers/new/"+step, m.customPage, core.RequireSession)
		r.Page("POST /models/providers/new/"+step, m.customPost, core.RequireSession)
	}
	r.Page("GET /models/providers/custom", func(w http.ResponseWriter, r *http.Request) error {
		core.Redirect(w, r, customURL("connect", ""))
		return nil
	}, core.RequireSession)
	r.Page("GET /models/providers/new/fal", func(w http.ResponseWriter, r *http.Request) error {
		q := r.URL.Query()
		q.Set("kind", providerKindFal)
		r.URL.RawQuery = q.Encode()
		return m.newProviderPage(w, r)
	}, core.RequireSession)
	r.Page("GET /models/providers/new/ollama", func(w http.ResponseWriter, r *http.Request) error {
		core.Redirect(w, r, customURL("connect", ""))
		return nil
	}, core.RequireSession)
	r.Page("GET /models", m.modelsPage, core.RequireSession)
	r.Page("GET /models/providers/new", m.newProviderPage, core.RequireSession)
	r.Page("POST /models/providers", m.createProvider, core.RequireSession)
	r.Page("POST /models/providers/{id}/test", m.testProvider, core.RequireSession)
	r.Page("POST /models/providers/{id}/enabled", m.setProviderEnabled, core.RequireSession)
	r.Page("POST /models/providers/{id}/key", m.replaceProviderKey, core.RequireSession)
}
