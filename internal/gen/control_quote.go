package gen

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

type ControlQuote struct {
	ProviderID  string    `json:"provider_id"`
	ModelID     string    `json:"model_id"`
	Placeholder bool      `json:"placeholder"`
	Estimate    jobs.Cost `json:"estimate"`
	Layers      int       `json:"layers"`
}

func (s *service) QuotePage(ctx context.Context, actor studio.Actor, req PageRequest, sketch bool) (ControlQuote, error) {
	var out ControlQuote
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.PageID) {
		return out, studio.ErrInvalid
	}
	if _, err := s.resolveImageActor(ctx, actor, jobs.Scope{Kind: jobs.ScopePage, ProjectID: req.ProjectID, PageID: req.PageID}, jobs.Cost{}, false); err != nil {
		return out, err
	}
	page, err := s.pages.GetPage(ctx, req.PageID)
	if err != nil {
		return out, err
	}
	if page.ProjectID != req.ProjectID {
		return out, studio.ErrForbidden
	}
	if req.ExpectedVersion != page.Version {
		return out, studio.ErrConflict
	}
	if err := s.studio.CheckScopeWritable(ctx, jobs.Scope{Kind: jobs.ScopePage, ProjectID: req.ProjectID, PageID: req.PageID}); err != nil {
		return out, err
	}
	pages, err := s.pages.ListPages(ctx, page.ChapterID)
	if err != nil {
		return out, err
	}
	neighbours := 0
	for i, p := range pages {
		if p.ID == page.ID {
			if i > 0 {
				neighbours++
			}
			if i+1 < len(pages) {
				neighbours++
			}
			break
		}
	}
	for _, pn := range page.Panels {
		p, err := s.preparePanelControls(ctx, actor, PanelRequest{ProjectID: req.ProjectID, PageID: req.PageID, PanelID: pn.ID, ExpectedVersion: page.Version, Variations: 1, RoutingMode: req.RoutingMode}, page, "", sketch)
		if err != nil {
			return out, err
		}
		needs := p.Plan.Needs
		if sketch {
			p.Pose = nil
		}
		needs.References = imageRefCount(p) + neighbours
		needs.Edit = sketch
		if sketch {
			needs.Pose = false
		}
		if err := s.addQuote(ctx, actor, &out, p, needs); err != nil {
			return out, err
		}
	}
	if out.Layers == 0 {
		return out, studio.ErrInvalid
	}
	return out, nil
}
func (s *service) QuoteRepair(ctx context.Context, actor studio.Actor, req RepairRequest) (ControlQuote, error) {
	var out ControlQuote
	if _, err := s.resolveImageActor(ctx, actor, projectTarget(req.ProjectID), jobs.Cost{}, false); err != nil {
		return out, err
	}
	if len(req.Targets) == 0 || len(req.Targets) > 24 {
		return out, studio.ErrInvalid
	}
	for _, target := range req.Targets {
		page, err := s.pages.GetPage(ctx, target.PageID)
		if err != nil {
			return out, err
		}
		if page.ProjectID != req.ProjectID || page.ChapterID != req.ChapterID {
			return out, studio.ErrNotFound
		}
		pn := page.Panel(target.PanelID)
		if pn == nil {
			return out, studio.ErrNotFound
		}
		l := pn.Layer(target.LayerID)
		if l == nil || l.CharacterID != req.CharacterID {
			return out, studio.ErrInvalid
		}
		p, err := s.preparePanel(ctx, actor, PanelRequest{ProjectID: req.ProjectID, PageID: page.ID, PanelID: pn.ID, LayerID: target.LayerID, ExpectedVersion: target.Version, Variations: 1, PreserveComposition: true}, page, "")
		if err != nil {
			return out, err
		}
		if err := s.studio.CheckScopeWritable(ctx, p.Scope); err != nil {
			return out, err
		}
		if err := s.addQuote(ctx, actor, &out, p, p.Plan.Needs); err != nil {
			return out, err
		}
	}
	return out, nil
}
func (s *service) addQuote(ctx context.Context, actor studio.Actor, out *ControlQuote, p imagePayload, needs ai.ImageNeeds) error {
	req := ai.ImagePlanRequest{Task: p.Plan.Task, ActorID: actor.UserID, ProjectID: p.Scope.ProjectID, Needs: needs, RoutingMode: p.Plan.RoutingMode, PanelType: p.PanelType}
	for _, c := range p.Components {
		if c.CharacterID != "" {
			req.CharacterIDs = append(req.CharacterIDs, c.CharacterID)
		}
	}
	if out.ModelID != "" {
		req.PinModelID = out.ModelID
		req.PinProviderID = out.ProviderID
	}
	plan, err := s.images.PlanImage(ctx, req)
	if err != nil {
		return sanitizePlanningError(ctx, err)
	}
	cost, err := scaleCost(plan.Estimate, len(p.Components)*imageAttempts)
	if err != nil {
		return err
	}
	out.Estimate = out.Estimate.Add(cost)
	out.ModelID = plan.ModelID
	out.ProviderID = plan.ProviderID
	out.Placeholder = plan.ProviderID == ai.PlaceholderID
	out.Layers += len(p.Components)
	return nil
}
