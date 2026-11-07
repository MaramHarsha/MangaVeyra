package compose

// Frozen Phase 1 contract (docs/plans/0002-phase-1-first-playable.md).
// Use core.MustUse[compose.Renderer](d).

import (
	"context"
	"image"

	"github.com/tdewolff/canvas"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// Options control page rendering.
type Options struct {
	// Lang selects the bubble text language ("" → project language / first available).
	Lang string
	// Scale multiplies the page pixel size for raster output (0 → 1).
	Scale float64
	// Lettering draws bubbles (default true via NoLettering=false).
	NoLettering bool
	// BleedPx extends the canvas on every side (page pixels) with white.
	BleedPx float64
	// Placeholder is ignored and kept only so existing callers compile: a
	// layer without art draws nothing, and an empty panel renders as a clean
	// white storyboard area with its border, matching the editor.
	//
	// Deprecated: no longer has any effect.
	Placeholder bool
	// BorderWidth is the panel border width in page pixels (0 → 6).
	BorderWidth float64
}

// Renderer composites page documents.
type Renderer interface {
	// Canvas draws the page (panels clipped to their polygons, layers in order,
	// panel borders, bubbles) onto a new canvas in millimetres-free page units
	// (1 unit = 1 page pixel). Export writes it to PDF/SVG/PNG.
	Canvas(ctx context.Context, p pagedoc.Page, o Options) (*canvas.Canvas, error)
	// Image renders the page to a raster image.
	Image(ctx context.Context, p pagedoc.Page, o Options) (image.Image, error)
	// PNG renders the page to PNG bytes.
	PNG(ctx context.Context, p pagedoc.Page, o Options) ([]byte, error)
	// Thumbnail renders a small PNG (max width px) without bleed.
	Thumbnail(ctx context.Context, p pagedoc.Page, maxWidth int) ([]byte, error)
}
