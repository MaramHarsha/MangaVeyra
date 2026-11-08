package lettering

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// Fit golden corpus
//
// testdata/fit_golden.json holds the layouts the one-pixel-step auto-fit of
// commit 3612a17 produced (recorded at c732f23, before fitting was made
// fast). Every case stores the chosen size, overflow, line count and a hash
// of the whole BubbleLayout JSON (lines, positions, TextPath, ShapePath), so
// any change to a fitting decision or to a drawn outline fails the test.
// Regenerate only for an intended behaviour change:
//
//	MV_LETTERING_GOLDEN=record go test -run TestFitMatchesGoldenCorpus ./internal/lettering/

type fitCase struct {
	name string
	b    pagedoc.Bubble
	lang string
}

type fitGolden struct {
	Name     string  `json:"name"`
	Size     float64 `json:"size"`
	Overflow bool    `json:"overflow"`
	Lines    int     `json:"lines"`
	// Text hashes the line texts and their positions rounded to 0.01 px:
	// identical on every architecture.
	Text string `json:"text"`
	// Layout hashes the whole BubbleLayout JSON bit for bit. Fused
	// multiply-add (arm64) can move a coordinate by one ulp, so it is only
	// compared on goldenArch, where it was recorded.
	Layout string `json:"layout"`
}

const goldenArch = "arm64"

type corpusText struct {
	lang, text string
	ruby       []pagedoc.Ruby
}

func corpusTexts() []corpusText {
	var out []corpusText
	for _, lang := range pagedoc.ContentLanguages {
		s := languageSamples[lang]
		sep := " "
		if lang == "ja" || lang == "zh-Hans" || lang == "zh-Hant" {
			sep = ""
		}
		out = append(out, corpusText{lang: lang, text: s}, corpusText{lang: lang, text: strings.Repeat(s+sep, 3)})
	}
	return append(out,
		corpusText{lang: "en", text: "Pneumonoultramicroscopicsilicovolcanoconiosis! Run."},
		corpusText{lang: "en", text: strings.Repeat("Ferrywaitsatdusk", 8)},
		corpusText{lang: "en", text: "Wait!\nDon't go.\n\nPlease."},
		corpusText{lang: "en", text: "  leading and trailing spaces  "},
		corpusText{lang: "en", text: "Tab\tseparated\twords and fjord jiffy (quotes) “yes”"},
		corpusText{lang: "en", text: ""},
		corpusText{lang: "en", text: "A"},
		corpusText{lang: "en", text: "Hello 港 안녕 नमस्ते"},
		corpusText{lang: "fr", text: "¿¡ñÑáéíóú àèìòù âêîôû äëïöü çÇ ãõ œŒ æÆ ß ÿŸ «» ‘’ “” € …"},
		corpusText{lang: "ja", text: "「」『』（）ー…、。「急げ！」"},
		corpusText{lang: "ja", text: strings.Repeat("ー", 30)},
		corpusText{lang: "ja", text: "灯台の光が、海を照らした。", ruby: []pagedoc.Ruby{{Start: 0, End: 2, Text: "とうだい"}, {Start: 6, End: 7, Text: "うみ"}}},
		corpusText{lang: "zh-Hant", text: "最後一班渡船黃昏時出發。", ruby: []pagedoc.Ruby{{Start: 4, End: 6, Text: "ㄉㄨˋㄔㄨㄢˊ"}}},
		corpusText{lang: "ko", text: "가나다라마바사아자차카타파하가나다라마바사아자차카타파하"},
		corpusText{lang: "hi", text: "क्षत्रिय नमस्ते दुनिया क्षत्रिय नमस्ते दुनिया"},
		corpusText{lang: "de", text: "Donaudampfschifffahrtsgesellschaftskapitänsmütze – Übermut!"},
	)
}

type worstText struct{ name, lang, text string }

// repeatRunes repeats s until the result has exactly n runes.
func repeatRunes(s string, n int) string {
	rs := []rune(strings.Repeat(s, n/len([]rune(s))+1))
	return string(rs[:n])
}

// worstTexts are the maximum-length texts of the denial-of-service report:
// Latin words, Latin without spaces and Japanese.
func worstTexts() []worstText {
	return []worstText{
		{"latin", "en", repeatRunes("The last ferry leaves at dusk. Hurry! ", pagedoc.MaxTextRunes)},
		{"latin-nospace", "en", repeatRunes("Thelastferryleavesatdusk.Hurry!", pagedoc.MaxTextRunes)},
		{"japanese", "ja", repeatRunes(languageSamples["ja"], pagedoc.MaxTextRunes)},
	}
}

var corpusShapes = []struct{ kind, shape string }{
	{pagedoc.BubbleSpeech, pagedoc.ShapeEllipse}, {pagedoc.BubbleWhisper, pagedoc.ShapeEllipse},
	{pagedoc.BubbleThought, pagedoc.ShapeCloud}, {pagedoc.BubbleShout, pagedoc.ShapeBurst},
	{pagedoc.BubbleCaption, pagedoc.ShapeRect}, {pagedoc.BubbleSFX, pagedoc.ShapeNone},
	{pagedoc.BubbleSpeech, pagedoc.ShapeRect}, {pagedoc.BubbleSFX, pagedoc.ShapeEllipse},
}

var corpusBoxes = []pagedoc.Rect{
	{X: 40, Y: 40, W: 420, H: 260}, {X: 40, Y: 40, W: 200, H: 340}, {X: 40, Y: 40, W: 160, H: 100},
	{X: 40, Y: 40, W: 600, H: 600}, {X: 40, Y: 40, W: 90, H: 300}, {X: 40, Y: 40, W: 1000, H: 640},
	{X: 40, Y: 40, W: 64, H: 48}, {X: 40, Y: 40, W: 1200, H: 180},
}

var corpusSizes = []float64{0, 9.5, 14, 14 * 96.0 / 72, 34, 44, 120, 333}

// fitCorpus is a deterministic set of bubbles: every text in every shape at
// two sizes and both orientations, then pseudo-random combinations of text,
// shape, box, size, orientation, auto-fit, alignment, stroke, font and
// rotation from a fixed LCG (stable across Go versions).
func fitCorpus() []fitCase {
	texts := corpusTexts()
	var out []fitCase
	add := func(name string, t corpusText, shape int, box pagedoc.Rect, style pagedoc.BubbleStyle, vertical bool) {
		s := corpusShapes[shape]
		b := pagedoc.Bubble{ID: "fit", Kind: s.kind, Shape: s.shape, Box: box, Vertical: vertical, Style: style,
			Tail:  pagedoc.Tail{Enabled: s.kind != pagedoc.BubbleCaption && s.kind != pagedoc.BubbleSFX, Tip: pagedoc.Point{X: box.X + box.W*.7, Y: box.Y + box.H + 40}},
			Texts: map[string]pagedoc.BubbleText{t.lang: {Text: t.text, Ruby: t.ruby}}}
		out = append(out, fitCase{name: name, b: b, lang: t.lang})
	}
	for ti, t := range texts {
		for si := range corpusShapes {
			for _, size := range []float64{0, 34} {
				for _, vertical := range []bool{false, true} {
					add(fmt.Sprintf("grid/t%d/s%d/size%.0f/v%v", ti, si, size, vertical), t, si, corpusBoxes[0],
						pagedoc.BubbleStyle{Size: size, AutoFit: true, Stroke: corpusShapes[si].kind == pagedoc.BubbleSFX}, vertical)
				}
			}
		}
	}
	// Stress: the reviewer's worst cases (2000 runes, size 1000, auto-fit)
	// and long texts in large boxes.
	for _, w := range worstTexts() {
		for _, box := range []pagedoc.Rect{{X: 0, Y: 0, W: 4000, H: 4000}, {X: 0, Y: 0, W: 200, H: 200}} {
			add(fmt.Sprintf("stress/%s/%gx%g", w.name, box.W, box.H), corpusText{lang: w.lang, text: w.text}, 0, box,
				pagedoc.BubbleStyle{Size: 1000, AutoFit: true}, false)
		}
	}
	for ti, t := range texts {
		if ti%2 == 0 || ti >= 2*len(pagedoc.ContentLanguages) {
			continue // the three-sentence samples only
		}
		long := corpusText{lang: t.lang, text: strings.Repeat(t.text, 4)}
		for si, size := range []float64{400, 90} {
			add(fmt.Sprintf("long/t%d/size%g", ti, size), long, si*2, pagedoc.Rect{X: 0, Y: 0, W: 1000, H: 900},
				pagedoc.BubbleStyle{Size: size, AutoFit: true, Align: []string{"", "left"}[si]}, ti%4 == 1)
		}
	}
	x := uint64(0x5eed1e77e5)
	pick := func(n int) int {
		x = x*6364136223846793005 + 1442695040888963407
		return int((x >> 33) % uint64(n))
	}
	aligns := []string{"", "", "left", "right", "center"}
	fonts := []string{"", "", "", FontSFX, FontComic, FontJP, FontDeva}
	for i := 0; i < 2400; i++ {
		ti, si, bi, zi := pick(len(texts)), pick(len(corpusShapes)), pick(len(corpusBoxes)), pick(len(corpusSizes))
		style := pagedoc.BubbleStyle{Size: corpusSizes[zi], AutoFit: pick(5) != 0, Align: aligns[pick(len(aligns))],
			Stroke: pick(3) == 0, Font: fonts[pick(len(fonts))]}
		if pick(4) == 0 {
			style.Rotate = -12
		}
		vertical := pick(2) == 0
		add(fmt.Sprintf("mix/%d/t%d/s%d/b%d/size%g/v%v/fit%v/%s/stroke%v/%s", i, ti, si, bi, style.Size, vertical, style.AutoFit, style.Align, style.Stroke, style.Font),
			texts[ti], si, corpusBoxes[bi], style, vertical)
	}
	return out
}

func goldenOf(t *testing.T, c fitCase, l BubbleLayout) fitGolden {
	t.Helper()
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	var text strings.Builder
	fmt.Fprintf(&text, "%.6f %v\n", l.FontSize, l.Overflow)
	for _, line := range l.Lines {
		fmt.Fprintf(&text, "%q %v %v %.2f %.2f %.4f\n", line.Text, line.Ruby, line.Vertical, line.X, line.Y, line.FontSize)
	}
	textSum := sha256.Sum256([]byte(text.String()))
	return fitGolden{Name: c.name, Size: l.FontSize, Overflow: l.Overflow, Lines: len(linesOf(l)),
		Text: hex.EncodeToString(textSum[:12]), Layout: hex.EncodeToString(sum[:12])}
}

const goldenPath = "testdata/fit_golden.json"

// layoutCorpus lays out every case with workers engines in parallel (each
// engine serialises its own work).
func layoutCorpus(t *testing.T, cases []fitCase, workers int) []fitGolden {
	t.Helper()
	out := make([]fitGolden, len(cases))
	errs := make([]error, len(cases))
	layouts := make([]BubbleLayout, len(cases))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < workers; w++ {
		e := testEngine(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				layouts[i], errs[i] = e.Layout(cases[i].b, cases[i].lang)
			}
		}()
	}
	for i := range cases {
		next <- i
	}
	close(next)
	wg.Wait()
	for i, c := range cases {
		if errs[i] != nil {
			t.Fatalf("%s: %v", c.name, errs[i])
		}
		out[i] = goldenOf(t, c, layouts[i])
	}
	return out
}

func TestFitMatchesGoldenCorpus(t *testing.T) {
	cases := fitCorpus()
	got := layoutCorpus(t, cases, 4)
	if os.Getenv("MV_LETTERING_GOLDEN") == "record" {
		rows := make([]string, len(got))
		for i, g := range got {
			raw, err := json.Marshal(g)
			if err != nil {
				t.Fatal(err)
			}
			rows[i] = string(raw)
		}
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte("[\n"+strings.Join(rows, ",\n")+"\n]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d cases", len(got))
		return
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var want []fitGolden
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("corpus has %d cases, golden %d", len(got), len(want))
	}
	bad := 0
	for i := range want {
		g, w := got[i], want[i]
		if runtime.GOARCH != goldenArch {
			g.Layout, w.Layout = "", ""
		}
		if g != w {
			bad++
			if bad <= 20 {
				t.Errorf("%s:\n got  %+v\n want %+v", cases[i].name, got[i], want[i])
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d layouts differ from the pre-optimisation engine", bad, len(want))
	}
}
