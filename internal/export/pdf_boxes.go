package export

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
)

// boxCanvasPDF adds real page boxes using a standards-compliant incremental
// update. It accepts ONLY our pinned canvas writer's unencrypted classic-xref
// output. It never scans image streams for PDF tokens, parses uploaded PDFs or
// rewrites content bytes. Unknown writer layouts fail closed. The original
// object offsets remain valid and /Prev links the replacement page xrefs.
func boxCanvasPDF(data []byte, t Trim, wantPages int) ([]byte, error) {
	bad := errors.New("export: unexpected canvas PDF structure")
	marker := []byte("\nstartxref\n")
	i := bytes.LastIndex(data, marker)
	if i < 0 {
		return nil, bad
	}
	line := bytes.SplitN(data[i+len(marker):], []byte("\n"), 2)[0]
	xref, err := strconv.Atoi(string(line))
	if err != nil || xref < 0 || xref >= len(data) || !bytes.HasPrefix(data[xref:], []byte("xref\n0 ")) {
		return nil, bad
	}
	reader := bytes.NewReader(data[xref:])
	var word string
	var first, count int
	if _, err := fmt.Fscan(reader, &word, &first, &count); err != nil || word != "xref" || first != 0 || count < 4 || count > 1000000 {
		return nil, bad
	}
	offsets := make([]int, count)
	for n := 0; n < count; n++ {
		var offText, genText, state string
		if _, err := fmt.Fscan(reader, &offText, &genText, &state); err != nil {
			return nil, bad
		}
		off, e1 := strconv.Atoi(offText)
		gen, e2 := strconv.Atoi(genText)
		if e1 != nil || e2 != nil {
			return nil, bad
		}
		if n == 0 {
			if state != "f" {
				return nil, bad
			}
			continue
		}
		if gen != 0 || state != "n" || off <= 0 || off >= xref {
			return nil, bad
		}
		offsets[n] = off
	}
	object := func(id int) ([]byte, error) {
		if id <= 0 || id >= len(offsets) {
			return nil, bad
		}
		off := offsets[id]
		prefix := fmt.Sprintf("%d 0 obj\n", id)
		if !bytes.HasPrefix(data[off:], []byte(prefix)) {
			return nil, bad
		}
		end := xref
		for _, o := range offsets {
			if o > off && o < end {
				end = o
			}
		}
		raw := bytes.TrimSpace(data[off+len(prefix) : end])
		if !bytes.HasSuffix(raw, []byte("endobj")) {
			return nil, bad
		}
		return bytes.TrimSpace(bytes.TrimSuffix(raw, []byte("endobj"))), nil
	}
	tree, err := object(3)
	if err != nil {
		return nil, err
	}
	kids := regexp.MustCompile(`/Kids\s*\[([^\]]*)\]`).FindSubmatch(tree)
	if len(kids) != 2 {
		return nil, bad
	}
	refs := regexp.MustCompile(`(\d+)\s+0\s+R`).FindAllSubmatch(kids[1], -1)
	if len(refs) != wantPages || wantPages < 1 {
		return nil, bad
	}
	type replacement struct {
		id, offset int
		dictionary []byte
	}
	replacements := make([]replacement, 0, wantPages)
	seen := map[int]bool{}
	for _, ref := range refs {
		id, err := strconv.Atoi(string(ref[1]))
		if err != nil || seen[id] {
			return nil, bad
		}
		seen[id] = true
		raw, err := object(id)
		if err != nil {
			return nil, err
		}
		if !regexp.MustCompile(`/Type\s*/Page(?:[\s/<>]|$)`).Match(raw) || !bytes.HasPrefix(raw, []byte("<<")) || !bytes.HasSuffix(raw, []byte(">>")) || bytes.Contains(raw, []byte("stream")) {
			return nil, bad
		}
		media := regexp.MustCompile(`/MediaBox\s*\[([^\]]+)\]`).FindSubmatch(raw)
		if len(media) != 2 {
			return nil, bad
		}
		var x0, y0, x1, y1 float64
		if _, err := fmt.Sscan(string(media[1]), &x0, &y0, &x1, &y1); err != nil || x0 != 0 || y0 != 0 || !closeMM(x1, (t.WidthMM+2*t.BleedMM)*72/25.4) || !closeMM(y1, (t.HeightMM+2*t.BleedMM)*72/25.4) {
			return nil, bad
		}
		b := t.BleedMM * 72 / 25.4
		dict := append([]byte(nil), raw[:len(raw)-2]...)
		dict = append(dict, []byte(fmt.Sprintf(" /TrimBox [%.8f %.8f %.8f %.8f] /BleedBox [0 0 %.8f %.8f] >>", b, b, b+t.WidthMM*72/25.4, b+t.HeightMM*72/25.4, (t.WidthMM+2*t.BleedMM)*72/25.4, (t.HeightMM+2*t.BleedMM)*72/25.4))...)
		replacements = append(replacements, replacement{id: id, dictionary: dict})
	}
	var out bytes.Buffer
	out.Write(data)
	out.WriteByte('\n')
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].id < replacements[j].id })
	for i := range replacements {
		r := &replacements[i]
		r.offset = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", r.id, r.dictionary)
	}
	newXref := out.Len()
	out.WriteString("xref\n")
	for _, r := range replacements {
		fmt.Fprintf(&out, "%d 1\n%010d 00000 n \n", r.id, r.offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info 2 0 R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", count, xref, newXref)
	return out.Bytes(), nil
}
func closeMM(a, b float64) bool {
	return !math.IsNaN(a) && !math.IsInf(a, 0) && math.Abs(a-b) < .01
}
