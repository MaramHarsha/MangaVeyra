package ai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestScriptInputStrictnessAndSourcePreservation(t *testing.T) {
	in := testScriptInput()
	in.Mode = "adapt"
	in.Premise = ""
	in.SourceText = "  Rain on the paper bridge.\n"
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeScriptInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SourceText != in.SourceText {
		t.Fatalf("source text was normalized: %q", decoded.SourceText)
	}

	duplicate := bytes.Replace(raw, []byte(`"id":"char-rin"`), []byte(`"id":"char-rin","id":"char-other"`), 1)
	if _, err := decodeScriptInput(duplicate); err == nil {
		t.Fatal("duplicate nested member accepted")
	}
	unknown := bytes.Replace(raw, []byte(`{"mode":`), []byte(`{"unknown":true,"mode":`), 1)
	if _, err := decodeScriptInput(unknown); err == nil {
		t.Fatal("unknown input field accepted")
	}
	if _, err := decodeScriptInput(append(append([]byte(nil), raw...), []byte(` {}`)...)); err == nil {
		t.Fatal("trailing input accepted")
	}
	invalidUTF8 := append([]byte(nil), raw...)
	invalidUTF8[len(invalidUTF8)-2] = 0xff
	if _, err := decodeScriptInput(invalidUTF8); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	for name, candidate := range map[string][]byte{
		"required premise": bytes.Replace(raw, []byte(`"premise":""`), []byte(`"premise":null`), 1),
		"optional source":  bytes.Replace(raw, []byte(`"source_text":"  Rain on the paper bridge.\n"`), []byte(`"source_text":null`), 1),
		"character role":   bytes.Replace(raw, []byte(`"role":"courier"`), []byte(`"role":null`), 1),
	} {
		t.Run("reject null "+name, func(t *testing.T) {
			if _, err := decodeScriptInput(candidate); err == nil {
				t.Fatalf("accepted null %s", name)
			}
		})
	}
}

func TestPlaceholderIsDeterministicAndSchemaStrict(t *testing.T) {
	in := testScriptInput()
	first := testScriptDocument(t, in)
	second := testScriptDocument(t, in)
	if !bytes.Equal(first, second) {
		t.Fatalf("placeholder is not byte-identical:\n%s\n%s", first, second)
	}
	_, normalized, err := decodeScriptDocument(first, in)
	if err != nil || !bytes.Equal(first, normalized) {
		t.Fatalf("placeholder strict validation = %v", err)
	}

	var doc scriptDocument
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	empty := scriptScene{Key: "scene-empty", Title: "Quiet platform", Summary: "", TimeOfDay: "dawn", Panels: []scriptPanel{}}
	doc.Scenes = append([]scriptScene{empty}, doc.Scenes...)
	withEmpty, _ := json.Marshal(doc)
	if _, _, err := decodeScriptDocument(withEmpty, in); err != nil {
		t.Fatalf("empty scene with global panels rejected: %v", err)
	}
	doc.Scenes = []scriptScene{empty}
	allEmpty, _ := json.Marshal(doc)
	if _, _, err := decodeScriptDocument(allEmpty, in); err == nil {
		t.Fatal("document with no panels accepted")
	}

	duplicateTop := append([]byte(`{"schema":"mangaveyra.script.v1",`), first[1:]...)
	if _, _, err := decodeScriptDocument(duplicateTop, in); err == nil {
		t.Fatal("duplicate top-level member accepted")
	}
	if _, _, err := decodeScriptDocument(append(append([]byte(nil), first...), []byte(` {}`)...), in); err == nil {
		t.Fatal("trailing output accepted")
	}

	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Scenes[0].Panels[0].CharacterIDs = []string{"char-not-allowed"}
	badCharacter, _ := json.Marshal(doc)
	if _, _, err := decodeScriptDocument(badCharacter, in); err == nil {
		t.Fatal("non-allowlisted character accepted")
	}

	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	copyPanel := doc.Scenes[0].Panels[0]
	doc.Scenes[0].Panels[0].PageNumber = 2
	copyPanel.Key = "panel-regresses"
	copyPanel.PageNumber = 1
	doc.Scenes[0].Panels = []scriptPanel{doc.Scenes[0].Panels[0], copyPanel}
	regressing, _ := json.Marshal(doc)
	if _, _, err := decodeScriptDocument(regressing, in); err == nil {
		t.Fatal("regressing page numbers accepted")
	}

	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	panel := &doc.Scenes[0].Panels[0]
	panel.Action = "The paper moon rises."
	panel.Dialogue = []scriptDialogue{{Kind: "caption"}}
	panel.SFX = []scriptSFX{{}}
	emptyEntriesWithAction, _ := json.Marshal(doc)
	if _, _, err := decodeScriptDocument(emptyEntriesWithAction, in); err != nil {
		t.Fatalf("empty dialogue/SFX entries beside content rejected: %v", err)
	}
	panel.Action = ""
	allContentEmpty, _ := json.Marshal(doc)
	if _, _, err := decodeScriptDocument(allContentEmpty, in); err == nil {
		t.Fatal("empty dialogue/SFX entries counted as panel content")
	}

	var generic map[string]any
	if err := json.Unmarshal(first, &generic); err != nil {
		t.Fatal(err)
	}
	scenes := generic["scenes"].([]any)
	panels := scenes[0].(map[string]any)["panels"].([]any)
	delete(panels[0].(map[string]any), "notes")
	missing, _ := json.Marshal(generic)
	if _, _, err := decodeScriptDocument(missing, in); err == nil {
		t.Fatal("missing required panel field accepted")
	}

	for name, mutate := range map[string]func(map[string]any){
		"top string": func(document map[string]any) { document["chapter_summary"] = nil },
		"scene array": func(document map[string]any) {
			document["scenes"].([]any)[0].(map[string]any)["panels"] = nil
		},
		"nested string": func(document map[string]any) {
			scene := document["scenes"].([]any)[0].(map[string]any)
			scene["panels"].([]any)[0].(map[string]any)["action"] = nil
		},
		"nested dialogue array": func(document map[string]any) {
			scene := document["scenes"].([]any)[0].(map[string]any)
			scene["panels"].([]any)[0].(map[string]any)["dialogue"] = nil
		},
		"nested character array": func(document map[string]any) {
			scene := document["scenes"].([]any)[0].(map[string]any)
			scene["panels"].([]any)[0].(map[string]any)["character_ids"] = nil
		},
	} {
		t.Run("reject null "+name, func(t *testing.T) {
			var candidate map[string]any
			if err := json.Unmarshal(first, &candidate); err != nil {
				t.Fatal(err)
			}
			mutate(candidate)
			raw, _ := json.Marshal(candidate)
			if _, _, err := decodeScriptDocument(raw, in); err == nil {
				t.Fatalf("accepted null %s", name)
			}
		})
	}

	maxInput := testScriptInput()
	maxInput.PagesTarget = 256
	maxInput.Premise = strings.Repeat("🌧", 4000)
	maxDocument, err := generatePlaceholderScript(maxInput)
	if err != nil {
		t.Fatalf("max-page multibyte placeholder: %v", err)
	}
	if len(maxDocument) > maxScriptDocumentBytes {
		t.Fatalf("max-page placeholder is %d bytes", len(maxDocument))
	}
}

func TestOpenRouterEnvelopeIsExactAndContextBounded(t *testing.T) {
	in := testScriptInput()
	req, err := buildOpenRouterRequest(in, scriptAttempt{})
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != openRouterModel || req.MaxTokens != 16_384 || len(req.Messages) != 2 ||
		req.ResponseFormat.Type != "json_schema" || !req.ResponseFormat.JSONSchema.Strict ||
		!req.Provider.RequireParameters || req.Provider.MaxPrice.Prompt != 0.40 ||
		req.Provider.MaxPrice.Completion != 1.60 || !req.Usage.Include {
		t.Fatalf("request envelope drift: %+v", req)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "messages", "max_tokens", "response_format", "provider", "usage"} {
		if _, ok := top[key]; !ok {
			t.Fatalf("request missing %q: %s", key, raw)
		}
	}
	if len(top) != 6 {
		t.Fatalf("unexpected request fields: %s", raw)
	}

	var schema map[string]any
	if err := json.Unmarshal(req.ResponseFormat.JSONSchema.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	sceneItems := schema["properties"].(map[string]any)["scenes"].(map[string]any)["items"].(map[string]any)
	panelSchema := sceneItems["properties"].(map[string]any)["panels"].(map[string]any)
	if _, hasMinimum := panelSchema["minItems"]; hasMinimum {
		t.Fatal("response schema incorrectly requires panels in every scene")
	}

	oversized := in
	oversized.Mode, oversized.Premise = "adapt", ""
	oversized.SourceText = strings.Repeat("x", maxScriptSourceBytes)
	if openRouterInputFits(oversized) {
		t.Fatal("oversized model-visible request considered context-safe")
	}
}
