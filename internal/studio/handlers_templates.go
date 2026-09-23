package studio

import (
	"net/http"
	"strconv"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Template tabs (B4).
const (
	TabStory      = "story"
	TabLayout     = "layout"
	TabCharacters = "characters"
	TabStyles     = "styles"
)

// Pack is an installed .mvpack (listed on the Character packs / Style packs tabs).
type Pack struct {
	ID       string `db:"id"`
	Kind     string `db:"kind"`
	Name     string `db:"name"`
	Version  string `db:"version"`
	Author   string `db:"author"`
	Licence  string `db:"licence"`
	Source   string `db:"source"`
	Verified bool   `db:"verified"`
}

// TemplatesView is B4.
type TemplatesView struct {
	Tab     string
	Layouts []LayoutTemplate
	Stories []StoryTemplate
	Packs   []Pack
	Counts  map[string]int
}

func (m *Module) templatesPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	v := TemplatesView{Tab: r.URL.Query().Get("tab"), Counts: map[string]int{}}
	switch v.Tab {
	case TabStory, TabLayout, TabCharacters, TabStyles:
	default:
		v.Tab = TabLayout
	}
	v.Layouts = BuiltinLayouts(ctx)
	if more, err := m.svc.UserLayouts(ctx, usr.ID); err == nil {
		v.Layouts = append(v.Layouts, more...)
	}
	v.Stories = BuiltinStories(ctx)
	if more, err := m.svc.UserStories(ctx, usr.ID); err == nil {
		v.Stories = append(v.Stories, more...)
	}
	var packs []Pack
	_ = m.d.DB.Select(ctx, &packs, `SELECT id, kind, name, version, author, licence, source, verified FROM packs
		WHERE (owner_id = ? OR owner_id IS NULL) AND kind IN ('character', 'style') AND installed_at IS NOT NULL ORDER BY name`, usr.ID)
	for _, p := range packs {
		v.Counts[p.Kind]++
		if (v.Tab == TabCharacters && p.Kind == "character") || (v.Tab == TabStyles && p.Kind == "style") {
			v.Packs = append(v.Packs, p)
		}
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "nav.templates"), MobileTitle: i18n.T(ctx, "nav.templates"),
		BackURL: "/studio", Section: layout.SectionStudio, Nav: layout.NavTemplates, Tab: layout.TabHome}, TemplatesPage(v))
}

// TemplateView is one template's detail page.
type TemplateView struct {
	Layout *LayoutTemplate
	Story  *StoryTemplate
	Pages  []PageChoice
	Rows   int
	Cols   int
	Own    bool
}

// PageChoice is a page the member may apply a layout to.
type PageChoice struct {
	ProjectID string `db:"project_id"`
	PageID    string `db:"page_id"`
	Title     string `db:"title"`
	Chapter   int    `db:"chapter"`
	Page      int    `db:"page"`
}

func (m *Module) templatePage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	id := r.PathValue("id")
	v := TemplateView{Rows: 3, Cols: 3}
	if id == "builtin:grid_3x3" || len(id) > 5 && id[:5] == "grid:" {
		q := r.URL.Query()
		if n, err := strconv.Atoi(q.Get("rows")); err == nil && n >= 1 && n <= 8 {
			v.Rows = n
		}
		if n, err := strconv.Atoi(q.Get("cols")); err == nil && n >= 1 && n <= 6 {
			v.Cols = n
		}
		id = "grid:" + strconv.Itoa(v.Rows) + "x" + strconv.Itoa(v.Cols)
	}
	if t, err := m.svc.Layout(ctx, usr.ID, id); err == nil {
		v.Layout = t
	} else if st, err := m.svc.Story(ctx, usr.ID, id); err == nil {
		v.Story = st
	} else {
		return core.ErrNotFound()
	}
	if db.ValidID(r.PathValue("id")) {
		var owner string
		_ = m.d.DB.Get(ctx, &owner, `SELECT COALESCE(owner_id, '') FROM templates WHERE id = ?`, r.PathValue("id"))
		v.Own = owner == usr.ID
	}
	if v.Layout != nil {
		_ = m.d.DB.Select(ctx, &v.Pages, `SELECT p.id AS project_id, pg.id AS page_id, p.title, c.number AS chapter, pg.number AS page
			FROM pages pg JOIN chapters c ON c.id = pg.chapter_id JOIN projects p ON p.id = pg.project_id
			JOIN project_members m ON m.project_id = p.id AND m.user_id = ?
			WHERE m.role IN ('owner', 'editor', 'artist') AND pg.locked = ? AND c.locked = ? AND p.status <> 'archived'
			ORDER BY pg.updated_at DESC LIMIT 60`, usr.ID, false, false)
	}
	title := ""
	if v.Layout != nil {
		title = v.Layout.Name
	} else {
		title = v.Story.Name
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: title, BackURL: "/studio/templates", Section: layout.SectionStudio,
		Nav: layout.NavTemplates, Tab: layout.TabHome}, TemplateDetail(v))
}

func (m *Module) deleteTemplate(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.svc.DeleteTemplate(ctx, core.UserFrom(ctx).ID, r.PathValue("id")); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.tpl.deleted"), "")
	core.Redirect(w, r, "/studio/templates")
	return nil
}

// pageLayout applies a layout template to a page (POST /studio/p/{project}/pages/{page}/layout).
func (m *Module) pageLayout(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	a := AccessFrom(ctx)
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	pg, err := m.svc.Page(ctx, r.PathValue("page"))
	if err != nil || pg.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	idx, _ := strconv.Atoi(r.PostFormValue("index"))
	if _, err := m.svc.ApplyLayout(ctx, a.Actor, pg.ID, web.FormValue(r, "layout"), idx); err != nil {
		return err
	}
	ch, _ := m.svc.ChapterByID(ctx, pg.ChapterID)
	web.Flash(w, r, "success", i18n.T(ctx, "studio.tpl.applied", "page", pg.Number), "")
	target := "/studio/p/" + a.Project.ID + "/story"
	if ch != nil {
		target = "/studio/p/" + a.Project.ID + "/chapters/" + itoa(ch.Number) + "/edit?page=" + itoa(pg.Number)
	}
	core.Redirect(w, r, core.SafeRedirectTarget(r.PostFormValue("next"), target))
	return nil
}

// applyTemplate applies a layout template to a page picked on the template page.
func (m *Module) applyTemplate(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	pg, err := m.svc.Page(ctx, web.FormValue(r, "page"))
	if err != nil {
		return core.ErrNotFound()
	}
	actor := ActorFrom(ctx)
	if _, err := m.svc.Access(ctx, actor, pg.ProjectID); err != nil {
		return err
	}
	id := r.PathValue("id")
	if rows, cols := web.FormValue(r, "rows"), web.FormValue(r, "cols"); rows != "" && cols != "" {
		id = "grid:" + rows + "x" + cols
	}
	idx, _ := strconv.Atoi(r.PostFormValue("index"))
	if _, err := m.svc.ApplyLayout(ctx, actor, pg.ID, id, idx); err != nil {
		return err
	}
	ch, err := m.svc.ChapterByID(ctx, pg.ChapterID)
	if err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.tpl.applied", "page", pg.Number), "")
	core.Redirect(w, r, "/studio/p/"+pg.ProjectID+"/chapters/"+itoa(ch.Number)+"/edit?page="+itoa(pg.Number))
	return nil
}
