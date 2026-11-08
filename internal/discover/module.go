package discover

import (
	"context"
	"net/http"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Module is the discover feature module: E6 Explore and E8 Search.
type Module struct {
	d       *core.Deps
	ui      *web.UI
	publish publish.Service
	assets  *assets.Service
	// follows is optional: without the social module no follow state is shown.
	follows social.Follows
	now     func() time.Time
}

// New creates the module.
func New(d *core.Deps) *Module { return &Module{d: d, now: db.Now} }

// Name implements core.Module.
func (m *Module) Name() string { return "discover" }

// Init resolves the publish read side, assets, the web layer and (when the
// social module provides it) follows.
func (m *Module) Init(context.Context) error {
	m.ui = web.From(m.d)
	m.publish = core.MustUse[publish.Service](m.d)
	m.assets = core.MustUse[*assets.Service](m.d)
	if f, ok := core.Use[social.Follows](m.d); ok {
		m.follows = f
	}
	return nil
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	r.Page("GET /explore", m.explorePage)
	r.Page("GET /search", m.searchPage)
}

// viewer returns the publish viewer of the request, or ok=false (after
// redirecting to sign-in) when the instance mode keeps anonymous visitors
// out: only public servers show Explore and search without an account.
func (m *Module) viewer(w http.ResponseWriter, r *http.Request) (publish.Viewer, bool, error) {
	if u := core.UserFrom(r.Context()); u != nil && u.Status == "active" {
		return publish.Viewer{UserID: u.ID, Admin: u.IsAdmin()}, true, nil
	}
	inst, err := m.d.Settings.Instance(r.Context())
	if err != nil {
		return publish.Viewer{}, false, err
	}
	if !inst.ExplorePublic() {
		core.Redirect(w, r, httpx.LoginURL(r))
		return publish.Viewer{}, false, nil
	}
	return publish.Viewer{}, true, nil
}
