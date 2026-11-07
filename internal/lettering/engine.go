package lettering

import (
	"embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/tdewolff/canvas"
)

//go:embed fonts/*.woff2
var fontFiles embed.FS

// engine owns immutable font data. The mutex also protects the font shaper's
// internal caches when multiple export jobs request layouts concurrently.
type engine struct {
	fonts map[string]*canvas.Font
	mu    sync.Mutex
}

var _ Engine = (*engine)(nil)

// NewEngine loads only bundled, licensed fonts; no host font or network lookup occurs.
func NewEngine() (Engine, error) {
	e := &engine{fonts: map[string]*canvas.Font{}}
	for key, name := range map[string]string{FontComic: "ComicNeue-Bold", FontSFX: "Bangers-Regular", FontJP: "NotoSansJP-Black"} {
		data, err := fontFiles.ReadFile("fonts/" + name + ".woff2")
		if err != nil {
			return nil, err
		}
		f, err := canvas.LoadFont(data, 0, canvas.FontRegular)
		if err != nil {
			return nil, fmt.Errorf("lettering: bundled font %s: %w", key, err)
		}
		e.fonts[key] = f
	}
	return e, nil
}

func fontFor(b pagedoc.Bubble, lang string) string {
	if b.Style.Font != "" {
		return b.Style.Font
	}
	if strings.HasPrefix(lang, "ja") {
		return FontJP
	}
	if b.Kind == pagedoc.BubbleSFX {
		return FontSFX
	}
	return FontComic
}
func (e *engine) face(key string, size float64) *canvas.FontFace {
	return e.fonts[key].Face(size*72/25.4, canvas.Black)
}
func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

type textRun struct {
	text       string
	start, end int
	x, y       float64
	vertical   bool
}

// splitText wraps by measured glyph advances, retaining rune offsets for ruby.
// A word is kept together where possible; Japanese punctuation never starts a line.
func splitText(text string, face *canvas.FontFace, width float64, vertical bool, size float64) []textRun {
	rs := []rune(text)
	var runs []textRun
	for start := 0; start < len(rs); {
		if rs[start] == '\n' {
			runs = append(runs, textRun{start: start, end: start})
			start++
			continue
		}
		end := start
		lastSpace := -1
		for end < len(rs) && rs[end] != '\n' {
			advance := face.TextWidth(string(rs[start : end+1]))
			if vertical {
				advance = float64(end-start+1) * size
			}
			if advance > width && end > start {
				break
			}
			if unicode.IsSpace(rs[end]) {
				lastSpace = end
			}
			end++
		}
		if end < len(rs) && rs[end] != '\n' && lastSpace > start && !vertical {
			end = lastSpace + 1
		}
		if end < len(rs) && strings.ContainsRune("、。，．！？）」』】〕〉》", rs[end]) {
			end++
		}
		if end == start {
			end++
		}
		runs = append(runs, textRun{text: strings.TrimRightFunc(string(rs[start:end]), unicode.IsSpace), start: start, end: end, vertical: vertical})
		start = end
		if start < len(rs) && rs[start] == '\n' {
			start++
		}
	}
	if len(runs) == 0 {
		runs = []textRun{{}}
	}
	return runs
}

func (e *engine) Layout(b pagedoc.Bubble, lang string) (BubbleLayout, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch b.Kind {
	case pagedoc.BubbleSpeech, pagedoc.BubbleThought, pagedoc.BubbleShout, pagedoc.BubbleWhisper, pagedoc.BubbleCaption, pagedoc.BubbleSFX:
	default:
		return BubbleLayout{}, errors.New("lettering: invalid bubble kind")
	}
	if lang == "" {
		if _, ok := b.Texts["en"]; ok {
			lang = "en"
		} else if _, ok := b.Texts["ja"]; ok {
			lang = "ja"
		} else {
			keys := make([]string, 0, len(b.Texts))
			for k := range b.Texts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if len(keys) > 0 {
				lang = keys[0]
			}
		}
	}
	if len(b.Texts) > 0 {
		if _, ok := b.Texts[lang]; !ok {
			if base := strings.SplitN(lang, "-", 2)[0]; b.Texts[base].Text != "" {
				lang = base
			} else if _, ok = b.Texts["en"]; ok {
				lang = "en"
			} else {
				keys := make([]string, 0, len(b.Texts))
				for k := range b.Texts {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				lang = keys[0]
			}
		}
	}
	key := fontFor(b, lang)
	if e.fonts[key] == nil {
		return BubbleLayout{}, errors.New("lettering: unknown font")
	}
	r := b.Box
	if !finite(r.X) || !finite(r.Y) || !finite(r.W) || !finite(r.H) || r.W <= 0 || r.H <= 0 || !finite(b.Style.Rotate) || !finite(b.Style.Size) || b.Style.Size < 0 || b.Style.Size > 1000 {
		return BubbleLayout{}, errors.New("lettering: invalid geometry")
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
	vertical := b.Vertical && strings.HasPrefix(lang, "ja")
	t := b.Texts[lang]
	for _, ruby := range t.Ruby {
		if ruby.Start < 0 || ruby.End <= ruby.Start || ruby.End > len([]rune(t.Text)) || len([]rune(ruby.Text)) > pagedoc.MaxTextRunes {
			return BubbleLayout{}, errors.New("lettering: invalid ruby")
		}
	}
	if len([]rune(t.Text)) > pagedoc.MaxTextRunes {
		return BubbleLayout{}, errors.New("lettering: text too long")
	}
	minSize := math.Min(size, 12.0)
	var runs []textRun
	overflow := false
	gap := 1.25
	if len(t.Ruby) > 0 {
		gap = 1.6
	}
	for {
		face := e.face(key, size)
		limit := box.W
		if vertical {
			limit = box.H
		}
		runs = splitText(t.Text, face, limit, vertical, size)
		fits := float64(len(runs))*size*gap <= box.H
		if vertical {
			fits = float64(len(runs))*size*gap <= box.W
		}
		for _, run := range runs {
			w := face.TextWidth(run.text)
			if vertical {
				w = float64(len([]rune(run.text))) * size
			}
			fits = fits && w <= limit+.01
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
	l := BubbleLayout{BubbleID: b.ID, Lang: lang, TextBox: box, Font: key, FontSize: size, LineHeight: size * gap, FontWeight: 700, Fill: "#FFFFFF", Stroke: "#070A12", StrokeWidth: 3, TextColor: "#070A12", Lines: []TextLine{}, Overflow: overflow, Rotate: b.Style.Rotate, RotationCenter: pagedoc.Point{X: r.X + r.W/2, Y: r.Y + r.H/2}}
	switch key {
	case FontJP:
		l.FontFamily = "Noto Sans JP"
		l.FontWeight = 900
	case FontSFX:
		l.FontFamily = "Bangers"
		l.FontWeight = 400
	default:
		l.FontFamily = "Comic Neue"
	}
	if b.Kind == pagedoc.BubbleWhisper {
		l.StrokeWidth = 1.5
	}
	shape, err := bubblePath(b)
	if err != nil {
		return BubbleLayout{}, err
	}
	l.ShapePath = shape
	if b.Style.Stroke {
		l.TextStroke = "#FFFFFF"
		l.TextStrokeWidth = math.Max(1, size*.06)
	}
	face := e.face(key, size)
	collector := &pathCollector{}
	for i := range runs {
		run := &runs[i]
		if vertical {
			run.x = box.X + box.W - (box.W-float64(len(runs))*size*gap)/2 - size*.5 - float64(i)*size*gap
			run.y = box.Y + (box.H-float64(len([]rune(run.text)))*size)/2
		} else {
			w := face.TextWidth(run.text)
			run.x = box.X + (box.W-w)/2
			if b.Style.Align == "left" {
				run.x = box.X
			} else if b.Style.Align == "right" {
				run.x = box.X + box.W - w
			}
			run.y = box.Y + (box.H-float64(len(runs))*size*gap)/2 + size + float64(i)*size*gap
		}
		l.Lines = append(l.Lines, TextLine{Text: run.text, X: run.x, Y: run.y, Vertical: vertical, FontSize: size})
		drawRun(collector, face, run.text, run.x, run.y, vertical, size)
	}
	rs := []rune(t.Text)
	for _, ruby := range t.Ruby {
		// A ruby annotation spanning wrapped lines is split proportionally across
		// those lines. Every annotation rune is drawn exactly once.
		rr := []rune(ruby.Text)
		for _, run := range runs {
			a, z := max(run.start, ruby.Start), min(run.end, ruby.End)
			if a >= z {
				continue
			}
			ra := int(math.Round(float64(a-ruby.Start) * float64(len(rr)) / float64(ruby.End-ruby.Start)))
			rz := int(math.Round(float64(z-ruby.Start) * float64(len(rr)) / float64(ruby.End-ruby.Start)))
			if ra >= rz {
				continue
			}
			annotation := string(rr[ra:rz])
			rubySize := size * .45
			rf := e.face(FontJP, rubySize)
			x, y := run.x, run.y
			if vertical {
				x += size * .65
				y += float64(a-run.start)*size + (float64(z-a)*size-float64(rz-ra)*rubySize)/2
			} else {
				prefix := face.TextWidth(string(rs[run.start:a]))
				base := face.TextWidth(string(rs[a:z]))
				x += prefix + (base-rf.TextWidth(annotation))/2
				y -= size * 1.05
			}
			l.Lines = append(l.Lines, TextLine{Text: annotation, X: x, Y: y, Vertical: vertical, Ruby: true, FontSize: rubySize})
			drawRun(collector, rf, annotation, x, y, vertical, rubySize)
		}
	}
	l.TextPath = collector.path.ToSVG()
	return l, nil
}

// collect shaped outlines in page space; both SVG and canvas receive these
// paths rather than measuring or shaping the text a second time.
type pathCollector struct{ path canvas.Path }

func (*pathCollector) Size() (float64, float64) { return 0, 0 }
func (r *pathCollector) RenderPath(p *canvas.Path, _ canvas.Style, m canvas.Matrix) {
	r.path = *r.path.Append(p.Copy().Transform(m))
}
func (r *pathCollector) RenderText(t *canvas.Text, m canvas.Matrix) { t.RenderTo(r, m, 0) }
func (*pathCollector) RenderImage(image.Image, canvas.Matrix)       {}
func drawRun(r *pathCollector, f *canvas.FontFace, s string, x, y float64, vertical bool, size float64) {
	if s == "" {
		return
	}
	if vertical {
		rt := canvas.NewRichText(f)
		rt.SetWritingMode(canvas.VerticalRL)
		rt.WriteString(s)
		text := rt.ToText(0, 0, canvas.Left, canvas.Top, nil)
		tmp := &pathCollector{}
		text.RenderTo(tmp, canvas.Identity.Scale(1, -1), 0)
		bounds := tmp.path.Bounds()
		r.RenderPath(&tmp.path, canvas.DefaultStyle, canvas.Identity.Translate(x-bounds.W()/2-bounds.X0, y-bounds.Y0))
	} else {
		p, _ := f.ToPath(s)
		r.RenderPath(p, canvas.DefaultStyle, canvas.Identity.Translate(x, y).Scale(1, -1))
	}
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
	if lang == "" {
		lang = "en"
	}
	pn := p.Panel(panelID)
	if pn == nil {
		return nil, errors.New("lettering: panel not found")
	}
	if len(lines)+len(p.Bubbles) > pagedoc.MaxBubbles {
		return nil, errors.New("lettering: too many bubbles")
	}
	bounds := pagedoc.Bounds(pn.Points)
	var out []pagedoc.Bubble
	occupied := make([]pagedoc.Rect, 0, len(p.Bubbles))
	for _, b := range p.Bubbles {
		occupied = append(occupied, b.Box)
	}
	for _, line := range lines {
		kind, shape := line.Kind, pagedoc.ShapeEllipse
		switch kind {
		case pagedoc.BubbleSpeech:
		case pagedoc.BubbleThought:
			shape = pagedoc.ShapeCloud
		case pagedoc.BubbleCaption:
			shape = pagedoc.ShapeRect
		case pagedoc.BubbleSFX:
			shape = pagedoc.ShapeNone
		case pagedoc.BubbleShout:
			shape = pagedoc.ShapeBurst
		case pagedoc.BubbleWhisper:
		default:
			return nil, errors.New("lettering: invalid dialogue kind")
		}
		w, h := math.Min(360, bounds.W*.44), math.Min(220, bounds.H*.28)
		if vertical {
			w, h = math.Min(180, bounds.W*.28), math.Min(360, bounds.H*.44)
		}
		if w < 32 || h < 32 {
			return nil, errors.New("lettering: panel too small")
		}
		found := false
		var box pagedoc.Rect
		step := math.Max(12, math.Min(w, h)*.2)
		for y := bounds.Y + 8; y+h <= bounds.Y+bounds.H-8 && !found; y += step {
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
					box = r
					found = true
					break
				}
			}
		}
		if !found {
			return nil, errors.New("lettering: no room for bubbles without overlap")
		}
		b := pagedoc.Bubble{ID: db.NewID(), PanelID: panelID, Kind: kind, Shape: shape, Order: len(p.Bubbles) + len(out), Box: box, SpeakerCharacterID: line.CharacterID, ScriptPanelID: line.ScriptPanelID, Vertical: vertical, Style: pagedoc.BubbleStyle{AutoFit: true}, Texts: map[string]pagedoc.BubbleText{lang: {Text: line.Text, Status: "machine", Source: pagedoc.ProvenanceAI}}}
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
	return out, nil
}

func provide(d *core.Deps, e Engine) { core.Provide[Engine](d, e) }
