package publish

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Routes implements core.Module. Every route is session-only: bearer and
// agent tokens can never publish, schedule, update, unpublish or pick covers.
func (m *Module) Routes(r *core.Router) {
	base := "/studio/p/{project}/chapters/{chapter}"
	r.Page("GET "+base+"/publish", m.publishPage, core.RequireSession)
	r.Page("POST "+base+"/publish", m.publishPost, core.RequireSession)
	r.Page("GET "+base+"/publish/jobs/{job}", m.publishJob, core.RequireSession)
	r.Page("POST "+base+"/publish/unpublish", m.unpublishPost, core.RequireSession)
	r.Page("GET "+base+"/publish/pages/{page}/image", m.pageImage, core.RequireSession)
	r.Page("POST "+base+"/publish/cover/page", m.coverPagePost, core.RequireSession)
	r.Page("POST "+base+"/publish/cover/generate", m.coverGeneratePost, core.RequireSession)
	r.Page("POST "+base+"/publish/cover/keep", m.coverKeepPost, core.RequireSession)
	r.Page("GET "+base+"/published", m.publishedPage, core.RequireSession)
	r.Page("GET /studio/p/{project}/series", m.seriesPage, core.RequireSession)
	r.Page("POST /studio/p/{project}/series", m.seriesPost, core.RequireSession)
}

func sessionActor(r *http.Request) (studio.Actor, error) {
	id := core.IdentityFrom(r.Context())
	if id.User == nil || id.User.ID == "" || id.Token != nil || id.SessionID == "" {
		return studio.Actor{}, errForbidden
	}
	return studio.Actor{UserID: id.User.ID, SiteRole: id.User.Role}, nil
}

// httpError maps service errors to pages: anything not visible is 404,
// members without a publishing role get 403.
func httpError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound), errors.Is(err, studio.ErrNotFound):
		return core.ErrNotFound()
	case errors.Is(err, errForbidden), errors.Is(err, studio.ErrForbidden):
		return core.ErrForbidden("forbidden")
	case errors.Is(err, studio.ErrLocked):
		return core.ErrLocked("locked")
	case errors.Is(err, ErrConflict), errors.Is(err, studio.ErrConflict):
		return core.ErrConflict("conflict")
	case errors.Is(err, ErrInvalid), errors.Is(err, studio.ErrInvalid):
		return core.ErrBadRequest("invalid")
	}
	return err
}

// target is the project and chapter a publish route acts on, for a member
// allowed to publish.
type target struct {
	actor   studio.Actor
	project studio.Project
	chapter studio.Chapter
	base    string // /studio/p/{id}/chapters/{n}
}

func (m *Module) loadTarget(r *http.Request) (target, error) {
	var t target
	actor, err := sessionActor(r)
	if err != nil {
		return t, core.ErrNotFound()
	}
	t.actor = actor
	projectID := r.PathValue("project")
	if !db.ValidID(projectID) {
		return t, core.ErrNotFound()
	}
	if err := m.svc.authorizePublish(r.Context(), m.svc.db, actor, projectID); err != nil {
		return t, httpError(err)
	}
	if t.project, err = m.svc.studio.GetProject(r.Context(), projectID); err != nil {
		return t, core.ErrNotFound()
	}
	if r.PathValue("chapter") != "" {
		chapters, err := m.svc.studio.ListChapters(r.Context(), projectID)
		if err != nil {
			return t, err
		}
		want := r.PathValue("chapter")
		for _, c := range chapters {
			if c.ID == want || strconv.Itoa(c.Number) == want {
				t.chapter = c
				break
			}
		}
		if t.chapter.ID == "" {
			return t, core.ErrNotFound()
		}
		t.base = "/studio/p/" + url.PathEscape(projectID) + "/chapters/" + strconv.Itoa(t.chapter.Number)
	}
	return t, nil
}

func (t target) publishURL(lang string, extra ...string) string {
	q := url.Values{}
	if lang != "" {
		q.Set("lang", lang)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	if len(q) == 0 {
		return t.base + "/publish"
	}
	return t.base + "/publish?" + q.Encode()
}

func (m *Module) publishPage(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	v, err := m.buildView(r, t, r.URL.Query().Get("lang"))
	if err != nil {
		return err
	}
	if id := r.URL.Query().Get("job"); id != "" {
		j, err := m.ownJob(r.Context(), t, id)
		if err != nil {
			return core.ErrNotFound()
		}
		if j.State == jobs.StateDone {
			core.Redirect(w, r, t.base+"/published?lang="+url.QueryEscape(v.Lang))
			return nil
		}
		v.Job = j
	}
	if id := r.URL.Query().Get("cover_job"); id != "" {
		v.CoverJob = m.coverJobView(r.Context(), t, id)
	}
	return m.renderPublish(w, r, http.StatusOK, v)
}

func (m *Module) renderPublish(w http.ResponseWriter, r *http.Request, status int, v publishView) error {
	if m.ui == nil {
		return errors.New("publish: web UI is not available")
	}
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(r.Context(), "publish.title", "number", v.Chapter.Number),
		Section: layout.SectionStudio, Nav: layout.NavProjects, BackURL: v.BackURL}, publishScreen(v))
}

// ownJob loads a publish job of this user for this chapter.
func (m *Module) ownJob(ctx context.Context, t target, id string) (*jobs.Job, error) {
	if !db.ValidID(id) {
		return nil, ErrNotFound
	}
	j, err := m.svc.jobs.Get(ctx, id)
	if err != nil || j.UserID != t.actor.UserID || j.Type != JobType || j.Scope.ProjectID != t.project.ID {
		return nil, ErrNotFound
	}
	var in publishInput
	if err := j.DecodeInput(&in); err != nil || in.ChapterID != t.chapter.ID {
		return nil, ErrNotFound
	}
	return j, nil
}

// publishJob is the HTMX progress poll; a finished job redirects to Published.
func (m *Module) publishJob(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	j, err := m.ownJob(r.Context(), t, r.PathValue("job"))
	if err != nil {
		return core.ErrNotFound()
	}
	var in publishInput
	_ = j.DecodeInput(&in)
	if j.State == jobs.StateDone {
		target := t.base + "/published?lang=" + url.QueryEscape(in.Lang)
		if r.Header.Get("HX-Request") != "" {
			w.Header().Set("HX-Redirect", target)
			w.WriteHeader(http.StatusNoContent)
			return nil
		}
		core.Redirect(w, r, target)
		return nil
	}
	if r.Header.Get("HX-Request") == "" {
		core.Redirect(w, r, t.publishURL(in.Lang, "job", j.ID))
		return nil
	}
	return web.Fragment(w, r, http.StatusOK, jobNotice(publishView{Base: t.base, Lang: in.Lang, Job: j}))
}

func (m *Module) publishPost(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	v, err := m.buildView(r, t, web.FormValue(r, "lang"))
	if err != nil {
		return err
	}
	f, errs := m.readPublishForm(r, &v)
	if pageLang := web.FormValue(r, "page_lang"); pageLang != "" && pageLang != v.Lang {
		// Another language was picked: show its lettering and alt-text drafts
		// for review instead of publishing edits made for the previous one.
		f.AILabel, f.AltText, f.When = v.Form.AILabel, map[string]string{}, v.Form.When
		v.Form, v.Switched = f, true
		return m.renderPublish(w, r, http.StatusOK, v)
	}
	v.Form, v.Errors = f, errs
	if len(errs) > 0 {
		v.applyFormAlt(r)
		return m.renderPublish(w, r, http.StatusUnprocessableEntity, v)
	}
	var series Series
	err = m.svc.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := m.svc.authorizePublish(ctx, tx, t.actor, t.project.ID); err != nil {
			return err
		}
		if series, err = m.svc.ensureSeriesTx(ctx, tx, t.actor, t.project, f.Visibility); err != nil {
			return err
		}
		return m.svc.setSeriesRulesTx(ctx, tx, series.ID, f.AgeRating, f.Licence)
	})
	if err != nil {
		return httpError(err)
	}
	in := publishInput{ProjectID: t.project.ID, ChapterID: t.chapter.ID, SeriesID: series.ID, Lang: v.Lang, Title: f.Title,
		Synopsis: f.Synopsis, Tags: normalizeTags([]string{f.Tags}, false), Visibility: f.Visibility, Notify: f.Notify,
		AILabel: f.AILabel, AltText: f.AltText}
	if f.When == "schedule" && v.Pub.Status != PublicationPublished {
		at := f.ScheduledAt
		in.ScheduledAt = &at
	}
	j, err := m.svc.enqueuePublish(ctx, t.actor, in)
	if err != nil {
		return httpError(err)
	}
	core.Redirect(w, r, t.publishURL(v.Lang, "job", j.ID))
	return nil
}

func (m *Module) unpublishPost(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	lang := web.FormValue(r, "lang")
	if err := m.svc.unpublish(r.Context(), t.actor, t.project.ID, t.chapter.ID, lang); err != nil {
		return httpError(err)
	}
	web.Flash(w, r, "success", i18n.T(r.Context(), "publish.flash.unpublished", "number", t.chapter.Number), "")
	core.Redirect(w, r, t.publishURL(lang))
	return nil
}

// pageImage renders a live page of the chapter for the cover picker.
func (m *Module) pageImage(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	page, err := m.svc.pages.GetPage(r.Context(), r.PathValue("page"))
	if err != nil || page.ProjectID != t.project.ID || page.ChapterID != t.chapter.ID {
		return core.ErrNotFound()
	}
	lang := r.URL.Query().Get("lang")
	if !pagedoc.IsContentLanguage(lang) {
		lang = t.project.Language
	}
	scale := 1.0
	if page.Width > 240 {
		scale = 240 / float64(page.Width)
	}
	b, err := m.svc.renderer.PNG(r.Context(), page, compose.Options{Lang: lang, Scale: scale})
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	_, err = w.Write(b)
	return err
}

func (m *Module) coverPagePost(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	lang := web.FormValue(r, "lang")
	if !pagedoc.IsContentLanguage(lang) {
		lang = t.project.Language
	}
	if err := m.svc.chooseCoverPage(r.Context(), t.actor, t.project, t.chapter, web.FormValue(r, "page"), lang); err != nil {
		return httpError(err)
	}
	web.Flash(w, r, "success", i18n.T(r.Context(), "publish.flash.cover"), "")
	core.Redirect(w, r, t.publishURL(lang))
	return nil
}

func (m *Module) coverGeneratePost(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	j, err := m.svc.generateCover(r.Context(), t.actor, t.project, t.chapter)
	if err != nil {
		return httpError(err)
	}
	core.Redirect(w, r, t.publishURL(web.FormValue(r, "lang"), "cover_job", j.ID))
	return nil
}

func (m *Module) coverKeepPost(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	index, err := strconv.Atoi(web.FormValue(r, "index"))
	if err != nil {
		return core.ErrBadRequest("index")
	}
	jobID := web.FormValue(r, "cover_job")
	if !db.ValidID(jobID) {
		return core.ErrNotFound()
	}
	if err := m.svc.keepGeneratedCover(r.Context(), t.actor, t.project, jobID, index); err != nil {
		return httpError(err)
	}
	web.Flash(w, r, "success", i18n.T(r.Context(), "publish.flash.cover"), "")
	core.Redirect(w, r, t.publishURL(web.FormValue(r, "lang")))
	return nil
}

// coverJobView reads a cover.generate job for the E1 cover column.
func (m *Module) coverJobView(ctx context.Context, t target, id string) *coverJobState {
	if !db.ValidID(id) {
		return nil
	}
	j, err := m.svc.jobs.Get(ctx, id)
	if err != nil || j.UserID != t.actor.UserID || j.Type != gen.JobCover || j.Scope.ChapterID != t.chapter.ID {
		return nil
	}
	st := &coverJobState{Job: j}
	if j.State == jobs.StateDone {
		if res, err := m.svc.coverCandidates(ctx, t.actor, id); err == nil {
			st.Result = res
		}
	}
	return st
}

func (m *Module) publishedPage(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	v, err := m.buildView(r, t, r.URL.Query().Get("lang"))
	if err != nil {
		return err
	}
	if v.Pub.ID == "" || (v.Pub.Status != PublicationPublished && v.Pub.Status != PublicationScheduled) {
		core.Redirect(w, r, t.publishURL(v.Lang))
		return nil
	}
	if m.ui == nil {
		return errors.New("publish: web UI is not available")
	}
	return m.ui.Plain(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(r.Context(), "publish.live.title", "number", v.Chapter.Number)},
		publishedScreen(v))
}

func (m *Module) seriesPage(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	v, err := m.seriesView(r, t)
	if err != nil {
		return err
	}
	return m.renderSeries(w, r, http.StatusOK, v)
}

func (m *Module) renderSeries(w http.ResponseWriter, r *http.Request, status int, v seriesView) error {
	if m.ui == nil {
		return errors.New("publish: web UI is not available")
	}
	return m.ui.Page(w, r, status, web.PageOpts{Title: i18n.T(r.Context(), "publish.series.title"), Section: layout.SectionStudio,
		Nav: layout.NavProjects, BackURL: "/studio"}, seriesScreen(v))
}

func (m *Module) seriesPost(w http.ResponseWriter, r *http.Request) error {
	t, err := m.loadTarget(r)
	if err != nil {
		return err
	}
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	v, err := m.seriesView(r, t)
	if err != nil {
		return err
	}
	in := seriesInput{Slug: web.FormValue(r, "slug"), Title: web.FormValue(r, "title"), Synopsis: web.FormValue(r, "synopsis"),
		Genres: []string{web.FormValue(r, "genres")}, Tags: []string{web.FormValue(r, "tags")}, Format: web.FormValue(r, "format"),
		AgeRating: web.FormValue(r, "age_rating"), Licence: web.FormValue(r, "licence"), Visibility: web.FormValue(r, "visibility"),
		Status: web.FormValue(r, "status"), OriginalLang: web.FormValue(r, "original_lang")}
	for i := range maxSupportLinks {
		n := strconv.Itoa(i)
		in.SupportLinks = append(in.SupportLinks, SupportLink{Label: web.FormValue(r, "link_label_"+n), URL: web.FormValue(r, "link_url_"+n)})
	}
	in.normalize()
	v.Form, v.Errors = in, in.validate()
	var expected time.Time
	if rev := web.FormValue(r, "revision"); rev != "" {
		if expected, err = time.Parse(time.RFC3339Nano, rev); err != nil {
			return core.ErrBadRequest("revision")
		}
	}
	if len(v.Errors) == 0 {
		err = m.svc.db.WithTx(r.Context(), func(tx *db.Tx) error {
			if err := m.svc.authorizePublish(r.Context(), tx, t.actor, t.project.ID); err != nil {
				return err
			}
			_, err := m.svc.saveSeriesTx(r.Context(), tx, t.actor, t.project, in, expected)
			return err
		})
		switch {
		case errors.Is(err, errSlugTaken):
			v.Errors["slug"] = "publish.error.slug_taken"
		case isConflict(err):
			v.Errors[""] = "publish.error.stale"
		case err != nil:
			return httpError(err)
		}
	}
	if len(v.Errors) > 0 {
		return m.renderSeries(w, r, http.StatusUnprocessableEntity, v)
	}
	web.Flash(w, r, "success", i18n.T(r.Context(), "publish.flash.series"), "")
	core.Redirect(w, r, "/studio/p/"+url.PathEscape(t.project.ID)+"/series")
	return nil
}
