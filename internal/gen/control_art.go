package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

type controlArtPayload struct {
	Request  *PageRequest   `json:"request,omitempty"`
	Version  int            `json:"version"`
	Actor    storedActor    `json:"actor"`
	Scope    jobs.Scope     `json:"scope"`
	Panels   []imagePayload `json:"panels"`
	Estimate jobs.Cost      `json:"estimate"`
	Remote   bool           `json:"remote"`
}

func (s *service) EnqueuePage(ctx context.Context, actor studio.Actor, req PageRequest) (*jobs.Job, error) {
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.PageID) || req.ExpectedVersion < 1 || req.RoutingMode != "" && !validRoutingMode(req.RoutingMode) || !finiteControlStrength(req.Strength) {
		return nil, studio.ErrInvalid
	}
	if _, err := s.resolveImageActor(ctx, actor, jobs.Scope{Kind: jobs.ScopePage, ProjectID: req.ProjectID, PageID: req.PageID}, jobs.Cost{}, false); err != nil {
		return nil, err
	}
	page, err := s.pages.GetPage(ctx, req.PageID)
	if err != nil {
		return nil, err
	}
	if page.ProjectID != req.ProjectID {
		return nil, studio.ErrForbidden
	}
	if page.Version != req.ExpectedVersion {
		return nil, studio.ErrConflict
	}
	if len(page.Panels) == 0 {
		return nil, studio.ErrInvalid
	}
	if req.SketchAssetID != "" {
		if _, _, err := s.readOwned(ctx, req.ProjectID, req.SketchAssetID); err != nil {
			return nil, err
		}
		if req.Strength == 0 {
			req.Strength = .62
		}
		if req.Finish != "screentone" && req.Finish != "grey" && req.Finish != "colour" {
			return nil, studio.ErrInvalid
		}
	}
	all, err := s.pages.ListPages(ctx, page.ChapterID)
	if err != nil {
		return nil, err
	}
	neighbours := []contextPage{}
	for i, p := range all {
		if p.ID == page.ID {
			if i > 0 {
				neighbours = append(neighbours, contextPage{all[i-1].ID, all[i-1].Version})
			}
			if i+1 < len(all) {
				neighbours = append(neighbours, contextPage{all[i+1].ID, all[i+1].Version})
			}
			break
		}
	}
	payload := controlArtPayload{Request: &req, Version: 1, Actor: storedActor{actor.UserID, actor.TokenID}, Scope: jobs.Scope{Kind: jobs.ScopePage, ProjectID: req.ProjectID, PageID: page.ID}}
	for _, pn := range page.Panels {
		p, err := s.preparePanelControls(ctx, actor, PanelRequest{ProjectID: req.ProjectID, PageID: page.ID, PanelID: pn.ID, ExpectedVersion: page.Version, Variations: 1, RoutingMode: req.RoutingMode}, page, "", req.SketchAssetID != "")
		if err != nil {
			return nil, err
		}
		p.ContextPages = neighbours
		p.SketchAssetID, p.Strength = req.SketchAssetID, req.Strength
		if req.SketchAssetID != "" {
			p.Pose = nil // The uploaded/drawn composition is authoritative.
			finish := map[string]string{"screentone": "black ink and screentone", "grey": "ink and flat greys", "colour": "clean manga linework and flat colour"}[req.Finish]
			for i := range p.Components {
				p.Components[i].Prompt = clip(p.Components[i].Prompt, 2800) + fmt.Sprintf(" Convert the supplied rough sketch to %s. Composition fidelity %.2f (0 loose, 1 exact). Keep the input composition and draw only this layer's subject. %s", finish, req.Strength, promptRules)
			}
		}
		for i := range p.Components {
			if len(neighbours) > 0 {
				p.Components[i].Prompt = clip(p.Components[i].Prompt, 3400) + " Neighbouring-page images are setting, lighting and continuity context only. Character identity comes exclusively from this character's kept references. " + promptRules
			}
		}
		if err := s.addControlPanel(ctx, &payload, p); err != nil {
			return nil, err
		}
	}
	return s.enqueueControlArt(ctx, actor, JobPage, payload)
}

func (s *service) EnqueueRepair(ctx context.Context, actor studio.Actor, req RepairRequest) (*jobs.Job, error) {
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.CharacterID) || !db.ValidID(req.ChapterID) || len(req.Targets) == 0 || len(req.Targets) > 24 {
		return nil, studio.ErrInvalid
	}
	if _, err := s.resolveImageActor(ctx, actor, projectTarget(req.ProjectID), jobs.Cost{}, false); err != nil {
		return nil, err
	}
	req.Targets = append([]RepairTarget(nil), req.Targets...)
	sort.Slice(req.Targets, func(i, j int) bool {
		a, b := req.Targets[i], req.Targets[j]
		if a.PageID != b.PageID {
			return a.PageID < b.PageID
		}
		return a.LayerID < b.LayerID
	})
	c, err := s.studio.GetCharacter(ctx, req.CharacterID)
	if err != nil {
		return nil, err
	}
	if c.ProjectID != req.ProjectID {
		return nil, studio.ErrForbidden
	}
	payload := controlArtPayload{Version: 1, Actor: storedActor{actor.UserID, actor.TokenID}, Scope: jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: req.ProjectID, ChapterID: req.ChapterID}}
	seen := map[string]bool{}
	for _, target := range req.Targets {
		if seen[target.LayerID] || !db.ValidID(target.LayerID) || target.Version < 1 {
			return nil, studio.ErrInvalid
		}
		seen[target.LayerID] = true
		page, err := s.pages.GetPage(ctx, target.PageID)
		if err != nil {
			return nil, err
		}
		if page.ProjectID != req.ProjectID || page.ChapterID != req.ChapterID {
			return nil, studio.ErrForbidden
		}
		pn := page.Panel(target.PanelID)
		if pn == nil {
			return nil, studio.ErrNotFound
		}
		l := pn.Layer(target.LayerID)
		if l == nil || l.CharacterID != req.CharacterID || l.Kind != pagedoc.LayerCharacter {
			return nil, studio.ErrInvalid
		}
		p, err := s.preparePanel(ctx, actor, PanelRequest{ProjectID: req.ProjectID, PageID: page.ID, PanelID: pn.ID, LayerID: l.ID, ExpectedVersion: target.Version, Variations: 1, PreserveComposition: true}, page, "")
		if err != nil {
			return nil, err
		}
		if err := s.addControlPanel(ctx, &payload, p); err != nil {
			return nil, err
		}
	}
	return s.enqueueControlArt(ctx, actor, JobRepair, payload)
}

func (s *service) addControlPanel(ctx context.Context, all *controlArtPayload, p imagePayload) error {
	needs := p.Plan.Needs
	needs.References = imageRefCount(p)
	needs.Pose = len(p.Pose) > 0
	needs.Edit = p.InitAssetID != "" || p.SketchAssetID != ""
	req := ai.ImagePlanRequest{Task: p.Plan.Task, ActorID: p.Actor.UserID, ProjectID: p.Scope.ProjectID, RoutingMode: p.Plan.RoutingMode, Needs: needs, PanelType: p.PanelType}
	for _, c := range p.Components {
		if c.CharacterID != "" {
			req.CharacterIDs = append(req.CharacterIDs, c.CharacterID)
		}
	}
	if len(all.Panels) > 0 {
		req.PinProviderID = all.Panels[0].Plan.ProviderID
		req.PinModelID = all.Panels[0].Plan.ModelID
	}
	plan, err := s.images.PlanImage(ctx, req)
	if err != nil {
		return sanitizePlanningError(ctx, err)
	}
	p.Plan = plan
	if _, err := decodeImagePayload(requestJSON(p)); err != nil {
		return err
	}
	cost, err := scaleCost(plan.Estimate, len(p.Components)*imageAttempts)
	if err != nil {
		return err
	}
	all.Estimate = all.Estimate.Add(cost)
	all.Remote = all.Remote || plan.Remote
	all.Panels = append(all.Panels, p)
	return nil
}
func (s *service) checkControlArtTx(ctx context.Context, tx *db.Tx, p controlArtPayload, actor studio.Actor) error {
	if err := s.authorizeImageTx(ctx, tx, actor, p.Scope); err != nil {
		return err
	}
	if err := s.studio.CheckScopeWritableTx(ctx, tx, p.Scope); err != nil {
		return err
	}
	for _, panel := range p.Panels {
		if err := s.checkImageTx(ctx, tx, panel, actor); err != nil {
			return err
		}
	}
	return s.approvalTx(ctx, tx, actor, p.Estimate, p.Remote)
}
func (s *service) enqueueControlArt(ctx context.Context, actor studio.Actor, kind string, p controlArtPayload) (*jobs.Job, error) {
	if _, err := s.resolveImageActor(ctx, actor, p.Scope, p.Estimate, p.Remote); err != nil {
		return nil, err
	}
	first := p.Panels[0].Plan
	j, err := s.queue.Enqueue(ctx, jobs.EnqueueParams{Type: kind, UserID: actor.UserID, Executor: jobs.ExecutorGo, Scope: p.Scope, ProviderID: first.ProviderID, ModelID: first.ModelID, Estimate: p.Estimate, Decision: first.Decision, Input: p, MaxAttempts: 1, Batch: true, Guard: func(ctx context.Context, tx *db.Tx) error {
		if err := s.checkControlArtTx(ctx, tx, p, actor); err != nil {
			return err
		}
		var active []struct {
			ID     string `db:"id"`
			UserID string `db:"user_id"`
			Type   string `db:"type"`
			Input  string `db:"input"`
		}
		if err := tx.Select(ctx, &active, `SELECT id,user_id,type,input FROM jobs WHERE project_id=? AND type IN (?,?) AND state IN ('queued','running') AND cancel_requested=FALSE ORDER BY id LIMIT 1001`, p.Scope.ProjectID, JobPage, JobRepair); err != nil {
			return err
		}
		if len(active) > 1000 {
			return studio.ErrConflict
		}
		targets := map[string]bool{}
		for _, pn := range p.Panels {
			targets[pn.PageID] = true
		}
		for _, old := range active {
			var previous controlArtPayload
			if json.Unmarshal([]byte(old.Input), &previous) != nil {
				return studio.ErrConflict
			}
			for _, pn := range previous.Panels {
				if targets[pn.PageID] {
					if old.UserID == actor.UserID && old.Type == kind && stringJSON(previous) == stringJSON(p) {
						return existingControlJob{old.ID}
					}
					return studio.ErrConflict
				}
			}
		}
		return nil
	}})
	var existing existingControlJob
	if errors.As(err, &existing) {
		// The guard above runs under the target page and chapter row locks
		// (BEGIN IMMEDIATE on SQLite), so this reuse holds across processes.
		return dedupedJob(ctx, s.queue, existing.ID)
	}
	return j, err
}

// dedupedJob returns the active job a guard matched, marked as reused.
func dedupedJob(ctx context.Context, q *jobs.Queue, id string) (*jobs.Job, error) {
	j, err := q.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	j.Deduplicated = true
	return j, nil
}

type existingControlJob struct{ ID string }

func (e existingControlJob) Error() string { return "scoped art job already active" }
func (s *service) handleControlArt(ctx context.Context, run *jobs.Run) error {
	var p controlArtPayload
	j := run.Job
	if _, err := strictDecodeJSON(j.Input, 2<<20, &p); err != nil || p.Version != 1 || p.Actor.UserID != j.UserID || stringJSON(p.Scope) != stringJSON(j.Scope) || len(p.Panels) == 0 || len(p.Panels) > 24 || j.Executor != jobs.ExecutorGo || j.MaxAttempts != 1 || j.Type != JobPage && j.Type != JobRepair {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	if j.Type == JobPage && p.Scope.Kind != jobs.ScopePage || j.Type == JobRepair && p.Scope.Kind != jobs.ScopeChapter {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	actor := studio.Actor{UserID: p.Actor.UserID, TokenID: p.Actor.TokenID}
	cost, slots := jobs.Cost{}, 0
	for _, item := range p.Panels {
		if _, err := decodeImagePayload(requestJSON(item)); err != nil || item.Actor != p.Actor || item.Scope.ProjectID != p.Scope.ProjectID || item.Plan.ProviderID != j.ProviderID || item.Plan.ModelID != j.ModelID || item.Plan.Needs.Count != 1 || j.Type == JobPage && item.PageID != p.Scope.PageID || j.Type == JobRepair && item.Scope.Kind != jobs.ScopeLayer {
			return mapImageJobError(ctx, studio.ErrInvalid)
		}
		c, err := scaleCost(item.Plan.Estimate, len(item.Components)*imageAttempts)
		if err != nil {
			return mapImageJobError(ctx, err)
		}
		cost = cost.Add(c)
		slots += len(item.Components)
	}
	if slots > 384 || cost != p.Estimate || cost != j.Estimate || math.IsNaN(cost.USD) {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	if err := s.db.WithTx(ctx, func(tx *db.Tx) error { return s.checkControlArtTx(ctx, tx, p, actor) }); err != nil {
		return mapImageJobError(ctx, err)
	}
	renders := []imageRender{}
	defer func() { s.discardRenders(context.WithoutCancel(ctx), renders) }()
	// Steps count every component call across all panels plus the commit, so
	// the parent's bar is measured across the whole job from the first call.
	slot, total := 0, slots+1
	report := func(ctx context.Context, done int) error {
		return run.Progress(ctx, float64(done)/float64(total), done, total, "Generating selected art")
	}
	for _, item := range p.Panels {
		var rendered imageRender
		base := slot
		progress := func(ctx context.Context, done int) error { return report(ctx, base+done) }
		if err := s.renderImage(ctx, run, item, slot, progress, &rendered); err != nil {
			return err
		}
		renders = append(renders, rendered)
		slot += len(item.Components)
		if err := report(ctx, slot); err != nil {
			return mapImageJobError(ctx, err)
		}
	}
	err := run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
		if err := s.checkControlArtTx(ctx, tx, p, actor); err != nil {
			return nil, err
		}
		// Every target is checked before the first write. A failure rolls back every
		// staged attachment, including earlier panels/pages of a selected repair.
		versions := map[string]int{}
		operations := map[string][]pagestore.SetLayerAsset{}
		order := []string{}
		for i, item := range p.Panels {
			for _, a := range renders[i].Staged {
				if _, err := s.assets.CommitStagedTx(ctx, tx, a); err != nil {
					return nil, err
				}
			}
			if err := s.saveAssessmentsTx(ctx, tx, renders[i].Assessments); err != nil {
				return nil, err
			}
			if _, seen := operations[item.PageID]; !seen {
				order = append(order, item.PageID)
			}
			page, err := s.pages.GetPageTx(ctx, tx, item.PageID)
			if err != nil {
				return nil, err
			}
			candidate := renders[i].Output.Candidates[0]
			if len(candidate.Layers) != len(item.Components) {
				return nil, studio.ErrInvalid
			}
			for k, l := range candidate.Layers {
				spec := item.Components[k]
				if l.LayerID != spec.LayerID || l.Kind != spec.Kind || l.CharacterID != spec.CharacterID || l.Kind == pagedoc.LayerLettering {
					return nil, studio.ErrInvalid
				}
				provenance := pagedoc.ProvenanceAI
				mask := ""
				if item.SketchAssetID != "" {
					provenance = pagedoc.ProvenanceMixed
				}
				if item.InitAssetID != "" {
					if old := page.Panel(item.PanelID).Layer(l.LayerID); old != nil {
						if old.Provenance != pagedoc.ProvenanceAI {
							provenance = pagedoc.ProvenanceMixed
						}
						if old.Mask != nil {
							mask = old.Mask.ID
						}
					}
				}
				seed := l.Seed
				// No LockScore: saved assessments are the only Character Lock evidence.
				operations[item.PageID] = append(operations[item.PageID], pagestore.SetLayerAsset{PageID: item.PageID, PanelID: item.PanelID, LayerID: l.LayerID, Kind: l.Kind, CharacterID: l.CharacterID, Name: l.Name, AssetID: l.AssetID, MaskAssetID: mask, Seed: &seed, Prompt: l.Prompt, Provenance: provenance, ModelID: j.ModelID, ProviderID: j.ProviderID, JobID: j.ID})
			}
		}
		action := "generate_page"
		if j.Type == JobRepair {
			action = "character_repair"
		}
		for _, pageID := range order {
			ch := changeFor(actor, j.ID, action)
			if _, err := s.pages.SetLayerAssetsTx(ctx, tx, pageID, operations[pageID], ch); err != nil {
				return nil, err
			}
			page, err := s.pages.GetPageTx(ctx, tx, pageID)
			if err != nil {
				return nil, err
			}
			versions[pageID] = page.Version
		}

		return map[string]any{"pages": versions, "applied": true}, nil
	})
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	return nil
}
