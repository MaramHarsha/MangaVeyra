package mcp

import (
	"context"
	"sync"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Module is the mcp feature module: the MCP server at /mcp, G13 at /agents
// and the agent approvals and undo behind them.
type Module struct {
	d       *core.Deps
	ui      *web.UI
	studio  studio.Service
	bible   studio.BibleService
	gen     gen.Service
	control gen.ControlService
	pages   pagestore.Service
	assets  *assets.Service

	// quote estimates a generation request; tests replace it to simulate a
	// remote route without a network call.
	quote func(ctx context.Context, a *agent, req genRequest) (quoteResult, error)

	servers  sync.Map // scope set → *mcpsdk.Server
	requests *core.Limiter
	writes   *core.Limiter
}

// New creates the module.
func New(d *core.Deps) *Module {
	m := &Module{d: d,
		// Per token: 5 MCP requests a second (bursts of 60) and one write a
		// second (bursts of 20), on top of the server's per-user API limit.
		requests: core.NewLimiter(5, 60),
		writes:   core.NewLimiter(1, 20),
	}
	m.quote = m.dryRunQuote
	return m
}

// Name implements core.Module.
func (m *Module) Name() string { return "mcp" }

// Init resolves the services the tools act through (the same ones the UI
// uses) and installs the enqueue hook behind generation estimates.
func (m *Module) Init(context.Context) error {
	m.ui = web.From(m.d)
	m.studio = core.MustUse[studio.Service](m.d)
	m.bible = core.MustUse[studio.BibleService](m.d)
	m.gen = core.MustUse[gen.Service](m.d)
	m.control = core.MustUse[gen.ControlService](m.d)
	m.pages = core.MustUse[pagestore.Service](m.d)
	m.assets = core.MustUse[*assets.Service](m.d)
	m.d.Jobs.AddValidator(func(ctx context.Context, p *jobs.EnqueueParams) error {
		return captureQuote(ctx, p)
	})
	return nil
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	// The MCP endpoint takes every method; the SDK answers GET and DELETE on
	// this stateless server with 405.
	r.Handle("/mcp", m.mcpHandler())
	r.Page("GET /agents", m.agentsPage, core.RequireSession)
	r.Page("POST /agents/allow", m.setAllowed, core.RequireSession)
	r.Page("POST /agents/tokens", m.createToken, core.RequireSession)
	r.Page("POST /agents/tokens/{id}/revoke", m.revokeToken, core.RequireSession)
	r.Page("POST /agents/actions/{id}/approve", m.approveAction, core.RequireSession)
	r.Page("POST /agents/actions/{id}/decline", m.declineAction, core.RequireSession)
	r.Page("POST /agents/actions/{id}/undo", m.undoAction, core.RequireSession)
}
