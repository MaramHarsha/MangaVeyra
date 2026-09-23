package studio

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Camera presets offered in the script editor.
var CameraPresets = []string{"wide", "medium", "close", "extreme close", "low", "high", "over-shoulder", "bird's-eye", "dutch"}

// StoryView is C1, the story editor.
type StoryView struct {
	Access     *Access
	Project    *Project
	Arcs       []Arc
	Chapters   []Chapter
	Chapter    *Chapter
	Scenes     []Scene
	Scene      *Scene
	SceneIdx   int
	Script     []ScriptPanel
	Tab        string
	Issues     int
	Characters []Character
	Locations  []SettingItem
	Assists    []ScriptAssist
	Route      RoutePreview
	EditID     string
	Rename     string
	Pages      int
	Errors     web.FormErrors
}

// Base is the project URL prefix.
func (v StoryView) Base() string { return "/studio/p/" + v.Project.ID }

// SceneURL links to a scene of the current chapter.
func (v StoryView) SceneURL(i int, tab string) string {
	u := v.Base() + "/story/" + itoa(v.Chapter.Number) + "/" + itoa(i+1)
	if tab == "prose" {
		u += "?tab=prose"
	}
	return u
}

// Here is the current story URL (for redirects back).
func (v StoryView) Here() string {
	if v.Chapter == nil {
		return v.Base() + "/story"
	}
	if v.Scene == nil {
		return v.Base() + "/story/" + itoa(v.Chapter.Number)
	}
	return v.SceneURL(v.SceneIdx, v.Tab)
}

// CharName returns a character's name by id.
func (v StoryView) CharName(id string) string {
	for _, c := range v.Characters {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

func (m *Module) loadStory(r *http.Request) (*StoryView, error) {
	ctx := r.Context()
	a := AccessFrom(ctx)
	v := &StoryView{Access: a, Project: a.Project, Tab: r.URL.Query().Get("tab"), EditID: r.URL.Query().Get("edit"),
		Rename: r.URL.Query().Get("rename"), Errors: web.FormErrors{}}
	if v.Tab != "prose" {
		v.Tab = "panels"
	}
	var err error
	if v.Arcs, err = m.svc.Arcs(ctx, a.Project.ID); err != nil {
		return nil, err
	}
	if v.Chapters, err = m.svc.Chapters(ctx, a.Project.ID); err != nil {
		return nil, err
	}
	if len(v.Chapters) == 0 {
		return v, nil
	}
	n := v.Chapters[0].Number
	if s := r.PathValue("chapter"); s != "" {
		if n, err = strconv.Atoi(s); err != nil {
			return nil, core.ErrNotFound()
		}
	}
	for i := range v.Chapters {
		if v.Chapters[i].Number == n {
			v.Chapter = &v.Chapters[i]
		}
	}
	if v.Chapter == nil {
		return nil, core.ErrNotFound()
	}
	if v.Scenes, err = m.svc.Scenes(ctx, v.Chapter.ID); err != nil {
		return nil, err
	}
	if len(v.Scenes) > 0 {
		idx := 0
		if s := r.PathValue("scene"); s != "" {
			if idx, err = strconv.Atoi(s); err != nil || idx < 1 || idx > len(v.Scenes) {
				return nil, core.ErrNotFound()
			}
			idx--
		}
		v.SceneIdx, v.Scene = idx, &v.Scenes[idx]
		if v.Script, err = m.svc.SceneScript(ctx, v.Scene.ID); err != nil {
			return nil, err
		}
	}
	v.Issues = m.svc.OpenContinuityIssues(ctx, v.Chapter.ID)
	if v.Characters, err = m.svc.Characters(ctx, a.Project.ID, ""); err != nil {
		return nil, err
	}
	if v.Locations, err = m.svc.SettingItems(ctx, a.Project.ID, SettingLocation); err != nil {
		return nil, err
	}
	if v.Assists, err = m.svc.Assists(ctx, v.Chapter.ID); err != nil {
		return nil, err
	}
	_ = m.d.DB.Get(ctx, &v.Pages, `SELECT COUNT(*) FROM pages WHERE chapter_id = ?`, v.Chapter.ID)
	v.Route = m.routePreview(ctx, a.Actor.UserID, a.Project.ID, JobScriptAssist)
	return v, nil
}

func (m *Module) storyPage(w http.ResponseWriter, r *http.Request) error {
	v, err := m.loadStory(r)
	if err != nil {
		return err
	}
	return m.renderStory(w, r, http.StatusOK, v)
}

func (m *Module) renderStory(w http.ResponseWriter, r *http.Request, status int, v *StoryView) error {
	ctx := r.Context()
	title := i18n.T(ctx, "studio.story.title", "title", v.Project.Title)
	mobile := v.Project.Title
	if v.Scene != nil {
		mobile = i18n.T(ctx, "studio.story.scene_short", "n", v.SceneIdx+1, "title", v.Scene.Title)
	}
	return m.ui.Page(w, r, status, web.PageOpts{Title: title, MobileTitle: mobile, BackURL: "/studio/projects",
		Section: layout.SectionStudio, Nav: layout.NavProjects, Tab: layout.TabHome}, StoryPage(*v))
}

// back redirects to the story page the form came from.
func (m *Module) back(w http.ResponseWriter, r *http.Request, fallback string) {
	core.Redirect(w, r, core.SafeRedirectTarget(r.FormValue("next"), fallback))
}

func (m *Module) parse(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	return nil
}

func delta(r *http.Request) int {
	if r.FormValue("dir") == "up" {
		return -1
	}
	return 1
}

// storyBase is the story URL of the project in the request.
func storyBase(r *http.Request) string { return "/studio/p/" + r.PathValue("project") + "/story" }

// ---- arcs, chapters, scenes -------------------------------------------------------

func (m *Module) arcCreate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	if _, err := m.svc.CreateArc(r.Context(), AccessFrom(r.Context()), web.FormValue(r, "title"), web.FormValue(r, "summary")); err != nil {
		return err
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) arcUpdate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	arc, err := m.svc.Arc(ctx, a.Project.ID, r.PathValue("id"))
	if err != nil {
		return err
	}
	summary := arc.Summary
	if _, ok := r.PostForm["summary"]; ok {
		summary = web.FormValue(r, "summary")
	}
	if err := m.svc.UpdateArc(ctx, a, arc.ID, web.FormValue(r, "title"), summary); err != nil {
		return err
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) arcMove(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	if err := m.svc.MoveArc(r.Context(), AccessFrom(r.Context()), r.PathValue("id"), delta(r)); err != nil {
		return err
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) arcDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	if err := m.svc.DeleteArc(r.Context(), AccessFrom(r.Context()), r.PathValue("id")); err != nil {
		return err
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) chapterCreate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	c, err := m.svc.CreateChapter(ctx, AccessFrom(ctx), web.FormValue(r, "arc_id"), web.FormValue(r, "title"), web.FormValue(r, "summary"))
	if err != nil {
		return err
	}
	core.Redirect(w, r, storyBase(r)+"/"+itoa(c.Number))
	return nil
}

func (m *Module) chapterUpdate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	pt := ChapterPatch{}
	if _, ok := r.PostForm["title"]; ok {
		t := web.FormValue(r, "title")
		pt.Title = &t
	}
	if _, ok := r.PostForm["summary"]; ok {
		s := web.FormValue(r, "summary")
		pt.Summary = &s
	}
	if _, ok := r.PostForm["arc_id"]; ok {
		s := web.FormValue(r, "arc_id")
		pt.ArcID = &s
	}
	if _, ok := r.Form["locked"]; ok {
		b := r.FormValue("locked") == "on"
		pt.Locked = &b
	}
	if _, err := m.svc.UpdateChapter(ctx, AccessFrom(ctx), r.PathValue("id"), pt); err != nil {
		return err
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) chapterMove(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	if err := m.svc.MoveChapter(r.Context(), AccessFrom(r.Context()), r.PathValue("id"), delta(r)); err != nil {
		return err
	}
	c, err := m.svc.ChapterByID(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	core.Redirect(w, r, storyBase(r)+"/"+itoa(c.Number))
	return nil
}

func (m *Module) chapterDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	if err := m.svc.DeleteChapter(ctx, AccessFrom(ctx), r.PathValue("id")); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.story.chapter_deleted"), "")
	core.Redirect(w, r, storyBase(r))
	return nil
}

func (m *Module) sceneCreate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	sc, err := m.svc.CreateScene(ctx, a, web.FormValue(r, "chapter_id"), web.FormValue(r, "title"))
	if err != nil {
		return err
	}
	c, _ := m.svc.ChapterByID(ctx, sc.ChapterID)
	core.Redirect(w, r, storyBase(r)+"/"+itoa(c.Number)+"/"+itoa(sc.OrderIdx+1))
	return nil
}

func (m *Module) sceneUpdate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	pt := ScenePatch{}
	for field, dst := range map[string]**string{"title": &pt.Title, "summary": &pt.Summary, "prose": &pt.Prose, "time_of_day": &pt.TimeOfDay, "location_id": &pt.LocationID} {
		if _, ok := r.PostForm[field]; ok {
			v := r.PostFormValue(field)
			if field != "prose" {
				v = strings.TrimSpace(v)
			}
			*dst = &v
		}
	}
	if _, err := m.svc.UpdateScene(ctx, AccessFrom(ctx), r.PathValue("id"), pt); err != nil {
		return err
	}
	if r.PostFormValue("autosave") == "1" {
		return web.Fragment(w, r, http.StatusOK, SavedNote(i18n.T(ctx, "studio.wizard.saved")))
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) sceneMove(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	if err := m.svc.MoveScene(r.Context(), AccessFrom(r.Context()), r.PathValue("id"), delta(r)); err != nil {
		return err
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) sceneDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	sc, err := m.svc.Scene(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := m.svc.DeleteScene(ctx, AccessFrom(ctx), sc.ID); err != nil {
		return err
	}
	c, _ := m.svc.ChapterByID(ctx, sc.ChapterID)
	core.Redirect(w, r, storyBase(r)+"/"+itoa(c.Number))
	return nil
}

// ---- script rows ------------------------------------------------------------------------

// ScriptRowView is one row of the script table.
type ScriptRowView struct {
	Story *StoryView
	Panel ScriptPanel
	Edit  bool
	Errs  web.FormErrors
}

func (m *Module) rowView(r *http.Request, id string, edit bool) (*ScriptRowView, error) {
	ctx := r.Context()
	a := AccessFrom(ctx)
	sp, err := m.svc.ScriptPanel(ctx, id)
	if err != nil || sp.ProjectID != a.Project.ID {
		return nil, core.ErrNotFound()
	}
	chars, err := m.svc.Characters(ctx, a.Project.ID, "")
	if err != nil {
		return nil, err
	}
	ch, err := m.svc.ChapterByID(ctx, sp.ChapterID)
	if err != nil {
		return nil, err
	}
	sv := &StoryView{Access: a, Project: a.Project, Characters: chars, Chapter: ch, Tab: "panels"}
	if sc, err := m.svc.Scene(ctx, sp.SceneID); err == nil {
		sv.Scene = sc
		sv.SceneIdx = sc.OrderIdx
	}
	return &ScriptRowView{Story: sv, Panel: *sp, Edit: edit, Errs: web.FormErrors{}}, nil
}

func (m *Module) scriptRow(w http.ResponseWriter, r *http.Request) error {
	v, err := m.rowView(r, r.PathValue("id"), false)
	if err != nil {
		return err
	}
	return web.Fragment(w, r, http.StatusOK, ScriptRow(*v))
}

func (m *Module) scriptEdit(w http.ResponseWriter, r *http.Request) error {
	v, err := m.rowView(r, r.PathValue("id"), true)
	if err != nil {
		return err
	}
	if !htmx(r) {
		sv := v.Story
		core.Redirect(w, r, sv.SceneURL(sv.SceneIdx, "")+"?edit="+v.Panel.ID)
		return nil
	}
	return web.Fragment(w, r, http.StatusOK, ScriptRow(*v))
}

// readScript parses the script row form.
func readScript(r *http.Request) ScriptInput {
	in := ScriptInput{Camera: r.PostFormValue("camera"), Emotion: r.PostFormValue("emotion"), Action: r.PostFormValue("action"),
		Shot: r.PostFormValue("shot"), Notes: r.PostFormValue("notes")}
	in.PageNumber, _ = strconv.Atoi(r.PostFormValue("page"))
	speakers, kinds, texts := r.PostForm["line_speaker"], r.PostForm["line_kind"], r.PostForm["line_text"]
	for i, t := range texts {
		l := DialogueLine{Text: t}
		if i < len(kinds) {
			l.Kind = kinds[i]
		}
		if i < len(speakers) {
			l.CharacterID = speakers[i]
		}
		in.Dialogue = append(in.Dialogue, l)
	}
	for _, x := range strings.Split(r.PostFormValue("sfx"), ",") {
		if x = strings.TrimSpace(x); x != "" {
			in.SFX = append(in.SFX, strings.ToUpper(x))
		}
	}
	in.CharacterIDs = r.PostForm["character_ids"]
	return in
}

// resolveSpeakers fills speaker names from character ids and adds speakers to the panel's characters.
func resolveSpeakers(in *ScriptInput, chars []Character) {
	names := map[string]string{}
	for _, c := range chars {
		names[c.ID] = c.Name
	}
	for i := range in.Dialogue {
		l := &in.Dialogue[i]
		if n, ok := names[l.CharacterID]; ok {
			l.Speaker = n
			if !contains(in.CharacterIDs, l.CharacterID) {
				in.CharacterIDs = append(in.CharacterIDs, l.CharacterID)
			}
		} else {
			l.CharacterID, l.Speaker = "", ""
			l.Kind = LineCaption
		}
	}
	var keep []string
	for _, id := range in.CharacterIDs {
		if _, ok := names[id]; ok && !contains(keep, id) {
			keep = append(keep, id)
		}
	}
	in.CharacterIDs = keep
}

func (m *Module) scriptSave(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	chars, err := m.svc.Characters(ctx, a.Project.ID, "")
	if err != nil {
		return err
	}
	in := readScript(r)
	resolveSpeakers(&in, chars)
	if _, err := m.svc.UpdateScriptPanel(ctx, a, r.PathValue("id"), in); err != nil {
		return err
	}
	if htmx(r) {
		v, err := m.rowView(r, r.PathValue("id"), false)
		if err != nil {
			return err
		}
		return web.Fragment(w, r, http.StatusOK, ScriptRow(*v))
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) scriptCreate(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	chars, err := m.svc.Characters(ctx, a.Project.ID, "")
	if err != nil {
		return err
	}
	in := readScript(r)
	resolveSpeakers(&in, chars)
	if in.Action == "" && len(in.Dialogue) == 0 {
		in.Action = i18n.T(ctx, "studio.script.new_panel")
		in.Camera = "medium"
	}
	sp, err := m.svc.CreateScriptPanel(ctx, a, web.FormValue(r, "scene_id"), web.FormValue(r, "after"), in)
	if err != nil {
		return err
	}
	next := core.SafeRedirectTarget(r.PostFormValue("next"), storyBase(r))
	if strings.Contains(next, "?") {
		next += "&edit=" + sp.ID
	} else {
		next += "?edit=" + sp.ID
	}
	core.Redirect(w, r, next+"#row-"+sp.ID)
	return nil
}

func (m *Module) scriptMove(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	if err := m.svc.MoveScriptPanel(r.Context(), AccessFrom(r.Context()), r.PathValue("id"), delta(r)); err != nil {
		return err
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) scriptDelete(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	if err := m.svc.DeleteScriptPanel(r.Context(), AccessFrom(r.Context()), r.PathValue("id")); err != nil {
		return err
	}
	if htmx(r) {
		w.WriteHeader(http.StatusOK)
		return nil
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) pagesFromScript(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	res, err := m.svc.MakePagesFromScript(ctx, a.Actor, r.PathValue("id"))
	if err != nil {
		return err
	}
	made := len(res.Created) + len(res.Filled)
	switch {
	case made == 0:
		web.Flash(w, r, "info", i18n.T(ctx, "studio.pages.none"), i18n.T(ctx, "studio.pages.skipped", "count", len(res.Skipped)))
	case len(res.Skipped) > 0:
		web.Flash(w, r, "success", i18n.T(ctx, "studio.pages.made", "count", made), i18n.T(ctx, "studio.pages.skipped", "count", len(res.Skipped)))
	default:
		web.Flash(w, r, "success", i18n.T(ctx, "studio.pages.made", "count", made), i18n.T(ctx, "studio.pages.made_body"))
	}
	m.back(w, r, storyBase(r))
	return nil
}

// ---- script assistant ---------------------------------------------------------------------

func (m *Module) assistRequest(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	a := AccessFrom(ctx)
	as, err := m.svc.RequestAssist(ctx, a, web.FormValue(r, "chapter_id"), web.FormValue(r, "scene_id"), web.FormValue(r, "request"))
	if err != nil {
		return err
	}
	if htmx(r) {
		v, err := m.assistView2(r, as)
		if err != nil {
			return err
		}
		return web.Fragment(w, r, http.StatusOK, AssistCreated(*v))
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) renderAssist(w http.ResponseWriter, r *http.Request, id string) error {
	ctx := r.Context()
	a := AccessFrom(ctx)
	as, err := m.svc.Assist(ctx, a.Project.ID, id)
	if err != nil {
		return err
	}
	v, err := m.assistView2(r, as)
	if err != nil {
		return err
	}
	return web.Fragment(w, r, http.StatusOK, AssistItem(*v))
}

// AssistView is one request and proposal in the assistant panel.
type AssistView struct {
	Story  *StoryView
	Assist ScriptAssist
	Panels map[string]ScriptPanel
	Job    string
	Pct    float64
}

func (m *Module) assistView2(r *http.Request, as *ScriptAssist) (*AssistView, error) {
	ctx := r.Context()
	a := AccessFrom(ctx)
	ch, err := m.svc.ChapterByID(ctx, as.ChapterID)
	if err != nil {
		return nil, err
	}
	sv := &StoryView{Access: a, Project: a.Project, Chapter: ch}
	sv.Characters, _ = m.svc.Characters(ctx, a.Project.ID, "")
	v := &AssistView{Story: sv, Assist: *as, Panels: map[string]ScriptPanel{}}
	panels, _ := m.svc.ScriptPanels(ctx, as.ChapterID)
	for _, p := range panels {
		v.Panels[p.ID] = p
	}
	if as.Status == "pending" && as.JobID != "" {
		if j, err := m.d.Jobs.Get(ctx, as.JobID); err == nil {
			v.Pct, v.Job = j.Progress, j.Message
		}
	}
	return v, nil
}

func (m *Module) assistView(w http.ResponseWriter, r *http.Request) error {
	return m.renderAssist(w, r, r.PathValue("id"))
}

func (m *Module) assistApply(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	if err := m.svc.ApplyAssist(ctx, AccessFrom(ctx), r.PathValue("id")); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "studio.assist.applied"), "")
	if htmx(r) {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	m.back(w, r, storyBase(r))
	return nil
}

func (m *Module) assistDismiss(w http.ResponseWriter, r *http.Request) error {
	if err := m.parse(w, r); err != nil {
		return err
	}
	ctx := r.Context()
	if err := m.svc.DismissAssist(ctx, AccessFrom(ctx), r.PathValue("id")); err != nil {
		return err
	}
	if htmx(r) {
		return m.renderAssist(w, r, r.PathValue("id"))
	}
	m.back(w, r, storyBase(r))
	return nil
}
