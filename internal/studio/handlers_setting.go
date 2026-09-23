package studio

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// SettingView is C5 (world-building).
type SettingView struct {
	Access  *Access
	Project *Project
	Kind    string
	Items   []SettingItem
	URLs    map[string]string
	Counts  map[string]int
	Usage   map[string]SettingUsage
	Edit    *SettingItem
	New     bool
	Errors  map[string]string
}

func (m *Module) settingView(r *http.Request) (*SettingView, error) {
	ctx := r.Context()
	a := AccessFrom(ctx)
	v := &SettingView{Access: a, Project: a.Project, Kind: r.URL.Query().Get("kind"), Errors: map[string]string{}}
	if !slices.Contains(SettingKinds, v.Kind) {
		v.Kind = SettingLocation
	}
	var err error
	if v.Items, err = m.svc.SettingItems(ctx, a.Project.ID, v.Kind); err != nil {
		return nil, err
	}
	var ids []string
	for _, it := range v.Items {
		ids = append(ids, it.AssetID)
	}
	v.URLs = m.svc.AssetURLs(ctx, ids...)
	if v.Counts, err = m.svc.SettingCounts(ctx, a.Project.ID); err != nil {
		return nil, err
	}
	if v.Usage, err = m.svc.SettingUsages(ctx, a.Project.ID); err != nil {
		return nil, err
	}
	switch e := r.URL.Query().Get("edit"); {
	case e == "new" && a.Can(EditSetting):
		v.New = true
	case e != "" && a.Can(EditSetting):
		it, err := m.svc.SettingItem(ctx, a.Project.ID, e)
		if err != nil {
			return nil, err
		}
		v.Edit, v.Kind = it, it.Kind
	}
	return v, nil
}

func (m *Module) settingPage(w http.ResponseWriter, r *http.Request) error {
	v, err := m.settingView(r)
	if err != nil {
		return err
	}
	return m.renderSetting(w, r, http.StatusOK, v)
}

func (m *Module) renderSetting(w http.ResponseWriter, r *http.Request, status int, v *SettingView) error {
	ctx := r.Context()
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(ctx, "studio.setting.title", "title", v.Project.Title),
		MobileTitle: i18n.T(ctx, "nav.setting"), BackURL: "/studio/p/" + v.Project.ID + "/story", Section: layout.SectionStudio,
		Nav: layout.NavSetting, Tab: layout.TabHome}, SettingPage(*v))
}

func (m *Module) settingSave(w http.ResponseWriter, r *http.Request) error {
	if err := parseUpload(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	in := SettingInput{Kind: r.PostFormValue("kind"), Name: r.PostFormValue("name"), Description: r.PostFormValue("description"),
		TrackTimeOfDay: r.PostFormValue("track_time") == "on", Notes: r.PostFormValue("notes")}
	for _, t := range strings.Split(r.PostFormValue("tags"), ",") {
		if t = strings.TrimSpace(t); t != "" && len(in.Tags) < 12 {
			in.Tags = append(in.Tags, clip(t, 30))
		}
	}
	assets, err := m.uploadImages(r, a, "image", "setting")
	if err != nil {
		return err
	}
	if len(assets) > 0 {
		in.AssetID = &assets[0].ID
	} else if r.PostFormValue("remove_image") == "on" {
		empty := ""
		in.AssetID = &empty
	}
	id := r.PathValue("id")
	var it *SettingItem
	if id == "" {
		it, err = m.svc.CreateSettingItem(ctx, a, in)
	} else {
		it, err = m.svc.UpdateSettingItem(ctx, a, id, in)
	}
	if err != nil {
		if ae, ok := err.(*core.APIError); ok && ae.Fields != nil {
			v, verr := m.settingView(r)
			if verr != nil {
				return verr
			}
			v.Errors = ae.Fields
			v.New = id == ""
			return m.renderSetting(w, r, http.StatusUnprocessableEntity, v)
		}
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.setting.saved", "name", it.Name), "")
	core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/setting?kind="+it.Kind)
	return nil
}

func (m *Module) settingDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	it, err := m.svc.SettingItem(ctx, a.Project.ID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := m.svc.DeleteSettingItem(ctx, a, it.ID); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.setting.deleted", "name", it.Name), "")
	core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/setting?kind="+it.Kind)
	return nil
}

// AllSettingView is /studio/setting: the setting of every project.
type AllSettingView struct {
	Projects []ProjectSummary
	Counts   map[string]map[string]int
}

func (m *Module) allSetting(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	list, err := m.svc.ListProjects(ctx, usr.ID, ProjectFilter{})
	if err != nil {
		return err
	}
	if len(list) == 1 {
		core.Redirect(w, r, "/studio/p/"+list[0].ID+"/setting")
		return nil
	}
	v := AllSettingView{Projects: list, Counts: map[string]map[string]int{}}
	for _, p := range list {
		c, err := m.svc.SettingCounts(ctx, p.ID)
		if err != nil {
			return err
		}
		v.Counts[p.ID] = c
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "nav.setting"), Section: layout.SectionStudio,
		Nav: layout.NavSetting, Tab: layout.TabHome}, AllSettingPage(v))
}

// ---- project settings & members ----------------------------------------------------------

// ProjectSettingsView is the project settings page.
type ProjectSettingsView struct {
	Access  *Access
	Project *Project
	Members []Member
	Pins    []RoutingPin
	Extra   templ.Component
	Errors  map[string]string
	Invite  map[string]string
}

// RoutingPin is a routing pin of the project (managed by the ai module).
type RoutingPin struct {
	Task       string `db:"task"`
	ModelID    string `db:"model_id"`
	ProviderID string `db:"provider_id"`
}

func (m *Module) settingsView(r *http.Request) (*ProjectSettingsView, error) {
	ctx := r.Context()
	a := AccessFrom(ctx)
	v := &ProjectSettingsView{Access: a, Project: a.Project, Errors: map[string]string{}, Invite: map[string]string{}}
	var err error
	if v.Members, err = m.svc.Members(ctx, a.Project.ID); err != nil {
		return nil, err
	}
	_ = m.d.DB.Select(ctx, &v.Pins, `SELECT task, model_id, provider_id FROM routing_pins WHERE scope_kind = 'project' AND scope_id = ? ORDER BY task`, a.Project.ID)
	if ext, ok := core.Use[ProjectSettingsSection](m.d); ok {
		if c, err := ext.ProjectSettingsSection(ctx, a.Project, a.Can(EditProject)); err == nil {
			v.Extra = c
		}
	}
	return v, nil
}

func (m *Module) projectSettings(w http.ResponseWriter, r *http.Request) error {
	v, err := m.settingsView(r)
	if err != nil {
		return err
	}
	return m.renderSettings(w, r, http.StatusOK, v)
}

func (m *Module) renderSettings(w http.ResponseWriter, r *http.Request, status int, v *ProjectSettingsView) error {
	ctx := r.Context()
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(ctx, "studio.settings.title", "title", v.Project.Title),
		MobileTitle: i18n.T(ctx, "studio.nav.settings"), BackURL: "/studio/p/" + v.Project.ID + "/story", Section: layout.SectionStudio,
		Nav: layout.NavProjects, Tab: layout.TabHome}, ProjectSettingsPage(*v))
}

func (m *Module) projectSettingsSave(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	pt := ProjectPatch{}
	str := func(field string) *string {
		if _, ok := r.PostForm[field]; !ok {
			return nil
		}
		v := strings.TrimSpace(r.PostFormValue(field))
		return &v
	}
	pt.Title, pt.Premise, pt.Logline = str("title"), str("premise"), str("logline")
	pt.Style, pt.Format, pt.PageSize, pt.Language = str("style"), str("format"), str("page_size"), str("language")
	pt.Visibility, pt.RoutingMode, pt.PagesPerChapter = str("visibility"), str("routing_mode"), str("pages_per_chapter")
	if s := str("chapters_planned"); s != nil {
		n, err := strconv.Atoi(*s)
		if err != nil {
			n = -1
		}
		pt.ChaptersPlanned = &n
	}
	if _, ok := r.PostForm["genres_set"]; ok {
		pt.Genres = append([]string{}, r.PostForm["genres"]...)
	}
	p, err := m.svc.UpdateProject(ctx, a, pt)
	if err != nil {
		if ae, ok := err.(*core.APIError); ok && ae.Fields != nil {
			v, verr := m.settingsView(r)
			if verr != nil {
				return verr
			}
			v.Errors = ae.Fields
			return m.renderSettings(w, r, http.StatusUnprocessableEntity, v)
		}
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.settings.saved"), "")
	core.Redirect(w, r, "/studio/p/"+p.ID+"/settings")
	return nil
}

func readScope(r *http.Request) MemberScope {
	s := MemberScope{Pages: strings.TrimSpace(r.PostFormValue("pages"))}
	if r.PostFormValue("lettering_only") == "on" {
		s.Layers = []string{LayerLettering}
	}
	return s
}

func (m *Module) memberInvite(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	mem, err := m.svc.AddMember(ctx, a, r.PostFormValue("username"), r.PostFormValue("role"), readScope(r))
	if err != nil {
		if ae, ok := err.(*core.APIError); ok && ae.Fields != nil {
			v, verr := m.settingsView(r)
			if verr != nil {
				return verr
			}
			v.Invite = ae.Fields
			return m.renderSettings(w, r, http.StatusUnprocessableEntity, v)
		}
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.members.added", "name", mem.Name()), "")
	core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/settings#members")
	return nil
}

func (m *Module) memberUpdate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	if err := m.svc.UpdateMember(ctx, a, r.PathValue("user"), r.PostFormValue("role"), readScope(r)); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.members.updated"), "")
	core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/settings#members")
	return nil
}

func (m *Module) memberRemove(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	uid := r.PathValue("user")
	if err := m.svc.RemoveMember(ctx, a, uid); err != nil {
		return err
	}
	if uid == a.Actor.UserID {
		web.Flash(w, r, "success", i18n.T(ctx, "studio.members.left", "title", a.Project.Title), "")
		core.Redirect(w, r, "/studio/projects")
		return nil
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.members.removed"), "")
	core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/settings#members")
	return nil
}

func (m *Module) projectArchive(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	archive := r.PostFormValue("archive") != "0"
	if err := m.svc.ArchiveProject(ctx, a, archive); err != nil {
		return err
	}
	if archive {
		web.Flash(w, r, "success", i18n.T(ctx, "studio.settings.archived", "title", a.Project.Title), "")
	} else {
		web.Flash(w, r, "success", i18n.T(ctx, "studio.settings.restored", "title", a.Project.Title), "")
	}
	core.Redirect(w, r, "/studio/p/"+a.Project.ID+"/settings")
	return nil
}

func (m *Module) projectDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	if err := m.svc.DeleteProject(ctx, a, r.PostFormValue("confirm")); err != nil {
		if ae, ok := err.(*core.APIError); ok && ae.Fields != nil {
			v, verr := m.settingsView(r)
			if verr != nil {
				return verr
			}
			v.Errors = ae.Fields
			return m.renderSettings(w, r, http.StatusUnprocessableEntity, v)
		}
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.settings.deleted", "title", a.Project.Title), "")
	core.Redirect(w, r, "/studio/projects")
	return nil
}
