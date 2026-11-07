package export

import (
	"image"
	"image/color"

	"github.com/tdewolff/canvas"
)

// streamingPDFRenderer is specific to the pinned canvas PDF writer. That writer
// embeds image objects synchronously in RenderImage, then retains image.Image
// keys solely for deduplication. Give it a unique lightweight key and release
// its pixels immediately, so chapter length cannot accumulate page rasters in
// that cache. Paths/text pass through unchanged and remain vector artwork.
//
// pdf_images_test exercises the real writer through Close with released keys;
// a writer upgrade that starts reading cached pixels later must fail that test.
type streamingPDFRenderer struct{ canvas.Renderer }

func (r streamingPDFRenderer) RenderImage(img image.Image, m canvas.Matrix) {
	key := &pdfImage{pixels: img}
	defer func() { key.pixels = nil }()
	r.Renderer.RenderImage(key, m)
}

type pdfImage struct{ pixels image.Image }

func (i *pdfImage) ColorModel() color.Model { return i.pixels.ColorModel() }
func (i *pdfImage) Bounds() image.Rectangle { return i.pixels.Bounds() }
func (i *pdfImage) At(x, y int) color.Color { return i.pixels.At(x, y) }
func (i *pdfImage) Opaque() bool {
	if opaque, ok := i.pixels.(interface{ Opaque() bool }); ok {
		return opaque.Opaque()
	}
	return false
}
