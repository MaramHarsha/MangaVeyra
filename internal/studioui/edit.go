package studioui

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

const maxScriptEditBody = 4 << 20

func (m *Module) editScript(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	actor, err := browserActor(r)
	if err != nil {
		return core.ErrNotFound()
	}
	view, err := m.loadStoryView(ctx, actor, r.PathValue("project"), r.PathValue("chapter"))
	if err != nil {
		return pageObjectError(err)
	}
	if len(view.Script.Scenes) == 0 {
		return core.ErrNotFound()
	}
	if err := m.studio.Authorize(ctx, actor, view.Project.ID, studio.ActEditScript); err != nil {
		return pageObjectError(err)
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxScriptEditBody)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return core.NewError(http.StatusRequestEntityTooLarge, "too_large", "")
		}
		return core.ErrBadRequest(i18n.T(ctx, "studioui.error.form"))
	}
	edited, parseErr := applyScriptForm(view.Script, r)
	submittedRevision := r.PostFormValue("revision")
	revision, revisionErr := time.Parse(time.RFC3339Nano, submittedRevision)
	// Every re-render below keeps the revision the user started from.
	view.SubmittedRevision = &submittedRevision
	if parseErr != nil || revisionErr != nil {
		view.Script = edited
		view.Error = i18n.T(ctx, "studioui.script.error.validation")
		return m.renderStory(w, r, http.StatusUnprocessableEntity, view)
	}

	chapter, err := m.studio.EditScript(ctx, studio.EditScriptInput{Actor: actor, ProjectID: view.Project.ID,
		ChapterID: view.Chapter.ID, Doc: edited, ExpectedUpdatedAt: revision})
	if err == nil {
		web.Flash(w, r, "success", i18n.T(ctx, "studioui.script.saved"), i18n.T(ctx, "studioui.script.saved_body"))
		core.Redirect(w, r, storyPath(view.Project.ID, chapter.ID))
		return nil
	}

	view.Script = edited
	switch {
	case errors.Is(err, studio.ErrConflict):
		view.Conflict = true
		view.ReadOnly = true
		view.Error = i18n.T(ctx, "studioui.script.conflict")
		return m.renderStory(w, r, http.StatusConflict, view)
	case errors.Is(err, studio.ErrLocked):
		view.ReadOnly = true
		view.Error = i18n.T(ctx, "studioui.script.locked")
		return m.renderStory(w, r, http.StatusLocked, view)
	case errors.Is(err, studio.ErrInvalid):
		view.Error = i18n.T(ctx, "studioui.script.error.validation")
		return m.renderStory(w, r, http.StatusUnprocessableEntity, view)
	case errors.Is(err, studio.ErrForbidden), errors.Is(err, studio.ErrNotFound):
		return core.ErrNotFound()
	default:
		return err
	}
}

func applyScriptForm(canonical studio.ScriptDoc, r *http.Request) (studio.ScriptDoc, error) {
	edited := cloneScript(canonical)
	edited.ChapterSummary = r.PostFormValue("chapter_summary")
	var firstErr error
	for si := range edited.Scenes {
		scene := &edited.Scenes[si]
		scene.Title = r.PostFormValue(sceneField(scene.ID, "title"))
		scene.Summary = r.PostFormValue(sceneField(scene.ID, "summary"))
		scene.TimeOfDay = r.PostFormValue(sceneField(scene.ID, "time_of_day"))
		for pi := range scene.Panels {
			panel := &scene.Panels[pi]
			page, err := strconv.Atoi(r.PostFormValue(panelField(panel.ID, "page_number")))
			if err != nil || page < 1 || page > 9999 {
				if firstErr == nil {
					firstErr = studio.ErrInvalid
				}
			} else {
				panel.PageNumber = page
			}
			panel.Camera = r.PostFormValue(panelField(panel.ID, "camera"))
			panel.Shot = r.PostFormValue(panelField(panel.ID, "shot"))
			panel.Emotion = r.PostFormValue(panelField(panel.ID, "emotion"))
			panel.Action = r.PostFormValue(panelField(panel.ID, "action"))
			panel.Notes = r.PostFormValue(panelField(panel.ID, "notes"))
			for di := range panel.Dialogue {
				line := &panel.Dialogue[di]
				line.Speaker = r.PostFormValue(dialogueField(panel.ID, di, "speaker"))
				line.Text = r.PostFormValue(dialogueField(panel.ID, di, "text"))
				kind := r.PostFormValue(dialogueField(panel.ID, di, "kind"))
				switch kind {
				case "speech", "thought", "caption":
					line.Kind = kind
				default:
					if firstErr == nil {
						firstErr = studio.ErrInvalid
					}
				}
			}
			for xi := range panel.SFX {
				sfx := &panel.SFX[xi]
				sfx.Text = r.PostFormValue(sfxField(panel.ID, xi, "text"))
				sfx.Note = r.PostFormValue(sfxField(panel.ID, xi, "note"))
			}
		}
	}
	return edited, firstErr
}

func cloneScript(in studio.ScriptDoc) studio.ScriptDoc {
	out := in
	out.Scenes = make([]studio.ScriptScene, len(in.Scenes))
	for si, scene := range in.Scenes {
		out.Scenes[si] = scene
		out.Scenes[si].Panels = make([]studio.ScriptPanel, len(scene.Panels))
		for pi, panel := range scene.Panels {
			out.Scenes[si].Panels[pi] = panel
			out.Scenes[si].Panels[pi].Dialogue = append([]studio.ScriptDialogue(nil), panel.Dialogue...)
			out.Scenes[si].Panels[pi].SFX = append([]studio.ScriptSFX(nil), panel.SFX...)
			out.Scenes[si].Panels[pi].CharacterIDs = append([]string(nil), panel.CharacterIDs...)
		}
	}
	return out
}
