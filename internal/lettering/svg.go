package lettering

import (
	"math"
	"strconv"

	"github.com/tdewolff/canvas"
)

// pathSVG formats glyph and bubble outlines; tests wrap it to compare
// svgPath with canvas.
var pathSVG = svgPath

// svgPath returns p.ToSVG() byte for byte without fmt, which made SVG
// output most of the cost of a long bubble. canvas writes every coordinate
// as fmt "%.*g" with canvas.Precision digits, minified by minify.Number. For
// the numbers outlines are made of (decimals of magnitude at least 0.001,
// integers below 1e8, and zero) minify.Number only drops the zero before a
// decimal point and writes three or more trailing zeros as an exponent, so
// strconv gives the same text. A path with any other number (exponent form,
// smaller magnitudes, negative zero) or an arc is formatted by canvas
// itself.
func svgPath(p *canvas.Path) string {
	if s, ok := fastSVG(p); ok {
		return s
	}
	return p.ToSVG()
}

// fastSVG is svgPath's strconv writer; false means canvas must write p.
func fastSVG(p *canvas.Path) (string, bool) {
	if p.Empty() {
		return "", true
	}
	if canvas.Precision < 1 {
		return "", false
	}
	d := p.Data()
	buf := make([]byte, 0, len(d)*6)
	var scratch [40]byte
	var last byte
	// put appends one field: a command letter with its first number, or a
	// further number, separated by a space only between two digits.
	put := func(cmd byte, v float64) bool {
		n, ok := svgNumber(scratch[:0], v)
		if !ok {
			return false
		}
		if cmd != 0 {
			buf = append(buf, cmd)
		} else if '0' <= last && last <= '9' && '0' <= n[0] && n[0] <= '9' {
			buf = append(buf, ' ')
		}
		buf = append(buf, n...)
		last = n[len(n)-1]
		return true
	}
	var x, y float64
	for i := 0; i < len(d); {
		ok := true
		switch d[i] {
		case canvas.MoveToCmd:
			x, y = d[i+1], d[i+2]
			ok = put('M', x) && put(0, y)
			i += 4
		case canvas.LineToCmd:
			xs, ys := x, y
			x, y = d[i+1], d[i+2]
			switch {
			case canvas.Equal(x, xs) && canvas.Equal(y, ys):
			case canvas.Equal(x, xs):
				ok = put('V', y)
			case canvas.Equal(y, ys):
				ok = put('H', x)
			default:
				ok = put('L', x) && put(0, y)
			}
			i += 4
		case canvas.QuadToCmd:
			x, y = d[i+3], d[i+4]
			ok = put('Q', d[i+1]) && put(0, d[i+2]) && put(0, x) && put(0, y)
			i += 6
		case canvas.CubeToCmd:
			x, y = d[i+5], d[i+6]
			ok = put('C', d[i+1]) && put(0, d[i+2]) && put(0, d[i+3]) && put(0, d[i+4]) && put(0, x) && put(0, y)
			i += 8
		case canvas.CloseCmd:
			x, y = d[i+1], d[i+2]
			buf, last = append(buf, 'z'), 'z'
			i += 4
		default: // arcs never occur in outlines
			ok = false
		}
		if !ok {
			return "", false
		}
	}
	return string(buf), true
}

// svgNumber appends v as canvas's SVG writer does, or reports false when
// that needs minify.Number's rewriting.
func svgNumber(b []byte, v float64) ([]byte, bool) {
	if v == 0 {
		if math.Signbit(v) {
			return nil, false
		}
		return append(b, '0'), true
	}
	b = strconv.AppendFloat(b, v, 'g', canvas.Precision, 64)
	dot := false
	for _, c := range b {
		switch c {
		case 'e', 'E', 'I', 'N':
			return nil, false
		case '.':
			dot = true
		}
	}
	if !dot {
		zeros := 0
		for i := len(b) - 1; i >= 0 && b[i] == '0'; i-- {
			zeros++
		}
		if zeros < 3 {
			return b, true
		}
		// minify writes three or more trailing zeros as an exponent: 2000 → 2e3.
		return strconv.AppendInt(append(b[:len(b)-zeros], 'e'), int64(zeros), 10), true
	}
	switch a := math.Abs(v); {
	case a >= 1:
		return b, true
	case a < 0.001:
		return nil, false // minify may write .0001 as 1e-4
	case b[0] == '-':
		return append(b[:1], b[2:]...), true // -0.5 → -.5
	}
	return b[1:], true // 0.5 → .5
}
