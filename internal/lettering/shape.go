package lettering

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-text/typesetting/segmenter"
	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/text"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// Shaping model
//
// Text is split into grapheme clusters (UAX #29, including the Indic conjunct
// rule), every cluster gets a script class and a font from the fallback order
// in fonts.go, and consecutive clusters with the same font, script group and
// orientation are shaped together by HarfBuzz (go-text, via canvas). Advances
// are kept in em units, so auto-fit scales one shaping result instead of
// reshaping per size; measurement, wrapping and drawing all read the same
// glyphs. Line breaks follow UAX #14, tailored for kinsoku and for Korean,
// which wraps at spaces.

type scriptClass uint8

const (
	classCommon scriptClass = iota
	classLatin
	classHan
	classKana
	classHangul
	classBopomofo
	classCJKSymbol // CJK punctuation and full-width forms
	classDevanagari
	classOther
)

// upright reports whether a class stays upright in a vertical column; every
// other class is set sideways (rotated 90° clockwise).
func (c scriptClass) upright() bool {
	switch c {
	case classHan, classKana, classHangul, classBopomofo, classCJKSymbol:
		return true
	}
	return false
}

func (c scriptClass) cjk() bool { return c.upright() }

// shapingGroup keeps classes that share a HarfBuzz script in one run.
func (c scriptClass) shapingGroup() scriptClass {
	switch c {
	case classHan, classKana, classBopomofo, classCJKSymbol:
		return classHan
	}
	return c
}

func (c scriptClass) script() text.Script {
	switch c.shapingGroup() {
	case classHan:
		return text.Han
	case classHangul:
		return text.Hangul
	case classDevanagari:
		return text.Devanagari
	case classLatin:
		return text.Latin
	}
	return text.ScriptInvalid // HarfBuzz guesses from the text
}

func inCJKSymbolBlock(r rune) bool {
	return r >= 0x3000 && r <= 0x303F || r >= 0x3200 && r <= 0x33FF || r >= 0xFE10 && r <= 0xFE1F ||
		r >= 0xFE30 && r <= 0xFE4F || r >= 0xFF00 && r <= 0xFF60 || r >= 0xFFE0 && r <= 0xFFEE
}

func runeClass(r rune) scriptClass {
	s := text.LookupScript(r)
	switch s {
	case text.Han:
		return classHan
	case text.Hiragana, text.Katakana:
		return classKana
	case text.Hangul:
		return classHangul
	case text.Bopomofo:
		return classBopomofo
	case text.Devanagari:
		return classDevanagari
	}
	if inCJKSymbolBlock(r) {
		return classCJKSymbol
	}
	switch s {
	case text.Latin:
		return classLatin
	case text.ScriptCommon, text.ScriptInherited, text.ScriptUnknown, text.ScriptInvalid:
		return classCommon
	}
	return classOther
}

// defaultClass resolves text that has no script of its own (digits,
// punctuation) when nothing around it decides.
func defaultClass(lang string) scriptClass {
	switch pagedoc.NormalizeContentLanguage(lang) {
	case "ja":
		return classKana
	case "ko":
		return classHangul
	case "zh-Hans", "zh-Hant":
		return classHan
	case "hi":
		return classDevanagari
	}
	return classLatin
}

func hardBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// ignorable runes are invisible and need no glyph of their own.
func ignorable(r rune) bool {
	return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r)
}

// Kinsoku shori: characters that may not start or end a line (JIS X 4051
// style, also applied to Chinese and Korean punctuation).
const (
	noStartChars = "，。、．！？；：」』）》〉】〕〗〙］｝｠ー…‥・々ゝゞヽヾぁぃぅぇぉっゃゅょゎゕゖァィゥェォッャュョヮヵヶ゛゜〟｡､･ｰ)]},.!?;:%”’‼⁇⁈⁉"
	noEndChars   = "「『（《〈【〔〖〘［｛｟｢([{“‘"
)

func noLineStart(r rune) bool { return strings.ContainsRune(noStartChars, r) }
func noLineEnd(r rune) bool   { return strings.ContainsRune(noEndChars, r) }

// rotatesInVertical marks glyphs that turn 90° in a vertical column when the
// font's vert feature has no upright replacement for them (long vowel mark,
// dashes, tildes, brackets).
func rotatesInVertical(r rune) bool {
	return strings.ContainsRune("ー－ｰ—―–‒‐-~〜～…‥_＿()[]{}<>（）［］｛｝〈〉《》「」『』【】〔〕〖〗｟｠｢｣=＝", r)
}

type grapheme struct {
	start, end int // rune range
	class      scriptClass
	key        string  // font key
	em         float64 // advance along the line, in em
	space      bool    // white space; hangs at the end of a line
	hard       bool    // hard line break; never drawn
	skip       bool    // control or default-ignorable only; never drawn
	breakOK    bool    // a soft line break may occur before this grapheme
	clusterOK  bool    // a shaping cluster starts here (emergency breaks)
}

type glyphRun struct {
	font       *loadedFont
	start, end int // rune range
	glyphs     []text.Glyph
	runeAt     []int      // rune offset of each glyph's cluster
	upright    bool       // shaped top to bottom (vertical layout)
	sideways   bool       // horizontal run turned 90° clockwise (vertical layout)
	rotate     []bool     // upright glyphs to turn 90° (no vert form)
	ink        []glyphInk // each glyph's exact outline bounds (fitting)
}

type shapedText struct {
	runes     []rune
	graphemes []grapheme
	gIndex    []int // rune offset → grapheme index
	runs      []glyphRun
	vertical  bool
	// Largest typographic ascent and descent (em) of the fonts used.
	ascent, descent float64
	notdef          int // glyphs a font could not map (unsupported scripts)
	fonts           map[string]bool
	// Ink extents of the shaped glyphs in em: above and below the baseline
	// (horizontal text), and the half width of a column about its axis
	// (vertical text). Fitting reserves exactly this much across each line.
	inkTop, inkBottom, inkHalf float64
}

type textLine struct {
	start, end int     // rune range drawn (trailing white space excluded)
	advance    float64 // em
}

// shape segments, classifies, picks fonts for and shapes s. primary is the
// bubble's font key.
func (e *engine) shape(s, lang, primary string, vertical bool) (*shapedText, error) {
	rs := []rune(s)
	st := &shapedText{runes: rs, vertical: vertical, gIndex: make([]int, len(rs)+1), fonts: map[string]bool{}}
	pf, err := e.fonts[primary].load()
	if err != nil {
		return nil, err
	}
	st.ascent, st.descent = pf.ascent, pf.descent
	if len(rs) == 0 {
		return st, nil
	}
	var seg segmenter.Segmenter
	seg.Init(rs)
	breaks := make([]bool, len(rs)+1)
	for li := seg.LineIterator(); li.Next(); {
		l := li.Line()
		breaks[l.Offset+len(l.Text)] = true
	}
	for gi := seg.GraphemeIterator(); gi.Next(); {
		g := gi.Grapheme()
		st.graphemes = append(st.graphemes, grapheme{start: g.Offset, end: g.Offset + len(g.Text)})
	}
	for i := range st.graphemes {
		g := &st.graphemes[i]
		g.hard, g.skip = true, true
		for _, r := range rs[g.start:g.end] {
			if !hardBreak(r) {
				g.hard = false
			}
			if r == '\t' || !unicode.IsControl(r) && !ignorable(r) {
				g.skip = false
			}
			if c := runeClass(r); c != classCommon && g.class == classCommon {
				g.class = c
			}
		}
		if g.hard {
			g.skip = false
		}
		g.space = !g.hard && !g.skip && (unicode.IsSpace(rs[g.start]) || rs[g.start] == '\t')
		for r := g.start; r < g.end; r++ {
			st.gIndex[r] = i
		}
	}
	st.gIndex[len(rs)] = len(st.graphemes)
	resolveCommon(st.graphemes, defaultClass(lang))
	for i := range st.graphemes {
		g := &st.graphemes[i]
		if g.hard || g.skip {
			continue
		}
		if g.key, err = e.pick(g.class, lang, primary, rs[g.start:g.end]); err != nil {
			return nil, err
		}
	}
	for i := 0; i < len(st.graphemes); {
		g := st.graphemes[i]
		if g.hard || g.skip {
			i++
			continue
		}
		up := vertical && g.class.upright()
		j := i + 1
		for ; j < len(st.graphemes); j++ {
			h := st.graphemes[j]
			if h.hard || h.skip || h.key != g.key || h.class.shapingGroup() != g.class.shapingGroup() || vertical && h.class.upright() != up {
				break
			}
		}
		run := e.shapeRun(rs, g.start, st.graphemes[j-1].end, g.key, lang, g.class, up, vertical && !up)
		st.runs = append(st.runs, run)
		st.fonts[g.key] = true
		if run.font.ascent > st.ascent {
			st.ascent = run.font.ascent
		}
		if run.font.descent > st.descent {
			st.descent = run.font.descent
		}
		i = j
	}
	for ri := range st.runs {
		run := &st.runs[ri]
		run.ink = make([]glyphInk, len(run.glyphs))
		for k, gl := range run.glyphs {
			if st.invisible(run.runeAt[k], gl.ID) {
				run.ink[k].empty = true
			} else {
				run.ink[k] = run.font.glyphInk(gl.ID)
			}
		}
	}
	clusterStart := make([]bool, len(rs)+1)
	for _, run := range st.runs {
		clusterStart[run.start] = true
		for k, gl := range run.glyphs {
			r := run.runeAt[k]
			clusterStart[r] = true
			adv := float64(gl.XAdvance)
			if run.upright {
				adv = -float64(gl.YAdvance)
			}
			st.graphemes[st.gIndex[r]].em += adv / run.font.upem
			if gl.ID == 0 && !ignorable(rs[r]) {
				st.notdef++
			}
		}
	}
	st.measureInk()
	keepAllHan := pagedoc.NormalizeContentLanguage(lang) == "ko"
	for i := range st.graphemes {
		g := &st.graphemes[i]
		g.clusterOK = i == 0 || clusterStart[g.start] || g.hard || g.skip || st.graphemes[i-1].hard || st.graphemes[i-1].skip
		if i == 0 {
			continue
		}
		prev := st.graphemes[i-1]
		ok := breaks[g.start] && g.clusterOK && !noLineStart(rs[g.start]) && !noLineEnd(rs[prev.end-1])
		if ok && !prev.space && keepAll(prev.class, g.class, keepAllHan) {
			ok = false
		}
		g.breakOK = ok
	}
	return st, nil
}

// keepAll forbids breaks inside Korean words (Hangul, and Hanja in Korean
// text): Korean wraps at spaces like Hindi does.
func keepAll(a, b scriptClass, korean bool) bool {
	if a == classHangul || b == classHangul {
		return true
	}
	return korean && (a.cjk() || b.cjk())
}

// resolveCommon gives script-neutral clusters (spaces, digits, punctuation)
// the class of the text before them, or after them at a paragraph start.
func resolveCommon(gs []grapheme, fallback scriptClass) {
	for start := 0; start < len(gs); {
		end := start
		for end < len(gs) && !gs[end].hard {
			end++
		}
		para := gs[start:end]
		prev := classCommon
		for i := range para {
			if para[i].class == classCommon {
				para[i].class = prev
			} else {
				prev = para[i].class
			}
		}
		next := fallback
		for i := len(para) - 1; i >= 0; i-- {
			if para[i].class == classCommon {
				para[i].class = next
			} else {
				next = para[i].class
			}
		}
		start = end + 1
	}
}

// pick returns the first font in the fallback order that has a glyph for
// every visible rune of the cluster. When no bundled font does (a script
// MangaVeyra does not support), the first choice is kept and shows .notdef.
func (e *engine) pick(c scriptClass, lang, primary string, rs []rune) (string, error) {
	order := candidates(c, lang, primary)
	for _, key := range order {
		f, err := e.fonts[key].load()
		if err != nil {
			return "", err
		}
		covered := true
		for _, r := range rs {
			if r != '\t' && (unicode.IsControl(r) || ignorable(r)) {
				continue
			}
			if r == '\t' {
				r = ' '
			}
			if !f.covers(r) {
				covered = false
				break
			}
		}
		if covered {
			return key, nil
		}
	}
	return order[0], nil
}

func (e *engine) shapeRun(rs []rune, start, end int, key, lang string, class scriptClass, upright, sideways bool) glyphRun {
	lf := e.fonts[key] // loaded by pick
	var b strings.Builder
	byteRune := make([]int, 0, (end-start)*3+1)
	for i := start; i < end; i++ {
		r := rs[i]
		if r == '\t' {
			r = ' '
		}
		for k := utf8.RuneLen(r); k > 0; k-- {
			byteRune = append(byteRune, i)
		}
		b.WriteRune(r)
	}
	byteRune = append(byteRune, end)
	face := lf.face
	face.Language = pagedoc.NormalizeContentLanguage(lang)
	if face.Language == "" {
		face.Language = lang
	}
	face.Script = class.script()
	face.Direction = text.LeftToRight
	if upright {
		face.Direction = text.TopToBottom
	}
	glyphs := face.Glyphs(b.String())
	run := glyphRun{font: lf, start: start, end: end, glyphs: glyphs, runeAt: make([]int, len(glyphs)), upright: upright, sideways: sideways}
	if upright {
		run.rotate = make([]bool, len(glyphs))
	}
	for k, g := range glyphs {
		c := int(g.Cluster)
		if c < 0 || c >= len(byteRune) {
			c = 0
		}
		r := byteRune[c]
		run.runeAt[k] = r
		// The vert feature replaced the glyph when an upright form exists;
		// otherwise turn dashes, the long vowel mark and brackets by hand.
		if upright && rotatesInVertical(rs[r]) && g.ID == lf.font.GlyphIndex(rs[r]) {
			run.rotate[k] = true
		}
	}
	return run
}

// wrap breaks the text into lines no longer than limit (em). Every line
// starts at a UAX #14 break opportunity allowed by kinsoku and keep-all; a
// cluster that alone is wider than the limit breaks only between shaping
// clusters, never inside a grapheme.
func (st *shapedText) wrap(limit float64) []textLine {
	return st.wrapLines(func(int) float64 { return limit })
}

// wrapLines is wrap with a limit per line index, so text follows a round
// bubble's curve: lines near its top and bottom are shorter.
func (st *shapedText) wrapLines(limitFor func(line int) float64) []textLine {
	lines, _ := st.wrapCapped(limitFor, -1)
	return lines
}

// wrapCapped is wrapLines that stops as soon as it has more than maxLines
// lines when maxLines >= 0 (fitting only needs to know that the text does
// not fit that block); the lines it returns up to then are identical. work
// counts the graphemes visited.
func (st *shapedText) wrapCapped(limitFor func(line int) float64, maxLines int) ([]textLine, int) {
	gs := st.graphemes
	n := len(gs)
	var lines []textLine
	work := 0
	emit := func(a, b int) {
		for b > a && (gs[b-1].space || gs[b-1].skip) {
			b--
		}
		l := textLine{start: len(st.runes), end: len(st.runes)}
		if a < n {
			l.start, l.end = gs[a].start, gs[a].start
		}
		if a < b {
			l.end = gs[b-1].end
		}
		for k := a; k < b; k++ {
			if !gs[k].hard && !gs[k].skip {
				l.advance += gs[k].em
			}
		}
		lines = append(lines, l)
	}
	start, soft := 0, false
	for start < n {
		if maxLines >= 0 && len(lines) > maxLines {
			return lines, work
		}
		if soft {
			for start < n && (gs[start].space || gs[start].skip) {
				start++
				work++
			}
			if start == n {
				break
			}
		}
		width, lastBreak, j := 0.0, -1, start
		limit := limitFor(len(lines))
		done := true
		for ; j < n; j++ {
			work++
			g := gs[j]
			if g.hard {
				emit(start, j)
				start, soft, done = j+1, false, false
				break
			}
			if j > start && g.breakOK {
				lastBreak = j
			}
			if !g.space && !g.skip && j > start && width+g.em > limit+1e-9 {
				end := lastBreak
				if end <= start {
					var w int
					end, w = st.emergencyBreak(start, j)
					work += w
				}
				emit(start, end)
				start, soft, done = end, true, false
				if end < n && gs[end].hard {
					start, soft = end+1, false
				}
				break
			}
			if !g.skip {
				width += g.em
			}
		}
		if done {
			emit(start, n)
			break
		}
	}
	if len(lines) == 0 {
		lines = []textLine{{start: 0, end: 0}}
	}
	return lines, work
}

// emergencyBreak finds a break for text with no allowed break opportunity
// before j: the latest cluster boundary that keeps kinsoku, else the next one
// after j (the line overflows and auto-fit shrinks the text) rather than
// starting a line with closing punctuation. work counts the graphemes it
// examined.
func (st *shapedText) emergencyBreak(start, j int) (int, int) {
	gs := st.graphemes
	clean := func(k int) bool {
		return gs[k].clusterOK && !noLineStart(st.runes[gs[k].start]) && !noLineEnd(st.runes[gs[k-1].end-1])
	}
	for k := j; k > start; k-- {
		if clean(k) {
			return k, j - k + 1
		}
	}
	for k := j + 1; k < len(gs); k++ {
		if gs[k].hard || clean(k) {
			return k, k - start
		}
	}
	return len(gs), len(gs) - start
}

// measureInk records how far the shaped glyphs reach across a line, using
// each glyph's outline bounds and its position (offsets, sideways turn or
// manual rotation) exactly as draw places it.
func (st *shapedText) measureInk() {
	for _, run := range st.runs {
		f := run.font
		for k, g := range run.glyphs {
			if st.invisible(run.runeAt[k], g.ID) {
				continue
			}
			x0, y0, x1, y1 := f.glyphBounds(g.ID)
			if x0 == x1 && y0 == y1 {
				continue // white space
			}
			xo, yo := float64(g.XOffset)/f.upem, float64(g.YOffset)/f.upem
			switch {
			case !st.vertical:
				st.inkTop = math.Max(st.inkTop, y1+yo)
				st.inkBottom = math.Max(st.inkBottom, -(y0 + yo))
			case run.sideways:
				c := f.capHeight / 2
				st.inkHalf = math.Max(st.inkHalf, math.Max(math.Abs(y0+yo-c), math.Abs(y1+yo-c)))
			case run.rotate[k]:
				mid := (f.ascent - f.descent) / 2
				st.inkHalf = math.Max(st.inkHalf, math.Max(math.Abs(y0-mid), math.Abs(y1-mid)))
			default:
				st.inkHalf = math.Max(st.inkHalf, math.Max(math.Abs(x0+xo), math.Abs(x1+xo)))
			}
		}
	}
}

// advance returns the em advance of the visible graphemes starting in [a, z).
func (st *shapedText) advance(a, z int) float64 {
	w := 0.0
	for i := st.gIndex[min(max(a, 0), len(st.runes))]; i < len(st.graphemes) && st.graphemes[i].start < z; i++ {
		g := st.graphemes[i]
		if g.start >= a && !g.hard && !g.skip {
			w += g.em
		}
	}
	return w
}

// glyphSink receives every visible glyph draw places: font f's glyph id,
// scaled by scale and moved by (x, y) in font orientation (y up), then
// transformed by m into page space. pathCollector builds the outlines;
// inkBox only measures them (fitting).
type glyphSink interface {
	glyph(f *loadedFont, id uint16, ink *glyphInk, x, y, scale float64, m canvas.Matrix)
}

// draw places the glyphs of runes [start, end) at size (page pixels).
// Horizontal text starts at baseline origin (x, y); vertical text is a
// column centred on x whose top is y.
func (st *shapedText) draw(c glyphSink, start, end int, x, y, size float64) {
	pen := 0.0
	for ri := range st.runs {
		run := &st.runs[ri]
		if run.end <= start || run.start >= end {
			continue
		}
		scale := size / run.font.upem
		switch {
		case !st.vertical || run.sideways:
			var m canvas.Matrix
			if !st.vertical {
				m = canvas.Identity.Translate(x+pen, y).Scale(1, -1)
			} else {
				// Sideways: centre the cap height on the column axis, turn 90° clockwise.
				capHeight := run.font.capHeight * size
				m = canvas.Identity.Translate(x-capHeight/2, y+pen).Rotate(90).Scale(1, -1)
			}
			w := 0.0
			for k, g := range run.glyphs {
				r := run.runeAt[k]
				if r < start || r >= end {
					continue
				}
				if !st.invisible(r, g.ID) {
					c.glyph(run.font, g.ID, &run.ink[k], w+float64(g.XOffset)*scale, float64(g.YOffset)*scale, scale, m)
				}
				w += float64(g.XAdvance) * scale
			}
			pen += w
		default:
			for k, g := range run.glyphs {
				r := run.runeAt[k]
				if r < start || r >= end {
					continue
				}
				adv := -float64(g.YAdvance) * scale
				if !st.invisible(r, g.ID) {
					if run.rotate[k] {
						// Turn the horizontal glyph about the centre of its em box.
						hadv := float64(run.font.font.GlyphAdvance(g.ID)) * scale
						mid := (run.font.ascent - run.font.descent) / 2 * size
						c.glyph(run.font, g.ID, &run.ink[k], 0, 0, scale, canvas.Identity.Translate(x, y+pen+adv/2).Rotate(90).Translate(-hadv/2, mid).Scale(1, -1))
					} else {
						c.glyph(run.font, g.ID, &run.ink[k], float64(g.XOffset)*scale, float64(g.YOffset)*scale, scale, canvas.Identity.Translate(x, y+pen).Scale(1, -1))
					}
				}
				pen += adv
			}
		}
	}
}

// invisible glyphs (default-ignorable runes the font does not map) are not
// drawn at all rather than as .notdef boxes.
func (st *shapedText) invisible(r int, id uint16) bool {
	return id == 0 && ignorable(st.runes[r])
}

// appendGlyph adds one glyph outline in font orientation (y up). TrueType
// contours are reversed so every font winds the same way and overlapping
// glyphs from different fonts never cancel under the non-zero fill rule.
func appendGlyph(p *canvas.Path, f *loadedFont, id uint16, x, y, scale float64) *canvas.Path {
	q := &canvas.Path{}
	if err := f.font.GlyphPath(q, id, 0, x, y, scale, 0); err != nil || q.Empty() {
		return p
	}
	if f.font.IsTrueType {
		q = q.Reverse()
	}
	return p.Append(q)
}
