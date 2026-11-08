package publish

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// maxAltText bounds a panel's alt-text.
const maxAltText = 1000

// altSource is what a panel's alt-text is drafted from: its script panel and
// scene, the characters it shows and its lettering in reading order.
type altSource struct {
	Action     string
	Setting    string // scene title or summary
	TimeOfDay  string
	Characters []string
	Bubbles    []GuidedBubble
}

// draftAltText writes a panel description deterministically, with no model
// call (ADR 0012 §5): action, setting, characters, then each lettered line in
// reading order. The connecting words follow the publication language.
func draftAltText(ctx context.Context, lang string, src altSource) string {
	ctx = i18n.WithLocale(ctx, lang)
	var parts []string
	if a := sentence(src.Action); a != "" {
		parts = append(parts, a)
	}
	switch place, tod := strings.TrimSpace(src.Setting), strings.TrimSpace(src.TimeOfDay); {
	case place != "" && tod != "":
		parts = append(parts, i18n.T(ctx, "publish.alt.setting_time", "place", place, "time", tod))
	case place != "":
		parts = append(parts, i18n.T(ctx, "publish.alt.setting", "place", place))
	}
	if len(src.Characters) > 0 {
		parts = append(parts, i18n.T(ctx, "publish.alt.characters", "names", strings.Join(src.Characters, i18n.T(ctx, "publish.alt.list_sep"))))
	}
	for _, b := range src.Bubbles {
		text := strings.Join(strings.Fields(b.Text), " ")
		if text == "" {
			continue
		}
		switch {
		case b.Kind == "sfx":
			parts = append(parts, i18n.T(ctx, "publish.alt.sfx", "text", text))
		case b.Kind == "caption":
			parts = append(parts, i18n.T(ctx, "publish.alt.caption", "text", text))
		case b.Speaker != "":
			parts = append(parts, i18n.T(ctx, "publish.alt.line", "speaker", b.Speaker, "text", text))
		default:
			parts = append(parts, i18n.T(ctx, "publish.alt.line_unknown", "text", text))
		}
	}
	return clip(strings.Join(parts, " "), maxAltText)
}

// sentence trims s and ends it with a full stop unless it already ends a sentence.
func sentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	if strings.ContainsRune(".!?…。！？」』”\"", r) {
		return s
	}
	return s + "."
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:max-1])) + "…"
}

// scriptIndex finds script panels and their scenes by script panel ID.
type scriptIndex map[string]struct {
	scene studio.ScriptScene
	panel studio.ScriptPanel
}

func newScriptIndex(doc studio.ScriptDoc) scriptIndex {
	idx := scriptIndex{}
	for _, sc := range doc.Scenes {
		for _, p := range sc.Panels {
			if p.ID != "" {
				idx[p.ID] = struct {
					scene studio.ScriptScene
					panel studio.ScriptPanel
				}{sc, p}
			}
		}
	}
	return idx
}
