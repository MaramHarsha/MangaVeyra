package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/rasterizer"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/vector"
)

// The aggregate allocation bound includes overlapping panels, not just the
// output dimensions, so a document cannot multiply memory by adding polygons.
const maxRenderPixels = 32 << 20

type renderer struct {
	assets    *assets.Service
	lettering lettering.Engine
}

var _ Renderer = (*renderer)(nil)

func NewRenderer(a *assets.Service, e lettering.Engine) Renderer {
	return &renderer{assets: a, lettering: e}
}
func validOptions(p pagedoc.Page, o Options) (Options, error) {
	if err := p.Validate(); err != nil {
		return o, fmt.Errorf("compose: invalid page: %w", err)
	}
	if o.Scale == 0 {
		o.Scale = 1
	}
	if o.BorderWidth == 0 {
		o.BorderWidth = 6
	}
	for _, v := range []float64{o.Scale, o.BleedPx, o.BorderWidth} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return o, errors.New("compose: invalid options")
		}
	}
	if o.Scale <= 0 || o.Scale > 8 || o.BleedPx < 0 || o.BleedPx > 2000 || o.BorderWidth < 0 || o.BorderWidth > 100 {
		return o, errors.New("compose: invalid options")
	}
	w, h := (float64(p.Width)+o.BleedPx*2)*o.Scale, (float64(p.Height)+o.BleedPx*2)*o.Scale
	if w < 1 || h < 1 || w*h > maxRenderPixels {
		return o, errors.New("compose: output exceeds pixel limit")
	}
	return o, nil
}
func (r *renderer) Canvas(ctx context.Context, p pagedoc.Page, o Options) (*canvas.Canvas, error) {
	var err error
	o, err = validOptions(p, o)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	c := canvas.New(float64(p.Width)+o.BleedPx*2, float64(p.Height)+o.BleedPx*2)
	cc := canvas.NewContext(c)
	cc.SetCoordSystem(canvas.CartesianIV)
	cc.SetFillColor(canvas.White)
	cc.DrawPath(0, 0, canvas.Rectangle(c.W, c.H))
	cc.Translate(o.BleedPx, o.BleedPx)
	panels := append([]pagedoc.Panel(nil), p.Panels...)
	sort.SliceStable(panels, func(i, j int) bool { return panels[i].Order < panels[j].Order })
	used := 0
	for _, pn := range panels {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b := pagedoc.Bounds(pn.Points)
		if b.W <= 0 || b.H <= 0 {
			return nil, errors.New("compose: degenerate polygon")
		}
		w, h := int(math.Ceil(b.W*o.Scale)), int(math.Ceil(b.H*o.Scale))
		used += w * h
		if used > maxRenderPixels {
			return nil, errors.New("compose: panels exceed pixel limit")
		}
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		mask := polygonMask(pn.Points, b, o.Scale, w, h)
		layers := append([]pagedoc.Layer(nil), pn.Layers...)
		sort.SliceStable(layers, func(i, j int) bool { return layers[i].Order < layers[j].Order })
		missing := false
		for _, l := range layers {
			if !l.Visible || l.Kind == pagedoc.LayerLettering || l.Opacity == 0 {
				continue
			}
			if l.Asset == nil {
				if o.Placeholder {
					missing = true
					tmp := image.NewNRGBA(dst.Bounds())
					placeholder(tmp, l.Kind)
					if err := blend(ctx, dst, tmp, l.Opacity, l.Blend); err != nil {
						return nil, err
					}
				}
				continue
			}
			if r.assets == nil {
				return nil, errors.New("compose: asset service unavailable")
			}
			data, _, err := r.assets.Read(ctx, l.Asset.ID, assets.MaxBytes)
			if err != nil {
				return nil, fmt.Errorf("compose: read layer: %w", err)
			}
			src, _, err := storage.DecodeImage(data, storage.DefaultImageLimits)
			if err != nil {
				return nil, err
			}
			if l.Mask != nil {
				data, _, err := r.assets.Read(ctx, l.Mask.ID, assets.MaxBytes)
				if err != nil {
					return nil, err
				}
				m, _, err := storage.DecodeImage(data, storage.DefaultImageLimits)
				if err != nil {
					return nil, err
				}
				if src.Bounds().Size() != m.Bounds().Size() {
					return nil, errors.New("compose: layer mask dimensions differ")
				}
				src = applyMask(src, m)
			}
			tmp := image.NewNRGBA(dst.Bounds())
			if err := transform(tmp, src, l.Transform, b, o.Scale); err != nil {
				return nil, err
			}
			if err := blend(ctx, dst, tmp, l.Opacity, l.Blend); err != nil {
				return nil, err
			}
		}
		// Antialiased polygon alpha is applied after blending all panel art.
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := dst.PixOffset(x, y)
				dst.Pix[i+3] = uint8(uint16(dst.Pix[i+3]) * uint16(mask.AlphaAt(x, y).A) / 255)
			}
		}
		cc.DrawImage(b.X, b.Y, dst, canvas.DPMM(o.Scale))
		path := polygonPath(pn.Points)
		cc.SetFillColor(color.Transparent)
		cc.SetStrokeColor(canvas.Black)
		cc.SetStrokeWidth(o.BorderWidth)
		cc.DrawPath(0, 0, path)
		if missing && r.lettering != nil {
			label := pagedoc.Bubble{ID: pn.ID + "-label", Kind: pagedoc.BubbleSFX, Shape: pagedoc.ShapeNone, Box: pagedoc.Rect{X: b.X + b.W*.15, Y: b.Y + b.H*.4, W: b.W * .7, H: b.H * .2}, Style: pagedoc.BubbleStyle{Size: math.Min(36, b.W*.08), AutoFit: true}, Texts: map[string]pagedoc.BubbleText{"en": {Text: fmt.Sprintf("PANEL %d · PLACEHOLDER", pn.Order+1)}}}
			l, err := r.lettering.Layout(label, "en")
			if err != nil {
				return nil, err
			}
			if err = r.lettering.Draw(cc, l); err != nil {
				return nil, err
			}
		}
	}
	if !o.NoLettering && len(p.Bubbles) > 0 {
		if r.lettering == nil {
			return nil, errors.New("compose: lettering engine unavailable")
		}
		ls, err := r.lettering.LayoutPage(p, o.Lang)
		if err != nil {
			return nil, err
		}
		bubbles := make(map[string]string, len(p.Bubbles))
		for _, b := range p.Bubbles {
			bubbles[b.ID] = b.PanelID
		}
		for _, l := range ls {
			opacity := letteringOpacity(p, bubbles[l.BubbleID])
			if opacity == 0 {
				continue
			}
			if opacity != 1 {
				l.Fill = opacityColor(l.Fill, opacity)
				l.Stroke = opacityColor(l.Stroke, opacity)
				l.TextColor = opacityColor(l.TextColor, opacity)
				l.TextStroke = opacityColor(l.TextStroke, opacity)
			}
			if err := r.lettering.Draw(cc, l); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

func letteringOpacity(p pagedoc.Page, panelID string) float64 {
	if pn := p.Panel(panelID); pn != nil {
		for _, l := range pn.Layers {
			if l.Kind == pagedoc.LayerLettering {
				if !l.Visible {
					return 0
				}
				return l.Opacity
			}
		}
	}
	// Legacy documents and page-level captions need no explicit layer.
	return 1
}

func opacityColor(hex string, opacity float64) string {
	if hex == "" {
		return ""
	}
	c := color.NRGBAModel.Convert(canvas.Hex(hex)).(color.NRGBA)
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, uint8(math.Round(float64(c.A)*opacity)))
}
func polygonPath(points []pagedoc.Point) *canvas.Path {
	p := &canvas.Path{}
	for i, v := range points {
		if i == 0 {
			p.MoveTo(v.X, v.Y)
		} else {
			p.LineTo(v.X, v.Y)
		}
	}
	p.Close()
	return p
}
func polygonMask(points []pagedoc.Point, b pagedoc.Rect, s float64, w, h int) *image.Alpha {
	z := vector.NewRasterizer(w, h)
	for i, v := range points {
		x, y := float32((v.X-b.X)*s), float32((v.Y-b.Y)*s)
		if i == 0 {
			z.MoveTo(x, y)
		} else {
			z.LineTo(x, y)
		}
	}
	z.ClosePath()
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	z.Draw(m, m.Bounds(), image.Opaque, image.Point{})
	return m
}
func transform(dst *image.NRGBA, src image.Image, t pagedoc.Transform, b pagedoc.Rect, res float64) error {
	for _, v := range []float64{t.OffsetX, t.OffsetY, t.Scale, t.Rotate} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("compose: invalid layer transform")
		}
	}
	if t.Scale == 0 {
		t.Scale = 1
	}
	if t.Scale < 0 || t.Scale > 100 {
		return errors.New("compose: invalid layer scale")
	}
	ib := src.Bounds()
	s := math.Max(b.W/float64(ib.Dx()), b.H/float64(ib.Dy())) * t.Scale * res
	a := t.Rotate * math.Pi / 180
	co, si := math.Cos(a)*s, math.Sin(a)*s
	flip := 1.0
	if t.FlipX {
		flip = -1
	}
	xx, xy, yx, yy := co*flip, -si, si*flip, co
	cx, cy := float64(ib.Min.X)+float64(ib.Dx())/2, float64(ib.Min.Y)+float64(ib.Dy())/2
	x, y := (b.W/2+t.OffsetX)*res, (b.H/2+t.OffsetY)*res
	xdraw.CatmullRom.Transform(dst, f64.Aff3{xx, xy, x - xx*cx - xy*cy, yx, yy, y - yx*cx - yy*cy}, src, ib, xdraw.Over, nil)
	return nil
}
func applyMask(src, mask image.Image) image.Image {
	b := src.Bounds()
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			m := color.GrayModel.Convert(mask.At(x, y)).(color.Gray)
			c.A = uint8(uint16(c.A) * uint16(m.Y) / 255)
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}
func blend(ctx context.Context, dst, src *image.NRGBA, opacity float64, mode string) error {
	if math.IsNaN(opacity) || math.IsInf(opacity, 0) || opacity < 0 || opacity > 1 {
		return errors.New("compose: invalid opacity")
	}
	switch mode {
	case "", "normal", "multiply", "screen", "overlay":
	default:
		return errors.New("compose: unsupported blend mode")
	}
	for y := 0; y < dst.Bounds().Dy(); y++ {
		if y%64 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		for x := 0; x < dst.Bounds().Dx(); x++ {
			i := dst.PixOffset(x, y)
			sa := float64(src.Pix[i+3]) / 255 * opacity
			da := float64(dst.Pix[i+3]) / 255
			a := sa + da*(1-sa)
			if a == 0 {
				continue
			}
			for k := 0; k < 3; k++ {
				s, d := float64(src.Pix[i+k])/255, float64(dst.Pix[i+k])/255
				v := s
				switch mode {
				case "multiply":
					v = s * d
				case "screen":
					v = s + d - s*d
				case "overlay":
					if d <= .5 {
						v = 2 * s * d
					} else {
						v = 1 - 2*(1-s)*(1-d)
					}
				}
				premul := sa*(1-da)*s + sa*da*v + (1-sa)*da*d
				dst.Pix[i+k] = uint8(math.Round(math.Min(1, premul/a) * 255))
			}
			dst.Pix[i+3] = uint8(math.Round(a * 255))
		}
	}
	return nil
}
func placeholder(dst *image.NRGBA, kind string) {
	for y := 0; y < dst.Bounds().Dy(); y++ {
		for x := 0; x < dst.Bounds().Dx(); x++ {
			v := uint8(232)
			if (x/3+y/3)%8 == 0 {
				v = 150
			}
			if kind == pagedoc.LayerCharacter && ((float64(x)/float64(dst.Bounds().Dx())-.5)*(float64(x)/float64(dst.Bounds().Dx())-.5)*4+math.Pow(float64(y)/float64(dst.Bounds().Dy())-.55, 2)*2) > .5 {
				continue
			}
			if kind == pagedoc.LayerFX {
				if (x+y)%32 > 3 {
					continue
				}
				v = 100
			}
			dst.SetNRGBA(x, y, color.NRGBA{v, v, v, 255})
		}
	}
}
func (r *renderer) Image(ctx context.Context, p pagedoc.Page, o Options) (image.Image, error) {
	c, err := r.Canvas(ctx, p, o)
	if err != nil {
		return nil, err
	}
	s := o.Scale
	if s == 0 {
		s = 1
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return rasterizer.Draw(c, canvas.DPMM(s), canvas.LinearColorSpace{}), nil
}
func (r *renderer) PNG(ctx context.Context, p pagedoc.Page, o Options) ([]byte, error) {
	im, err := r.Image(ctx, p, o)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	err = png.Encode(&b, im)
	return b.Bytes(), err
}
func (r *renderer) Thumbnail(ctx context.Context, p pagedoc.Page, maxWidth int) ([]byte, error) {
	if maxWidth <= 0 || maxWidth > 2048 {
		return nil, errors.New("compose: invalid thumbnail width")
	}
	return r.PNG(ctx, p, Options{Scale: math.Min(1, float64(maxWidth)/float64(p.Width)), Placeholder: true})
}
