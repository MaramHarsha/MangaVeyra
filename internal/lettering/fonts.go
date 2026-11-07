package lettering

import (
	"embed"
	"fmt"
	"sync"

	"github.com/tdewolff/canvas"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// Every lettering font is an OFL-licensed WOFF2 embedded in the binary (see
// fonts/LICENSES.md). Nothing is read from the host or the network.
//
//go:embed fonts/*.woff2
var fontFiles embed.FS

type fontInfo struct {
	key, file, family string
	weight            int
}

// fontInfos is ordered as pagedoc.LetteringFonts.
var fontInfos = []fontInfo{
	{FontComic, "ComicNeue-Bold", "Comic Neue", 700},
	{FontSFX, "Bangers-Regular", "Bangers", 400},
	{FontJP, "NotoSansJP-Black", "Noto Sans JP", 900},
	{FontKO, "NotoSansKR-Black", "Noto Sans KR", 900},
	{FontSC, "NotoSansSC-Black", "Noto Sans SC", 900},
	{FontTC, "NotoSansTC-Black", "Noto Sans TC", 900},
	{FontDeva, "NotoSansDevanagari-Black", "Noto Sans Devanagari", 900},
}

func lookupFont(key string) (fontInfo, bool) {
	for _, f := range fontInfos {
		if f.key == key {
			return f, true
		}
	}
	return fontInfo{}, false
}

// loadedFont parses its font on first use: an English-only instance never
// spends memory on the CJK faces.
type loadedFont struct {
	info fontInfo
	once sync.Once
	font *canvas.Font
	face *canvas.FontFace // shaping face; its script/direction are set per run
	// Typographic metrics in em units (ascender positive, descender positive).
	ascent, descent, capHeight float64
	upem                       float64
	err                        error
}

func (l *loadedFont) load() (*loadedFont, error) {
	l.once.Do(func() {
		data, err := fontFiles.ReadFile("fonts/" + l.info.file + ".woff2")
		if err != nil {
			l.err = fmt.Errorf("lettering: bundled font %s: %w", l.info.key, err)
			return
		}
		f, err := canvas.LoadFont(data, 0, canvas.FontRegular)
		if err != nil {
			l.err = fmt.Errorf("lettering: bundled font %s: %w", l.info.key, err)
			return
		}
		l.font = f
		l.face = f.Face(10, canvas.Black)
		l.upem = float64(f.Head.UnitsPerEm)
		if l.upem <= 0 {
			l.err = fmt.Errorf("lettering: bundled font %s has no units per em", l.info.key)
			return
		}
		asc, desc, capHeight := 0.0, 0.0, 0.0
		if f.OS2 != nil {
			asc, desc, capHeight = float64(f.OS2.STypoAscender), -float64(f.OS2.STypoDescender), float64(f.OS2.SCapHeight)
		}
		if asc <= 0 && f.Hhea != nil {
			asc, desc = float64(f.Hhea.Ascender), -float64(f.Hhea.Descender)
		}
		if asc <= 0 {
			asc, desc = .88*l.upem, .12*l.upem
		}
		if capHeight <= 0 {
			capHeight = .7 * l.upem
		}
		l.ascent, l.descent, l.capHeight = asc/l.upem, desc/l.upem, capHeight/l.upem
	})
	return l, l.err
}

// covers reports whether the font maps r to a real glyph.
func (l *loadedFont) covers(r rune) bool { return l.font.GlyphIndex(r) != 0 }

func newFontSet() (map[string]*loadedFont, error) {
	if len(fontInfos) != len(pagedoc.LetteringFonts) {
		return nil, fmt.Errorf("lettering: font table does not match pagedoc.LetteringFonts")
	}
	set := make(map[string]*loadedFont, len(fontInfos))
	for i, info := range fontInfos {
		if pagedoc.LetteringFonts[i] != info.key {
			return nil, fmt.Errorf("lettering: font table does not match pagedoc.LetteringFonts")
		}
		if _, err := fontFiles.Open("fonts/" + info.file + ".woff2"); err != nil {
			return nil, fmt.Errorf("lettering: bundled font %s: %w", info.key, err)
		}
		set[info.key] = &loadedFont{info: info}
	}
	return set, nil
}

func cjkFont(key string) bool {
	return key == FontJP || key == FontKO || key == FontSC || key == FontTC
}

// languageFont is the font a content language letters in by default
// (plan 0004 contract): ja→jp, ko→ko, zh-Hans→sc, zh-Hant→tc, hi→deva.
// Latin-script languages return "".
func languageFont(lang string) string {
	switch pagedoc.NormalizeContentLanguage(lang) {
	case "ja":
		return FontJP
	case "ko":
		return FontKO
	case "zh-Hans":
		return FontSC
	case "zh-Hant":
		return FontTC
	case "hi":
		return FontDeva
	}
	return ""
}

// fontFor picks the bubble's font key: an explicit known style font, else the
// language default, else Bangers for Latin SFX and Comic Neue for the rest.
func fontFor(b pagedoc.Bubble, lang string) string {
	if _, ok := lookupFont(b.Style.Font); ok {
		return b.Style.Font
	}
	if key := languageFont(lang); key != "" {
		return key
	}
	if b.Kind == pagedoc.BubbleSFX {
		return FontSFX
	}
	return FontComic
}

// hanOrder lists the CJK fonts in the order a language prefers for Han
// characters; the first entry is the language-appropriate regional font.
func hanOrder(lang string) []string {
	switch pagedoc.NormalizeContentLanguage(lang) {
	case "ja":
		return []string{FontJP, FontTC, FontSC, FontKO}
	case "zh-Hans":
		return []string{FontSC, FontTC, FontJP, FontKO}
	case "zh-Hant":
		return []string{FontTC, FontSC, FontJP, FontKO}
	case "ko":
		return []string{FontKO, FontTC, FontSC, FontJP}
	}
	return []string{FontJP, FontSC, FontTC, FontKO}
}

// candidates is the fallback order for a grapheme of class c in a bubble whose
// font key is primary. Han always prefers the language's regional font; the
// remaining bundled fonts follow so that every supported script has a glyph.
func candidates(c scriptClass, lang, primary string) []string {
	var order []string
	cjkPrimary := ""
	if cjkFont(primary) {
		cjkPrimary = primary
	}
	langCJK := ""
	if cjkFont(languageFont(lang)) {
		langCJK = languageFont(lang)
	}
	switch c {
	case classHan:
		order = append(order, hanOrder(lang)[0], cjkPrimary)
		order = append(order, hanOrder(lang)...)
	case classKana, classCJKSymbol:
		order = append(order, langCJK, cjkPrimary, FontJP)
		order = append(order, hanOrder(lang)...)
	case classBopomofo:
		order = append(order, langCJK, cjkPrimary, FontTC, FontSC)
		order = append(order, hanOrder(lang)...)
	case classHangul:
		order = append(order, FontKO, cjkPrimary)
		order = append(order, hanOrder(lang)...)
	case classDevanagari:
		order = append(order, FontDeva)
	}
	order = append(order, primary, languageFont(lang), FontComic, FontSFX, FontDeva)
	order = append(order, hanOrder(lang)...)
	out := order[:0]
	seen := map[string]bool{"": true}
	for _, k := range order {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}
