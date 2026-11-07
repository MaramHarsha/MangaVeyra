package gen

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

func TestScriptPayloadStrictDecoderAdversarialMatrix(t *testing.T) {
	characterID := db.NewID()
	valid := []byte(`{"version":2,"expected_updated_at":"2026-11-07T00:00:00Z","token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"A courier crosses the gate.","chapter_number":1,"characters":[{"id":"` + characterID + `","name":"Rin"}]}}`)
	if payload, err := decodeScriptPayload(valid); err != nil || payload.Input.Characters[0].ID != characterID {
		t.Fatalf("valid payload: %+v, %v", payload, err)
	}
	withRevision := func(raw string) []byte {
		return []byte(`{"expected_updated_at":"2026-11-07T00:00:00Z",` + strings.TrimPrefix(raw, "{"))
	}
	cases := map[string][]byte{
		"duplicate":        withRevision(`{"version":2,"version":2,"token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
		"unknown":          append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"unknown":1}`)...),
		"trailing":         append(append([]byte(nil), valid...), []byte(` {}`)...),
		"null top":         []byte(`null`),
		"missing revision": []byte(`{"version":2,"token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
		"null revision":    []byte(`{"version":2,"expected_updated_at":null,"token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
		"bad revision":     []byte(`{"version":2,"expected_updated_at":"not-a-time","token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
		"zero revision":    []byte(`{"version":2,"expected_updated_at":"0001-01-01T00:00:00Z","token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
		"null input":       withRevision(`{"version":2,"token_id":"","routing_mode":"balanced","remote":false,"input":null}`),
		"null member":      withRevision(`{"version":2,"token_id":null,"routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
		"wrong type":       withRevision(`{"version":"2","token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
		"bad version":      withRevision(`{"version":1,"token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
		"empty mode":       withRevision(`{"version":2,"token_id":"","routing_mode":"","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
		"input unknown":    withRevision(`{"version":2,"token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1,"unknown":1}}`),
		"null array":       withRevision(`{"version":2,"token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1,"characters":null}}`),
		"bad bound":        withRevision(`{"version":2,"token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":0}}`),
		"uppercase key":    withRevision(`{"Version":2,"token_id":"","routing_mode":"balanced","remote":false,"input":{"mode":"premise","premise":"x","chapter_number":1}}`),
	}
	invalidUTF8 := append([]byte(nil), valid...)
	invalidUTF8[len(invalidUTF8)-2] = 0xff
	cases["invalid UTF-8"] = invalidUTF8
	oversized := bytes.Repeat([]byte{'x'}, maxScriptPayloadBytes+1)
	cases["oversized"] = oversized
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeScriptPayload(raw); err == nil {
				t.Fatalf("accepted adversarial payload: %.200s", raw)
			}
		})
	}
}

func TestGeneratedScriptStrictDecoderAdversarialMatrix(t *testing.T) {
	characterID := db.NewID()
	input := ai.ScriptInput{Mode: "premise", Premise: "A courier.", ChapterNumber: 1,
		Characters: []ai.ScriptCharacter{{ID: characterID, Name: "Rin"}}}
	valid := decodeTestScriptJSON(characterID, "Rin runs.")
	if doc, err := decodeGeneratedScript(valid, input, db.NewID()); err != nil || len(doc.Scenes) != 1 {
		t.Fatalf("valid generated script: %+v, %v", doc, err)
	}
	text := string(valid)
	unknown := strings.Replace(text, `"chapter_summary":`, `"unknown":1,"chapter_summary":`, 1)
	duplicate := strings.Replace(text, `"schema":`, `"schema":"mangaveyra.script.v1","schema":`, 1)
	nullScenes := strings.Replace(text, regexpValue(t, valid, "scenes"), `null`, 1)
	badEnum := strings.Replace(text, `"kind":"speech"`, `"kind":"shout"`, 1)
	badPage := strings.Replace(text, `"page_number":1`, `"page_number":0`, 1)
	badAllowlist := strings.ReplaceAll(text, characterID, db.NewID())
	emptyPanel := json.RawMessage(strings.Replace(string(decodeTestScriptJSON("", "")), `"text":"SPLASH"`, `"text":""`, 1))
	missing := strings.Replace(text, `,"notes":""`, "", 1)
	nullDialogue := strings.Replace(text, regexpValue(t, valid, "dialogue"), `null`, 1)
	cases := map[string]json.RawMessage{
		"unknown":           json.RawMessage(unknown),
		"duplicate":         json.RawMessage(duplicate),
		"trailing":          append(append(json.RawMessage(nil), valid...), []byte(` {}`)...),
		"null scenes":       json.RawMessage(nullScenes),
		"bad enum":          json.RawMessage(badEnum),
		"bad page":          json.RawMessage(badPage),
		"outside allowlist": json.RawMessage(badAllowlist),
		"empty panel":       emptyPanel,
		"missing field":     json.RawMessage(missing),
		"null dialogue":     json.RawMessage(nullDialogue),
		"top array":         json.RawMessage(`[]`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeGeneratedScript(raw, input, db.NewID()); err == nil {
				t.Fatalf("accepted adversarial document: %.300s", raw)
			}
		})
	}
}

func regexpValue(t *testing.T, raw json.RawMessage, key string) string {
	t.Helper()
	var top any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	value, ok := findJSONValue(top, key)
	if !ok {
		t.Fatalf("key %q not found", key)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func findJSONValue(value any, key string) (any, bool) {
	switch current := value.(type) {
	case map[string]any:
		if found, ok := current[key]; ok {
			return found, true
		}
		for _, child := range current {
			if found, ok := findJSONValue(child, key); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range current {
			if found, ok := findJSONValue(child, key); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func decodeTestScriptJSON(characterID, action string) json.RawMessage {
	dialogue := []map[string]any{}
	characterIDs := []string{}
	if characterID != "" {
		dialogue = append(dialogue, map[string]any{"speaker": "Rin", "character_id": characterID,
			"kind": "speech", "text": "The tide is turning."})
		characterIDs = append(characterIDs, characterID)
	}
	doc := map[string]any{
		"schema": "mangaveyra.script.v1", "chapter_summary": "The delivery begins.",
		"scenes": []any{map[string]any{
			"key": "scene-1", "title": "Sea Gate", "summary": "A storm closes in.", "time_of_day": "dawn",
			"panels": []any{map[string]any{
				"key": "panel-1", "page_number": 1, "camera": "wide", "shot": "establishing",
				"emotion": "urgent", "action": action, "dialogue": dialogue,
				"sfx":           []any{map[string]any{"text": "SPLASH", "note": "distant"}},
				"character_ids": characterIDs, "notes": "",
			}},
		}},
	}
	raw, _ := json.Marshal(doc)
	return raw
}
