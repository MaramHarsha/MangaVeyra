package editor

import (
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// thumbWidth is the rendered filmstrip thumbnail width in pixels (2× the
// 88 px design thumbnail for sharp display).
const thumbWidth = 176

// thumb renders a saved page's thumbnail with its art and lettering, the
// same composition the export screen previews. The client adds the page
// version to the URL, so a cached image is never stale.
func (m *Module) thumb(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	renderer, ok := core.Use[compose.Renderer](m.d)
	if !ok || renderer == nil {
		return core.ErrNotFound()
	}
	png, err := renderer.PNG(r.Context(), res.Page, compose.Options{Lang: res.Project.Language, Scale: math.Min(1, thumbWidth/float64(res.Page.Width))})
	if err != nil {
		return core.ErrNotFound()
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, err = w.Write(png)
	return err
}

// routeView is the model the router would pick for a generation right now,
// for the routing chip before anything is queued.
type routeView struct {
	Available       bool    `json:"available"`
	ModelID         string  `json:"model_id,omitempty"`
	ModelName       string  `json:"model_name,omitempty"`
	ProviderID      string  `json:"provider_id,omitempty"`
	Remote          bool    `json:"remote"`
	Placeholder     bool    `json:"placeholder"`
	EstimateSeconds float64 `json:"estimate_seconds,omitempty"`
	RemoteCost      bool    `json:"remote_cost"`
	Message         string  `json:"message,omitempty"`
}

// route previews the routing decision for regenerating a panel, one layer or
// a region, using the same needs the enqueue path plans with. Planning is
// read-only: nothing is reserved or queued.
func (m *Module) route(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, true)
	if err != nil {
		return mapped(r, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := m.authorize(r, res, studio.ActGenerate); err != nil {
		core.JSON(w, 200, routeView{})
		return nil
	}
	images, ok := core.Use[ai.ImageService](m.d)
	q := r.URL.Query()
	pn := res.Page.Panel(q.Get("panel_id"))
	if !ok || images == nil || pn == nil {
		core.JSON(w, 200, routeView{})
		return nil
	}
	b := pagedoc.Bounds(pn.Points)
	scale := math.Min(1, 1024/math.Max(b.W, b.H))
	width, height := max(64, int(math.Round(b.W*scale))), max(64, int(math.Round(b.H*scale)))
	task, needs := ai.TaskPanel, ai.ImageNeeds{Count: 4, BW: strings.Contains(res.Project.Style, "bw")}
	if q.Get("kind") == "region" {
		l := pn.Layer(q.Get("layer_id"))
		if l == nil || l.Asset == nil || l.Asset.Width < 64 || l.Asset.Height < 64 {
			core.JSON(w, 200, routeView{})
			return nil
		}
		task, width, height, needs.Edit, needs.Mask = ai.TaskRegionEdit, l.Asset.Width, l.Asset.Height, true, true
	}
	needs.Width, needs.Height = width, height
	characters := []string{}
	for _, l := range pn.Layers {
		if l.CharacterID == "" {
			continue
		}
		characters = append(characters, l.CharacterID)
		if c, err := m.studio.GetCharacter(r.Context(), l.CharacterID); err == nil {
			needs.References = min(32, needs.References+len(c.Refs))
		}
	}
	plan, err := images.PlanImage(r.Context(), ai.ImagePlanRequest{Task: task, ActorID: res.Actor.UserID, ProjectID: res.Project.ID, RoutingMode: "balanced", CharacterIDs: characters, PanelType: pn.Shot, Needs: needs})
	if err != nil {
		out := routeView{}
		var refused *ai.GenerateError
		if errors.As(err, &refused) {
			out.Message = refused.SafeMessage
		}
		core.JSON(w, 200, out)
		return nil
	}
	core.JSON(w, 200, routeView{Available: true, ModelID: plan.ModelID, ModelName: m.modelName(r.Context(), plan.ModelID), ProviderID: plan.ProviderID, Remote: plan.Remote,
		Placeholder: plan.ProviderID == ai.PlaceholderID, EstimateSeconds: plan.Estimate.GPUSeconds, RemoteCost: plan.Estimate.USD > 0})
	return nil
}
