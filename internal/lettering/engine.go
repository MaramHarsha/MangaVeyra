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
	pad := math.Max(8, math.Min(r.W, r.H)*.16)
	box := pagedoc.Rect{X: r.X + pad, Y: r.Y + pad, W: math.Max(1, r.W-2*pad), H: math.Max(1, r.H-2*pad)}
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
	minSize := math.Min(size, 12.0)
	gap := 1.25
	if len(t.Ruby) > 0 {
		gap = 1.6
	}
	if !vertical {
		// Scripts with tall marks (Devanagari) need more leading.
		gap = math.Max(gap, (st.ascent+st.descent)*1.06)
	}
	limit, across := box.W, box.H
	if vertical {
		limit, across = box.H, box.W
	}
	var lines []textLine
	overflow := false
	for {
		lines = st.wrap(limit / size)
		fits := float64(len(lines))*size*gap <= across
		for _, line := range lines {
			fits = fits && line.advance*size <= limit+.01
		}
		if fits {
			break
		}
		if !b.Style.AutoFit || size <= minSize {
			overflow = true
			break
		}
		size = math.Max(minSize, size-1)
	}
	l := BubbleLayout{BubbleID: b.ID, Lang: lang, TextBox: box, Font: key, FontFamily: info.family, FontWeight: info.weight,
		FontSize: size, LineHeight: size * gap, Fill: "#FFFFFF", Stroke: "#070A12", StrokeWidth: 3, TextColor: "#070A12",
		Lines: []TextLine{}, Overflow: overflow, Rotate: b.Style.Rotate, RotationCenter: pagedoc.Point{X: r.X + r.W/2, Y: r.Y + r.H/2}}
	if b.Kind == pagedoc.BubbleWhisper {
		l.StrokeWidth = 1.5
	}
	shape, err := bubblePath(b)
	if err != nil {
		return BubbleLayout{}, stats, err
	}
	l.ShapePath = shape
	if b.Style.Stroke {
		l.TextStroke = "#FFFFFF"
		l.TextStrokeWidth = math.Max(1, size*.06)
	}
	rubySize := size * .45
	rubyBand := 0.0
	if len(t.Ruby) > 0 {
		rubyBand = rubySize * 1.1
	}
	collector := &pathCollector{path: &canvas.Path{}}
	type placed struct {
		textLine
		x, y float64
	}
	positions := make([]placed, len(lines))
	block := float64(len(lines)) * size * gap
	// Vertical columns share one top edge (the block is centred as a whole).
	longest := 0.0
	for _, line := range lines {
		longest = math.Max(longest, line.advance*size)
	}
	for i, line := range lines {
		var x, y float64
		if vertical {
			// Columns run right to left; ruby takes a band on each column's right.
			slotLeft := box.X + box.W - (box.W-block)/2 - float64(i+1)*size*gap
			x = slotLeft + size/2 + (size*gap-size-rubyBand)/2
			y = box.Y + (box.H-longest)/2
		} else {
			w := line.advance * size
			x = box.X + (box.W-w)/2
			if b.Style.Align == "left" {
				x = box.X
			} else if b.Style.Align == "right" {
				x = box.X + box.W - w
			}
			slotTop := box.Y + (box.H-block)/2 + float64(i)*size*gap
			y = slotTop + rubyBand + (size*gap-rubyBand-(st.ascent+st.descent)*size)/2 + st.ascent*size
		}
		positions[i] = placed{line, x, y}
		l.Lines = append(l.Lines, TextLine{Text: string(st.runes[line.start:line.end]), X: x, Y: y, Vertical: vertical, FontSize: size})
		stats.lines = append(stats.lines, string(st.runes[line.start:line.end]))
		st.draw(collector, line.start, line.end, x, y, size)
	}
	for _, ruby := range t.Ruby {
		// A ruby annotation spanning wrapped lines is split proportionally across
		// those lines. Every annotation rune is drawn exactly once.
		rr := []rune(ruby.Text)
		for _, pos := range positions {
			a, z := max(pos.start, ruby.Start), min(pos.end, ruby.End)
			if a >= z {
				continue
			}
			ra := int(math.Round(float64(a-ruby.Start) * float64(len(rr)) / float64(ruby.End-ruby.Start)))
			rz := int(math.Round(float64(z-ruby.Start) * float64(len(rr)) / float64(ruby.End-ruby.Start)))
			if ra >= rz {
				continue
			}
			annotation := string(rr[ra:rz])
			rs, err := e.shape(annotation, lang, key, vertical)
			if err != nil {
				return BubbleLayout{}, stats, err
			}
			stats.notdef += rs.notdef
			for k := range rs.fonts {
				stats.fonts[k] = true
			}
			prefix, base := st.advance(pos.start, a)*size, st.advance(a, z)*size
			length := rs.advance(0, len(rs.runes)) * rubySize
			x, y := pos.x, pos.y
			if vertical {
				x += size/2 + rubyBand/2
				y += prefix + (base-length)/2
			} else {
				x += prefix + (base-length)/2
				y -= st.ascent*size + rubySize*.2
			}
			l.Lines = append(l.Lines, TextLine{Text: annotation, X: x, Y: y, Vertical: vertical, Ruby: true, FontSize: rubySize})
			rs.draw(collector, 0, len(rs.runes), x, y, rubySize)
		}
	}
	l.TextPath = collector.path.ToSVG()
	return l, stats, nil
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
