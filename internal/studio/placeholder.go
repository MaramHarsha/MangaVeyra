package studio

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// The built-in placeholder generators let every studio flow work with no
// model installed. They are deterministic (the same input gives the same
// output), write only original text, and label their images as placeholders.
// The gen module replaces the job handlers that use them.

// OutlineInput is the input of a script.outline job.
type OutlineInput struct {
	Title           string   `json:"title,omitempty"`
	Premise         string   `json:"premise"`
	Genres          []string `json:"genres"`
	Style           string   `json:"style"`
	Format          string   `json:"format"`
	Chapters        int      `json:"chapters"`
	PagesPerChapter string   `json:"pages_per_chapter"`
	Lang            string   `json:"lang"`
	// Structure is the story template to follow (arcs with chapter counts), optional.
	Structure []StoryArcSpec `json:"structure,omitempty"`
	ProjectID string         `json:"project_id,omitempty"`
	Actor     string         `json:"actor,omitempty"`
}

func seedFor(parts ...string) *rand.Rand {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	s := h.Sum64()
	return rand.New(rand.NewPCG(s, s^0x9e3779b97f4a7c15))
}

var stopWords = map[string]bool{"about": true, "after": true, "again": true, "against": true, "where": true, "which": true, "while": true,
	"their": true, "there": true, "these": true, "those": true, "every": true, "other": true, "under": true, "night": false, "could": true,
	"would": true, "should": true, "cannot": true, "can't": true, "nobody": true, "someone": true, "something": true, "because": true,
	"before": true, "across": true, "through": true, "being": true, "never": true, "always": true, "story": true, "built": true,
	"read": true, "reads": true, "carries": true, "carry": true, "deliver": true, "delivers": true, "floods": true, "steals": true,
	"owes": true, "paid": true, "pays": true, "makes": true, "finds": true, "wants": true, "tries": true, "only": true, "once": true,
	"with": true, "from": true, "into": true, "over": true, "that": true, "this": true, "they": true, "them": true, "have": true,
	"will": true, "must": true, "else": true, "nobody's": true, "everyone": true, "anyone": true, "until": true, "when": true}

var wordRe = regexp.MustCompile(`[\p{L}']+`)

// motifs picks the distinctive words of a premise ("courier", "letters", "salt").
func motifs(premise string) []string {
	var out []string
	for _, w := range wordRe.FindAllString(premise, -1) {
		l := strings.ToLower(strings.Trim(w, "'"))
		if len([]rune(l)) < 4 || stopWords[l] || slices.Contains(out, l) {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		out = []string{"harbour", "lantern", "signal", "tide"}
	}
	return out
}

var smallWords = map[string]bool{"a": true, "an": true, "the": true, "of": true, "and": true, "at": true, "in": true, "on": true, "to": true}

func title(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		r := []rune(w)
		if len(r) > 0 && (i == 0 || !smallWords[strings.ToLower(w)]) {
			r[0] = unicode.ToUpper(r[0])
		}
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

func pick[T any](r *rand.Rand, list []T) T { return list[r.IntN(len(list))] }

var (
	firstNames = []string{"Mika", "Suzu", "Enzo", "Noor", "Ada", "Hollis", "Juno", "Tobi", "Yara", "Cato", "Lio", "Isla", "Remy", "Kenji",
		"Asha", "Oren", "Talia", "Bram", "Nell", "Ikaika", "Saoirse", "Dario", "Emeka", "Pim"}
	lastNames = []string{"Kurose", "Morrow", "Okafor", "Vale", "Ishikawa", "Calder", "Haldane", "Tsubame", "Ferro", "Linde", "Achebe",
		"Moriyama", "Sato", "Quill", "Brightwater", "Nakase", "Duarte", "Hale"}
	arcNouns     = []string{"Road", "Debt", "Tide", "Bargain", "Signal", "Storm", "Promise", "Ledger", "Crossing", "Harbour", "Lantern", "Echo"}
	chapterNouns = []string{"Paper Crows", "Low Water", "The Last Ferry", "Borrowed Keys", "Salt and Smoke", "The Quiet Bell", "Night Market",
		"A Map of Nothing", "Rooftops", "The Keeper", "Glass Rain", "Undertow", "Stolen Hour", "The Long Stair", "Ink on the Sails",
		"Tin Lanterns", "First Light", "Hollow Pier"}
	places   = []string{"rooftops", "harbour", "night market", "old station", "lighthouse", "canal steps", "guild hall", "tide gates", "ferry deck", "archive"}
	times    = []string{"dawn", "morning", "noon", "dusk", "night", "midnight"}
	cameras  = []string{"wide", "medium", "close", "low", "high", "over-shoulder", "extreme close", "wide"}
	emotions = []string{"Tense", "Annoyed", "Reckless joy", "Awe", "Disbelief", "Calm", "Wary", "Hopeful", "Stubborn", "Shaken", "Amused", "Determined"}
	sfxWords = []string{"WHOOSH", "THUD", "KRAK", "FWIP", "DOON", "TAP TAP", "SPLASH", "CLANG", "ZAAA", "BA-DUM"}
)

// PlaceholderScript writes an original, well-formed ScriptDraft from a
// premise: an arc outline with chapter summaries, a three-scene script for
// chapter 1, four to five character drafts and setting items.
func PlaceholderScript(in OutlineInput) ScriptDraft {
	r := seedFor(in.Premise, in.Title, strings.Join(in.Genres, ","), in.Style)
	m := motifs(in.Premise)
	motif := func(i int) string { return m[i%len(m)] }
	chapters := in.Chapters
	if chapters <= 0 {
		chapters = 12
	}
	chapters = min(chapters, 60)

	// Characters.
	roles := []struct{ role, line, age string }{{"lead", "Lead", ""}, {"rival", "Rival", ""}, {"mentor", "Mentor", ""}, {"antagonist", "Antagonist", ""}}
	if chapters > 6 {
		roles = append(roles, struct{ role, line, age string }{"sidekick", "Sidekick", ""})
	}
	usedFirst := map[string]bool{}
	var chars []DraftCharacter
	for i, ro := range roles {
		first := pick(r, firstNames)
		for usedFirst[first] {
			first = pick(r, firstNames)
		}
		usedFirst[first] = true
		name := first + " " + pick(r, lastNames)
		age := strconv.Itoa(16 + r.IntN(10) + i*4)
		chars = append(chars, DraftCharacter{
			Name: name, Role: ro.role, RoleLine: ro.line, Age: age,
			Look:        fmt.Sprintf("%s, %s, a %s that has seen the %s too often.", pick(r, []string{"Tall", "Wiry", "Small and quick", "Broad-shouldered", "Lanky"}), pick(r, []string{"cropped dark hair", "a silver bob", "braided hair", "a shaved head", "wind-knotted curls"}), pick(r, []string{"long coat", "patched jacket", "courier satchel", "brass-buttoned vest"}), motif(i)),
			Personality: pick(r, []string{"Reckless, loyal, hates being told no.", "Punctual to a fault, secretly kind.", "Keeps a ledger of every favour.", "Laughs at danger, cries at goodbyes.", "Says little and notices everything."}),
			SpeechStyle: pick(r, []string{"Short sentences, never says sorry.", "Formal, clipped, a little sarcastic.", "Rambles when nervous.", "Speaks in questions.", "Quiet, then suddenly loud."}),
		})
	}
	lead, rival, mentor := strings.Fields(chars[0].Name)[0], strings.Fields(chars[1].Name)[0], strings.Fields(chars[2].Name)[0]

	// Settings.
	var settings []DraftSetting
	locs := []string{}
	start := r.IntN(len(places))
	for i := 0; i < 3; i++ {
		loc := title(places[(start+i*3)%len(places)])
		if i == 0 {
			loc = title(motif(0) + " " + places[start])
		}
		locs = append(locs, loc)
		settings = append(settings, DraftSetting{Kind: SettingLocation, Name: loc, Notes: "Where the story keeps returning; tracked across scenes."})
	}
	settings = append(settings, DraftSetting{Kind: SettingProp, Name: title(pick(r, []string{"brass pocket watch", "sealed letter", "tin lantern", "folded map", "ledger"})), Notes: "Recurring prop; keep its look consistent."})

	// Arcs.
	var structure []StoryArcSpec
	if len(in.Structure) > 0 {
		structure = in.Structure
	} else {
		arcs := min(4, max(1, (chapters+3)/4))
		per := chapters / arcs
		for i := 0; i < arcs; i++ {
			n := per
			if i == arcs-1 {
				n = chapters - per*(arcs-1)
			}
			structure = append(structure, StoryArcSpec{Chapters: n})
		}
	}
	chapterTitles := append([]string(nil), chapterNouns...)
	r.Shuffle(len(chapterTitles), func(i, j int) { chapterTitles[i], chapterTitles[j] = chapterTitles[j], chapterTitles[i] })
	var arcs []DraftArc
	num := 1
	for i, sa := range structure {
		t := sa.Title
		if t == "" {
			t = "The " + title(motif(i)) + " " + arcNouns[(i*5+r.IntN(3))%len(arcNouns)]
		}
		summary := sa.Summary
		if summary == "" {
			summary = fmt.Sprintf("%s is pulled deeper into the %s while %s keeps score.", lead, motif(i), rival)
		}
		a := DraftArc{Title: t, Summary: summary}
		for c := 0; c < sa.Chapters; c++ {
			ct := chapterTitles[(num-1)%len(chapterTitles)]
			beat := ""
			if len(sa.Beats) > 0 {
				beat = sa.Beats[c%len(sa.Beats)] + ": "
			}
			a.Chapters = append(a.Chapters, DraftChapter{Number: num, Title: ct,
				Summary: beat + chapterSummary(r, num, lead, rival, mentor, motif(num), locs[num%len(locs)])})
			num++
		}
		arcs = append(arcs, a)
	}

	// Chapter 1 script: three scenes.
	caption := strings.TrimSpace(in.Premise)
	if i := strings.IndexAny(caption, ".!?"); i > 0 {
		caption = caption[:i+1]
	}
	caption = clip(caption, 140)
	sceneTitles := []string{title(locs[0]) + " at " + times[3], "Chase across the " + strings.ToLower(locs[1]), "The " + strings.ToLower(title(motif(2))) + " keeper"}
	page, onPage := 1, 0
	var scenes []DraftScene
	for si := 0; si < 3; si++ {
		sc := DraftScene{Title: sceneTitles[si], TimeOfDay: times[(3+si)%len(times)], Location: locs[si%len(locs)],
			Prose: fmt.Sprintf("%s. %s moves through the %s while the %s gathers. %s is already there, pretending not to wait.",
				sceneTitles[si], lead, strings.ToLower(locs[si%len(locs)]), motif(si), rival)}
		n := 4 + r.IntN(2)
		for pi := 0; pi < n; pi++ {
			if onPage == 4 {
				page, onPage = page+1, 0
			}
			onPage++
			p := DraftPanel{Label: fmt.Sprintf("%d.%d", page, onPage), Camera: cameras[(si*3+pi)%len(cameras)], Emotion: pick(r, emotions)}
			switch {
			case si == 0 && pi == 0:
				p.Action = fmt.Sprintf("%s at %s; the %s catches the last light.", title(locs[0]), times[3], motif(0))
				if caption != "" {
					p.Dialogue = []DraftLine{{Kind: LineCaption, Text: caption}}
				}
			case pi%3 == 1:
				p.Action = fmt.Sprintf("%s checks the %s, then the horizon.", rival, motif(pi))
				p.Dialogue = []DraftLine{{Speaker: rival, Kind: LineSpeech, Text: pick(r, []string{"You're late. Again.", "We don't have time for this.", "Tell me you have a plan.", "Of course it's you."})}}
			case pi%3 == 2:
				p.Action = fmt.Sprintf("%s vaults over a railing, coat flaring.", lead)
				p.Dialogue = []DraftLine{{Speaker: lead, Kind: LineShout, Text: pick(r, []string{"Then I'll take the roof!", "Keep up!", "Trust me on this one!", "Don't wait for me!"})}}
				p.SFX = []string{pick(r, sfxWords)}
			default:
				p.Action = fmt.Sprintf("%s watches from the doorway, %s in hand.", mentor, motif(pi+1))
				p.Dialogue = []DraftLine{{Speaker: mentor, Kind: LineSpeech, Text: pick(r, []string{"Every debt comes due at high tide.", "You carry more than letters, child.", "The city remembers.", "Not yet. Soon."})}}
			}
			sc.Panels = append(sc.Panels, p)
		}
		scenes = append(scenes, sc)
	}
	return ScriptDraft{Arcs: arcs, Chapter1: DraftScript{Scenes: scenes}, Characters: chars, Settings: settings}
}

func chapterSummary(r *rand.Rand, n int, lead, rival, mentor, motif, place string) string {
	templates := []string{
		"%[1]s takes a job nobody else will touch; %[2]s is waiting at the %[5]s.",
		"A %[4]s goes missing and %[1]s is blamed. %[3]s knows more than they say.",
		"%[2]s and %[1]s are forced to work together across the %[5]s.",
		"The truth about the %[4]s surfaces; %[3]s asks %[1]s for a favour.",
		"Chase through the %[5]s at night; %[1]s loses something important.",
		"%[1]s pays an old debt and makes a new enemy.",
		"A quiet chapter: %[1]s and %[2]s talk until dawn at the %[5]s.",
		"Everything the %[4]s was hiding comes due at once.",
	}
	return fmt.Sprintf(templates[(n+r.IntN(3))%len(templates)], lead, rival, mentor, motif, strings.ToLower(place))
}

// AssistInput is the input of a script.assist job.
type AssistInput struct {
	ChapterID string `json:"chapter_id"`
	SceneID   string `json:"scene_id"`
	Request   string `json:"request"`
	AssistID  string `json:"assist_id,omitempty"`
	Actor     string `json:"actor,omitempty"`
}

var labelRe = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,2})\b`)

// PlaceholderAssist proposes a ScriptDiff for a request about a scene's
// panels using simple rules: it finds the panel named in the request ("7.3")
// or the scene's last panel, then adds a beat before it ("add", "beat",
// "before"), removes it ("cut", "remove", "delete"), makes it a splash
// ("splash", "full-width") or tightens its action.
func PlaceholderAssist(req string, scene []ScriptPanel, format func(key string, args ...any) string) ScriptDiff {
	d := ScriptDiff{Ops: []ScriptOp{}}
	if len(scene) == 0 {
		d.Summary = format("studio.assist.ph.empty")
		d.Affects = format("studio.assist.ph.affects_none")
		return d
	}
	target := scene[len(scene)-1]
	if m := labelRe.FindString(req); m != "" {
		for _, sp := range scene {
			if sp.Label == m {
				target = sp
			}
		}
	}
	lower := strings.ToLower(req)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}
	prevID := ""
	for i, sp := range scene {
		if sp.ID == target.ID && i > 0 {
			prevID = scene[i-1].ID
		}
	}
	changed := 0
	if has("splash", "full-width", "full width", "bigger", "big panel") {
		data := panelData(target)
		data.Camera = "wide"
		data.Action = strings.TrimSuffix(target.Action, ".") + " — full-width splash, held for a beat."
		d.Ops = append(d.Ops, ScriptOp{Op: OpUpdate, PanelID: target.ID, Panel: &data})
		changed++
	}
	if has("add", "beat", "before", "insert", "extra panel") {
		data := ScriptPanelData{Camera: "close", Emotion: target.Emotion, Action: "A held breath before the move: eyes measure the gap.",
			Dialogue: []DialogueLine{}, SFX: []string{}, CharacterIDs: target.CharacterIDs.V}
		d.Ops = append(d.Ops, ScriptOp{Op: OpAdd, SceneID: target.SceneID, AfterID: prevID, PageNumber: target.PageNumber, Panel: &data})
		changed++
	}
	if has("cut", "remove", "delete", "drop") && len(d.Ops) == 0 {
		d.Ops = append(d.Ops, ScriptOp{Op: OpRemove, PanelID: target.ID})
		changed++
	}
	if len(d.Ops) == 0 {
		data := panelData(target)
		data.Action = tighten(target.Action)
		if data.Emotion == "" {
			data.Emotion = "Focused"
		}
		d.Ops = append(d.Ops, ScriptOp{Op: OpUpdate, PanelID: target.ID, Panel: &data})
		changed++
	}
	d.Summary = format("studio.assist.ph.summary", "label", target.Label, "count", changed)
	d.Affects = format("studio.assist.ph.affects", "count", changed, "page", target.PageNumber)
	return d
}

func panelData(sp ScriptPanel) ScriptPanelData {
	return ScriptPanelData{Camera: sp.Camera, Shot: sp.Shot, Emotion: sp.Emotion, Action: sp.Action, Dialogue: dialogueList(sp.Dialogue.V),
		SFX: jsonList(sp.SFX.V), CharacterIDs: sp.CharacterIDs.V}
}

// tighten shortens an action line to its first clause.
func tighten(action string) string {
	a := strings.TrimSpace(action)
	if i := strings.IndexAny(a, ";,"); i > 12 {
		a = a[:i]
	}
	return strings.TrimSuffix(a, ".") + "."
}

// PlaceholderSheet draws a labelled screentone reference sheet (PNG).
func PlaceholderSheet(label string, seed uint64, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{0xDC, 0xE3, 0xF3, 0xff}
	ink := color.RGBA{0x07, 0x0A, 0x12, 0xff}
	dot := color.RGBA{0x07, 0x0A, 0x12, 0x55}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, bg)
		}
	}
	// Screentone dots.
	step := 8
	for y := step / 2; y < h; y += step {
		for x := step / 2; x < w; x += step {
			img.Set(x, y, dot)
			img.Set(x+1, y, dot)
			img.Set(x, y+1, dot)
		}
	}
	// Three figure silhouettes (front / side / face) whose proportions vary with the seed.
	r := rand.New(rand.NewPCG(seed, seed>>1|1))
	cols := 3
	for i := 0; i < cols; i++ {
		cx := w*(2*i+1)/(2*cols) + r.IntN(9) - 4
		headR := h/12 + r.IntN(h/40+1)
		if i == 2 {
			headR = h / 5
		}
		cy := h/4 + r.IntN(h/20+1)
		for y := -headR; y <= headR; y++ {
			for x := -headR; x <= headR; x++ {
				d := x*x + y*y
				if d <= headR*headR && d >= (headR-3)*(headR-3) {
					img.Set(cx+x, cy+y, ink)
				}
			}
		}
		if i < 2 {
			bw := headR + r.IntN(headR/2+1)
			for y := cy + headR + 4; y < h-40; y++ {
				t := float64(y-cy) / float64(h-cy)
				half := int(float64(bw) * (0.6 + 0.4*t))
				img.Set(cx-half, y, ink)
				img.Set(cx+half, y, ink)
				img.Set(cx-half+1, y, ink)
				img.Set(cx+half-1, y, ink)
			}
		}
	}
	// Label strip.
	for y := h - 26; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{0xF7, 0xF9, 0xFF, 0xff})
		}
	}
	dr := font.Drawer{Dst: img, Src: image.NewUniform(ink), Face: basicfont.Face7x13, Dot: fixed.P(10, h-9)}
	dr.DrawString(strings.ToUpper(label) + " · PLACEHOLDER")
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// PlaceholderLockScore returns a plausible Character Lock score (0.91–0.97) for a character and its references.
func PlaceholderLockScore(characterID string, refs int) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(characterID))
	base := 0.91 + float64(h.Sum32()%50)/1000
	if refs >= 4 {
		base += 0.01
	}
	if base > 0.97 {
		base = 0.97
	}
	return float64(int(base*100+0.5)) / 100
}
