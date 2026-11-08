package gen

import (
	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"strings"
	"unicode/utf8"
)

func validatePayloadTarget(p imagePayload) error {
	if !db.ValidID(p.Scope.ProjectID) || !utf8.ValidString(p.Style) || len(p.Style) > 256 || len(p.Components) > maxComponents || len(p.TargetHash) != 0 && len(p.TargetHash) != 64 {
		return studio.ErrInvalid
	}
	if len(p.ContextPages) > 2 || !finiteControlStrength(p.Strength) {
		return studio.ErrInvalid
	}
	if p.Plan.Needs.Pose != (len(p.Pose) > 0) {
		return studio.ErrInvalid
	}
	if _, err := pagedoc.DecodePose(p.Pose); err != nil {
		return studio.ErrInvalid
	}
	seenContext := map[string]bool{}
	for _, c := range p.ContextPages {
		if !db.ValidID(c.ID) || c.ID == p.PageID || c.Version < 1 || seenContext[c.ID] {
			return studio.ErrInvalid
		}
		seenContext[c.ID] = true
	}
	if p.SketchAssetID != "" && (!db.ValidID(p.SketchAssetID) || p.InitAssetID != "" || p.MaskAssetID != "") {
		return studio.ErrInvalid
	}
	for _, id := range p.References {
		if !db.ValidID(id) {
			return studio.ErrInvalid
		}
	}
	known := map[string]bool{}
	for _, id := range p.References {
		known[id] = true
	}
	for _, c := range p.Components {
		if c.ReferenceHash != "" && (len(c.ReferenceHash) != 64 || c.CharacterID == "") {
			return studio.ErrInvalid
		}
		if strings.TrimSpace(c.Prompt) == "" || !utf8.ValidString(c.Prompt) || len([]rune(c.Prompt)) > pagedoc.MaxPromptRunes || !utf8.ValidString(c.Name) || len([]rune(c.Name)) > 240 {
			return studio.ErrInvalid
		}
		if c.Background != "" && c.Background != "opaque" && c.Background != "transparent" || !utf8.ValidString(c.Negative) || len(c.Negative) > negativeBytes || !c.OwnRefs && len(c.References) > 0 || len(c.References) > 32 {
			return studio.ErrInvalid
		}
		for _, id := range c.References {
			if !known[id] {
				return studio.ErrInvalid
			}
		}
	}
	switch p.Plan.Task {
	case ai.TaskCharacterSheet:
		var req CharacterSheetRequest
		if _, err := strictDecodeJSON(p.Request, 16<<10, &req); err != nil || req.ProjectID != p.Scope.ProjectID || req.CharacterID != p.CharacterID || p.Scope.Kind != jobs.ScopeProject || !db.ValidID(p.CharacterID) || p.ExpectedUpdatedAt.IsZero() || len(p.Components) != 1 || p.Components[0].Kind != "" || p.PageID != "" || p.PanelID != "" || req.Count != p.Plan.Needs.Count || !req.ExpectedUpdatedAt.UTC().Equal(p.ExpectedUpdatedAt.UTC()) {
			return studio.ErrInvalid
		}
	case ai.TaskPanel:
		var req PanelRequest
		if _, err := strictDecodeJSON(p.Request, 32<<10, &req); err != nil || req.ProjectID != p.Scope.ProjectID || req.PageID != p.PageID || req.PanelID != p.PanelID || req.Variations != p.Plan.Needs.Count || p.CharacterID != "" || p.MaskAssetID != "" || req.PreserveComposition != (p.InitAssetID != "") || p.InitAssetID != "" && (!db.ValidID(p.InitAssetID) || req.LayerID == "" || len(p.References) == 0 || len(p.Components) != 1 || p.Components[0].Kind != pagedoc.LayerCharacter) {
			return studio.ErrInvalid
		}
		if p.Scope.Kind == jobs.ScopePanel {
			if p.Scope.PageID != p.PageID || p.Scope.PanelID != p.PanelID || req.LayerID != "" {
				return studio.ErrInvalid
			}
		} else if p.Scope.Kind == jobs.ScopeLayer {
			if p.Scope.PanelID != p.PanelID || req.LayerID != p.Scope.LayerID || len(p.Components) != 1 || p.Components[0].LayerID != req.LayerID {
				return studio.ErrInvalid
			}
		} else {
			return studio.ErrInvalid
		}
		if req.ExpectedVersion != p.ExpectedVersion {
			return studio.ErrInvalid
		}
	case ai.TaskRegionEdit:
		var req RegionEditRequest
		if _, err := strictDecodeJSON(p.Request, 32<<10, &req); err != nil || req.ProjectID != p.Scope.ProjectID || req.LayerID != p.Scope.LayerID || p.Scope.Kind != jobs.ScopeRegion || req.MaskAssetID != p.MaskAssetID || !db.ValidID(p.InitAssetID) || !db.ValidID(p.MaskAssetID) || req.Variations != p.Plan.Needs.Count || req.ExpectedVersion != p.ExpectedVersion || len(p.Components) != 1 || p.Components[0].LayerID != req.LayerID || p.Components[0].Prompt != req.Prompt || p.CharacterID != "" {
			return studio.ErrInvalid
		}
	default:
		return studio.ErrInvalid
	}
	if p.CharacterID == "" {
		if !db.ValidID(p.PageID) || !db.ValidID(p.PanelID) || p.ExpectedVersion < 1 || len(p.TargetHash) != 64 {
			return studio.ErrInvalid
		}
		for _, c := range p.Components {
			if c.LayerID != "" && !db.ValidID(c.LayerID) {
				return studio.ErrInvalid
			}
			if c.Kind != pagedoc.LayerBackground && c.Kind != pagedoc.LayerCharacter && c.Kind != pagedoc.LayerFX || c.Kind == pagedoc.LayerCharacter && !db.ValidID(c.CharacterID) {
				return studio.ErrInvalid
			}
		}
	}
	return nil
}

func finiteControlStrength(n float64) bool { return n >= 0 && n <= 1 }
