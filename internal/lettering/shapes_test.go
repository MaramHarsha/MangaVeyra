package lettering

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tdewolff/canvas"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// insideOutline reports the first glyph outline point (widened by the text
// stroke) that is not inside the bubble's drawn outline less half its stroke.
// The outline is the shape path itself (without the tail), so this checks the
// rendering, not the fitting arithmetic.
func insideOutline(t *testing.T, b pagedoc.Bubble, l BubbleLayout) (canvas.Point, bool) {
	t.Helper()
	text, err := canvas.ParseSVGPath(l.TextPath)
	if err != nil {
		t.Fatal(err)
	}
	inner := b
	inner.Tail = pagedoc.Tail{}
	half := l.StrokeWidth / 2
	if b.Shape != pagedoc.ShapeNone {
		inner.Box = pagedoc.Rect{X: b.Box.X + half, Y: b.Box.Y + half, W: b.Box.W - 2*half, H: b.Box.H - 2*half}
	}
	var outline *canvas.Path
	if b.Shape == pagedoc.ShapeNone {
		outline = canvas.Rectangle(inner.Box.W, inner.Box.H).Translate(inner.Box.X, inner.Box.Y)
	} else {
		d, err := bubblePath(inner)
		if err != nil {
			t.Fatal(err)
		}
		if outline, err = canvas.ParseSVGPath(d); err != nil {
			t.Fatal(err)
		}
	}
	grow := l.TextStrokeWidth / 2
	for _, p := range text.Flatten(.5).Coords() {
		for _, o := range [5][2]float64{{0, 0}, {grow, 0}, {-grow, 0}, {0, grow}, {0, -grow}} {
			if !outline.ContainsPoint(p.X+o[0], p.Y+o[1], canvas.NonZero) {
				return canvas.Point{X: p.X + o[0], Y: p.Y + o[1]}, false
			}
		}
	}
	return canvas.Point{}, true
}

// Every shape keeps every glyph inside its drawn outline under auto-fit, in
// every content language, horizontally and vertically.
func TestAutoFitKeepsGlyphsInsideEveryShape(t *testing.T) {
	e := testEngine(t)
	shapes := []struct{ kind, shape string }{
		{pagedoc.BubbleSpeech, pagedoc.ShapeEllipse}, {pagedoc.BubbleWhisper, pagedoc.ShapeEllipse},
		{pagedoc.BubbleThought, pagedoc.ShapeCloud}, {pagedoc.BubbleShout, pagedoc.ShapeBurst},
		{pagedoc.BubbleCaption, pagedoc.ShapeRect}, {pagedoc.BubbleSFX, pagedoc.ShapeNone},
	}
	boxes := []pagedoc.Rect{{X: 60, Y: 60, W: 420, H: 260}, {X: 60, Y: 60, W: 200, H: 340}}
	var sheet []BubbleLayout
	for _, lang := range pagedoc.ContentLanguages {
		for _, s := range shapes {
			for bi, box := range boxes {
				for _, vertical := range []bool{false, true} {
					if vertical && !pagedoc.VerticalAllowed(lang) {
						continue
					}
					for _, size := range []float64{0, 14, 14 * 96.0 / 72, 44} {
						b := languageBubble(lang, languageSamples[lang])
						b.Kind, b.Shape, b.Box, b.Vertical = s.kind, s.shape, box, vertical
						b.Style = pagedoc.BubbleStyle{Size: size, AutoFit: true, Stroke: s.kind == pagedoc.BubbleSFX}
						b.Tail = pagedoc.Tail{Enabled: s.kind != pagedoc.BubbleCaption && s.kind != pagedoc.BubbleSFX, Tip: pagedoc.Point{X: box.X + box.W*.7, Y: box.Y + box.H + 40}}
						name := fmt.Sprintf("%s/%s/box%d/vertical=%v/size=%.0f", lang, s.shape, bi, vertical, size)
						l, stats := layoutStatsFor(t, e, b, lang)
						if stats.notdef != 0 {
							t.Fatalf("%s: .notdef", name)
						}
						if l.Overflow {
							p, _ := canvas.ParseSVGPath(l.TextPath)
							t.Fatalf("%s: a sample sentence must fit (size %.1f, ink %+v, area %+v, lines %+v)", name, l.FontSize, p.Bounds(), l.TextBox, l.Lines)
						}
						if p, ok := insideOutline(t, b, l); !ok {
							t.Fatalf("%s: glyph point %.1f,%.1f is outside the %s outline (size %.1f)", name, p.X, p.Y, s.shape, l.FontSize)
						}
						if size == 14 && bi == 0 && !vertical {
							x, y := float64(20+len(sheet)%6*440), float64(20+len(sheet)/6*300)
							b.Tail.Tip.X += x - b.Box.X
							b.Tail.Tip.Y += y - b.Box.Y
							b.Box.X, b.Box.Y = x, y
							if l, _ = layoutStatsFor(t, e, b, lang); len(sheet) < 60 {
								sheet = append(sheet, l)
							}
						}
					}
				}
			}
		}
	}
	writeQA(t, "lettering-shapes.png", 2660, 3020, sheet)
}

// QA: a 14 pt Shout and a long Thought used to cut into burst spikes and
// cloud lobes because every shape was inset like a rectangle.
func TestShoutAndThoughtTextStaysOffTheSpikesAndLobes(t *testing.T) {
	e := testEngine(t)
	for _, tc := range []struct {
		kind, shape, text string
		size              float64
	}{
		{pagedoc.BubbleShout, pagedoc.ShapeBurst, "WAIT! Don't you dare leave without me!", 14},
		{pagedoc.BubbleShout, pagedoc.ShapeBurst, "WAIT! Don't you dare leave without me!", 0},
		{pagedoc.BubbleThought, pagedoc.ShapeCloud, "If the tide turns before sunset, the bridge will be gone and so will the last ferry home.", 14},
		{pagedoc.BubbleThought, pagedoc.ShapeCloud, "If the tide turns before sunset, the bridge will be gone.", 30},
	} {
		for _, box := range []pagedoc.Rect{{X: 100, Y: 100, W: 300, H: 200}, {X: 100, Y: 100, W: 180, H: 180}, {X: 100, Y: 100, W: 520, H: 160}} {
			b := pagedoc.Bubble{ID: "b", Kind: tc.kind, Shape: tc.shape, Box: box, Style: pagedoc.BubbleStyle{Size: tc.size, AutoFit: true},
				Texts: map[string]pagedoc.BubbleText{"en": {Text: tc.text}}}
			l, err := e.Layout(b, "en")
			if err != nil {
				t.Fatal(err)
			}
			if l.Overflow {
				t.Fatalf("%s %+v: overflow at %.1f", tc.shape, box, l.FontSize)
			}
			if p, ok := insideOutline(t, b, l); !ok {
				t.Fatalf("%s %+v size %.1f: glyph at %.1f,%.1f leaves the outline", tc.shape, box, l.FontSize, p.X, p.Y)
			}
			if strings.Join(linesOf(l), " ") == "" {
				t.Fatal("no text set")
			}
		}
	}
	// Without auto-fit an oversized setting is reported, never silently clipped.
	b := pagedoc.Bubble{ID: "b", Kind: pagedoc.BubbleShout, Shape: pagedoc.ShapeBurst, Box: pagedoc.Rect{X: 0, Y: 0, W: 200, H: 120},
		Style: pagedoc.BubbleStyle{Size: 40}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "WAIT! Don't you dare leave without me!"}}}
	if l, err := e.Layout(b, "en"); err != nil || !l.Overflow {
		t.Fatalf("oversized burst text must report overflow: %v %v", l.Overflow, err)
	}
}

func linesOf(l BubbleLayout) []string {
	var out []string
	for _, line := range l.Lines {
		if !line.Ruby {
			out = append(out, line.Text)
		}
	}
	return out
}
