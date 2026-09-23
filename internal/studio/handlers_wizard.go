package studio

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Wizard steps (B3).
const (
	StepPremise  = "premise"
	StepGenre    = "genre"
	StepStyle    = "style"
	StepFormat   = "format"
	StepGenerate = "generate"
)

// WizardSteps lists the steps in order.
var WizardSteps = []string{StepPremise, StepGenre, StepStyle, StepFormat, StepGenerate}

// PremiseExamples are the example chips on the premise step (original ideas).
var PremiseExamples = []string{"studio.wizard.example.1", "studio.wizard.example.2", "studio.wizard.example.3"}

// WizardView is one step of the new project wizard.
type WizardView struct {
	Draft   *Draft
	Step    string
	Index   int
	Errors  web.FormErrors
	Route   RoutePreview
	Stories []StoryTemplate
	Job     *jobs.Job
	Project *Project
	SavedAt string
}

// Data returns the draft answers.
func (v WizardView) Data() DraftData { return v.Draft.Data.V }

func stepIndex(step string) int { return slices.Index(WizardSteps, step) }

func (m *Module) wizardView(r *http.Request, d *Draft, step string) WizardView {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	v := WizardView{Draft: d, Step: step, Index: stepIndex(step), Errors: web.FormErrors{}}
	v.Route = m.routePreview(ctx, usr.ID, "", JobScriptOutline)
	if step == StepGenerate {
		v.Stories = BuiltinStories(ctx)
		if more, err := m.svc.UserStories(ctx, usr.ID); err == nil {
			v.Stories = append(v.Stories, more...)
		}
	}
	return v
}

func (m *Module) renderWizard(w http.ResponseWriter, r *http.Request, status int, v WizardView) error {
	ctx := r.Context()
	c := m.ui.Chrome(w, r, web.PageOpts{Title: i18n.T(ctx, "nav.new_manga"), Transition: "turn", NoSidebar: true})
	return web.Render(w, r, status, WizardPage(c, v), nil)
}

// wizardStart opens (or resumes) the draft; ?template= and ?from= preselect.
func (m *Module) wizardStart(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	if !usr.CanCreate() {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.creator_only"))
	}
	d, err := m.svc.CurrentDraft(ctx, usr.ID, true)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	changed := false
	if id := q.Get("template"); id != "" {
		if st, err := m.svc.Story(ctx, usr.ID, id); err == nil {
			d.Data.V.TemplateID = st.ID
			if n := st.Chapters(); n > 0 {
				d.Data.V.Chapters = n
			}
			if st.LayoutID != "" {
				d.Data.V.LayoutID = st.LayoutID
			}
			changed = true
		} else if lt, err := m.svc.Layout(ctx, usr.ID, id); err == nil {
			d.Data.V.LayoutID = lt.ID
			if lt.Vertical {
				d.Data.V.Format, d.Data.V.PageSize = FormatWebtoon, "webtoon"
			}
			changed = true
		}
	}
	if from := q.Get("from"); from == "sketch" || from == "import" {
		d.Data.V.From, changed = from, true
	}
	if changed {
		if err := m.svc.SaveDraft(ctx, d); err != nil {
			return err
		}
	}
	step := d.Step
	if stepIndex(step) < 0 {
		step = StepPremise
	}
	core.Redirect(w, r, "/studio/new/"+step)
	return nil
}

func (m *Module) wizardDiscard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	if id := web.FormValue(r, "draft"); id != "" {
		if err := m.svc.DiscardDraft(ctx, usr.ID, id); err != nil {
			return err
		}
	}
	core.Redirect(w, r, "/studio")
	return nil
}

func (m *Module) wizardStep(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	step := r.PathValue("step")
	if stepIndex(step) < 0 {
		return core.ErrNotFound()
	}
	if !usr.CanCreate() {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.creator_only"))
	}
	d, err := m.svc.CurrentDraft(ctx, usr.ID, true)
	if err != nil {
		return err
	}
	// Steps after an unanswered premise send the member back to it.
	if step != StepPremise && strings.TrimSpace(d.Data.V.Premise) == "" {
		core.Redirect(w, r, "/studio/new/"+StepPremise)
		return nil
	}
	return m.renderWizard(w, r, http.StatusOK, m.wizardView(r, d, step))
}

var pagesPattern = regexp.MustCompile(`^\s*\d{1,3}\s*(?:[-–]\s*\d{1,3}\s*)?$`)

// readStep copies the posted answers of step into d and validates them.
func readStep(r *http.Request, step string, d *DraftData, errs web.FormErrors, strict bool) {
	ctx := r.Context()
	switch step {
	case StepPremise:
		d.Premise = clip(r.PostFormValue("premise"), 600)
		if t, ok := r.PostForm["title"]; ok {
			d.Title = clip(strings.Join(t, " "), 120)
		}
		if strict && len([]rune(d.Premise)) < 10 {
			errs.Add("premise", i18n.T(ctx, "studio.wizard.premise.short"))
		}
	case StepGenre:
		var g []string
		for _, v := range r.PostForm["genres"] {
			if slices.Contains(Genres, v) && !slices.Contains(g, v) {
				g = append(g, v)
			}
		}
		d.Genres = g
		if strict && len(g) == 0 {
			errs.Add("genres", i18n.T(ctx, "studio.wizard.genre.min"))
		}
		if len(g) > 3 {
			errs.Add("genres", i18n.T(ctx, "studio.wizard.genre.max"))
		}
	case StepStyle:
		if s := r.PostFormValue("style"); slices.Contains(Styles, s) {
			d.Style = s
		} else if strict {
			errs.Add("style", i18n.T(ctx, "studio.form.invalid"))
		}
	case StepFormat:
		if f := r.PostFormValue("format"); slices.Contains(Formats, f) {
			d.Format = f
		} else if strict {
			errs.Add("format", i18n.T(ctx, "studio.form.invalid"))
		}
		if ps := r.PostFormValue("page_size"); ps != "" {
			d.PageSize = PageSizeFor(ps).Key
		}
		if d.Format == FormatWebtoon {
			d.PageSize = "webtoon"
		} else if d.PageSize == "webtoon" {
			d.PageSize = "b5"
		}
		if c := strings.TrimSpace(r.PostFormValue("chapters")); c != "" {
			n, err := strconv.Atoi(c)
			if err != nil || n < 1 || n > 200 {
				errs.Add("chapters", i18n.T(ctx, "studio.wizard.format.chapters_invalid"))
			} else {
				d.Chapters = n
			}
		}
		if p := r.PostFormValue("pages_per_chapter"); p != "" {
			if !pagesPattern.MatchString(p) {
				errs.Add("pages_per_chapter", i18n.T(ctx, "studio.wizard.format.pages_invalid"))
			} else {
				d.PagesPerChapter = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(p), " ", ""), "–", "-")
			}
		}
	case StepGenerate:
		if t, ok := r.PostForm["template_id"]; ok {
			d.TemplateID = strings.Join(t, "")
		}
		if t := r.PostFormValue("title"); t != "" {
			d.Title = clip(t, 120)
		}
	}
}

func (m *Module) wizardSave(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	step := r.PathValue("step")
	if stepIndex(step) < 0 {
		return core.ErrNotFound()
	}
	if !usr.CanCreate() {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.creator_only"))
	}
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	d, err := m.svc.CurrentDraft(ctx, usr.ID, true)
	if err != nil {
		return err
	}
	if id := r.PostFormValue("draft"); id != "" && id != d.ID {
		// Retrying a generated draft.
		if d2, err := m.svc.DraftByID(ctx, usr.ID, id); err == nil {
			d = d2
		}
	}
	autosave := r.PostFormValue("autosave") == "1"
	if ex := r.PostFormValue("example"); ex != "" && step == StepPremise && slices.Contains(PremiseExamples, ex) {
		d.Data.V.Premise = i18n.T(ctx, ex)
		d.Step = StepPremise
		if err := m.svc.SaveDraft(ctx, d); err != nil {
			return err
		}
		core.Redirect(w, r, "/studio/new/"+StepPremise)
		return nil
	}
	errs := web.FormErrors{}
	readStep(r, step, &d.Data.V, errs, !autosave)
	if autosave {
		if err := m.svc.SaveDraft(ctx, d); err != nil {
			return err
		}
		v := m.wizardView(r, d, step)
		v.SavedAt = i18n.T(ctx, "studio.wizard.saved")
		return web.Fragment(w, r, http.StatusOK, WizardAutosaved(v))
	}
	if errs.Has() {
		v := m.wizardView(r, d, step)
		v.Errors = errs
		return m.renderWizard(w, r, http.StatusUnprocessableEntity, v)
	}
	if step != StepGenerate {
		next := WizardSteps[stepIndex(step)+1]
		if stepIndex(d.Step) < stepIndex(next) {
			d.Step = next
		}
		if err := m.svc.SaveDraft(ctx, d); err != nil {
			return err
		}
		core.Redirect(w, r, "/studio/new/"+next)
		return nil
	}
	return m.wizardGenerate(w, r, d)
}

// wizardGenerate creates the project and enqueues script.outline.
func (m *Module) wizardGenerate(w http.ResponseWriter, r *http.Request, d *Draft) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	dd := d.Data.V
	if strings.TrimSpace(dd.Premise) == "" {
		core.Redirect(w, r, "/studio/new/"+StepPremise)
		return nil
	}
	var p *Project
	var err error
	if d.ProjectID != "" {
		if p, err = m.svc.Project(ctx, d.ProjectID); err != nil {
			return err
		}
	} else {
		title := dd.Title
		if title == "" {
			title = SuggestTitle(dd.Premise)
		}
		p, err = m.svc.CreateProject(ctx, usr.ID, NewProject{Title: title, Premise: dd.Premise, Genres: dd.Genres, Style: dd.Style,
			Format: dd.Format, PageSize: dd.PageSize, Language: i18n.LocaleFrom(ctx), ChaptersPlanned: dd.Chapters,
			PagesPerChapter: dd.PagesPerChapter, TemplateID: dd.TemplateID})
		if err != nil {
			return err
		}
		if dd.LayoutID != "" {
			_ = m.svc.SetProjectSetting(ctx, p.ID, "default_layout", dd.LayoutID)
		}
		d.ProjectID = p.ID
	}
	in := OutlineInput{Title: p.Title, Premise: dd.Premise, Genres: jsonList(dd.Genres), Style: dd.Style, Format: dd.Format,
		Chapters: dd.Chapters, PagesPerChapter: dd.PagesPerChapter, Lang: p.Language}
	if dd.TemplateID != "" {
		if st, err := m.svc.Story(ctx, usr.ID, dd.TemplateID); err == nil {
			in.Structure = st.Arcs
			in.Chapters = st.Chapters()
		}
	}
	j, err := m.svc.EnqueueOutline(ctx, ActorFrom(ctx), p, in)
	if err != nil {
		return err
	}
	d.JobID, d.Step = j.ID, StepGenerate
	if err := m.svc.SaveDraft(ctx, d); err != nil {
		return err
	}
	core.Redirect(w, r, "/studio/new/status?draft="+d.ID)
	return nil
}

// wizardStatus shows the generation progress; when the outline is ready it goes to the story editor.
func (m *Module) wizardStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	usr := core.UserFrom(ctx)
	d, err := m.svc.DraftByID(ctx, usr.ID, r.URL.Query().Get("draft"))
	if err != nil {
		return err
	}
	if d.JobID == "" || d.ProjectID == "" {
		core.Redirect(w, r, "/studio/new/"+StepGenerate)
		return nil
	}
	j, err := m.d.Jobs.Get(ctx, d.JobID)
	if err != nil {
		return err
	}
	story := "/studio/p/" + d.ProjectID + "/story"
	if j.State == jobs.StateDone {
		if htmx(r) {
			w.Header().Set("HX-Redirect", story)
			w.WriteHeader(http.StatusNoContent)
			return nil
		}
		core.Redirect(w, r, story)
		return nil
	}
	v := m.wizardView(r, d, StepGenerate)
	v.Job = j
	v.Project, _ = m.svc.Project(ctx, d.ProjectID)
	if htmx(r) {
		return web.Fragment(w, r, http.StatusOK, GenerateProgress(v))
	}
	return m.renderWizard(w, r, http.StatusOK, v)
}

// SuggestTitle makes a working title from a premise ("Courier Letters").
func SuggestTitle(premise string) string {
	m := motifs(premise)
	if len(m) > 2 {
		m = m[:2]
	}
	t := title(strings.Join(m, " "))
	if t == "" {
		return "Untitled"
	}
	return t
}
