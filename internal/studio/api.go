package studio

import (
	"net/http"
	"strconv"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
)

// apiRoutes registers the JSON API under /api/v1/projects (session cookie or
// bearer token). Agents (MCP tokens) need the matching scopes; archive,
// delete and member changes need a human.
func (m *Module) apiRoutes(r *core.Router) {
	r.API("GET /api/v1/projects", m.apiList, core.RequireUser, core.RequireScope("projects:read"))
	r.API("POST /api/v1/projects", m.apiCreate, core.RequireUser, core.RequireScope("projects:write"))
	r.API("GET /api/v1/projects/{project}", m.apiProject, core.RequireUser, m.svc.RequireProjectRole(), core.RequireScope("projects:read"))
	r.API("PATCH /api/v1/projects/{project}", m.apiPatch, core.RequireUser, m.svc.RequireProjectRole(), core.RequireScope("projects:write"))
	p := r.Group("/api/v1/projects/{project}", core.RequireUser, m.svc.RequireProjectRole())
	p.API("GET /members", m.apiMembers, core.RequireScope("projects:read"))
	p.API("GET /chapters", m.apiChapters, core.RequireScope("projects:read"))
	p.API("GET /chapters/{n}/script", m.apiScript, core.RequireScope("projects:read"))
	p.API("POST /chapters/{n}/pages-from-script", m.apiPagesFromScript, core.RequireScope("pages:write"))
	p.API("GET /characters", m.apiCharacters, core.RequireScope("projects:read"))
	p.API("GET /setting", m.apiSetting, core.RequireScope("projects:read"))
	p.API("GET /pages/{page}/doc", m.apiPageDoc, core.RequireScope("projects:read"))
	p.API("PUT /pages/{page}/doc", m.apiSavePageDoc, core.RequireScope("pages:write"))
	p.API("GET /pages/{page}/versions", m.apiVersions, core.RequireScope("projects:read"))
	p.API("POST /pages/{page}/versions/{version}/restore", m.apiRestore, core.RequireScope("pages:write"))
	p.API("POST /pages/{page}/undo", m.apiUndo, core.RequireScope("pages:write"))
	p.API("GET /pages/{page}/branches", m.apiBranches, core.RequireScope("projects:read"))
	p.API("POST /pages/{page}/branches", m.apiBranch, core.RequireScope("pages:write"))
	p.API("POST /pages/{page}/branches/{branch}/switch", m.apiSwitch, core.RequireScope("pages:write"))
	p.API("POST /pages/{page}/branches/{branch}/merge", m.apiMerge, core.RequireScope("pages:write"))
	p.API("GET /pages/{page}/compare", m.apiCompare, core.RequireScope("projects:read"))
	p.API("POST /pages/{page}/layout", m.apiLayout, core.RequireScope("pages:write"))
	p.API("POST /pages/{page}/lock", m.apiLock, core.RequireScope("pages:write"))
	r.API("GET /api/v1/templates", m.apiTemplates, core.RequireUser)
}

func (m *Module) apiList(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := r.URL.Query()
	list, err := m.svc.ListProjects(ctx, core.UserFrom(ctx).ID, ProjectFilter{Query: q.Get("q"), Badge: q.Get("status"), Format: q.Get("format"), Sort: q.Get("sort")})
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, map[string]any{"project": p.Project, "role": p.Role, "badge": p.Badge(), "chapters": p.Chapters, "pages": p.Pages})
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

type apiNewProject struct {
	Title           string   `json:"title"`
	Premise         string   `json:"premise"`
	Genres          []string `json:"genres"`
	Style           string   `json:"style"`
	Format          string   `json:"format"`
	PageSize        string   `json:"page_size"`
	Language        string   `json:"language"`
	Visibility      string   `json:"visibility"`
	ChaptersPlanned int      `json:"chapters_planned"`
	PagesPerChapter string   `json:"pages_per_chapter"`
}

func (m *Module) apiCreate(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	if !usr.CanCreate() {
		return core.ErrForbidden("only creators can start projects")
	}
	var in apiNewProject
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	p, err := m.svc.CreateProject(ctx, usr.ID, NewProject{Title: in.Title, Premise: in.Premise, Genres: in.Genres, Style: in.Style, Format: in.Format,
		PageSize: in.PageSize, Language: in.Language, Visibility: in.Visibility, ChaptersPlanned: in.ChaptersPlanned, PagesPerChapter: in.PagesPerChapter})
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusCreated, p)
	return nil
}

func (m *Module) apiProject(w http.ResponseWriter, r *http.Request) error {
	a := AccessFrom(r.Context())
	core.JSON(w, http.StatusOK, map[string]any{"project": a.Project, "role": a.Role, "scope": a.Scope})
	return nil
}

type apiPatch struct {
	Title           *string  `json:"title"`
	Premise         *string  `json:"premise"`
	Logline         *string  `json:"logline"`
	Genres          []string `json:"genres"`
	Style           *string  `json:"style"`
	Format          *string  `json:"format"`
	PageSize        *string  `json:"page_size"`
	Language        *string  `json:"language"`
	Visibility      *string  `json:"visibility"`
	RoutingMode     *string  `json:"routing_mode"`
	ChaptersPlanned *int     `json:"chapters_planned"`
	PagesPerChapter *string  `json:"pages_per_chapter"`
}

func (m *Module) apiPatch(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var in apiPatch
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	p, err := m.svc.UpdateProject(ctx, AccessFrom(ctx), ProjectPatch{Title: in.Title, Premise: in.Premise, Logline: in.Logline, Genres: in.Genres,
		Style: in.Style, Format: in.Format, PageSize: in.PageSize, Language: in.Language, Visibility: in.Visibility, RoutingMode: in.RoutingMode,
		ChaptersPlanned: in.ChaptersPlanned, PagesPerChapter: in.PagesPerChapter})
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, p)
	return nil
}

func (m *Module) apiMembers(w http.ResponseWriter, r *http.Request) error {
	list, err := m.svc.Members(r.Context(), AccessFrom(r.Context()).Project.ID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": list})
	return nil
}

func (m *Module) apiChapters(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	a := AccessFrom(ctx)
	arcs, err := m.svc.Arcs(ctx, a.Project.ID)
	if err != nil {
		return err
	}
	chs, err := m.svc.Chapters(ctx, a.Project.ID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"arcs": arcs, "chapters": chs})
	return nil
}

func (m *Module) chapterParam(r *http.Request) (*Chapter, error) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		return nil, core.ErrNotFound()
	}
	return m.svc.Chapter(r.Context(), AccessFrom(r.Context()).Project.ID, n)
}

func (m *Module) apiScript(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	ch, err := m.chapterParam(r)
	if err != nil {
		return err
	}
	scenes, err := m.svc.Scenes(ctx, ch.ID)
	if err != nil {
		return err
	}
	panels, err := m.svc.ScriptPanels(ctx, ch.ID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"chapter": ch, "scenes": scenes, "panels": panels})
	return nil
}

func (m *Module) apiPagesFromScript(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	ch, err := m.chapterParam(r)
	if err != nil {
		return err
	}
	res, err := m.svc.MakePagesFromScript(ctx, AccessFrom(ctx).Actor, ch.ID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, res)
	return nil
}

func (m *Module) apiCharacters(w http.ResponseWriter, r *http.Request) error {
	list, err := m.svc.Characters(r.Context(), AccessFrom(r.Context()).Project.ID, r.URL.Query().Get("category"))
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": list})
	return nil
}

func (m *Module) apiSetting(w http.ResponseWriter, r *http.Request) error {
	list, err := m.svc.SettingItems(r.Context(), AccessFrom(r.Context()).Project.ID, r.URL.Query().Get("kind"))
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": list})
	return nil
}

func (m *Module) pageParam(r *http.Request) (*Page, error) {
	p, err := m.svc.Page(r.Context(), r.PathValue("page"))
	if err != nil || p.ProjectID != AccessFrom(r.Context()).Project.ID {
		return nil, core.ErrNotFound()
	}
	return p, nil
}

func (m *Module) apiPageDoc(w http.ResponseWriter, r *http.Request) error {
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	doc, err := m.svc.LoadPageDoc(r.Context(), p.ID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, doc)
	return nil
}

type apiSaveDoc struct {
	Doc         PageDoc `json:"doc"`
	Summary     string  `json:"summary"`
	Action      string  `json:"action"`
	BaseVersion int     `json:"base_version"`
}

func (m *Module) apiSavePageDoc(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	var in apiSaveDoc
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	in.Doc.Page.ID = p.ID
	doc, v, err := m.svc.SavePageDoc(ctx, AccessFrom(ctx).Actor, &in.Doc, SaveOptions{Summary: in.Summary, Action: in.Action, BaseVersion: in.BaseVersion})
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"doc": doc, "version": v})
	return nil
}

func (m *Module) apiVersions(w http.ResponseWriter, r *http.Request) error {
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	branch := r.URL.Query().Get("branch")
	if !r.URL.Query().Has("branch") {
		branch = p.BranchID
	}
	list, err := m.svc.Versions(r.Context(), p.ID, branch, 200)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": list, "current_branch": p.BranchID, "version": p.Version})
	return nil
}

func (m *Module) apiRestore(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	v, err := m.svc.Version(ctx, r.PathValue("version"))
	if err != nil || v.PageID != p.ID {
		return core.ErrNotFound()
	}
	doc, nv, err := m.svc.RestoreVersion(ctx, AccessFrom(ctx).Actor, v.ID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"doc": doc, "version": nv})
	return nil
}

func (m *Module) apiUndo(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	doc, v, err := m.svc.UndoLast(ctx, AccessFrom(ctx).Actor, p.ID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"doc": doc, "version": v})
	return nil
}

func (m *Module) apiBranches(w http.ResponseWriter, r *http.Request) error {
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	list, err := m.svc.Branches(r.Context(), p.ID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": list, "current_branch": p.BranchID})
	return nil
}

func (m *Module) apiBranch(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	b, err := m.svc.CreateBranch(ctx, AccessFrom(ctx).Actor, p.ID, in.Name)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusCreated, b)
	return nil
}

func branchParam(r *http.Request) string {
	if b := r.PathValue("branch"); b != "main" {
		return b
	}
	return ""
}

func (m *Module) apiSwitch(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	doc, err := m.svc.SwitchBranch(ctx, AccessFrom(ctx).Actor, p.ID, branchParam(r))
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, doc)
	return nil
}

func (m *Module) apiMerge(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	into := r.URL.Query().Get("into")
	if into == "main" {
		into = ""
	}
	doc, err := m.svc.MergeBranch(ctx, AccessFrom(ctx).Actor, p.ID, branchParam(r), into)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, doc)
	return nil
}

func (m *Module) apiCompare(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	a, b, d, err := m.svc.CompareVersions(ctx, r.URL.Query().Get("a"), r.URL.Query().Get("b"))
	if err != nil {
		return err
	}
	if a.Page.ID != p.ID {
		return core.ErrNotFound()
	}
	core.JSON(w, http.StatusOK, map[string]any{"a": a, "b": b, "diff": d})
	return nil
}

func (m *Module) apiLayout(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	var in struct {
		Layout string `json:"layout"`
		Index  int    `json:"index"`
	}
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	doc, err := m.svc.ApplyLayout(ctx, AccessFrom(ctx).Actor, p.ID, in.Layout, in.Index)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, doc)
	return nil
}

func (m *Module) apiLock(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	a := AccessFrom(ctx)
	p, err := m.pageParam(r)
	if err != nil {
		return err
	}
	var in struct {
		Locked  bool   `json:"locked"`
		PanelID string `json:"panel_id"`
		LayerID string `json:"layer_id"`
	}
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	switch {
	case in.LayerID != "":
		err = m.svc.SetLayerLock(ctx, a, in.LayerID, in.Locked)
	case in.PanelID != "":
		pn, perr := m.svc.Panel(ctx, in.PanelID)
		if perr != nil || pn.PageID != p.ID {
			return core.ErrNotFound()
		}
		err = m.svc.SetPanelLock(ctx, a, in.PanelID, in.Locked)
	default:
		err = m.svc.SetPageLock(ctx, a, p.ID, in.Locked)
	}
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (m *Module) apiTemplates(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	layouts := BuiltinLayouts(ctx)
	if more, err := m.svc.UserLayouts(ctx, usr.ID); err == nil {
		layouts = append(layouts, more...)
	}
	stories := BuiltinStories(ctx)
	if more, err := m.svc.UserStories(ctx, usr.ID); err == nil {
		stories = append(stories, more...)
	}
	core.JSON(w, http.StatusOK, map[string]any{"layouts": layouts, "stories": stories})
	return nil
}
