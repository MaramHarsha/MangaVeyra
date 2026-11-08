package social

import (
	"context"
	"errors"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Write rate limits, per signed-in account (ADR 0012 §9).
const (
	commentRate, commentBurst = 1.0 / 15, 5 // new comments: 5 at once, then one every 15 s
	editRate, editBurst       = 0.5, 20     // edits, deletes, hides
	reportRate, reportBurst   = 1.0 / 60, 5 // reports: 5 at once, then one a minute
	followRate, followBurst   = 1, 30       // follow toggles
)

// Module is the social feature module: E5, F1, follows, comments, reports.
type Module struct {
	d      *core.Deps
	svc    *service
	ui     *web.UI
	assets *assets.Service

	commentL, editL, reportL, followL *core.Limiter
}

// New creates the module and provides social.Follows.
func New(d *core.Deps) *Module {
	svc := &service{db: d.DB, auth: d.Auth, now: db.Now}
	core.Provide[Follows](d, svc)
	return &Module{d: d, svc: svc,
		commentL: core.NewLimiter(commentRate, commentBurst),
		editL:    core.NewLimiter(editRate, editBurst),
		reportL:  core.NewLimiter(reportRate, reportBurst),
		followL:  core.NewLimiter(followRate, followBurst),
	}
}

// Name implements core.Module.
func (m *Module) Name() string { return "social" }

// Init resolves the publish read side, the web layer and assets.
func (m *Module) Init(context.Context) error {
	var ok bool
	if m.svc.pub, ok = core.Use[publish.Service](m.d); !ok {
		return errors.New("social: publish service unavailable")
	}
	if m.ui, ok = core.Use[*web.UI](m.d); !ok {
		return errors.New("social: web UI unavailable")
	}
	m.assets = core.MustUse[*assets.Service](m.d)
	return nil
}

// Routes mounts E5, F1, their no-JavaScript forms and the JSON API. Reads are
// open (publish.Service decides what each viewer sees); writes need a
// signed-in browser session and are rate limited.
func (m *Module) Routes(r *core.Router) {

	api := r.Group("/api/v1/social")
	api.API("GET /comments", m.apiListComments)
	api.API("POST /comments", m.apiCreateComment, core.RequireSession)
	api.API("PATCH /comments/{id}", m.apiEditComment, core.RequireSession)
	api.API("DELETE /comments/{id}", m.apiDeleteComment, core.RequireSession)
	api.API("POST /comments/{id}/hide", m.apiHideComment, core.RequireSession)
	api.API("DELETE /comments/{id}/hide", m.apiUnhideComment, core.RequireSession)
	api.API("POST /reports", m.apiReport, core.RequireSession)
	api.API("GET /series/{id}/follow", m.apiSeriesFollowState, core.RequireUser)
	api.API("PUT /series/{id}/follow", m.apiFollowSeries, core.RequireSession)
	api.API("DELETE /series/{id}/follow", m.apiUnfollowSeries, core.RequireSession)
	api.API("GET /users/{username}/follow", m.apiUserFollowState, core.RequireUser)
	api.API("PUT /users/{username}/follow", m.apiFollowUser, core.RequireSession)
	api.API("DELETE /users/{username}/follow", m.apiUnfollowUser, core.RequireSession)
}

// allow applies a per-account limiter.
func allow(l *core.Limiter, r *http.Request) error {
	u := core.UserFrom(r.Context())
	if u == nil {
		return core.ErrUnauthorized()
	}
	if ok, wait := l.Allow("u:" + u.ID); !ok {
		e := core.NewError(http.StatusTooManyRequests, "rate_limited", "")
		e.RetryAfter = wait
		return e
	}
	return nil
}

// apiError maps service errors onto the JSON error envelope.
func apiError(ctx context.Context, err error) error {
	var ie *invalidError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ie):
		return core.ErrValidation(map[string]string{ie.field: i18n.T(ctx, ie.key)})
	case errors.Is(err, ErrInvalid):
		return core.ErrBadRequest(i18n.T(ctx, "social.error.invalid"))
	case errors.Is(err, ErrNotFound):
		return core.ErrNotFound()
	case errors.Is(err, ErrForbidden):
		return core.ErrForbidden("")
	}
	return err
}

// viewer is the request's publish viewer.
func viewer(r *http.Request) publish.Viewer { return viewerOf(core.UserFrom(r.Context())) }
