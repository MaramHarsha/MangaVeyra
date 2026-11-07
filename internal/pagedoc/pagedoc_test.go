package pagedoc

import (
	"encoding/json"
	"math"
	"testing"
)

func validPage() Page {
	p := New("pg1", "pr1", "ch1", 1, 1600, 2400, DirRTL)
	p.Panels = []Panel{{ID: "pn1", Points: RectPoints(Rect{X: 80, Y: 80, W: 700, H: 600}), PromptSource: "script",
		Layers: []Layer{{ID: "ly1", Kind: LayerBackground, Visible: true, Opacity: 1, Provenance: ProvenanceAI},
			{ID: "ly2", Kind: LayerCharacter, CharacterID: "c1", Visible: true, Opacity: 1, Provenance: ProvenanceAI}}}}
	p.Bubbles = []Bubble{{ID: "b1", PanelID: "pn1", Kind: BubbleSpeech, Shape: ShapeEllipse, Box: Rect{X: 100, Y: 100, W: 200, H: 120},
		Style: BubbleStyle{AutoFit: true}, Texts: map[string]BubbleText{"en": {Text: "You're late. Again.", Status: "draft", Source: "human"}}}}
	return p
}

func TestValidateAcceptsValidPage(t *testing.T) {
	p := validPage()
	if err := p.Validate(); err != nil {
		t.Fatalf("valid page rejected: %v", err)
	}
	b, _ := json.Marshal(p)
	var back Page
	if err := json.Unmarshal(b, &back); err != nil || back.Validate() != nil {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Page){
		"schema":        func(p *Page) { p.Schema = "x" },
		"direction":     func(p *Page) { p.Direction = "up" },
		"dup id":        func(p *Page) { p.Bubbles[0].ID = "pn1" },
		"few points":    func(p *Page) { p.Panels[0].Points = p.Panels[0].Points[:2] },
		"off page":      func(p *Page) { p.Panels[0].Points[0].X = 99999 },
		"layer kind":    func(p *Page) { p.Panels[0].Layers[0].Kind = "sky" },
		"char no id":    func(p *Page) { p.Panels[0].Layers[1].CharacterID = "" },
		"opacity":       func(p *Page) { p.Panels[0].Layers[0].Opacity = 2 },
		"bubble kind":   func(p *Page) { p.Bubbles[0].Kind = "yell" },
		"unknown panel": func(p *Page) { p.Bubbles[0].PanelID = "nope" },
		"bad box":       func(p *Page) { p.Bubbles[0].Box.W = 0 },
		"bad ruby": func(p *Page) {
			p.Bubbles[0].Texts["en"] = BubbleText{Text: "ab", Ruby: []Ruby{{Start: 1, End: 5, Text: "x"}}}
		},
		"prompt source":   func(p *Page) { p.Panels[0].PromptSource = "robot" },
		"provenance":      func(p *Page) { p.Panels[0].Layers[0].Provenance = "" },
		"nan box":         func(p *Page) { p.Bubbles[0].Box.W = math.NaN() },
		"infinite box":    func(p *Page) { p.Bubbles[0].Box.H = math.Inf(1) },
		"outside bubble":  func(p *Page) { p.Bubbles[0].Box.X = 1700 },
		"nan font":        func(p *Page) { p.Bubbles[0].Style.Size = math.NaN() },
		"nan transform":   func(p *Page) { p.Panels[0].Layers[0].Transform.Rotate = math.NaN() },
		"negative scale":  func(p *Page) { p.Panels[0].Layers[0].Transform.Scale = -1 },
		"bad blend":       func(p *Page) { p.Panels[0].Layers[0].Blend = "arbitrary-css" },
		"flat polygon":    func(p *Page) { p.Panels[0].Points = []Point{{1, 1}, {2, 2}, {3, 3}} },
		"crossed polygon": func(p *Page) { p.Panels[0].Points = []Point{{10, 10}, {100, 100}, {10, 100}, {100, 10}} },
		"raster lettering": func(p *Page) {
			p.Panels[0].Layers[0].Kind = LayerLettering
			p.Panels[0].Layers[0].Asset = &AssetRef{ID: "art"}
		},
		"bad text source": func(p *Page) { t := p.Bubbles[0].Texts["en"]; t.Source = "model-html"; p.Bubbles[0].Texts["en"] = t },
		"overlapping ruby": func(p *Page) {
			p.Bubbles[0].Texts["en"] = BubbleText{Text: "abcd", Ruby: []Ruby{{Start: 0, End: 3, Text: "a"}, {Start: 2, End: 4, Text: "b"}}}
		},
	}
	for name, mutate := range cases {
		p := validPage()
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestGeometryHelpers(t *testing.T) {
	pts := RectPoints(Rect{X: 10, Y: 20, W: 100, H: 50})
	if b := Bounds(pts); b != (Rect{X: 10, Y: 20, W: 100, H: 50}) {
		t.Fatalf("bounds %+v", b)
	}
	if !Contains(pts, Point{50, 40}) || Contains(pts, Point{5, 40}) {
		t.Fatal("contains is wrong")
	}
}

func TestLayoutsProduceValidPanelsInReadingOrder(t *testing.T) {
	for _, l := range Layouts() {
		for _, dir := range []string{DirRTL, DirLTR, DirVertical} {
			polys := l.PanelPoints(1600, 2400, dir)
			if len(polys) != l.Panels {
				t.Fatalf("%s/%s: %d polygons, want %d", l.ID, dir, len(polys), l.Panels)
			}
			p := New("pg", "pr", "ch", 1, 1600, 2400, dir)
			for i, pts := range polys {
				p.Panels = append(p.Panels, Panel{ID: string(rune('a' + i)), Points: pts, Layers: []Layer{}})
				b := Bounds(pts)
				if b.W < 50 || b.H < 50 {
					t.Fatalf("%s/%s panel %d too small: %+v", l.ID, dir, i, b)
				}
			}
			if err := p.Validate(); err != nil {
				t.Fatalf("%s/%s invalid: %v", l.ID, dir, err)
			}
		}
	}
	// Right-to-left: the first panel of a two-cell row sits to the right of the second.
	l, _ := LayoutByID("talking-heads")
	rtl := l.PanelPoints(1600, 2400, DirRTL)
	if Bounds(rtl[0]).X <= Bounds(rtl[1]).X {
		t.Fatal("rtl first panel should be on the right")
	}
	ltr := l.PanelPoints(1600, 2400, DirLTR)
	if Bounds(ltr[0]).X >= Bounds(ltr[1]).X {
		t.Fatal("ltr first panel should be on the left")
	}
}

func TestSuggest(t *testing.T) {
	if s := Suggest(5, DirRTL); s[0].Panels != 5 {
		t.Fatalf("suggest 5 -> %s (%d)", s[0].ID, s[0].Panels)
	}
	if s := Suggest(3, DirVertical); s[0].ID != "webtoon-strip" {
		t.Fatalf("vertical should prefer webtoon, got %s", s[0].ID)
	}
}
