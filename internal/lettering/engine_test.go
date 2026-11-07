package lettering

import (
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/tdewolff/canvas"
	"math"
	"strings"
	"testing"
)

func testBubble() pagedoc.Bubble {
	return pagedoc.Bubble{ID: "bubble", Kind: pagedoc.BubbleSpeech, Shape: pagedoc.ShapeEllipse, Box: pagedoc.Rect{X: 100, Y: 100, W: 400, H: 240}, Style: pagedoc.BubbleStyle{Size: 40, AutoFit: true}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "The ferry is waiting."}, "ja": {Text: "灯台への道", Ruby: []pagedoc.Ruby{{Start: 0, End: 2, Text: "とうだい"}}}}}
}
func testEngine(t *testing.T) Engine {
	t.Helper()
	e, err := NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestLayoutShapedPathsAutoFitAndOverflow(t *testing.T) {
	e := testEngine(t)
	b := testBubble()
	l, err := e.Layout(b, "en")
	if err != nil {
		t.Fatal(err)
	}
	if l.FontSize != 40 || l.FontFamily != "Comic Neue" || l.Overflow || l.TextPath == "" {
		t.Fatalf("layout: %+v", l)
	}
	path, err := canvas.ParseSVGPath(l.TextPath)
	if err != nil {
		t.Fatal(err)
	}
	bounds := path.Bounds()
	if bounds.W() < 100 || bounds.W() > l.TextBox.W || bounds.Y0 < l.TextBox.Y-1 || bounds.Y1 > l.TextBox.Y+l.TextBox.H+1 {
		t.Fatalf("glyphs not in page-pixel box: %+v %+v", bounds, l.TextBox)
	}
	b.Texts["en"] = pagedoc.BubbleText{Text: strings.Repeat("A long courier message ", 50)}
	l, err = e.Layout(b, "en")
	if err != nil || l.FontSize >= 40 {
		t.Fatalf("auto-fit failed: %+v %v", l, err)
	}
	b.Style.AutoFit = false
	l, err = e.Layout(b, "en")
	if err != nil || !l.Overflow || l.FontSize != 40 {
		t.Fatalf("overflow must be explicit: %+v %v", l, err)
	}
}
func TestHorizontalAndVerticalRubyAreActualOutlines(t *testing.T) {
	e0 := testEngine(t)
	local, err := e0.Layout(testBubble(), "ja-JP")
	if err != nil || local.Lang != "ja" {
		t.Fatal("BCP47 language fallback", local.Lang, err)
	}
	e := testEngine(t)
	for _, vertical := range []bool{false, true} {
		t.Run(map[bool]string{false: "horizontal", true: "vertical"}[vertical], func(t *testing.T) {
			b := testBubble()
			b.Vertical = vertical
			l, err := e.Layout(b, "ja")
			if err != nil {
				t.Fatal(err)
			}
			var ruby, base *TextLine
			for i := range l.Lines {
				line := &l.Lines[i]
				if line.Ruby {
					ruby = line
				} else if base == nil {
					base = line
				}
			}
			if ruby == nil || base == nil || ruby.FontSize >= base.FontSize || ruby.Vertical != vertical {
				t.Fatalf("missing ruby geometry: %+v", l)
			}
			if vertical && ruby.X <= base.X {
				t.Fatal("vertical ruby must sit to the right")
			}
			if !vertical && ruby.Y >= base.Y {
				t.Fatal("horizontal ruby must sit above")
			}
			without := b
			bt := without.Texts["ja"]
			bt.Ruby = nil
			without.Texts = map[string]pagedoc.BubbleText{"ja": bt}
			plain, err := e.Layout(without, "ja")
			if err != nil || plain.TextPath == l.TextPath {
				t.Fatal("ruby was not included in glyph outlines")
			}
			p, err := canvas.ParseSVGPath(l.TextPath)
			if err != nil || p.Bounds().Empty() {
				t.Fatal("invalid glyph path", err)
			}
			if p.Bounds().X0 < l.TextBox.X-50 || p.Bounds().X1 > l.TextBox.X+l.TextBox.W+50 {
				t.Fatalf("vertical glyphs escaped box: %+v %+v", p.Bounds(), l.TextBox)
			}
		})
	}
}
func TestAllKindsShapesTailsAndDraw(t *testing.T) {
	e := testEngine(t)
	for i, kind := range []string{pagedoc.BubbleSpeech, pagedoc.BubbleThought, pagedoc.BubbleShout, pagedoc.BubbleWhisper, pagedoc.BubbleCaption, pagedoc.BubbleSFX} {
		b := testBubble()
		b.Kind = kind
		b.Shape = []string{pagedoc.ShapeEllipse, pagedoc.ShapeCloud, pagedoc.ShapeBurst, pagedoc.ShapeEllipse, pagedoc.ShapeRect, pagedoc.ShapeNone}[i]
		b.Tail = pagedoc.Tail{Enabled: true, Tip: pagedoc.Point{X: 500, Y: 390}}
		b.Style.Rotate = -12
		b.Style.Stroke = true
		l, err := e.Layout(b, "en")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if kind != pagedoc.BubbleSFX && l.ShapePath == "" {
			t.Fatal("missing shape", kind)
		}
		if l.TextStrokeWidth <= 0 || l.Rotate != -12 {
			t.Fatal("missing stroke/rotation")
		}
		c := canvas.New(600, 500)
		ctx := canvas.NewContext(c)
		ctx.SetCoordSystem(canvas.CartesianIV)
		if err = e.Draw(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	b := testBubble()
	b.Box.W = math.Inf(1)
	if _, err := e.Layout(b, "en"); err == nil {
		t.Fatal("non-finite geometry accepted")
	}
	b = testBubble()
	b.Texts["ja"] = pagedoc.BubbleText{Text: "灯", Ruby: []pagedoc.Ruby{{Start: 0, End: 3, Text: "あ"}}}
	if _, err := e.Layout(b, "ja"); err == nil {
		t.Fatal("invalid ruby accepted")
	}
}
func TestAutoPlaceAndOrder(t *testing.T) {
	e := testEngine(t)
	p := pagedoc.New("p", "project", "chapter", 1, 1600, 2400, pagedoc.DirRTL)
	p.Panels = []pagedoc.Panel{{ID: "pn", Points: pagedoc.RectPoints(pagedoc.Rect{X: 80, Y: 80, W: 1440, H: 1000})}}
	bs, err := e.AutoPlace(p, "pn", []Line{{Kind: "speech", Text: "Run!"}, {Kind: "caption", Text: "At dusk"}, {Kind: "sfx", Text: "WHOOSH"}}, "en", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 3 {
		t.Fatal("missing bubbles")
	}
	for i, b := range bs {
		for j := 0; j < i; j++ {
			if overlap(b.Box, bs[j].Box) {
				t.Fatal("overlapping bubbles")
			}
		}
	}
	p.Bubbles = bs
	order := e.ReadingOrder(p)
	if order[0] != bs[0].ID || bs[2].Style.Rotate == 0 {
		t.Fatal("order/SFX missing")
	}
	if _, err = e.AutoPlace(p, "missing", nil, "en", false); err == nil {
		t.Fatal("unknown panel accepted")
	}
	// Equal authored order uses the page's reading direction.
	p.Bubbles = []pagedoc.Bubble{{ID: "left", PanelID: "pn", Box: pagedoc.Rect{X: 100, Y: 100}}, {ID: "right", PanelID: "pn", Box: pagedoc.Rect{X: 900, Y: 100}}}
	if e.ReadingOrder(p)[0] != "right" {
		t.Fatal("RTL order")
	}
	p.Direction = pagedoc.DirLTR
	if e.ReadingOrder(p)[0] != "left" {
		t.Fatal("LTR order")
	}
}
