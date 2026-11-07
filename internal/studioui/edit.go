package studioui

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

const maxScriptEditBody = 4 << 20

// maxDialogueLines bounds the lines one panel may carry in a posted form.
const maxDialogueLines = 32

// autosaveHeader marks the studio script's background save. Those requests
// get a small JSON answer instead of a redirect or a re-rendered page.
const autosaveHeader = "X-MV-Autosave"

// Without JavaScript the add and remove line buttons submit the whole script
// form with one of these intents; the editor re-renders the draft with the
// change applied (nothing is saved until Save).
const (
	intentAddLine    = "add_line"
	intentRemoveLine = "remove_line"
)

type autosaveResult struct {
	State    string `json:"state"`
	Revision string `json:"revision,omitempty"`
	Message  string `json:"message,omitempty"`
}

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
	autosave := r.Header.Get(autosaveHeader) != ""

	r.Body = http.MaxBytesReader(w, r.Body, maxScriptEditBody)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return core.NewError(http.StatusRequestEntityTooLarge, "too_large", "")
		}
		return core.ErrBadRequest(i18n.T(ctx, "studioui.error.form"))
	}
	view.SceneIndex = sceneIndexOf(view.Script, r.PostFormValue("scene"))
	edited, parseErr := applyScriptForm(view.Script, r)
	submittedRevision := r.PostFormValue("revision")
	revision, revisionErr := time.Parse(time.RFC3339Nano, submittedRevision)
	// Every re-render below keeps the revision the user started from.
	view.SubmittedRevision = &submittedRevision

	if !autosave {
		if panelID := r.PostFormValue(intentAddLine); panelID != "" {
			view.Script, view.FocusLine = addDialogueLine(edited, panelID)
			view.Dirty = true
			return m.renderStory(w, r, http.StatusOK, view)
		}
		if target := r.PostFormValue(intentRemoveLine); target != "" {
			view.Script = removeDialogueLine(edited, target)
			view.Dirty = true
			return m.renderStory(w, r, http.StatusOK, view)
		}
	}

	if parseErr != nil || revisionErr != nil {
		if autosave {
			core.JSON(w, http.StatusUnprocessableEntity, autosaveResult{State: "invalid",
				Message: i18n.T(ctx, "studioui.script.error.validation")})
			return nil
		}
		view.Script = edited
		view.Error = i18n.T(ctx, "studioui.script.error.validation")
		return m.renderStory(w, r, http.StatusUnprocessableEntity, view)
	}

	chapter, err := m.studio.EditScript(ctx, studio.EditScriptInput{Actor: actor, ProjectID: view.Project.ID,
		ChapterID: view.Chapter.ID, Doc: withoutEmptyLines(edited), ExpectedUpdatedAt: revision})
	if err == nil {
		if autosave {
			core.JSON(w, http.StatusOK, autosaveResult{State: "saved", Revision: chapter.UpdatedAt.Format(time.RFC3339Nano)})
			return nil
		}
		web.Flash(w, r, "success", i18n.T(ctx, "studioui.script.saved"), i18n.T(ctx, "studioui.script.saved_body"))
		core.Redirect(w, r, view.sceneURL())
		return nil
	}
	if autosave {
		return autosaveError(w, r, err)
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

// autosaveError answers a failed background save. Conflicts and locks are
// expected outcomes the script shows in place; other errors use the normal
// error path.
func autosaveError(w http.ResponseWriter, r *http.Request, err error) error {
	ctx := r.Context()
	switch {
	case errors.Is(err, studio.ErrConflict):
		core.JSON(w, http.StatusConflict, autosaveResult{State: "conflict", Message: i18n.T(ctx, "studioui.script.conflict")})
	case errors.Is(err, studio.ErrLocked):
		core.JSON(w, http.StatusLocked, autosaveResult{State: "locked", Message: i18n.T(ctx, "studioui.script.locked")})
	case errors.Is(err, studio.ErrInvalid):
		core.JSON(w, http.StatusUnprocessableEntity, autosaveResult{State: "invalid",
			Message: i18n.T(ctx, "studioui.script.error.validation")})
	case errors.Is(err, studio.ErrForbidden), errors.Is(err, studio.ErrNotFound):
		return core.ErrNotFound()
	default:
		return err
	}
	return nil
}

// applyScriptForm copies the posted fields onto a clone of the canonical
// script. The form names the scenes it carries ("scene"); scenes it does not
// carry keep their saved values. Dialogue lines are read from whatever line
// indices were posted, so lines added or removed in the browser round-trip.
func applyScriptForm(canonical studio.ScriptDoc, r *http.Request) (studio.ScriptDoc, error) {
	edited := cloneScript(canonical)
	posted := r.PostForm["scene"]
	if _, ok := r.PostForm["chapter_summary"]; ok || len(posted) == 0 {
		edited.ChapterSummary = r.PostFormValue("chapter_summary")
	}
	var firstErr error
	fail := func() {
		if firstErr == nil {
			firstErr = studio.ErrInvalid
		}
	}
	for si := range edited.Scenes {
		scene := &edited.Scenes[si]
		if len(posted) != 0 && !contains(posted, scene.ID) {
			continue
		}
		scene.Title = r.PostFormValue(sceneField(scene.ID, "title"))
		scene.Summary = r.PostFormValue(sceneField(scene.ID, "summary"))
		scene.TimeOfDay = r.PostFormValue(sceneField(scene.ID, "time_of_day"))
		for pi := range scene.Panels {
			panel := &scene.Panels[pi]
			page, err := strconv.Atoi(r.PostFormValue(panelField(panel.ID, "page_number")))
			if err != nil || page < 1 || page > 9999 {
				fail()
			} else {
				panel.PageNumber = page
			}
			panel.Camera = r.PostFormValue(panelField(panel.ID, "camera"))
			panel.Shot = r.PostFormValue(panelField(panel.ID, "shot"))
			panel.Emotion = r.PostFormValue(panelField(panel.ID, "emotion"))
			panel.Action = r.PostFormValue(panelField(panel.ID, "action"))
			panel.Notes = r.PostFormValue(panelField(panel.ID, "notes"))
			lines, ok := postedDialogue(r, panel.ID, panel.Dialogue)
			if !ok {
				fail()
			}
			panel.Dialogue = lines
			for xi := range panel.SFX {
				sfx := &panel.SFX[xi]
				sfx.Text = r.PostFormValue(sfxField(panel.ID, xi, "text"))
				sfx.Note = r.PostFormValue(sfxField(panel.ID, xi, "note"))
			}
		}
	}
	return edited, firstErr
}

// postedDialogue reads one panel's lines in index order. A line keeps the
// character link of the saved line at the same index only while it still
// names the same speaker.
func postedDialogue(r *http.Request, panelID string, saved []studio.ScriptDialogue) ([]studio.ScriptDialogue, bool) {
	prefix := "panel." + panelID + ".dialogue."
	seen := map[int]bool{}
	for key := range r.PostForm {
		rest, found := strings.CutPrefix(key, prefix)
		if !found {
			continue
		}
		raw, _, found := strings.Cut(rest, ".")
		index, err := strconv.Atoi(raw)
		if !found || err != nil || index < 0 || index > 9999 || strconv.Itoa(index) != raw {
			return saved, false
		}
		seen[index] = true
	}
	if len(seen) > maxDialogueLines {
		return saved, false
	}
	indices := make([]int, 0, len(seen))
	for index := range seen {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	ok := true
	lines := make([]studio.ScriptDialogue, 0, len(indices))
	for _, index := range indices {
		line := studio.ScriptDialogue{
			Speaker: r.PostFormValue(dialogueField(panelID, index, "speaker")),
			Text:    r.PostFormValue(dialogueField(panelID, index, "text")),
		}
		switch kind := r.PostFormValue(dialogueField(panelID, index, "kind")); kind {
		case "speech", "thought", "caption":
			line.Kind = kind
		default:
			line.Kind = "speech"
			ok = false
		}
		if index < len(saved) && strings.TrimSpace(saved[index].Speaker) == strings.TrimSpace(line.Speaker) {
			line.CharacterID = saved[index].CharacterID
		}
		lines = append(lines, line)
	}
	return lines, ok
}

// addDialogueLine appends an empty speech line to a panel and returns the
// field to focus.
func addDialogueLine(doc studio.ScriptDoc, panelID string) (studio.ScriptDoc, string) {
	for si := range doc.Scenes {
		for pi := range doc.Scenes[si].Panels {
			panel := &doc.Scenes[si].Panels[pi]
			if panel.ID != panelID || len(panel.Dialogue) >= maxDialogueLines {
				continue
			}
			panel.Dialogue = append(panel.Dialogue, studio.ScriptDialogue{Kind: "speech"})
			return doc, indexID("speaker-", panel.ID, len(panel.Dialogue)-1)
		}
	}
	return doc, ""
}

// removeDialogueLine drops the line named "<panelID>.<index>".
func removeDialogueLine(doc studio.ScriptDoc, target string) studio.ScriptDoc {
	cut := strings.LastIndex(target, ".")
	if cut < 0 {
		return doc
	}
	index, err := strconv.Atoi(target[cut+1:])
	if err != nil {
		return doc
	}
	for si := range doc.Scenes {
		for pi := range doc.Scenes[si].Panels {
			panel := &doc.Scenes[si].Panels[pi]
			if panel.ID == target[:cut] && index >= 0 && index < len(panel.Dialogue) {
				panel.Dialogue = append(panel.Dialogue[:index:index], panel.Dialogue[index+1:]...)
			}
		}
	}
	return doc
}

// withoutEmptyLines drops dialogue lines without text before saving: a line
// added in the editor is only kept once it says something, so an empty bubble
// is never lettered.
func withoutEmptyLines(doc studio.ScriptDoc) studio.ScriptDoc {
	doc = cloneScript(doc)
	for si := range doc.Scenes {
		for pi := range doc.Scenes[si].Panels {
			panel := &doc.Scenes[si].Panels[pi]
			kept := panel.Dialogue[:0:0]
			for _, line := range panel.Dialogue {
				if strings.TrimSpace(line.Text) != "" {
					kept = append(kept, line)
				}
			}
			panel.Dialogue = kept
		}
	}
	return doc
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
