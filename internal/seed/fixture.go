package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

const demoTitle = "Tidebreak Courier"
const demoPremise = "When the tide cuts Bellward Island off from the harbor, a young courier discovers that the smallest delivery can carry an entire community home."
const demoNote = "Original authored demo. Art is labelled deterministic placeholder illustration; all text and layers remain editable."

type characterSpec struct {
	key, name, role, age, look, personality, speech, backstory, outfit, clothing string
}

var characters = []characterSpec{
	{"rin", "Rin Asagiri", "Courier · carries promises across the tide", "19", "Short windswept hair, a patched rain cape and a waterproof satchel.", "Impatient with clocks; patient with people.", "Short, direct sentences; softens around frightened passengers.", "Learned the harbor paths delivering groceries for Old Wren. Today is her first solo storm run.", "Storm-run cape", "Patched waxed cape, striped scarf, sturdy boots and a clasped courier satchel."},
	{"kaito", "Kaito Mbeki", "Boatwright · repairs what the sea returns", "21", "Tall, close-cropped hair, rolled work sleeves and a tool belt.", "Calm, observant, quietly inventive.", "Measured words and practical questions.", "Restores the harbor's old tide ferries and tests every knot twice.", "Boatyard workwear", "Rolled work shirt, canvas apron, rope belt and worn deck shoes."},
	{"wren", "Old Wren", "Postkeeper · remembers every address", "72", "A small figure with a broad hat, round spectacles and an oversized coat.", "Wry, meticulous and generous with second chances.", "Warm dry humor; calls a destination a promise.", "Kept the island post open through forty storm seasons.", "Postkeeper's coat", "Oversized coat, brimmed rain hat and a ring of brass sorting keys."},
	{"sora", "Sora Vell", "Bell keeper · listens before sounding the alarm", "20", "Long tied-back hair, a sleeveless tunic and a bell-rope sash.", "Resourceful and slow to ask for help.", "Gentle, deliberate; pauses to listen to the wind.", "Tends the Bellward signal tower, whose quiet bell guides late ferries.", "Tower work tunic", "Simple belted tunic, fingerless gloves and a coiled bell-rope sash."},
	{"pip", "Pip the Gull", "Harbor companion · an enthusiastic lookout", "Adult gull", "A round gull with a dark wingtip and an irrepressible crest.", "Bold around sandwiches, loyal in bad weather.", "Kree! Body language and perfectly timed interruptions.", "Adopted the post office windowsill after Wren rescued a tangled wing.", "Natural plumage", "White plumage, dark wingtips and one slightly crooked crest feather; no costume."},
}

type chapterFixture struct {
	id, title, summary, location string
	doc                          studio.ScriptDoc
	pages                        []pagedoc.Page
}

type fixture struct {
	projectID, owner string
	ids, art         map[string]string
	chapters         []chapterFixture
}

func newFixture(owner string) *fixture {
	f := &fixture{projectID: db.NewID(), owner: owner, ids: map[string]string{}, art: map[string]string{}}
	for _, c := range characters {
		f.ids[c.key] = db.NewID()
	}
	for _, key := range []string{"quay", "tower", "parcel", "style"} {
		f.ids[key] = db.NewID()
	}
	line := func(who, text string) studio.ScriptDialogue {
		for _, c := range characters {
			if c.key == who {
				return studio.ScriptDialogue{Speaker: c.name, CharacterID: f.ids[who], Kind: "speech", Text: text}
			}
		}
		return studio.ScriptDialogue{Kind: "caption", Text: text}
	}
	panel := func(page int, action string, cast []string, lines ...studio.ScriptDialogue) studio.ScriptPanel {
		ids := make([]string, 0, len(cast))
		for _, key := range cast {
			ids = append(ids, f.ids[key])
		}
		return studio.ScriptPanel{ID: db.NewID(), PageNumber: page, Camera: "eye-level", Shot: "medium",
			Emotion: "hopeful", Action: action, Dialogue: lines, CharacterIDs: ids, SFX: []studio.ScriptSFX{}, Notes: demoNote}
	}
	f.chapters = []chapterFixture{
		{id: db.NewID(), title: "The Last Crossing", summary: "Rin accepts a small parcel for Bellward Island as the storm closes the harbor.", location: "quay",
			doc: studio.ScriptDoc{Schema: "mangaveyra.script.v1", Scenes: []studio.ScriptScene{{ID: db.NewID(), Title: "A promise at the quay", Summary: "A parcel, a broken bridge and an unexpected boat.", TimeOfDay: "late afternoon", Panels: []studio.ScriptPanel{
				panel(1, "Rain stipples Tidebreak Quay. Rin leans into the wind while Pip circles the post office sign.", []string{"rin", "pip"}, line("", "At Tidebreak, every address changes with the water.")),
				panel(1, "Old Wren holds out a small, carefully wrapped parcel. Rin checks the dark water beyond the quay.", []string{"wren", "rin"}, line("wren", "One last delivery. Bellward Tower."), line("rin", "The tide bridge is under water.")),
				panel(1, "Wren places the parcel in Rin's satchel. A handwritten address shows Sora Vell's name.", []string{"wren", "rin"}, line("wren", "An address is a promise. Find another path.")),
				panel(2, "Kaito rolls a narrow repaired ferry from his boat shed. Rin shields her satchel from the spray.", []string{"kaito", "rin"}, line("kaito", "I fixed the hull. Can you find the channel?"), line("rin", "Pip knows the shallow bits.")),
				panel(2, "Pip lands on the bow, chest out. Kaito pushes the little boat clear of the flooded steps.", []string{"pip", "kaito"}, line("pip", "Kree!")),
				panel(2, "Rin and Kaito paddle toward the small tower. A dark wave curls behind the boat.", []string{"rin", "kaito"}, line("rin", "Then let's keep a promise.")),
			}}}}},
		{id: db.NewID(), title: "A Sound to Follow", summary: "A tiny bell clapper helps Sora guide the last boat home, and Rin discovers what her parcel was carrying.", location: "tower",
			doc: studio.ScriptDoc{Schema: "mangaveyra.script.v1", Scenes: []studio.ScriptScene{{ID: db.NewID(), Title: "Bellward answers", Summary: "A repaired bell gives the harbor a way home.", TimeOfDay: "dusk", Panels: []studio.ScriptPanel{
				panel(1, "At Bellward's landing, Sora catches the mooring rope. Rin climbs the wet stone steps with the parcel.", []string{"sora", "rin"}, line("sora", "I thought the post had stopped."), line("rin", "Only the bridge did.")),
				panel(1, "Sora unwraps a polished bell clapper. A note from Wren rests inside the paper.", []string{"sora"}, line("", "For the bell that brings us home. - Wren")),
				panel(1, "Kaito steadies the ladder while Sora fits the clapper. Rin braces the window against the gusts.", []string{"sora", "kaito", "rin"}, line("kaito", "That should hold. Give it a voice.")),
				panel(2, "Sora pulls the rope. The bell swings above the rain, and Pip spreads both wings.", []string{"sora", "pip"}, line("sora", "This way. You're nearly home.")),
				panel(2, "A small lantern answers from the channel. Rin and Kaito smile as the last ferry follows the sound.", []string{"rin", "kaito"}, line("rin", "All that sound, in such a little parcel.")),
				panel(2, "Back at the post office window, Wren sets out five mugs and a saucer. The tower bell sounds across the harbor.", []string{"wren", "pip"}, line("wren", "Good. The kettle's still warm."), line("", "Delivery complete. The way home stays open.")),
			}}}}},
	}
	for i := range f.chapters {
		c := &f.chapters[i]
		c.doc.ChapterID, c.doc.ChapterSummary = c.id, c.summary
		for si := range c.doc.Scenes {
			scene := &c.doc.Scenes[si]
			scene.Key = scene.ID
			for pi := range scene.Panels {
				p := &scene.Panels[pi]
				p.Key, p.Label = p.ID, fmt.Sprintf("%d.%d", p.PageNumber, pi%3+1)
			}
		}
	}
	return f
}

func (f *fixture) makePages(engine lettering.Engine) error {
	layout, _ := pagedoc.LayoutByID("establishing")
	for ci := range f.chapters {
		c := &f.chapters[ci]
		for number := 1; number <= 2; number++ {
			page := pagedoc.New(db.NewID(), f.projectID, c.id, number, 1600, 2400, pagedoc.DirRTL)
			page.LayoutID, page.Note = layout.ID, demoNote
			points := layout.PanelPoints(page.Width, page.Height, page.Direction)
			for _, scene := range c.doc.Scenes {
				for _, sp := range scene.Panels {
					if sp.PageNumber != number {
						continue
					}
					panel := pagedoc.Panel{ID: db.NewID(), Order: len(page.Panels), Points: points[len(page.Panels)], ScriptPanelID: sp.ID,
						Prompt: sp.Action, PromptSource: "script", Camera: sp.Camera, Shot: sp.Shot, Style: "bw_screentone",
						AltText: "Labelled original demo placeholder: " + sp.Action}
					layer := func(kind, name, asset string) pagedoc.Layer {
						l := pagedoc.Layer{ID: db.NewID(), Kind: kind, Name: name, Order: len(panel.Layers), Visible: true,
							Opacity: 1, Blend: "normal", Provenance: pagedoc.ProvenanceHuman}
						if asset != "" {
							l.Asset = &pagedoc.AssetRef{ID: asset}
						}
						return l
					}
					panel.Layers = append(panel.Layers, layer(pagedoc.LayerBackground, "Background · demo placeholder", f.art[c.location]))
					for i, id := range sp.CharacterIDs {
						var key, name string
						for _, character := range characters {
							if f.ids[character.key] == id {
								key, name = character.key, character.name
							}
						}
						l := layer(pagedoc.LayerCharacter, name+" · demo placeholder", f.art[key])
						l.CharacterID = id
						l.Transform = characterSlot(pagedoc.Bounds(panel.Points), i, len(sp.CharacterIDs), page.Direction)
						panel.Layers = append(panel.Layers, l)
					}
					panel.Layers = append(panel.Layers, layer(pagedoc.LayerFX, "Rain · demo placeholder", f.art["fx"]))
					panel.Layers = append(panel.Layers, layer(pagedoc.LayerLettering, "Editable dialogue", ""))
					page.Panels = append(page.Panels, panel)
					lines := make([]lettering.Line, 0, len(sp.Dialogue))
					for _, d := range sp.Dialogue {
						lines = append(lines, lettering.Line{Kind: d.Kind, Text: d.Text, CharacterID: d.CharacterID, ScriptPanelID: sp.ID})
					}
					bubbles, err := engine.AutoPlace(page, panel.ID, lines, "en", false)
					if err != nil {
						return fmt.Errorf("seed: place authored lettering: %w", err)
					}
					for i := range bubbles {
						bubbles[i].Texts["en"] = pagedoc.BubbleText{Text: lines[i].Text, Status: "reviewed", Source: pagedoc.ProvenanceHuman}
					}
					page.Bubbles = append(page.Bubbles, bubbles...)
				}
			}
			if err := page.Validate(); err != nil {
				return fmt.Errorf("seed: invalid demo page: %w", err)
			}
			c.pages = append(c.pages, page)
		}
	}
	return nil
}

// characterSlot places character i of n so that its whole characterBox
// (figure and name plate) fits its own slot of the panel, standing on the
// panel's lower margin: labels never overlap each other or the panel edge.
// Slots run in reading order (right to left on RTL pages). The transform
// follows compose: the image covers the panel box, then Scale and offsets.
func characterSlot(b pagedoc.Rect, i, n int, direction string) pagedoc.Transform {
	const artW, artH = 640.0, 960.0
	margin := math.Min(b.W, b.H) * .05
	slotW := (b.W - 2*margin) / float64(n)
	boxW, boxH := float64(characterBox.Dx()), float64(characterBox.Dy())
	s := math.Min(slotW/boxW, (b.H-2*margin)/boxH) * .96
	cover := math.Max(b.W/artW, b.H/artH)
	centre := b.X + margin + slotW*(float64(i)+.5)
	if direction == pagedoc.DirRTL {
		centre = b.X + b.W - margin - slotW*(float64(i)+.5)
	}
	boxCentreX := float64(characterBox.Min.X+characterBox.Max.X) / 2
	return pagedoc.Transform{
		Scale:   s / cover,
		OffsetX: centre - b.X - b.W/2 - (boxCentreX-artW/2)*s,
		OffsetY: b.H/2 - margin - (float64(characterBox.Max.Y)-artH/2)*s,
	}
}

func (f *fixture) insert(ctx context.Context, tx *db.Tx, studioService studio.Service, pages pagestore.Service, a *assets.Service, staged []*assets.StagedAsset) error {
	now := db.Now()
	var slugs []string
	if err := tx.Select(ctx, &slugs, `SELECT slug FROM projects WHERE owner_id = ?`, f.owner); err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, slug := range slugs {
		taken[slug] = true
	}
	slug := "tidebreak-courier-demo"
	for suffix := 2; taken[slug]; suffix++ {
		slug = "tidebreak-courier-demo-" + strconv.Itoa(suffix)
	}
	source := []string{demoTitle, demoPremise}
	for _, c := range f.chapters {
		source = append(source, c.title, c.summary)
		for _, scene := range c.doc.Scenes {
			for _, p := range scene.Panels {
				source = append(source, p.Action)
				for _, line := range p.Dialogue {
					source = append(source, line.Speaker+": "+line.Text)
				}
			}
		}
	}
	settings := jsonValue(map[string]any{"demo_seed": demoMarker{demoID, demoVersion}, "script_source_text": strings.Join(source, "\n"), "demo_note": demoNote})
	if _, err := tx.Exec(ctx, `INSERT INTO projects (id, owner_id, title, slug, premise, logline, genres, tags, style,
		format, page_size, page_width, page_height, language, status, settings, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'bw_screentone', 'manga_rtl', 'b5', 1600, 2400, 'en', 'active', ?, ?, ?)`,
		f.projectID, f.owner, demoTitle, slug, demoPremise, "One small parcel. One storm crossing. A sound to follow home.",
		`["adventure","slice_of_life"]`, `["original","demo","placeholder_art"]`, settings, now, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, 'owner', ?)`, f.projectID, f.owner, now); err != nil {
		return err
	}
	for _, asset := range staged {
		if _, err := a.CommitStagedTx(ctx, tx, asset); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET cover_asset_id = ? WHERE id = ?`, f.art["cover"], f.projectID); err != nil {
		return err
	}
	for i, c := range characters {
		category := "supporting"
		if i < 2 {
			category = "main"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO characters (id, project_id, name, role, role_line, age, look, personality, speech_style,
			backstory, category, order_idx, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, f.ids[c.key],
			f.projectID, c.name, strings.Split(c.role, " · ")[0], c.role, c.age, c.look, c.personality, c.speech, c.backstory, category, i, now, now); err != nil {
			return err
		}
		if _, err := studioService.AddCharacterRefTx(ctx, tx, f.ids[c.key], f.art[c.key], "front", "Original demo placeholder · "+c.name); err != nil {
			return err
		}
		if err := studioService.SetCharacterPortraitTx(ctx, tx, f.ids[c.key], f.art[c.key]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO character_outfits (id, character_id, name, description, asset_id, is_default, order_idx, created_at)
			VALUES (?, ?, ?, ?, ?, TRUE, 0, ?)`, db.NewID(), f.ids[c.key], c.outfit, c.clothing, f.art[c.key], now); err != nil {
			return err
		}
	}
	for i, setting := range []struct{ key, kind, name, description string }{
		{"quay", "location", "Tidebreak Quay", "Flooded harbor steps, a boat shed and Wren's small post office. The tide determines which paths remain open."},
		{"tower", "location", "Bellward Tower", "A stone signal tower on a low island. Its bell guides late ferries through the shallow channel."},
		{"parcel", "prop", "The little parcel", "A waterproof wrapping around a polished replacement bell clapper and Wren's handwritten note."},
		{"style", "style", "Storm-season screentone", "Original labelled placeholder line art, black ink, paper white and stippled gray. Replace each layer independently."},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO setting_items (id, project_id, kind, name, description, tags, asset_id, data, order_idx, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, f.ids[setting.key], f.projectID, setting.kind, setting.name, setting.description,
			`["original","demo"]`, db.NullString(f.art[setting.key]), `{"source":"human","placeholder":true}`, i, now, now); err != nil {
			return err
		}
	}
	arcID := db.NewID()
	if _, err := tx.Exec(ctx, `INSERT INTO arcs (id, project_id, title, summary, order_idx, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?)`,
		arcID, f.projectID, "The Way Home", "A storm crossing turns a small delivery into a signal for the whole harbor.", now, now); err != nil {
		return err
	}
	for i, c := range f.chapters {
		if _, err := tx.Exec(ctx, `INSERT INTO chapters (id, project_id, arc_id, number, title, summary, status, ai_label, settings, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'drawing', ?, ?, ?, ?)`, c.id, f.projectID, arcID, i+1, c.title, c.summary,
			`{"script":"human","art":"human","lettering":"human"}`, `{"script_provenance":{"source":"human","label":"Original authored demo"}}`, now, now); err != nil {
			return err
		}
		for si, scene := range c.doc.Scenes {
			if _, err := tx.Exec(ctx, `INSERT INTO scenes (id, project_id, chapter_id, order_idx, title, summary, location_id, time_of_day, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, scene.ID, f.projectID, c.id, si, scene.Title, scene.Summary, f.ids[c.location], scene.TimeOfDay, now, now); err != nil {
				return err
			}
			for pi, p := range scene.Panels {
				if _, err := tx.Exec(ctx, `INSERT INTO script_panels (id, project_id, chapter_id, scene_id, page_number, order_idx, label,
					camera, shot, emotion, action, dialogue, sfx, character_ids, notes, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.ID, f.projectID, c.id, scene.ID, p.PageNumber, pi, p.Label,
					p.Camera, p.Shot, p.Emotion, p.Action, jsonValue(p.Dialogue), jsonValue(p.SFX), jsonValue(p.CharacterIDs), p.Notes, now, now); err != nil {
					return err
				}
			}
		}
		if _, err := pages.CreatePagesTx(ctx, tx, c.id, c.pages, pagestore.Change{ActorKind: "system", ActorID: f.owner,
			Action: "seed_demo", Summary: "Original authored demo with labelled placeholder art"}); err != nil {
			return err
		}
	}
	return nil
}

func jsonValue(v any) string {
	b, _ := json.Marshal(v) // All fixture values are bounded strings and structs.
	return string(b)
}
