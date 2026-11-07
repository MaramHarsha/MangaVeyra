package lettering

import (
	"errors"
	"image/color"
	"math"
	"sort"
	"sync"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/tdewolff/canvas"
)

// engine owns immutable font data. The mutex also protects the shaper's
// internal caches and lazy font loading when export jobs lay out pages
// concurrently.
type engine struct {
	fonts map[string]*loadedFont
	mu    sync.Mutex
}

var _ Engine = (*engine)(nil)

// NewEngine loads only bundled, licensed fonts; no host font or network lookup
// occurs. Comic Neue and Bangers load now; the CJK and Devanagari faces load
// the first time a bubble needs them.
func NewEngine() (Engine, error) {
	fonts, err := newFontSet()
	if err != nil {
		return nil, err
	}
	for _, key := range []string{FontComic, FontSFX} {
		if _, err := fonts[key].load(); err != nil {
			return nil, err
		}
	}
	return &engine{fonts: fonts}, nil
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// layoutStats is internal evidence for tests: missing glyphs, fonts used and
// the line breaks chosen.
type layoutStats struct {
	notdef int
	fonts  map[string]bool
	lines  []string
}

func (e *engine) Layout(b pagedoc.Bubble, lang string) (BubbleLayout, error) {
	l, _, err := e.layout(b, lang)
	return l, err
}

func (e *engine) layout(b pagedoc.Bubble, lang string) (BubbleLayout, layoutStats, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var stats layoutStats
	switch b.Kind {
	case pagedoc.BubbleSpeech, pagedoc.BubbleThought, pagedoc.BubbleShout, pagedoc.BubbleWhisper, pagedoc.BubbleCaption, pagedoc.BubbleSFX:
	default:
		return BubbleLayout{}, stats, errors.New("lettering: invalid bubble kind")
	}
	if len(b.Texts) > 0 {
		lang = pagedoc.TextLanguage(b.Texts, lang)
	}
	key := fontFor(b, lang)
	info, ok := lookupFont(key)
	if !ok {
		return BubbleLayout{}, stats, errors.New("lettering: unknown font")
	}
	r := b.Box
	if !finite(r.X) || !finite(r.Y) || !finite(r.W) || !finite(r.H) || r.W <= 0 || r.H <= 0 || !finite(b.Style.Rotate) || !finite(b.Style.Size) || b.Style.Size < 0 || b.Style.Size > 1000 {
		return BubbleLayout{}, stats, errors.New("lettering: invalid geometry")
	}
	strokeWidth := 3.0
	if b.Kind == pagedoc.BubbleWhisper {
		strokeWidth = 1.5
	}
	area := newTextArea(b, strokeWidth)
	box := area.inner()
	size := b.Style.Size
	if size == 0 {
		size = math.Min(48, math.Max(18, math.Min(box.W, box.H)*.22))
		if b.Kind == pagedoc.BubbleSFX {
			size = math.Min(100, box.H*.8)
		}
	}
	vertical := b.Vertical && pagedoc.VerticalAllowed(lang)
	t := b.Texts[lang]
	textRunes := len([]rune(t.Text))
	for _, ruby := range t.Ruby {
		if ruby.Start < 0 || ruby.End <= ruby.Start || ruby.End > textRunes || len([]rune(ruby.Text)) > pagedoc.MaxTextRunes {
			return BubbleLayout{}, stats, errors.New("lettering: invalid ruby")
		}
	}
	if textRunes > pagedoc.MaxTextRunes {
		return BubbleLayout{}, stats, errors.New("lettering: text too long")
	}
	st, err := e.shape(t.Text, lang, key, vertical)
	if err != nil {
		return BubbleLayout{}, stats, err
	}
	stats.notdef, stats.fonts = st.notdef, st.fonts
	rubies := make([]*shapedText, len(t.Ruby))
	for i, ruby := range t.Ruby {
		if rubies[i], err = e.shape(ruby.Text, lang, key, vertical); err != nil {
			return BubbleLayout{}, stats, err
		}
		stats.notdef += rubies[i].notdef
		for k := range rubies[i].fonts {
			stats.fonts[k] = true
		}
	}
	minSize := math.Min(size, 12.0)
	gap := 1.25
	if len(t.Ruby) > 0 {
		gap = 1.6
	}
	if !vertical {
		// Scripts with tall marks (Devanagari) need more leading.
		gap = math.Max(gap, (st.ascent+st.descent)*1.06)
	}
	var set setting
	overflow := false
	for {
		set = e.set(b, st, t.Ruby, rubies, area, size, gap, vertical)
		if set.inside || textRunes == 0 {
			break
		}
		if !b.Style.AutoFit || size <= minSize {
			overflow = true
			break
		}
		size = math.Max(minSize, size-1)
	}
	l := BubbleLayout{BubbleID: b.ID, Lang: lang, TextBox: area.bounds(), Font: key, FontFamily: info.family, FontWeight: info.weight,
		FontSize: size, LineHeight: size * gap, Fill: "#FFFFFF", Stroke: "#070A12", StrokeWidth: strokeWidth, TextColor: "#070A12",
		Lines: set.lines, Overflow: overflow, Rotate: b.Style.Rotate, RotationCenter: pagedoc.Point{X: r.X + r.W/2, Y: r.Y + r.H/2}}
	shape, err := bubblePath(b)
	if err != nil {
		return BubbleLayout{}, stats, err
	}
	l.ShapePath = shape
	if b.Style.Stroke {
		l.TextStroke = "#FFFFFF"
		l.TextStrokeWidth = textStrokeWidth(size)
	}
	stats.lines = set.texts
	l.TextPath = set.path.ToSVG()
	return l, stats, nil
}

func textStrokeWidth(size float64) float64 { return math.Max(1, size*.06) }

// setting is the text set at one size: positioned lines, their outlines and
// whether every glyph (with its text stroke) lies inside the bubble's area.
type setting struct {
	lines  []TextLine
	texts  []string
	path   *canvas.Path
	inside bool
}

// set wraps the text against the area's chords at size, places it and draws
// it. Horizontal lines are centred on the bubble and follow a round shape's
// curve; vertical columns run right to left from one shared top edge. The
// final check measures the drawn outlines, so auto-fit never accepts a size
// whose glyphs cross the outline.
func (e *engine) set(b pagedoc.Bubble, st *shapedText, ruby []pagedoc.Ruby, rubies []*shapedText, area textArea, size, gap float64, vertical bool) setting {
	stroke := 0.0
	if b.Style.Stroke {
		stroke = textStrokeWidth(size) / 2
	}
	rubySize := size * .45
	rubyBand := 0.0
	if len(ruby) > 0 {
		rubyBand = rubySize * 1.1
	}
	pitch := size * gap
	maxLines := int(math.Floor(area.across(vertical)/pitch + 1e-9))
	// slot returns line i's position across the block of n lines and the
	// band its ink occupies across the reading direction.
	slot := func(i, n int) (pos, lo, hi float64) {
		block := float64(n) * pitch
		if vertical {
			left := area.cx + block/2 - float64(i+1)*pitch
			pos = left + size/2 + (pitch-size-rubyBand)/2
			lo, hi = pos-st.inkHalf*size, pos+st.inkHalf*size
			if rubyBand > 0 {
				hi = math.Max(hi, pos+size/2+rubyBand)
			}
			return pos, lo, hi
		}
		top := area.cy - block/2 + float64(i)*pitch
		pos = top + rubyBand + (pitch-rubyBand-(st.ascent+st.descent)*size)/2 + st.ascent*size
		return pos, pos - st.inkTop*size - rubyBand, pos + st.inkBottom*size
	}
	// limits returns each line's start and length along the reading
	// direction for a block of n lines, or false when a line has no room.
	limits := func(n int) ([]float64, []float64, bool) {
		starts, lengths := make([]float64, n), make([]float64, n)
		top := math.Inf(-1)
		for i := 0; i < n; i++ {
			_, lo, hi := slot(i, n)
			a, z, ok := area.span(lo-stroke, hi+stroke, vertical)
			if !ok {
				return nil, nil, false
			}
			a, z = a+stroke, z-stroke
			if z <= a {
				return nil, nil, false
			}
			starts[i], lengths[i] = a, z-a
			top = math.Max(top, a)
		}
		if vertical {
			for i := range lengths {
				lengths[i] -= top - starts[i]
				starts[i] = top
			}
		}
		return starts, lengths, true
	}
	var lines []textLine
	var starts, lengths []float64
	found := false
	for n := 1; n <= maxLines && !found; n++ {
		_, ls, ok := limits(n)
		if !ok {
			break
		}
		lines = st.wrapLines(func(i int) float64 {
			if i < len(ls) {
				return ls[i] / size
			}
			return 0
		})
		if len(lines) > n {
			continue
		}
		var okM bool
		if starts, lengths, okM = limits(len(lines)); !okM {
			continue
		}
		found = true
		for i, line := range lines {
			found = found && line.advance*size <= lengths[i]+.01
		}
	}
	if !found {
		// No size-n block fits: set the text against the widest chord so the
		// overflow is visible, centred on the bubble.
		width := area.inner().W
		if vertical {
			width = area.inner().H
		}
		lines = st.wrap(width / size)
		starts, lengths = make([]float64, len(lines)), make([]float64, len(lines))
		for i := range lines {
			lengths[i] = width
			starts[i] = area.cx - width/2
			if vertical {
				starts[i] = area.cy - width/2
			}
		}
	}
	out := setting{path: &canvas.Path{}, inside: found}
	add := func(c *pathCollector) {
		if c.path.Empty() {
			return
		}
		bounds := c.path.Bounds()
		bounds.X0, bounds.Y0, bounds.X1, bounds.Y1 = bounds.X0-stroke, bounds.Y0-stroke, bounds.X1+stroke, bounds.Y1+stroke
		out.inside = out.inside && area.contains(bounds)
		out.path = out.path.Append(c.path)
	}
	type placed struct {
		textLine
		x, y float64
	}
	positions := make([]placed, len(lines))
	for i, line := range lines {
		pos, _, _ := slot(i, len(lines))
		var x, y float64
		if vertical {
			x, y = pos, starts[i]
		} else {
			w := line.advance * size
			x, y = starts[i]+(lengths[i]-w)/2, pos
			if b.Style.Align == "left" {
				x = starts[i]
			} else if b.Style.Align == "right" {
				x = starts[i] + lengths[i] - w
			}
		}
		positions[i] = placed{line, x, y}
		text := string(st.runes[line.start:line.end])
		out.lines = append(out.lines, TextLine{Text: text, X: x, Y: y, Vertical: vertical, FontSize: size})
		out.texts = append(out.texts, text)
		c := &pathCollector{path: &canvas.Path{}}
		st.draw(c, line.start, line.end, x, y, size)
		add(c)
	}
	for ri, r := range ruby {
		// A ruby annotation spanning wrapped lines is split proportionally across
		// those lines. Every annotation rune is drawn exactly once.
		rr := []rune(r.Text)
		for _, pos := range positions {
			a, z := max(pos.start, r.Start), min(pos.end, r.End)
			if a >= z {
				continue
			}
			ra := int(math.Round(float64(a-r.Start) * float64(len(rr)) / float64(r.End-r.Start)))
			rz := int(math.Round(float64(z-r.Start) * float64(len(rr)) / float64(r.End-r.Start)))
			if ra >= rz {
				continue
			}
			rs := rubies[ri]
			prefix, base := st.advance(pos.start, a)*size, st.advance(a, z)*size
			length := rs.advance(ra, rz) * rubySize
			x, y := pos.x, pos.y
			if vertical {
				x += size/2 + rubyBand/2
				y += prefix + (base-length)/2
			} else {
				x += prefix + (base-length)/2
				y -= st.ascent*size + rubySize*.2
			}
			out.lines = append(out.lines, TextLine{Text: string(rr[ra:rz]), X: x, Y: y, Vertical: vertical, Ruby: true, FontSize: rubySize})
			c := &pathCollector{path: &canvas.Path{}}
			rs.draw(c, ra, rz, x, y, rubySize)
			add(c)
		}
	}
	return out
}

// pathCollector gathers shaped outlines in page space; both SVG and canvas
// receive these paths rather than measuring or shaping the text a second time.
type pathCollector struct{ path *canvas.Path }

func (r *pathCollector) add(p *canvas.Path, m canvas.Matrix) {
	if p.Empty() {
		return
	}
	r.path = r.path.Append(p.Transform(m))
}
func ellipse(x, y, w, h float64) *canvas.Path {
	p := &canvas.Path{}
	k := .5522847498307936
	cx, cy := x+w/2, y+h/2
	rx, ry := w/2, h/2
	p.MoveTo(cx+rx, cy)
	p.CubeTo(cx+rx, cy+k*ry, cx+k*rx, cy+ry, cx, cy+ry)
	p.CubeTo(cx-k*rx, cy+ry, cx-rx, cy+k*ry, cx-rx, cy)
	p.CubeTo(cx-rx, cy-k*ry, cx-k*rx, cy-ry, cx, cy-ry)
	p.CubeTo(cx+k*rx, cy-ry, cx+rx, cy-k*ry, cx+rx, cy)
	p.Close()
	return p
}
func bubblePath(b pagedoc.Bubble) (string, error) {
	r := b.Box
	var p *canvas.Path
	switch b.Shape {
	case pagedoc.ShapeNone:
		return "", nil
	case pagedoc.ShapeRect:
		p = &canvas.Path{}
		p.MoveTo(r.X, r.Y)
		p.LineTo(r.X+r.W, r.Y)
		p.LineTo(r.X+r.W, r.Y+r.H)
		p.LineTo(r.X, r.Y+r.H)
		p.Close()
	case pagedoc.ShapeEllipse:
		p = ellipse(r.X, r.Y, r.W, r.H)
	case pagedoc.ShapeCloud, pagedoc.ShapeBurst:
		p = &canvas.Path{}
		n := 72
		if b.Shape == pagedoc.ShapeBurst {
			n = 32
		}
		for i := 0; i < n; i++ {
			a := 2 * math.Pi * float64(i) / float64(n)
			radius := 1.0
			if b.Shape == pagedoc.ShapeCloud {
				radius = .94 + .06*math.Cos(12*a)
			} else if i%2 != 0 {
				radius = .82
			}
			x, y := r.X+r.W/2+math.Cos(a)*r.W/2*radius, r.Y+r.H/2+math.Sin(a)*r.H/2*radius
			if i == 0 {
				p.MoveTo(x, y)
			} else {
				p.LineTo(x, y)
			}
		}
		p.Close()
	default:
		return "", errors.New("lettering: unknown bubble shape")
	}
	if b.Tail.Enabled {
		tip := b.Tail.Tip
		if !finite(tip.X) || !finite(tip.Y) {
			return "", errors.New("lettering: invalid tail")
		}
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		dx, dy := tip.X-cx, tip.Y-cy
		d := math.Hypot(dx, dy)
		if d > 0 {
			if b.Kind == pagedoc.BubbleThought {
				boundary := 1 / math.Sqrt(math.Pow(dx/(r.W/2), 2)+math.Pow(dy/(r.H/2), 2))
				if boundary < 1 {
					for i := 1; i <= 3; i++ {
						fraction := float64(i) / 3.5
						t := boundary + (1-boundary)*fraction
						rad := math.Min(r.W, r.H) * .04 * (1 - fraction*.65)
						p = p.Append(ellipse(cx+dx*t-rad, cy+dy*t-rad, rad*2, rad*2))
					}
				}
			} else {
				nx, ny := -dy/d*math.Min(r.W, r.H)*.11, dx/d*math.Min(r.W, r.H)*.11
				t := &canvas.Path{}
				t.MoveTo(cx+nx, cy+ny)
				t.LineTo(tip.X, tip.Y)
				t.LineTo(cx-nx, cy-ny)
				t.Close()
				p = p.Or(t)
			}
		}
	}
	return p.ToSVG(), nil
}
func (e *engine) LayoutPage(p pagedoc.Page, lang string) ([]BubbleLayout, error) {
	out := make([]BubbleLayout, 0, len(p.Bubbles))
	byID := map[string]pagedoc.Bubble{}
	for _, b := range p.Bubbles {
		byID[b.ID] = b
	}
	for _, id := range e.ReadingOrder(p) {
		l, err := e.Layout(byID[id], lang)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}
func parseColor(s string) color.Color {
	switch s {
	case "#FFFFFF":
		return canvas.White
	case "":
		return color.Transparent
	default:
		return canvas.Hex(s)
	}
}
func (e *engine) Draw(ctx *canvas.Context, l BubbleLayout) error {
	if ctx == nil {
		return errors.New("lettering: nil canvas")
	}
	ctx.Push()
	defer ctx.Pop()
	ctx.RotateAbout(l.Rotate, l.RotationCenter.X, l.RotationCenter.Y)
	for _, v := range []struct {
		path, fill, stroke string
		width              float64
	}{{l.ShapePath, l.Fill, l.Stroke, l.StrokeWidth}, {l.TextPath, l.TextColor, l.TextStroke, l.TextStrokeWidth}} {
		if v.path == "" {
			continue
		}
		p, err := canvas.ParseSVGPath(v.path)
		if err != nil {
			return err
		}
		ctx.SetFillColor(parseColor(v.fill))
		ctx.SetStrokeColor(parseColor(v.stroke))
		ctx.SetStrokeWidth(v.width)
		ctx.DrawPath(0, 0, p)
	}
	return nil
}
func (e *engine) ReadingOrder(p pagedoc.Page) []string {
	rank := map[string]int{}
	panels := append([]pagedoc.Panel(nil), p.Panels...)
	sort.SliceStable(panels, func(i, j int) bool { return panels[i].Order < panels[j].Order })
	for i, pn := range panels {
		rank[pn.ID] = i
	}
	bs := append([]pagedoc.Bubble(nil), p.Bubbles...)
	sort.SliceStable(bs, func(i, j int) bool {
		a, b := bs[i], bs[j]
		if rank[a.PanelID] != rank[b.PanelID] {
			return rank[a.PanelID] < rank[b.PanelID]
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		ay, by := math.Floor(a.Box.Y/32), math.Floor(b.Box.Y/32)
		if ay != by {
			return ay < by
		}
		if a.Box.X != b.Box.X {
			if p.Direction == pagedoc.DirRTL {
				return a.Box.X > b.Box.X
			}
			return a.Box.X < b.Box.X
		}
		return a.ID < b.ID
	})
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.ID
	}
	return out
}
func overlap(a, b pagedoc.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}
func (e *engine) AutoPlace(p pagedoc.Page, panelID string, lines []Line, lang string, vertical bool) ([]pagedoc.Bubble, error) {
	if canonical := pagedoc.NormalizeContentLanguage(lang); canonical != "" {
		lang = canonical
	} else if lang == "" {
		lang = "en"
	}
	vertical = vertical && pagedoc.VerticalAllowed(lang)
	pn := p.Panel(panelID)
	if pn == nil {
		return nil, errors.New("lettering: panel not found")
	}
	if len(lines)+len(p.Bubbles) > pagedoc.MaxBubbles {
		return nil, errors.New("lettering: too many bubbles")
	}
	bounds := pagedoc.Bounds(pn.Points)
	for _, line := range lines {
		switch line.Kind {
		case pagedoc.BubbleSpeech, pagedoc.BubbleThought, pagedoc.BubbleCaption, pagedoc.BubbleSFX, pagedoc.BubbleShout, pagedoc.BubbleWhisper:
		default:
			return nil, errors.New("lettering: invalid dialogue kind")
		}
	}
	w, h := math.Min(360, bounds.W*.44), math.Min(220, bounds.H*.28)
	if vertical {
		w, h = math.Min(180, bounds.W*.28), math.Min(360, bounds.H*.44)
	}
	if w < 32 || h < 32 {
		if len(lines) == 0 {
			return nil, nil
		}
		return nil, errors.New("lettering: panel too small")
	}
	// A crowded panel gets uniformly smaller bubbles (auto-fit shrinks the
	// text) before placement gives up; bubbles never overlap.
	for _, scale := range []float64{1, .8, .64, .5, .4} {
		if w*scale < 32 || h*scale < 32 {
			break
		}
		if out, ok := e.place(p, pn, bounds, lines, lang, vertical, w*scale, h*scale); ok {
			return out, nil
		}
	}
	return nil, errors.New("lettering: no room for bubbles without overlap")
}

func (e *engine) place(p pagedoc.Page, pn *pagedoc.Panel, bounds pagedoc.Rect, lines []Line, lang string, vertical bool, w, h float64) ([]pagedoc.Bubble, bool) {
	var out []pagedoc.Bubble
	occupied := make([]pagedoc.Rect, 0, len(p.Bubbles)+len(lines))
	for _, b := range p.Bubbles {
		occupied = append(occupied, b.Box)
	}
	// find scans rows in reading order for the first free w×h box inside the panel.
	find := func() (pagedoc.Rect, bool) {
		step := math.Max(12, math.Min(w, h)*.2)
		for y := bounds.Y + 8; y+h <= bounds.Y+bounds.H-8; y += step {
			for off := 8.0; off+w <= bounds.W-8; off += step {
				x := bounds.X + off
				if p.Direction == pagedoc.DirRTL {
					x = bounds.X + bounds.W - off - w
				}
				r := pagedoc.Rect{X: x, Y: y, W: w, H: h}
				inside := true
				for _, pt := range []pagedoc.Point{{X: x, Y: y}, {X: x + w, Y: y}, {X: x, Y: y + h}, {X: x + w, Y: y + h}} {
					inside = inside && pagedoc.Contains(pn.Points, pt)
				}
				if !inside {
					continue
				}
				clear := true
				for _, o := range occupied {
					if overlap(r, o) {
						clear = false
						break
					}
				}
				if clear {
					return r, true
				}
			}
		}
		return pagedoc.Rect{}, false
	}
	for _, line := range lines {
		kind, shape := line.Kind, pagedoc.ShapeEllipse
		switch kind {
		case pagedoc.BubbleThought:
			shape = pagedoc.ShapeCloud
		case pagedoc.BubbleCaption:
			shape = pagedoc.ShapeRect
		case pagedoc.BubbleSFX:
			shape = pagedoc.ShapeNone
		case pagedoc.BubbleShout:
			shape = pagedoc.ShapeBurst
		}
		box, found := find()
		if !found {
			return nil, false
		}
		b := pagedoc.Bubble{ID: db.NewID(), PanelID: pn.ID, Kind: kind, Shape: shape, Order: len(p.Bubbles) + len(out), Box: box, SpeakerCharacterID: line.CharacterID, ScriptPanelID: line.ScriptPanelID, Vertical: vertical, Style: pagedoc.BubbleStyle{AutoFit: true}, Texts: map[string]pagedoc.BubbleText{lang: {Text: line.Text, Status: "machine", Source: pagedoc.ProvenanceAI}}}
		if kind == pagedoc.BubbleSFX {
			b.Style.Rotate = -12
			b.Style.Stroke = true
		}
		if kind == pagedoc.BubbleSpeech || kind == pagedoc.BubbleThought || kind == pagedoc.BubbleShout || kind == pagedoc.BubbleWhisper {
			b.Tail = pagedoc.Tail{Enabled: true, Tip: pagedoc.Point{X: box.X + box.W*.6, Y: box.Y + box.H + math.Min(24, bounds.H*.05)}}
		}
		out = append(out, b)
		occupied = append(occupied, box)
	}
	return out, true
}

func provide(d *core.Deps, e Engine) { core.Provide[Engine](d, e) }
