package gen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func (s *service) readOwned(ctx context.Context, projectID, assetID string) ([]byte, assets.Asset, error) {
	a, err := s.assets.Get(ctx, assetID)
	if err != nil {
		return nil, a, err
	}
	if a.ProjectID == nil || *a.ProjectID != projectID {
		return nil, a, studio.ErrForbidden
	}
	data, _, err := s.assets.Read(ctx, assetID, assets.MaxBytes)
	if err != nil {
		return nil, a, err
	}
	if _, _, err = storage.ImageConfig(data, storage.ImageLimits{MaxWidth: 4096, MaxHeight: 4096, MaxPixels: 16 << 20}); err != nil {
		return nil, a, err
	}
	return data, a, nil
}
func decodePixels(data []byte) (image.Image, error) {
	im, _, err := storage.DecodeImage(data, storage.ImageLimits{MaxWidth: 4096, MaxHeight: 4096, MaxPixels: 16 << 20})
	return im, err
}
func whiteMask(c color.Color) bool {
	v := color.NRGBAModel.Convert(c).(color.NRGBA)
	return v.R == 255 && v.G == 255 && v.B == 255 && v.A == 255
}
func validateMask(init, mask []byte, ct string) (int, int, error) {
	if ct != "image/png" || storage.DetectContentType(mask) != "image/png" {
		return 0, 0, studio.ErrInvalid
	}
	a, err := decodePixels(init)
	if err != nil {
		return 0, 0, err
	}
	m, err := decodePixels(mask)
	if err != nil || a.Bounds().Size() != m.Bounds().Size() {
		return 0, 0, studio.ErrInvalid
	}
	n := 0
	for y := 0; y < m.Bounds().Dy(); y++ {
		for x := 0; x < m.Bounds().Dx(); x++ {
			if whiteMask(m.At(x, y)) {
				n++
			}
		}
	}
	if n == 0 {
		return 0, 0, studio.ErrInvalid
	}
	return a.Bounds().Dx(), a.Bounds().Dy(), nil
}
func preserveRegion(init, mask, generated []byte) ([]byte, error) {
	a, err := decodePixels(init)
	if err != nil {
		return nil, err
	}
	m, err := decodePixels(mask)
	if err != nil {
		return nil, err
	}
	g, err := decodePixels(generated)
	if err != nil || g.Bounds().Size() != a.Bounds().Size() || m.Bounds().Size() != a.Bounds().Size() {
		return nil, studio.ErrInvalid
	}
	out := image.NewNRGBA(a.Bounds())
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			c := a.At(x, y)
			if whiteMask(m.At(x, y)) {
				c = g.At(x, y)
			}
			out.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	err = png.Encode(&buf, out)
	return buf.Bytes(), err
}
func seedFor(jobID string, component int) int64 {
	h := sha256.Sum256([]byte(jobID + ":" + string(rune(component))))
	return int64(binary.BigEndian.Uint64(h[:8]) & 0x7fffffffffffffff)
}
func (s *service) handleImage(ctx context.Context, run *jobs.Run) error {
	j := run.Job
	if j == nil || j.Executor != jobs.ExecutorGo || j.MaxAttempts != imageAttempts || j.RoundAttempt() < 1 || j.RoundAttempt() > imageAttempts {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	p, err := decodeImagePayload(j.Input)
	if err != nil || p.Plan.Task != j.Type || p.Actor.UserID != j.UserID || p.BatchID != j.ParentID || stringJSON(p.Scope) != stringJSON(j.Scope) {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	plan := currentImagePlan(p, j)
	if plan.Remote && !p.Plan.Remote || validateImagePlan(plan) != nil {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	cost, err := scaleCost(p.Plan.Estimate, imageAttempts*len(p.Components))
	if err != nil || cost != j.Estimate {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	actor, err := s.resolveImageActor(ctx, imageActor(p), p.Scope.ProjectID, cost, plan.Remote)
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	if err = s.db.WithTx(ctx, func(tx *db.Tx) error { return s.checkImageTx(ctx, tx, p, actor) }); err != nil {
		return mapImageJobError(ctx, err)
	}
	totalInputBytes := 0
	var references []ai.ImageRef
	refByID := map[string]ai.ImageRef{}
	for _, id := range p.References {
		data, a, err := s.readOwned(ctx, p.Scope.ProjectID, id)
		if err != nil {
			return mapImageJobError(ctx, err)
		}
		totalInputBytes += len(data)
		if totalInputBytes > 48<<20 {
			return mapImageJobError(ctx, studio.ErrInvalid)
		}
		ref := ai.ImageRef{Data: data, ContentType: a.ContentType, Role: "reference"}
		references = append(references, ref)
		refByID[id] = ref
	}
	var initRef, maskRef *ai.ImageRef
	var init, mask []byte
	if p.InitAssetID != "" {
		data, a, err := s.readOwned(ctx, p.Scope.ProjectID, p.InitAssetID)
		if err != nil {
			return mapImageJobError(ctx, err)
		}
		totalInputBytes += len(data)
		if totalInputBytes > 48<<20 {
			return mapImageJobError(ctx, studio.ErrInvalid)
		}
		init = data
		initRef = &ai.ImageRef{Data: data, ContentType: a.ContentType, Role: "init"}
		data, a, err = s.readOwned(ctx, p.Scope.ProjectID, p.MaskAssetID)
		if err != nil {
			return mapImageJobError(ctx, err)
		}
		totalInputBytes += len(data)
		if totalInputBytes > 48<<20 {
			return mapImageJobError(ctx, studio.ErrInvalid)
		}
		mask = data
		maskRef = &ai.ImageRef{Data: data, ContentType: a.ContentType, Role: "mask"}
		if _, _, err = validateMask(init, mask, a.ContentType); err != nil {
			return mapImageJobError(ctx, err)
		}
	}
	staged := []*assets.StagedAsset{}
	committed := false
	defer func() {
		if !committed {
			for _, a := range staged {
				_ = s.assets.DiscardStaged(context.WithoutCancel(ctx), a)
			}
		}
	}()
	stage := func(data []byte, kind string) (string, error) {
		a, err := s.assets.Stage(ctx, assets.SaveParams{OwnerID: actor.UserID, ProjectID: p.Scope.ProjectID, Kind: kind, Source: assets.SourceAI, ContentType: "image/png", Data: data, Meta: map[string]string{"job_id": j.ID, "provider_id": plan.ProviderID, "model_id": plan.ModelID}})
		if err != nil {
			return "", err
		}
		staged = append(staged, a)
		return a.Asset().ID, nil
	}
	out := imageOutput{Candidates: make([]Candidate, plan.Needs.Count), PageID: p.PageID, CharacterID: p.CharacterID}
	previewImages := make([][]image.Image, plan.Needs.Count)
	for i, c := range p.Components {
		if err := run.Progress(ctx, float64(i)/float64(len(p.Components)+1), i, len(p.Components)+1, "Generating art"); err != nil {
			return mapImageJobError(ctx, err)
		}
		if _, err := s.resolveImageActor(ctx, actor, p.Scope.ProjectID, cost, plan.Remote); err != nil {
			return mapImageJobError(ctx, err)
		}
		if err = s.db.WithTx(ctx, func(tx *db.Tx) error { return s.checkImageTx(ctx, tx, p, actor) }); err != nil {
			return mapImageJobError(ctx, err)
		}
		// ADR 0009 payloads carry each component's own references, alpha
		// behaviour and avoid list; older payloads keep their original call.
		callRefs, prompt, negative := references, c.Prompt, legacyNegative
		if c.OwnRefs {
			callRefs = make([]ai.ImageRef, 0, len(c.References))
			for _, id := range c.References {
				callRefs = append(callRefs, refByID[id])
			}
			if c.Negative != "" {
				negative = c.Negative
			}
			if p.Plan.Task == ai.TaskRegionEdit {
				prompt = regionEditPrompt(p.Style, c.Kind, c.Prompt)
			}
		}
		result, err := s.images.GenerateImage(ctx, ai.ImageGenerateRequest{Plan: plan, ActorID: actor.UserID, JobID: j.ID, ProjectID: p.Scope.ProjectID, Attempt: j.Attempts, CallSlot: i, Prompt: prompt, Negative: negative, Width: plan.Needs.Width, Height: plan.Needs.Height, Count: plan.Needs.Count, Seed: seedFor(j.ID, i), Style: p.Style, Background: c.Background, References: callRefs, Init: initRef, Mask: maskRef})
		if err != nil {
			return mapAIJobError(ctx, run, err)
		}
		if !validCost(result.Actual) {
			return internalJobError()
		}
		run.AddActual(result.Actual)
		if result.ProviderID != plan.ProviderID || result.ModelID != plan.ModelID || len(result.Images) != plan.Needs.Count {
			return mapImageJobError(ctx, studio.ErrInvalid)
		}
		for k, im := range result.Images {
			pixels, err := decodePixels(im.Data)
			if err != nil || pixels.Bounds().Dx() != plan.Needs.Width || pixels.Bounds().Dy() != plan.Needs.Height {
				return mapImageJobError(ctx, studio.ErrInvalid)
			}
			data := im.Data
			if initRef != nil {
				data, err = preserveRegion(init, mask, data)
				if err != nil {
					return mapImageJobError(ctx, err)
				}
				pixels, err = decodePixels(data)
				if err != nil {
					return mapImageJobError(ctx, err)
				}
			}
			if plan.ProviderID == ai.PlaceholderID && j.Type == JobPanel && (c.Kind == pagedoc.LayerCharacter || c.Kind == pagedoc.LayerFX) {
				pixels = placeholderComponent(pixels, c.Kind)
			}
			var b bytes.Buffer
			if err = png.Encode(&b, pixels); err != nil {
				return mapImageJobError(ctx, err)
			}
			kind := "layer"
			if p.CharacterID != "" {
				kind = "character_ref"
			}
			id, err := stage(b.Bytes(), kind)
			if err != nil {
				return mapImageJobError(ctx, err)
			}
			candidate := &out.Candidates[k]
			candidate.Index = k
			candidate.Seed = im.Seed
			candidate.AssetID = id
			if c.Kind != "" {
				candidate.Layers = append(candidate.Layers, CandidateLayer{LayerID: c.LayerID, Kind: c.Kind, CharacterID: c.CharacterID, Name: c.Name, AssetID: id, Seed: im.Seed, Prompt: c.Prompt})
			}
			previewImages[k] = append(previewImages[k], pixels)
		}
	}
	if len(p.Components) > 1 {
		for i, images := range previewImages {
			preview := image.NewNRGBA(image.Rect(0, 0, plan.Needs.Width, plan.Needs.Height))
			for _, im := range images {
				draw.Draw(preview, preview.Bounds(), im, im.Bounds().Min, draw.Over)
			}
			var b bytes.Buffer
			if err = png.Encode(&b, preview); err != nil {
				return mapImageJobError(ctx, err)
			}
			id, err := stage(b.Bytes(), "generation_preview")
			if err != nil {
				return mapImageJobError(ctx, err)
			}
			out.Candidates[i].AssetID = id
		}
	}
	err = run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
		if err := s.checkImageTx(ctx, tx, p, actor); err != nil {
			return nil, err
		}
		for _, a := range staged {
			if _, err := s.assets.CommitStagedTx(ctx, tx, a); err != nil {
				return nil, err
			}
		}
		if len(out.Candidates) == 1 {
			if err := s.attachCandidateTx(ctx, tx, j, p, actor, &out, 0); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	committed = true
	return nil
}
func (s *service) attachCandidateTx(ctx context.Context, tx *db.Tx, j *jobs.Job, p imagePayload, actor studio.Actor, out *imageOutput, index int) error {
	if index < 0 || index >= len(out.Candidates) || out.AppliedIndex != nil {
		return studio.ErrConflict
	}
	c := out.Candidates[index]
	if c.Index != index {
		return studio.ErrInvalid
	}
	if p.CharacterID != "" {
		a, err := s.assets.GetTx(ctx, tx, c.AssetID)
		if err != nil || a.ProjectID == nil || *a.ProjectID != p.Scope.ProjectID {
			return studio.ErrForbidden
		}
		if _, err = s.studio.AddCharacterRefTx(ctx, tx, p.CharacterID, c.AssetID, "sheet", "Generated reference sheet"); err != nil {
			return err
		}
		if err = s.studio.SetCharacterPortraitTx(ctx, tx, p.CharacterID, c.AssetID); err != nil {
			return err
		}
	} else {
		if len(c.Layers) != len(p.Components) {
			return studio.ErrInvalid
		}
		ch := changeFor(actor, j.ID, "generate_layer")
		for i, l := range c.Layers {
			spec := p.Components[i]
			if l.LayerID != spec.LayerID || l.Kind != spec.Kind || l.CharacterID != spec.CharacterID || l.Kind == pagedoc.LayerLettering {
				return studio.ErrInvalid
			}
			seed := l.Seed
			maskToKeep := ""
			provenance := pagedoc.ProvenanceAI
			if p.InitAssetID != "" {
				page, err := s.pages.GetPageTx(ctx, tx, p.PageID)
				if err != nil {
					return err
				}
				if current := page.Panel(p.PanelID).Layer(l.LayerID); current != nil {
					if current.Provenance != pagedoc.ProvenanceAI {
						provenance = pagedoc.ProvenanceMixed
					}
					if current.Mask != nil {
						maskToKeep = current.Mask.ID
					}
				}
			}
			stored, err := s.pages.SetLayerAssetTx(ctx, tx, pagestore.SetLayerAsset{PageID: p.PageID, PanelID: p.PanelID, LayerID: l.LayerID, Kind: l.Kind, CharacterID: l.CharacterID, Name: l.Name, AssetID: l.AssetID, MaskAssetID: maskToKeep, Seed: &seed, Prompt: l.Prompt, Provenance: provenance, ModelID: j.ModelID, ProviderID: j.ProviderID, JobID: j.ID}, ch)
			if err != nil {
				return err
			}
			out.Candidates[index].Layers[i].LayerID = stored.ID
		}
		page, err := s.pages.GetPageTx(ctx, tx, p.PageID)
		if err != nil {
			return err
		}
		out.Version = page.Version
	}
	chosen := index
	out.AppliedIndex = &chosen
	return nil
}
func (s *service) candidateJob(ctx context.Context, actor studio.Actor, id string) (*jobs.Job, imagePayload, imageOutput, error) {
	var p imagePayload
	var out imageOutput
	if !db.ValidID(id) {
		return nil, p, out, studio.ErrInvalid
	}
	j, err := s.queue.Get(ctx, id)
	if err != nil {
		return nil, p, out, err
	}
	if j.UserID != actor.UserID || j.State != jobs.StateDone || j.Type != JobPanel && j.Type != JobRegionEdit && j.Type != JobCharacterSheet {
		return nil, p, out, studio.ErrForbidden
	}
	p, err = decodeImagePayload(j.Input)
	if err != nil || p.Plan.Task != j.Type || p.BatchID != j.ParentID || stringJSON(p.Scope) != stringJSON(j.Scope) || p.Actor.UserID != j.UserID {
		return nil, p, out, studio.ErrInvalid
	}
	if err = s.studio.Authorize(ctx, actor, j.Scope.ProjectID, studio.ActRead); err != nil {
		return nil, p, out, err
	}
	if _, err = strictDecodeJSON(j.Output, 2<<20, &out); err != nil || len(out.Candidates) < 1 || len(out.Candidates) > 4 {
		return nil, p, out, studio.ErrInvalid
	}
	return j, p, out, nil
}
func (s *service) Candidates(ctx context.Context, actor studio.Actor, id string) ([]Candidate, error) {
	_, p, out, err := s.candidateJob(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	for i := range out.Candidates {
		a, err := s.assets.Get(ctx, out.Candidates[i].AssetID)
		if err != nil || a.ProjectID == nil || *a.ProjectID != p.Scope.ProjectID {
			return nil, studio.ErrForbidden
		}
		out.Candidates[i].URL = s.assets.URL(a, time.Hour)
	}
	return out.Candidates, nil
}
func (s *service) ApplyVariation(ctx context.Context, actor studio.Actor, id string, index int) error {
	j, p, out, err := s.candidateJob(ctx, actor, id)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(out.Candidates) || out.AppliedIndex != nil {
		return studio.ErrConflict
	}
	if _, err = s.resolveImageActor(ctx, actor, p.Scope.ProjectID, jobs.Cost{}, false); err != nil {
		return err
	}
	// Storage is read outside the transaction. Refuse any region candidate that
	// does not preserve exactly the original decoded pixels outside white mask.
	if p.InitAssetID != "" {
		a, _, err := s.readOwned(ctx, p.Scope.ProjectID, p.InitAssetID)
		if err != nil {
			return err
		}
		m, _, err := s.readOwned(ctx, p.Scope.ProjectID, p.MaskAssetID)
		if err != nil {
			return err
		}
		g, _, err := s.readOwned(ctx, p.Scope.ProjectID, out.Candidates[index].AssetID)
		if err != nil {
			return err
		}
		safe, err := preserveRegion(a, m, g)
		if err != nil {
			return err
		}
		expected, err := decodePixels(safe)
		if err != nil {
			return err
		}
		actual, err := decodePixels(g)
		if err != nil {
			return err
		}
		for y := 0; y < expected.Bounds().Dy(); y++ {
			for x := 0; x < expected.Bounds().Dx(); x++ {
				if color.NRGBAModel.Convert(expected.At(x, y)) != color.NRGBAModel.Convert(actual.At(x, y)) {
					return studio.ErrInvalid
				}
			}
		}
	}
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		var raw string
		if err := tx.Get(ctx, &raw, `SELECT output FROM jobs WHERE id=? AND state='done'`+lockSQL(tx), id); err != nil {
			return err
		}
		var fresh imageOutput
		if err := json.Unmarshal([]byte(raw), &fresh); err != nil || fresh.AppliedIndex != nil {
			return studio.ErrConflict
		}
		if err := s.checkImageTx(ctx, tx, p, actor); err != nil {
			return err
		}
		if err := s.attachCandidateTx(ctx, tx, j, p, actor, &fresh, index); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE jobs SET output=?,updated_at=? WHERE id=?`, db.MustJSON(fresh), db.Now(), id)
		return err
	})
}

// The offline provider's labelled art is a stand-in. Its overlays get simple
// transparent silhouettes so its independent background remains visible.
// Real model pixels are preserved as returned by the image service.
func placeholderComponent(src image.Image, kind string) image.Image {
	b := src.Bounds()
	out := image.NewNRGBA(b)
	w, h := float64(b.Dx()), float64(b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			visible := false
			if kind == pagedoc.LayerCharacter {
				nx, ny := (float64(x)-w*.5)/(w*.19), (float64(y)-h*.36)/(h*.16)
				visible = nx*nx+ny*ny <= 1 || float64(x) > w*.32 && float64(x) < w*.68 && float64(y) > h*.5 && float64(y) < h*.88
			} else {
				visible = (x+y)%53 < 2 && float64(y) > h*.12
			}
			if visible {
				out.Set(x, y, src.At(x, y))
			}
		}
	}
	return out
}
