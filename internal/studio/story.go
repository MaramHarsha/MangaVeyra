package studio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrNotFound()
	}
	return err
}

func (s *Service) require(ctx context.Context, a *Access, what string) error {
	if !a.Can(what) {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	return nil
}

// ---- arcs -------------------------------------------------------------------------

// Arcs lists a project's arcs in order.
func (s *Service) Arcs(ctx context.Context, projectID string) ([]Arc, error) {
	var out []Arc
	err := s.db.Select(ctx, &out, `SELECT `+arcCols+` FROM arcs WHERE project_id = ? ORDER BY order_idx, created_at`, projectID)
	return out, err
}

// Arc returns one arc of a project.
func (s *Service) Arc(ctx context.Context, projectID, id string) (*Arc, error) {
	var a Arc
	if err := s.db.Get(ctx, &a, `SELECT `+arcCols+` FROM arcs WHERE id = ? AND project_id = ?`, id, projectID); err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

// CreateArc appends an arc.
func (s *Service) CreateArc(ctx context.Context, a *Access, title, summary string) (*Arc, error) {
	if err := s.require(ctx, a, EditStory); err != nil {
		return nil, err
	}
	title = clip(title, 120)
	if title == "" {
		return nil, core.ErrValidation(map[string]string{"title": i18n.T(ctx, "studio.form.required")})
	}
	return s.createArc(ctx, s.db, a.Project.ID, title, summary)
}

func (s *Service) createArc(ctx context.Context, q db.Querier, projectID, title, summary string) (*Arc, error) {
	var next int
	if err := q.Get(ctx, &next, `SELECT COALESCE(MAX(order_idx), -1) + 1 FROM arcs WHERE project_id = ?`, projectID); err != nil {
		return nil, err
	}
	now := s.now()
	arc := &Arc{ID: db.NewID(), ProjectID: projectID, Title: title, Summary: clip(summary, 2000), OrderIdx: next, CreatedAt: now, UpdatedAt: now}
	if _, err := q.Exec(ctx, `INSERT INTO arcs (id, project_id, title, summary, order_idx, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		arc.ID, projectID, arc.Title, arc.Summary, arc.OrderIdx, now, now); err != nil {
		return nil, err
	}
	s.touchProject(ctx, q, projectID)
	s.Publish(ctx, projectID, "studio.story.updated", Event{Kind: "arc", ID: arc.ID})
	return arc, nil
}

// UpdateArc renames an arc and sets its summary.
func (s *Service) UpdateArc(ctx context.Context, a *Access, id, title, summary string) error {
	if err := s.require(ctx, a, EditStory); err != nil {
		return err
	}
	if title = clip(title, 120); title == "" {
		return core.ErrValidation(map[string]string{"title": i18n.T(ctx, "studio.form.required")})
	}
	res, err := s.db.Exec(ctx, `UPDATE arcs SET title = ?, summary = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		title, clip(summary, 2000), s.now(), id, a.Project.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound()
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.story.updated", Event{Kind: "arc", ID: id})
	return nil
}

// DeleteArc removes an arc; its chapters stay (unassigned).
func (s *Service) DeleteArc(ctx context.Context, a *Access, id string) error {
	if err := s.require(ctx, a, EditStory); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM arcs WHERE id = ? AND project_id = ?`, id, a.Project.ID); err != nil {
		return err
	}
	s.Publish(ctx, a.Project.ID, "studio.story.updated", Event{Kind: "arc", ID: id})
	return nil
}

// MoveArc moves an arc up (-1) or down (+1).
func (s *Service) MoveArc(ctx context.Context, a *Access, id string, delta int) error {
	if err := s.require(ctx, a, EditStory); err != nil {
		return err
	}
	return s.reorder(ctx, "arcs", "project_id = ?", []any{a.Project.ID}, id, delta, a.Project.ID)
}

// reorder swaps an item with its neighbour in order_idx order and renumbers the list.
func (s *Service) reorder(ctx context.Context, table, cond string, args []any, id string, delta int, projectID string) error {
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		var ids []string
		if err := tx.Select(ctx, &ids, `SELECT id FROM `+table+` WHERE `+cond+` ORDER BY order_idx, created_at, id`, args...); err != nil {
			return err
		}
		i := slices.Index(ids, id)
		if i < 0 {
			return core.ErrNotFound()
		}
		j := i + delta
		if j < 0 || j >= len(ids) {
			return nil
		}
		ids[i], ids[j] = ids[j], ids[i]
		for k, v := range ids {
			if _, err := tx.Exec(ctx, `UPDATE `+table+` SET order_idx = ? WHERE id = ?`, k, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.touchProject(ctx, s.db, projectID)
		s.Publish(ctx, projectID, "studio.story.updated", Event{Kind: strings.TrimSuffix(table, "s"), ID: id})
	}
	return err
}

// ---- chapters ---------------------------------------------------------------------

// Chapters lists a project's chapters by number.
func (s *Service) Chapters(ctx context.Context, projectID string) ([]Chapter, error) {
	var out []Chapter
	err := s.db.Select(ctx, &out, `SELECT `+chapterCols+` FROM chapters WHERE project_id = ? ORDER BY number`, projectID)
	return out, err
}

// Chapter returns chapter number n of a project.
func (s *Service) Chapter(ctx context.Context, projectID string, n int) (*Chapter, error) {
	var c Chapter
	if err := s.db.Get(ctx, &c, `SELECT `+chapterCols+` FROM chapters WHERE project_id = ? AND number = ?`, projectID, n); err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

// ChapterByID returns a chapter by id.
func (s *Service) ChapterByID(ctx context.Context, id string) (*Chapter, error) {
	var c Chapter
	if err := s.db.Get(ctx, &c, `SELECT `+chapterCols+` FROM chapters WHERE id = ?`, id); err != nil {
		return nil, notFound(err)
	}
	return &c, nil
}

// CreateChapter adds a chapter (number = last + 1) to an arc (arcID may be "").
func (s *Service) CreateChapter(ctx context.Context, a *Access, arcID, title, summary string) (*Chapter, error) {
	if err := s.require(ctx, a, EditStory); err != nil {
		return nil, err
	}
	if arcID != "" {
		if _, err := s.Arc(ctx, a.Project.ID, arcID); err != nil {
			return nil, err
		}
	}
	var c *Chapter
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		var next int
		if err := tx.Get(ctx, &next, `SELECT COALESCE(MAX(number), 0) + 1 FROM chapters WHERE project_id = ?`, a.Project.ID); err != nil {
			return err
		}
		var err error
		c, err = s.createChapter(ctx, tx, a.Project.ID, arcID, next, title, summary)
		return err
	})
	return c, err
}

func (s *Service) createChapter(ctx context.Context, q db.Querier, projectID, arcID string, number int, title, summary string) (*Chapter, error) {
	now := s.now()
	title = clip(title, 160)
	c := &Chapter{ID: db.NewID(), ProjectID: projectID, ArcID: arcID, Number: number, Title: title, Summary: clip(summary, 4000),
		Status: ChapterDraft, CreatedAt: now, UpdatedAt: now}
	c.AILabel.V = AILabel{Script: LabelHuman, Art: LabelHuman, Lettering: LabelHuman}
	c.Settings.V = map[string]any{}
	if _, err := q.Exec(ctx, `INSERT INTO chapters (id, project_id, arc_id, number, title, summary, status, ai_label, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'draft', ?, ?, ?)`, c.ID, projectID, db.NullString(arcID), number, c.Title, c.Summary,
		db.MustJSON(c.AILabel.V), now, now); err != nil {
		return nil, err
	}
	s.touchProject(ctx, q, projectID)
	s.Publish(ctx, projectID, "studio.story.updated", Event{Kind: "chapter", ID: c.ID, ChapterID: c.ID})
	return c, nil
}

// ChapterPatch updates chapter fields (nil = unchanged).
type ChapterPatch struct {
	Title   *string
	Summary *string
	ArcID   *string
	Status  *string
	Locked  *bool
	AILabel *AILabel
}

var chapterStatuses = []string{ChapterDraft, ChapterScripting, ChapterDrawing, ChapterLettering, ChapterReview, ChapterReady, ChapterPublished}

// UpdateChapter changes a chapter. Unlocking needs a human.
func (s *Service) UpdateChapter(ctx context.Context, a *Access, id string, pt ChapterPatch) (*Chapter, error) {
	if err := s.require(ctx, a, EditStory); err != nil && pt.Locked == nil {
		return nil, err
	}
	c, err := s.ChapterByID(ctx, id)
	if err != nil || c.ProjectID != a.Project.ID {
		return nil, core.ErrNotFound()
	}
	if pt.Title != nil {
		c.Title = clip(*pt.Title, 160)
	}
	if pt.Summary != nil {
		c.Summary = clip(*pt.Summary, 4000)
	}
	if pt.ArcID != nil {
		if *pt.ArcID != "" {
			if _, err := s.Arc(ctx, a.Project.ID, *pt.ArcID); err != nil {
				return nil, err
			}
		}
		c.ArcID = *pt.ArcID
	}
	if pt.Status != nil {
		if !slices.Contains(chapterStatuses, *pt.Status) {
			return nil, core.ErrValidation(map[string]string{"status": i18n.T(ctx, "studio.form.invalid")})
		}
		c.Status = *pt.Status
	}
	if pt.Locked != nil {
		if !a.Can(EditPages) && !a.Can(EditStory) {
			return nil, core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
		}
		if c.Locked && !*pt.Locked && a.Actor.IsAgent() {
			return nil, core.ErrForbidden(i18n.T(ctx, "studio.error.unlock_human"))
		}
		c.Locked = *pt.Locked
	}
	if pt.AILabel != nil {
		c.AILabel.V = *pt.AILabel
	}
	_, err = s.db.Exec(ctx, `UPDATE chapters SET title = ?, summary = ?, arc_id = ?, status = ?, locked = ?, ai_label = ?, updated_at = ?
		WHERE id = ?`, c.Title, c.Summary, db.NullString(c.ArcID), c.Status, c.Locked, db.MustJSON(c.AILabel.V), s.now(), c.ID)
	if err != nil {
		return nil, err
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.story.updated", Event{Kind: "chapter", ID: c.ID, ChapterID: c.ID})
	return s.ChapterByID(ctx, c.ID)
}

// DeleteChapter deletes a chapter with its scenes, script and pages, then renumbers the chapters after it.
func (s *Service) DeleteChapter(ctx context.Context, a *Access, id string) error {
	if err := s.require(ctx, a, EditStory); err != nil {
		return err
	}
	c, err := s.ChapterByID(ctx, id)
	if err != nil || c.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	if c.Locked {
		return lockedErr(ctx, i18n.T(ctx, "studio.lock.chapter", "n", c.Number))
	}
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM chapters WHERE id = ?`, id); err != nil {
			return err
		}
		return s.renumberChapters(ctx, tx, a.Project.ID)
	})
	if err == nil {
		s.touchProject(ctx, s.db, a.Project.ID)
		s.Publish(ctx, a.Project.ID, "studio.story.updated", Event{Kind: "chapter", ID: id})
	}
	return err
}

// renumberChapters makes chapter numbers 1..n in their current order (two passes to respect the unique index).
func (s *Service) renumberChapters(ctx context.Context, tx *db.Tx, projectID string) error {
	var ids []string
	if err := tx.Select(ctx, &ids, `SELECT id FROM chapters WHERE project_id = ? ORDER BY number`, projectID); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE chapters SET number = ? WHERE id = ?`, -(i + 1), id); err != nil {
			return err
		}
	}
	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE chapters SET number = ? WHERE id = ?`, i+1, id); err != nil {
			return err
		}
	}
	return nil
}

// MoveChapter swaps a chapter with its neighbour (-1 earlier, +1 later), renumbering both.
func (s *Service) MoveChapter(ctx context.Context, a *Access, id string, delta int) error {
	if err := s.require(ctx, a, EditStory); err != nil {
		return err
	}
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		var ids []string
		if err := tx.Select(ctx, &ids, `SELECT id FROM chapters WHERE project_id = ? ORDER BY number`, a.Project.ID); err != nil {
			return err
		}
		i := slices.Index(ids, id)
		if i < 0 {
			return core.ErrNotFound()
		}
		j := i + delta
		if j < 0 || j >= len(ids) {
			return nil
		}
		ids[i], ids[j] = ids[j], ids[i]
		for k, v := range ids {
			if _, err := tx.Exec(ctx, `UPDATE chapters SET number = ? WHERE id = ?`, -(k + 1), v); err != nil {
				return err
			}
		}
		for k, v := range ids {
			if _, err := tx.Exec(ctx, `UPDATE chapters SET number = ? WHERE id = ?`, k+1, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.touchProject(ctx, s.db, a.Project.ID)
		s.Publish(ctx, a.Project.ID, "studio.story.updated", Event{Kind: "chapter", ID: id})
	}
	return err
}

// OpenContinuityIssues counts open continuity issues of a chapter (the gen module fills the table).
func (s *Service) OpenContinuityIssues(ctx context.Context, chapterID string) int {
	var n int
	_ = s.db.Get(ctx, &n, `SELECT COUNT(*) FROM continuity_issues WHERE chapter_id = ? AND status = 'open'`, chapterID)
	return n
}

// ---- scenes -------------------------------------------------------------------------

// Scenes lists a chapter's scenes in order.
func (s *Service) Scenes(ctx context.Context, chapterID string) ([]Scene, error) {
	var out []Scene
	err := s.db.Select(ctx, &out, `SELECT `+sceneCols+` FROM scenes WHERE chapter_id = ? ORDER BY order_idx, created_at`, chapterID)
	return out, err
}

// ProjectScenes lists every scene of a project (chapter, then order).
func (s *Service) ProjectScenes(ctx context.Context, projectID string) ([]Scene, error) {
	var out []Scene
	err := s.db.Select(ctx, &out, `SELECT `+sceneCols+` FROM scenes WHERE project_id = ? ORDER BY chapter_id, order_idx`, projectID)
	return out, err
}

// Scene returns a scene by id.
func (s *Service) Scene(ctx context.Context, id string) (*Scene, error) {
	var sc Scene
	if err := s.db.Get(ctx, &sc, `SELECT `+sceneCols+` FROM scenes WHERE id = ?`, id); err != nil {
		return nil, notFound(err)
	}
	return &sc, nil
}

// CreateScene appends a scene to a chapter.
func (s *Service) CreateScene(ctx context.Context, a *Access, chapterID, title string) (*Scene, error) {
	if err := s.require(ctx, a, EditStory); err != nil {
		return nil, err
	}
	c, err := s.ChapterByID(ctx, chapterID)
	if err != nil || c.ProjectID != a.Project.ID {
		return nil, core.ErrNotFound()
	}
	if title = clip(title, 160); title == "" {
		return nil, core.ErrValidation(map[string]string{"title": i18n.T(ctx, "studio.form.required")})
	}
	return s.createScene(ctx, s.db, c, title, "", "", "")
}

func (s *Service) createScene(ctx context.Context, q db.Querier, c *Chapter, title, summary, timeOfDay, locationID string) (*Scene, error) {
	var next int
	if err := q.Get(ctx, &next, `SELECT COALESCE(MAX(order_idx), -1) + 1 FROM scenes WHERE chapter_id = ?`, c.ID); err != nil {
		return nil, err
	}
	now := s.now()
	sc := &Scene{ID: db.NewID(), ProjectID: c.ProjectID, ChapterID: c.ID, OrderIdx: next, Title: title, Summary: clip(summary, 4000),
		TimeOfDay: clip(timeOfDay, 40), LocationID: locationID, CreatedAt: now, UpdatedAt: now}
	if _, err := q.Exec(ctx, `INSERT INTO scenes (id, project_id, chapter_id, order_idx, title, summary, location_id, time_of_day, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, sc.ID, sc.ProjectID, sc.ChapterID, sc.OrderIdx, sc.Title, sc.Summary,
		db.NullString(locationID), sc.TimeOfDay, now, now); err != nil {
		return nil, err
	}
	s.touchProject(ctx, q, c.ProjectID)
	s.Publish(ctx, c.ProjectID, "studio.story.updated", Event{Kind: "scene", ID: sc.ID, ChapterID: c.ID})
	return sc, nil
}

// ScenePatch updates scene fields (nil = unchanged).
type ScenePatch struct {
	Title      *string
	Summary    *string
	Prose      *string
	TimeOfDay  *string
	LocationID *string
}

// UpdateScene changes a scene.
func (s *Service) UpdateScene(ctx context.Context, a *Access, id string, pt ScenePatch) (*Scene, error) {
	if err := s.require(ctx, a, EditStory); err != nil {
		return nil, err
	}
	sc, err := s.Scene(ctx, id)
	if err != nil || sc.ProjectID != a.Project.ID {
		return nil, core.ErrNotFound()
	}
	if pt.Title != nil {
		if t := clip(*pt.Title, 160); t != "" {
			sc.Title = t
		}
	}
	if pt.Summary != nil {
		sc.Summary = clip(*pt.Summary, 4000)
	}
	if pt.Prose != nil {
		sc.Prose = clip(*pt.Prose, 60000)
	}
	if pt.TimeOfDay != nil {
		sc.TimeOfDay = clip(*pt.TimeOfDay, 40)
	}
	if pt.LocationID != nil {
		if *pt.LocationID != "" {
			it, err := s.SettingItem(ctx, a.Project.ID, *pt.LocationID)
			if err != nil || it.Kind != SettingLocation {
				return nil, core.ErrValidation(map[string]string{"location_id": i18n.T(ctx, "studio.form.invalid")})
			}
		}
		sc.LocationID = *pt.LocationID
	}
	_, err = s.db.Exec(ctx, `UPDATE scenes SET title = ?, summary = ?, prose = ?, time_of_day = ?, location_id = ?, updated_at = ? WHERE id = ?`,
		sc.Title, sc.Summary, sc.Prose, sc.TimeOfDay, db.NullString(sc.LocationID), s.now(), sc.ID)
	if err != nil {
		return nil, err
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.story.updated", Event{Kind: "scene", ID: sc.ID, ChapterID: sc.ChapterID})
	return sc, nil
}

// DeleteScene deletes a scene and its script panels.
func (s *Service) DeleteScene(ctx context.Context, a *Access, id string) error {
	if err := s.require(ctx, a, EditStory); err != nil {
		return err
	}
	sc, err := s.Scene(ctx, id)
	if err != nil || sc.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM scenes WHERE id = ?`, id); err != nil {
		return err
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.story.updated", Event{Kind: "scene", ID: id, ChapterID: sc.ChapterID})
	return nil
}

// MoveScene moves a scene up (-1) or down (+1) within its chapter.
func (s *Service) MoveScene(ctx context.Context, a *Access, id string, delta int) error {
	if err := s.require(ctx, a, EditStory); err != nil {
		return err
	}
	sc, err := s.Scene(ctx, id)
	if err != nil || sc.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	return s.reorder(ctx, "scenes", "chapter_id = ?", []any{sc.ChapterID}, id, delta, a.Project.ID)
}

// ---- script panels -------------------------------------------------------------------

// ScriptPanels lists a chapter's script panels (page, then order).
func (s *Service) ScriptPanels(ctx context.Context, chapterID string) ([]ScriptPanel, error) {
	var out []ScriptPanel
	err := s.db.Select(ctx, &out, `SELECT `+scriptPanelCols+` FROM script_panels WHERE chapter_id = ?
		ORDER BY page_number, order_idx, created_at`, chapterID)
	return out, err
}

// SceneScript lists a scene's script panels in order.
func (s *Service) SceneScript(ctx context.Context, sceneID string) ([]ScriptPanel, error) {
	var out []ScriptPanel
	err := s.db.Select(ctx, &out, `SELECT `+scriptPanelCols+` FROM script_panels WHERE scene_id = ?
		ORDER BY page_number, order_idx, created_at`, sceneID)
	return out, err
}

// ScriptPanel returns one script panel.
func (s *Service) ScriptPanel(ctx context.Context, id string) (*ScriptPanel, error) {
	var sp ScriptPanel
	if err := s.db.Get(ctx, &sp, `SELECT `+scriptPanelCols+` FROM script_panels WHERE id = ?`, id); err != nil {
		return nil, notFound(err)
	}
	return &sp, nil
}

// ScriptInput is the editable content of a script panel.
type ScriptInput struct {
	PageNumber   int
	Label        string
	Camera       string
	Shot         string
	Emotion      string
	Action       string
	Dialogue     []DialogueLine
	SFX          []string
	CharacterIDs []string
	SettingIDs   []string
	Notes        string
}

func (in *ScriptInput) clean() {
	in.Label = clip(in.Label, 16)
	in.Camera = clip(strings.ToLower(in.Camera), 40)
	in.Shot = clip(in.Shot, 60)
	in.Emotion = clip(in.Emotion, 80)
	in.Action = clip(in.Action, 2000)
	in.Notes = clip(in.Notes, 2000)
	var lines []DialogueLine
	for _, l := range in.Dialogue {
		l.Text = clip(l.Text, 600)
		l.Speaker = clip(l.Speaker, 80)
		if l.Text == "" {
			continue
		}
		if !slices.Contains(LineKinds, l.Kind) {
			l.Kind = LineSpeech
		}
		if l.Speaker == "" && l.Kind != LineCaption {
			l.Kind = LineCaption
		}
		lines = append(lines, l)
	}
	in.Dialogue = lines
	var sfx []string
	for _, x := range in.SFX {
		if x = clip(x, 40); x != "" {
			sfx = append(sfx, x)
		}
	}
	in.SFX = sfx
	if in.PageNumber < 0 {
		in.PageNumber = 0
	}
}

func dialogueList(v []DialogueLine) []DialogueLine {
	if v == nil {
		return []DialogueLine{}
	}
	return v
}

// CreateScriptPanel adds a script panel to a scene (after afterID, or at the end).
func (s *Service) CreateScriptPanel(ctx context.Context, a *Access, sceneID, afterID string, in ScriptInput) (*ScriptPanel, error) {
	if err := s.require(ctx, a, EditScript); err != nil {
		return nil, err
	}
	sc, err := s.Scene(ctx, sceneID)
	if err != nil || sc.ProjectID != a.Project.ID {
		return nil, core.ErrNotFound()
	}
	in.clean()
	var sp *ScriptPanel
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		var err error
		sp, err = s.insertScriptPanel(ctx, tx, sc, afterID, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.script.updated", Event{Kind: "script_panel", ID: sp.ID, ChapterID: sc.ChapterID})
	return sp, nil
}

// insertScriptPanel inserts a panel into a scene, shifting later panels. When
// the page number is 0 it continues the previous panel's page.
func (s *Service) insertScriptPanel(ctx context.Context, tx *db.Tx, sc *Scene, afterID string, in ScriptInput) (*ScriptPanel, error) {
	var list []ScriptPanel
	if err := tx.Select(ctx, &list, `SELECT `+scriptPanelCols+` FROM script_panels WHERE scene_id = ? ORDER BY page_number, order_idx, created_at`, sc.ID); err != nil {
		return nil, err
	}
	pos := len(list)
	if afterID != "" {
		for i, p := range list {
			if p.ID == afterID {
				pos = i + 1
			}
		}
	}
	if in.PageNumber == 0 {
		switch {
		case pos > 0:
			in.PageNumber = list[pos-1].PageNumber
		default:
			var last int
			_ = tx.Get(ctx, &last, `SELECT COALESCE(MAX(page_number), 0) FROM script_panels WHERE chapter_id = ?`, sc.ChapterID)
			in.PageNumber = last + 1
		}
		if in.PageNumber == 0 {
			in.PageNumber = 1
		}
	}
	now := s.now()
	sp := &ScriptPanel{ID: db.NewID(), ProjectID: sc.ProjectID, ChapterID: sc.ChapterID, SceneID: sc.ID, PageNumber: in.PageNumber,
		Label: in.Label, Camera: in.Camera, Shot: in.Shot, Emotion: in.Emotion, Action: in.Action, Notes: in.Notes,
		CreatedAt: now, UpdatedAt: now}
	sp.Dialogue.V, sp.SFX.V, sp.CharacterIDs.V, sp.SettingIDs.V = dialogueList(in.Dialogue), jsonList(in.SFX), jsonList(in.CharacterIDs), jsonList(in.SettingIDs)
	if _, err := tx.Exec(ctx, `INSERT INTO script_panels (id, project_id, chapter_id, scene_id, page_number, order_idx, label, camera, shot,
		emotion, action, dialogue, sfx, character_ids, setting_ids, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, sp.ID, sp.ProjectID, sp.ChapterID, sp.SceneID, sp.PageNumber, 0,
		sp.Label, sp.Camera, sp.Shot, sp.Emotion, sp.Action, db.MustJSON(sp.Dialogue.V), db.MustJSON(sp.SFX.V),
		db.MustJSON(sp.CharacterIDs.V), db.MustJSON(sp.SettingIDs.V), sp.Notes, now, now); err != nil {
		return nil, err
	}
	ordered := append(append(append([]ScriptPanel{}, list[:pos]...), *sp), list[pos:]...)
	for i, p := range ordered {
		if _, err := tx.Exec(ctx, `UPDATE script_panels SET order_idx = ? WHERE id = ?`, i, p.ID); err != nil {
			return nil, err
		}
	}
	if err := s.relabel(ctx, tx, sc.ChapterID); err != nil {
		return nil, err
	}
	return s.scriptPanelTx(ctx, tx, sp.ID)
}

func (s *Service) scriptPanelTx(ctx context.Context, q db.Querier, id string) (*ScriptPanel, error) {
	var sp ScriptPanel
	if err := q.Get(ctx, &sp, `SELECT `+scriptPanelCols+` FROM script_panels WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return &sp, nil
}

// relabel renumbers the "page.panel" labels of a chapter's script in reading
// order (scene order, then panel order) so 7.1, 7.2, 7.3 stay consecutive.
func (s *Service) relabel(ctx context.Context, q db.Querier, chapterID string) error {
	var rows []struct {
		ID   string `db:"id"`
		Page int    `db:"page_number"`
	}
	if err := q.Select(ctx, &rows, `SELECT sp.id, sp.page_number FROM script_panels sp LEFT JOIN scenes sc ON sc.id = sp.scene_id
		WHERE sp.chapter_id = ? ORDER BY sp.page_number, COALESCE(sc.order_idx, 0), sp.order_idx, sp.created_at`, chapterID); err != nil {
		return err
	}
	count := map[int]int{}
	for _, r := range rows {
		count[r.Page]++
		if _, err := q.Exec(ctx, `UPDATE script_panels SET label = ? WHERE id = ?`, fmt.Sprintf("%d.%d", r.Page, count[r.Page]), r.ID); err != nil {
			return err
		}
	}
	return nil
}

// UpdateScriptPanel replaces the content of a script panel.
func (s *Service) UpdateScriptPanel(ctx context.Context, a *Access, id string, in ScriptInput) (*ScriptPanel, error) {
	if err := s.require(ctx, a, EditScript); err != nil {
		return nil, err
	}
	sp, err := s.ScriptPanel(ctx, id)
	if err != nil || sp.ProjectID != a.Project.ID {
		return nil, core.ErrNotFound()
	}
	in.clean()
	if in.PageNumber == 0 {
		in.PageNumber = sp.PageNumber
	}
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE script_panels SET page_number = ?, camera = ?, shot = ?, emotion = ?, action = ?, dialogue = ?, sfx = ?,
			character_ids = ?, setting_ids = ?, notes = ?, updated_at = ? WHERE id = ?`, in.PageNumber, in.Camera, in.Shot, in.Emotion, in.Action,
			db.MustJSON(dialogueList(in.Dialogue)), db.MustJSON(jsonList(in.SFX)), db.MustJSON(jsonList(in.CharacterIDs)),
			db.MustJSON(jsonList(in.SettingIDs)), in.Notes, s.now(), id); err != nil {
			return err
		}
		return s.relabel(ctx, tx, sp.ChapterID)
	})
	if err != nil {
		return nil, err
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.script.updated", Event{Kind: "script_panel", ID: id, ChapterID: sp.ChapterID})
	return s.ScriptPanel(ctx, id)
}

// DeleteScriptPanel removes a script panel.
func (s *Service) DeleteScriptPanel(ctx context.Context, a *Access, id string) error {
	if err := s.require(ctx, a, EditScript); err != nil {
		return err
	}
	sp, err := s.ScriptPanel(ctx, id)
	if err != nil || sp.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM script_panels WHERE id = ?`, id); err != nil {
			return err
		}
		return s.relabel(ctx, tx, sp.ChapterID)
	})
	if err == nil {
		s.touchProject(ctx, s.db, a.Project.ID)
		s.Publish(ctx, a.Project.ID, "studio.script.updated", Event{Kind: "script_panel", ID: id, ChapterID: sp.ChapterID})
	}
	return err
}

// MoveScriptPanel moves a script panel up (-1) or down (+1) within its scene;
// crossing into the neighbour's page moves it onto that page.
func (s *Service) MoveScriptPanel(ctx context.Context, a *Access, id string, delta int) error {
	if err := s.require(ctx, a, EditScript); err != nil {
		return err
	}
	sp, err := s.ScriptPanel(ctx, id)
	if err != nil || sp.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		var list []ScriptPanel
		if err := tx.Select(ctx, &list, `SELECT `+scriptPanelCols+` FROM script_panels WHERE scene_id = ? ORDER BY page_number, order_idx, created_at`, sp.SceneID); err != nil {
			return err
		}
		i := slices.IndexFunc(list, func(p ScriptPanel) bool { return p.ID == id })
		j := i + delta
		if i < 0 || j < 0 || j >= len(list) {
			return nil
		}
		if list[i].PageNumber != list[j].PageNumber {
			// Crossing a page break moves the panel onto the neighbouring page
			// (last panel of the previous page, or first of the next one).
			list[i].PageNumber = list[j].PageNumber
		} else {
			list[i], list[j] = list[j], list[i]
		}
		for k, p := range list {
			if _, err := tx.Exec(ctx, `UPDATE script_panels SET order_idx = ?, page_number = ? WHERE id = ?`, k, p.PageNumber, p.ID); err != nil {
				return err
			}
		}
		return s.relabel(ctx, tx, sp.ChapterID)
	})
	if err == nil {
		s.Publish(ctx, a.Project.ID, "studio.script.updated", Event{Kind: "script_panel", ID: id, ChapterID: sp.ChapterID})
	}
	return err
}
