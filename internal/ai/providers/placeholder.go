package providers

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// PlaceholderLabel is how the built-in provider is labelled everywhere.
const PlaceholderLabel = "Built-in placeholder · no AI"

// Placeholder is the built-in provider. It is always available and never
// leaves the server: images are labelled screentone art sized to the request
// (edits and inpaints tone the changed region of the source), upscales are
// plain resampling and text is deterministic template text. It exists so
// every flow works before any model is connected, and it is honest about it.
type Placeholder struct{}

// Kind implements Adapter.
func (Placeholder) Kind() string { return KindPlaceholder }

var (
	fontOnce sync.Once
	fontData *opentype.Font
)

func face(size float64) font.Face {
	fontOnce.Do(func() { fontData, _ = opentype.Parse(gomonobold.TTF) })
	if fontData == nil {
		return nil
	}
	f, err := opentype.NewFace(fontData, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	return f
}

var (
	toneBG  = color.RGBA{0x14, 0x1C, 0x30, 0xFF}
	toneDot = color.RGBA{0xB5, 0xC0, 0xDC, 0xFF}
	toneBox = color.RGBA{0x0D, 0x13, 0x22, 0xFF}
	toneTxt = color.RGBA{0xE8, 0xEE, 0xFF, 0xFF}
	toneSub = color.RGBA{0xA3, 0xB3, 0xD9, 0xFF}
)

func seedOf(parts ...string) uint32 {
	h := fnv.New32a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum32()
}

// screentone fills r with a dot screen whose density follows a diagonal gradient.
func screentone(img *image.RGBA, r image.Rectangle, seed uint32) {
	draw.Draw(img, r, &image.Uniform{toneBG}, image.Point{}, draw.Src)
	pitch := max(6, min(r.Dx(), r.Dy())/90)
	angle := int(seed % 4)
	for y := r.Min.Y; y < r.Max.Y; y += pitch {
		for x := r.Min.X; x < r.Max.X; x += pitch {
			ox := x
			if (y/pitch)%2 == 1 {
				ox += pitch / 2
			}
			var t float64
			switch angle {
			case 0:
				t = float64(ox-r.Min.X+y-r.Min.Y) / float64(r.Dx()+r.Dy())
			case 1:
				t = float64(r.Max.X-ox+y-r.Min.Y) / float64(r.Dx()+r.Dy())
			case 2:
				t = float64(y-r.Min.Y) / float64(max(1, r.Dy()))
			default:
				t = 1 - float64(y-r.Min.Y)/float64(max(1, r.Dy()))
			}
			rad := float64(pitch) * (0.12 + 0.28*t)
			dot(img, ox, y, rad, r)
		}
	}
}

func dot(img *image.RGBA, cx, cy int, rad float64, clip image.Rectangle) {
	ir := int(rad + 1)
	for y := cy - ir; y <= cy+ir; y++ {
		for x := cx - ir; x <= cx+ir; x++ {
			dx, dy := float64(x-cx), float64(y-cy)
			if dx*dx+dy*dy <= rad*rad && (image.Point{x, y}).In(clip) {
				img.SetRGBA(x, y, toneDot)
			}
		}
	}
}

// truncateRunes shortens s to n runes with an ellipsis.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:max(0, n-1)]) + "…"
}

// caption draws the placeholder label box in the bottom-left corner.
func caption(img *image.RGBA, lines []string) {
	b := img.Bounds()
	size := max(10, min(28, float64(b.Dx())/48))
	f := face(size)
	if f == nil {
		return
	}
	defer f.Close()
	lh := int(size * 1.35)
	pad := int(size * 0.5)
	maxChars := max(8, int(float64(b.Dx()-4*pad)/(size*0.62)))
	width := 0
	for i, l := range lines {
		lines[i] = truncateRunes(l, maxChars)
		width = max(width, font.MeasureString(f, lines[i]).Ceil())
	}
	box := image.Rect(b.Min.X+pad, b.Max.Y-pad-lh*len(lines)-pad, b.Min.X+pad+width+2*pad, b.Max.Y-pad)
	draw.Draw(img, box, &image.Uniform{toneBox}, image.Point{}, draw.Src)
	for i, l := range lines {
		col := toneSub
		if i == 0 {
			col = toneTxt
		}
		d := &font.Drawer{Dst: img, Src: &image.Uniform{col}, Face: f,
			Dot: fixed.P(box.Min.X+pad, box.Min.Y+pad+lh*(i+1)-lh/4)}
		d.DrawString(l)
	}
}

// Art renders a labelled placeholder panel of w×h pixels.
func Art(w, h int, title, prompt string, variant int) []byte {
	w, h = max(16, min(w, 4096)), max(16, min(h, 4096))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	screentone(img, img.Bounds(), seedOf(prompt, fmt.Sprint(variant)))
	lines := []string{strings.ToUpper(PlaceholderLabel)}
	if title != "" {
		lines = append(lines, title)
	}
	if prompt != "" {
		lines = append(lines, "“"+prompt+"”")
	}
	lines = append(lines, fmt.Sprintf("%d×%d", w, h))
	caption(img, lines)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// Image implements ImageGenerator.
func (Placeholder) Image(ctx context.Context, _ Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	res := &ImageResult{Note: PlaceholderLabel}
	label := req.Label
	switch req.Task {
	case "upscale":
		if req.Image == nil {
			return nil, Errf(CodeBadRequest, "upscale needs an image")
		}
		src, _, err := storage.DecodeImage(req.Image.Data, storage.ImageLimits{})
		if err != nil {
			return nil, Errf(CodeBadRequest, "cannot read the image: %v", err)
		}
		scale := max(2, min(req.Scale, 4))
		sb := src.Bounds()
		dw, dh := min(sb.Dx()*scale, 8192), min(sb.Dy()*scale, 8192)
		dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, draw.Over, nil)
		var buf bytes.Buffer
		if err := png.Encode(&buf, dst); err != nil {
			return nil, Errf(CodeInternal, "%v", err)
		}
		res.Images = []Image{{Data: buf.Bytes(), MIME: "image/png"}}
		return res, nil
	case "edit", "inpaint":
		if req.Image != nil {
			for i := 0; i < req.Count(); i++ {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				b, err := toneRegion(req.Image.Data, maskData(req.Mask), req.Prompt, i)
				if err != nil {
					return nil, err
				}
				res.Images = append(res.Images, Image{Data: b, MIME: "image/png", Seed: int64(i)})
			}
			return res, nil
		}
	}
	for i := 0; i < req.Count(); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		progress.report(float64(i)/float64(req.Count()), "drawing placeholder")
		title := label
		if req.Count() > 1 {
			title = strings.TrimSpace(fmt.Sprintf("%s  %d/%d", label, i+1, req.Count()))
		}
		res.Images = append(res.Images, Image{Data: Art(w, h, title, req.Prompt, i), MIME: "image/png", Seed: int64(i)})
	}
	return res, nil
}

func maskData(m *Image) []byte {
	if m == nil {
		return nil
	}
	return m.Data
}

// toneRegion tones the masked (white) region of src, or the whole image without a mask.
func toneRegion(src, mask []byte, prompt string, variant int) ([]byte, error) {
	s, _, err := storage.DecodeImage(src, storage.ImageLimits{})
	if err != nil {
		return nil, Errf(CodeBadRequest, "cannot read the source image: %v", err)
	}
	b := s.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), s, b.Min, draw.Src)
	tone := image.NewRGBA(out.Bounds())
	screentone(tone, tone.Bounds(), seedOf(prompt, fmt.Sprint(variant)))
	var m *image.Gray
	area := out.Bounds()
	if len(mask) > 0 {
		mi, _, err := storage.DecodeImage(mask, storage.ImageLimits{})
		if err != nil {
			return nil, Errf(CodeBadRequest, "cannot read the mask: %v", err)
		}
		m = image.NewGray(out.Bounds())
		draw.NearestNeighbor.Scale(m, m.Bounds(), mi, mi.Bounds(), draw.Src, nil)
		area = image.Rectangle{}
		for y := 0; y < m.Bounds().Dy(); y++ {
			for x := 0; x < m.Bounds().Dx(); x++ {
				if m.GrayAt(x, y).Y >= 128 {
					area = area.Union(image.Rect(x, y, x+1, y+1))
				}
			}
		}
	}
	// The label is drawn on the tone inside the changed area only, so pixels
	// outside the mask stay exactly as they were.
	if !area.Empty() {
		caption(tone.SubImage(area).(*image.RGBA), []string{strings.ToUpper(PlaceholderLabel), "“" + prompt + "”"})
	}
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			if m != nil && m.GrayAt(x, y).Y < 128 {
				continue
			}
			out.SetRGBA(x, y, tone.RGBAAt(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, Errf(CodeInternal, "%v", err)
	}
	return buf.Bytes(), nil
}

// Text implements TextGenerator with deterministic template text.
func (Placeholder) Text(ctx context.Context, _ Config, req TextRequest) (*TextResult, error) {
	last := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "assistant" {
			last = req.Messages[i].Content
			break
		}
	}
	last = strings.TrimSpace(last)
	var text string
	switch {
	case req.Task == "translate":
		lang := req.Lang
		if lang == "" {
			lang = "?"
		}
		text = fmt.Sprintf("[%s · %s] %s", PlaceholderLabel, lang, last)
	case req.JSON:
		text = fmt.Sprintf(`{"placeholder": true, "label": %q, "summary": %q}`, PlaceholderLabel, truncateRunes(last, 160))
	default:
		text = fmt.Sprintf("[%s]\nThis is template text so the flow can be tried without a text model.\nConnect a text model in Models & providers for real results.\nRequest: %s",
			PlaceholderLabel, truncateRunes(strings.Join(strings.Fields(last), " "), 240))
	}
	return &TextResult{Text: text, TokensIn: len(strings.Fields(last)), TokensOut: len(strings.Fields(text))}, nil
}
