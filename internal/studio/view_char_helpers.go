package studio

import (
	"context"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func charDesc(c Character) string {
	parts := []string{}
	if c.Age != "" {
		parts = append(parts, c.Age+".")
	}
	if c.Personality != "" {
		parts = append(parts, c.Personality)
	}
	return clip(strings.Join(parts, " "), 160)
}

func charAbout(c Character) string {
	parts := []string{}
	if c.Age != "" {
		parts = append(parts, c.Age+".")
	}
	for _, s := range []string{c.Personality, c.Look, c.SpeechStyle} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

func scoreOf(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func lockBarTone(v *float64) string {
	if v != nil && *v < DriftThreshold {
		return "warning"
	}
	return "success"
}

type refSlot struct{ Label, URL string }

// refSlots fills the turnaround (front, side, back, 3/4 face) and four expression slots.
func refSlots(ctx context.Context, refs []CharacterRef, urls map[string]string) []refSlot {
	slots := []refSlot{{Label: "FRONT"}, {Label: "SIDE"}, {Label: "BACK"}, {Label: "3/4"}}
	idx := map[string]int{RefFront: 0, RefSide: 1, RefBack: 2, RefFace: 3}
	var expressions []refSlot
	for _, r := range refs {
		if i, ok := idx[r.Kind]; ok && slots[i].URL == "" {
			slots[i].URL = urls[r.ID]
			if r.Label != "" {
				slots[i].Label = strings.ToUpper(r.Label)
			}
			continue
		}
		if r.Kind == RefExpression {
			l := strings.ToUpper(r.Label)
			if l == "" {
				l = "EXPRESSION"
			}
			expressions = append(expressions, refSlot{Label: l, URL: urls[r.ID]})
		}
	}
	defaults := []string{"JOY", "ANGER", "FEAR", "SMUG"}
	for i := 0; i < 4; i++ {
		if i < len(expressions) {
			slots = append(slots, expressions[i])
		} else {
			slots = append(slots, refSlot{Label: defaults[i]})
		}
	}
	return slots
}

func refLabel(ctx context.Context, r CharacterRef) string {
	k := i18n.T(ctx, "studio.ref."+r.Kind)
	if r.Label != "" {
		return k + " · " + r.Label
	}
	return k
}

func refKindOptions(ctx context.Context) []ui.Option {
	out := make([]ui.Option, 0, len(RefKinds))
	for _, k := range RefKinds {
		out = append(out, ui.Option{Value: k, Label: i18n.T(ctx, "studio.ref."+k)})
	}
	return out
}

func joinDot(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "–"
	}
	return s
}

func voiceLine(ctx context.Context, v Voice) string {
	switch {
	case v.Description != "" && v.Preset != "":
		return v.Description + " · " + i18n.T(ctx, "studio.character.voice_preset", "name", v.Preset)
	case v.Description != "":
		return v.Description
	case v.Preset != "":
		return i18n.T(ctx, "studio.character.voice_preset", "name", v.Preset)
	}
	return "–"
}

func outfitLabel(ctx context.Context, o Outfit) string {
	parts := []string{strings.ToUpper(o.Name)}
	if o.IsDefault {
		parts = append(parts, strings.ToUpper(i18n.T(ctx, "studio.character.default")))
	} else if o.Chapters != "" {
		parts = append(parts, "CH. "+o.Chapters)
	}
	return strings.Join(parts, " · ")
}

func otherOptions(list []Character) []ui.Option {
	out := make([]ui.Option, len(list))
	for i, c := range list {
		out[i] = ui.Option{Value: c.ID, Label: c.Name}
	}
	return out
}

func lockDesc(ctx context.Context, v CharacterView) string {
	info := v.Card.LockInfo.V
	model := info.Model
	if model == "" {
		model = "–"
	}
	s := i18n.T(ctx, "studio.lock.desc", "count", info.Refs, "model", model)
	if v.Stats.ScoredPanels > 0 {
		s += " " + i18n.T(ctx, "studio.lock.desc_avg", "count", v.Stats.ScoredPanels)
	}
	if info.Method != "" {
		s += " (" + info.Method + ")"
	}
	return s
}

func barHeight(score float64) int {
	// Scores live between ~0.7 and 1; stretch that range for readability.
	h := int((score - 0.6) / 0.4 * 100)
	return max(12, min(100, h))
}

func editInput(v CharacterView, f *characterForm) CharacterInput {
	if f != nil {
		return f.In
	}
	c := v.Card.Character
	return CharacterInput{Name: c.Name, Kanji: c.Kanji, Role: c.Role, RoleLine: c.RoleLine, Age: c.Age, Look: c.Look, Personality: c.Personality,
		SpeechStyle: c.SpeechStyle, Backstory: c.Backstory, Voice: c.Voice.V, Category: c.Category, Style: c.Style}
}

func editErrors(f *characterForm) map[string]string {
	if f == nil {
		return map[string]string{}
	}
	return f.Errors
}

func roleOptions(ctx context.Context) []ui.Option {
	out := make([]ui.Option, 0, len(CharacterRoles))
	for _, r := range CharacterRoles {
		out = append(out, ui.Option{Value: r, Label: i18n.T(ctx, "studio.role_word."+r)})
	}
	return out
}

func categoryOptions(ctx context.Context) []ui.Option {
	out := make([]ui.Option, 0, len(Categories))
	for _, c := range Categories {
		out = append(out, ui.Option{Value: c, Label: i18n.T(ctx, "studio.category."+c)})
	}
	return out
}

func createHref(v CreateView, mode string) string {
	u := "/studio/p/" + v.Project.ID + "/characters/new?mode=" + mode
	if v.Character != nil {
		u += "&character=" + v.Character.ID
	}
	return u
}

func createAction(v CreateView) string {
	if v.Character != nil && v.Mode == "describe" {
		return "/studio/p/" + v.Project.ID + "/characters/" + v.Character.ID + "/sheets"
	}
	if v.Character != nil && v.Mode == "upload" {
		return "/studio/p/" + v.Project.ID + "/characters/" + v.Character.ID + "/refs"
	}
	return "/studio/p/" + v.Project.ID + "/characters/new?mode=" + v.Mode
}

func styleValue(s string) string {
	switch s {
	case "colour", "chibi":
		return s
	}
	return "series"
}

// sheetProgress spreads a job's progress over its four sheets (sheet i fills after sheet i−1).
func sheetProgress(p float64, i int) float64 {
	v := p*4 - float64(i-1)
	return max(0, min(1, v))
}
