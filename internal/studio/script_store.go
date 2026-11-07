package studio

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

func (s *service) GetScript(ctx context.Context, chapterID string) (ScriptDoc, error) {
	return getScript(ctx, s.db, chapterID)
}

func getScript(ctx context.Context, q db.Querier, chapterID string) (ScriptDoc, error) {
	chapterID = strings.TrimSpace(chapterID)
	var chapter struct {
		ID      string `db:"id"`
		Summary string `db:"summary"`
	}
	if err := q.Get(ctx, &chapter, `SELECT id, summary FROM chapters WHERE id = ?`, chapterID); err != nil {
		if db.IsNotFound(err) {
			return ScriptDoc{}, ErrNotFound
		}
		return ScriptDoc{}, fmt.Errorf("studio: get script chapter: %w", err)
	}
	type sceneRow struct {
		ID        string `db:"id"`
		Title     string `db:"title"`
		Summary   string `db:"summary"`
		TimeOfDay string `db:"time_of_day"`
	}
	var scenes []sceneRow
	if err := q.Select(ctx, &scenes, `SELECT id, title, summary, time_of_day FROM scenes
		WHERE chapter_id = ? ORDER BY order_idx, id`, chapterID); err != nil {
		return ScriptDoc{}, fmt.Errorf("studio: get script scenes: %w", err)
	}
	type panelRow struct {
		ID           string         `db:"id"`
		SceneID      sql.NullString `db:"scene_id"`
		PageNumber   int            `db:"page_number"`
		Label        string         `db:"label"`
		Camera       string         `db:"camera"`
		Shot         string         `db:"shot"`
		Emotion      string         `db:"emotion"`
		Action       string         `db:"action"`
		Dialogue     string         `db:"dialogue"`
		SFX          string         `db:"sfx"`
		CharacterIDs string         `db:"character_ids"`
		Notes        string         `db:"notes"`
	}
	var panels []panelRow
	if err := q.Select(ctx, &panels, `SELECT id, scene_id, page_number, label, camera, shot, emotion, action,
		dialogue, sfx, character_ids, notes FROM script_panels WHERE chapter_id = ? ORDER BY order_idx, id`, chapterID); err != nil {
		return ScriptDoc{}, fmt.Errorf("studio: get script panels: %w", err)
	}
	byScene := make(map[string][]ScriptPanel, len(scenes))
	for _, row := range panels {
		if !row.SceneID.Valid {
			return ScriptDoc{}, fmt.Errorf("studio: script panel %s has no scene", row.ID)
		}
		panel := ScriptPanel{ID: row.ID, Key: row.ID, PageNumber: row.PageNumber, Label: row.Label, Camera: row.Camera,
			Shot: row.Shot, Emotion: row.Emotion, Action: row.Action, Notes: row.Notes,
			Dialogue: []ScriptDialogue{}, SFX: []ScriptSFX{}, CharacterIDs: []string{}}
		if err := json.Unmarshal([]byte(row.Dialogue), &panel.Dialogue); err != nil {
			return ScriptDoc{}, fmt.Errorf("studio: decode panel dialogue: %w", err)
		}
		if err := json.Unmarshal([]byte(row.SFX), &panel.SFX); err != nil {
			return ScriptDoc{}, fmt.Errorf("studio: decode panel SFX: %w", err)
		}
		if err := json.Unmarshal([]byte(row.CharacterIDs), &panel.CharacterIDs); err != nil {
			return ScriptDoc{}, fmt.Errorf("studio: decode panel character IDs: %w", err)
		}
		if panel.Dialogue == nil {
			panel.Dialogue = []ScriptDialogue{}
		}
		if panel.SFX == nil {
			panel.SFX = []ScriptSFX{}
		}
		if panel.CharacterIDs == nil {
			panel.CharacterIDs = []string{}
		}
		byScene[row.SceneID.String] = append(byScene[row.SceneID.String], panel)
	}
	doc := ScriptDoc{Schema: scriptSchemaV1, ChapterID: chapter.ID, ChapterSummary: chapter.Summary, Scenes: make([]ScriptScene, 0, len(scenes))}
	for _, row := range scenes {
		scenePanels := byScene[row.ID]
		if scenePanels == nil {
			scenePanels = []ScriptPanel{}
		}
		doc.Scenes = append(doc.Scenes, ScriptScene{ID: row.ID, Key: row.ID, Title: row.Title, Summary: row.Summary,
			TimeOfDay: row.TimeOfDay, Panels: scenePanels})
		delete(byScene, row.ID)
	}
	if len(byScene) != 0 {
		return ScriptDoc{}, fmt.Errorf("studio: script contains panels for an unknown scene")
	}
	return doc, nil
}

// EditScript updates the editable fields of an existing script without
// replacing scene or panel rows. This keeps page/layer references to
// script_panels.id valid and makes the chapter timestamp an optimistic fence.
func (s *service) EditScript(ctx context.Context, in EditScriptInput) (Chapter, error) {
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.ChapterID = strings.TrimSpace(in.ChapterID)
	in.Doc.ChapterID = strings.TrimSpace(in.Doc.ChapterID)
	if in.ProjectID == "" || in.ChapterID == "" || in.Doc.ChapterID != in.ChapterID || in.ExpectedUpdatedAt.IsZero() {
		return Chapter{}, ErrInvalid
	}
	var out Chapter
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.authorize(ctx, tx, in.Actor, in.ProjectID, ActEditScript); err != nil {
			return err
		}
		var chapter struct {
			ProjectID string    `db:"project_id"`
			Locked    bool      `db:"locked"`
			AILabel   string    `db:"ai_label"`
			UpdatedAt time.Time `db:"updated_at"`
		}
		if err := tx.Get(ctx, &chapter, `SELECT project_id, locked, ai_label, updated_at FROM chapters WHERE id = ?`+forUpdate(tx), in.ChapterID); err != nil {
			if db.IsNotFound(err) {
				return ErrNotFound
			}
			return fmt.Errorf("studio: lock script chapter for edit: %w", err)
		}
		if chapter.ProjectID != in.ProjectID {
			return ErrForbidden
		}
		if chapter.Locked {
			return ErrLocked
		}
		if !chapter.UpdatedAt.UTC().Equal(in.ExpectedUpdatedAt.UTC()) {
			return ErrConflict
		}

		current, err := getScript(ctx, tx, in.ChapterID)
		if err != nil {
			return err
		}
		if err := sameScriptStructure(current, in.Doc); err != nil {
			return err
		}
		doc, err := s.normalizeAndValidateScript(ctx, tx, in.ProjectID, in.Doc)
		if err != nil {
			return err
		}

		now := db.Now()
		if !now.After(chapter.UpdatedAt.UTC()) {
			now = chapter.UpdatedAt.UTC().Add(time.Microsecond)
		}
		panelOrder := 0
		pagePositions := map[int]int{}
		for sceneIndex, scene := range doc.Scenes {
			if err := updateScriptScene(ctx, tx, in, scene, sceneIndex, now); err != nil {
				return err
			}
			for _, panel := range scene.Panels {
				pagePositions[panel.PageNumber]++
				label := fmt.Sprintf("%d.%d", panel.PageNumber, pagePositions[panel.PageNumber])
				if err := updateScriptPanel(ctx, tx, in, scene.ID, panel, panelOrder, label, now); err != nil {
					return err
				}
				panelOrder++
			}
		}

		labels := map[string]string{}
		if err := json.Unmarshal([]byte(chapter.AILabel), &labels); err != nil {
			return fmt.Errorf("studio: decode chapter AI label for edit: %w", err)
		}
		if labels == nil {
			labels = map[string]string{}
		}
		if labels["script"] == "ai" || labels["script"] == "ai_assisted" {
			labels["script"] = "ai_assisted"
		} else {
			labels["script"] = "human"
		}
		labelJSON, err := json.Marshal(labels)
		if err != nil {
			return fmt.Errorf("studio: encode chapter AI label for edit: %w", err)
		}
		result, err := tx.Exec(ctx, `UPDATE chapters SET summary = ?, status = 'scripting', ai_label = ?, updated_at = ? WHERE id = ?`,
			doc.ChapterSummary, string(labelJSON), now, in.ChapterID)
		if err != nil {
			return fmt.Errorf("studio: update edited script chapter: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return err
		}
		out, err = getChapter(ctx, tx, in.ChapterID)
		return err
	})
	if err != nil {
		return Chapter{}, err
	}
	return out, nil
}

func sameScriptStructure(current, edited ScriptDoc) error {
	if len(current.Scenes) == 0 || len(current.Scenes) != len(edited.Scenes) {
		return ErrConflict
	}
	for sceneIndex := range current.Scenes {
		before, after := current.Scenes[sceneIndex], edited.Scenes[sceneIndex]
		if after.ID == "" || after.ID != before.ID || after.Key != before.Key || len(before.Panels) == 0 ||
			len(before.Panels) != len(after.Panels) {
			return ErrConflict
		}
		for panelIndex := range before.Panels {
			beforePanel, afterPanel := before.Panels[panelIndex], after.Panels[panelIndex]
			if afterPanel.ID == "" || afterPanel.ID != beforePanel.ID || afterPanel.Key != beforePanel.Key {
				return ErrConflict
			}
		}
	}
	return nil
}

func updateScriptScene(ctx context.Context, tx *db.Tx, in EditScriptInput, scene ScriptScene, order int, now time.Time) error {
	result, err := tx.Exec(ctx, `UPDATE scenes SET order_idx = ?, title = ?, summary = ?, time_of_day = ?, updated_at = ?
		WHERE id = ? AND project_id = ? AND chapter_id = ?`, order, scene.Title, scene.Summary, scene.TimeOfDay, now,
		scene.ID, in.ProjectID, in.ChapterID)
	if err != nil {
		return fmt.Errorf("studio: update edited script scene: %w", err)
	}
	return requireOneRow(result)
}

func updateScriptPanel(ctx context.Context, tx *db.Tx, in EditScriptInput, sceneID string, panel ScriptPanel, order int,
	label string, now time.Time) error {
	dialogue, err := json.Marshal(panel.Dialogue)
	if err != nil {
		return fmt.Errorf("studio: encode edited panel dialogue: %w", err)
	}
	sfx, err := json.Marshal(panel.SFX)
	if err != nil {
		return fmt.Errorf("studio: encode edited panel SFX: %w", err)
	}
	characters, err := json.Marshal(panel.CharacterIDs)
	if err != nil {
		return fmt.Errorf("studio: encode edited panel characters: %w", err)
	}
	result, err := tx.Exec(ctx, `UPDATE script_panels SET scene_id = ?, page_number = ?, order_idx = ?, label = ?, camera = ?,
		shot = ?, emotion = ?, action = ?, dialogue = ?, sfx = ?, character_ids = ?, notes = ?, updated_at = ?
		WHERE id = ? AND project_id = ? AND chapter_id = ?`, sceneID, panel.PageNumber, order, label, panel.Camera,
		panel.Shot, panel.Emotion, panel.Action, string(dialogue), string(sfx), string(characters), panel.Notes, now,
		panel.ID, in.ProjectID, in.ChapterID)
	if err != nil {
		return fmt.Errorf("studio: update edited script panel: %w", err)
	}
	return requireOneRow(result)
}

func requireOneRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("studio: read edited script row count: %w", err)
	}
	if rows != 1 {
		return ErrConflict
	}
	return nil
}

func (s *service) ReplaceGeneratedScriptTx(ctx context.Context, tx *db.Tx, in ReplaceScriptInput) (ReplaceScriptResult, error) {
	if tx == nil {
		return ReplaceScriptResult{}, ErrInvalid
	}
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.ChapterID = strings.TrimSpace(in.ChapterID)
	in.JobID = strings.TrimSpace(in.JobID)
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	in.ModelID = strings.TrimSpace(in.ModelID)
	in.Provenance = strings.TrimSpace(in.Provenance)
	if in.ProjectID == "" || in.ChapterID == "" || in.JobID == "" || in.Provenance != "ai" {
		return ReplaceScriptResult{}, fmt.Errorf("%w: generated-script provenance is incomplete", ErrInvalid)
	}
	for _, check := range []struct{ name, value string }{{"job ID", in.JobID}, {"provider ID", in.ProviderID}, {"model ID", in.ModelID}} {
		if err := validateText(check.name, check.value, 240, check.name == "job ID"); err != nil {
			return ReplaceScriptResult{}, err
		}
	}
	// A generated script needs both the generic generation permission and the
	// narrower script-edit permission (artists may generate art, not scripts).
	if err := s.authorize(ctx, tx, in.Actor, in.ProjectID, ActGenerate); err != nil {
		return ReplaceScriptResult{}, err
	}
	if err := s.authorize(ctx, tx, in.Actor, in.ProjectID, ActEditScript); err != nil {
		return ReplaceScriptResult{}, err
	}

	var chapter struct {
		ProjectID string    `db:"project_id"`
		Locked    bool      `db:"locked"`
		Settings  string    `db:"settings"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	if err := tx.Get(ctx, &chapter, `SELECT project_id, locked, settings, updated_at FROM chapters WHERE id = ?`+forUpdate(tx), in.ChapterID); err != nil {
		if db.IsNotFound(err) {
			return ReplaceScriptResult{}, ErrNotFound
		}
		return ReplaceScriptResult{}, fmt.Errorf("studio: lock script chapter: %w", err)
	}
	if chapter.ProjectID != in.ProjectID {
		return ReplaceScriptResult{}, ErrForbidden
	}
	if chapter.Locked {
		return ReplaceScriptResult{}, ErrLocked
	}
	if !in.ExpectedUpdatedAt.IsZero() && !chapter.UpdatedAt.UTC().Equal(in.ExpectedUpdatedAt.UTC()) {
		return ReplaceScriptResult{}, ErrConflict
	}
	if in.Doc.ChapterID != "" && strings.TrimSpace(in.Doc.ChapterID) != in.ChapterID {
		return ReplaceScriptResult{}, fmt.Errorf("%w: document chapter does not match target", ErrInvalid)
	}
	doc, err := s.normalizeAndValidateScript(ctx, tx, in.ProjectID, in.Doc)
	if err != nil {
		return ReplaceScriptResult{}, err
	}

	settings := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(chapter.Settings), &settings); err != nil {
		return ReplaceScriptResult{}, fmt.Errorf("studio: decode chapter settings: %w", err)
	}
	provenance, err := json.Marshal(map[string]string{
		"source": in.Provenance, "job_id": in.JobID, "provider_id": in.ProviderID, "model_id": in.ModelID,
	})
	if err != nil {
		return ReplaceScriptResult{}, fmt.Errorf("studio: encode script provenance: %w", err)
	}
	settings["script_provenance"] = provenance
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return ReplaceScriptResult{}, fmt.Errorf("studio: encode chapter settings: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM script_panels WHERE chapter_id = ?`, in.ChapterID); err != nil {
		return ReplaceScriptResult{}, fmt.Errorf("studio: delete old script panels: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM scenes WHERE chapter_id = ?`, in.ChapterID); err != nil {
		return ReplaceScriptResult{}, fmt.Errorf("studio: delete old script scenes: %w", err)
	}

	now := db.Now()
	panelOrder := 0
	pagePositions := map[int]int{}
	pages := map[int]struct{}{}
	for sceneIndex, scene := range doc.Scenes {
		sceneID := db.NewID()
		if _, err := tx.Exec(ctx, `INSERT INTO scenes
			(id, project_id, chapter_id, order_idx, title, summary, time_of_day, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, sceneID, in.ProjectID, in.ChapterID, sceneIndex,
			scene.Title, scene.Summary, scene.TimeOfDay, now, now); err != nil {
			return ReplaceScriptResult{}, fmt.Errorf("studio: insert script scene: %w", err)
		}
		for _, panel := range scene.Panels {
			panelID := db.NewID()
			pagePositions[panel.PageNumber]++
			label := fmt.Sprintf("%d.%d", panel.PageNumber, pagePositions[panel.PageNumber])
			pages[panel.PageNumber] = struct{}{}
			dialogue, err := json.Marshal(panel.Dialogue)
			if err != nil {
				return ReplaceScriptResult{}, fmt.Errorf("studio: encode panel dialogue: %w", err)
			}
			sfx, err := json.Marshal(panel.SFX)
			if err != nil {
				return ReplaceScriptResult{}, fmt.Errorf("studio: encode panel SFX: %w", err)
			}
			characters, err := json.Marshal(panel.CharacterIDs)
			if err != nil {
				return ReplaceScriptResult{}, fmt.Errorf("studio: encode panel characters: %w", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO script_panels
				(id, project_id, chapter_id, scene_id, page_number, order_idx, label, camera, shot, emotion, action,
				 dialogue, sfx, character_ids, notes, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, panelID, in.ProjectID, in.ChapterID,
				sceneID, panel.PageNumber, panelOrder, label, panel.Camera, panel.Shot, panel.Emotion, panel.Action,
				string(dialogue), string(sfx), string(characters), panel.Notes, now, now); err != nil {
				return ReplaceScriptResult{}, fmt.Errorf("studio: insert script panel: %w", err)
			}
			panelOrder++
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE chapters SET summary = ?, status = 'scripting', settings = ?, updated_at = ? WHERE id = ?`,
		doc.ChapterSummary, string(settingsJSON), now, in.ChapterID); err != nil {
		return ReplaceScriptResult{}, fmt.Errorf("studio: update script chapter: %w", err)
	}
	if err := s.SetChapterAILabelTx(ctx, tx, in.ChapterID, "script", "ai"); err != nil {
		return ReplaceScriptResult{}, err
	}
	return ReplaceScriptResult{Scenes: len(doc.Scenes), Panels: panelOrder, Pages: len(pages)}, nil
}
