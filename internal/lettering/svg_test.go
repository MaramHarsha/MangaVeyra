package lettering

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/tdewolff/canvas"
)

// svgPath must write exactly what canvas writes, for any number.
func TestSVGPathMatchesCanvasForAnyNumber(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	special := []float64{0, math.Copysign(0, -1), 1, -1, .5, -.5, .001, .000999, .0001, 1e-12, 100, 1000, -1000, 1200, 12000,
		99999999, 1e8, 123456789, 2147483648, 0.99999999995, 0.0009999999996, 0.00100000001, 123.45678, 1.5e-5, 20000, 19999.995}
	value := func(i int) float64 {
		if i < len(special) {
			return special[i]
		}
		switch r.IntN(6) {
		case 0: // integers, often round
			return float64(r.IntN(30000)-15000) * math.Pow(10, float64(r.IntN(4)))
		case 1: // tiny
			return (r.Float64() - .5) * math.Pow(10, -float64(r.IntN(12)))
		case 2: // huge
			return (r.Float64() - .5) * math.Pow(10, float64(r.IntN(12)))
		case 3: // page coordinates rounded near eight digits
			return math.Round((r.Float64()*40000-20000)*1e4) / 1e4
		}
		return r.Float64()*40000 - 20000
	}
	for k := 0; k < 4000; k++ {
		p := &canvas.Path{}
		i := k * 9
		p.MoveTo(value(i), value(i+1))
		switch k % 4 {
		case 0:
			p.LineTo(value(i+2), value(i+3))
			p.LineTo(value(i+2), value(i+4)) // vertical
			p.LineTo(value(i+5), value(i+4)) // horizontal
		case 1:
			p.QuadTo(value(i+2), value(i+3), value(i+4), value(i+5))
		case 2:
			p.CubeTo(value(i+2), value(i+3), value(i+4), value(i+5), value(i+6), value(i+7))
		case 3:
			p.LineTo(value(i+2), value(i+3))
			p.ArcTo(5, 3, 0, false, true, value(i+4), value(i+5))
		}
		p.Close()
		p.MoveTo(value(i+8), value(i))
		p.LineTo(value(i+1), value(i+8))
		if got, want := svgPath(p), p.ToSVG(); got != want {
			t.Fatalf("path %d:\n got  %s\n want %s", k, got, want)
		}
	}
	if svgPath(&canvas.Path{}) != "" {
		t.Fatal("empty path")
	}
}

// Every outline in the golden corpus formats identically.
func TestSVGPathMatchesCanvasOnCorpus(t *testing.T) {
	compared, fast := 0, 0
	pathSVG = func(p *canvas.Path) string {
		want := p.ToSVG()
		if got := svgPath(p); got != want {
			t.Errorf("svgPath differs from canvas:\n got  %.300s\n want %.300s", got, want)
		}
		if _, ok := fastSVG(p); ok {
			fast++
		}
		compared++
		return want
	}
	defer func() { pathSVG = svgPath }()
	e := testEngine(t)
	for i, c := range fitCorpus() {
		if i%3 != 0 && len(c.b.Texts[c.lang].Text) < 1000 {
			continue
		}
		if _, err := e.Layout(c.b, c.lang); err != nil {
			t.Fatal(err)
		}
		if t.Failed() {
			t.Fatalf("%s", c.name)
		}
	}
	if compared < 2000 || fast < compared*9/10 {
		t.Fatalf("%d paths compared, %d by the strconv writer", compared, fast)
	}
	t.Logf("%d paths compared, %d by the strconv writer", compared, fast)
}
