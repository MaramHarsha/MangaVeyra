package ai

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const defaultPlaceholderPages = 8

// generatePlaceholderScript is intentionally free of clocks, randomness and
// database identifiers. Normalized input therefore produces byte-identical
// output on every host.
func generatePlaceholderScript(in ScriptInput) (json.RawMessage, error) {
	pages := in.PagesTarget
	if pages == 0 {
		pages = defaultPlaceholderPages
	}
	source := in.Premise
	if in.Mode == "adapt" {
		source = in.SourceText
	}
	summary := runePrefix(strings.TrimSpace(source), 4000)
	if summary == "" {
		summary = "An original MangaVeyra chapter begins."
	}
	title := strings.TrimSpace(in.ChapterTitle)
	if title == "" {
		title = fmt.Sprintf("Chapter %d", in.ChapterNumber)
	}
	scene := scriptScene{Key: "scene-1", Title: runePrefix(title, 160), Summary: summary,
		TimeOfDay: "unspecified", Panels: make([]scriptPanel, 0, pages)}
	for page := 1; page <= pages; page++ {
		// Keep the repeated excerpt byte-bounded. At 256 pages, repeating the
		// full 4,000-rune summary (especially four-byte Unicode) could otherwise
		// push a valid offline result beyond the 1 MiB document ceiling.
		action := fmt.Sprintf("Placeholder panel %d of %d. %s", page, pages, utf8BytePrefix(summary, 512))
		panel := scriptPanel{Key: fmt.Sprintf("panel-%d", page), PageNumber: page, Camera: "storyboard",
			Shot: "placeholder", Emotion: "neutral", Action: runePrefix(action, 2000),
			Dialogue: []scriptDialogue{}, SFX: []scriptSFX{}, CharacterIDs: []string{}, Notes: "Editable offline placeholder."}
		if len(in.Characters) > 0 {
			character := in.Characters[(page-1)%len(in.Characters)]
			panel.CharacterIDs = []string{character.ID}
			panel.Dialogue = []scriptDialogue{{Speaker: character.Name, CharacterID: character.ID,
				Kind: "caption", Text: fmt.Sprintf("Storyboard beat %d.", page)}}
		}
		scene.Panels = append(scene.Panels, panel)
	}
	doc := scriptDocument{Schema: SchemaScriptV1, ChapterSummary: summary, Scenes: []scriptScene{scene}}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal placeholder: %w", err)
	}
	_, normalized, err := decodeScriptDocument(raw, in)
	if err != nil {
		return nil, fmt.Errorf("ai: validate placeholder: %w", err)
	}
	return normalized, nil
}

func runePrefix(s string, max int) string {
	runes := []rune(s)
	if len(runes) > max {
		runes = runes[:max]
	}
	return string(runes)
}

func utf8BytePrefix(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
