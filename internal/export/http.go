package export

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
	"github.com/a-h/templ"
)

type exportView struct {
	Project                            studio.Project
	Chapter                            studio.Chapter
	Pages                              []pagestore.PageSummary
	Page                               pagedoc.Page
	Request                            Request
	Languages                          []ui.Option
	PageMode, Range, Error, Base, Back string
	Job                                *jobs.Job
	Result                             *Result
}

func (m *Module) routes(r *core.Router) {
	base := "/studio/p/{project}/chapters/{chapter}/export"
	r.Page("GET "+base, m.page, core.RequireSession)
	r.Page("POST "+base, m.enqueue, core.RequireSession)
	r.Page("GET "+base+"/jobs/{job}", m.status, core.RequireSession)
	r.Page("POST "+base+"/jobs/{job}/cancel", m.cancel, core.RequireSession)
	r.Page("GET "+base+"/preview/{page}", m.preview, core.RequireSession)
}
func sessionActor(r *http.Request) (studio.Actor, error) {
	id := core.IdentityFrom(r.Context())
	if id.User == nil || id.Token != nil || id.SessionID == "" {
		return studio.Actor{}, studio.ErrForbidden
	}
	return studio.Actor{UserID: id.User.ID}, nil
}
func (m *Module) load(r *http.Request) (studio.Actor, exportView, error) {
	actor, err := sessionActor(r)
	if err != nil {
		return actor, exportView{}, core.ErrNotFound()
	}
	projectID := r.PathValue("project")
	if err = m.s.studio.Authorize(r.Context(), actor, projectID, studio.ActRead); err != nil {
		return actor, exportView{}, core.ErrNotFound()
	}
	p, err := m.s.studio.GetProject(r.Context(), projectID)
	if err != nil {
		return actor, exportView{}, core.ErrNotFound()
	}
	chapters, err := m.s.studio.ListChapters(r.Context(), projectID)
	if err != nil {
		return actor, exportView{}, err
	}
	var chapter studio.Chapter
	for _, c := range chapters {
		if c.ID == r.PathValue("chapter") || strconv.Itoa(c.Number) == r.PathValue("chapter") {
			chapter = c
			break
		}
	}
	if chapter.ID == "" {
		return actor, exportView{}, core.ErrNotFound()
	}
	pages, err := m.s.pages.ListPages(r.Context(), chapter.ID)
	if err != nil {
		return actor, exportView{}, err
	}
	if len(pages) > maxPages {
		return actor, exportView{}, ErrInvalid
	}
	v := exportView{Project: p, Chapter: chapter, Pages: pages, PageMode: "all", Request: Request{ProjectID: p.ID, ChapterID: chapter.ID, Format: FormatPDF, TrimID: "b5", Lang: p.Language, DPI: 0, CreditsPage: true}}
	v.Base = "/studio/p/" + url.PathEscape(p.ID) + "/chapters/" + strconv.Itoa(chapter.Number) + "/export"
	v.Back = strings.TrimSuffix(v.Base, "/export") + "/edit"
	if len(pages) > 0 {
		pageID := pages[0].ID
		for _, pg := range pages {
			if r.URL.Query().Get("page") == pg.ID || r.URL.Query().Get("page") == strconv.Itoa(pg.Number) {
				pageID = pg.ID
			}
		}
		v.Page, err = m.s.pages.GetPage(r.Context(), pageID)
		if err != nil {
			return actor, v, err
		}
	}
	languages := map[string]bool{p.Language: true}
	for _, b := range v.Page.Bubbles {
		for lang := range b.Texts {
			languages[lang] = true
		}
	}
	ls := make([]string, 0, len(languages))
	for lang := range languages {
		ls = append(ls, lang)
	}
	sort.Strings(ls)
	for _, lang := range ls {
		label := lang
		if lang == "en" || lang == "ja" {
			label = i18n.T(r.Context(), "exportui.language."+lang)
		}
		if lang == p.Language {
			label = i18n.T(r.Context(), "exportui.original", "language", label)
		}
		v.Languages = append(v.Languages, ui.Option{Value: lang, Label: label})
	}
	return actor, v, nil
}
func (m *Module) render(w http.ResponseWriter, r *http.Request, status int, v exportView) error {
	chrome := m.ui.Chrome(w, r, web.PageOpts{Title: i18n.T(r.Context(), "exportui.title", "number", v.Chapter.Number)})
	return web.Render(w, r, status, exportScreen(chrome, v), nil)
}
func (m *Module) page(w http.ResponseWriter, r *http.Request) error {
	actor, v, err := m.load(r)
	if err != nil {
		return err
	}
	if id := r.URL.Query().Get("job"); id != "" {
		v.Job, err = m.job(r.Context(), actor, v, id)
		if err != nil {
			return core.ErrNotFound()
		}
		m.result(r.Context(), &v)
	}
	return m.render(w, r, http.StatusOK, v)
}
func parseRange(s string, pages []pagestore.PageSummary) ([]string, error) {
	if len(s) > 2048 || strings.TrimSpace(s) == "" {
		return nil, ErrInvalid
	}
	numbers := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		pieces := strings.Split(strings.TrimSpace(part), "-")
		if len(pieces) > 2 {
			return nil, ErrInvalid
		}
		a, err := strconv.Atoi(strings.TrimSpace(pieces[0]))
		if err != nil {
			return nil, ErrInvalid
		}
		z := a
		if len(pieces) == 2 {
			z, err = strconv.Atoi(strings.TrimSpace(pieces[1]))
			if err != nil {
				return nil, ErrInvalid
			}
		}
		if a < 1 || z < a || z > maxPages {
			return nil, ErrInvalid
		}
		for n := a; n <= z; n++ {
			numbers[n] = true
		}
	}
	var ids []string
	for _, p := range pages {
		if numbers[p.Number] {
			ids = append(ids, p.ID)
			delete(numbers, p.Number)
		}
	}
	if len(numbers) > 0 || len(ids) == 0 {
		return nil, ErrInvalid
	}
	return ids, nil
}
func (m *Module) enqueue(w http.ResponseWriter, r *http.Request) error {
	actor, v, err := m.load(r)
	if err != nil {
		return err
	}
	if err = web.ParseForm(w, r); err != nil {
		return err
	}
	v.Request.Format = web.FormValue(r, "format")
	v.Request.TrimID = web.FormValue(r, "trim_id")
	v.Request.Lang = web.FormValue(r, "lang")
	v.Request.CreditsPage = web.FormValue(r, "credits_page") == "on"
	v.PageMode = web.FormValue(r, "pages")
	v.Range = web.FormValue(r, "range")
	v.Request.DPI, err = strconv.Atoi(web.FormValue(r, "dpi"))
	if err != nil {
		v.Error = i18n.T(r.Context(), "exportui.invalid")
	}
	switch v.PageMode {
	case "all":
	case "current":
		if v.Page.ID == "" {
			v.Error = i18n.T(r.Context(), "exportui.empty")
		} else {
			v.Request.PageIDs = []string{v.Page.ID}
		}
	case "range":
		v.Request.PageIDs, err = parseRange(v.Range, v.Pages)
		if err != nil {
			v.Error = i18n.T(r.Context(), "exportui.range_error")
		}
	default:
		v.Error = i18n.T(r.Context(), "exportui.invalid")
	}
	validLang := false
	for _, l := range v.Languages {
		validLang = validLang || l.Value == v.Request.Lang
	}
	if !validLang {
		v.Error = i18n.T(r.Context(), "exportui.invalid")
	}
	if v.Error != "" {
		return m.render(w, r, http.StatusUnprocessableEntity, v)
	}
	j, err := m.s.Enqueue(r.Context(), actor, v.Request)
	if err != nil {
		key := "exportui.invalid"
		status := http.StatusUnprocessableEntity
		if errors.Is(err, ErrChanged) || errors.Is(err, ErrActive) {
			key = "exportui.changed"
			status = http.StatusConflict
		}
		if errors.Is(err, studio.ErrForbidden) || errors.Is(err, studio.ErrNotFound) {
			return core.ErrNotFound()
		}
		v.Error = i18n.T(r.Context(), key)
		return m.render(w, r, status, v)
	}
	core.Redirect(w, r, v.Base+"?job="+url.QueryEscape(j.ID))
	return nil
}
func (m *Module) job(ctx context.Context, actor studio.Actor, v exportView, id string) (*jobs.Job, error) {
	j, err := m.d.Jobs.Get(ctx, id)
	if err != nil || j.UserID != actor.UserID || j.Type != JobType || j.Scope.ProjectID != v.Project.ID {
		return nil, core.ErrNotFound()
	}
	in, err := decodeInput(j.Input)
	if err != nil || in.Request.ChapterID != v.Chapter.ID {
		return nil, core.ErrNotFound()
	}
	return j, nil
}
func (m *Module) result(ctx context.Context, v *exportView) {
	if v.Job == nil || v.Job.State != jobs.StateDone {
		return
	}
	var result Result
	if v.Job.DecodeOutput(&result) != nil {
		return
	}
	a, err := m.s.assets.Get(ctx, result.AssetID)
	if err != nil || a.ProjectID == nil || *a.ProjectID != v.Project.ID {
		return
	}
	result.URL = m.s.assets.URL(a, time.Hour)
	v.Result = &result
}
func (m *Module) status(w http.ResponseWriter, r *http.Request) error {
	actor, v, err := m.load(r)
	if err != nil {
		return err
	}
	v.Job, err = m.job(r.Context(), actor, v, r.PathValue("job"))
	if err != nil {
		return err
	}
	m.result(r.Context(), &v)
	if !core.IsHTMX(r) {
		core.Redirect(w, r, v.Base+"?job="+url.QueryEscape(v.Job.ID))
		return nil
	}
	if v.Job.Finished() {
		w.Header().Set("HX-Refresh", "true")
	}
	return web.Fragment(w, r, http.StatusOK, jobStatus(v))
}
func (m *Module) cancel(w http.ResponseWriter, r *http.Request) error {
	actor, v, err := m.load(r)
	if err != nil {
		return err
	}
	j, err := m.job(r.Context(), actor, v, r.PathValue("job"))
	if err != nil {
		return err
	}
	if _, err = m.d.Jobs.Cancel(r.Context(), j.ID); err != nil {
		return err
	}
	core.Redirect(w, r, v.Base+"?job="+url.QueryEscape(j.ID))
	return nil
}
func (m *Module) preview(w http.ResponseWriter, r *http.Request) error {
	_, v, err := m.load(r)
	if err != nil {
		return err
	}
	page, err := m.s.pages.GetPage(r.Context(), r.PathValue("page"))
	if err != nil || page.ProjectID != v.Project.ID || page.ChapterID != v.Chapter.ID {
		return core.ErrNotFound()
	}
	png, err := m.s.renderer.Thumbnail(r.Context(), page, 520)
	if err != nil {
		return core.ErrBadRequest(i18n.T(r.Context(), "exportui.preview_error"))
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=30")
	_, err = w.Write(png)
	return err
}
func (v exportView) action() string {
	if v.Page.ID != "" {
		return v.Base + "?page=" + url.QueryEscape(v.Page.ID)
	}
	return v.Base
}
func (v exportView) previewURL(id string) string { return v.Base + "/preview/" + url.PathEscape(id) }
func (v exportView) active() bool                { return v.Job != nil && !v.Job.Finished() }
func poll(v exportView) templ.Attributes {
	if !v.active() {
		return nil
	}
	return templ.Attributes{"hx-get": v.Base + "/jobs/" + v.Job.ID, "hx-trigger": "load delay:2s", "hx-swap": "outerHTML"}
}
func trimOptions(ctx context.Context) []ui.Option {
	out := make([]ui.Option, len(Trims))
	for i, t := range Trims {
		out[i] = ui.Option{Value: t.ID, Label: i18n.T(ctx, "exportui.trim."+t.ID)}
	}
	return out
}
func dpiOptions(ctx context.Context) []ui.Option {
	return []ui.Option{{Value: "0", Label: i18n.T(ctx, "exportui.dpi.default")}, {Value: "150", Label: "150 dpi"}, {Value: "300", Label: "300 dpi"}, {Value: "600", Label: "600 dpi"}}
}
func jobProps(ctx context.Context, j *jobs.Job) ui.GenProps {
	state := ui.GenQueued
	if j.State == jobs.StateRunning {
		state = ui.GenGenerating
	} else if j.State == jobs.StateDone {
		state = ui.GenReady
	} else if j.Finished() {
		state = ui.GenFailed
	}
	return ui.GenProps{State: state, Title: i18n.T(ctx, "exportui.job."+j.State), Detail: i18n.T(ctx, "exportui.job_detail."+j.State), Progress: j.Progress, ProgressText: fmt.Sprintf("%d%%", int(j.Progress*100)), Step: j.Step, Steps: j.Steps, SFX: "FZZT", ResultLabel: i18n.T(ctx, "exportui.ready")}
}
