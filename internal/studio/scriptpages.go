package studio

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// PagesResult reports what MakePagesFromScript did.
type PagesResult struct {
	Created []int `json:"created"`
	Filled  []int `json:"filled"`
	// Skipped pages already had panels or are locked.
	Skipped []int `json:"skipped"`
}

// MakePagesFromScript turns a chapter's panel-by-panel script into pages:
// one page per script page number, panels laid out with the layout
// suggested for that page's beat count, each panel linked to its script
// panel, prompted from the script (camera, action, emotion, characters,
// location) and given background, character and lettering layers, with the
// dialogue and SFX as bubbles. Pages that already have panels, and locked
// pages, are left alone. Each written page records a version.
func (s *Service) MakePagesFromScript(ctx context.Context, actor Actor, chapterID string) (*PagesResult, error) {
	ch, err := s.ChapterByID(ctx, chapterID)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, actor, ch.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := s.require(ctx, a, EditPages); err != nil {
		return nil, err
	}
	if ch.Locked {
		return nil, lockedErr(ctx, i18n.T(ctx, "studio.lock.chapter", "n", ch.Number))
	}
	script, err := s.ScriptPanels(ctx, chapterID)
	if err != nil {
		return nil, err
	}
	if len(script) == 0 {
		return nil, core.ErrValidation(map[string]string{"script": i18n.T(ctx, "studio.story.no_script")})
	}
	scenes, err := s.Scenes(ctx, chapterID)
	if err != nil {
		return nil, err
	}
	sceneByID := map[string]Scene{}
	sceneOrder := map[string]int{}
	for _, sc := range scenes {
		sceneByID[sc.ID], sceneOrder[sc.ID] = sc, sc.OrderIdx
	}
	chars, err := s.Characters(ctx, ch.ProjectID, "")
	if err != nil {
		return nil, err
	}
	charName := map[string]string{}
	for _, c := range chars {
		charName[c.ID] = c.Name
	}
	settings, err := s.SettingItems(ctx, ch.ProjectID, "")
	if err != nil {
		return nil, err
	}
	settingName := map[string]string{}
	for _, it := range settings {
		settingName[it.ID] = it.Name
	}
	// Group by page in reading order.
	sort.SliceStable(script, func(i, j int) bool {
		if script[i].PageNumber != script[j].PageNumber {
			return script[i].PageNumber < script[j].PageNumber
		}
		if oi, oj := sceneOrder[script[i].SceneID], sceneOrder[script[j].SceneID]; oi != oj {
			return oi < oj
		}
		return script[i].OrderIdx < script[j].OrderIdx
	})
	groups := map[int][]ScriptPanel{}
	var numbers []int
	for _, sp := range script {
		n := max(sp.PageNumber, 1)
		if _, ok := groups[n]; !ok {
			numbers = append(numbers, n)
		}
		groups[n] = append(groups[n], sp)
	}
	res := &PagesResult{Created: []int{}, Filled: []int{}, Skipped: []int{}}
	pr := a.Project
	for _, n := range numbers {
		page, err := s.PageByNumber(ctx, chapterID, n)
		created := false
		if err != nil {
			if page, err = s.insertPage(ctx, s.db, pr, ch, n, ""); err != nil {
				return nil, err
			}
			created = true
		}
		doc, err := s.LoadPageDoc(ctx, page.ID)
		if err != nil {
			return nil, err
		}
		if len(doc.Panels) > 0 || doc.Page.Locked {
			res.Skipped = append(res.Skipped, n)
			continue
		}
		group := groups[n]
		polys, layoutID := SuggestLayout(len(group), pr.Format)
		if pr.Format == FormatComicLTR {
			for i := range polys {
				polys[i] = MirrorPolygon(polys[i])
			}
		}
		doc.Page.LayoutID = layoutID
		for i, sp := range group {
			pd := newPanelDoc(doc.Page, polys[i], sp.CharacterIDs.V)
			pd.ScriptPanelID = sp.ID
			pd.Camera, pd.Shot = sp.Camera, sp.Shot
			pd.Prompt = scriptPrompt(sp, sceneByID[sp.SceneID], charName, settingName)
			pd.AltText = clip(sp.Action, 2000)
			for j := range pd.Layers {
				if pd.Layers[j].Kind == LayerCharacter {
					pd.Layers[j].Name = charName[pd.Layers[j].CharacterID]
				}
			}
			doc.Panels = append(doc.Panels, pd)
			doc.Bubbles = append(doc.Bubbles, scriptBubbles(pd, sp, pr)...)
		}
		summary := i18n.T(ctx, "studio.version.from_script", "count", len(group))
		if _, _, err := s.SavePageDoc(ctx, actor, doc, SaveOptions{Action: "script", Summary: summary}); err != nil {
			return nil, err
		}
		if created {
			res.Created = append(res.Created, n)
		} else {
			res.Filled = append(res.Filled, n)
		}
	}
	if len(res.Created)+len(res.Filled) > 0 && (ch.Status == ChapterDraft || ch.Status == ChapterScripting) {
		_, _ = s.db.Exec(ctx, `UPDATE chapters SET status = ?, updated_at = ? WHERE id = ?`, ChapterDrawing, s.now(), ch.ID)
	}
	s.Publish(ctx, ch.ProjectID, "studio.pages.created", Event{Kind: "pages", ChapterID: ch.ID, Summary: fmt.Sprint(len(res.Created))})
	return res, nil
}

// scriptPrompt writes a generation prompt from a script panel.
func scriptPrompt(sp ScriptPanel, sc Scene, charName, settingName map[string]string) string {
	var parts []string
	if sp.Camera != "" {
		parts = append(parts, strings.ToLower(sp.Camera)+" shot")
	}
	if sp.Action != "" {
		parts = append(parts, strings.TrimSuffix(sp.Action, "."))
	}
	var names []string
	for _, id := range sp.CharacterIDs.V {
		if n := charName[id]; n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		parts = append(parts, "characters: "+strings.Join(names, ", "))
	}
	if sp.Emotion != "" {
		parts = append(parts, "mood: "+strings.ToLower(sp.Emotion))
	}
	var places []string
	if n := settingName[sc.LocationID]; n != "" {
		places = append(places, n)
	}
	for _, id := range sp.SettingIDs.V {
		if n := settingName[id]; n != "" && id != sc.LocationID {
			places = append(places, n)
		}
	}
	if len(places) > 0 {
		parts = append(parts, "setting: "+strings.Join(places, ", "))
	}
	if sc.TimeOfDay != "" {
		parts = append(parts, "time: "+sc.TimeOfDay)
	}
	return clip(strings.Join(parts, "; "), 4000)
}

func bbox(p Polygon) (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = 100, 100, 0, 0
	for _, pt := range p {
		x0, y0 = math.Min(x0, pt[0]), math.Min(y0, pt[1])
		x1, y1 = math.Max(x1, pt[0]), math.Max(y1, pt[1])
	}
	return
}

// scriptBubbles places a script panel's dialogue and SFX in the panel: speech
// in reading order from the leading edge (right for manga), captions in the
// top corner, SFX across the middle.
func scriptBubbles(pd PanelDoc, sp ScriptPanel, pr *Project) []BubbleDoc {
	x0, y0, x1, y1 := bbox(pd.Points.V)
	w, h := x1-x0, y1-y0
	rtl := pr.Format == FormatMangaRTL
	vertical := rtl && strings.HasPrefix(pr.Language, "ja")
	now := time.Now().UTC()
	var out []BubbleDoc
	slot := 0
	mk := func(kind, speaker, text string, g BubbleGeometry) BubbleDoc {
		b := BubbleDoc{Bubble: Bubble{ID: db.NewID(), PageID: pd.PageID, PanelID: pd.ID, Kind: kind, SpeakerCharacterID: speaker,
			ScriptPanelID: sp.ID, Vertical: vertical && kind != BubbleSFX && kind != BubbleCaption, CreatedAt: now, UpdatedAt: now}}
		b.Geometry.V = g
		b.Tail.V, b.Style.V = map[string]any{}, map[string]any{}
		if kind != BubbleCaption && kind != BubbleSFX && speaker != "" {
			b.Tail.V = map[string]any{"x": round1(g.X + g.W/2), "y": round1(math.Min(y1-2, g.Y+g.H+6)), "curve": 0.2}
		}
		t := BubbleText{ID: db.NewID(), BubbleID: b.ID, Lang: pr.Language, Text: text, Status: "draft", Source: "human", CreatedAt: now, UpdatedAt: now}
		t.Fit.V = map[string]any{}
		b.Texts = []BubbleText{t}
		return b
	}
	for _, l := range sp.Dialogue.V {
		if l.Kind == LineCaption {
			bw := round1(math.Min(w*0.55, 40))
			x := x0 + 1
			if rtl {
				x = x1 - bw - 1
			}
			out = append(out, mk(BubbleCaption, "", l.Text, BubbleGeometry{X: round1(x), Y: round1(y0 + 1), W: bw, H: round1(math.Min(h*0.2, 6))}))
			continue
		}
		bw := round1(math.Max(10, math.Min(w*0.42, 24)))
		bh := round1(math.Max(5, math.Min(h*0.45, 4+float64(len([]rune(l.Text)))/12)))
		step := bw + 1.5
		x := x0 + 1.5 + float64(slot)*step
		if rtl {
			x = x1 - 1.5 - bw - float64(slot)*step
		}
		x = math.Max(x0, math.Min(x, x1-bw))
		y := y0 + 2 + float64(slot%2)*3
		kind := l.Kind
		if kind == "" {
			kind = BubbleSpeech
		}
		out = append(out, mk(kind, l.CharacterID, l.Text, BubbleGeometry{X: round1(x), Y: round1(y), W: bw, H: bh}))
		slot++
	}
	for i, fx := range sp.SFX.V {
		bw := round1(math.Min(w*0.5, 30))
		x := x0 + (w-bw)/2 + float64(i)*4
		out = append(out, mk(BubbleSFX, "", fx, BubbleGeometry{X: round1(math.Min(x, x1-bw)), Y: round1(y0 + h*0.55 + float64(i)*4), W: bw, H: round1(math.Min(h*0.25, 10))}))
	}
	return out
}
