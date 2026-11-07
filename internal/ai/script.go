package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

const (
	maxScriptDocumentBytes = 1 << 20
	maxScriptSourceBytes   = 1 << 20
	// A 1 MiB source can expand substantially when JSON-escaped. Bound the raw
	// request without reducing the documented source-text allowance.
	maxScriptInputBytes = 8 << 20
)

var (
	errInvalidScriptInput  = errors.New("ai: invalid script input")
	errInvalidScriptOutput = errors.New("ai: invalid script output")
)

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

func decodeScriptInput(raw json.RawMessage) (ScriptInput, error) {
	var in ScriptInput
	canonical, err := strictDecodeJSON(raw, maxScriptInputBytes, &in)
	if err != nil {
		return in, fmt.Errorf("%w: malformed JSON", errInvalidScriptInput)
	}
	if err := requireScriptInputShape(canonical); err != nil {
		return ScriptInput{}, fmt.Errorf("%w: input shape", errInvalidScriptInput)
	}
	if err := normalizeScriptInput(&in); err != nil {
		return ScriptInput{}, err
	}
	return in, nil
}

func normalizeScriptInput(in *ScriptInput) error {
	in.Mode = strings.TrimSpace(in.Mode)
	in.Premise = strings.TrimSpace(in.Premise)
	in.Style = strings.TrimSpace(in.Style)
	in.Format = strings.TrimSpace(in.Format)
	in.Language = strings.TrimSpace(in.Language)
	in.ChapterTitle = strings.TrimSpace(in.ChapterTitle)
	if in.Mode != "premise" && in.Mode != "adapt" {
		return fmt.Errorf("%w: mode", errInvalidScriptInput)
	}
	if in.Mode == "premise" && in.Premise == "" {
		return fmt.Errorf("%w: premise", errInvalidScriptInput)
	}
	// SourceText is model-visible author material. Preserve it byte-for-byte;
	// only use a trimmed view to decide whether the adaptation input is empty.
	if in.Mode == "adapt" && strings.TrimSpace(in.SourceText) == "" {
		return fmt.Errorf("%w: source text", errInvalidScriptInput)
	}
	if len(in.SourceText) > maxScriptSourceBytes || !utf8.ValidString(in.SourceText) {
		return fmt.Errorf("%w: source text", errInvalidScriptInput)
	}
	if utf8.RuneCountInString(in.Premise) > 16_000 || utf8.RuneCountInString(in.Style) > 240 ||
		utf8.RuneCountInString(in.Format) > 240 || utf8.RuneCountInString(in.Language) > 80 ||
		utf8.RuneCountInString(in.ChapterTitle) > 160 || in.ChapterNumber < 1 || in.ChapterNumber > 9999 {
		return fmt.Errorf("%w: input bounds", errInvalidScriptInput)
	}
	if in.PagesTarget < 0 || in.PagesTarget > 256 {
		return fmt.Errorf("%w: pages target", errInvalidScriptInput)
	}
	if len(in.Genres) > 32 || len(in.Characters) > 256 {
		return fmt.Errorf("%w: input cardinality", errInvalidScriptInput)
	}
	seenGenres := map[string]struct{}{}
	for i := range in.Genres {
		in.Genres[i] = strings.TrimSpace(in.Genres[i])
		if in.Genres[i] == "" || utf8.RuneCountInString(in.Genres[i]) > 160 {
			return fmt.Errorf("%w: genre", errInvalidScriptInput)
		}
		if _, exists := seenGenres[in.Genres[i]]; exists {
			return fmt.Errorf("%w: duplicate genre", errInvalidScriptInput)
		}
		seenGenres[in.Genres[i]] = struct{}{}
	}
	seenCharacters := map[string]struct{}{}
	for i := range in.Characters {
		c := &in.Characters[i]
		c.ID = strings.TrimSpace(c.ID)
		c.Name = strings.TrimSpace(c.Name)
		c.Role = strings.TrimSpace(c.Role)
		c.Look = strings.TrimSpace(c.Look)
		c.Personality = strings.TrimSpace(c.Personality)
		if c.ID == "" || len(c.ID) > 128 || c.Name == "" || utf8.RuneCountInString(c.Name) > 160 ||
			utf8.RuneCountInString(c.Role) > 240 || utf8.RuneCountInString(c.Look) > 2000 ||
			utf8.RuneCountInString(c.Personality) > 2000 {
			return fmt.Errorf("%w: character", errInvalidScriptInput)
		}
		if _, exists := seenCharacters[c.ID]; exists {
			return fmt.Errorf("%w: duplicate character", errInvalidScriptInput)
		}
		seenCharacters[c.ID] = struct{}{}
	}
	return nil
}

func decodeScriptDocument(raw json.RawMessage, input ScriptInput) (scriptDocument, json.RawMessage, error) {
	var doc scriptDocument
	canonical, err := strictDecodeJSON(raw, maxScriptDocumentBytes, &doc)
	if err != nil {
		return doc, nil, fmt.Errorf("%w: malformed JSON", errInvalidScriptOutput)
	}
	if err := requireScriptShape(canonical); err != nil {
		return scriptDocument{}, nil, fmt.Errorf("%w: document shape", errInvalidScriptOutput)
	}
	if err := normalizeScriptDocument(&doc, input); err != nil {
		return scriptDocument{}, nil, err
	}
	normalized, err := json.Marshal(doc)
	if err != nil || len(normalized) > maxScriptDocumentBytes {
		return scriptDocument{}, nil, fmt.Errorf("%w: normalized document", errInvalidScriptOutput)
	}
	return doc, normalized, nil
}

func normalizeScriptDocument(doc *scriptDocument, input ScriptInput) error {
	if doc.Schema != SchemaScriptV1 || len(doc.Scenes) < 1 || len(doc.Scenes) > 32 {
		return fmt.Errorf("%w: schema or scene count", errInvalidScriptOutput)
	}
	doc.ChapterSummary = strings.TrimSpace(doc.ChapterSummary)
	if utf8.RuneCountInString(doc.ChapterSummary) > 4000 {
		return fmt.Errorf("%w: chapter summary", errInvalidScriptOutput)
	}
	allowed := make(map[string]struct{}, len(input.Characters))
	for _, c := range input.Characters {
		allowed[c.ID] = struct{}{}
	}
	sceneKeys, panelKeys := map[string]struct{}{}, map[string]struct{}{}
	panels, previousPage := 0, 0
	for si := range doc.Scenes {
		scene := &doc.Scenes[si]
		scene.Key = strings.TrimSpace(scene.Key)
		scene.Title = strings.TrimSpace(scene.Title)
		scene.Summary = strings.TrimSpace(scene.Summary)
		scene.TimeOfDay = strings.TrimSpace(scene.TimeOfDay)
		if scene.Key == "" || utf8.RuneCountInString(scene.Title) > 160 || utf8.RuneCountInString(scene.Summary) > 4000 ||
			utf8.RuneCountInString(scene.TimeOfDay) > 240 {
			return fmt.Errorf("%w: scene", errInvalidScriptOutput)
		}
		if _, exists := sceneKeys[scene.Key]; exists {
			return fmt.Errorf("%w: duplicate scene key", errInvalidScriptOutput)
		}
		sceneKeys[scene.Key] = struct{}{}
		for pi := range scene.Panels {
			panels++
			if panels > 256 {
				return fmt.Errorf("%w: panel count", errInvalidScriptOutput)
			}
			panel := &scene.Panels[pi]
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
				return fmt.Errorf("%w: panel", errInvalidScriptOutput)
			}
			previousPage = panel.PageNumber
			if _, exists := panelKeys[panel.Key]; exists {
				return fmt.Errorf("%w: duplicate panel key", errInvalidScriptOutput)
			}
			panelKeys[panel.Key] = struct{}{}
			hasContent := panel.Action != ""
			for di := range panel.Dialogue {
				d := &panel.Dialogue[di]
				d.Speaker = strings.TrimSpace(d.Speaker)
				d.CharacterID = strings.TrimSpace(d.CharacterID)
				d.Kind = strings.TrimSpace(d.Kind)
				d.Text = strings.TrimSpace(d.Text)
				if utf8.RuneCountInString(d.Speaker) > 160 || utf8.RuneCountInString(d.Text) > 2000 ||
					(d.Kind != "speech" && d.Kind != "thought" && d.Kind != "caption") {
					return fmt.Errorf("%w: dialogue", errInvalidScriptOutput)
				}
				if d.CharacterID != "" {
					if _, ok := allowed[d.CharacterID]; !ok {
						return fmt.Errorf("%w: dialogue character", errInvalidScriptOutput)
					}
				}
				hasContent = hasContent || d.Text != ""
			}
			for xi := range panel.SFX {
				x := &panel.SFX[xi]
				x.Text = strings.TrimSpace(x.Text)
				x.Note = strings.TrimSpace(x.Note)
				if utf8.RuneCountInString(x.Text) > 240 || utf8.RuneCountInString(x.Note) > 240 {
					return fmt.Errorf("%w: sfx", errInvalidScriptOutput)
				}
				hasContent = hasContent || x.Text != ""
			}
			seenIDs := map[string]struct{}{}
			for ci := range panel.CharacterIDs {
				id := strings.TrimSpace(panel.CharacterIDs[ci])
				panel.CharacterIDs[ci] = id
				if id == "" {
					return fmt.Errorf("%w: character id", errInvalidScriptOutput)
				}
				if _, ok := allowed[id]; !ok {
					return fmt.Errorf("%w: character id", errInvalidScriptOutput)
				}
				if _, exists := seenIDs[id]; exists {
					return fmt.Errorf("%w: duplicate character id", errInvalidScriptOutput)
				}
				seenIDs[id] = struct{}{}
			}
			if !hasContent {
				return fmt.Errorf("%w: empty panel", errInvalidScriptOutput)
			}
		}
	}
	if panels == 0 {
		return fmt.Errorf("%w: panel count", errInvalidScriptOutput)
	}
	return nil
}

func strictDecodeJSON(raw []byte, limit int, dest any) ([]byte, error) {
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
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, err
	}
	return canonical, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON data")
		}
		return err
	}
	return nil
}

// requireScriptInputShape closes encoding/json's null-to-zero-value gap and
// requires the non-omitempty contract fields. Optional members may be absent,
// but if present they must have their declared concrete type rather than null.
func requireScriptInputShape(raw []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	for _, key := range []string{"mode", "premise", "chapter_number"} {
		if value, ok := top[key]; !ok || isJSONNull(value) {
			return errors.New("missing required input member")
		}
	}
	for key, value := range top {
		if isJSONNull(value) {
			return fmt.Errorf("null input member %q", key)
		}
	}
	charactersRaw, ok := top["characters"]
	if !ok {
		return nil
	}
	var characters []map[string]json.RawMessage
	if err := json.Unmarshal(charactersRaw, &characters); err != nil {
		return err
	}
	for _, character := range characters {
		if character == nil {
			return errors.New("null character")
		}
		for _, key := range []string{"id", "name"} {
			if value, ok := character[key]; !ok || isJSONNull(value) {
				return errors.New("missing required character member")
			}
		}
		for key, value := range character {
			if isJSONNull(value) {
				return fmt.Errorf("null character member %q", key)
			}
		}
	}
	return nil
}

func isJSONNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

// requireScriptShape distinguishes an omitted field from an intentionally
// empty string or array. Every field in mangaveyra.script.v1 is required.
func requireScriptShape(raw []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || !hasExactKeys(top, "schema", "chapter_summary", "scenes") {
		return errors.New("invalid top-level shape")
	}
	var scenes []map[string]json.RawMessage
	if err := json.Unmarshal(top["scenes"], &scenes); err != nil {
		return err
	}
	for _, scene := range scenes {
		if !hasExactKeys(scene, "key", "title", "summary", "time_of_day", "panels") {
			return errors.New("invalid scene shape")
		}
		var panels []map[string]json.RawMessage
		if err := json.Unmarshal(scene["panels"], &panels); err != nil {
			return err
		}
		for _, panel := range panels {
			if !hasExactKeys(panel, "key", "page_number", "camera", "shot", "emotion", "action", "dialogue", "sfx", "character_ids", "notes") {
				return errors.New("invalid panel shape")
			}
			var dialogue []map[string]json.RawMessage
			if err := json.Unmarshal(panel["dialogue"], &dialogue); err != nil {
				return err
			}
			for _, d := range dialogue {
				if !hasExactKeys(d, "speaker", "character_id", "kind", "text") {
					return errors.New("invalid dialogue shape")
				}
			}
			var effects []map[string]json.RawMessage
			if err := json.Unmarshal(panel["sfx"], &effects); err != nil {
				return err
			}
			for _, effect := range effects {
				if !hasExactKeys(effect, "text", "note") {
					return errors.New("invalid sfx shape")
				}
			}
		}
	}
	return nil
}

func hasExactKeys(m map[string]json.RawMessage, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, key := range keys {
		value, ok := m[key]
		// encoding/json accepts null for strings, slices, and other zeroable
		// destinations. The v1 schema declares every member's concrete type, so
		// null is never a substitute for a required empty string or empty array.
		if !ok || isJSONNull(value) {
			return false
		}
	}
	return true
}

var scriptResponseSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["schema","chapter_summary","scenes"],
  "properties":{
    "schema":{"type":"string","const":"mangaveyra.script.v1"},
    "chapter_summary":{"type":"string","maxLength":4000},
    "scenes":{
      "type":"array","minItems":1,"maxItems":32,
      "items":{
        "type":"object","additionalProperties":false,
        "required":["key","title","summary","time_of_day","panels"],
        "properties":{
          "key":{"type":"string","minLength":1},
          "title":{"type":"string","maxLength":160},
          "summary":{"type":"string","maxLength":4000},
          "time_of_day":{"type":"string","maxLength":240},
          "panels":{
            "type":"array","maxItems":256,
            "items":{
              "type":"object","additionalProperties":false,
              "required":["key","page_number","camera","shot","emotion","action","dialogue","sfx","character_ids","notes"],
              "properties":{
                "key":{"type":"string","minLength":1},
                "page_number":{"type":"integer","minimum":1,"maximum":9999},
                "camera":{"type":"string","maxLength":240},
                "shot":{"type":"string","maxLength":240},
                "emotion":{"type":"string","maxLength":240},
                "action":{"type":"string","maxLength":2000},
                "dialogue":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["speaker","character_id","kind","text"],"properties":{"speaker":{"type":"string","maxLength":160},"character_id":{"type":"string"},"kind":{"type":"string","enum":["speech","thought","caption"]},"text":{"type":"string","maxLength":2000}}}},
                "sfx":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["text","note"],"properties":{"text":{"type":"string","maxLength":240},"note":{"type":"string","maxLength":240}}}},
                "character_ids":{"type":"array","items":{"type":"string"}},
                "notes":{"type":"string","maxLength":2000}
              }
            }
          }
        }
      }
    }
  }
}`)
