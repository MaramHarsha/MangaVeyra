package studio

import (
	"context"
	"fmt"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

const scriptSchemaV1 = "mangaveyra.script.v1"

func (s *service) ValidateScript(ctx context.Context, projectID string, doc ScriptDoc) error {
	_, err := s.normalizeAndValidateScript(ctx, s.db, strings.TrimSpace(projectID), doc)
	return err
}

func (s *service) normalizeAndValidateScript(ctx context.Context, q db.Querier, projectID string, doc ScriptDoc) (ScriptDoc, error) {
	if projectID == "" || doc.Schema != scriptSchemaV1 {
		return ScriptDoc{}, fmt.Errorf("%w: script schema must be %s", ErrInvalid, scriptSchemaV1)
	}
	doc.ChapterID = strings.TrimSpace(doc.ChapterID)
	doc.ChapterSummary = strings.TrimSpace(doc.ChapterSummary)
	if err := validateText("chapter summary", doc.ChapterSummary, 4000, false); err != nil {
		return ScriptDoc{}, err
	}
	if len(doc.Scenes) < 1 || len(doc.Scenes) > 32 {
		return ScriptDoc{}, fmt.Errorf("%w: script must contain 1 to 32 scenes", ErrInvalid)
	}

	var projectExists int
	if err := q.Get(ctx, &projectExists, `SELECT 1 FROM projects WHERE id = ?`+forUpdate(q), projectID); err != nil {
		if db.IsNotFound(err) {
			return ScriptDoc{}, ErrNotFound
		}
		return ScriptDoc{}, fmt.Errorf("studio: find script project: %w", err)
	}
	var allowedIDs []string
	if err := q.Select(ctx, &allowedIDs, `SELECT id FROM characters WHERE project_id = ? ORDER BY id`+forUpdate(q), projectID); err != nil {
		return ScriptDoc{}, fmt.Errorf("studio: load script character allow-list: %w", err)
	}
	allowed := make(map[string]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = struct{}{}
	}

	sceneKeys := make(map[string]struct{}, len(doc.Scenes))
	panelKeys := make(map[string]struct{})
	panelCount, previousPage := 0, 0
	for sceneIndex := range doc.Scenes {
		scene := &doc.Scenes[sceneIndex]
		if scene.Panels == nil {
			scene.Panels = []ScriptPanel{}
		}
		scene.Key = strings.TrimSpace(scene.Key)
		scene.Title = strings.TrimSpace(scene.Title)
		scene.Summary = strings.TrimSpace(scene.Summary)
		scene.TimeOfDay = strings.TrimSpace(scene.TimeOfDay)
		if err := validateKey("scene", scene.Key, sceneKeys); err != nil {
			return ScriptDoc{}, err
		}
		for _, check := range []struct {
			name, value string
			max         int
		}{{"scene title", scene.Title, 160}, {"scene summary", scene.Summary, 4000}, {"time of day", scene.TimeOfDay, 240}} {
			if err := validateText(check.name, check.value, check.max, false); err != nil {
				return ScriptDoc{}, err
			}
		}
		for panelIndex := range scene.Panels {
			panelCount++
			if panelCount > 256 {
				return ScriptDoc{}, fmt.Errorf("%w: script may contain at most 256 panels", ErrInvalid)
			}
			panel := &scene.Panels[panelIndex]
			panel.Key = strings.TrimSpace(panel.Key)
			panel.Camera = strings.TrimSpace(panel.Camera)
			panel.Shot = strings.TrimSpace(panel.Shot)
			panel.Emotion = strings.TrimSpace(panel.Emotion)
			panel.Action = strings.TrimSpace(panel.Action)
			panel.Notes = strings.TrimSpace(panel.Notes)
			if panel.Dialogue == nil {
				panel.Dialogue = []ScriptDialogue{}
			}
			if panel.SFX == nil {
				panel.SFX = []ScriptSFX{}
			}
			if panel.CharacterIDs == nil {
				panel.CharacterIDs = []string{}
			}
			if err := validateKey("panel", panel.Key, panelKeys); err != nil {
				return ScriptDoc{}, err
			}
			if panel.PageNumber < 1 || panel.PageNumber > 9999 || panel.PageNumber < previousPage {
				return ScriptDoc{}, fmt.Errorf("%w: panel page numbers must be 1 to 9999 and nondecreasing", ErrInvalid)
			}
			previousPage = panel.PageNumber
			for _, check := range []struct {
				name, value string
				max         int
			}{{"camera", panel.Camera, 240}, {"shot", panel.Shot, 240}, {"emotion", panel.Emotion, 240},
				{"action", panel.Action, 2000}, {"panel notes", panel.Notes, 2000}} {
				if err := validateText(check.name, check.value, check.max, false); err != nil {
					return ScriptDoc{}, err
				}
			}

			hasContent := panel.Action != ""
			for i := range panel.Dialogue {
				line := &panel.Dialogue[i]
				line.Speaker = strings.TrimSpace(line.Speaker)
				line.CharacterID = strings.TrimSpace(line.CharacterID)
				line.Kind = strings.TrimSpace(line.Kind)
				line.Text = strings.TrimSpace(line.Text)
				if err := validateText("dialogue speaker", line.Speaker, 160, false); err != nil {
					return ScriptDoc{}, err
				}
				if err := validateText("dialogue text", line.Text, 2000, false); err != nil {
					return ScriptDoc{}, err
				}
				switch line.Kind {
				case "speech", "thought", "caption":
				default:
					return ScriptDoc{}, fmt.Errorf("%w: invalid dialogue kind", ErrInvalid)
				}
				if err := allowedCharacterID(line.CharacterID, allowed); err != nil {
					return ScriptDoc{}, err
				}
				hasContent = hasContent || line.Text != ""
			}
			for i := range panel.SFX {
				sfx := &panel.SFX[i]
				sfx.Text = strings.TrimSpace(sfx.Text)
				sfx.Note = strings.TrimSpace(sfx.Note)
				if err := validateText("SFX text", sfx.Text, 240, false); err != nil {
					return ScriptDoc{}, err
				}
				if err := validateText("SFX note", sfx.Note, 240, false); err != nil {
					return ScriptDoc{}, err
				}
				hasContent = hasContent || sfx.Text != ""
			}
			seenCharacters := make(map[string]struct{}, len(panel.CharacterIDs))
			for i := range panel.CharacterIDs {
				id := strings.TrimSpace(panel.CharacterIDs[i])
				panel.CharacterIDs[i] = id
				if id == "" {
					return ScriptDoc{}, fmt.Errorf("%w: empty character ID", ErrInvalid)
				}
				if _, duplicate := seenCharacters[id]; duplicate {
					return ScriptDoc{}, fmt.Errorf("%w: duplicate character ID", ErrInvalid)
				}
				seenCharacters[id] = struct{}{}
				if err := allowedCharacterID(id, allowed); err != nil {
					return ScriptDoc{}, err
				}
			}
			if !hasContent {
				return ScriptDoc{}, fmt.Errorf("%w: every panel needs action, dialogue, or SFX", ErrInvalid)
			}
		}
	}
	if panelCount == 0 {
		return ScriptDoc{}, fmt.Errorf("%w: script must contain at least one panel", ErrInvalid)
	}
	return doc, nil
}

func validateKey(kind, key string, seen map[string]struct{}) error {
	if err := validateText(kind+" key", key, 0, true); err != nil {
		return err
	}
	if _, duplicate := seen[key]; duplicate {
		return fmt.Errorf("%w: duplicate %s key", ErrInvalid, kind)
	}
	seen[key] = struct{}{}
	return nil
}

func allowedCharacterID(id string, allowed map[string]struct{}) error {
	if id == "" {
		return nil
	}
	if _, ok := allowed[id]; !ok {
		return fmt.Errorf("%w: character ID is outside the project allow-list", ErrInvalid)
	}
	return nil
}
