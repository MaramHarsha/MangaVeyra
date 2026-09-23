package studio

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// ScriptDraft is the output contract of the script.outline job: an arc
// outline, a panel-by-panel script for chapter 1, character drafts and
// setting items. Any handler (the gen module's text model, the built-in
// placeholder) sets it as the job output; the studio applies it to the
// project when the job finishes (ApplyScriptDraft).
type ScriptDraft struct {
	Arcs       []DraftArc       `json:"arcs"`
	Chapter1   DraftScript      `json:"chapter1"`
	Characters []DraftCharacter `json:"characters"`
	Settings   []DraftSetting   `json:"settings"`
}

// DraftArc is one arc of the outline.
type DraftArc struct {
	Title    string         `json:"title"`
	Summary  string         `json:"summary"`
	Chapters []DraftChapter `json:"chapters"`
}

// DraftChapter is one chapter summary of the outline.
type DraftChapter struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// DraftScript is the scripted chapter.
type DraftScript struct {
	Scenes []DraftScene `json:"scenes"`
}

// DraftScene is one scene with its panels.
type DraftScene struct {
	Title     string       `json:"title"`
	Prose     string       `json:"prose"`
	TimeOfDay string       `json:"time_of_day"`
	Location  string       `json:"location"`
	Panels    []DraftPanel `json:"panels"`
}

// DraftPanel is one scripted panel. Label is "page.panel" ("1.3").
type DraftPanel struct {
	Label    string      `json:"label"`
	Camera   string      `json:"camera"`
	Emotion  string      `json:"emotion"`
	Action   string      `json:"action"`
	Dialogue []DraftLine `json:"dialogue"`
	SFX      []string    `json:"sfx"`
}

// DraftLine is one dialogue line; Kind is speech, thought, shout, whisper or caption.
type DraftLine struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	Kind    string `json:"kind"`
}

// DraftCharacter is a character draft.
type DraftCharacter struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	RoleLine    string `json:"role_line"`
	Age         string `json:"age"`
	Look        string `json:"look"`
	Personality string `json:"personality"`
	SpeechStyle string `json:"speech_style"`
}

// DraftSetting is a location, prop or style reference.
type DraftSetting struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Notes string `json:"notes"`
}

// Valid reports whether the draft has something to apply.
func (d *ScriptDraft) Valid() bool {
	return d != nil && (len(d.Arcs) > 0 || len(d.Chapter1.Scenes) > 0 || len(d.Characters) > 0)
}

// categoryForRole maps a draft role to a character category.
func categoryForRole(role string) string {
	switch r := strings.ToLower(role); {
	case strings.Contains(r, "lead"), strings.Contains(r, "protagonist"), strings.Contains(r, "main"), strings.Contains(r, "rival"),
		strings.Contains(r, "antagonist"), strings.Contains(r, "deuteragonist"):
		return CategoryMain
	case strings.Contains(r, "extra"), strings.Contains(r, "background"), strings.Contains(r, "crowd"):
		return CategoryExtra
	}
	return CategorySupporting
}

// parseLabel splits "7.3" into page 7 (0 when missing).
func parseLabel(label string) int {
	p, _, _ := strings.Cut(strings.TrimSpace(label), ".")
	n, err := strconv.Atoi(p)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ApplyScriptDraft merges a ScriptDraft into a project without overwriting
// human work: characters and setting items are matched by name (new ones are
// created, empty fields of existing ones filled), arcs by title, chapters by
// number (titles and summaries updated), and chapter 1's scenes and panels
// are only written when chapter 1 has no script yet. Applying the same draft
// twice is harmless.
func (s *Service) ApplyScriptDraft(ctx context.Context, projectID string, d ScriptDraft) error {
	pr, err := s.Project(ctx, projectID)
	if err != nil {
		return err
	}
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		// Characters.
		var existing []Character
		if err := tx.Select(ctx, &existing, `SELECT `+characterCols+` FROM characters WHERE project_id = ?`, projectID); err != nil {
			return err
		}
		chars := map[string]string{} // lower name / first name → id
		addName := func(name, id string) {
			l := strings.ToLower(strings.TrimSpace(name))
			chars[l] = id
			if f := strings.Fields(l); len(f) > 1 {
				if _, ok := chars[f[0]]; !ok {
					chars[f[0]] = id
				}
			}
		}
		for _, c := range existing {
			addName(c.Name, c.ID)
		}
		for _, dc := range d.Characters {
			name := clip(dc.Name, 80)
			if name == "" {
				continue
			}
			if id, ok := chars[strings.ToLower(name)]; ok {
				if _, err := tx.Exec(ctx, `UPDATE characters SET role = CASE WHEN role = '' THEN ? ELSE role END,
					role_line = CASE WHEN role_line = '' THEN ? ELSE role_line END, age = CASE WHEN age = '' THEN ? ELSE age END,
					look = CASE WHEN look = '' THEN ? ELSE look END, personality = CASE WHEN personality = '' THEN ? ELSE personality END,
					speech_style = CASE WHEN speech_style = '' THEN ? ELSE speech_style END WHERE id = ?`,
					clip(dc.Role, 40), clip(dc.RoleLine, 80), clip(dc.Age, 20), clip(dc.Look, 1500), clip(dc.Personality, 1500),
					clip(dc.SpeechStyle, 400), id); err != nil {
					return err
				}
				continue
			}
			c, err := s.createCharacter(ctx, tx, projectID, CharacterInput{Name: name, Role: clip(dc.Role, 40), RoleLine: clip(dc.RoleLine, 80),
				Age: clip(dc.Age, 20), Look: clip(dc.Look, 1500), Personality: clip(dc.Personality, 1500), SpeechStyle: clip(dc.SpeechStyle, 400),
				Category: categoryForRole(dc.Role), Style: pr.Style})
			if err != nil {
				return err
			}
			addName(c.Name, c.ID)
		}
		// Setting items.
		var items []SettingItem
		if err := tx.Select(ctx, &items, `SELECT `+settingCols+` FROM setting_items WHERE project_id = ?`, projectID); err != nil {
			return err
		}
		places := map[string]string{} // kind|lower name → id
		for _, it := range items {
			places[it.Kind+"|"+strings.ToLower(it.Name)] = it.ID
		}
		ensureSetting := func(kind, name, notes string) (string, error) {
			name = clip(name, 120)
			if name == "" {
				return "", nil
			}
			switch strings.ToLower(kind) {
			case "prop", "object", "item":
				kind = SettingProp
			case "style", "style_ref", "look":
				kind = SettingStyle
			default:
				kind = SettingLocation
			}
			key := kind + "|" + strings.ToLower(name)
			if id, ok := places[key]; ok {
				return id, nil
			}
			it, err := s.createSettingItem(ctx, tx, projectID, SettingInput{Kind: kind, Name: name, Description: clip(notes, 2000)})
			if err != nil {
				return "", err
			}
			places[key] = it.ID
			return it.ID, nil
		}
		for _, ds := range d.Settings {
			if _, err := ensureSetting(ds.Kind, ds.Name, ds.Notes); err != nil {
				return err
			}
		}
		// Arcs and chapters.
		var arcs []Arc
		if err := tx.Select(ctx, &arcs, `SELECT `+arcCols+` FROM arcs WHERE project_id = ?`, projectID); err != nil {
			return err
		}
		arcByTitle := map[string]string{}
		for _, a := range arcs {
			arcByTitle[strings.ToLower(a.Title)] = a.ID
		}
		chapterCount := 0
		for _, da := range d.Arcs {
			title := clip(da.Title, 120)
			if title == "" {
				continue
			}
			arcID, ok := arcByTitle[strings.ToLower(title)]
			if !ok {
				a, err := s.createArc(ctx, tx, projectID, title, da.Summary)
				if err != nil {
					return err
				}
				arcID = a.ID
				arcByTitle[strings.ToLower(title)] = arcID
			}
			for _, dc := range da.Chapters {
				if dc.Number < 1 || dc.Number > 999 {
					continue
				}
				chapterCount = max(chapterCount, dc.Number)
				var id string
				err := tx.Get(ctx, &id, `SELECT id FROM chapters WHERE project_id = ? AND number = ?`, projectID, dc.Number)
				if err == nil {
					if _, err := tx.Exec(ctx, `UPDATE chapters SET title = CASE WHEN title = '' THEN ? ELSE title END,
						summary = CASE WHEN summary = '' THEN ? ELSE summary END, arc_id = COALESCE(arc_id, ?), updated_at = ? WHERE id = ?`,
						clip(dc.Title, 160), clip(dc.Summary, 4000), arcID, s.now(), id); err != nil {
						return err
					}
					continue
				}
				if _, err := s.createChapter(ctx, tx, projectID, arcID, dc.Number, dc.Title, dc.Summary); err != nil {
					return err
				}
			}
		}
		// Chapter 1 script.
		if len(d.Chapter1.Scenes) > 0 {
			var ch Chapter
			err := tx.Get(ctx, &ch, `SELECT `+chapterCols+` FROM chapters WHERE project_id = ? AND number = 1`, projectID)
			if err != nil {
				c, err := s.createChapter(ctx, tx, projectID, "", 1, "", "")
				if err != nil {
					return err
				}
				ch = *c
			}
			var scenes int
			if err := tx.Get(ctx, &scenes, `SELECT COUNT(*) FROM scenes WHERE chapter_id = ?`, ch.ID); err != nil {
				return err
			}
			if scenes == 0 {
				if err := s.writeDraftScript(ctx, tx, &ch, d.Chapter1, chars, ensureSetting); err != nil {
					return err
				}
				label := ch.AILabel.V
				label.Script = LabelAI
				if _, err := tx.Exec(ctx, `UPDATE chapters SET status = ?, ai_label = ? WHERE id = ?`, ChapterScripting, db.MustJSON(label), ch.ID); err != nil {
					return err
				}
			}
			chapterCount = max(chapterCount, 1)
		}
		if chapterCount > pr.ChaptersPlanned {
			if _, err := tx.Exec(ctx, `UPDATE projects SET chapters_planned = ? WHERE id = ?`, chapterCount, projectID); err != nil {
				return err
			}
		}
		s.touchProject(ctx, tx, projectID)
		return nil
	})
	if err != nil {
		return err
	}
	s.Publish(ctx, projectID, "studio.story.updated", Event{Kind: "outline", ID: projectID})
	return nil
}

func (s *Service) writeDraftScript(ctx context.Context, tx *db.Tx, ch *Chapter, ds DraftScript, chars map[string]string,
	ensureSetting func(kind, name, notes string) (string, error)) error {
	page, onPage := 1, 0
	for _, dsc := range ds.Scenes {
		locID, err := ensureSetting(SettingLocation, dsc.Location, "")
		if err != nil {
			return err
		}
		title := clip(dsc.Title, 160)
		if title == "" {
			title = i18n.T(ctx, "studio.story.untitled_scene")
		}
		sc, err := s.createScene(ctx, tx, ch, title, "", dsc.TimeOfDay, locID)
		if err != nil {
			return err
		}
		if dsc.Prose != "" {
			if _, err := tx.Exec(ctx, `UPDATE scenes SET prose = ? WHERE id = ?`, clip(dsc.Prose, 60000), sc.ID); err != nil {
				return err
			}
		}
		prev := ""
		for _, dp := range dsc.Panels {
			pn := parseLabel(dp.Label)
			if pn == 0 {
				if onPage >= 5 {
					page, onPage = page+1, 0
				}
				pn = page
			}
			if pn != page {
				page, onPage = pn, 0
			}
			onPage++
			in := ScriptInput{PageNumber: pn, Camera: dp.Camera, Emotion: dp.Emotion, Action: dp.Action, SFX: dp.SFX}
			var ids []string
			for _, l := range dp.Dialogue {
				line := DialogueLine{Speaker: l.Speaker, Text: l.Text, Kind: l.Kind}
				if id, ok := chars[strings.ToLower(strings.TrimSpace(l.Speaker))]; ok {
					line.CharacterID = id
					if !slices.Contains(ids, id) {
						ids = append(ids, id)
					}
				}
				in.Dialogue = append(in.Dialogue, line)
			}
			lowerAction := strings.ToLower(dp.Action)
			for name, id := range chars {
				if len(name) > 2 && strings.Contains(lowerAction, name) && !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
			in.CharacterIDs = sortStrings(ids)
			if locID != "" {
				in.SettingIDs = []string{locID}
			}
			in.clean()
			sp, err := s.insertScriptPanel(ctx, tx, sc, prev, in)
			if err != nil {
				return err
			}
			prev = sp.ID
		}
	}
	return nil
}

// ---- script assistant diffs -------------------------------------------------------------

// Script diff operations.
const (
	OpAdd    = "add"
	OpUpdate = "update"
	OpRemove = "remove"
	OpMove   = "move"
)

// ScriptDiff is the output contract of the script.assist job: a list of
// operations on the chapter's script panels plus an "affects" summary
// ("Changes 2 panels · pages 8–34 unaffected"). ApplyScriptDiff applies it.
type ScriptDiff struct {
	// Summary is the assistant's proposal in one or two sentences.
	Summary string     `json:"summary"`
	Affects string     `json:"affects"`
	Ops     []ScriptOp `json:"ops"`
}

// ScriptOp is one change. PanelID names the target of update, remove and
// move; add and move insert after AfterID (in SceneID, "" = the scene of
// AfterID); PageNumber moves or places the panel on a page (0 = keep / same
// page as AfterID).
type ScriptOp struct {
	Op         string           `json:"op"`
	PanelID    string           `json:"panel_id,omitempty"`
	SceneID    string           `json:"scene_id,omitempty"`
	AfterID    string           `json:"after_id,omitempty"`
	PageNumber int              `json:"page_number,omitempty"`
	Panel      *ScriptPanelData `json:"panel,omitempty"`
}

// ScriptPanelData is the content of a panel in a diff.
type ScriptPanelData struct {
	Camera       string         `json:"camera"`
	Shot         string         `json:"shot,omitempty"`
	Emotion      string         `json:"emotion"`
	Action       string         `json:"action"`
	Dialogue     []DialogueLine `json:"dialogue"`
	SFX          []string       `json:"sfx"`
	CharacterIDs []string       `json:"character_ids,omitempty"`
}

func (d *ScriptPanelData) input(page int) ScriptInput {
	return ScriptInput{PageNumber: page, Camera: d.Camera, Shot: d.Shot, Emotion: d.Emotion, Action: d.Action, Dialogue: d.Dialogue,
		SFX: d.SFX, CharacterIDs: d.CharacterIDs}
}

// ChangedPanels returns how many existing or new panels the diff touches.
func (d *ScriptDiff) ChangedPanels() int { return len(d.Ops) }

// ApplyScriptDiff applies a script assistant proposal to a chapter in one
// transaction. Every referenced panel must belong to the chapter; a locked
// chapter cannot change.
func (s *Service) ApplyScriptDiff(ctx context.Context, a *Access, chapterID string, d ScriptDiff) error {
	if err := s.require(ctx, a, EditScript); err != nil {
		return err
	}
	ch, err := s.ChapterByID(ctx, chapterID)
	if err != nil || ch.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	if ch.Locked {
		return lockedErr(ctx, i18n.T(ctx, "studio.lock.chapter", "n", ch.Number))
	}
	bad := func() error { return core.ErrValidation(map[string]string{"ops": i18n.T(ctx, "studio.assist.stale")}) }
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		get := func(id string) (*ScriptPanel, error) {
			sp, err := s.scriptPanelTx(ctx, tx, id)
			if err != nil || sp.ChapterID != chapterID {
				return nil, bad()
			}
			return sp, nil
		}
		sceneOf := func(sceneID string) (*Scene, error) {
			var sc Scene
			if err := tx.Get(ctx, &sc, `SELECT `+sceneCols+` FROM scenes WHERE id = ? AND chapter_id = ?`, sceneID, chapterID); err != nil {
				return nil, bad()
			}
			return &sc, nil
		}
		for _, op := range d.Ops {
			switch op.Op {
			case OpUpdate:
				sp, err := get(op.PanelID)
				if err != nil || op.Panel == nil {
					return bad()
				}
				in := op.Panel.input(op.PageNumber)
				in.clean()
				if in.PageNumber == 0 {
					in.PageNumber = sp.PageNumber
				}
				if _, err := tx.Exec(ctx, `UPDATE script_panels SET page_number = ?, camera = ?, shot = ?, emotion = ?, action = ?, dialogue = ?, sfx = ?,
					character_ids = CASE WHEN ? = '[]' THEN character_ids ELSE ? END, updated_at = ? WHERE id = ?`, in.PageNumber, in.Camera, in.Shot,
					in.Emotion, in.Action, db.MustJSON(dialogueList(in.Dialogue)), db.MustJSON(jsonList(in.SFX)), db.MustJSON(jsonList(in.CharacterIDs)),
					db.MustJSON(jsonList(in.CharacterIDs)), s.now(), sp.ID); err != nil {
					return err
				}
			case OpRemove:
				if _, err := get(op.PanelID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM script_panels WHERE id = ?`, op.PanelID); err != nil {
					return err
				}
			case OpAdd, OpMove:
				sceneID := op.SceneID
				page := op.PageNumber
				if op.AfterID != "" {
					after, err := get(op.AfterID)
					if err != nil {
						return err
					}
					if sceneID == "" {
						sceneID = after.SceneID
					}
					if page == 0 {
						page = after.PageNumber
					}
				}
				if sceneID == "" {
					return bad()
				}
				sc, err := sceneOf(sceneID)
				if err != nil {
					return err
				}
				if op.Op == OpMove {
					sp, err := get(op.PanelID)
					if err != nil {
						return err
					}
					if page == 0 {
						page = sp.PageNumber
					}
					if _, err := tx.Exec(ctx, `UPDATE script_panels SET scene_id = ?, page_number = ?, updated_at = ? WHERE id = ?`,
						sc.ID, page, s.now(), sp.ID); err != nil {
						return err
					}
					if err := placeAfter(ctx, tx, sc.ID, sp.ID, op.AfterID); err != nil {
						return err
					}
					continue
				}
				if op.Panel == nil {
					return bad()
				}
				in := op.Panel.input(0)
				if page > 0 {
					in.PageNumber = page
				}
				in.clean()
				if _, err := s.insertScriptPanel(ctx, tx, sc, op.AfterID, in); err != nil {
					return err
				}
			default:
				return bad()
			}
		}
		return s.relabel(ctx, tx, chapterID)
	})
	if err != nil {
		return err
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.script.updated", Event{Kind: "script_diff", ChapterID: chapterID, Summary: d.Summary})
	return nil
}

// placeAfter puts panel id right after afterID ("" = first) in a scene's order.
func placeAfter(ctx context.Context, tx *db.Tx, sceneID, id, afterID string) error {
	var ids []string
	if err := tx.Select(ctx, &ids, `SELECT id FROM script_panels WHERE scene_id = ? AND id <> ? ORDER BY page_number, order_idx, created_at`,
		sceneID, id); err != nil {
		return err
	}
	pos := 0
	if i := slices.Index(ids, afterID); i >= 0 {
		pos = i + 1
	}
	ids = slices.Insert(ids, pos, id)
	for k, v := range ids {
		if _, err := tx.Exec(ctx, `UPDATE script_panels SET order_idx = ? WHERE id = ?`, k, v); err != nil {
			return err
		}
	}
	return nil
}
