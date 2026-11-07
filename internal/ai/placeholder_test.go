package ai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

func placeholderDoc(t *testing.T, in ScriptInput) scriptDocument {
	t.Helper()
	raw, err := generatePlaceholderScript(in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := generatePlaceholderScript(in)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("placeholder is not deterministic: %v", err)
	}
	var doc scriptDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// scriptOf reports whether s contains a letter of the language's own script.
func scriptOf(lang, s string) bool {
	tables := map[string][]*unicode.RangeTable{
		"ja": {unicode.Hiragana, unicode.Katakana, unicode.Han}, "ko": {unicode.Hangul}, "zh-Hans": {unicode.Han},
		"zh-Hant": {unicode.Han}, "hi": {unicode.Devanagari},
	}[lang]
	if tables == nil {
		tables = []*unicode.RangeTable{unicode.Latin}
	}
	for _, r := range s {
		if unicode.IsOneOf(tables, r) {
			return true
		}
	}
	return false
}

func TestPlaceholderScriptHasDialogueInEveryContentLanguage(t *testing.T) {
	seen := map[string]string{}
	for _, lang := range pagedoc.ContentLanguages {
		for _, characters := range [][]ScriptCharacter{nil, {{ID: "char-rin", Name: "Rin"}}, {{ID: "char-rin", Name: "Rin"}, {ID: "char-pip", Name: "Pip"}}} {
			in := ScriptInput{Mode: "premise", Language: lang, ChapterNumber: 3, PagesTarget: 8, Characters: characters}
			doc := placeholderDoc(t, in)
			if !scriptOf(lang, doc.ChapterSummary) || !scriptOf(lang, doc.Scenes[0].Title) {
				t.Fatalf("%s: template summary/title not localised: %q %q", lang, doc.ChapterSummary, doc.Scenes[0].Title)
			}
			kinds := map[string]bool{}
			for _, p := range doc.Scenes[0].Panels {
				if len(p.Dialogue) < 1 || len(p.Dialogue) > 2 {
					t.Fatalf("%s: panel %s has %d lines", lang, p.Key, len(p.Dialogue))
				}
				if !scriptOf(lang, p.Action) || !scriptOf(lang, p.Notes) {
					t.Fatalf("%s: panel text not localised: %q", lang, p.Action)
				}
				for _, d := range p.Dialogue {
					kinds[d.Kind] = true
					if !scriptOf(lang, d.Text) {
						t.Fatalf("%s: dialogue %q is not in the project language", lang, d.Text)
					}
					if d.Kind == "caption" && (d.Speaker != "" || d.CharacterID != "") {
						t.Fatalf("%s: caption has a speaker", lang)
					}
					if len(characters) == 0 && d.CharacterID != "" {
						t.Fatalf("%s: invented a character", lang)
					}
					if d.CharacterID != "" {
						found := false
						for _, id := range p.CharacterIDs {
							found = found || id == d.CharacterID
						}
						if !found {
							t.Fatalf("%s: speaker missing from panel characters", lang)
						}
					}
				}
				if len(characters) == 2 && strings.Contains(strings.Join(dialogueTexts(p), " "), "Rin") {
					seen[lang] = "named"
				}
			}
			for _, kind := range []string{"speech", "thought", "caption"} {
				if !kinds[kind] {
					t.Fatalf("%s: no %s lines", lang, kind)
				}
			}
		}
		if seen[lang] != "named" {
			t.Fatalf("%s: character names are never used in dialogue", lang)
		}
	}
	// Each language has its own copy; regional tags normalise.
	if a, b := placeholderDoc(t, ScriptInput{Mode: "premise", Language: "zh-TW", PagesTarget: 1}), placeholderDoc(t, ScriptInput{Mode: "premise", Language: "zh-Hant", PagesTarget: 1}); a.ChapterSummary != b.ChapterSummary {
		t.Fatal("zh-TW did not use the Traditional Chinese template")
	}
	if doc := placeholderDoc(t, ScriptInput{Mode: "premise", Language: "x-unknown", PagesTarget: 1}); doc.ChapterSummary != placeholderCopies["en"].summary {
		t.Fatal("unknown languages fall back to English")
	}
	// A premise always wins over the template summary.
	if doc := placeholderDoc(t, ScriptInput{Mode: "premise", Premise: "灯台守の物語。", Language: "ja", PagesTarget: 1}); doc.ChapterSummary != "灯台守の物語。" {
		t.Fatal("premise ignored")
	}
}

func dialogueTexts(p scriptPanel) []string {
	out := make([]string, len(p.Dialogue))
	for i, d := range p.Dialogue {
		out[i] = d.Text
	}
	return out
}
