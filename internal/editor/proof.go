package editor

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// defaultLayerNames are the names the server stores for layers it creates
// (pages from script, generation, lettering repair). They are labels, not
// user input, so the interface shows them in the reader's language.
var defaultLayerNames = map[string][]string{
	pagedoc.LayerBackground: {"background"},
	pagedoc.LayerFX:         {"effects", "fx"},
	pagedoc.LayerLettering:  {"lettering"},
	pagedoc.LayerCharacter:  {"character"},
}

// layerName is the localised display name of a layer: the character's name
// for character layers, the interface label for default names, otherwise the
// creator's own name.
func layerName(ctx context.Context, l pagedoc.Layer, characters []studio.Character) string {
	if l.Kind == pagedoc.LayerCharacter && l.CharacterID != "" {
		for _, c := range characters {
			if c.ID == l.CharacterID && c.Name != "" {
				return c.Name
			}
		}
	}
	name := strings.TrimSpace(l.Name)
	if name != "" {
		lower := strings.ToLower(name)
		custom := true
		for _, d := range defaultLayerNames[l.Kind] {
			if lower == d {
				custom = false
			}
		}
		if custom {
			return name
		}
	}
	return i18n.T(ctx, "editor.layer."+l.Kind)
}

func layerIcon(kind string) string {
	switch kind {
	case pagedoc.LayerLettering:
		return "message-circle"
	case pagedoc.LayerCharacter:
		return "user"
	case pagedoc.LayerFX:
		return "bolt"
	}
	return "photo"
}

func provenanceLabel(ctx context.Context, l pagedoc.Layer, bubbles int) string {
	if l.Kind == pagedoc.LayerLettering {
		return i18n.T(ctx, "editor.bubble.count", "count", bubbles)
	}
	switch l.Provenance {
	case pagedoc.ProvenanceAI:
		return i18n.T(ctx, "editor.ai")
	case pagedoc.ProvenanceMixed:
		return i18n.T(ctx, "editor.mixed")
	}
	return i18n.T(ctx, "editor.human")
}

// orderedLayers lists a panel's layers top first, as the layers list shows them.
func orderedLayers(pn pagedoc.Panel) []pagedoc.Layer {
	out := make([]pagedoc.Layer, len(pn.Layers))
	for i, l := range pn.Layers {
		out[len(pn.Layers)-1-i] = l
	}
	return out
}

func panelBubbles(p pagedoc.Page, panelID string) int {
	n := 0
	for _, b := range p.Bubbles {
		if b.PanelID == panelID {
			n++
		}
	}
	return n
}

// proofImage is one visible art layer placed in its panel for the static proof.
type proofImage struct {
	URL       string
	Width     int
	Height    int
	Transform string
	Opacity   float64
}

// proofImages places a panel's visible art layers with the editor's cover
// rule (the same maths as the island's layerPlacement), bottom layer first.
func proofImages(pn pagedoc.Panel) []proofImage {
	b := pagedoc.Bounds(pn.Points)
	out := []proofImage{}
	for _, l := range pn.Layers {
		if l.Kind == pagedoc.LayerLettering || !l.Visible || l.Asset == nil || l.Asset.URL == "" || l.Asset.Width <= 0 || l.Asset.Height <= 0 {
			continue
		}
		w, h := float64(l.Asset.Width), float64(l.Asset.Height)
		scale := math.Max(b.W/w, b.H/h)
		if l.Transform.Scale > 0 {
			scale *= l.Transform.Scale
		}
		sx := scale
		if l.Transform.FlipX {
			sx = -scale
		}
		cx, cy := b.X+b.W/2+l.Transform.OffsetX, b.Y+b.H/2+l.Transform.OffsetY
		out = append(out, proofImage{URL: l.Asset.URL, Width: l.Asset.Width, Height: l.Asset.Height, Opacity: l.Opacity,
			Transform: fmt.Sprintf("translate(%g %g) rotate(%g) scale(%g %g) translate(%g %g)", cx, cy, l.Transform.Rotate, sx, scale, -w/2, -h/2)})
	}
	return out
}

// thumbPolygons returns the panel outlines a page thumbnail draws: the open
// page's own panels, otherwise its layout's.
func thumbPolygons(data bootstrapData, s pagestore.PageSummary) [][]pagedoc.Point {
	if data.Current != nil && data.Current.Page.ID == s.ID {
		out := make([][]pagedoc.Point, 0, len(data.Current.Page.Panels))
		for _, pn := range data.Current.Page.Panels {
			out = append(out, pn.Points)
		}
		return out
	}
	for _, l := range data.Layouts {
		if l.ID == s.LayoutID {
			return l.Points
		}
	}
	return nil
}

func thumbView(data bootstrapData) string {
	return fmt.Sprintf("0 0 %d %d", data.Project.PageWidth, data.Project.PageHeight)
}

func chapterLabel(ctx context.Context, c studio.Chapter) string {
	return i18n.T(ctx, "editor.chapter", "number", c.Number, "title", c.Title)
}

func pageOf(ctx context.Context, data bootstrapData) string {
	n := 0
	if data.Current != nil {
		n = data.Current.Page.Number
	}
	return i18n.T(ctx, "editor.page.of", "number", n, "count", len(data.Pages))
}

func pageTitle(ctx context.Context, p pagedoc.Page) string {
	return i18n.T(ctx, "editor.page.number", "number", p.Number)
}

func panelTitle(ctx context.Context, p pagedoc.Page, i int) string {
	return i18n.T(ctx, "editor.panel.label", "page", p.Number, "panel", i+1)
}

func clipID(pn pagedoc.Panel) string { return "proof-clip-" + pn.ID }

// letteringOpacity mirrors the island: a bubble follows its panel's lettering
// layer visibility and opacity (fully visible when the panel has none).
func letteringOpacity(p pagedoc.Page, bubbleID string) float64 {
	b := p.Bubble(bubbleID)
	if b == nil {
		return 1
	}
	pn := p.Panel(b.PanelID)
	if pn == nil {
		return 1
	}
	for _, l := range pn.Layers {
		if l.Kind == pagedoc.LayerLettering {
			if !l.Visible {
				return 0
			}
			return l.Opacity
		}
	}
	return 1
}
