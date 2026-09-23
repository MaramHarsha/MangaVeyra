package storage

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder
)

// ErrImageTooLarge is returned when an image exceeds the decode limits.
var ErrImageTooLarge = errors.New("storage: image dimensions exceed the limit")

// ErrUnsupportedType is returned when uploaded content is not an allowed type.
var ErrUnsupportedType = errors.New("storage: unsupported file type")

// ImageLimits bounds image decoding so a small "decompression bomb" file cannot exhaust memory.
type ImageLimits struct {
	MaxWidth  int
	MaxHeight int
	MaxPixels int
}

// DefaultImageLimits allows double-page spreads at print resolution (e.g. 8000×6000) and long webtoon strips.
var DefaultImageLimits = ImageLimits{MaxWidth: 16384, MaxHeight: 32768, MaxPixels: 64 << 20}

// ImageTypes are the raster formats accepted for artwork uploads.
var ImageTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

// DetectContentType sniffs the content type of data (the first 512 bytes are enough).
// It extends http.DetectContentType with AVIF and a few formats the app handles.
func DetectContentType(data []byte) string {
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		brand := string(data[8:12])
		switch brand {
		case "avif", "avis":
			return "image/avif"
		case "isom", "iso2", "mp41", "mp42", "avc1", "dash":
			return "video/mp4"
		}
	}
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return "application/zip"
	}
	if bytes.HasPrefix(data, []byte("Rar!\x1a\x07")) {
		return "application/vnd.rar"
	}
	ct := http.DetectContentType(data)
	if i := strings.IndexByte(ct, ';'); i >= 0 && !strings.HasPrefix(ct, "text/") {
		ct = ct[:i]
	}
	return ct
}

// Sniff peeks at the start of r and returns its content type plus a reader
// that still yields the complete stream.
func Sniff(r io.Reader) (string, io.Reader, error) {
	br := bufio.NewReaderSize(r, 512)
	head, err := br.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return "", nil, err
	}
	return DetectContentType(head), br, nil
}

// CheckType returns ErrUnsupportedType unless contentType is one of allowed
// (media parameters such as "; charset=utf-8" are ignored).
func CheckType(contentType string, allowed ...string) error {
	ct := contentType
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(ct)
	for _, a := range allowed {
		if ct == a {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedType, ct)
}

// ImageConfig reads only the header of an image and enforces the limits.
func ImageConfig(data []byte, lim ImageLimits) (image.Config, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, "", fmt.Errorf("%w: %v", ErrUnsupportedType, err)
	}
	if err := lim.check(cfg.Width, cfg.Height); err != nil {
		return cfg, format, err
	}
	return cfg, format, nil
}

func (lim ImageLimits) check(w, h int) error {
	if lim.MaxWidth == 0 && lim.MaxHeight == 0 && lim.MaxPixels == 0 {
		lim = DefaultImageLimits
	}
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%w: empty image", ErrUnsupportedType)
	}
	if (lim.MaxWidth > 0 && w > lim.MaxWidth) || (lim.MaxHeight > 0 && h > lim.MaxHeight) ||
		(lim.MaxPixels > 0 && w*h > lim.MaxPixels) {
		return fmt.Errorf("%w: %d×%d", ErrImageTooLarge, w, h)
	}
	return nil
}

// DecodeImage decodes PNG, JPEG, GIF or WebP data after checking the header against the limits.
func DecodeImage(data []byte, lim ImageLimits) (image.Image, string, error) {
	if _, _, err := ImageConfig(data, lim); err != nil {
		return nil, "", err
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("storage: decode image: %w", err)
	}
	return img, format, nil
}

// Fit returns the largest size with the aspect ratio of w×h that fits in maxW×maxH (never upscaling).
func Fit(w, h, maxW, maxH int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if maxW <= 0 {
		maxW = w
	}
	if maxH <= 0 {
		maxH = h
	}
	if w <= maxW && h <= maxH {
		return w, h
	}
	rw := float64(maxW) / float64(w)
	rh := float64(maxH) / float64(h)
	r := rw
	if rh < r {
		r = rh
	}
	nw, nh := int(float64(w)*r+0.5), int(float64(h)*r+0.5)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	return nw, nh
}

// Thumbnail scales img to fit within maxW×maxH with Catmull-Rom resampling.
func Thumbnail(img image.Image, maxW, maxH int) image.Image {
	b := img.Bounds()
	w, h := Fit(b.Dx(), b.Dy(), maxW, maxH)
	if w == b.Dx() && h == b.Dy() {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// Encode writes img as "png", "jpeg" (quality 85) or "gif" and returns the content type.
func Encode(w io.Writer, img image.Image, format string) (string, error) {
	switch format {
	case "jpeg", "jpg":
		return "image/jpeg", jpeg.Encode(w, img, &jpeg.Options{Quality: 85})
	case "gif":
		return "image/gif", gif.Encode(w, img, nil)
	default:
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		return "image/png", enc.Encode(w, img)
	}
}

// ThumbnailBytes decodes data, scales it to fit maxW×maxH and re-encodes it as
// JPEG (opaque sources) or PNG (sources with transparency).
func ThumbnailBytes(data []byte, maxW, maxH int, lim ImageLimits) ([]byte, string, error) {
	img, _, err := DecodeImage(data, lim)
	if err != nil {
		return nil, "", err
	}
	th := Thumbnail(img, maxW, maxH)
	format := "jpeg"
	if hasAlpha(img) {
		format = "png"
	}
	var buf bytes.Buffer
	ct, err := Encode(&buf, th, format)
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), ct, nil
}

func hasAlpha(img image.Image) bool {
	switch m := img.(type) {
	case *image.RGBA:
		return !m.Opaque()
	case *image.NRGBA:
		return !m.Opaque()
	case *image.Paletted:
		return !m.Opaque()
	case *image.YCbCr, *image.Gray, *image.Gray16, *image.CMYK:
		return false
	default:
		if o, ok := img.(interface{ Opaque() bool }); ok {
			return !o.Opaque()
		}
		return true
	}
}
