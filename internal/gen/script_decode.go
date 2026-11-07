package gen

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

const maxScriptDocumentBytes = 1 << 20

type scriptDocument struct {
	Schema         string        `json:"schema"`
	ChapterSummary string        `json:"chapter_summary"`
	Scenes         []scriptScene `json:"scenes"`
}

type scriptScene struct {
	Key       string        `json:"key"`
	Title     string        `json:"title"`
	Summary   string        `json:"summary"`
	TimeOfDay string        `json:"time_of_day"`
	Panels    []scriptPanel `json:"panels"`
}

type scriptPanel struct {
	Key          string           `json:"key"`
	PageNumber   int              `json:"page_number"`
	Camera       string           `json:"camera"`
	Shot         string           `json:"shot"`
	Emotion      string           `json:"emotion"`
	Action       string           `json:"action"`
	Dialogue     []scriptDialogue `json:"dialogue"`
	SFX          []scriptSFX      `json:"sfx"`
	CharacterIDs []string         `json:"character_ids"`
	Notes        string           `json:"notes"`
}

type scriptDialogue struct {
	Speaker     string `json:"speaker"`
	CharacterID string `json:"character_id"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
}

type scriptSFX struct {
	Text string `json:"text"`
	Note string `json:"note"`
}

func decodeScriptPayload(raw json.RawMessage) (scriptJobPayload, error) {
	var payload scriptJobPayload
	canonical, err := strictDecodeJSON(raw, maxScriptPayloadBytes, &payload)
	if err != nil {
		return scriptJobPayload{}, err
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(canonical, &top) != nil || !hasExactKeys(top, "version", "expected_updated_at", "token_id", "routing_mode", "remote", "input") {
		return scriptJobPayload{}, errors.New("invalid script job payload shape")
	}
	if err := requireScriptInputShape(top["input"]); err != nil {
		return scriptJobPayload{}, err
	}
	if payload.Version != scriptPayloadVersion || payload.ExpectedUpdatedAt.IsZero() || !validRoutingMode(payload.RoutingMode) ||
		(payload.TokenID != "" && !boundedStoredID(payload.TokenID)) {
		return scriptJobPayload{}, errors.New("invalid script job payload")
	}
	payload.Input, err = normalizeScriptInput(payload.Input)
	if err != nil {
		return scriptJobPayload{}, err
	}
	return payload, nil
}

func decodeGeneratedScript(raw json.RawMessage, input ai.ScriptInput, chapterID string) (studio.ScriptDoc, error) {
	var wire scriptDocument
	canonical, err := strictDecodeJSON(raw, maxScriptDocumentBytes, &wire)
	if err != nil {
		return studio.ScriptDoc{}, err
	}
	if err := requireGeneratedScriptShape(canonical); err != nil {
		return studio.ScriptDoc{}, err
	}
	allowed := make(map[string]struct{}, len(input.Characters))
	for _, character := range input.Characters {
		allowed[character.ID] = struct{}{}
	}
	if err := normalizeGeneratedScript(&wire, allowed); err != nil {
		return studio.ScriptDoc{}, err
	}
	doc := studio.ScriptDoc{Schema: wire.Schema, ChapterID: chapterID, ChapterSummary: wire.ChapterSummary,
		Scenes: make([]studio.ScriptScene, 0, len(wire.Scenes))}
	for _, scene := range wire.Scenes {
		outScene := studio.ScriptScene{Key: scene.Key, Title: scene.Title, Summary: scene.Summary,
			TimeOfDay: scene.TimeOfDay, Panels: make([]studio.ScriptPanel, 0, len(scene.Panels))}
		for _, panel := range scene.Panels {
			outPanel := studio.ScriptPanel{Key: panel.Key, PageNumber: panel.PageNumber, Camera: panel.Camera,
				Shot: panel.Shot, Emotion: panel.Emotion, Action: panel.Action, Notes: panel.Notes,
				Dialogue:     make([]studio.ScriptDialogue, 0, len(panel.Dialogue)),
				SFX:          make([]studio.ScriptSFX, 0, len(panel.SFX)),
				CharacterIDs: append([]string(nil), panel.CharacterIDs...)}
			for _, line := range panel.Dialogue {
				outPanel.Dialogue = append(outPanel.Dialogue, studio.ScriptDialogue{
					Speaker: line.Speaker, CharacterID: line.CharacterID, Kind: line.Kind, Text: line.Text,
				})
			}
			for _, effect := range panel.SFX {
				outPanel.SFX = append(outPanel.SFX, studio.ScriptSFX{Text: effect.Text, Note: effect.Note})
			}
			outScene.Panels = append(outScene.Panels, outPanel)
		}
		doc.Scenes = append(doc.Scenes, outScene)
	}
	return doc, nil
}

func strictDecodeJSON(raw []byte, limit int, destination any) ([]byte, error) {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) {
		return nil, errors.New("invalid JSON boundary")
	}
	if err := mvpack.CheckMemberNames(raw, ""); err != nil {
		return nil, err
	}
	canonical, err := mvpack.Canonicalize(raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON data")
		}
		return nil, err
	}
	return canonical, nil
}

func requireScriptInputShape(raw json.RawMessage) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return errors.New("invalid script input object")
	}
	for _, key := range []string{"mode", "premise", "chapter_number"} {
		if value, ok := top[key]; !ok || isJSONNull(value) {
			return errors.New("missing script input member")
		}
	}
	for _, value := range top {
		if isJSONNull(value) {
			return errors.New("null script input member")
		}
	}
	charactersRaw, ok := top["characters"]
	if !ok {
		return nil
	}
	var characters []map[string]json.RawMessage
	if err := json.Unmarshal(charactersRaw, &characters); err != nil || characters == nil {
		return errors.New("invalid characters array")
	}
	for _, character := range characters {
		if character == nil {
			return errors.New("null character")
		}
		for _, key := range []string{"id", "name"} {
			if value, ok := character[key]; !ok || isJSONNull(value) {
				return errors.New("missing character member")
			}
		}
		for _, value := range character {
			if isJSONNull(value) {
				return errors.New("null character member")
			}
		}
	}
	return nil
}

func requireGeneratedScriptShape(raw json.RawMessage) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || !hasExactKeys(top, "schema", "chapter_summary", "scenes") {
		return errors.New("invalid script document shape")
	}
	var scenes []map[string]json.RawMessage
	if err := json.Unmarshal(top["scenes"], &scenes); err != nil || scenes == nil {
		return errors.New("invalid scenes array")
	}
	for _, scene := range scenes {
		if !hasExactKeys(scene, "key", "title", "summary", "time_of_day", "panels") {
			return errors.New("invalid scene shape")
		}
		var panels []map[string]json.RawMessage
		if err := json.Unmarshal(scene["panels"], &panels); err != nil || panels == nil {
			return errors.New("invalid panels array")
		}
		for _, panel := range panels {
			if !hasExactKeys(panel, "key", "page_number", "camera", "shot", "emotion", "action", "dialogue", "sfx", "character_ids", "notes") {
				return errors.New("invalid panel shape")
			}
			var dialogue []map[string]json.RawMessage
			if err := json.Unmarshal(panel["dialogue"], &dialogue); err != nil || dialogue == nil {
				return errors.New("invalid dialogue array")
			}
			for _, line := range dialogue {
				if !hasExactKeys(line, "speaker", "character_id", "kind", "text") {
					return errors.New("invalid dialogue shape")
				}
			}
			var effects []map[string]json.RawMessage
			if err := json.Unmarshal(panel["sfx"], &effects); err != nil || effects == nil {
				return errors.New("invalid SFX array")
			}
			for _, effect := range effects {
				if !hasExactKeys(effect, "text", "note") {
					return errors.New("invalid SFX shape")
				}
			}
			var characterIDs []string
			if err := json.Unmarshal(panel["character_ids"], &characterIDs); err != nil || characterIDs == nil {
				return errors.New("invalid character IDs array")
			}
		}
	}
	return nil
}

func normalizeGeneratedScript(doc *scriptDocument, allowed map[string]struct{}) error {
	if doc.Schema != ai.SchemaScriptV1 || len(doc.Scenes) < 1 || len(doc.Scenes) > 32 {
		return errors.New("invalid script schema or scene count")
	}
	doc.ChapterSummary = strings.TrimSpace(doc.ChapterSummary)
	if utf8.RuneCountInString(doc.ChapterSummary) > 4000 {
		return errors.New("chapter summary exceeds bounds")
	}
	sceneKeys := make(map[string]struct{}, len(doc.Scenes))
	panelKeys := make(map[string]struct{})
	panelCount, previousPage := 0, 0
	for sceneIndex := range doc.Scenes {
		scene := &doc.Scenes[sceneIndex]
		scene.Key = strings.TrimSpace(scene.Key)
		scene.Title = strings.TrimSpace(scene.Title)
		scene.Summary = strings.TrimSpace(scene.Summary)
		scene.TimeOfDay = strings.TrimSpace(scene.TimeOfDay)
		if scene.Key == "" || utf8.RuneCountInString(scene.Title) > 160 || utf8.RuneCountInString(scene.Summary) > 4000 ||
			utf8.RuneCountInString(scene.TimeOfDay) > 240 {
			return errors.New("invalid scene")
		}
		if _, duplicate := sceneKeys[scene.Key]; duplicate {
			return errors.New("duplicate scene key")
		}
		sceneKeys[scene.Key] = struct{}{}
		for panelIndex := range scene.Panels {
			panelCount++
			if panelCount > 256 {
				return errors.New("too many panels")
			}
			panel := &scene.Panels[panelIndex]
			panel.Key = strings.TrimSpace(panel.Key)
			panel.Camera = strings.TrimSpace(panel.Camera)
			panel.Shot = strings.TrimSpace(panel.Shot)
			panel.Emotion = strings.TrimSpace(panel.Emotion)
			panel.Action = strings.TrimSpace(panel.Action)
			panel.Notes = strings.TrimSpace(panel.Notes)
			if panel.Key == "" || panel.PageNumber < 1 || panel.PageNumber > 9999 || panel.PageNumber < previousPage ||
				utf8.RuneCountInString(panel.Camera) > 240 || utf8.RuneCountInString(panel.Shot) > 240 ||
				utf8.RuneCountInString(panel.Emotion) > 240 || utf8.RuneCountInString(panel.Action) > 2000 ||
				utf8.RuneCountInString(panel.Notes) > 2000 {
				return errors.New("invalid panel")
			}
			previousPage = panel.PageNumber
			if _, duplicate := panelKeys[panel.Key]; duplicate {
				return errors.New("duplicate panel key")
			}
			panelKeys[panel.Key] = struct{}{}
			hasContent := panel.Action != ""
			for lineIndex := range panel.Dialogue {
				line := &panel.Dialogue[lineIndex]
				line.Speaker = strings.TrimSpace(line.Speaker)
				line.CharacterID = strings.TrimSpace(line.CharacterID)
				line.Kind = strings.TrimSpace(line.Kind)
				line.Text = strings.TrimSpace(line.Text)
				if utf8.RuneCountInString(line.Speaker) > 160 || utf8.RuneCountInString(line.Text) > 2000 ||
					(line.Kind != "speech" && line.Kind != "thought" && line.Kind != "caption") {
					return errors.New("invalid dialogue")
				}
				if line.CharacterID != "" {
					if _, ok := allowed[line.CharacterID]; !ok {
						return errors.New("dialogue character is outside the request allow-list")
					}
				}
				hasContent = hasContent || line.Text != ""
			}
			for effectIndex := range panel.SFX {
				effect := &panel.SFX[effectIndex]
				effect.Text = strings.TrimSpace(effect.Text)
				effect.Note = strings.TrimSpace(effect.Note)
				if utf8.RuneCountInString(effect.Text) > 240 || utf8.RuneCountInString(effect.Note) > 240 {
					return errors.New("invalid SFX")
				}
				hasContent = hasContent || effect.Text != ""
			}
			seenCharacters := make(map[string]struct{}, len(panel.CharacterIDs))
			for characterIndex := range panel.CharacterIDs {
				id := strings.TrimSpace(panel.CharacterIDs[characterIndex])
				panel.CharacterIDs[characterIndex] = id
				if id == "" {
					return errors.New("empty panel character ID")
				}
				if _, ok := allowed[id]; !ok {
					return errors.New("panel character is outside the request allow-list")
				}
				if _, duplicate := seenCharacters[id]; duplicate {
					return errors.New("duplicate panel character ID")
				}
				seenCharacters[id] = struct{}{}
			}
			if !hasContent {
				return errors.New("empty panel")
			}
		}
	}
	if panelCount == 0 {
		return errors.New("script contains no panels")
	}
	return nil
}

func hasExactKeys(object map[string]json.RawMessage, keys ...string) bool {
	if object == nil || len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		value, ok := object[key]
		if !ok || isJSONNull(value) {
			return false
		}
	}
	return true
}

func isJSONNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func boundedStoredID(value string) bool {
	return db.ValidID(value) && strings.TrimSpace(value) == value
}
