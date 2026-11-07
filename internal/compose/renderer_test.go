package compose

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func pageFixture() pagedoc.Page {
	p := pagedoc.New("page", "project", "chapter", 1, 200, 300, "rtl")
	p.Panels = []pagedoc.Panel{{ID: "pn", Points: []pagedoc.Point{{X: 20, Y: 20}, {X: 180, Y: 20}, {X: 20, Y: 280}}, Layers: []pagedoc.Layer{}}}
	return p
}
func setupRenderer(t *testing.T, d *db.DB) (Renderer, *assets.Service) {
	t.Helper()
	store, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := assets.New(d, store, storage.NewSigner([]byte("test media key")))
	e, err := lettering.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	return NewRenderer(a, e), a
}
func saveColor(t *testing.T, a *assets.Service, c color.Color) *pagedoc.AssetRef {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			im.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	asset, err := a.Save(context.Background(), assets.SaveParams{Data: b.Bytes(), ContentType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	return &pagedoc.AssetRef{ID: asset.ID}
}
func TestPolygonClippingLayerOrderOpacityMasks(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		r, a := setupRenderer(t, d)
		p := pageFixture()
		red := saveColor(t, a, color.NRGBA{R: 255, A: 255})
		blue := saveColor(t, a, color.NRGBA{B: 255, A: 255})
		layer := func(id string, asset *pagedoc.AssetRef, order int, opacity float64) pagedoc.Layer {
			return pagedoc.Layer{ID: id, Kind: "background", Asset: asset, Order: order, Visible: true, Opacity: opacity, Provenance: "human"}
		}
		p.Panels[0].Layers = []pagedoc.Layer{layer("top", blue, 2, .5), layer("bottom", red, 1, 1)}
		im, err := r.Image(context.Background(), p, Options{})
		if err != nil {
			t.Fatal(err)
		}
		inside := color.NRGBAModel.Convert(im.At(40, 50)).(color.NRGBA)
		outside := color.NRGBAModel.Convert(im.At(170, 260)).(color.NRGBA)
		if inside.R < 110 || inside.R > 145 || inside.B < 110 || inside.B > 145 {
			t.Fatalf("order/opacity: %+v", inside)
		}
		if outside.R != 255 || outside.G != 255 || outside.B != 255 {
			t.Fatalf("art escaped polygon: %+v", outside)
		}
		p.Panels[0].Layers[0].Mask = saveColor(t, a, color.Black)
		im, err = r.Image(context.Background(), p, Options{})
		if err != nil {
			t.Fatal(err)
		}
		inside = color.NRGBAModel.Convert(im.At(40, 50)).(color.NRGBA)
		if inside.R < 250 || inside.B > 5 {
			t.Fatal("black mask failed", inside)
		}
		p.Panels[0].Layers[0].Mask = nil
		p.Panels[0].Layers[0].Visible = false
		im, err = r.Image(context.Background(), p, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if c := color.NRGBAModel.Convert(im.At(40, 50)).(color.NRGBA); c.R < 250 || c.B > 5 {
			t.Fatal("hidden layer visible")
		}
	})
}
func TestBlendModesAndTransforms(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		mode string
		want uint8
	}{{"normal", 128}, {"multiply", 64}, {"screen", 192}, {"overlay", 128}} {
		a, b := image.NewNRGBA(image.Rect(0, 0, 1, 1)), image.NewNRGBA(image.Rect(0, 0, 1, 1))
		a.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
		b.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
		if err := blend(ctx, a, b, 1, tc.mode); err != nil {
			t.Fatal(err)
		}
		if delta := int(a.Pix[0]) - int(tc.want); delta > 1 || delta < -1 {
			t.Fatal(tc.mode, a.Pix)
		}
	}
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		src.SetNRGBA(0, y, color.NRGBA{R: 255, A: 255})
		src.SetNRGBA(1, y, color.NRGBA{B: 255, A: 255})
	}
	dst := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	if err := transform(dst, src, pagedoc.Transform{FlipX: true}, pagedoc.Rect{W: 20, H: 20}, 1); err != nil {
		t.Fatal(err)
	}
	if dst.NRGBAAt(2, 10).B < 200 || dst.NRGBAAt(17, 10).R < 200 {
		t.Fatal("flip transform failed")
	}
	if err := transform(dst, src, pagedoc.Transform{Rotate: math.Inf(1)}, pagedoc.Rect{W: 20, H: 20}, 1); err == nil {
		t.Fatal("nonfinite transform accepted")
	}
}
func TestLetteringScaleThumbnailPlaceholderAndBounds(t *testing.T) {
	r, a := func() (Renderer, *assets.Service) {
		e, err := lettering.NewEngine()
		if err != nil {
			t.Fatal(err)
		}
		return NewRenderer(nil, e), nil
	}()
	_ = a
	p := pageFixture()
	p.Bubbles = []pagedoc.Bubble{{ID: "bubble", PanelID: "pn", Kind: "speech", Shape: "ellipse", Box: pagedoc.Rect{X: 50, Y: 50, W: 110, H: 90}, Style: pagedoc.BubbleStyle{AutoFit: true, Size: 24}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "Run!"}}}}
	im, err := r.Image(context.Background(), p, Options{Scale: 2, BleedPx: 4})
	if err != nil {
		t.Fatal(err)
	}
	if im.Bounds().Dx() != 416 || im.Bounds().Dy() != 616 {
		t.Fatal("scale/bleed", im.Bounds())
	}
	data, err := r.Thumbnail(context.Background(), p, 100)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 100 || cfg.Height != 150 {
		t.Fatal(cfg, err)
	}
	if _, err = r.Image(context.Background(), p, Options{Scale: 8}); err != nil {
		t.Fatal(err)
	}
	p.Width = 20000
	p.Height = 20000
	if _, err = r.Image(context.Background(), p, Options{}); err == nil {
		t.Fatal("allocation limit ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.PNG(ctx, pageFixture(), Options{}); err == nil {
		t.Fatal("cancellation ignored")
	}
}

// QA #11: a panel without art is a clean white storyboard area with its
// border, as in the editor; no hatch and no "PANEL n · PLACEHOLDER" label.
func TestEmptyPanelsRenderCleanWhiteWithBorder(t *testing.T) {
	e, err := lettering.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	r := NewRenderer(nil, e)
	p := pagedoc.New("page", "project", "chapter", 1, 400, 600, "rtl")
	layer := func(id, kind string) pagedoc.Layer {
		l := pagedoc.Layer{ID: id, Kind: kind, Visible: true, Opacity: 1, Provenance: "human"}
		if kind == pagedoc.LayerCharacter {
			l.CharacterID = "rin"
		}
		return l
	}
	p.Panels = []pagedoc.Panel{
		{ID: "a", Order: 0, Points: pagedoc.RectPoints(pagedoc.Rect{X: 20, Y: 20, W: 360, H: 260}),
			Layers: []pagedoc.Layer{layer("bg", "background"), layer("ch", "character"), layer("fx", "fx"), layer("lt", "lettering")}},
		{ID: "b", Order: 1, Points: pagedoc.RectPoints(pagedoc.Rect{X: 20, Y: 300, W: 360, H: 280}), Layers: []pagedoc.Layer{}},
	}
	for _, o := range []Options{{}, {Placeholder: true, Scale: 2, BleedPx: 6}} {
		im, err := r.Image(context.Background(), p, o)
		if err != nil {
			t.Fatal(err)
		}
		s := o.Scale
		if s == 0 {
			s = 1
		}
		at := func(x, y float64) color.Gray {
			return color.GrayModel.Convert(im.At(int((x+o.BleedPx)*s), int((y+o.BleedPx)*s))).(color.Gray)
		}
		for _, pn := range p.Panels {
			b := pagedoc.Bounds(pn.Points)
			for y := b.Y + 8; y < b.Y+b.H-8; y += 2 {
				for x := b.X + 8; x < b.X+b.W-8; x += 2 {
					if g := at(x, y); g.Y < 250 {
						t.Fatalf("panel %s interior is not white at %.0f,%.0f: %d", pn.ID, x, y, g.Y)
					}
				}
			}
			if g := at(b.X, b.Y+b.H/2); g.Y > 40 {
				t.Fatalf("panel %s border missing: %d", pn.ID, g.Y)
			}
		}
	}
	plain, err := r.PNG(context.Background(), p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := r.PNG(context.Background(), p, Options{Placeholder: true})
	if err != nil || !bytes.Equal(plain, legacy) {
		t.Fatal("the deprecated Placeholder option must not change output", err)
	}
}

func TestLetteringProofGeometry(t *testing.T) {
	e, err := lettering.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	r := NewRenderer(nil, e)
	p := pagedoc.New("proof", "project", "chapter", 1, 900, 1200, "rtl")
	kinds := []string{"speech", "thought", "shout", "whisper", "caption", "sfx"}
	shapes := []string{"ellipse", "cloud", "burst", "ellipse", "rect", "none"}
	for i, kind := range kinds {
		x, y := float64(40+(i%2)*440), float64(50+(i/2)*360)
		b := pagedoc.Bubble{ID: kind, Kind: kind, Shape: shapes[i], Order: i, Box: pagedoc.Rect{X: x, Y: y, W: 380, H: 280}, Style: pagedoc.BubbleStyle{Size: 38, AutoFit: true}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "Follow the lighthouse!", Status: "approved", Source: "human"}}}
		if kind == "thought" {
			b.Vertical = true
			b.Texts = map[string]pagedoc.BubbleText{"ja": {Text: "灯台への道", Status: "approved", Source: "human", Ruby: []pagedoc.Ruby{{Start: 0, End: 2, Text: "とうだい"}}}}
		}
		if kind == "sfx" {
			b.Texts["en"] = pagedoc.BubbleText{Text: "WHOOSH!", Status: "approved", Source: "human"}
			b.Style.Rotate = -12
			b.Style.Stroke = true
		}
		if i < 4 {
			b.Tail = pagedoc.Tail{Enabled: true, Tip: pagedoc.Point{X: x + 330, Y: y + 310}}
		}
		p.Bubbles = append(p.Bubbles, b)
	}
	data, err := r.PNG(context.Background(), p, Options{Lang: "ja"})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 1000 {
		t.Fatal("empty proof")
	}
	if dir := os.Getenv("MV_RENDER_QA_DIR"); dir != "" {
		if err = os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "lettering-proof.png"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLetteringLayerVisibilityOpacityAndLegacyDocuments(t *testing.T) {
	e, err := lettering.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	r := NewRenderer(nil, e)
	p := pagedoc.New("page", "project", "chapter", 1, 200, 300, "rtl")
	p.Panels = []pagedoc.Panel{{ID: "panel", Points: pagedoc.RectPoints(pagedoc.Rect{X: 10, Y: 10, W: 180, H: 280}),
		Layers: []pagedoc.Layer{{ID: "lettering", Kind: "lettering", Visible: true, Opacity: 1, Provenance: "human"}}}}
	p.Bubbles = []pagedoc.Bubble{{ID: "bubble", PanelID: "panel", Kind: "speech", Shape: "ellipse",
		Box: pagedoc.Rect{X: 30, Y: 40, W: 140, H: 100}, Style: pagedoc.BubbleStyle{AutoFit: true, Size: 24},
		Texts: map[string]pagedoc.BubbleText{"en": {Text: "The ferry awaits.", Source: "human"}}}}
	full, err := r.Image(context.Background(), p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	off, err := r.PNG(context.Background(), p, Options{NoLettering: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []struct {
		visible bool
		opacity float64
	}{{false, 1}, {true, 0}} {
		p.Panels[0].Layers[0].Visible, p.Panels[0].Layers[0].Opacity = state.visible, state.opacity
		data, err := r.PNG(context.Background(), p, Options{})
		if err != nil || !bytes.Equal(data, off) {
			t.Fatalf("hidden lettering rendered: %+v %v", state, err)
		}
	}
	p.Panels[0].Layers[0].Visible, p.Panels[0].Layers[0].Opacity = true, .5
	half, err := r.Image(context.Background(), p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	faded := 0
	for y := 40; y < 140; y++ {
		for x := 30; x < 170; x++ {
			fullGray := color.GrayModel.Convert(full.At(x, y)).(color.Gray).Y
			if fullGray < 30 {
				halfGray := color.GrayModel.Convert(half.At(x, y)).(color.Gray).Y
				if halfGray <= fullGray+30 || halfGray >= 240 {
					t.Fatalf("lettering alpha not applied: full=%d half=%d", fullGray, halfGray)
				}
				faded++
			}
		}
	}
	if faded < 100 {
		t.Fatal("missing text and outline samples")
	}
	p.Panels[0].Layers = nil
	legacy, err := r.Image(context.Background(), p, Options{})
	if err != nil || !reflect.DeepEqual(legacy, full) {
		t.Fatal("legacy bubbles without a lettering layer changed", err)
	}
}
