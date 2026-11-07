package gen

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func countImages(n int) (int, error) {
	if n == 0 {
		n = 1
	}
	if n < 1 || n > 4 {
		return 0, studio.ErrInvalid
	}
	return n, nil
}
func requestJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (s *service) plan(ctx context.Context, p *imagePayload, mode, pin string, width, height, count int) error {
	if mode != "" && !validRoutingMode(mode) {
		return studio.ErrInvalid
	}
	task := p.Plan.Task
	charIDs := []string{}
	if p.CharacterID != "" {
		charIDs = append(charIDs, p.CharacterID)
	}
	for _, c := range p.Components {
		if c.CharacterID != "" {
			charIDs = append(charIDs, c.CharacterID)
		}
	}
	plan, err := s.images.PlanImage(ctx, ai.ImagePlanRequest{Task: task, ActorID: p.Actor.UserID, ProjectID: p.Scope.ProjectID, RoutingMode: mode, PinModelID: pin, CharacterIDs: charIDs, PanelType: p.PanelType, Needs: ai.ImageNeeds{Width: width, Height: height, Count: count, References: len(p.References), BW: strings.Contains(p.Style, "bw"), Edit: p.InitAssetID != "", Mask: p.MaskAssetID != ""}})
	if err != nil {
		return sanitizePlanningError(ctx, err)
	}
	if mode != "" && plan.RoutingMode != mode && plan.RoutingMode != ai.ModeLocalOnly {
		return studio.ErrInvalid
	}
	if validateImagePlan(plan) != nil || plan.Task != task || plan.Needs.Width != width || plan.Needs.Height != height || plan.Needs.Count != count || plan.Needs.References != len(p.References) || plan.Needs.Edit != (p.InitAssetID != "") || plan.Needs.Mask != (p.MaskAssetID != "") || (pin != "" && plan.PinModelID != pin) {
		return studio.ErrInvalid
	}
	p.Plan = plan
	return nil
}
func (s *service) EnqueueCharacterSheet(ctx context.Context, actor studio.Actor, req CharacterSheetRequest) (*jobs.Job, error) {
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.CharacterID) || !utf8.ValidString(req.Style) || len(req.Style) > 256 {
		return nil, studio.ErrInvalid
	}
	n, err := countImages(req.Count)
	if err != nil {
		return nil, err
	}
	req.Count = n
	if _, err = s.resolveImageActor(ctx, actor, req.ProjectID, jobs.Cost{}, false); err != nil {
		return nil, err
	}
	c, err := s.studio.GetCharacter(ctx, req.CharacterID)
	if err != nil {
		return nil, err
	}
	if c.ProjectID != req.ProjectID {
		return nil, studio.ErrForbidden
	}
	if !req.ExpectedUpdatedAt.IsZero() && !req.ExpectedUpdatedAt.UTC().Equal(c.UpdatedAt.UTC()) {
		return nil, studio.ErrConflict
	}
	project, err := s.studio.GetProject(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	if req.Style == "" {
		req.Style = project.Style
	}
	req.ExpectedUpdatedAt = c.UpdatedAt
	sheet := component{Name: c.Name + " · sheet", Prompt: sheetPrompt(req.Style, c), OwnRefs: true, Background: "opaque", Negative: sheetNegative(req.Style)}
	p := imagePayload{Version: imagePayloadVersion, Request: requestJSON(req), Actor: storedActor{actor.UserID, actor.TokenID}, Scope: jobs.Scope{Kind: jobs.ScopeProject, ProjectID: req.ProjectID}, CharacterID: c.ID, ExpectedUpdatedAt: c.UpdatedAt, Style: req.Style, Plan: ai.ImagePlan{Task: ai.TaskCharacterSheet}}
	for _, ref := range orderedRefs(c.Refs) {
		if len(p.References) == 4 {
			break
		}
		p.References = append(p.References, ref.AssetID)
	}
	sheet.References = append([]string(nil), p.References...)
	p.Components = []component{sheet}
	if err = s.plan(ctx, &p, req.RoutingMode, "", 1024, 1024, n); err != nil {
		return nil, err
	}
	return s.enqueueImage(ctx, p)
}
func (s *service) EnqueuePanel(ctx context.Context, actor studio.Actor, req PanelRequest) (*jobs.Job, error) {
	if _, err := s.resolveImageActor(ctx, actor, req.ProjectID, jobs.Cost{}, false); err != nil {
		return nil, err
	}
	page, err := s.pages.GetPage(ctx, req.PageID)
	if err != nil {
		return nil, err
	}
	p, err := s.preparePanel(ctx, actor, req, page, "")
	if err != nil {
		return nil, err
	}
	return s.enqueueImage(ctx, p)
}
func (s *service) preparePanel(ctx context.Context, actor studio.Actor, req PanelRequest, page pagedoc.Page, batchID string) (imagePayload, error) {
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.PageID) || !db.ValidID(req.PanelID) || !utf8.ValidString(req.Prompt) || len([]rune(req.Prompt)) > pagedoc.MaxPromptRunes {
		return imagePayload{}, studio.ErrInvalid
	}
	n, err := countImages(req.Variations)
	if err != nil {
		return imagePayload{}, err
	}
	req.Variations = n
	if page.ProjectID != req.ProjectID || page.ID != req.PageID {
		return imagePayload{}, studio.ErrForbidden
	}
	if req.ExpectedVersion != 0 && req.ExpectedVersion != page.Version {
		return imagePayload{}, studio.ErrConflict
	}
	req.ExpectedVersion = page.Version
	pn := page.Panel(req.PanelID)
	if pn == nil {
		return imagePayload{}, studio.ErrNotFound
	}
	project, err := s.studio.GetProject(ctx, req.ProjectID)
	if err != nil {
		return imagePayload{}, err
	}
	p := imagePayload{PanelType: pn.Shot, Version: imagePayloadVersion, Request: requestJSON(req), Actor: storedActor{actor.UserID, actor.TokenID}, Scope: jobs.Scope{Kind: jobs.ScopePanel, ProjectID: req.ProjectID, PageID: page.ID, PanelID: pn.ID}, PageID: page.ID, PanelID: pn.ID, ExpectedVersion: page.Version, TargetHash: hashPanel(*pn), Style: project.Style, BatchID: batchID, Plan: ai.ImagePlan{Task: ai.TaskPanel}}
	style := project.Style
	if strings.TrimSpace(pn.Style) != "" {
		style = pn.Style
	}
	var doc studio.ScriptDoc
	haveDoc := false
	if pn.ScriptPanelID != "" && page.ChapterID != "" {
		got, err := s.studio.GetScript(ctx, page.ChapterID)
		switch {
		case err == nil:
			doc, haveDoc = got, true
		case !errors.Is(err, studio.ErrNotFound):
			return p, err
		}
	}
	scene := panelScene(*pn, req.Prompt, doc, haveDoc)
	add := func(l pagedoc.Layer) {
		p.Components = append(p.Components, component{LayerID: l.ID, Kind: l.Kind, CharacterID: l.CharacterID, Name: l.Name, OwnRefs: true, Background: layerBackground(l.Kind), Negative: layerNegative(style, l.Kind)})
	}
	if req.LayerID != "" {
		l := pn.Layer(req.LayerID)
		if l == nil {
			return p, studio.ErrNotFound
		}
		if l.Kind == pagedoc.LayerLettering {
			return p, studio.ErrForbidden
		}
		add(*l)
		p.Scope = jobs.Scope{Kind: jobs.ScopeLayer, ProjectID: req.ProjectID, PanelID: pn.ID, LayerID: l.ID}
	} else {
		for _, l := range pn.Layers {
			if l.Kind != pagedoc.LayerLettering {
				add(l)
			}
		}
		hasBG, hasFX := false, false
		for _, l := range p.Components {
			hasBG = hasBG || l.Kind == pagedoc.LayerBackground
			hasFX = hasFX || l.Kind == pagedoc.LayerFX
		}
		if !hasBG {
			add(pagedoc.Layer{Kind: pagedoc.LayerBackground, Name: "Background"})
		}
		if !hasFX {
			add(pagedoc.Layer{Kind: pagedoc.LayerFX, Name: "Effects"})
		}
	}
	sort.SliceStable(p.Components, func(i, j int) bool {
		rank := map[string]int{pagedoc.LayerBackground: 0, pagedoc.LayerCharacter: 1, pagedoc.LayerFX: 2}
		return rank[p.Components[i].Kind] < rank[p.Components[j].Kind]
	})
	if len(p.Components) > maxComponents {
		return p, studio.ErrInvalid
	}
	// Each character layer carries only its own character's references
	// (sheets first, at most four); backgrounds and effects carry none.
	seen := map[string]bool{}
	for idx, c := range p.Components {
		focus := castMember{}
		if c.CharacterID != "" {
			character, err := s.studio.GetCharacter(ctx, c.CharacterID)
			if err != nil || character.ProjectID != req.ProjectID {
				return p, studio.ErrForbidden
			}
			focus = castMember{Name: character.Name, Look: character.Look, Outfit: defaultOutfit(character)}
			for _, r := range orderedRefs(character.Refs) {
				if !seen[r.AssetID] {
					if len(p.References) >= 32 {
						continue
					}
					seen[r.AssetID] = true
					p.References = append(p.References, r.AssetID)
				}
				if len(p.Components[idx].References) < 4 {
					p.Components[idx].References = append(p.Components[idx].References, r.AssetID)
				}
			}
		}
		if focus.Name == "" && c.Kind == pagedoc.LayerCharacter {
			focus.Name = c.Name
		}
		p.Components[idx].Prompt = layerPrompt(style, scene, c.Kind, focus)
	}
	bounds := pagedoc.Bounds(pn.Points)
	scale := math.Min(1, 1024/math.Max(bounds.W, bounds.H))
	w, h := max(64, int(math.Round(bounds.W*scale))), max(64, int(math.Round(bounds.H*scale)))
	if err = s.plan(ctx, &p, req.RoutingMode, req.PinModelID, w, h, n); err != nil {
		return p, err
	}
	return p, nil
}
func (s *service) EnqueueRegionEdit(ctx context.Context, actor studio.Actor, req RegionEditRequest) (*jobs.Job, error) {
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.LayerID) || !db.ValidID(req.MaskAssetID) || strings.TrimSpace(req.Prompt) == "" || !utf8.ValidString(req.Prompt) || len([]rune(req.Prompt)) > pagedoc.MaxPromptRunes {
		return nil, studio.ErrInvalid
	}
	n, err := countImages(req.Variations)
	if err != nil {
		return nil, err
	}
	req.Variations = n
	if _, err = s.resolveImageActor(ctx, actor, req.ProjectID, jobs.Cost{}, false); err != nil {
		return nil, err
	}
	var row struct {
		PageID    string `db:"page_id"`
		PanelID   string `db:"panel_id"`
		ProjectID string `db:"project_id"`
	}
	if err = s.db.Get(ctx, &row, `SELECT p.id AS page_id,pn.id AS panel_id,p.project_id FROM layers l JOIN panels pn ON pn.id=l.panel_id JOIN pages p ON p.id=pn.page_id WHERE l.id=?`, req.LayerID); err != nil {
		return nil, studio.ErrNotFound
	}
	if row.ProjectID != req.ProjectID {
		return nil, studio.ErrForbidden
	}
	page, err := s.pages.GetPage(ctx, row.PageID)
	if err != nil {
		return nil, err
	}
	if req.ExpectedVersion != 0 && req.ExpectedVersion != page.Version {
		return nil, studio.ErrConflict
	}
	req.ExpectedVersion = page.Version
	pn := page.Panel(row.PanelID)
	l := pn.Layer(req.LayerID)
	if l == nil || l.Kind == pagedoc.LayerLettering || l.Asset == nil {
		return nil, studio.ErrForbidden
	}
	init, asset, err := s.readOwned(ctx, req.ProjectID, l.Asset.ID)
	if err != nil {
		return nil, err
	}
	mask, maskAsset, err := s.readOwned(ctx, req.ProjectID, req.MaskAssetID)
	if err != nil {
		return nil, err
	}
	w, h, err := validateMask(init, mask, maskAsset.ContentType)
	if err != nil || asset.Width != w || asset.Height != h {
		return nil, studio.ErrInvalid
	}
	project, err := s.studio.GetProject(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	// The component keeps the creator's instruction (the layer's stored
	// prompt); the handler wraps it with regionEditPrompt at dispatch.
	edit := component{LayerID: l.ID, Kind: l.Kind, CharacterID: l.CharacterID, Name: l.Name, Prompt: req.Prompt, OwnRefs: true, Background: layerBackground(l.Kind), Negative: layerNegative(project.Style, l.Kind)}
	p := imagePayload{Version: imagePayloadVersion, Request: requestJSON(req), Actor: storedActor{actor.UserID, actor.TokenID}, Scope: jobs.Scope{Kind: jobs.ScopeRegion, ProjectID: req.ProjectID, LayerID: l.ID, Region: requestJSON(map[string]string{"mask_asset_id": req.MaskAssetID})}, PageID: page.ID, PanelID: pn.ID, ExpectedVersion: page.Version, TargetHash: hashPanel(*pn), InitAssetID: l.Asset.ID, MaskAssetID: req.MaskAssetID, Style: project.Style, Plan: ai.ImagePlan{Task: ai.TaskRegionEdit}, Components: []component{edit}}
	if err = s.plan(ctx, &p, req.RoutingMode, "", w, h, n); err != nil {
		return nil, err
	}
	return s.enqueueImage(ctx, p)
}
