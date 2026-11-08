package lettering

import (
	"fmt"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// worstBubble is a maximum-size auto-fit bubble: every input is within
// pagedoc validation limits.
func worstBubble(w worstText, box pagedoc.Rect) pagedoc.Bubble {
	return pagedoc.Bubble{ID: "worst-" + w.name, Kind: pagedoc.BubbleSpeech, Shape: pagedoc.ShapeEllipse, Box: box,
		Style: pagedoc.BubbleStyle{Size: 1000, AutoFit: true}, Texts: map[string]pagedoc.BubbleText{w.lang: {Text: w.text}}}
}

// worstPage has MaxBubbles maximum-size bubbles (4000×4000, 2000 runes,
// size 1000) cycling through the worst texts.
func worstPage() (pagedoc.Page, string) {
	p := pagedoc.New("worst", "project", "chapter", 1, pagedoc.MaxPageSide, pagedoc.MaxPageSide, pagedoc.DirRTL)
	p.Panels = []pagedoc.Panel{{ID: "pn", Points: pagedoc.RectPoints(pagedoc.Rect{X: 0, Y: 0, W: pagedoc.MaxPageSide, H: pagedoc.MaxPageSide})}}
	ws := worstTexts()
	for i := 0; i < pagedoc.MaxBubbles; i++ {
		w := ws[i%len(ws)]
		b := worstBubble(w, pagedoc.Rect{X: float64(i%8) * 2000, Y: float64(i/8) * 2000, W: 4000, H: 4000})
		b.ID, b.PanelID, b.Order = fmt.Sprintf("b%02d", i), "pn", i
		// LayoutPage letters every bubble in the page language; give each
		// bubble its text in that language too.
		b.Texts = map[string]pagedoc.BubbleText{"en": {Text: w.text}}
		p.Bubbles = append(p.Bubbles, b)
	}
	return p, "en"
}

// normalPage is a typical editor page: eight bubbles of everyday dialogue in
// every bubble kind, auto-fit at the default size.
func normalPage() (pagedoc.Page, string) {
	p := pagedoc.New("normal", "project", "chapter", 1, 1600, 2400, pagedoc.DirRTL)
	p.Panels = []pagedoc.Panel{{ID: "pn", Points: pagedoc.RectPoints(pagedoc.Rect{X: 40, Y: 40, W: 1520, H: 2320})}}
	lines := []string{"The last ferry leaves at dusk. Hurry!", "Wait! Don't you dare leave without me!",
		"If the tide turns before sunset, the bridge will be gone.", "At dusk, the harbour.", "WHOOSH",
		"I kept the lighthouse lit for you.", "Psst… over here.", "Then we row."}
	for i, text := range lines {
		s := corpusShapes[i%6]
		box := pagedoc.Rect{X: 80 + float64(i%2)*700, Y: 80 + float64(i/2)*560, W: 380, H: 240}
		p.Bubbles = append(p.Bubbles, pagedoc.Bubble{ID: fmt.Sprintf("n%d", i), PanelID: "pn", Order: i, Kind: s.kind, Shape: s.shape, Box: box,
			Tail:  pagedoc.Tail{Enabled: s.kind != pagedoc.BubbleCaption && s.kind != pagedoc.BubbleSFX, Tip: pagedoc.Point{X: box.X + box.W*.6, Y: box.Y + box.H + 30}},
			Style: pagedoc.BubbleStyle{AutoFit: true, Stroke: s.kind == pagedoc.BubbleSFX}, Texts: map[string]pagedoc.BubbleText{"en": {Text: text}}})
	}
	return p, "en"
}

func BenchmarkLayoutWorstBubble(b *testing.B) {
	e, err := NewEngine()
	if err != nil {
		b.Fatal(err)
	}
	for _, w := range worstTexts() {
		for _, box := range []pagedoc.Rect{{W: 4000, H: 4000}, {W: 200, H: 200}} {
			bubble := worstBubble(w, box)
			b.Run(fmt.Sprintf("%s/%gx%g", w.name, box.W, box.H), func(b *testing.B) {
				for range b.N {
					if _, err := e.Layout(bubble, w.lang); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkLayoutNormalPage(b *testing.B) {
	e, err := NewEngine()
	if err != nil {
		b.Fatal(err)
	}
	p, lang := normalPage()
	for range b.N {
		if _, err := e.LayoutPage(p, lang); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLayoutWorstPage(b *testing.B) {
	e, err := NewEngine()
	if err != nil {
		b.Fatal(err)
	}
	p, lang := worstPage()
	if err := p.Validate(); err != nil {
		b.Fatal(err)
	}
	for range b.N {
		if _, err := e.LayoutPage(p, lang); err != nil {
			b.Fatal(err)
		}
	}
}
