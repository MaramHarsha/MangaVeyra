package studioui

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

func (m *Module) projectStory(w http.ResponseWriter, r *http.Request) error {
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrNotFound()
	}
	project, chapters, err := m.loadProject(r.Context(), actor, r.PathValue("project"))
	if err != nil {
		return pageObjectError(err)
	}
	if len(chapters) == 0 {
		canEdit, err := m.can(r.Context(), actor, project.ID, studio.ActEditScript)
		if err != nil {
			return err
		}
		return m.renderNoChapter(w, r, http.StatusOK, noChapterView{Project: project, CanEdit: canEdit})
	}
	latest := chapters[0]
	for _, c := range chapters[1:] {
		if c.UpdatedAt.After(latest.UpdatedAt) || (c.UpdatedAt.Equal(latest.UpdatedAt) && c.Number > latest.Number) {
			latest = c
		}
	}
	core.Redirect(w, r, storyPath(project.ID, latest.ID))
	return nil
}

func (m *Module) createChapter(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrNotFound()
	}
	project, chapters, err := m.loadProject(ctx, actor, r.PathValue("project"))
	if err != nil {
		return pageObjectError(err)
	}
	if err := m.studio.Authorize(ctx, actor, project.ID, studio.ActEditScript); err != nil {
		return pageObjectError(err)
	}
	if err := web.ParseForm(w, r); err != nil {
		return err
	}
	title := web.FormValue(r, "chapter_title")
	if title == "" || len([]rune(title)) > 160 {
		return m.renderNoChapter(w, r, http.StatusUnprocessableEntity, noChapterView{Project: project,
			Chapters: chapters, CanEdit: true, Error: i18n.T(ctx, "studioui.chapter.error.title")})
	}
	chapter, err := m.studio.CreateChapter(ctx, actor, project.ID, title)
	if err != nil {
		if errors.Is(err, studio.ErrInvalid) || errors.Is(err, studio.ErrConflict) {
			return m.renderNoChapter(w, r, http.StatusUnprocessableEntity, noChapterView{Project: project,
				Chapters: chapters, CanEdit: true, Error: i18n.T(ctx, "studioui.chapter.error.create")})
		}
		return pageObjectError(err)
	}
	core.Redirect(w, r, storyPath(project.ID, chapter.ID))
	return nil
}

func (m *Module) chapterStory(w http.ResponseWriter, r *http.Request) error {
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrNotFound()
	}
	v, err := m.loadStoryView(r.Context(), actor, r.PathValue("project"), r.PathValue("chapter"))
	if err != nil {
		return pageObjectError(err)
	}
	if sceneID := r.PathValue("scene"); sceneID != "" {
		index := -1
		for i, scene := range v.Script.Scenes {
			if scene.ID == sceneID {
				index = i
			}
		}
		if index < 0 {
			return core.ErrNotFound()
		}
		v.SceneIndex = index
	}
	if r.URL.Query().Get("view") == viewProse {
		v.View = viewProse
	}
	return m.renderStory(w, r, http.StatusOK, v)
}

// makePages uses the saved manuscript and never implicitly deletes existing pages.
func (m *Module) makePages(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrNotFound()
	}
	view, err := m.loadStoryView(ctx, actor, r.PathValue("project"), r.PathValue("chapter"))
	if err != nil {
		return pageObjectError(err)
	}
	editor := editorPath(view.Project.ID, view.Chapter.ID)
	// Pages already exist, so the button only opens the editor. Every page save
	// moves the chapter revision, so checking a revision here would turn a
	// Story page left open during editing into a dead end.
	if view.Chapter.PageCount != 0 {
		core.Redirect(w, r, editor)
		return nil
	}
	for _, action := range []studio.Action{studio.ActGenerate, studio.ActEditArt, studio.ActLetter} {
		if err := m.studio.Authorize(ctx, actor, view.Project.ID, action); err != nil {
			return pageObjectError(err)
		}
	}
	if view.Chapter.Locked {
		return core.ErrLocked(i18n.T(ctx, "studioui.generate.locked"))
	}
	if err := web.ParseForm(w, r); err != nil {
		return err
	}
	revision, err := time.Parse(time.RFC3339Nano, r.PostFormValue("revision"))
	if err != nil {
		return core.ErrBadRequest(i18n.T(ctx, "studioui.pages.invalid_revision"))
	}
	if !revision.Equal(view.Chapter.UpdatedAt) {
		return core.ErrConflict(i18n.T(ctx, "studioui.generate.changed"))
	}
	if len(view.Script.Scenes) == 0 {
		return core.ErrConflict(i18n.T(ctx, "studioui.pages.no_script"))
	}
	active, err := m.d.Jobs.List(ctx, jobs.Filter{UserID: actor.UserID, ProjectID: view.Project.ID,
		ChapterID: view.Chapter.ID, Types: []string{gen.JobPagesFromScript}, States: []string{jobs.StateQueued, jobs.StateRunning}, Limit: 1})
	if err != nil {
		return err
	}
	if len(active) != 0 {
		core.Redirect(w, r, editor+"?job="+active[0].ID)
		return nil
	}
	job, err := m.gen.EnqueuePagesFromScript(ctx, actor, gen.PagesFromScriptRequest{ProjectID: view.Project.ID,
		ChapterID: view.Chapter.ID, Letter: true, ExpectedUpdatedAt: revision})
	if err != nil {
		if errors.Is(err, studio.ErrConflict) {
			return core.ErrConflict(i18n.T(ctx, "studioui.generate.changed"))
		}
		web.Flash(w, r, "error", i18n.T(ctx, "studioui.pages.failed"), safeGenerationMessage(ctx, err))
		core.Redirect(w, r, storyPath(view.Project.ID, view.Chapter.ID))
		return nil
	}
	core.Redirect(w, r, editor+"?job="+job.ID)
	return nil
}

func (m *Module) generateScript(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrNotFound()
	}
	project, chapter, _, err := m.loadChapter(ctx, actor, r.PathValue("project"), r.PathValue("chapter"))
	if err != nil {
		return pageObjectError(err)
	}
	for _, action := range []studio.Action{studio.ActGenerate, studio.ActEditScript} {
		if err := m.studio.Authorize(ctx, actor, project.ID, action); err != nil {
			return pageObjectError(err)
		}
	}
	if chapter.Locked {
		return core.ErrLocked(i18n.T(ctx, "studioui.generate.locked"))
	}
	doc, err := m.studio.GetScript(ctx, chapter.ID)
	if err != nil {
		return pageObjectError(err)
	}
	if len(doc.Scenes) != 0 {
		return core.ErrConflict(i18n.T(ctx, "studioui.generate.exists"))
	}
	active, err := m.d.Jobs.List(ctx, jobs.Filter{ProjectID: project.ID, ChapterID: chapter.ID,
		Types: []string{gen.JobScript}, States: []string{jobs.StateQueued, jobs.StateRunning}, Limit: 1})
	if err != nil {
		return err
	}
	if len(active) != 0 {
		return core.ErrConflict(i18n.T(ctx, "studioui.generate.active"))
	}
	if err := web.ParseForm(w, r); err != nil {
		return err
	}
	pages := defaultPagesTarget
	if raw := web.FormValue(r, "pages_target"); raw != "" {
		pages, err = strconv.Atoi(raw)
		if err != nil || pages < 1 || pages > 256 {
			return core.ErrValidation(map[string]string{"pages_target": i18n.T(ctx, "studioui.new.error.pages")})
		}
	}
	source, err := m.studio.SourceText(ctx, project.ID)
	if err != nil {
		return pageObjectError(err)
	}
	mode := "premise"
	if strings.TrimSpace(source) != "" {
		mode = "adapt"
	}
	// chapter was read before the "no script yet" check above, so binding the
	// job to its revision makes that check atomic with the enqueue.
	_, err = m.gen.EnqueueScript(ctx, actor, gen.ScriptRequest{ProjectID: project.ID, ChapterID: chapter.ID,
		Mode: mode, Premise: project.Premise, SourceText: source, PagesTarget: pages, ExpectedUpdatedAt: chapter.UpdatedAt})
	switch {
	case errors.Is(err, gen.ErrScriptJobActive):
		return core.ErrConflict(i18n.T(ctx, "studioui.generate.active"))
	case errors.Is(err, studio.ErrConflict):
		return core.ErrConflict(i18n.T(ctx, "studioui.generate.changed"))
	}
	if err != nil {
		web.Flash(w, r, "error", i18n.T(ctx, "studioui.generate.failed"), safeGenerationMessage(ctx, err))
	} else {
		web.Flash(w, r, "success", i18n.T(ctx, "studioui.generate.queued"), "")
	}
	core.Redirect(w, r, storyPath(project.ID, chapter.ID))
	return nil
}

func (m *Module) scriptJob(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrNotFound()
	}
	project, chapter, _, err := m.loadChapter(ctx, actor, r.PathValue("project"), r.PathValue("chapter"))
	if err != nil {
		return pageObjectError(err)
	}
	job, err := m.ownedScriptJob(ctx, r, actor, project.ID, chapter.ID, r.PathValue("job"))
	if err != nil {
		return core.ErrNotFound()
	}
	if job.State == jobs.StateDone {
		core.Redirect(w, r, storyPath(project.ID, chapter.ID))
		return nil
	}
	position := 0
	if job.State == jobs.StateQueued {
		position, err = m.d.Jobs.Position(ctx, job)
		if err != nil {
			return err
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.Render(w, r, http.StatusOK, JobPanel(m.makeJobView(ctx, job, position)), nil)
}

func (m *Module) retryScriptJob(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrNotFound()
	}
	project, chapter, _, err := m.loadChapter(ctx, actor, r.PathValue("project"), r.PathValue("chapter"))
	if err != nil {
		return pageObjectError(err)
	}
	job, err := m.ownedScriptJob(ctx, r, actor, project.ID, chapter.ID, r.PathValue("job"))
	if err != nil {
		return core.ErrNotFound()
	}
	if job.State != jobs.StateFailed && job.State != jobs.StateCanceled {
		return core.ErrConflict(i18n.T(ctx, "studioui.job.retry_state"))
	}
	if !manualRetryAllowed(job) {
		return core.ErrConflict(i18n.T(ctx, "studioui.job.retry_blocked"))
	}
	doc, err := m.studio.GetScript(ctx, chapter.ID)
	if err != nil {
		return pageObjectError(err)
	}
	if len(doc.Scenes) != 0 {
		return core.ErrConflict(i18n.T(ctx, "studioui.generate.exists"))
	}
	for _, action := range []studio.Action{studio.ActGenerate, studio.ActEditScript} {
		if err := m.studio.Authorize(ctx, actor, project.ID, action); err != nil {
			return pageObjectError(err)
		}
	}
	if err := m.studio.CheckScopeWritable(ctx, job.Scope); err != nil {
		if errors.Is(err, studio.ErrLocked) {
			return core.ErrLocked(i18n.T(ctx, "studioui.generate.locked"))
		}
		return pageObjectError(err)
	}
	if _, err := m.d.Jobs.Retry(ctx, job.ID); err != nil {
		if errors.Is(err, jobs.ErrNotActive) {
			return core.ErrConflict(i18n.T(ctx, "studioui.job.retry_state"))
		}
		return err
	}
	core.Redirect(w, r, storyPath(project.ID, chapter.ID))
	return nil
}

func (m *Module) loadProject(ctx context.Context, actor studio.Actor, projectID string) (studio.Project, []studio.Chapter, error) {
	if !db.ValidID(projectID) {
		return studio.Project{}, nil, studio.ErrNotFound
	}
	if err := m.studio.Authorize(ctx, actor, projectID, studio.ActRead); err != nil {
		return studio.Project{}, nil, err
	}
	project, err := m.studio.GetProject(ctx, projectID)
	if err != nil {
		return studio.Project{}, nil, err
	}
	chapters, err := m.studio.ListChapters(ctx, projectID)
	return project, chapters, err
}

func (m *Module) loadChapter(ctx context.Context, actor studio.Actor, projectID, chapterID string) (studio.Project, studio.Chapter, []studio.Chapter, error) {
	project, chapters, err := m.loadProject(ctx, actor, projectID)
	if err != nil {
		return studio.Project{}, studio.Chapter{}, nil, err
	}
	if !db.ValidID(chapterID) {
		return studio.Project{}, studio.Chapter{}, nil, studio.ErrNotFound
	}
	chapter, err := m.studio.GetChapter(ctx, chapterID)
	if err != nil || chapter.ProjectID != project.ID {
		if err == nil {
			err = studio.ErrForbidden
		}
		return studio.Project{}, studio.Chapter{}, nil, err
	}
	return project, chapter, chapters, nil
}

// loadStorySnapshot fences the manuscript read with the chapter revision.
// A generation commit can otherwise land after loadChapter but before
// GetScript, pairing a ready manuscript with the old form revision. Retrying
// also rechecks membership; continually changing chapters fail with a conflict.
func (m *Module) loadStorySnapshot(ctx context.Context, actor studio.Actor, projectID, chapterID string) (studio.Project, studio.Chapter, []studio.Chapter, studio.ScriptDoc, error) {
	for range 3 {
		project, chapter, chapters, err := m.loadChapter(ctx, actor, projectID, chapterID)
		if err != nil {
			return studio.Project{}, studio.Chapter{}, nil, studio.ScriptDoc{}, err
		}
		doc, err := m.studio.GetScript(ctx, chapter.ID)
		if err != nil {
			return studio.Project{}, studio.Chapter{}, nil, studio.ScriptDoc{}, err
		}
		latest, err := m.studio.GetChapter(ctx, chapter.ID)
		if err != nil {
			return studio.Project{}, studio.Chapter{}, nil, studio.ScriptDoc{}, err
		}
		if !chapter.UpdatedAt.Equal(latest.UpdatedAt) {
			continue
		}
		for i := range chapters {
			if chapters[i].ID == latest.ID {
				chapters[i] = latest
			}
		}
		return project, latest, chapters, doc, nil
	}
	return studio.Project{}, studio.Chapter{}, nil, studio.ScriptDoc{}, core.ErrConflict(i18n.T(ctx, "studioui.generate.changed"))
}

func (m *Module) loadStoryView(ctx context.Context, actor studio.Actor, projectID, chapterID string) (storyView, error) {
	project, chapter, chapters, doc, err := m.loadStorySnapshot(ctx, actor, projectID, chapterID)
	if err != nil {
		return storyView{}, err
	}
	var job *jobView
	if len(doc.Scenes) == 0 {
		found, err := m.d.Jobs.List(ctx, jobs.Filter{UserID: actor.UserID, ProjectID: project.ID,
			ChapterID: chapter.ID, Types: []string{gen.JobScript}, Limit: 1})
		if err != nil {
			return storyView{}, err
		}
		if len(found) == 1 && validScriptJob(found[0], actor.UserID, project.ID, chapter.ID) {
			if found[0].State == jobs.StateDone {
				// Completion commits the manuscript and chapter revision together,
				// but it may land after the empty script snapshot above. Recover
				// both here so the ready job cannot strand C1 on its empty form.
				// One reload also bounds recovery for empty legacy results.
				project, chapter, chapters, doc, err = m.loadStorySnapshot(ctx, actor, project.ID, chapter.ID)
				if err != nil {
					return storyView{}, err
				}
			}
			if len(doc.Scenes) == 0 {
				position := 0
				if found[0].State == jobs.StateQueued {
					position, err = m.d.Jobs.Position(ctx, found[0])
					if err != nil {
						return storyView{}, err
					}
				}
				job = m.makeJobView(ctx, found[0], position)
			}
		}
	}
	canEdit, err := m.can(ctx, actor, project.ID, studio.ActEditScript)
	if err != nil {
		return storyView{}, err
	}
	canGenerate, err := m.can(ctx, actor, project.ID, studio.ActGenerate)
	if err != nil {
		return storyView{}, err
	}
	canArt, err := m.can(ctx, actor, project.ID, studio.ActEditArt)
	if err != nil {
		return storyView{}, err
	}
	canLetter, err := m.can(ctx, actor, project.ID, studio.ActLetter)
	if err != nil {
		return storyView{}, err
	}
	return storyView{Project: project, Chapter: chapter, Chapters: chapters, Script: doc, CanEdit: canEdit,
		CanGenerate: canEdit && canGenerate, CanMakePages: canGenerate && canArt && canLetter,
		ReadOnly: chapter.Locked || !canEdit, Job: job}, nil
}

func (m *Module) can(ctx context.Context, actor studio.Actor, projectID string, action studio.Action) (bool, error) {
	err := m.studio.Authorize(ctx, actor, projectID, action)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, studio.ErrForbidden) {
		return false, nil
	}
	return false, err
}

func (m *Module) ownedScriptJob(ctx context.Context, r *http.Request, actor studio.Actor, projectID, chapterID, jobID string) (*jobs.Job, error) {
	if !db.ValidID(jobID) {
		return nil, jobs.ErrNotFound
	}
	job, err := m.d.Jobs.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !m.d.Jobs.CanAccess(ctx, core.IdentityFrom(r.Context()), job) || !validScriptJob(job, actor.UserID, projectID, chapterID) {
		return nil, jobs.ErrNotFound
	}
	return job, nil
}

func validScriptJob(job *jobs.Job, userID, projectID, chapterID string) bool {
	return job != nil && job.UserID == userID && job.Type == gen.JobScript && job.Scope.Kind == jobs.ScopeChapter &&
		job.Scope.ProjectID == projectID && job.Scope.ChapterID == chapterID && job.Scope.PageID == "" &&
		job.Scope.PanelID == "" && job.Scope.LayerID == "" && len(job.Scope.Region) == 0
}

func (m *Module) makeJobView(ctx context.Context, job *jobs.Job, position int) *jobView {
	v := &jobView{Job: job, StatusURL: jobPath(job.Scope.ProjectID, job.Scope.ChapterID, job.ID),
		RetryURL: jobPath(job.Scope.ProjectID, job.Scope.ChapterID, job.ID) + "/retry",
		StoryURL: storyPath(job.Scope.ProjectID, job.Scope.ChapterID)}
	switch job.State {
	case jobs.StateQueued:
		v.Active, v.State, v.Position = true, "queued", position+1
		v.Title, v.Detail = i18n.T(ctx, "studioui.job.queued"), i18n.T(ctx, "studioui.job.queued_detail")
	case jobs.StateRunning:
		v.Active, v.State = true, "generating"
		v.Title, v.Detail = i18n.T(ctx, "studioui.job.running"), i18n.T(ctx, "studioui.job.running_detail")
		v.Progress = i18n.T(ctx, "studioui.job.progress", "percent", percent(job.Progress), "step", job.Step, "steps", job.Steps)
	case jobs.StateDone:
		v.State, v.Title = "ready", i18n.T(ctx, "studioui.job.done")
		v.Detail, v.ResultLabel = i18n.T(ctx, "studioui.job.done_detail"), i18n.T(ctx, "studioui.job.result")
	case jobs.StateFailed, jobs.StateCanceled:
		v.State, v.Retryable = "failed", manualRetryAllowed(job)
		v.Title, v.Detail = i18n.T(ctx, "studioui.job.failed"), i18n.T(ctx, jobErrorKey(job))
	default:
		v.State, v.Title, v.Detail = "failed", i18n.T(ctx, "studioui.job.failed"), i18n.T(ctx, "studioui.job.failed_detail")
	}
	return v
}

func (m *Module) renderNoChapter(w http.ResponseWriter, r *http.Request, status int, v noChapterView) error {
	return m.ui.Page(w, r, status, web.PageOpts{Title: v.Project.Title, Section: layout.SectionStudio,
		Nav: layout.NavProjects, BackURL: "/studio"}, NoChapterPage(v))
}

// renderStory renders C1 inside the Studio shell with Projects highlighted,
// as designed. The phone header names the chapter.
func (m *Module) renderStory(w http.ResponseWriter, r *http.Request, status int, v storyView) error {
	mobileTitle := i18n.T(r.Context(), "studioui.story.chapter_node", "number", v.Chapter.Number, "title", v.Chapter.Title)
	return m.ui.Page(w, r, status, web.PageOpts{Title: v.Chapter.Title, MobileTitle: mobileTitle,
		Section: layout.SectionStudio, Nav: layout.NavProjects, BackURL: "/studio"}, StoryPage(v))
}

func pageObjectError(err error) error {
	if errors.Is(err, studio.ErrNotFound) || errors.Is(err, studio.ErrForbidden) || errors.Is(err, studio.ErrInvalid) || errors.Is(err, jobs.ErrNotFound) {
		return core.ErrNotFound()
	}
	return err
}
