package studio

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// Template kinds (templates.kind).
const (
	TemplateStory  = "story"
	TemplateLayout = "layout"
)

// LayoutTemplate is a page layout: panel polygons in page percent for one
// page (or two for a spread), listed in reading order for the project's
// direction (right-to-left layouts are mirrored when applied to LTR comics).
type LayoutTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Badge is the mono tag on the card ("4 PANELS", "SPREAD", "STRIP", "SPLASH", "GRID").
	Badge   string      `json:"badge"`
	Pages   [][]Polygon `json:"pages"`
	Builtin bool        `json:"builtin"`
	// NameKey/DescKey translate built-in templates.
	NameKey string `json:"-"`
	DescKey string `json:"-"`
	// Vertical marks webtoon strip layouts.
	Vertical bool `json:"vertical,omitempty"`
}

// PanelCount returns the number of panels on the first page.
func (t *LayoutTemplate) PanelCount() int {
	if len(t.Pages) == 0 {
		return 0
	}
	return len(t.Pages[0])
}

// StoryArcSpec is one arc of a story template.
type StoryArcSpec struct {
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Chapters int      `json:"chapters"`
	Beats    []string `json:"beats,omitempty"`
	// TitleKey/SummaryKey translate built-in arcs; BeatKeys the beats.
	TitleKey   string   `json:"-"`
	SummaryKey string   `json:"-"`
	BeatKeys   []string `json:"-"`
}

// StoryTemplate is a story structure: arcs with chapter counts and beats.
type StoryTemplate struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Badge       string         `json:"badge"`
	Arcs        []StoryArcSpec `json:"arcs"`
	// Format and LayoutID are suggestions for the wizard ("" = keep).
	Format   string `json:"format,omitempty"`
	LayoutID string `json:"layout_id,omitempty"`
	Builtin  bool   `json:"builtin"`
	NameKey  string `json:"-"`
	DescKey  string `json:"-"`
}

// Chapters returns the total chapter count.
func (t *StoryTemplate) Chapters() int {
	n := 0
	for _, a := range t.Arcs {
		n += a.Chapters
	}
	return n
}

// ---- geometry -------------------------------------------------------------------------

const (
	pageMargin = 5.0
	pageGutter = 2.2
)

// rowSpec describes one tier of panels.
type rowSpec struct {
	H float64 // relative height
	// Cols are relative widths, in reading order left to right.
	Cols []float64
	// Slant is the vertical offset (page %) of this row's bottom border's right end relative to its left end.
	Slant float64
	// ColSlant tilts the vertical gutters (top x − bottom x, page %).
	ColSlant float64
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// rowsLayout builds polygons for tiers of panels with optional diagonal gutters.
func rowsLayout(rows []rowSpec, margin, gutter float64) []Polygon {
	x0, x1 := margin, 100-margin
	y0, y1 := margin, 100-margin
	total := 0.0
	for _, r := range rows {
		total += r.H
	}
	// Border k is the line between row k-1 and row k: (leftY, rightY).
	type line struct{ l, r float64 }
	borders := make([]line, len(rows)+1)
	borders[0] = line{y0, y0}
	borders[len(rows)] = line{y1, y1}
	acc := 0.0
	for k := 0; k < len(rows)-1; k++ {
		acc += rows[k].H
		mid := y0 + (y1-y0)*acc/total
		s := rows[k].Slant
		borders[k+1] = line{mid - s/2, mid + s/2}
	}
	yAt := func(b line, x float64) float64 { return b.l + (b.r-b.l)*(x-x0)/(x1-x0) }
	var out []Polygon
	for k, r := range rows {
		top, bot := borders[k], borders[k+1]
		topInset, botInset := gutter/2, gutter/2
		if k == 0 {
			topInset = 0
		}
		if k == len(rows)-1 {
			botInset = 0
		}
		cw := 0.0
		for _, c := range r.Cols {
			cw += c
		}
		// Vertical borders: (topX, bottomX).
		xs := [][2]float64{{x0, x0}}
		a := 0.0
		for j := 0; j < len(r.Cols)-1; j++ {
			a += r.Cols[j]
			mid := x0 + (x1-x0)*a/cw
			xs = append(xs, [2]float64{mid + r.ColSlant/2, mid - r.ColSlant/2})
		}
		xs = append(xs, [2]float64{x1, x1})
		for j := range r.Cols {
			lIn, rIn := gutter/2, gutter/2
			if j == 0 {
				lIn = 0
			}
			if j == len(r.Cols)-1 {
				rIn = 0
			}
			ltx, lbx := xs[j][0]+lIn, xs[j][1]+lIn
			rtx, rbx := xs[j+1][0]-rIn, xs[j+1][1]-rIn
			p := Polygon{
				{round1(ltx), round1(yAt(top, ltx) + topInset)},
				{round1(rtx), round1(yAt(top, rtx) + topInset)},
				{round1(rbx), round1(yAt(bot, rbx) - botInset)},
				{round1(lbx), round1(yAt(bot, lbx) - botInset)},
			}
			out = append(out, p)
		}
	}
	return out
}

// GridLayout returns a rows × cols grid (the "Custom grid" template).
func GridLayout(rows, cols int) []Polygon {
	rows, cols = max(1, min(rows, 8)), max(1, min(cols, 6))
	specs := make([]rowSpec, rows)
	for i := range specs {
		c := make([]float64, cols)
		for j := range c {
			c[j] = 1
		}
		specs[i] = rowSpec{H: 1, Cols: c}
	}
	return rowsLayout(specs, pageMargin, pageGutter)
}

// fourKoma is a single column of four equal panels, the yonkoma gag strip.
func fourKoma() []Polygon {
	var out []Polygon
	x0, x1, y, gap := 22.0, 78.0, 5.0, 2.4
	h := (90 - 3*gap) / 4
	for i := 0; i < 4; i++ {
		out = append(out, Polygon{{x0, round1(y)}, {x1, round1(y)}, {x1, round1(y + h)}, {x0, round1(y + h)}})
		y += h + gap
	}
	return out
}

func stripLayout(heights []float64) []Polygon {
	var out []Polygon
	total := 0.0
	for _, h := range heights {
		total += h
	}
	gap := 6.0
	y := 3.0
	avail := 100 - 6 - gap*float64(len(heights)-1)
	for _, h := range heights {
		hh := avail * h / total
		out = append(out, Polygon{{4, round1(y)}, {96, round1(y)}, {96, round1(y + hh)}, {4, round1(y + hh)}})
		y += hh + gap
	}
	return out
}

// builtinLayouts are the shipped layout templates with real panel geometry.
var builtinLayouts = []LayoutTemplate{
	{ID: "builtin:4koma", NameKey: "studio.tpl.layout.4koma", DescKey: "studio.tpl.layout.4koma.desc", Badge: "4 PANELS",
		Pages: [][]Polygon{fourKoma()}},
	{ID: "builtin:action_spread", NameKey: "studio.tpl.layout.action_spread", DescKey: "studio.tpl.layout.action_spread.desc", Badge: "SPREAD",
		Pages: [][]Polygon{
			rowsLayout([]rowSpec{{H: 1, Cols: []float64{1.2, 1}, Slant: -6, ColSlant: 5}, {H: 2.6, Cols: []float64{1}}}, pageMargin, pageGutter),
			rowsLayout([]rowSpec{{H: 3, Cols: []float64{1}, Slant: 8}, {H: 1, Cols: []float64{1, 1, 1}, ColSlant: -4}}, pageMargin, pageGutter),
		}},
	{ID: "builtin:confession", NameKey: "studio.tpl.layout.confession", DescKey: "studio.tpl.layout.confession.desc", Badge: "6 PANELS",
		Pages: [][]Polygon{rowsLayout([]rowSpec{{H: 1, Cols: []float64{1, 1, 1}}, {H: 1, Cols: []float64{1, 1}}, {H: 1.8, Cols: []float64{1}}}, pageMargin, pageGutter)}},
	{ID: "builtin:chase", NameKey: "studio.tpl.layout.chase", DescKey: "studio.tpl.layout.chase.desc", Badge: "7 PANELS",
		Pages: [][]Polygon{rowsLayout([]rowSpec{
			{H: 1, Cols: []float64{1.4, 1}, Slant: 7, ColSlant: 6},
			{H: 1.1, Cols: []float64{1, 1.2, 1}, Slant: -8, ColSlant: -5},
			{H: 1.2, Cols: []float64{1}, Slant: 6},
			{H: 1, Cols: []float64{1}},
		}, pageMargin, pageGutter)}},
	{ID: "builtin:webtoon_scroll", NameKey: "studio.tpl.layout.webtoon_scroll", DescKey: "studio.tpl.layout.webtoon_scroll.desc", Badge: "STRIP",
		Vertical: true, Pages: [][]Polygon{stripLayout([]float64{1.1, 0.8, 1.4})}},
	{ID: "builtin:talking_heads", NameKey: "studio.tpl.layout.talking_heads", DescKey: "studio.tpl.layout.talking_heads.desc", Badge: "6 PANELS",
		Pages: [][]Polygon{GridLayout(3, 2)}},
	{ID: "builtin:reveal", NameKey: "studio.tpl.layout.reveal", DescKey: "studio.tpl.layout.reveal.desc", Badge: "SPLASH",
		Pages: [][]Polygon{rowsLayout([]rowSpec{{H: 0.55, Cols: []float64{1, 1}}, {H: 3, Cols: []float64{1}}}, pageMargin, pageGutter)}},
	{ID: "builtin:fight_beat", NameKey: "studio.tpl.layout.fight_beat", DescKey: "studio.tpl.layout.fight_beat.desc", Badge: "5 PANELS",
		Pages: [][]Polygon{rowsLayout([]rowSpec{
			{H: 1.7, Cols: []float64{1}, Slant: -9},
			{H: 1, Cols: []float64{1, 1, 1}, Slant: 5, ColSlant: 6},
			{H: 1.1, Cols: []float64{1}},
		}, pageMargin, pageGutter)}},
	{ID: "builtin:establishing", NameKey: "studio.tpl.layout.establishing", DescKey: "studio.tpl.layout.establishing.desc", Badge: "3 PANELS",
		Pages: [][]Polygon{rowsLayout([]rowSpec{{H: 1.8, Cols: []float64{1}}, {H: 1, Cols: []float64{1.3, 1}}}, pageMargin, pageGutter)}},
	{ID: "builtin:grid_3x3", NameKey: "studio.tpl.layout.grid", DescKey: "studio.tpl.layout.grid.desc", Badge: "GRID",
		Pages: [][]Polygon{GridLayout(3, 3)}},
}

// fallback layouts by panel count used by SuggestLayout.
var suggestByCount = map[int]string{1: "", 2: "", 3: "builtin:establishing", 4: "", 5: "builtin:fight_beat", 6: "builtin:talking_heads",
	7: "builtin:chase", 9: "builtin:grid_3x3"}

// splashLayout is a single full-page panel.
func splashLayout() []Polygon {
	return []Polygon{{{pageMargin, pageMargin}, {100 - pageMargin, pageMargin}, {100 - pageMargin, 100 - pageMargin}, {pageMargin, 100 - pageMargin}}}
}

// SuggestLayout returns panel polygons for a page with n beats (panels) in a
// format, and the id of the template used ("" for a generated layout). The
// layout follows the pacing: one beat is a splash, 3 an establishing page,
// 5 a fight beat, 6 talking heads, 7 a chase; webtoons get tall strips.
func SuggestLayout(n int, format string) ([]Polygon, string) {
	if n < 1 {
		n = 1
	}
	if format == FormatWebtoon {
		h := make([]float64, n)
		for i := range h {
			h[i] = 1 + 0.3*float64((i*7)%3)
		}
		return stripLayout(h), "builtin:webtoon_scroll"
	}
	if id := suggestByCount[n]; id != "" {
		t, _ := BuiltinLayout(id)
		return t.Pages[0], id
	}
	switch n {
	case 1:
		return splashLayout(), ""
	case 2:
		return rowsLayout([]rowSpec{{H: 1, Cols: []float64{1}, Slant: 5}, {H: 1.4, Cols: []float64{1}}}, pageMargin, pageGutter), ""
	case 4:
		return rowsLayout([]rowSpec{{H: 1.2, Cols: []float64{1}}, {H: 1, Cols: []float64{1, 1.2}, ColSlant: 4}, {H: 1, Cols: []float64{1}}}, pageMargin, pageGutter), ""
	}
	// 8 and more: tiers of two or three.
	var rows []rowSpec
	left := n
	for left > 0 {
		c := 3
		if left == 4 || left == 2 {
			c = 2
		}
		if left < c {
			c = left
		}
		cols := make([]float64, c)
		for i := range cols {
			cols[i] = 1
		}
		rows = append(rows, rowSpec{H: 1, Cols: cols})
		left -= c
	}
	return rowsLayout(rows, pageMargin, pageGutter), ""
}

// BuiltinLayout returns a built-in layout template.
func BuiltinLayout(id string) (LayoutTemplate, bool) {
	for _, t := range builtinLayouts {
		if t.ID == id {
			t.Builtin = true
			return t, true
		}
	}
	return LayoutTemplate{}, false
}

// BuiltinLayouts returns the built-in layout templates, translated.
func BuiltinLayouts(ctx context.Context) []LayoutTemplate {
	out := make([]LayoutTemplate, 0, len(builtinLayouts))
	for _, t := range builtinLayouts {
		t.Builtin = true
		t.Name, t.Description = i18n.T(ctx, t.NameKey), i18n.T(ctx, t.DescKey)
		out = append(out, t)
	}
	return out
}

// builtinStories are the shipped story structures.
var builtinStories = []StoryTemplate{
	{ID: "builtin:three_act", NameKey: "studio.tpl.story.three_act", DescKey: "studio.tpl.story.three_act.desc", Badge: "3 ARCS",
		Arcs: []StoryArcSpec{
			{TitleKey: "studio.tpl.arc.setup", SummaryKey: "studio.tpl.arc.setup.desc", Chapters: 3},
			{TitleKey: "studio.tpl.arc.confrontation", SummaryKey: "studio.tpl.arc.confrontation.desc", Chapters: 4},
			{TitleKey: "studio.tpl.arc.resolution", SummaryKey: "studio.tpl.arc.resolution.desc", Chapters: 3},
		}},
	{ID: "builtin:twelve_chapter", NameKey: "studio.tpl.story.twelve", DescKey: "studio.tpl.story.twelve.desc", Badge: "12 CHAPTERS",
		Arcs: []StoryArcSpec{
			{TitleKey: "studio.tpl.arc.hook", SummaryKey: "studio.tpl.arc.hook.desc", Chapters: 3},
			{TitleKey: "studio.tpl.arc.rising", SummaryKey: "studio.tpl.arc.rising.desc", Chapters: 3},
			{TitleKey: "studio.tpl.arc.turn", SummaryKey: "studio.tpl.arc.turn.desc", Chapters: 3},
			{TitleKey: "studio.tpl.arc.finale", SummaryKey: "studio.tpl.arc.finale.desc", Chapters: 3},
		}},
	{ID: "builtin:gag_strip", NameKey: "studio.tpl.story.gag", DescKey: "studio.tpl.story.gag.desc", Badge: "4-KOMA", LayoutID: "builtin:4koma",
		Arcs: []StoryArcSpec{{TitleKey: "studio.tpl.arc.gags", SummaryKey: "studio.tpl.arc.gags.desc", Chapters: 1,
			BeatKeys: []string{"studio.tpl.beat.ki", "studio.tpl.beat.sho", "studio.tpl.beat.ten", "studio.tpl.beat.ketsu"}}}},
	{ID: "builtin:confession", NameKey: "studio.tpl.story.confession", DescKey: "studio.tpl.story.confession.desc", Badge: "1 SCENE", LayoutID: "builtin:confession",
		Arcs: []StoryArcSpec{{TitleKey: "studio.tpl.arc.confession", SummaryKey: "studio.tpl.arc.confession.desc", Chapters: 1,
			BeatKeys: []string{"studio.tpl.beat.nerve", "studio.tpl.beat.almost", "studio.tpl.beat.words", "studio.tpl.beat.answer"}}}},
}

// BuiltinStory returns a built-in story template, translated.
func BuiltinStory(ctx context.Context, id string) (StoryTemplate, bool) {
	for _, t := range builtinStories {
		if t.ID == id {
			return translateStory(ctx, t), true
		}
	}
	return StoryTemplate{}, false
}

func translateStory(ctx context.Context, t StoryTemplate) StoryTemplate {
	t.Builtin = true
	t.Name, t.Description = i18n.T(ctx, t.NameKey), i18n.T(ctx, t.DescKey)
	arcs := make([]StoryArcSpec, len(t.Arcs))
	for i, a := range t.Arcs {
		a.Title, a.Summary = i18n.T(ctx, a.TitleKey), i18n.T(ctx, a.SummaryKey)
		a.Beats = nil
		for _, k := range a.BeatKeys {
			a.Beats = append(a.Beats, i18n.T(ctx, k))
		}
		arcs[i] = a
	}
	t.Arcs = arcs
	return t
}

// BuiltinStories returns the built-in story templates, translated.
func BuiltinStories(ctx context.Context) []StoryTemplate {
	out := make([]StoryTemplate, 0, len(builtinStories))
	for _, t := range builtinStories {
		out = append(out, translateStory(ctx, t))
	}
	return out
}

// ---- user and pack templates ---------------------------------------------------------------

type templateRow struct {
	ID          string            `db:"id"`
	OwnerID     string            `db:"owner_id"`
	Kind        string            `db:"kind"`
	Name        string            `db:"name"`
	Description string            `db:"description"`
	Tags        db.JSON[[]string] `db:"tags"`
	Data        string            `db:"data"`
	Shared      bool              `db:"shared"`
}

// UserLayouts lists layout templates stored in the database that userID may use (own + shared).
func (s *Service) UserLayouts(ctx context.Context, userID string) ([]LayoutTemplate, error) {
	rows, err := s.templateRows(ctx, userID, TemplateLayout)
	if err != nil {
		return nil, err
	}
	var out []LayoutTemplate
	for _, r := range rows {
		var d struct {
			Pages    [][]Polygon `json:"pages"`
			Badge    string      `json:"badge"`
			Vertical bool        `json:"vertical"`
		}
		if json.Unmarshal([]byte(r.Data), &d) != nil || len(d.Pages) == 0 {
			continue
		}
		if d.Badge == "" {
			d.Badge = strconv.Itoa(len(d.Pages[0])) + " PANELS"
		}
		out = append(out, LayoutTemplate{ID: r.ID, Name: r.Name, Description: r.Description, Badge: d.Badge, Pages: d.Pages, Vertical: d.Vertical})
	}
	return out, nil
}

// UserStories lists story templates stored in the database that userID may use.
func (s *Service) UserStories(ctx context.Context, userID string) ([]StoryTemplate, error) {
	rows, err := s.templateRows(ctx, userID, TemplateStory)
	if err != nil {
		return nil, err
	}
	var out []StoryTemplate
	for _, r := range rows {
		var d StoryTemplate
		if json.Unmarshal([]byte(r.Data), &d) != nil || len(d.Arcs) == 0 {
			continue
		}
		d.ID, d.Name, d.Description, d.Builtin = r.ID, r.Name, r.Description, false
		if d.Badge == "" {
			d.Badge = strconv.Itoa(d.Chapters()) + " CHAPTERS"
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Service) templateRows(ctx context.Context, userID, kind string) ([]templateRow, error) {
	var rows []templateRow
	err := s.db.Select(ctx, &rows, `SELECT id, COALESCE(owner_id, '') AS owner_id, kind, name, description, tags, data, shared
		FROM templates WHERE kind = ? AND (owner_id = ? OR shared = ? OR builtin = ?) ORDER BY name`, kind, userID, true, true)
	return rows, err
}

// Layout returns a layout template by id (built-in or stored).
func (s *Service) Layout(ctx context.Context, userID, id string) (*LayoutTemplate, error) {
	if strings.HasPrefix(id, "grid:") {
		// grid:RxC — the custom grid with any size.
		r, c, _ := strings.Cut(strings.TrimPrefix(id, "grid:"), "x")
		rows, _ := strconv.Atoi(r)
		cols, _ := strconv.Atoi(c)
		if rows > 0 && cols > 0 {
			t := LayoutTemplate{ID: id, Name: i18n.T(ctx, "studio.tpl.layout.grid"), Badge: "GRID", Pages: [][]Polygon{GridLayout(rows, cols)}}
			return &t, nil
		}
	}
	if t, ok := BuiltinLayout(id); ok {
		t.Name, t.Description = i18n.T(ctx, t.NameKey), i18n.T(ctx, t.DescKey)
		return &t, nil
	}
	list, err := s.UserLayouts(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, t := range list {
		if t.ID == id {
			return &t, nil
		}
	}
	return nil, core.ErrNotFound()
}

// Story returns a story template by id (built-in or stored).
func (s *Service) Story(ctx context.Context, userID, id string) (*StoryTemplate, error) {
	if t, ok := BuiltinStory(ctx, id); ok {
		return &t, nil
	}
	list, err := s.UserStories(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, t := range list {
		if t.ID == id {
			return &t, nil
		}
	}
	return nil, core.ErrNotFound()
}

// SaveLayoutAsTemplate stores the panel geometry of a page as a personal layout template.
func (s *Service) SaveLayoutAsTemplate(ctx context.Context, a *Access, pageID, name, desc string) (*LayoutTemplate, error) {
	doc, err := s.LoadPageDoc(ctx, pageID)
	if err != nil || doc.Page.ProjectID != a.Project.ID {
		return nil, core.ErrNotFound()
	}
	if name = clip(name, 80); name == "" {
		return nil, core.ErrValidation(map[string]string{"name": i18n.T(ctx, "studio.form.required")})
	}
	var polys []Polygon
	for _, p := range doc.Panels {
		polys = append(polys, p.Points.V)
	}
	if len(polys) == 0 {
		return nil, core.ErrValidation(map[string]string{"page": i18n.T(ctx, "studio.tpl.empty_page")})
	}
	t := &LayoutTemplate{ID: db.NewID(), Name: name, Description: clip(desc, 200), Badge: strconv.Itoa(len(polys)) + " PANELS", Pages: [][]Polygon{polys}}
	now := s.now()
	_, err = s.db.Exec(ctx, `INSERT INTO templates (id, owner_id, kind, name, description, data, created_at, updated_at) VALUES (?, ?, 'layout', ?, ?, ?, ?, ?)`,
		t.ID, a.Actor.UserID, t.Name, t.Description, db.MustJSON(map[string]any{"pages": t.Pages, "badge": t.Badge}), now, now)
	return t, err
}

// DeleteTemplate deletes a personal template.
func (s *Service) DeleteTemplate(ctx context.Context, userID, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM templates WHERE id = ? AND owner_id = ? AND builtin = ?`, id, userID, false)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound()
	}
	return nil
}

// MirrorPolygon flips a polygon horizontally (right-to-left ↔ left-to-right).
func MirrorPolygon(p Polygon) Polygon {
	out := make(Polygon, len(p))
	for i, pt := range p {
		out[i] = Point{round1(100 - pt[0]), pt[1]}
	}
	return out
}

// ApplyLayout gives a page the panel geometry of a layout template (page
// index idx of the template). Existing panels keep their layers, prompts and
// bubbles in reading order; missing panels are created with background,
// character and lettering layers; surplus panels are removed and their
// bubbles move to the last panel. It is recorded as a version.
func (s *Service) ApplyLayout(ctx context.Context, actor Actor, pageID, layoutID string, idx int) (*PageDoc, error) {
	doc, err := s.LoadPageDoc(ctx, pageID)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, actor, doc.Page.ProjectID)
	if err != nil {
		return nil, err
	}
	t, err := s.Layout(ctx, actor.UserID, layoutID)
	if err != nil {
		return nil, err
	}
	if idx < 0 || idx >= len(t.Pages) {
		idx = 0
	}
	polys := t.Pages[idx]
	if a.Project.Format == FormatComicLTR && !t.Vertical {
		// Templates are drawn for right-to-left reading; comics read left to right,
		// so mirror the page (the panel order stays the reading order).
		m := make([]Polygon, len(polys))
		for i, p := range polys {
			m[i] = MirrorPolygon(p)
		}
		polys = m
	}
	doc.Page.LayoutID = t.ID
	applyPolygons(doc, polys)
	saved, _, err := s.SavePageDoc(ctx, actor, doc, SaveOptions{Action: "layout", Summary: i18n.T(ctx, "studio.version.layout", "name", t.Name)})
	return saved, err
}

// applyPolygons reshapes doc's panels to polys (see ApplyLayout).
func applyPolygons(doc *PageDoc, polys []Polygon) {
	var out []PanelDoc
	for i, poly := range polys {
		if i < len(doc.Panels) {
			p := doc.Panels[i]
			p.Points.V = poly
			out = append(out, p)
			continue
		}
		out = append(out, newPanelDoc(doc.Page, poly, nil))
	}
	if len(polys) < len(doc.Panels) && len(out) > 0 {
		last := out[len(out)-1].ID
		removed := map[string]bool{}
		for _, p := range doc.Panels[len(polys):] {
			removed[p.ID] = true
		}
		for i := range doc.Bubbles {
			if removed[doc.Bubbles[i].PanelID] {
				doc.Bubbles[i].PanelID = last
			}
		}
	}
	doc.Panels = out
}

// newPanelDoc returns a fresh panel with its default layers: background, one
// per character, and lettering.
func newPanelDoc(page Page, poly Polygon, characterIDs []string) PanelDoc {
	now := time.Now().UTC()
	p := PanelDoc{Panel: Panel{ID: db.NewID(), ProjectID: page.ProjectID, PageID: page.ID, PromptSource: "script", ReviewStatus: "none",
		CreatedAt: now, UpdatedAt: now}}
	p.Points.V = poly
	p.Border.V, p.Pose.V, p.Living.V = map[string]any{}, map[string]any{}, map[string]any{}
	mk := func(kind, name, charID string) Layer {
		l := Layer{ID: db.NewID(), PanelID: p.ID, Kind: kind, Name: name, CharacterID: charID, Visible: true, Opacity: 1, Blend: "normal",
			Provenance: "human", CreatedAt: now, UpdatedAt: now}
		l.Transform.V = map[string]any{}
		return l
	}
	p.Layers = append(p.Layers, mk(LayerBackground, "Background", ""))
	for _, c := range characterIDs {
		p.Layers = append(p.Layers, mk(LayerCharacter, "Character", c))
	}
	p.Layers = append(p.Layers, mk(LayerLettering, "Lettering", ""))
	return p
}
