package pagedoc

import (
	"math"
	"sort"
)

// Layout is a built-in page layout: a recipe that turns a page size into panel
// polygons. Rows are listed top to bottom; within a row, cells are listed in
// reading order for left-to-right pages and mirrored for right-to-left pages.
type Layout struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Panels is the number of panels the layout makes.
	Panels int `json:"panels"`
	// Tags help suggestions ("gag", "action", "dialogue", "reveal", "webtoon").
	Tags []string `json:"tags"`
	rows []row
	// tilt slants inner gutters by this many pixels at 1600 px width (action layouts).
	tilt float64
}

type row struct {
	weight float64   // relative height
	cells  []float64 // relative widths in reading order
}

// Margins and gutters, in pixels at a 1600 px wide page (scaled with width).
const (
	baseWidth  = 1600.0
	marginPx   = 80.0
	gutterPx   = 28.0
	gutterVert = 36.0
)

var builtin = []Layout{
	{ID: "4koma", Name: "4-koma", Description: "Four stacked beats with gag timing", Panels: 4, Tags: []string{"gag", "comedy"},
		rows: []row{{1, []float64{1}}, {1, []float64{1}}, {1, []float64{1}}, {1, []float64{1}}}},
	{ID: "establishing", Name: "Establishing", Description: "Wide open, then zoom in", Panels: 3, Tags: []string{"opening", "scene"},
		rows: []row{{1.4, []float64{1}}, {1, []float64{1, 1}}}},
	{ID: "talking-heads", Name: "Talking heads", Description: "Dialogue grid with room for bubbles", Panels: 6, Tags: []string{"dialogue"},
		rows: []row{{1, []float64{1, 1}}, {1, []float64{1, 1}}, {1, []float64{1, 1}}}},
	{ID: "reveal-5", Name: "Reveal", Description: "Four beats, then one big reveal", Panels: 5, Tags: []string{"reveal", "drama"},
		rows: []row{{0.8, []float64{1, 1}}, {0.8, []float64{1, 1}}, {1.6, []float64{1}}}},
	{ID: "action-spread", Name: "Action spread", Description: "A burst of slanted panels around a big hit", Panels: 5, Tags: []string{"action"},
		rows: []row{{0.9, []float64{1.3, 1}}, {1.5, []float64{1}}, {0.9, []float64{1, 1.3}}}, tilt: 60},
	{ID: "chase", Name: "Chase", Description: "Diagonal gutters and speed", Panels: 7, Tags: []string{"action", "chase"},
		rows: []row{{1, []float64{1, 1, 1}}, {1.2, []float64{1.6, 1}}, {1, []float64{1, 1}}}, tilt: 70},
	{ID: "confession", Name: "Confession", Description: "Slow close-ups and one quiet splash", Panels: 6, Tags: []string{"romance", "drama"},
		rows: []row{{0.8, []float64{1, 1, 1}}, {1.6, []float64{1}}, {0.8, []float64{1, 1}}}},
	{ID: "fight-beat", Name: "Fight beat", Description: "Impact, reaction, aftermath", Panels: 5, Tags: []string{"action"},
		rows: []row{{1.3, []float64{1}}, {1, []float64{1, 1}}, {1, []float64{1.4, 1}}}, tilt: 50},
	{ID: "splash", Name: "Splash", Description: "One full-page panel", Panels: 1, Tags: []string{"reveal", "splash"},
		rows: []row{{1, []float64{1}}}},
	{ID: "grid-6", Name: "Six-panel grid", Description: "Even 2 × 3 grid", Panels: 6, Tags: []string{"grid"},
		rows: []row{{1, []float64{1, 1}}, {1, []float64{1, 1}}, {1, []float64{1, 1}}}},
	{ID: "grid-9", Name: "Custom grid", Description: "Start from a 3 × 3 grid", Panels: 9, Tags: []string{"grid"},
		rows: []row{{1, []float64{1, 1, 1}}, {1, []float64{1, 1, 1}}, {1, []float64{1, 1, 1}}}},
	{ID: "webtoon-strip", Name: "Webtoon scroll", Description: "Tall stacked panels with breathing space", Panels: 3, Tags: []string{"webtoon"},
		rows: []row{{1, []float64{1}}, {1.3, []float64{1}}, {1, []float64{1}}}},
}

// Layouts returns the built-in layouts.
func Layouts() []Layout {
	out := make([]Layout, len(builtin))
	copy(out, builtin)
	return out
}

// LayoutByID returns a built-in layout.
func LayoutByID(id string) (Layout, bool) {
	for _, l := range builtin {
		if l.ID == id {
			return l, true
		}
	}
	return Layout{}, false
}

// Suggest returns layouts that make exactly n panels, best first, falling back
// to the closest panel counts. direction "vertical" prefers webtoon layouts.
func Suggest(n int, direction string) []Layout {
	ls := Layouts()
	sort.SliceStable(ls, func(i, j int) bool {
		di, dj := abs(ls[i].Panels-n), abs(ls[j].Panels-n)
		if direction == DirVertical {
			wi, wj := hasTag(ls[i], "webtoon"), hasTag(ls[j], "webtoon")
			if wi != wj {
				return wi
			}
		}
		return di < dj
	})
	return ls
}

// PanelPoints computes the panel polygons for a page, in reading order.
func (l Layout) PanelPoints(width, height int, direction string) [][]Point {
	w, h := float64(width), float64(height)
	s := w / baseWidth
	margin, gx, gy := marginPx*s, gutterPx*s, gutterVert*s
	if direction == DirVertical {
		gy = 120 * s
	}
	innerW, innerH := w-2*margin, h-2*margin
	var total float64
	for _, r := range l.rows {
		total += r.weight
	}
	availH := innerH - gy*float64(len(l.rows)-1)
	tilt := l.tilt * s
	var out [][]Point
	y := margin
	for ri, r := range l.rows {
		rh := availH * r.weight / total
		var cw float64
		for _, c := range r.cells {
			cw += c
		}
		availW := innerW - gx*float64(len(r.cells)-1)
		x := margin
		cells := make([][]Point, len(r.cells))
		for ci, c := range r.cells {
			cwPx := availW * c / cw
			x0, x1 := x, x+cwPx
			// Slant inner vertical gutters for action layouts: top shifted right, bottom left.
			t0, b0, t1, b1 := x0, x0, x1, x1
			if tilt != 0 {
				if ci > 0 {
					t0, b0 = x0+tilt/2, x0-tilt/2
				}
				if ci < len(r.cells)-1 {
					t1, b1 = x1+tilt/2, x1-tilt/2
				}
			}
			top, bot := y, y+rh
			// Slant horizontal gutters slightly too (alternate per row) for action layouts.
			if tilt != 0 && len(l.rows) > 1 {
				d := tilt / 3
				if ri%2 == 1 {
					d = -d
				}
				cells[ci] = []Point{{t0, top + ifel(ri > 0, d, 0)}, {t1, top - ifel(ri > 0, d, 0)},
					{b1, bot + ifel(ri < len(l.rows)-1, -d, 0)}, {b0, bot - ifel(ri < len(l.rows)-1, -d, 0)}}
			} else {
				cells[ci] = []Point{{t0, top}, {t1, top}, {b1, bot}, {b0, bot}}
			}
			x = x1 + gx
		}
		if direction == DirRTL {
			// Mirror horizontally so the first cell in reading order is on the right.
			for i := range cells {
				for k := range cells[i] {
					cells[i][k].X = w - cells[i][k].X
				}
				// Keep clockwise winding after mirroring.
				pts := cells[i]
				pts[0], pts[1] = pts[1], pts[0]
				pts[2], pts[3] = pts[3], pts[2]
			}
		}
		for i := range cells {
			for k := range cells[i] {
				cells[i][k] = Point{X: round1(clamp(cells[i][k].X, 0, w)), Y: round1(clamp(cells[i][k].Y, 0, h))}
			}
		}
		out = append(out, cells...)
		y += rh + gy
	}
	return out
}

func ifel(c bool, a, b float64) float64 {
	if c {
		return a
	}
	return b
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
func round1(v float64) float64        { return math.Round(v*10) / 10 }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func hasTag(l Layout, tag string) bool {
	for _, t := range l.Tags {
		if t == tag {
			return true
		}
	}
	return false
}
