package seed

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// These illustrations are intentionally simple, labelled authored fixtures.
// They need no AI service, network, installed font or random-number source.
type artSpec struct{ key, kind, name string }

func (f *fixture) artSpecs() []artSpec {
	out := []artSpec{{"cover", "cover", demoTitle}, {"quay", "layer", "Tidebreak Quay"}, {"tower", "layer", "Bellward Tower"}}
	for _, c := range characters {
		out = append(out, artSpec{c.key, "character_ref", c.name})
	}
	return append(out, artSpec{"fx", "layer", "Wind and rain"})
}

var ink = color.NRGBA{R: 24, G: 24, B: 28, A: 255}
var paper = color.NRGBA{R: 250, G: 249, B: 245, A: 255}

func renderArt(spec artSpec) ([]byte, error) {
	im := image.NewNRGBA(image.Rect(0, 0, 640, 960))
	if spec.key == "quay" || spec.key == "tower" || spec.key == "cover" {
		draw.Draw(im, im.Bounds(), image.NewUniform(paper), image.Point{}, draw.Src)
		drawHarbor(im, spec.key == "tower")
	}
	switch spec.key {
	case "cover":
		for i := 0; i < 12; i++ {
			line(im, 0, 235+i*4, 640, 235+i*4, color.NRGBA{R: 225, G: 225, B: 222, A: 255}, 1)
		}
		label(im, 28, 44, "TIDEBREAK", 6, ink)
		label(im, 28, 130, "COURIER", 6, ink)
		label(im, 28, 230, "THE WAY HOME", 3, ink)
		label(im, 28, 818, "AN ORIGINAL MANGAVEYRA DEMO", 2, ink)
	case "quay", "tower":
		backgroundPlate(im, strings.ToUpper(spec.name))
	case "fx":
		for i := 0; i < 30; i++ {
			x := (i*137 + 53) % 680
			y := (i*229 + 89) % 850
			line(im, x, y, x-70, y+140, color.NRGBA{R: 36, G: 36, B: 40, A: 110}, 2)
		}
	default:
		drawCharacter(im, spec.key)
		characterPlate(im, strings.ToUpper(spec.name))
	}
	if spec.key == "cover" {
		// The cover is shown whole, so its plate may use the bottom edge.
		draw.Draw(im, image.Rect(14, 886, 626, 946), image.NewUniform(paper), image.Point{}, draw.Src)
		line(im, 14, 886, 626, 886, ink, 2)
		label(im, 28, 900, "DEMO PLACEHOLDER ART", 2, ink)
		label(im, 28, 925, "ORIGINAL AUTHORED FIXTURE - NO AI GENERATION", 1, ink)
	}
	// The FX layer carries no baked text: it sits above the characters in
	// every panel, and its layer name already marks it as placeholder art.
	var out bytes.Buffer
	err := png.Encode(&out, im)
	return out.Bytes(), err
}

// backgroundPlate labels a location inside backgroundSafe, the band of the
// image that stays visible when a panel of any demo aspect crops it (layers
// cover their panel), so the label is never cut at a panel edge.
var backgroundSafe = image.Rect(16, 212, 240, 292)

func backgroundPlate(im *image.NRGBA, name string) {
	r := backgroundSafe
	draw.Draw(im, r, image.NewUniform(paper), image.Point{}, draw.Src)
	box(im, r.Min.X, r.Min.Y, r.Max.X-1, r.Max.Y-1, ink, 2)
	label(im, r.Min.X+12, r.Min.Y+8, name, 2, ink)
	label(im, r.Min.X+12, r.Min.Y+42, "DEMO PLACEHOLDER ART", 1, ink)
	label(im, r.Min.X+12, r.Min.Y+60, "NO AI GENERATION", 1, ink)
}

// characterBox is the part of a character image that holds the figure and its
// label plate; everything outside it stays transparent. Panels place each
// character's box in its own slot, so labels never overlap or leave the panel.
var characterBox = image.Rect(130, 160, 510, 900)

// characterPlate centres the name and the placeholder notice on a small opaque
// plate under the figure, inside characterBox.
func characterPlate(im *image.NRGBA, name string) {
	const nameScale, noteScale = 3, 2
	note := "DEMO PLACEHOLDER ART"
	w := max(len(name)*7*nameScale, len(note)*7*noteScale) + 24
	cx := characterBox.Min.X + characterBox.Dx()/2
	plate := image.Rect(cx-w/2, 800, cx+w/2, 894)
	draw.Draw(im, plate, image.NewUniform(paper), image.Point{}, draw.Src)
	box(im, plate.Min.X, plate.Min.Y, plate.Max.X-1, plate.Max.Y-1, ink, 2)
	label(im, cx-len(name)*7*nameScale/2, 810, name, nameScale, ink)
	label(im, cx-len(note)*7*noteScale/2, 857, note, noteScale, ink)
}

func drawHarbor(im *image.NRGBA, tower bool) {
	// Stippled sky and water make the small diagrams read as screentone.
	for y := 270; y < 875; y += 8 {
		for x := 0; x < 640; x += 8 {
			if (x/8+y/8)%3 == 0 {
				im.SetNRGBA(x, y, color.NRGBA{R: 150, G: 150, B: 150, A: 255})
			}
		}
	}
	line(im, 0, 570, 640, 570, ink, 4)
	for y := 594; y < 850; y += 38 {
		for x := (y * 7) % 90; x < 620; x += 110 {
			line(im, x, y, x+65, y-4, ink, 2)
		}
	}
	if tower {
		draw.Draw(im, image.Rect(252, 320, 402, 685), image.NewUniform(paper), image.Point{}, draw.Src)
		box(im, 252, 320, 402, 685, ink, 4)
		line(im, 220, 320, 327, 245, ink, 5)
		line(im, 327, 245, 435, 320, ink, 5)
		box(im, 285, 360, 368, 440, ink, 4)
		ellipse(im, 327, 401, 20, 27, ink, true)
		line(im, 327, 424, 327, 560, ink, 3)
		for y := 480; y < 680; y += 40 {
			line(im, 253, y, 401, y, ink, 1)
		}
	} else {
		draw.Draw(im, image.Rect(35, 400, 215, 690), image.NewUniform(paper), image.Point{}, draw.Src)
		box(im, 35, 400, 215, 690, ink, 4)
		line(im, 20, 400, 125, 320, ink, 4)
		line(im, 125, 320, 230, 400, ink, 4)
		label(im, 61, 430, "POST", 4, ink)
		box(im, 75, 525, 160, 640, ink, 3)
		line(im, 160, 682, 630, 760, ink, 6)
		line(im, 160, 710, 630, 788, ink, 4)
		for x := 250; x < 630; x += 100 {
			line(im, x, 694+(x-250)/6, x, 835, ink, 5)
		}
	}
	// A small empty skiff, separate from the character layers.
	line(im, 365, 740, 590, 740, ink, 4)
	line(im, 365, 740, 410, 790, ink, 4)
	line(im, 410, 790, 550, 790, ink, 4)
	line(im, 550, 790, 590, 740, ink, 4)
}

func drawCharacter(im *image.NRGBA, key string) {
	if key == "pip" {
		ellipse(im, 320, 540, 140, 115, paper, true)
		ellipse(im, 320, 540, 140, 115, ink, false)
		ellipse(im, 390, 440, 72, 70, paper, true)
		ellipse(im, 390, 440, 72, 70, ink, false)
		line(im, 445, 440, 505, 463, ink, 5)
		line(im, 505, 463, 445, 475, ink, 5)
		ellipse(im, 411, 429, 7, 7, ink, true)
		line(im, 278, 536, 352, 595, ink, 7)
		line(im, 310, 650, 294, 711, ink, 4)
		line(im, 360, 650, 374, 711, ink, 4)
		line(im, 374, 378, 390, 350, ink, 4)
		return
	}
	faceY, headW := 300, 85
	if key == "wren" {
		faceY, headW = 365, 76
	}
	// Opaque paper inside the figure; outside stays transparent for independent placement.
	ellipse(im, 320, faceY, headW, 105, paper, true)
	ellipse(im, 320, faceY, headW, 105, ink, false)
	box(im, 224, faceY+95, 416, 700, ink, 5)
	draw.Draw(im, image.Rect(228, faceY+98, 413, 699), image.NewUniform(paper), image.Point{}, draw.Src)
	for y := faceY + 120; y < 690; y += 12 {
		for x := 238; x < 405; x += 12 {
			im.SetNRGBA(x, y, ink)
		}
	}
	line(im, 260, 700, 250, 786, ink, 7)
	line(im, 378, 700, 390, 786, ink, 7)
	line(im, 260, 786, 218, 786, ink, 7)
	line(im, 390, 786, 430, 786, ink, 7)
	line(im, 224, faceY+110, 171, 630, ink, 7)
	line(im, 416, faceY+110, 470, 610, ink, 7)
	ellipse(im, 292, faceY-5, 5, 5, ink, true)
	ellipse(im, 347, faceY-5, 5, 5, ink, true)
	line(im, 300, faceY+42, 340, faceY+42, ink, 3)
	switch key {
	case "rin":
		for i := 0; i < 7; i++ {
			line(im, 237+i*22, faceY-72, 230+i*22, faceY-125+(i%2)*20, ink, 8)
		}
		line(im, 230, faceY+118, 405, 660, ink, 12)
		box(im, 330, 555, 453, 670, ink, 5)
		line(im, 235, faceY+109, 410, faceY+109, ink, 14)
	case "kaito":
		for x := 246; x < 398; x += 11 {
			line(im, x, faceY-80, x+6, faceY-102, ink, 6)
		}
		box(im, 252, faceY+138, 389, 645, ink, 4)
		line(im, 230, 650, 410, 650, ink, 12)
		line(im, 340, 632, 349, 690, ink, 7)
	case "wren":
		line(im, 200, faceY-90, 440, faceY-90, ink, 13)
		box(im, 250, faceY-150, 390, faceY-92, ink, 6)
		ellipse(im, 286, faceY, 26, 22, ink, false)
		ellipse(im, 352, faceY, 26, 22, ink, false)
		line(im, 312, faceY, 326, faceY, ink, 3)
		for y := 520; y < 670; y += 40 {
			ellipse(im, 320, y, 5, 5, ink, true)
		}
	case "sora":
		line(im, 240, faceY-60, 225, 530, ink, 13)
		line(im, 400, faceY-65, 430, 505, ink, 13)
		line(im, 227, 580, 413, 580, ink, 9)
		for i := 0; i < 4; i++ {
			ellipse(im, 390, 593+i*10, 28, 12, ink, false)
		}
	}
}

func box(im *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA, width int) {
	line(im, x0, y0, x1, y0, c, width)
	line(im, x1, y0, x1, y1, c, width)
	line(im, x1, y1, x0, y1, c, width)
	line(im, x0, y1, x0, y0, c, width)
}

func line(im *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA, width int) {
	steps := max(abs(x1-x0), abs(y1-y0))
	for i := 0; i <= max(steps, 1); i++ {
		t := float64(i) / float64(max(steps, 1))
		x, y := x0+int(float64(x1-x0)*t), y0+int(float64(y1-y0)*t)
		for dy := -width / 2; dy <= width/2; dy++ {
			for dx := -width / 2; dx <= width/2; dx++ {
				im.SetNRGBA(x+dx, y+dy, c)
			}
		}
	}
}

func ellipse(im *image.NRGBA, cx, cy, rx, ry int, c color.NRGBA, fill bool) {
	for y := -ry; y <= ry; y++ {
		for x := -rx; x <= rx; x++ {
			d := math.Pow(float64(x)/float64(rx), 2) + math.Pow(float64(y)/float64(ry), 2)
			if d <= 1 && (fill || d >= .90) {
				im.SetNRGBA(cx+x, cy+y, c)
			}
		}
	}
}

func label(im *image.NRGBA, x, y int, text string, scale int, c color.NRGBA) {
	mask := image.NewNRGBA(image.Rect(0, 0, len(text)*7, 15))
	d := font.Drawer{Dst: mask, Src: image.NewUniform(c), Face: basicfont.Face7x13, Dot: fixed.P(0, 12)}
	d.DrawString(text)
	for yy := 0; yy < mask.Bounds().Dy(); yy++ {
		for xx := 0; xx < mask.Bounds().Dx(); xx++ {
			v := mask.NRGBAAt(xx, yy)
			if v.A > 0 {
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						im.SetNRGBA(x+xx*scale+dx, y+yy*scale+dy, v)
					}
				}
			}
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
