package lettering

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/rasterizer"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// One original sample line per content language.
var languageSamples = map[string]string{
	"en":      "The last ferry leaves at dusk. Hurry!",
	"ja":      "灯台の光が、海を照らした。「急げ！」",
	"ko":      "마지막 배는 해 질 무렵에 떠나요. 서둘러!",
	"zh-Hans": "最后一班渡船黄昏时出发。快点！",
	"zh-Hant": "最後一班渡船黃昏時出發。快點！",
	"es":      "¿Llegó el último ferri? ¡Sí, señora, ya zarpó!",
	"fr":      "« Où est le phare ? » Œuvre, garçon, très bien… Ça y est !",
	"de":      "Die Fähre wartet. Größe, Übermut und Äpfel – schön!",
	"pt":      "O último barco já saiu? Não, ainda está à espera — ação!",
	"hi":      "आख़िरी नाव शाम को निकलती है। जल्दी करो! क्षत्रिय नमस्ते",
}

func languageBubble(lang, text string) pagedoc.Bubble {
	return pagedoc.Bubble{ID: "b-" + lang, Kind: pagedoc.BubbleSpeech, Shape: pagedoc.ShapeEllipse,
		Box:   pagedoc.Rect{X: 40, Y: 40, W: 460, H: 300},
		Style: pagedoc.BubbleStyle{Size: 34, AutoFit: true},
		Texts: map[string]pagedoc.BubbleText{lang: {Text: text}}}
}

func layoutStatsFor(t *testing.T, e Engine, b pagedoc.Bubble, lang string) (BubbleLayout, layoutStats) {
	t.Helper()
	l, stats, err := e.(*engine).layout(b, lang)
	if err != nil {
		t.Fatalf("%s: %v", lang, err)
	}
	return l, stats
}

func pathBounds(t *testing.T, d string) canvas.Rect {
	t.Helper()
	p, err := canvas.ParseSVGPath(d)
	if err != nil {
		t.Fatal(err)
	}
	return p.Bounds()
}

func writeQA(t *testing.T, name string, w, h float64, layouts []BubbleLayout) {
	t.Helper()
	dir := os.Getenv("MV_RENDER_QA_DIR")
	if dir == "" {
		return
	}
	c := canvas.New(w, h)
	ctx := canvas.NewContext(c)
	ctx.SetCoordSystem(canvas.CartesianIV)
	ctx.SetFillColor(canvas.White)
	ctx.DrawPath(0, 0, canvas.Rectangle(w, h))
	e := &engine{}
	for _, l := range layouts {
		if err := e.Draw(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, rasterizer.Draw(c, canvas.DPMM(1), canvas.DefaultColorSpace)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEveryContentLanguageRendersRealGlyphs(t *testing.T) {
	e := testEngine(t)
	var sheet []BubbleLayout
	for i, lang := range pagedoc.ContentLanguages {
		sample := languageSamples[lang]
		if sample == "" {
			t.Fatalf("no sample for %s", lang)
		}
		for _, vertical := range []bool{false, true} {
			b := languageBubble(lang, sample)
			b.Vertical = vertical
			l, stats := layoutStatsFor(t, e, b, lang)
			if stats.notdef != 0 {
				t.Fatalf("%s vertical=%v: %d .notdef glyphs", lang, vertical, stats.notdef)
			}
			if l.Overflow || l.TextPath == "" || l.Lang != lang {
				t.Fatalf("%s vertical=%v: layout %+v", lang, vertical, l)
			}
			if want := languageFont(lang); want != "" && (l.Font != want || !stats.fonts[want]) {
				t.Fatalf("%s: font %s, used %v", lang, l.Font, stats.fonts)
			}
			bounds := pathBounds(t, l.TextPath)
			if bounds.X0 < b.Box.X || bounds.X1 > b.Box.X+b.Box.W || bounds.Y0 < b.Box.Y || bounds.Y1 > b.Box.Y+b.Box.H {
				t.Fatalf("%s vertical=%v: glyphs %+v escape bubble %+v", lang, vertical, bounds, b.Box)
			}
			if vertical && l.Lines[0].Vertical != pagedoc.VerticalAllowed(lang) {
				t.Fatalf("%s: vertical lettering must be honoured exactly where allowed", lang)
			}
			if !vertical || pagedoc.VerticalAllowed(lang) {
				b.Box.X, b.Box.Y = float64(40+(i%2)*500), float64(40+(i/2)*340)
				if vertical {
					b.Box.X += 1000
				}
				l, _ = layoutStatsFor(t, e, b, lang)
				sheet = append(sheet, l)
			}
		}
	}
	writeQA(t, "lettering-languages.png", 2000, 1760, sheet)
}

func TestFontDefaultsAndFamilies(t *testing.T) {
	e := testEngine(t)
	for _, tc := range []struct {
		lang, kind, font, family string
		weight                   int
	}{
		{"en", pagedoc.BubbleSpeech, FontComic, "Comic Neue", 700},
		{"es", pagedoc.BubbleSFX, FontSFX, "Bangers", 400},
		{"ja", pagedoc.BubbleSpeech, FontJP, "Noto Sans JP", 900},
		{"ja", pagedoc.BubbleSFX, FontJP, "Noto Sans JP", 900},
		{"ko", pagedoc.BubbleSFX, FontKO, "Noto Sans KR", 900},
		{"zh-Hans", pagedoc.BubbleSpeech, FontSC, "Noto Sans SC", 900},
		{"zh-Hant", pagedoc.BubbleSFX, FontTC, "Noto Sans TC", 900},
		{"hi", pagedoc.BubbleSFX, FontDeva, "Noto Sans Devanagari", 900},
		{"zh-TW", pagedoc.BubbleSpeech, FontTC, "Noto Sans TC", 900},
	} {
		b := languageBubble(tc.lang, "OK")
		b.Kind = tc.kind
		l, err := e.Layout(b, tc.lang)
		if err != nil || l.Font != tc.font || l.FontFamily != tc.family || l.FontWeight != tc.weight {
			t.Fatalf("%s/%s: %s %s %d %v", tc.lang, tc.kind, l.Font, l.FontFamily, l.FontWeight, err)
		}
	}
	for _, key := range pagedoc.LetteringFonts {
		b := languageBubble("en", "Ahoy 港 안녕 नमस्ते")
		b.Style.Font = key
		l, stats := layoutStatsFor(t, e, b, "en")
		info, _ := lookupFont(key)
		if l.Font != key || l.FontFamily != info.family || l.FontWeight != info.weight || stats.notdef != 0 {
			t.Fatalf("font key %s: %+v %d", key, l, stats.notdef)
		}
	}
}

func TestMixedScriptsFallBackPerRun(t *testing.T) {
	e := testEngine(t)
	b := languageBubble("en", "Hello 港 안녕 नमस्ते")
	l, stats := layoutStatsFor(t, e, b, "en")
	if l.Font != FontComic || stats.notdef != 0 {
		t.Fatalf("mixed text: font %s, %d .notdef", l.Font, stats.notdef)
	}
	for _, key := range []string{FontComic, FontJP, FontKO, FontDeva} {
		if !stats.fonts[key] {
			t.Fatalf("mixed text did not use %s: %v", key, stats.fonts)
		}
	}
	// Han follows the bubble language's regional font.
	for lang, key := range map[string]string{"ja": FontJP, "zh-Hans": FontSC, "zh-Hant": FontTC, "ko": FontKO} {
		b := languageBubble(lang, "港")
		b.Style.Font = FontComic
		_, stats := layoutStatsFor(t, e, b, lang)
		if !stats.fonts[key] || stats.notdef != 0 {
			t.Fatalf("Han in %s used %v", lang, stats.fonts)
		}
	}
	// Every Latin letter the European languages use exists; Ÿ (missing from
	// Comic Neue and Bangers) falls back to Noto Sans Devanagari's Latin.
	latin := "¿¡ñÑáéíóúàèìòùâêîôûäëïöüçÇãõÃÕœŒæÆßÿŸ«»‘’“”€…"
	for _, key := range []string{FontComic, FontSFX} {
		b := languageBubble("fr", latin)
		b.Style.Font = key
		_, stats := layoutStatsFor(t, e, b, "fr")
		if stats.notdef != 0 || !stats.fonts[key] || !stats.fonts[FontDeva] || len(stats.fonts) != 2 {
			t.Fatalf("%s Latin coverage: %d .notdef, fonts %v", key, stats.notdef, stats.fonts)
		}
		b.Texts["fr"] = pagedoc.BubbleText{Text: strings.ReplaceAll(latin, "Ÿ", "")}
		if _, stats = layoutStatsFor(t, e, b, "fr"); len(stats.fonts) != 1 {
			t.Fatalf("%s should cover European Latin alone: %v", key, stats.fonts)
		}
	}
}

func TestDevanagariShapesConjunctsAndKeepsClusters(t *testing.T) {
	e := testEngine(t).(*engine)
	shape := func(s string) *shapedText {
		st, err := e.shape(s, "hi", FontDeva, false)
		if err != nil {
			t.Fatal(err)
		}
		if st.notdef != 0 {
			t.Fatalf("%q: .notdef", s)
		}
		return st
	}
	ids := func(st *shapedText) []uint16 {
		var out []uint16
		for _, r := range st.runs {
			for _, g := range r.glyphs {
				out = append(out, g.ID)
			}
		}
		return out
	}
	ka, ssa := ids(shape("क"))[0], ids(shape("ष"))[0]
	conjunct := ids(shape("क्ष"))
	if len(conjunct) != 1 || conjunct[0] == ka || conjunct[0] == ssa {
		t.Fatalf("क्ष is not a conjunct glyph: %v (क %d, ष %d)", conjunct, ka, ssa)
	}
	// A half form joins क्त without the visible virama of a word-final क्.
	if halant, half := ids(shape("क्")), ids(shape("क्त")); len(halant) != 2 || len(half) > 2 || half[0] == ka {
		t.Fatalf("half forms: क् %v, क्त %v", halant, half)
	}
	// The i matra is stored after the consonant but drawn before it.
	ki := ids(shape("कि"))
	if len(ki) != 2 || ki[1] != ka || ki[0] == ka {
		t.Fatalf("कि matra was not reordered: %v", ki)
	}
	for _, s := range []string{"क्षत्रिय", "नमस्ते दुनिया"} {
		st := shape(s)
		if n := len(ids(st)); n >= len([]rune(s)) {
			t.Fatalf("%q: %d glyphs for %d runes, no conjunct shaping", s, n, len([]rune(s)))
		}
	}
	// A narrow box breaks only between words, then only between clusters.
	st := shape("नमस्ते दुनिया क्षत्रिय")
	for _, limit := range []float64{0.5, 1.5, 3} {
		for _, line := range st.wrap(limit) {
			if line.start == line.end {
				continue
			}
			first := st.runes[line.start]
			if unicode.Is(unicode.Mn, first) || unicode.Is(unicode.Mc, first) {
				t.Fatalf("limit %.1f: line %q starts with a mark", limit, string(st.runes[line.start:line.end]))
			}
			if line.start > 0 && st.runes[line.start-1] == '्' {
				t.Fatalf("limit %.1f: line %q splits a conjunct", limit, string(st.runes[line.start:line.end]))
			}
		}
	}
	words := st.wrap(3)
	for _, line := range words {
		if s := string(st.runes[line.start:line.end]); strings.Contains(s, " ") || !strings.Contains("नमस्ते दुनिया क्षत्रिय", s) {
			t.Fatalf("Hindi should wrap at spaces: %q", s)
		}
	}
}

func TestKoreanChineseJapaneseLineBreaking(t *testing.T) {
	e := testEngine(t).(*engine)
	ko, err := e.shape("마지막 배는 해 질 무렵에 떠나요", "ko", FontKO, false)
	if err != nil {
		t.Fatal(err)
	}
	words := map[string]bool{"마지막": true, "배는": true, "해": true, "질": true, "무렵에": true, "떠나요": true, "배는 해": true, "해 질": true, "질 무렵에": true}
	for _, line := range ko.wrap(4) {
		if s := string(ko.runes[line.start:line.end]); !words[s] {
			t.Fatalf("Korean must wrap at spaces, got %q", s)
		}
	}
	long, err := e.shape("가나다라마바사아자차카타파하", "ko", FontKO, false)
	if err != nil {
		t.Fatal(err)
	}
	if lines := long.wrap(3); len(lines) < 4 || lines[0].end-lines[0].start != 3 {
		t.Fatalf("an over-long Korean word breaks between syllables: %+v", lines)
	}
	for _, tc := range []struct{ lang, key, text string }{
		{"ja", FontJP, "灯台の光が、海を照らした。「急げ！」と彼女は言った。ー…"},
		{"zh-Hans", FontSC, "最后一班渡船黄昏时出发。“快点！”她说，（真的）。"},
		{"zh-Hant", FontTC, "最後一班渡船黃昏時出發。「快點！」她說，（真的）。"},
	} {
		st, err := e.shape(tc.text, tc.lang, tc.key, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []float64{2, 3, 4, 5, 7} {
			lines := st.wrap(limit)
			if len(lines) < 2 {
				t.Fatalf("%s: no ideographic line breaks at %.0f em", tc.lang, limit)
			}
			for i, line := range lines {
				s := []rune(string(st.runes[line.start:line.end]))
				if len(s) == 0 {
					continue
				}
				if i > 0 && noLineStart(s[0]) {
					t.Fatalf("%s %.0f em: line %q starts with a prohibited character", tc.lang, limit, string(s))
				}
				if i < len(lines)-1 && noLineEnd(s[len(s)-1]) {
					t.Fatalf("%s %.0f em: line %q ends with an opening bracket", tc.lang, limit, string(s))
				}
				// Only kinsoku may push a line past the limit (auto-fit then shrinks it).
				if line.advance > limit+2+1e-9 {
					t.Fatalf("%s %.0f em: line %q is too long", tc.lang, limit, string(s))
				}
			}
		}
	}
}

func TestVerticalPunctuationUsesVerticalForms(t *testing.T) {
	e := testEngine(t).(*engine)
	type glyphInfo struct {
		substituted, rotated bool
	}
	inspect := func(lang, key, s string) map[rune]glyphInfo {
		st, err := e.shape(s, lang, key, true)
		if err != nil || st.notdef != 0 {
			t.Fatalf("%s: %v %d", lang, err, st.notdef)
		}
		out := map[rune]glyphInfo{}
		for _, run := range st.runs {
			if !run.upright {
				t.Fatalf("%s: CJK punctuation must stay upright", lang)
			}
			for k, g := range run.glyphs {
				r := st.runes[run.runeAt[k]]
				out[r] = glyphInfo{g.ID != run.font.font.GlyphIndex(r), run.rotate[k]}
			}
		}
		return out
	}
	ja := inspect("ja", FontJP, "、。「」ー")
	for _, r := range "、。「」" {
		if !ja[r].substituted {
			t.Fatalf("ja %q has no vertical form", r)
		}
	}
	if !ja['ー'].rotated {
		t.Fatal("ja ー must turn in vertical text")
	}
	tc := inspect("zh-Hant", FontTC, "、。「」ー—")
	if !tc['「'].substituted || !tc['」'].substituted || !tc['ー'].rotated || !(tc['—'].rotated || tc['—'].substituted) {
		t.Fatalf("zh-Hant vertical forms: %+v", tc)
	}
	// Japanese and Simplified Chinese commas sit in the top right of their
	// cell; Traditional Chinese keeps them centred (Taiwan convention).
	cell := func(lang, key string) (canvas.Rect, float64, float64) {
		st, err := e.shape("、", lang, key, true)
		if err != nil {
			t.Fatal(err)
		}
		c := &pathCollector{path: &canvas.Path{}}
		st.draw(c, 0, 1, 500, 100, 100)
		return c.path.Bounds(), 500, 100
	}
	for _, tc := range []struct {
		lang, key string
		topRight  bool
	}{{"ja", FontJP, true}, {"zh-Hans", FontSC, true}, {"ko", FontKO, true}, {"zh-Hant", FontTC, false}} {
		b, cx, top := cell(tc.lang, tc.key)
		midX, midY := (b.X0+b.X1)/2, (b.Y0+b.Y1)/2
		if tc.topRight && (midX <= cx+10 || midY >= top+50) {
			t.Fatalf("%s 、 not top right: %+v", tc.lang, b)
		}
		if !tc.topRight && (midX < cx-20 || midX > cx+20 || midY < top+30 || midY > top+70) {
			t.Fatalf("%s 、 not centred: %+v", tc.lang, b)
		}
	}
	// Latin words in a vertical column are turned sideways; ー is a vertical bar.
	st, err := e.shape("ABー", "ja", FontJP, true)
	if err != nil {
		t.Fatal(err)
	}
	c := &pathCollector{path: &canvas.Path{}}
	st.draw(c, 2, 3, 500, 100, 100)
	if b := c.path.Bounds(); b.H() < 2*b.W() {
		t.Fatalf("ー did not turn vertical: %+v", b)
	}
	c = &pathCollector{path: &canvas.Path{}}
	st.draw(c, 0, 2, 500, 100, 100)
	if b := c.path.Bounds(); b.H() < b.W() {
		t.Fatalf("Latin did not turn sideways: %+v", b)
	}
	// Ruby keeps working in vertical Korean and Traditional Chinese layouts.
	for _, lang := range []string{"ko", "zh-Hant", "zh-Hans"} {
		b := languageBubble(lang, "灯台")
		b.Vertical = true
		b.Texts[lang] = pagedoc.BubbleText{Text: "灯台", Ruby: []pagedoc.Ruby{{Start: 0, End: 2, Text: "とうだい"}}}
		l, stats := layoutStatsFor(t, e, b, lang)
		var ruby, base *TextLine
		for i := range l.Lines {
			if l.Lines[i].Ruby {
				ruby = &l.Lines[i]
			} else {
				base = &l.Lines[i]
			}
		}
		if stats.notdef != 0 || ruby == nil || base == nil || !ruby.Vertical || ruby.X <= base.X {
			t.Fatalf("%s vertical ruby: %+v", lang, l.Lines)
		}
	}
	// Vertical is only honoured for languages that allow it.
	for _, lang := range []string{"en", "hi", "es"} {
		b := languageBubble(lang, languageSamples[lang])
		b.Vertical = true
		if l, _ := layoutStatsFor(t, e, b, lang); l.Lines[0].Vertical {
			t.Fatalf("%s set vertically", lang)
		}
	}
}

func TestAutoPlaceUsesContentLanguageContract(t *testing.T) {
	e := testEngine(t)
	p := pagedoc.New("p", "project", "chapter", 1, 1600, 2400, pagedoc.DirRTL)
	p.Panels = []pagedoc.Panel{{ID: "pn", Points: pagedoc.RectPoints(pagedoc.Rect{X: 80, Y: 80, W: 1440, H: 1000})}}
	for _, tc := range []struct {
		lang, key string
		vertical  bool
	}{{"zh-TW", "zh-Hant", true}, {"ko", "ko", true}, {"hi", "hi", false}, {"en", "en", false}, {"", "en", false}} {
		bs, err := e.AutoPlace(p, "pn", []Line{{Kind: "speech", Text: languageSamples[tc.key]}}, tc.lang, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := bs[0].Texts[tc.key]; !ok || bs[0].Vertical != tc.vertical {
			t.Fatalf("%q: texts %v vertical %v", tc.lang, bs[0].Texts, bs[0].Vertical)
		}
	}
	// A crowded panel shrinks bubbles instead of failing, never overlapping.
	small := p
	small.Panels = []pagedoc.Panel{{ID: "pn", Points: pagedoc.RectPoints(pagedoc.Rect{X: 80, Y: 80, W: 700, H: 700})}}
	var lines []Line
	for i := 0; i < 7; i++ {
		lines = append(lines, Line{Kind: "speech", Text: "Kree!"})
	}
	bs, err := e.AutoPlace(small, "pn", lines, "en", false)
	if err != nil || len(bs) != len(lines) {
		t.Fatalf("crowded panel: %d bubbles, %v", len(bs), err)
	}
	for i := range bs {
		for j := 0; j < i; j++ {
			if overlap(bs[i].Box, bs[j].Box) {
				t.Fatal("overlapping bubbles")
			}
		}
	}
}
