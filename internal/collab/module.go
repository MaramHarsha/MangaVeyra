package collab

import (
	"context"
	"errors"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/ws"
)

// Module is the collaboration feature module.
type Module struct {
	d       *core.Deps
	studio  studio.Service
	targets studio.TargetAuthorizer
	pages   pagestore.Service
}

// New creates the module.
func New(d *core.Deps) *Module { return &Module{d: d} }

// Name implements core.Module.
func (m *Module) Name() string { return "collab" }

// Init resolves services and authorizes the page document topic used for
// presence (live cursors) and review events.
func (m *Module) Init(context.Context) error {
	m.studio = core.MustUse[studio.Service](m.d)
	m.targets = core.MustUse[studio.TargetAuthorizer](m.d)
	m.pages = core.MustUse[pagestore.Service](m.d)
	if m.d.Hub != nil {
		m.d.Hub.Authorize(ws.DocPagePrefix, m.authorizeDoc)
	}
	return nil
}

// authorizeDoc lets project readers follow doc:page:{id} (presence and
// review events). Writing the page's Yjs document needs the right to change
// that page (art or lettering, under the member scope).
func (m *Module) authorizeDoc(ctx context.Context, id httpx.Identity, topic string, write bool) error {
	pageID := strings.TrimPrefix(topic, ws.DocPagePrefix)
	if id.User == nil || !db.ValidID(pageID) {
		return ws.ErrForbidden
	}
	var projectID string
	if err := m.d.DB.Get(ctx, &projectID, `SELECT project_id FROM pages WHERE id = ?`, pageID); err != nil {
		if db.IsNotFound(err) {
			return ws.ErrForbidden
		}
		return err
	}
	actor := studio.Actor{UserID: id.User.ID, SiteRole: id.User.Role}
	if id.Token != nil {
		actor.TokenID = id.Token.ID
	}
	if !write {
		return docErr(m.studio.Authorize(ctx, actor, projectID, studio.ActRead))
	}
	t := jobs.Scope{Kind: jobs.ScopePage, ProjectID: projectID, PageID: pageID}
	err := m.targets.AuthorizeTarget(ctx, actor, t, studio.ActEditArt)
	if errors.Is(err, studio.ErrForbidden) {
		err = m.targets.AuthorizeTarget(ctx, actor, t, studio.ActLetter)
	}
	return docErr(err)
}

func docErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, studio.ErrForbidden) || errors.Is(err, studio.ErrNotFound) || errors.Is(err, studio.ErrInvalid) {
		return ws.ErrForbidden
	}
	return err
}

// Routes implements core.Module. Every route is JSON for the editor island;
// the editor itself needs JavaScript, so there is no form fallback, and the
// server validates every input.
func (m *Module) Routes(r *core.Router) {
	base := "/api/v1/collab/p/{project}"
	r.API("GET "+base+"/pages/{page}/review", m.getReview, core.RequireUser)
	r.API("POST "+base+"/pages/{page}/comments", m.postComment, core.RequireUser)
	r.API("POST "+base+"/pages/{page}/approval", m.postApproval, core.RequireSession)
	r.API("POST "+base+"/comments/{comment}/replies", m.postReply, core.RequireUser)
	r.API("POST "+base+"/comments/{comment}/status", m.postStatus, core.RequireSession)
	r.API("GET "+base+"/members", m.getMembers, core.RequireUser)
	r.API("POST "+base+"/members", m.postMember, core.RequireSession)
	r.API("PATCH "+base+"/members/{user}", m.patchMember, core.RequireSession)
	r.API("DELETE "+base+"/members/{user}", m.deleteMember, core.RequireSession)
}
