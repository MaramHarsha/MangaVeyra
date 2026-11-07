// Package pagedoc defines the page document: the single structure that the
// editor island, the page store, generation, lettering, compositing and export
// all exchange. It is a leaf package (no imports from other internal packages)
// so every module can depend on it.
//
// Coordinates are page pixels with the origin at the top-left corner. A page is
// Width × Height pixels (default 1600 × 2400 for B5 at ~240 dpi). Panels are
// polygons; each panel holds independent layers (background, one per
// character, effects, lettering). Bubbles are structured text that belongs to
// the page (optionally anchored to a panel) and are never baked into images.
package pagedoc

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// Schema is the document schema identifier.
const Schema = "mangaveyra.page.v1"

// Layer kinds.
const (
	LayerBackground = "background"
	LayerCharacter  = "character"
	LayerFX         = "fx"
	LayerLettering  = "lettering"
)

// Bubble kinds.
const (
	BubbleSpeech  = "speech"
	BubbleThought = "thought"
	BubbleShout   = "shout"
	BubbleWhisper = "whisper"
	BubbleCaption = "caption"
	BubbleSFX     = "sfx"
)

// Bubble shapes.
const (
	ShapeEllipse = "ellipse"
	ShapeCloud   = "cloud"
	ShapeBurst   = "burst"
	ShapeRect    = "rect"
	ShapeNone    = "none"
)

// Provenance values for layers and bubble texts.
const (
	ProvenanceHuman = "human"
	ProvenanceAI    = "ai"
	ProvenanceMixed = "mixed"
)

// Reading directions (from the project format).
const (
	DirRTL      = "rtl"      // manga: right to left
	DirLTR      = "ltr"      // comic: left to right
	DirVertical = "vertical" // webtoon strip: top to bottom
)

// Limits enforced by Validate.
const (
	MaxPanels        = 24
	MaxLayersPerPane = 16
	MaxBubbles       = 64
	MaxPolygonPoints = 16
	MaxTextRunes     = 2000
	MaxPromptRunes   = 4000
	MaxPageSide      = 20000
)

// Point is a page coordinate in pixels.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Rect is an axis-aligned box in page pixels.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Page is one page of a chapter.
type Page struct {
	Schema    string `json:"schema"`
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	ChapterID string `json:"chapter_id"`
	Number    int    `json:"number"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	// Direction is the reading direction (rtl, ltr, vertical) taken from the project format.
	Direction string `json:"direction"`
	LayoutID  string `json:"layout_id,omitempty"`
	Locked    bool   `json:"locked"`
	LockedBy  string `json:"locked_by,omitempty"`
	// Version increases on every saved change; patches name the version they apply to.
	Version int       `json:"version"`
	Panels  []Panel   `json:"panels"`
	Bubbles []Bubble  `json:"bubbles"`
	Note    string    `json:"note,omitempty"`
	Thumb   *AssetRef `json:"thumb,omitempty"`
}

// AssetRef points at a stored asset. URL is a signed, expiring media URL filled
// in when the document is sent to a client; it is never persisted.
type AssetRef struct {
	ID     string `json:"id"`
	URL    string `json:"url,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Panel is a polygon on the page with its own layers.
type Panel struct {
	ID    string `json:"id"`
	Order int    `json:"order"`
	// Points is the polygon outline, clockwise, at least 3 points.
	Points        []Point `json:"points"`
	ScriptPanelID string  `json:"script_panel_id,omitempty"`
	Prompt        string  `json:"prompt"`
	// PromptSource is "script", "user" or "agent".
	PromptSource string `json:"prompt_source"`
	Camera       string `json:"camera,omitempty"`
	Shot         string `json:"shot,omitempty"`
	Style        string `json:"style,omitempty"`
	// Pose is an optional OpenPose-style JSON object (kept opaque here).
	Pose     map[string]any `json:"pose,omitempty"`
	Locked   bool           `json:"locked"`
	LockedBy string         `json:"locked_by,omitempty"`
	AltText  string         `json:"alt_text,omitempty"`
	Layers   []Layer        `json:"layers"`
}

// Transform places a layer image inside the panel's bounding box. With the zero
// value the image covers the box (object-fit: cover, centred).
type Transform struct {
	OffsetX float64 `json:"offset_x,omitempty"`
	OffsetY float64 `json:"offset_y,omitempty"`
	// Scale multiplies the cover size (0 means 1).
	Scale  float64 `json:"scale,omitempty"`
	Rotate float64 `json:"rotate,omitempty"`
	FlipX  bool    `json:"flip_x,omitempty"`
}

// Layer is one independently regenerable image inside a panel.
type Layer struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Name        string    `json:"name,omitempty"`
	CharacterID string    `json:"character_id,omitempty"`
	Asset       *AssetRef `json:"asset,omitempty"`
	Mask        *AssetRef `json:"mask,omitempty"`
	Order       int       `json:"order"`
	Visible     bool      `json:"visible"`
	Locked      bool      `json:"locked"`
	Opacity     float64   `json:"opacity"`
	Blend       string    `json:"blend,omitempty"`
	Transform   Transform `json:"transform"`
	Provenance  string    `json:"provenance"`
	ModelID     string    `json:"model_id,omitempty"`
	ProviderID  string    `json:"provider_id,omitempty"`
	JobID       string    `json:"job_id,omitempty"`
	Seed        *int64    `json:"seed,omitempty"`
	Prompt      string    `json:"prompt,omitempty"`
	LockScore   *float64  `json:"lock_score,omitempty"`
}

// Tail is the pointer from a bubble to its speaker.
type Tail struct {
	Enabled bool `json:"enabled"`
	// Tip is the point the tail aims at (page pixels).
	Tip Point `json:"tip"`
}

// BubbleStyle controls lettering.
type BubbleStyle struct {
	// Font is a lettering font key ("comic", "sfx", "jp"); empty picks by kind and language.
	Font string `json:"font,omitempty"`
	// Size is the font size in page pixels; 0 means auto.
	Size float64 `json:"size,omitempty"`
	// AutoFit shrinks text (down to a minimum) so it fits the bubble.
	AutoFit bool `json:"auto_fit"`
	// Align is "center" (default), "left" or "right" for horizontal text.
	Align  string  `json:"align,omitempty"`
	Rotate float64 `json:"rotate,omitempty"`
	// Stroke draws an outline around SFX glyphs.
	Stroke bool `json:"stroke,omitempty"`
}

// Ruby is a furigana annotation over a run of base text.
type Ruby struct {
	// Start and End are rune offsets into Text (End exclusive).
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// BubbleText is the text of a bubble in one language.
type BubbleText struct {
	Text string `json:"text"`
	// Status is draft, machine, reviewed, approved or overflow.
	Status string `json:"status"`
	// Source is human or ai.
	Source string `json:"source"`
	Ruby   []Ruby `json:"ruby,omitempty"`
}

// Bubble is a speech balloon, caption or sound effect.
type Bubble struct {
	ID                 string `json:"id"`
	PanelID            string `json:"panel_id,omitempty"`
	Kind               string `json:"kind"`
	SpeakerCharacterID string `json:"speaker_character_id,omitempty"`
	ScriptPanelID      string `json:"script_panel_id,omitempty"`
	// Order is the reading order on the page (0 first).
	Order int `json:"order"`
	// Box is the bubble's bounding box; the shape is drawn inside it.
	Box      Rect        `json:"box"`
	Shape    string      `json:"shape"`
	Tail     Tail        `json:"tail"`
	Style    BubbleStyle `json:"style"`
	Vertical bool        `json:"vertical"`
	// Texts maps a BCP-47 language tag ("en", "ja") to the text in that language.
	Texts map[string]BubbleText `json:"texts"`
}

// New returns an empty page with defaults applied.
func New(id, projectID, chapterID string, number, width, height int, direction string) Page {
	if width <= 0 {
		width = 1600
	}
	if height <= 0 {
		height = 2400
	}
	if direction == "" {
		direction = DirRTL
	}
	return Page{Schema: Schema, ID: id, ProjectID: projectID, ChapterID: chapterID, Number: number,
		Width: width, Height: height, Direction: direction, Version: 1, Panels: []Panel{}, Bubbles: []Bubble{}}
}

// DirectionForFormat maps a project format to a reading direction.
func DirectionForFormat(format string) string {
	switch format {
	case "comic_ltr":
		return DirLTR
	case "webtoon":
		return DirVertical
	default:
		return DirRTL
	}
}

// Bounds returns the bounding box of a polygon.
func Bounds(pts []Point) Rect {
	if len(pts) == 0 {
		return Rect{}
	}
	minX, minY, maxX, maxY := pts[0].X, pts[0].Y, pts[0].X, pts[0].Y
	for _, p := range pts[1:] {
		minX, maxX = math.Min(minX, p.X), math.Max(maxX, p.X)
		minY, maxY = math.Min(minY, p.Y), math.Max(maxY, p.Y)
	}
	return Rect{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
}

// Contains reports whether p lies inside the polygon (even-odd rule).
func Contains(pts []Point, p Point) bool {
	in := false
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		a, b := pts[i], pts[j]
		if (a.Y > p.Y) != (b.Y > p.Y) && p.X < (b.X-a.X)*(p.Y-a.Y)/(b.Y-a.Y)+a.X {
			in = !in
		}
	}
	return in
}

// RectPoints returns the clockwise polygon for a rectangle.
func RectPoints(r Rect) []Point {
	return []Point{{r.X, r.Y}, {r.X + r.W, r.Y}, {r.X + r.W, r.Y + r.H}, {r.X, r.Y + r.H}}
}

// Panel returns the panel with id, or nil.
func (p *Page) Panel(id string) *Panel {
	for i := range p.Panels {
		if p.Panels[i].ID == id {
			return &p.Panels[i]
		}
	}
	return nil
}

// Bubble returns the bubble with id, or nil.
func (p *Page) Bubble(id string) *Bubble {
	for i := range p.Bubbles {
		if p.Bubbles[i].ID == id {
			return &p.Bubbles[i]
		}
	}
	return nil
}

// Layer returns the layer with id inside the panel, or nil.
func (p *Panel) Layer(id string) *Layer {
	for i := range p.Layers {
		if p.Layers[i].ID == id {
			return &p.Layers[i]
		}
	}
	return nil
}

// LayerOfKind returns the first layer of a kind (and character, for character layers), or nil.
func (p *Panel) LayerOfKind(kind, characterID string) *Layer {
	for i := range p.Layers {
		l := &p.Layers[i]
		if l.Kind == kind && (kind != LayerCharacter || l.CharacterID == characterID) {
			return l
		}
	}
	return nil
}

// Text returns the bubble text for lang, falling back to any language when missing.
func (b *Bubble) Text(lang string) BubbleText {
	if t, ok := b.Texts[lang]; ok {
		return t
	}
	for _, k := range []string{"en", "ja"} {
		if t, ok := b.Texts[k]; ok {
			return t
		}
	}
	for _, t := range b.Texts {
		return t
	}
	return BubbleText{}
}

// Validate checks structural invariants. It does not check IDs against the
// database (the page store does that).
func (p *Page) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if p.Schema != Schema {
		add("schema must be %q", Schema)
	}
	if p.Width <= 0 || p.Height <= 0 || p.Width > MaxPageSide || p.Height > MaxPageSide {
		add("page size %d×%d is out of range", p.Width, p.Height)
	}
	switch p.Direction {
	case DirRTL, DirLTR, DirVertical:
	default:
		add("direction %q is invalid", p.Direction)
	}
	if len(p.Panels) > MaxPanels {
		add("too many panels (%d > %d)", len(p.Panels), MaxPanels)
	}
	if len(p.Bubbles) > MaxBubbles {
		add("too many bubbles (%d > %d)", len(p.Bubbles), MaxBubbles)
	}
	ids := map[string]bool{}
	seen := func(kind, id string) {
		if strings.TrimSpace(id) == "" {
			add("%s has an empty id", kind)
			return
		}
		if ids[id] {
			add("duplicate id %q", id)
		}
		ids[id] = true
	}
	panelIDs := map[string]bool{}
	for i, pn := range p.Panels {
		seen("panel", pn.ID)
		panelIDs[pn.ID] = true
		if len(pn.Points) < 3 || len(pn.Points) > MaxPolygonPoints {
			add("panel %d needs 3–%d points", i, MaxPolygonPoints)
		}
		for _, pt := range pn.Points {
			if !finite(pt.X) || !finite(pt.Y) || pt.X < -1 || pt.Y < -1 || pt.X > float64(p.Width)+1 || pt.Y > float64(p.Height)+1 {
				add("panel %d has a point outside the page", i)
				break
			}
		}
		if utf8.RuneCountInString(pn.Prompt) > MaxPromptRunes {
			add("panel %d prompt is too long", i)
		}
		switch pn.PromptSource {
		case "", "script", "user", "agent":
		default:
			add("panel %d prompt_source %q is invalid", i, pn.PromptSource)
		}
		if len(pn.Layers) > MaxLayersPerPane {
			add("panel %d has too many layers", i)
		}
		for _, l := range pn.Layers {
			seen("layer", l.ID)
			switch l.Kind {
			case LayerBackground, LayerCharacter, LayerFX, LayerLettering:
			default:
				add("layer %q kind %q is invalid", l.ID, l.Kind)
			}
			if l.Kind == LayerCharacter && l.CharacterID == "" {
				add("character layer %q needs character_id", l.ID)
			}
			switch l.Provenance {
			case ProvenanceHuman, ProvenanceAI, ProvenanceMixed:
			default:
				add("layer %q provenance %q is invalid", l.ID, l.Provenance)
			}
			if l.Opacity < 0 || l.Opacity > 1 || !finite(l.Opacity) {
				add("layer %q opacity must be 0–1", l.ID)
			}
		}
	}
	for i, b := range p.Bubbles {
		seen("bubble", b.ID)
		switch b.Kind {
		case BubbleSpeech, BubbleThought, BubbleShout, BubbleWhisper, BubbleCaption, BubbleSFX:
		default:
			add("bubble %d kind %q is invalid", i, b.Kind)
		}
		switch b.Shape {
		case ShapeEllipse, ShapeCloud, ShapeBurst, ShapeRect, ShapeNone:
		default:
			add("bubble %d shape %q is invalid", i, b.Shape)
		}
		if b.PanelID != "" && !panelIDs[b.PanelID] {
			add("bubble %d refers to unknown panel %q", i, b.PanelID)
		}
		if b.Box.W <= 0 || b.Box.H <= 0 || !finite(b.Box.X) || !finite(b.Box.Y) {
			add("bubble %d box is invalid", i)
		}
		if b.Style.Size < 0 || b.Style.Size > 1000 {
			add("bubble %d font size is out of range", i)
		}
		for lang, t := range b.Texts {
			if lang == "" || len(lang) > 35 {
				add("bubble %d has an invalid language tag", i)
			}
			if !utf8.ValidString(t.Text) || utf8.RuneCountInString(t.Text) > MaxTextRunes {
				add("bubble %d text (%s) is invalid or too long", i, lang)
			}
			n := utf8.RuneCountInString(t.Text)
			for _, r := range t.Ruby {
				if r.Start < 0 || r.End <= r.Start || r.End > n || r.Text == "" {
					add("bubble %d has an invalid ruby range", i)
					break
				}
			}
		}
	}
	return errors.Join(errs...)
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
