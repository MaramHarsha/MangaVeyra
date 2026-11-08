package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Keeping QA-only references is a separate, deliberate stage after visual and
// prospective scorer validation. The normal Bible service owns the mutation.
func (h *harness) keepOutfitViews(reviewed bool, artifactPath string) error {
	if !reviewed || artifactPath == "" {
		return errors.New("keep needs --reviewed-outfit-views and the independently validated --outfit-model")
	}
	var model struct {
		Schema string   `json:"schema"`
		Refs   []string `json:"validation_reference_sha256"`
	}
	raw, err := os.ReadFile(artifactPath)
	if err != nil || json.Unmarshal(raw, &model) != nil || model.Schema != "mangaveyra.outfit-multiview.v1" || len(model.Refs) != 5 {
		return errors.New("missing validated five-view outfit artifact")
	}
	var pack benchmarkState
	raw, err = os.ReadFile(filepath.Join(h.root, "benchmark-outfit-views.json"))
	if err != nil || json.Unmarshal(raw, &pack) != nil || len(pack.Cases) != 4 {
		return errors.New("missing dedicated canonical outfit pose pack")
	}
	before, err := h.pages.ChapterPages(h.ctx, h.s.ChapterID)
	if err != nil {
		return err
	}
	c, err := h.studio.GetCharacter(h.ctx, h.s.CharacterID)
	if err != nil {
		return err
	}
	variant, front := "", ""
	for _, o := range c.Outfits {
		if o.IsDefault {
			variant, front = o.ID, o.AssetID
		}
	}
	if variant == "" || front == "" {
		return errors.New("QA character needs its reviewed default outfit")
	}
	as := core.MustUse[*assets.Service](h.a.Deps)
	frontBytes, _, err := as.Read(h.ctx, front, assets.MaxBytes)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(frontBytes)
	if hex.EncodeToString(sum[:]) != model.Refs[0] {
		return errors.New("default outfit differs from frozen validation reference")
	}
	baseline := filepath.Join(h.root, "report-ccip-before-multiview.json")
	if _, err := os.Stat(baseline); os.IsNotExist(err) {
		data, err := os.ReadFile(filepath.Join(h.root, "report.json"))
		if err != nil {
			return err
		}
		if err := os.WriteFile(baseline, data, 0600); err != nil {
			return err
		}
	}
	type keptView struct{ SourceID, AssetID, SHA256, ReferenceID string }
	resultPath := filepath.Join(h.root, "outfit-reference-keep-result.json")
	result := struct {
		OutfitID       string     `json:"outfit_id"`
		Reviewer       string     `json:"reviewed_by"`
		Views          []keptView `json:"views"`
		UnchangedPages int        `json:"unchanged_pages"`
	}{OutfitID: variant, Reviewer: "Codex (AI visual review)"}
	if raw, err := os.ReadFile(resultPath); err == nil {
		if err = json.Unmarshal(raw, &result); err != nil {
			return err
		}
		if result.OutfitID != variant {
			return errors.New("default outfit changed since keep began")
		}
	}
	save := func() error {
		raw, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(resultPath, raw, 0600)
	}
	bible := core.MustUse[studio.BibleService](h.a.Deps)
	for i, pose := range pack.Cases {
		data, source, err := as.Read(h.ctx, pose.AssetID, assets.MaxBytes)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		if digest != model.Refs[i+1] || source.Source != assets.SourceAI || source.ProjectID == nil || *source.ProjectID != pack.ProjectID {
			return errors.New("canonical pose lineage differs from frozen validation")
		}
		if len(result.Views) <= i {
			asset, err := as.Save(h.ctx, assets.SaveParams{OwnerID: h.s.UserID, ProjectID: h.s.ProjectID, Kind: "character_ref", Source: assets.SourceAI, Data: data, Meta: map[string]any{"canonical_source_asset_id": pose.AssetID, "canonical_pose": pose.Key, "reviewed_by": result.Reviewer, "validation_reference_only": true}})
			if err != nil {
				return err
			}
			result.Views = append(result.Views, keptView{SourceID: pose.AssetID, AssetID: asset.ID, SHA256: digest})
			if err := save(); err != nil {
				return err
			}
		}
		view := &result.Views[i]
		if view.SourceID != pose.AssetID || view.SHA256 != digest {
			return errors.New("kept reference state differs")
		}
		c, err = h.studio.GetCharacter(h.ctx, c.ID)
		if err != nil {
			return err
		}
		found := false
		for _, ref := range c.Refs {
			if ref.AssetID == view.AssetID && ref.Kind == "outfit" && ref.OutfitID == variant {
				found, view.ReferenceID = true, ref.ID
			}
		}
		if !found {
			ref, err := bible.AddCharacterRef(h.ctx, h.actor, studio.NewCharacterRef{ProjectID: h.s.ProjectID, CharacterID: c.ID, AssetID: view.AssetID, Kind: "outfit", Label: "Tide Courier · " + pose.Key, ExpectedUpdatedAt: c.UpdatedAt})
			if err != nil {
				return err
			}
			view.ReferenceID = ref.ID
		}
		if err := save(); err != nil {
			return err
		}
	}
	after, err := h.pages.ChapterPages(h.ctx, h.s.ChapterID)
	if err != nil {
		return err
	}
	if !wireEqual(before, after) {
		return errors.New("keeping outfit views changed chapter documents")
	}
	result.UnchangedPages = len(after)
	if err := save(); err != nil {
		return err
	}
	fmt.Printf("Kept four dedicated outfit views; %d chapter documents unchanged\n", len(after))
	return nil
}
