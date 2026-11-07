package export

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/tdewolff/canvas"
	canvaspdf "github.com/tdewolff/canvas/renderers/pdf"
)

type imageKeyRecorder struct {
	canvas.Renderer
	keys []*pdfImage
}

func (r *imageKeyRecorder) RenderImage(img image.Image, matrix canvas.Matrix) {
	r.keys = append(r.keys, img.(*pdfImage))
	r.Renderer.RenderImage(img, matrix)
}

func TestPDFReleasesPixelsBeforeNextPageAndClose(t *testing.T) {
	var data bytes.Buffer
	pdf := canvaspdf.New(&data, 182, 257, nil)
	record := &imageKeyRecorder{Renderer: pdf}
	stream := streamingPDFRenderer{Renderer: record}
	for n := 0; n < 24; n++ {
		if n > 0 {
			pdf.NewPage(182, 257)
		}
		im := image.NewNRGBA(image.Rect(0, 0, 64, 96))
		for y := 0; y < 96; y++ {
			for x := 0; x < 64; x++ {
				im.SetNRGBA(x, y, color.NRGBA{R: uint8(n * 7), B: uint8(x + y), A: 255})
			}
		}
		stream.RenderImage(im, canvas.Identity)
		// Inspect references themselves, not a GC/heap threshold: the pinned
		// writer can retain these keys, but none may retain backing pixels.
		for _, key := range record.keys {
			if key.pixels != nil {
				t.Fatal("PDF cache retains pixels from a completed RenderImage")
			}
		}
	}
	if len(record.keys) != 24 || record.keys[0] == record.keys[1] {
		t.Fatal("image keys must be unique to avoid reusing another page's pixels")
	}
	// Released keys deliberately cannot answer Bounds/At/ColorModel. This
	// fails on a pinned-writer upgrade that defers reading pixels to Close.
	if err := pdf.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data.Bytes(), []byte("/Subtype/Image")) && !bytes.Contains(data.Bytes(), []byte("/Subtype /Image")) {
		t.Fatal("writer did not embed the image objects")
	}
}

func TestPDFNormalChapterAt300DPIHasNoAggregatePageRejection(t *testing.T) {
	p := pagedoc.New("page", "project", "chapter", 1, 1600, 2400, "rtl")
	p.Panels = []pagedoc.Panel{{ID: "panel", Points: pagedoc.RectPoints(pagedoc.Rect{X: 80, Y: 80, W: 1440, H: 2240}),
		Layers: []pagedoc.Layer{{ID: "art", Kind: "background", Visible: true, Opacity: 1, Provenance: "human"}}}}
	pages := make([]pagedoc.Page, 24)
	for i := range pages {
		pages[i] = p
	}
	s := &service{renderer: compose.NewRenderer(nil, nil)}
	prep := prepared{project: studio.Project{Title: "Tidebreak Courier"}, chapter: studio.Chapter{Title: "Storm", Number: 1}, pages: pages,
		input: exportInput{Request: Request{Format: FormatPDF, TrimID: "b5", DPI: 300, Lang: "en"}}}
	data, result, err := s.build(context.Background(), prep, nil)
	if err != nil || result.Pages != 24 || len(data) == 0 {
		t.Fatalf("24-page chapter: %+v, %v", result, err)
	}
}
