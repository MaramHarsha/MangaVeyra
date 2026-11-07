package export

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/compose"
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
	r.Page("GET "+base+"/jobs/{job}/download", m.download, core.RequireSession)
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

// load builds the export view with language options gathered from every page
// of the chapter (the E2 form). loadLite skips that scan for previews,
// downloads and job polling, which only need the project and chapter.
func (m *Module) load(r *http.Request) (studio.Actor, exportView, error) {
	return m.loadView(r, true)
}

func (m *Module) loadLite(r *http.Request) (studio.Actor, exportView, error) {
	return m.loadView(r, false)
}

func (m *Module) loadView(r *http.Request, allPages bool) (studio.Actor, exportView, error) {
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
		wanted := r.PathValue("page")
		if wanted == "" {
			wanted = r.URL.Query().Get("page")
		}
		for _, pg := range pages {
			if wanted == pg.ID || wanted == strconv.Itoa(pg.Number) {
				pageID = pg.ID
			}
		}
		v.Page, err = m.s.pages.GetPage(r.Context(), pageID)
		if err != nil {
			return actor, v, err
		}
	}
	// Options are the project language plus every language that has bubble
	// text somewhere in the chapter, in content-language order.
	languages := map[string]bool{p.Language: true}
	collect := func(docs []pagedoc.Page) {
		for _, doc := range docs {
			for _, b := range doc.Bubbles {
				for lang, t := range b.Texts {
					if strings.TrimSpace(t.Text) != "" {
						languages[lang] = true
					}
				}
			}
		}
	}
	collect([]pagedoc.Page{v.Page})
	wanted := r.URL.Query().Get("lang")
	// Previews and downloads only scan the rest of the chapter when the
	// requested language is not on the page they show.
	if allPages || wanted != "" && !languages[wanted] {
		docs, err := m.s.pages.ChapterPages(r.Context(), chapter.ID)
		if err != nil {
			return actor, v, err
		}
		collect(docs)
	}
	for _, lang := range languageOrder(languages) {
		label := languageLabel(r.Context(), lang)
		if lang == p.Language {
			label = i18n.T(r.Context(), "exportui.original", "language", label)
		}
		v.Languages = append(v.Languages, ui.Option{Value: lang, Label: label})
	}
	if wanted != "" {
		if !languages[wanted] {
			return actor, v, core.ErrBadRequest(i18n.T(r.Context(), "exportui.invalid"))
		}
		v.Request.Lang = wanted
	}
	return actor, v, nil
}

// languageOrder lists content languages in ContentLanguages order, then any
// other stored tags alphabetically.
func languageOrder(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for _, lang := range pagedoc.ContentLanguages {
		if set[lang] {
			out = append(out, lang)
		}
	}
	var rest []string
	for lang := range set {
		if !pagedoc.IsContentLanguage(lang) {
			rest = append(rest, lang)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// languageLabel names a content language with the exportui catalogue; other
// tags (and a missing key) show the tag itself.
func languageLabel(ctx context.Context, lang string) string {
	if !pagedoc.IsContentLanguage(lang) {
		return lang
	}
	key := "exportui.language." + lang
	if label := i18n.T(ctx, key); label != key {
		return label
	}
	return lang
}

// restoreRequest shows a job's settings in the form again, so a finished CBZ
// export does not reset the form to PDF.
func restoreRequest(v *exportView, in Request) {
	v.Request.Format, v.Request.TrimID, v.Request.DPI, v.Request.CreditsPage = in.Format, in.TrimID, in.DPI, in.CreditsPage
	for _, l := range v.Languages {
		if l.Value == in.Lang {
			v.Request.Lang = in.Lang
		}
	}
	v.PageMode, v.Range = "all", ""
	if len(in.PageIDs) == 0 {
		return
	}
	if len(in.PageIDs) == 1 && in.PageIDs[0] == v.Page.ID {
		v.PageMode = "current"
		return
	}
	numbers := map[string]int{}
	for _, p := range v.Pages {
		numbers[p.ID] = p.Number
	}
	var ns []int
	for _, id := range in.PageIDs {
		if n, ok := numbers[id]; ok {
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	var parts []string
	for i := 0; i < len(ns); {
		j := i
		for j+1 < len(ns) && ns[j+1] == ns[j]+1 {
			j++
		}
		if i == j {
			parts = append(parts, strconv.Itoa(ns[i]))
		} else {
			parts = append(parts, strconv.Itoa(ns[i])+"-"+strconv.Itoa(ns[j]))
		}
		i = j + 1
	}
	v.PageMode, v.Range = "range", strings.Join(parts, ",")
}

// contentDisposition offers the Unicode filename (RFC 6266 filename*) with an
// ASCII fallback for older clients.
func contentDisposition(name, fallback string) string {
	if fallback == "" {
		fallback = name
	}
	fallback = asciiFilename(fallback)
	var b strings.Builder
	b.WriteString(`attachment; filename="` + fallback + `"; filename*=UTF-8''`)
	for i := 0; i < len(name); i++ {
		c := name[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
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
		if in, err := decodeInput(v.Job.Input); err == nil {
			restoreRequest(&v, in.Request)
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
	core.Redirect(w, r, v.Base+"?job="+url.QueryEscape(j.ID)+"&lang="+url.QueryEscape(v.Request.Lang))
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
	result.URL = v.Base + "/jobs/" + url.PathEscape(v.Job.ID) + "/download"
	v.Result = &result
}
func (m *Module) download(w http.ResponseWriter, r *http.Request) error {
	actor, v, err := m.loadLite(r)
	if err != nil {
		return err
	}
	v.Job, err = m.job(r.Context(), actor, v, r.PathValue("job"))
	if err != nil {
		return err
	}
	m.result(r.Context(), &v)
	if v.Result == nil {
		return core.ErrNotFound()
	}
	a, err := m.s.assets.Get(r.Context(), v.Result.AssetID)
	if err != nil || a.Kind != "export" {
		return core.ErrNotFound()
	}
	object, info, err := m.d.Store.Get(r.Context(), a.Key)
	if err != nil {
		return core.ErrNotFound()
	}
	defer object.Close()
	w.Header().Set("Content-Type", v.Result.ContentType)
	w.Header().Set("Content-Disposition", contentDisposition(v.Result.Filename, v.Result.ASCIIFilename))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, v.Result.Filename, info.ModTime, object)
	return nil
}
func (m *Module) status(w http.ResponseWriter, r *http.Request) error {
	actor, v, err := m.loadLite(r)
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
	actor, v, err := m.loadLite(r)
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
	_, v, err := m.loadLite(r)
	if err != nil {
		return err
	}
	page, err := m.s.pages.GetPage(r.Context(), r.PathValue("page"))
	if err != nil || page.ProjectID != v.Project.ID || page.ChapterID != v.Chapter.ID {
		return core.ErrNotFound()
	}
	png, err := m.s.renderer.PNG(r.Context(), page, compose.Options{Lang: v.Request.Lang,
		Scale: math.Min(1, 520/float64(page.Width))})
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
func (v exportView) previewURL(id string) string {
	query := url.Values{"lang": {v.Request.Lang}}
	return v.Base + "/preview/" + url.PathEscape(id) + "?" + query.Encode()
}
func (v exportView) active() bool { return v.Job != nil && !v.Job.Finished() }
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
