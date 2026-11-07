package editor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/authui"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// option is a value with its localised label.
type option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// chapterLink is one entry of the top bar's chapter switcher.
type chapterLink struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Href   string `json:"href"`
}

// printInfo carries the localised format and trim labels of the footer
// ("Manga · right to left", "B5 · 182 × 257 mm") and the trim size the
// lettering inspector converts page pixels to points with.
type printInfo struct {
	FormatLabel string  `json:"format_label"`
	SizeLabel   string  `json:"size_label"`
	WidthMM     float64 `json:"width_mm,omitempty"`
	HeightMM    float64 `json:"height_mm,omitempty"`
}

// beat summarises the script panels written for one page number.
type beat struct {
	Panels int `json:"panels"`
	Wide   int `json:"wide"`
	Medium int `json:"medium"`
	Close  int `json:"close"`
}

var trims = map[string][2]float64{"b5": {182, 257}, "a5": {148, 210}, "a4": {210, 297}}

func printFor(ctx context.Context, p studio.Project) printInfo {
	out := printInfo{FormatLabel: labelOr(ctx, "editor.format."+p.Format, p.Format)}
	if t, ok := trims[p.PageSize]; ok {
		out.WidthMM, out.HeightMM = t[0], t[1]
		out.SizeLabel = labelOr(ctx, "editor.page_size."+p.PageSize, strings.ToUpper(p.PageSize))
	} else {
		out.SizeLabel = i18n.T(ctx, "editor.page_size.pixels", "width", p.PageWidth, "height", p.PageHeight)
	}
	return out
}

// labelOr translates key, falling back when the catalogue has no entry.
func labelOr(ctx context.Context, key, fallback string) string {
	if v := i18n.T(ctx, key); v != "" && v != key {
		return v
	}
	return fallback
}

// styles lists the project art styles with their onboarding labels, keeping a
// stored style that is not in the list so the select never hides it.
func styles(ctx context.Context, current string) []option {
	out := make([]option, 0, len(authui.ArtStyles)+1)
	found := current == ""
	for _, s := range authui.ArtStyles {
		out = append(out, option{ID: s.Value, Label: labelOr(ctx, "style."+s.Value, s.Value)})
		found = found || s.Value == current
	}
	if !found {
		out = append(out, option{ID: current, Label: labelOr(ctx, "editor.style_legacy."+current, humanize(current))})
	}
	return out
}

// humanize turns a stored identifier ("bw_screentone") into readable text
// ("Bw screentone") for styles the catalogue has no label for.
func humanize(id string) string {
	s := strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(id))
	if s == "" {
		return id
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func shotClass(shot string) string {
	s := strings.ToLower(shot)
	switch {
	case strings.Contains(s, "close"), strings.Contains(s, "detail"):
		return "close"
	case strings.Contains(s, "medium"), strings.Contains(s, "mid"), strings.Contains(s, "waist"):
		return "medium"
	case strings.Contains(s, "wide"), strings.Contains(s, "establish"), strings.Contains(s, "long"), strings.Contains(s, "full"), strings.Contains(s, "splash"):
		return "wide"
	}
	return ""
}

// beats reads the chapter script and summarises it per page number. A chapter
// without a script has no beats.
func (m *Module) beats(ctx context.Context, chapterID string) map[int]beat {
	out := map[int]beat{}
	doc, err := m.studio.GetScript(ctx, chapterID)
	if err != nil {
		return out
	}
	for _, scene := range doc.Scenes {
		for _, p := range scene.Panels {
			if p.PageNumber <= 0 {
				continue
			}
			b := out[p.PageNumber]
			b.Panels++
			switch shotClass(p.Shot) {
			case "wide":
				b.Wide++
			case "medium":
				b.Medium++
			case "close":
				b.Close++
			}
			out[p.PageNumber] = b
		}
	}
	return out
}

// maxSuggested is how many layouts the Suggested tab shows (design D2).
const maxSuggested = 7

// layouts returns every built-in layout with localised names, marking the
// best fits for a page of n panels as suggested. The 3 × 3 grid is a plain
// preset; it is never suggested in place of the designed custom-grid tile.
func layouts(ctx context.Context, p studio.Project, n int) []layoutView {
	direction := pagedoc.DirectionForFormat(p.Format)
	chosen := map[string]bool{}
	count := 0
	for _, l := range pagedoc.Suggest(n, direction) {
		if l.ID == "grid-9" || count == maxSuggested {
			continue
		}
		chosen[l.ID] = true
		count++
	}
	out := []layoutView{}
	for _, l := range pagedoc.Layouts() {
		out = append(out, layoutView{ID: l.ID, Name: labelOr(ctx, "editor.layout."+l.ID, l.Name), Panels: l.Panels, Points: l.PanelPoints(p.PageWidth, p.PageHeight, direction), Suggested: chosen[l.ID]})
	}
	return out
}

// panelTarget is the panel count suggestions are made for: the script beat
// for the page when there is one, otherwise the page's panels (5 for a new page).
func panelTarget(b beat, ok bool, panels int) int {
	if ok && b.Panels > 0 {
		return b.Panels
	}
	if panels > 0 {
		return panels
	}
	return 5
}

func chapterLinks(chapters []studio.Chapter, projectID string) []chapterLink {
	out := make([]chapterLink, 0, len(chapters))
	for _, c := range chapters {
		out = append(out, chapterLink{ID: c.ID, Number: c.Number, Title: c.Title, Href: fmt.Sprintf("/studio/p/%s/chapters/%s/edit", projectID, c.ID)})
	}
	return out
}

// pageLayouts serves the D2 picker for one page: the layouts suggested for its
// script beat (or panel count) and the beat itself for the hint line.
func (m *Module) pageLayouts(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	b, ok := m.beats(r.Context(), res.Chapter.ID)[res.Page.Number]
	out := map[string]any{"layouts": layouts(r.Context(), res.Project, panelTarget(b, ok, len(res.Page.Panels)))}
	if ok {
		out["beat"] = b
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, out)
	return nil
}
