package gen

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Image prompt construction (ADR 0009). Prompts are deterministic: the same
// panel, script, style and cast always produce the same text. They describe
// original art only, keep a creator-written prompt as the primary
// description, and always end with the no-lettering rules because lettering
// is structured text on its own layer.

const (
	// imagePromptBytes bounds a component prompt. It is below
	// pagedoc.MaxPromptRunes and leaves room for the route's own additions
	// within the ADR 0009 6,144-byte request bound.
	imagePromptBytes = 4000
	negativeBytes    = 900
)

// Fixed instructions shared by every art request.
const (
	promptRules = "Rules: original artwork only; no text, letters, numbers, speech bubbles, captions, sound-effect lettering, signatures, watermarks or logos; no panel borders, frames or gutters; one single image, not a comic page; non-explicit, no nudity, no gore."
	negativeArt = "text, letters, words, numbers, speech bubbles, word balloons, captions, sound effect lettering, signature, watermark, logo, panel borders, frames, gutters, multiple panels, comic page layout, nudity, gore, blurry, low quality, deformed hands, extra fingers, extra limbs"
	// legacyNegative is the avoid list of payloads queued before ADR 0009.
	legacyNegative = "Lettering, speech balloons, captions, text. Other layer content."
)

// sceneContext is what the script and the panel say about one panel.
type sceneContext struct {
	// Description is the primary description: the creator's prompt when one
	// exists, otherwise the script panel's action.
	Description  string
	UserAuthored bool
	Action       string
	Emotion      string
	Notes        string
	Camera       string
	Shot         string
	Setting      string
	TimeOfDay    string
}

// castMember is one character's visual bible entry.
type castMember struct {
	Name, Look, Outfit string
}

type styleSpec struct {
	Lines string
	BW    bool
}

// styleFor maps the project's style key (the new-project wizard's choices,
// or the bw_screentone default) to drawing instructions. Any other value is
// a creator's free-text style and is quoted after the default B/W manga look.
func styleFor(style string) styleSpec {
	switch strings.TrimSpace(style) {
	case "seinen_bw", "seinen":
		return styleSpec{"Seinen manga, black and white: fine detailed ink linework, grounded realistic proportions and faces, grey screentone shading, crosshatching in the shadows, spotted solid blacks.", true}
	case "shonen_ink", "shonen":
		return styleSpec{"Shonen manga, black and white: bold confident ink lines, heavy solid blacks, high-contrast screentone, energetic poses, impact frames for big moments.", true}
	case "shojo_soft", "shoujo", "shojo":
		return styleSpec{"Shojo manga, black and white: light delicate linework, soft gradient screentone, sparkle and flower tones for mood, large expressive eyes.", true}
	case "webtoon_color", "color_webtoon":
		return styleSpec{"Full-colour webtoon illustration: clean lineart, flat cel shading with soft highlights, a harmonious limited palette.", false}
	case "chibi":
		return styleSpec{"Chibi manga, black and white: rounded super-deformed proportions, simple clean lines, light screentone, comic expressiveness.", true}
	case "sketch":
		return styleSpec{"Rough pencil storyboard sketch, black and white: loose construction lines and quick hatching, no inked polish.", true}
	case "", "bw_screentone", "bw":
		return styleSpec{"Black-and-white manga: clean ink linework, screentone shading, solid blacks, high contrast.", true}
	}
	return styleSpec{"Black-and-white manga: clean ink linework, screentone shading, solid blacks, high contrast. Project style: " + clip(style, 80) + ".", true}
}

func (s styleSpec) palette() string {
	if s.BW {
		return " Black ink and grey screentone only, no colour."
	}
	return ""
}

// clip collapses whitespace and control characters into single spaces and
// cuts the result to at most n bytes on a rune boundary.
func clip(v string, n int) string {
	var b strings.Builder
	space := false
	for _, r := range v {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) <= n {
		return out
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return strings.TrimSpace(out[:cut])
}

func sentence(label, v string, n int) string {
	v = strings.TrimRight(clip(v, n), ".")
	if v == "" {
		return ""
	}
	return label + v + "."
}

var motionWords = []string{"run", "dash", "leap", "jump", "punch", "kick", "strike", "slash", "swing", "fall", "charge", "rush", "chase", "fly", "explod", "crash", "attack", "sprint", "spin", "throw", "hit", "impact", "burst", "lunge", "dodge", "race", "speed", "fight", "clash", "blast", "tackle"}

// hasMotion reports whether the panel describes movement, which decides if
// speed lines belong in the art.
func hasMotion(scene sceneContext) bool {
	text := strings.ToLower(scene.Description + " " + scene.Action + " " + scene.Emotion + " " + scene.Notes)
	for _, w := range motionWords {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// layerPrompt builds the prompt for one independent panel layer.
func layerPrompt(style string, scene sceneContext, kind string, focus castMember) string {
	spec := styleFor(style)
	head := []string{"Original manga panel artwork. " + spec.Lines + spec.palette()}
	details := []string{}
	if scene.UserAuthored {
		details = append(details, sentence("Script context: ", strings.Join(nonEmpty(scene.Action, scene.Notes), ". "), 400))
	}
	details = append(details,
		sentence("Mood: ", scene.Emotion, 160),
		sentence("Setting: ", scene.Setting, 300),
		sentence("Time of day: ", scene.TimeOfDay, 40),
		sentence("Camera: ", strings.Join(nonEmpty(scene.Camera, scene.Shot), ", "), 200))
	composition := "Composition: dynamic manga composition with a strong diagonal and a clear focal point"
	if hasMotion(scene) {
		composition += "; speed lines and motion streaks where the action moves"
	}
	details = append(details, composition+".")
	switch kind {
	case pagedoc.LayerCharacter:
		details = append(details, sentence("Character: ", focus.Name, 80), sentence("Appearance: ", focus.Look, 400), sentence("Outfit: ", focus.Outfit, 200),
			"Layer: draw only this one character, a full pose framed for the shot, with the expression the scene calls for, isolated on a fully transparent background. No setting, no floor or cast shadow, no other characters, no effects.")
	case pagedoc.LayerFX:
		details = append(details, "Layer: effects only, ink speed lines, impact bursts, motion streaks or atmospheric tone that fit the action, on a fully transparent background. No characters, no setting.")
	default:
		details = append(details, "Layer: background plate only. Draw the setting and environment with no people, no characters and no figures, filling the whole frame edge to edge on an opaque background; leave natural space where the characters will stand.")
	}
	label := "Scene: "
	if kind != pagedoc.LayerCharacter {
		label = "Scene context (the characters are drawn on separate layers): "
	}
	return assemblePrompt(head, label, scene.Description, details)
}

// assemblePrompt places the primary description after the head and gives it
// whatever byte budget the fixed parts leave, so the rules always survive.
func assemblePrompt(head []string, label, description string, details []string) string {
	fixed := append(append([]string{}, head...), details...)
	fixed = append(fixed, promptRules)
	parts := nonEmpty(fixed...)
	used := len(strings.Join(parts, "\n")) + 1 + len(label) + 1
	desc := sentence(label, description, max(0, imagePromptBytes-used))
	if desc == "" {
		return clip(strings.Join(parts, "\n"), imagePromptBytes)
	}
	out := append(nonEmpty(head...), desc)
	out = append(out, nonEmpty(details...)...)
	return strings.Join(append(out, promptRules), "\n")
}

func nonEmpty(values ...string) []string {
	out := []string{}
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

// layerNegative is the avoid list for one layer kind.
func layerNegative(style, kind string) string {
	n := negativeArt
	if styleFor(style).BW {
		n += ", colour, color"
	}
	switch kind {
	case pagedoc.LayerCharacter:
		n += ", background scenery, room, landscape, floor, other characters, crowd"
	case pagedoc.LayerFX:
		n += ", characters, people, faces, scenery, buildings"
	default:
		n += ", people, characters, figures, faces, crowd"
	}
	return clip(n, negativeBytes)
}

// layerBackground is the alpha behaviour a layer kind needs.
func layerBackground(kind string) string {
	if kind == pagedoc.LayerCharacter || kind == pagedoc.LayerFX {
		return "transparent"
	}
	return "opaque"
}

func defaultOutfit(c studio.Character) string {
	for _, o := range c.Outfits {
		if o.IsDefault {
			return strings.Join(nonEmpty(o.Name, o.Description), ": ")
		}
	}
	return ""
}

// sheetPrompt builds a character reference (model) sheet prompt.
func sheetPrompt(style string, c studio.Character) string {
	spec := styleFor(style)
	head := []string{"Original character reference sheet (model sheet) for a manga. " + spec.Lines + spec.palette()}
	details := []string{
		sentence("Appearance: ", c.Look, 600),
		sentence("Default outfit: ", defaultOutfit(c), 240),
		sentence("Personality to show through posture and expressions: ", c.Personality, 300),
		"Layout: this one character only, drawn as a full-body turnaround (front, three-quarter, side and back views) standing in a relaxed neutral pose and evenly spaced in a row, plus a row of head close-ups with neutral, happy, angry and surprised expressions. Identical proportions, hairstyle, costume and accessories in every view. Plain opaque white background, no scenery, no props.",
	}
	return assemblePrompt(head, "Character: ", c.Name, details)
}

func sheetNegative(style string) string {
	n := negativeArt + ", labels, arrows, colour swatches, background scenery, props, multiple different characters"
	if styleFor(style).BW {
		n += ", colour, color"
	}
	return clip(n, negativeBytes)
}

// regionEditPrompt keeps the creator's edit instruction primary and asks for
// a seamless, mask-limited change. The server restores every pixel outside
// the mask regardless of what the model returns.
func regionEditPrompt(style, kind, instruction string) string {
	spec := styleFor(style)
	head := []string{"Edit only the masked region of this original manga " + layerNoun(kind) + "."}
	details := []string{
		"Match the surrounding ink linework, line weight, screentone and lighting exactly so the edit is seamless. " + spec.Lines + spec.palette(),
		"Keep everything outside the mask unchanged.",
	}
	if layerBackground(kind) == "transparent" {
		details = append(details, "Keep the transparent background transparent.")
	}
	return assemblePrompt(head, "Change: ", instruction, details)
}

func layerNoun(kind string) string {
	switch kind {
	case pagedoc.LayerCharacter:
		return "character layer"
	case pagedoc.LayerFX:
		return "effects layer"
	case pagedoc.LayerBackground:
		return "background layer"
	}
	return "panel layer"
}

// orderedRefs puts a character's references in the order a route should use
// them when it can carry only a few: kept sheets first, then front, face,
// side and any others, each group in its stored order.
func orderedRefs(refs []studio.CharacterRef) []studio.CharacterRef {
	rank := map[string]int{"sheet": 0, "front": 1, "face": 2, "side": 3, "expression": 4, "outfit": 5, "back": 6}
	out := append([]studio.CharacterRef(nil), refs...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, ok := rank[out[i].Kind]
		if !ok {
			ri = 7
		}
		rj, ok := rank[out[j].Kind]
		if !ok {
			rj = 7
		}
		if ri != rj {
			return ri < rj
		}
		return out[i].Order < out[j].Order
	})
	return out
}

// findScriptPanel finds a panel's script panel and scene, if the chapter still
// has them.
func findScriptPanel(doc studio.ScriptDoc, scriptPanelID string) (studio.ScriptScene, studio.ScriptPanel, bool) {
	if scriptPanelID == "" {
		return studio.ScriptScene{}, studio.ScriptPanel{}, false
	}
	for _, scene := range doc.Scenes {
		for _, p := range scene.Panels {
			if p.ID == scriptPanelID {
				return scene, p, true
			}
		}
	}
	return studio.ScriptScene{}, studio.ScriptPanel{}, false
}

// panelScene combines the stored panel and its script panel. A creator- or
// agent-written prompt (or a request override) stays the primary
// description; a script prompt is replaced by the structured script fields.
func panelScene(pn pagedoc.Panel, override string, doc studio.ScriptDoc, haveDoc bool) sceneContext {
	sc := sceneContext{Camera: pn.Camera, Shot: pn.Shot}
	var scene studio.ScriptScene
	var sp studio.ScriptPanel
	found := false
	if haveDoc {
		scene, sp, found = findScriptPanel(doc, pn.ScriptPanelID)
	}
	if found {
		sc.Action, sc.Emotion, sc.Notes = sp.Action, sp.Emotion, sp.Notes
		sc.Setting = strings.Join(nonEmpty(scene.Title, scene.Summary), ": ")
		sc.TimeOfDay = scene.TimeOfDay
		if sc.Camera == "" {
			sc.Camera = sp.Camera
		}
		if sc.Shot == "" {
			sc.Shot = sp.Shot
		}
	}
	switch {
	case strings.TrimSpace(override) != "":
		sc.Description, sc.UserAuthored = override, true
	case pn.PromptSource != "script" && strings.TrimSpace(pn.Prompt) != "":
		sc.Description, sc.UserAuthored = pn.Prompt, true
	case found:
		sc.Description = strings.Join(nonEmpty(sp.Action, sp.Notes), ". ")
		sc.Notes = ""
	default:
		sc.Description = pn.Prompt
	}
	if strings.TrimSpace(sc.Description) == "" {
		sc.Description = "An original manga scene"
	}
	return sc
}
