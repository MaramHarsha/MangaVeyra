package studio

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// What names an area of a project a member may change (CanEdit).
const (
	EditProject    = "project"    // title, format, style, visibility, routing
	EditMembers    = "members"    // invite, change roles, remove
	EditStory      = "story"      // arcs, chapters, scenes, prose
	EditScript     = "script"     // panel-by-panel script
	EditCharacters = "characters" // character bible, refs, Character Lock
	EditSetting    = "setting"    // locations, props, style refs
	EditPages      = "pages"      // pages, panels, layout, locks
	EditArt        = "art"        // image layers, generation
	EditLettering  = "lettering"  // bubbles, texts, lettering layers
	EditReview     = "review"     // review status and comments
	EditDanger     = "danger"     // archive, delete (humans only)
)

// roleCaps lists what each role may change.
var roleCaps = map[string][]string{
	RoleOwner:    {EditProject, EditMembers, EditStory, EditScript, EditCharacters, EditSetting, EditPages, EditArt, EditLettering, EditReview, EditDanger},
	RoleEditor:   {EditStory, EditScript, EditCharacters, EditSetting, EditPages, EditArt, EditLettering, EditReview},
	RoleWriter:   {EditStory, EditScript, EditCharacters, EditSetting},
	RoleArtist:   {EditCharacters, EditSetting, EditPages, EditArt},
	RoleLetterer: {EditLettering},
	RoleViewer:   {},
}

// Access is a member's standing in a project, resolved once per request.
type Access struct {
	Project *Project
	Role    string
	Scope   MemberScope
	Actor   Actor
}

// Can reports whether the member may change the given area. Agents never get
// the danger zone or member management (those always need a human).
func (a *Access) Can(what string) bool {
	if a == nil || a.Role == "" {
		return false
	}
	if a.Actor.IsAgent() && (what == EditDanger || what == EditMembers) {
		return false
	}
	if a.Project != nil && a.Project.Status == ProjectArchived && what != EditDanger && what != EditMembers {
		return false
	}
	return slices.Contains(roleCaps[a.Role], what)
}

// IsOwner reports whether the member owns the project.
func (a *Access) IsOwner() bool { return a != nil && a.Role == RoleOwner }

// CanEditPageNumber reports whether the member's scope covers a page number.
func (a *Access) CanEditPageNumber(n int) bool {
	if a == nil {
		return false
	}
	return PageInRange(a.Scope.Pages, n)
}

// CanEditLayerKind reports whether the member's scope covers a layer kind
// (a "lettering layer only" letterer may not touch art layers).
func (a *Access) CanEditLayerKind(kind string) bool {
	if a == nil {
		return false
	}
	if len(a.Scope.Layers) > 0 && !slices.Contains(a.Scope.Layers, kind) {
		return false
	}
	if kind == LayerLettering {
		return a.Can(EditLettering)
	}
	return a.Can(EditArt)
}

// PageInRange reports whether n is inside a page range such as "1-20", "3",
// "1-4, 9" (empty = every page).
func PageInRange(spec string, n int) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(strings.ReplaceAll(part, "–", "-"))
		if part == "" {
			continue
		}
		lo, hi, found := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		if err1 != nil {
			continue
		}
		b := a
		if found {
			if v, err := strconv.Atoi(strings.TrimSpace(hi)); err == nil {
				b = v
			}
		}
		if n >= a && n <= b {
			return true
		}
	}
	return false
}

// ValidPageRange reports whether spec parses as a page range.
func ValidPageRange(spec string) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(strings.ReplaceAll(part, "–", "-"))
		lo, hi, found := strings.Cut(part, "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil || a < 1 {
			return false
		}
		if found {
			b, err := strconv.Atoi(strings.TrimSpace(hi))
			if err != nil || b < a {
				return false
			}
		}
	}
	return true
}

// Access resolves actor's access to a project. It returns a 404 error when
// the project does not exist or the actor is not a member (projects are
// private to their members), so ids of other people's projects leak nothing.
func (s *Service) Access(ctx context.Context, actor Actor, projectID string) (*Access, error) {
	p, err := s.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.accessFor(ctx, actor, p)
}

func (s *Service) accessFor(ctx context.Context, actor Actor, p *Project) (*Access, error) {
	if actor.UserID == "" {
		return nil, core.ErrNotFound()
	}
	if actor.Token != nil && !actor.Token.AllowsProject(p.ID) {
		return nil, core.ErrNotFound()
	}
	var row struct {
		Role  string               `db:"role"`
		Scope db.JSON[MemberScope] `db:"scope"`
	}
	err := s.db.Get(ctx, &row, `SELECT role, scope FROM project_members WHERE project_id = ? AND user_id = ?`, p.ID, actor.UserID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if p.OwnerID == actor.UserID {
			return &Access{Project: p, Role: RoleOwner, Actor: actor}, nil
		}
		return nil, core.ErrNotFound()
	case err != nil:
		return nil, err
	}
	return &Access{Project: p, Role: row.Role, Scope: row.Scope.V, Actor: actor}, nil
}

// CanEdit reports whether user may change the given area of project (what is one of the Edit* constants).
func (s *Service) CanEdit(ctx context.Context, user *core.User, projectID, what string) bool {
	if user == nil {
		return false
	}
	a, err := s.Access(ctx, Actor{Kind: ActorUser, UserID: user.ID, SiteRole: user.Role}, projectID)
	return err == nil && a.Can(what)
}

type accessKey struct{}

// WithAccess attaches a resolved Access to ctx.
func WithAccess(ctx context.Context, a *Access) context.Context {
	return context.WithValue(ctx, accessKey{}, a)
}

// AccessFrom returns the Access resolved by RequireProjectRole (nil outside such routes).
func AccessFrom(ctx context.Context) *Access {
	a, _ := ctx.Value(accessKey{}).(*Access)
	return a
}

// ProjectFrom returns the project resolved by RequireProjectRole.
func ProjectFrom(ctx context.Context) *Project {
	if a := AccessFrom(ctx); a != nil {
		return a.Project
	}
	return nil
}

// RequireProjectRole resolves the {project} path value, checks that the
// caller is a member with one of roles (owners always pass; no roles = any
// member, including viewers) and stores the Access in the request context.
// Non-members get 404. Agent tokens must allow the project.
func (s *Service) RequireProjectRole(roles ...string) core.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			id := r.PathValue("project")
			if id == "" || !db.ValidID(id) {
				httpx.WriteError(w, r, core.ErrNotFound())
				return
			}
			a, err := s.Access(ctx, ActorFrom(ctx), id)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if len(roles) > 0 && a.Role != RoleOwner && !slices.Contains(roles, a.Role) {
				httpx.WriteError(w, r, core.ErrForbidden(i18n.T(ctx, "studio.error.role")))
				return
			}
			next.ServeHTTP(w, r.WithContext(WithAccess(ctx, a)))
		})
	}
}

// RequireProjectRole is the package-level form used in route tables:
// studio.RequireProjectRole(d, "editor").
func RequireProjectRole(d *core.Deps, roles ...string) core.Middleware {
	return core.MustUse[*Service](d).RequireProjectRole(roles...)
}

// ErrLockedScope is returned (wrapped in a 423 APIError) when a scope touches locked work.
var ErrLockedScope = errors.New("studio: scope is locked")

func lockedErr(ctx context.Context, what string) error {
	e := core.ErrLocked(i18n.T(ctx, "studio.error.locked", "what", what))
	e.Err = ErrLockedScope
	return e
}

// CheckScopeWritable is the one place that enforces product rules 3 and 4
// for AI, batch and agent actions: the scope must name existing objects of
// one project that belong together, the actor must be a member allowed to
// change that area, and nothing in the scope may be locked (chapter, page,
// panel or layer locks; agents can never unlock). It is registered as a jobs
// enqueue validator, so every job with a project scope passes through it.
func (s *Service) CheckScopeWritable(ctx context.Context, actor Actor, scope jobs.Scope) error {
	if scope.ProjectID == "" {
		if scope.Kind == jobs.ScopeNone {
			return nil
		}
		return core.ErrBadRequest("scope needs project_id")
	}
	a, err := s.Access(ctx, actor, scope.ProjectID)
	if err != nil {
		return err
	}
	if a.Project.Status == ProjectArchived {
		return lockedErr(ctx, i18n.T(ctx, "studio.lock.archived"))
	}
	need := EditArt
	switch scope.Kind {
	case jobs.ScopeProject, jobs.ScopeChapter:
		need = ""
	}
	if need != "" && !a.Can(need) && !a.Can(EditLettering) {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	if need == "" && !a.Can(EditStory) && !a.Can(EditArt) && !a.Can(EditCharacters) && !a.Can(EditLettering) {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}

	// Objects that do not exist (yet) hold nothing to protect; objects that
	// exist must belong to the scope's project and to each other.
	gone := func(err error) bool { return errors.Is(err, sql.ErrNoRows) || isNotFound(err) }
	chapterID := scope.ChapterID
	var page *Page
	if scope.PageID != "" {
		p, err := s.Page(ctx, scope.PageID)
		switch {
		case err == nil:
			if p.ProjectID != scope.ProjectID {
				return core.ErrBadRequest("page is not part of the project")
			}
			if chapterID != "" && chapterID != p.ChapterID {
				return core.ErrBadRequest("page is not part of the chapter")
			}
			page, chapterID = p, p.ChapterID
			if !a.CanEditPageNumber(p.Number) {
				return core.ErrForbidden(i18n.T(ctx, "studio.error.page_scope"))
			}
		case !gone(err):
			return err
		}
	}
	var panel *Panel
	if scope.PanelID != "" {
		pn, err := s.Panel(ctx, scope.PanelID)
		switch {
		case err == nil:
			if pn.ProjectID != scope.ProjectID || (scope.PageID != "" && pn.PageID != scope.PageID) {
				return core.ErrBadRequest("panel is not part of the page")
			}
			panel = pn
			if page == nil {
				if page, err = s.Page(ctx, pn.PageID); err != nil {
					return err
				}
				chapterID = page.ChapterID
				if !a.CanEditPageNumber(page.Number) {
					return core.ErrForbidden(i18n.T(ctx, "studio.error.page_scope"))
				}
			}
		case !gone(err):
			return err
		}
	}
	var layer *Layer
	if scope.LayerID != "" {
		l, err := s.Layer(ctx, scope.LayerID)
		switch {
		case err == nil:
			if panel == nil || l.PanelID != panel.ID {
				return core.ErrBadRequest("layer is not part of the panel")
			}
			layer = l
			if !a.CanEditLayerKind(l.Kind) {
				return core.ErrForbidden(i18n.T(ctx, "studio.error.layer_scope"))
			}
		case !gone(err):
			return err
		}
	} else if (scope.Kind == jobs.ScopePanel || scope.Kind == jobs.ScopeRegion) && !a.Can(EditArt) {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	if chapterID != "" {
		ch, err := s.ChapterByID(ctx, chapterID)
		switch {
		case err == nil:
			if ch.ProjectID != scope.ProjectID {
				return core.ErrBadRequest("chapter is not part of the project")
			}
			if ch.Locked {
				return lockedErr(ctx, i18n.T(ctx, "studio.lock.chapter", "n", ch.Number))
			}
		case !gone(err):
			return err
		}
	}
	if page != nil && page.Locked {
		return lockedErr(ctx, i18n.T(ctx, "studio.lock.page", "n", page.Number))
	}
	if panel != nil && panel.Locked {
		return lockedErr(ctx, i18n.T(ctx, "studio.lock.panel"))
	}
	if layer != nil && layer.Locked {
		return lockedErr(ctx, i18n.T(ctx, "studio.lock.layer"))
	}
	return nil
}

// isNotFound reports a 404 APIError (the repositories map missing rows to it).
func isNotFound(err error) bool {
	var ae *core.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// LockedIn lists the locked pages and panels inside a chapter, so batch
// handlers with a chapter scope can skip them (they must never change them).
func (s *Service) LockedIn(ctx context.Context, chapterID string) (pages, panels []string, err error) {
	if err = s.db.Select(ctx, &pages, `SELECT id FROM pages WHERE chapter_id = ? AND locked = ? ORDER BY number`, chapterID, true); err != nil {
		return nil, nil, err
	}
	err = s.db.Select(ctx, &panels, `SELECT pn.id FROM panels pn JOIN pages pg ON pg.id = pn.page_id
		WHERE pg.chapter_id = ? AND (pn.locked = ? OR pg.locked = ?) ORDER BY pg.number, pn.order_idx`, chapterID, true, true)
	return pages, panels, err
}

// scopeValidator adapts CheckScopeWritable to the jobs queue. The actor is
// the request identity (agents stay agents); background enqueues without a
// request identity act for the job's user.
func (s *Service) scopeValidator(ctx context.Context, p *jobs.EnqueueParams) error {
	if p.Scope.ProjectID == "" {
		return nil
	}
	actor := ActorFrom(ctx)
	if actor.UserID == "" {
		actor = SystemActor(p.ParentID, p.UserID)
	}
	if actor.UserID == "" {
		return core.ErrForbidden("job has no user")
	}
	if p.UserID != "" && p.UserID != actor.UserID {
		return core.ErrForbidden("job user does not match the caller")
	}
	return s.CheckScopeWritable(ctx, actor, p.Scope)
}
