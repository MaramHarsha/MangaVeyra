package lettering

import (
	"math"

	"github.com/tdewolff/canvas"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// textArea is the region of a bubble that text may occupy: the inside of its
// drawn outline less the stroke and a margin, in page pixels. Round shapes
// are an ellipse centred on the bubble; rectangles and shapeless bubbles are
// a box. Every round shape's safe area is an exact, convex ellipse:
//
//   - ellipse: the bubble ellipse itself;
//   - cloud: the lobes are r(a) = .94 + .06·cos(12a) times the ellipse along
//     every ray, so .87 of the ellipse (the troughs, less polygon chords) is
//     inside;
//   - burst: alternating outer (1) and inner (.82) points; the inner points'
//     convex polygon contains the ellipse scaled by .82·cos(π/16) ≈ .80.
//
// Containment is therefore decided analytically for every shape, and the
// same area drives line widths, auto-fit and the final glyph check.
type textArea struct {
	cx, cy, rx, ry float64 // centre and semi-axes (half sizes for boxes)
	round          bool
}

func newTextArea(b pagedoc.Bubble, strokeWidth float64) textArea {
	r := b.Box
	short := math.Min(r.W, r.H)
	a := textArea{cx: r.X + r.W/2, cy: r.Y + r.H/2, round: true}
	k, margin := 1.0, strokeWidth/2+math.Max(3, short*.05)
	switch b.Shape {
	case pagedoc.ShapeCloud:
		k = .87
	case pagedoc.ShapeBurst:
		k = .80
	case pagedoc.ShapeRect:
		a.round, margin = false, strokeWidth/2+math.Max(6, short*.10)
	case pagedoc.ShapeNone:
		a.round, margin = false, math.Max(2, short*.03)
	}
	a.rx = math.Max(.5, r.W/2*k-margin)
	a.ry = math.Max(.5, r.H/2*k-margin)
	return a
}

// bounds is the area's bounding box, reported as the layout's TextBox: every
// glyph lies inside it.
func (a textArea) bounds() pagedoc.Rect {
	return pagedoc.Rect{X: a.cx - a.rx, Y: a.cy - a.ry, W: 2 * a.rx, H: 2 * a.ry}
}

// inner is the largest box of the area's proportions inside it; default
// sizes are derived from it.
func (a textArea) inner() pagedoc.Rect {
	if !a.round {
		return pagedoc.Rect{X: a.cx - a.rx, Y: a.cy - a.ry, W: 2 * a.rx, H: 2 * a.ry}
	}
	hx, hy := a.rx/math.Sqrt2, a.ry/math.Sqrt2
	return pagedoc.Rect{X: a.cx - hx, Y: a.cy - hy, W: 2 * hx, H: 2 * hy}
}

// across is the area's extent across lines: its height for horizontal text,
// its width for vertical columns.
func (a textArea) across(vertical bool) float64 {
	if vertical {
		return 2 * a.rx
	}
	return 2 * a.ry
}

// span returns the interval along the reading direction that is inside the
// area for the whole band [lo, hi] across it (rows of horizontal text, or
// columns of vertical text).
func (a textArea) span(lo, hi float64, vertical bool) (float64, float64, bool) {
	c, r, cAlong, rAlong := a.cy, a.ry, a.cx, a.rx
	if vertical {
		c, r, cAlong, rAlong = a.cx, a.rx, a.cy, a.ry
	}
	if lo < c-r-1e-9 || hi > c+r+1e-9 {
		return 0, 0, false
	}
	if !a.round {
		return cAlong - rAlong, cAlong + rAlong, true
	}
	d := math.Max(math.Abs(lo-c), math.Abs(hi-c)) / r
	if d >= 1 {
		return 0, 0, false
	}
	half := rAlong * math.Sqrt(1-d*d)
	return cAlong - half, cAlong + half, true
}

// contains reports whether the rectangle lies inside the area. The area is
// convex, so its corners decide.
func (a textArea) contains(box canvas.Rect) bool {
	const eps = 1e-6
	for _, p := range [4][2]float64{{box.X0, box.Y0}, {box.X1, box.Y0}, {box.X0, box.Y1}, {box.X1, box.Y1}} {
		dx, dy := (p[0]-a.cx)/a.rx, (p[1]-a.cy)/a.ry
		if a.round && dx*dx+dy*dy > 1+eps || !a.round && (math.Abs(dx) > 1+eps || math.Abs(dy) > 1+eps) {
			return false
		}
	}
	return true
}
