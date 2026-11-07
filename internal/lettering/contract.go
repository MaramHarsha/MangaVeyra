package lettering

// Frozen Phase 1 contract (docs/plans/0002-phase-1-first-playable.md).
// The engine is the single source of truth for bubble geometry: the editor
// island renders the BubbleLayout it gets from the server, and compositing
// draws the same layout, so screen and export match.
//
// Use core.MustUse[lettering.Engine](d).

import (
	"github.com/tdewolff/canvas"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// Font keys.
const (
	FontComic = "comic" // Comic Neue Bold — dialogue
	FontSFX   = "sfx"   // Bangers — Latin sound effects
	FontJP    = "jp"    // Noto Sans JP Black — Japanese text and SFX
)

// TextLine is one positioned line (or column, for vertical text).
type TextLine struct {
	Text string `json:"text"`
	// X, Y is the baseline start for horizontal lines, or the top centre of a
	// column for vertical lines (page pixels).
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Vertical bool    `json:"vertical"`
	// Ruby lines are furigana placed beside/above their base text.
	Ruby     bool    `json:"ruby,omitempty"`
	FontSize float64 `json:"font_size,omitempty"`
}

// BubbleLayout is the computed geometry of one bubble in one language.
type BubbleLayout struct {
	BubbleID string `json:"bubble_id"`
	Lang     string `json:"lang"`
	// ShapePath is an SVG path ("d" attribute) of the outline including the
	// tail, in page pixels. Empty for shape "none".
	ShapePath string `json:"shape_path"`
	// TextBox is the inner box text is laid out in.
	TextBox pagedoc.Rect `json:"text_box"`
	Font    string       `json:"font"` // font key
	// FontFamily is the CSS family the client must use ("Comic Neue", "Bangers", "Noto Sans JP").
	FontFamily string     `json:"font_family"`
	FontWeight int        `json:"font_weight"`
	FontSize   float64    `json:"font_size"` // page pixels
	LineHeight float64    `json:"line_height"`
	Lines      []TextLine `json:"lines"`
	// Overflow is true when the text does not fit even at the minimum size.
	Overflow bool `json:"overflow"`
	// Fill and Stroke colours (CSS hex).
	Fill        string  `json:"fill"`
	Stroke      string  `json:"stroke"`
	StrokeWidth float64 `json:"stroke_width"`
	TextColor   string  `json:"text_color"`
	// TextPath is the authoritative shaped glyph outline, including ruby,
	// in page pixels. Editor SVG and export draw the identical path.
	TextPath        string        `json:"text_path"`
	Rotate          float64       `json:"rotate"`
	RotationCenter  pagedoc.Point `json:"rotation_center"`
	TextStroke      string        `json:"text_stroke,omitempty"`
	TextStrokeWidth float64       `json:"text_stroke_width,omitempty"`
}

// Line is one line of dialogue to place automatically.
type Line struct {
	// Kind is speech, thought, caption (script kinds) or sfx.
	Kind          string
	Text          string
	CharacterID   string
	ScriptPanelID string
}

// Engine lays out and draws lettering.
type Engine interface {
	// Layout computes the bubble geometry for lang (auto-fit, line breaks,
	// vertical text for ja when the bubble is vertical, tail path).
	Layout(b pagedoc.Bubble, lang string) (BubbleLayout, error)
	// LayoutPage lays out every bubble of a page.
	LayoutPage(p pagedoc.Page, lang string) ([]BubbleLayout, error)
	// Draw renders a layout onto a canvas context whose coordinate system is
	// page pixels with the origin at the top-left (callers set up the flip).
	Draw(ctx *canvas.Context, l BubbleLayout) error
	// AutoPlace creates bubbles for dialogue lines inside a panel: speech near
	// the top in reading order, captions in a corner, SFX large and angled,
	// avoiding overlap with each other. vertical selects vertical Japanese.
	AutoPlace(p pagedoc.Page, panelID string, lines []Line, lang string, vertical bool) ([]pagedoc.Bubble, error)
	// ReadingOrder returns bubble IDs in reading order for the page direction
	// (rtl: right-to-left then top-to-bottom within panel order).
	ReadingOrder(p pagedoc.Page) []string
}
