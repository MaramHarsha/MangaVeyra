package lettering

import (
	"context"
	"math"

	"github.com/tdewolff/canvas"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// Fitting
//
// Auto-fit tries sizes from the requested size down to the minimum in
// one-pixel steps and keeps the first size at which the wrapped text fits the
// bubble's area with every drawn glyph inside it (ADR 0008 §8). The result
// must stay exactly what that scan chooses: fit is not monotonic in the size
// (a smaller size re-wraps and re-centres the block), so a bisection could
// pick a different size. What made the scan slow was drawing every glyph
// outline at every size; here a size is decided without drawing:
//
//  1. room rejects, in O(1), a size whose chords cannot hold the text's
//     unbreakable advance in any number of lines;
//  2. line counts whose chords cannot hold it are not wrapped;
//  3. a wrap stops as soon as it needs more lines than the block has;
//  4. inkInside places each glyph's cached exact outline bounds exactly as
//     draw would place the glyph; a box within inkSlack of the area's edge is
//     drawn and measured exactly.
//
// Only the chosen size is drawn. Every step is counted against a fixed work
// budget (graphemes wrapped, line slots measured, glyphs placed), so a
// bubble whose sizes all fail late cannot spin: once the budget is spent the
// text is set at the minimum size and reported as overflowing if it still
// does not fit there. Realistic text never reaches the budget (the golden
// corpus is checked for it).

// fitBudget is the work one bubble's size scan may spend, and then again the
// minimum-size attempt that follows an exhausted scan. One unit is roughly
// one grapheme wrapped or one glyph placed; the worst bubbles within
// pagedoc's limits need about a tenth of it.
const fitBudget = 6_000_000

// inkSlack (page pixels) is how close a measured ink box may come to the
// area's edge before it is drawn and measured exactly. Bounds measured from
// cached outlines differ from drawn ones by rounding only (about 1e-10 px
// at the largest page coordinates), so a box farther from the edge than
// this decides the same way the drawn outline does.
const inkSlack = 1e-6

// fitStats is internal evidence for tests and benchmarks.
type fitStats struct {
	sizes     int  // sizes tried
	work      int  // work units spent
	exact     int  // ink boxes too close to the edge to measure, so drawn
	mismatch  int  // final drawings that disagreed with the measurement (never expected)
	exhausted bool // the budget ran out; the text was set at the minimum size
}

type fitter struct {
	e        *engine
	b        pagedoc.Bubble
	st       *shapedText
	ruby     []pagedoc.Ruby
	rubies   []*shapedText
	area     textArea
	gap      float64
	vertical bool
	minEm    float64 // advance every wrap keeps on some line (em)
	limit    int     // work allowed so far
	stats    fitStats
	// scratch for limits
	starts, lengths []float64
}

// arrangement is the text wrapped and positioned at one size, before any
// glyph is drawn.
type arrangement struct {
	size, stroke, rubySize, rubyBand, pitch float64
	lines                                   []textLine
	starts, lengths                         []float64
	found                                   bool // every line fits its chord in a block of len(lines) lines
	fits                                    bool // found, and every glyph lies inside the area
	aborted                                 bool // the work budget ran out before the size was decided
}

// fit returns the size the scan chooses (the first, from size down to
// minSize, at which the text fits; without auto-fit, or at minSize, the
// last size tried, overflowing), the text set and drawn at it, and whether
// it overflows.
func (f *fitter) fit(ctx context.Context, size, minSize float64, empty bool) (float64, setting, bool, error) {
	for {
		a, err := f.scan(ctx, size, minSize, empty)
		if err != nil {
			return 0, setting{}, false, err
		}
		if !a.found {
			f.overflowLines(&a)
		}
		f.e.mu.Lock()
		set := f.draw(&a)
		f.e.mu.Unlock()
		if set.inside == a.fits {
			return a.size, set, !a.fits && !empty, nil
		}
		// The drawing disagrees with the measurement, which inkSlack rules
		// out; should it happen, the drawing decides, as it always did.
		f.stats.mismatch++
		if set.inside || empty {
			return a.size, set, false, nil
		}
		if !f.b.Style.AutoFit || a.size <= minSize {
			return a.size, set, true, nil
		}
		size = math.Max(minSize, a.size-1)
	}
}

// scan tries sizes from size down and returns the arrangement at the size it
// stops at.
func (f *fitter) scan(ctx context.Context, size, minSize float64, empty bool) (arrangement, error) {
	for {
		if err := ctx.Err(); err != nil {
			return arrangement{}, err
		}
		a := f.try(size)
		switch {
		case a.fits || empty:
			return a, nil
		case a.aborted:
			// Out of budget: set the minimum size (with a fresh budget for
			// that one attempt) instead of scanning on. Without auto-fit the
			// requested size overflows.
			f.stats.exhausted = true
			if !f.b.Style.AutoFit {
				return a, nil
			}
			f.limit = f.stats.work + fitBudget
			return f.try(minSize), nil
		case !f.b.Style.AutoFit || size <= minSize:
			return a, nil
		}
		size = math.Max(minSize, size-1)
	}
}

// try decides one size without drawing.
func (f *fitter) try(size float64) arrangement {
	f.stats.sizes++
	a := f.arrange(size)
	if a.found && !a.aborted {
		a.fits, a.aborted = f.inkInside(&a)
	}
	if a.aborted {
		a.lines, a.starts, a.lengths, a.found, a.fits = nil, nil, nil, false, false
	}
	return a
}

// arrange wraps the text at size into the smallest block of lines whose
// chords hold it: for n = 1, 2, … the text is wrapped against the chords of
// an n-line block, and the first wrap that needs at most n lines, each
// fitting its chord in a block of that many lines, is kept. A line count
// whose chords cannot hold the unbreakable advance is not wrapped (no wrap
// could fit it), and a wrap stops once it needs more than n lines.
func (f *fitter) arrange(size float64) arrangement {
	a := arrangement{size: size, pitch: size * f.gap, rubySize: size * .45}
	if f.b.Style.Stroke {
		a.stroke = textStrokeWidth(size) / 2
	}
	if len(f.ruby) > 0 {
		a.rubyBand = a.rubySize * 1.1
	}
	maxLines := int(math.Floor(f.area.across(f.vertical)/a.pitch + 1e-9))
	// Lines fit only if Σ(length+.01) ≥ Σ advance·size ≥ minEm·size; need
	// allows for rounding between the two sums.
	need := f.minEm*size*(1-1e-9) - 1e-6
	if maxLines < 1 || f.room(&a, maxLines) < need {
		return a
	}
	enough := false
	for n := 1; n <= maxLines && !a.found; n++ {
		if f.stats.work > f.limit {
			a.aborted = true
			return a
		}
		_, ls, ok := f.limits(&a, n, true)
		if !ok {
			break
		}
		if !enough {
			sum := 0.0
			for _, l := range ls {
				sum += l + .01
			}
			if enough = sum >= need; !enough {
				continue
			}
		}
		lines, work := f.st.wrapCapped(func(i int) float64 {
			if i < len(ls) {
				return ls[i] / size
			}
			return 0
		}, n)
		f.stats.work += work
		if len(lines) > n {
			continue
		}
		starts, lengths, ok := f.limits(&a, len(lines), false)
		if !ok {
			continue
		}
		found := true
		for i, line := range lines {
			found = found && line.advance*size <= lengths[i]+.01
		}
		if found {
			a.lines, a.starts, a.lengths, a.found = lines, starts, lengths, true
		}
	}
	return a
}

// room bounds Σ(length+.01) over the lines of any block of at most maxLines
// lines from above. A line's length is at most the area's chord through its
// baseline (or column axis), consecutive baselines are one pitch apart, and
// such chords of a convex area sum to at most its area/pitch plus its widest
// chord; twice the widest chord leaves room for rounding.
func (f *fitter) room(a *arrangement, maxLines int) float64 {
	along, across := 2*f.area.rx, 2*f.area.ry
	if f.vertical {
		along, across = across, along
	}
	surface := along * across
	if f.area.round {
		surface *= math.Pi / 4
	}
	return (surface/a.pitch+2*along)*(1+1e-9) + .01*float64(maxLines) + 1e-6
}

// slot returns line i's position across a block of n lines and the band its
// ink occupies across the reading direction. Horizontal lines are centred on
// the bubble; vertical columns run right to left.
func (f *fitter) slot(a *arrangement, i, n int) (pos, lo, hi float64) {
	st, size, pitch, rubyBand := f.st, a.size, a.pitch, a.rubyBand
	block := float64(n) * pitch
	if f.vertical {
		left := f.area.cx + block/2 - float64(i+1)*pitch
		pos = left + size/2 + (pitch-size-rubyBand)/2
		lo, hi = pos-st.inkHalf*size, pos+st.inkHalf*size
		if rubyBand > 0 {
			hi = math.Max(hi, pos+size/2+rubyBand)
		}
		return pos, lo, hi
	}
	top := f.area.cy - block/2 + float64(i)*pitch
	pos = top + rubyBand + (pitch-rubyBand-(st.ascent+st.descent)*size)/2 + st.ascent*size
	return pos, pos - st.inkTop*size - rubyBand, pos + st.inkBottom*size
}

// limits returns each line's start and length along the reading direction
// for a block of n lines, or false when a line has no room. Vertical
// columns share one top edge. With scratch the slices are the fitter's
// buffers, valid until the next scratch call.
func (f *fitter) limits(a *arrangement, n int, scratch bool) ([]float64, []float64, bool) {
	f.stats.work += n
	var starts, lengths []float64
	if scratch {
		if cap(f.starts) < n {
			f.starts, f.lengths = make([]float64, n, 2*n), make([]float64, n, 2*n)
		}
		starts, lengths = f.starts[:n], f.lengths[:n]
	} else {
		starts, lengths = make([]float64, n), make([]float64, n)
	}
	top := math.Inf(-1)
	for i := 0; i < n; i++ {
		_, lo, hi := f.slot(a, i, n)
		s, z, ok := f.area.span(lo-a.stroke, hi+a.stroke, f.vertical)
		if !ok {
			return nil, nil, false
		}
		s, z = s+a.stroke, z-a.stroke
		if z <= s {
			return nil, nil, false
		}
		starts[i], lengths[i] = s, z-s
		top = math.Max(top, s)
	}
	if f.vertical {
		for i := range lengths {
			lengths[i] -= top - starts[i]
			starts[i] = top
		}
	}
	return starts, lengths, true
}

// overflowLines sets text that no block fits against the widest chord, so
// the overflow is visible, centred on the bubble.
func (f *fitter) overflowLines(a *arrangement) {
	width := f.area.inner().W
	if f.vertical {
		width = f.area.inner().H
	}
	a.lines = f.st.wrap(width / a.size)
	a.starts, a.lengths = make([]float64, len(a.lines)), make([]float64, len(a.lines))
	for i := range a.lines {
		a.lengths[i] = width
		a.starts[i] = f.area.cx - width/2
		if f.vertical {
			a.starts[i] = f.area.cy - width/2
		}
	}
}

// placed is one line, or one ruby segment, positioned on the page.
type placed struct {
	st         *shapedText
	start, end int // rune range of st
	x, y, size float64
	ruby       bool
}

// place positions the arrangement's lines, then the ruby segments over them,
// and calls emit for each in drawing order until emit returns false. A ruby
// annotation spanning wrapped lines is split proportionally across those
// lines; every annotation rune is drawn exactly once.
func (f *fitter) place(a *arrangement, emit func(placed) bool) {
	st, size := f.st, a.size
	positions := make([]placed, len(a.lines))
	for i, line := range a.lines {
		pos, _, _ := f.slot(a, i, len(a.lines))
		var x, y float64
		if f.vertical {
			x, y = pos, a.starts[i]
		} else {
			w := line.advance * size
			x, y = a.starts[i]+(a.lengths[i]-w)/2, pos
			if f.b.Style.Align == "left" {
				x = a.starts[i]
			} else if f.b.Style.Align == "right" {
				x = a.starts[i] + a.lengths[i] - w
			}
		}
		positions[i] = placed{st: st, start: line.start, end: line.end, x: x, y: y, size: size}
		if !emit(positions[i]) {
			return
		}
	}
	for ri, r := range f.ruby {
		rs := f.rubies[ri]
		rn := len(rs.runes)
		for _, pos := range positions {
			f.stats.work++
			start, end := max(pos.start, r.Start), min(pos.end, r.End)
			if start >= end {
				continue
			}
			ra := int(math.Round(float64(start-r.Start) * float64(rn) / float64(r.End-r.Start)))
			rz := int(math.Round(float64(end-r.Start) * float64(rn) / float64(r.End-r.Start)))
			if ra >= rz {
				continue
			}
			prefix, base := st.advance(pos.start, start)*size, st.advance(start, end)*size
			length := rs.advance(ra, rz) * a.rubySize
			x, y := pos.x, pos.y
			if f.vertical {
				x += size/2 + a.rubyBand/2
				y += prefix + (base-length)/2
			} else {
				x += prefix + (base-length)/2
				y -= st.ascent*size + a.rubySize*.2
			}
			if !emit(placed{st: rs, start: ra, end: rz, x: x, y: y, size: a.rubySize, ruby: true}) {
				return
			}
		}
	}
}

// draw sets the arrangement's lines and ruby and draws their outlines
// (engine mutex held); inside is whether every glyph, with its text stroke,
// lies inside the area.
func (f *fitter) draw(a *arrangement) setting {
	out := setting{path: &canvas.Path{}, inside: a.found}
	f.place(a, func(p placed) bool {
		text := string(p.st.runes[p.start:p.end])
		out.lines = append(out.lines, TextLine{Text: text, X: p.x, Y: p.y, Vertical: f.vertical, Ruby: p.ruby, FontSize: p.size})
		if !p.ruby {
			out.texts = append(out.texts, text)
		}
		c := &pathCollector{path: &canvas.Path{}}
		p.st.draw(c, p.start, p.end, p.x, p.y, p.size)
		if !c.path.Empty() {
			bounds := c.path.Bounds()
			bounds.X0, bounds.Y0, bounds.X1, bounds.Y1 = bounds.X0-a.stroke, bounds.Y0-a.stroke, bounds.X1+a.stroke, bounds.Y1+a.stroke
			out.inside = out.inside && f.area.contains(bounds)
			out.path = out.path.Append(c.path)
		}
		return true
	})
	return out
}

// inkInside reports whether every line and ruby segment of a found
// arrangement, widened by the text stroke, lies inside the area, as draw
// would find, measuring each glyph's cached outline bounds where draw would
// put it. A box too close to the edge to decide from rounded bounds is drawn
// and measured exactly.
func (f *fitter) inkInside(a *arrangement) (inside, aborted bool) {
	inside = true
	f.place(a, func(p placed) bool {
		if f.stats.work > f.limit {
			aborted = true
			return false
		}
		var box inkBox
		p.st.draw(&box, p.start, p.end, p.x, p.y, p.size)
		f.stats.work += box.n + 1
		if !box.any {
			return true
		}
		r := box.r
		r.X0, r.Y0, r.X1, r.Y1 = r.X0-a.stroke, r.Y0-a.stroke, r.X1+a.stroke, r.Y1+a.stroke
		switch {
		case f.area.contains(canvas.Rect{X0: r.X0 - inkSlack, Y0: r.Y0 - inkSlack, X1: r.X1 + inkSlack, Y1: r.Y1 + inkSlack}):
			return true
		case f.area.outside(r, inkSlack):
			inside = false
			return false
		}
		f.stats.exact++
		c := &pathCollector{path: &canvas.Path{}}
		f.e.mu.Lock()
		p.st.draw(c, p.start, p.end, p.x, p.y, p.size)
		f.e.mu.Unlock()
		if c.path.Empty() {
			return true
		}
		bounds := c.path.Bounds()
		bounds.X0, bounds.Y0, bounds.X1, bounds.Y1 = bounds.X0-a.stroke, bounds.Y0-a.stroke, bounds.X1+a.stroke, bounds.Y1+a.stroke
		inside = f.area.contains(bounds)
		return inside
	})
	return inside, aborted
}

// inkBox measures what draw would draw: the union of the glyphs' outline
// bounds, each placed from its cached exact bounds with draw's arithmetic
// (glyphSink).
type inkBox struct {
	r   canvas.Rect
	any bool
	n   int // glyphs placed
}

func (b *inkBox) glyph(_ *loadedFont, _ uint16, ink *glyphInk, x, y, scale float64, m canvas.Matrix) {
	b.n++
	if ink.empty {
		return
	}
	r := canvas.Rect{X0: x + scale*ink.x0, Y0: y + scale*ink.y0, X1: x + scale*ink.x1, Y1: y + scale*ink.y1}.Transform(m)
	if b.any {
		r = r.Add(b.r)
	}
	b.r, b.any = r, true
}

// unbreakableEm is the advance (em) every wrap keeps on some line: all
// visible graphemes except white space, which may hang at a break (a
// negative white-space advance still counts, since a line may keep it).
func (st *shapedText) unbreakableEm() float64 {
	w := 0.0
	for _, g := range st.graphemes {
		switch {
		case g.hard || g.skip:
		case g.space:
			w += math.Min(g.em, 0)
		default:
			w += g.em
		}
	}
	return w
}
