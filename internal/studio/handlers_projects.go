package studio

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// ProjectsView is B2.
type ProjectsView struct {
	Cards     []ProjectCard
	Total     int
	Shared    int
	Query     string
	Status    string
	Format    string
	Sort      string
	View      string
	Filtered  bool
	CanCreate bool
}

// URL returns the projects URL with one parameter changed.
func (v ProjectsView) URL(key, value string) string {
	q := url.Values{}
	set := func(k, val string) {
		if val != "" {
			q.Set(k, val)
		}
	}
	set("q", v.Query)
	set("status", v.Status)
	set("format", v.Format)
	set("sort", v.Sort)
	set("view", v.View)
	if value == "" {
		q.Del(key)
	} else {
		q.Set(key, value)
	}
	if len(q) == 0 {
		return "/studio/projects"
	}
	return "/studio/projects?" + q.Encode()
}

var projectStatuses = []string{BadgeDraft, BadgePublished, BadgeScript, BadgeWebtoon, BadgeImported, "shared"}

func (m *Module) projectsPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	q := r.URL.Query()
	v := ProjectsView{Query: strings.TrimSpace(q.Get("q")), Status: q.Get("status"), Format: q.Get("format"), Sort: q.Get("sort"),
		View: q.Get("view"), CanCreate: usr.CanCreate()}
	if !slices.Contains(projectStatuses, v.Status) {
		v.Status = ""
	}
	if !slices.Contains(Formats, v.Format) {
		v.Format = ""
	}
	if v.Sort != "created" && v.Sort != "title" {
		v.Sort = ""
	}
	if v.View != "list" {
		v.View = ""
	}
	v.Filtered = v.Query != "" || v.Status != "" || v.Format != ""
	list, err := m.svc.ListProjects(ctx, usr.ID, ProjectFilter{Query: v.Query, Badge: v.Status, Format: v.Format, Sort: v.Sort})
	if err != nil {
		return err
	}
	for i := range list {
		v.Cards = append(v.Cards, m.cardProject(ctx, &list[i], usr.ID))
	}
	if v.Total, v.Shared, err = m.svc.CountProjects(ctx, usr.ID); err != nil {
		return err
	}
	if htmx(r) && r.Header.Get("HX-Target") == "mv-projects-results" {
		return web.Fragment(w, r, http.StatusOK, ProjectsResults(v))
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "nav.projects"), Section: layout.SectionStudio,
		Nav: layout.NavProjects, Tab: layout.TabHome}, ProjectsPage(v))
}

// projectHome sends /studio/p/{id} to the story editor.
func (m *Module) projectHome(w http.ResponseWriter, r *http.Request) error {
	core.Redirect(w, r, "/studio/p/"+r.PathValue("project")+"/story")
	return nil
}
