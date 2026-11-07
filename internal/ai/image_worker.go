package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

var errImageChildExists = errors.New("ai: image child already exists")

func (s *imageService) workerImage(ctx context.Context, req ImageGenerateRequest) (ImageResult, error) {
	result := ImageResult{ProviderID: providerKindWorker, ModelID: req.Plan.ModelID}
	parent, err := s.dispatchGuard(ctx, req)
	if err != nil {
		return result, err
	}
	if !validWorkerCallSlot(parent, req) {
		return result, imageError(CodeBadInput, false, jobs.Cost{})
	}
	a := assets.New(s.d.DB, s.d.Store, s.d.Media)
	files := map[string]string{}
	saved := []string{}
	defer func() {
		for _, id := range saved {
			_ = a.Delete(context.WithoutCancel(ctx), id)
		}
	}()
	add := func(name string, ref ImageRef) error {
		asset, err := a.Save(ctx, assets.SaveParams{OwnerID: req.ActorID, ProjectID: req.ProjectID, Kind: "generation_input", Source: assets.SourceSystem, Data: ref.Data, ContentType: ref.ContentType, Prefix: fmt.Sprintf("jobs/%s/inputs/%d", req.JobID, req.Attempt)})
		if err != nil {
			return err
		}
		saved = append(saved, asset.ID)
		files[name] = asset.Key
		return nil
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d", req.Plan.ModelID, req.Prompt, req.Negative, req.Style, req.Width, req.Height, req.Count, req.Seed)
	fmt.Fprintf(hash, "\x00%.6f", req.Strength)
	for i, r := range req.References {
		hash.Write([]byte(r.Role))
		hash.Write(r.Data)
		name := fmt.Sprintf("ref_%d", i)
		if r.Role == "pose" {
			name = "pose_image"
		}
		if err := add(name, r); err != nil {
			return result, imageError(CodeUpstream, false, jobs.Cost{})
		}
	}
	if req.Init != nil {
		hash.Write(req.Init.Data)
		if err := add("image", *req.Init); err != nil {
			return result, imageError(CodeUpstream, false, jobs.Cost{})
		}
	}
	if req.Mask != nil {
		hash.Write(req.Mask.Data)
		if err := add("mask", *req.Mask); err != nil {
			return result, imageError(CodeUpstream, false, jobs.Cost{})
		}
	}
	var loraKey string
	if req.LoRAAssetID != "" {
		lora, err := a.Get(ctx, req.LoRAAssetID)
		var meta struct {
			Model string `json:"base_model"`
		}
		if err != nil || lora.ProjectID == nil || *lora.ProjectID != req.ProjectID || lora.Kind != "character_lora" || json.Unmarshal(lora.Meta, &meta) != nil || meta.Model != req.Plan.ModelID {
			return result, imageError(CodeBadInput, false, jobs.Cost{})
		}
		loraKey = lora.Key
		files["lora_0"] = lora.Key
		hash.Write([]byte(lora.SHA256))
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	existing := ""
	input := map[string]any{"prompt": req.Prompt, "negative": req.Negative, "width": req.Width, "height": req.Height, "n": req.Count, "seed": req.Seed, "style": req.Style, "_parent_attempt": req.Attempt, "_call_slot": req.CallSlot, "_request_digest": digest, "routing_mode": req.Plan.RoutingMode}
	if loraKey != "" {
		input["loras"] = []map[string]any{{"key": loraKey, "weight": .9}}
	}
	if req.Init != nil {
		input["strength"] = .9 - .55*req.Strength
	}
	child, err := s.d.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: workerTask(req.Plan.Task, req.Plan.Needs), Scope: imageWorkerScope(parent, req), UserID: req.ActorID, Executor: jobs.ExecutorWorker, ProviderID: providerKindWorker, ModelID: req.Plan.ModelID, Input: input, Files: files, ParentID: req.JobID, MaxAttempts: 1, Estimate: jobs.Cost{GPUSeconds: req.Plan.Estimate.GPUSeconds}, Guard: func(ctx context.Context, tx *db.Tx) error {
		var cur struct {
			State    string `db:"state"`
			Attempts int    `db:"attempts"`
			Canceled bool   `db:"cancel_requested"`
			Provider string `db:"provider_id"`
			Model    string `db:"model_id"`
			Type     string `db:"type"`
			Input    string `db:"input"`
			Executor string `db:"executor"`
		}
		query := `SELECT state, attempts, cancel_requested, provider_id, model_id, type, input, executor FROM jobs WHERE id = ?`
		if tx.Dialect() == db.Postgres {
			query += ` FOR UPDATE`
		}
		if err := tx.Get(ctx, &cur, query, req.JobID); err != nil {
			return err
		}
		if cur.State != jobs.StateRunning || cur.Attempts != req.Attempt || cur.Canceled || cur.Provider != providerKindWorker || cur.Model != req.Plan.ModelID {
			return jobs.ErrLeaseLost
		}
		if !validWorkerCallSlot(&jobs.Job{Type: cur.Type, Executor: cur.Executor, Input: json.RawMessage(cur.Input)}, req) {
			return errors.New("ai: invalid child call slot")
		}
		inst, err := s.d.Settings.InstanceTx(ctx, tx)
		if err != nil || inst.Validate() != nil || inst.LocalOnly || req.Plan.RoutingMode == ModeLocalOnly {
			return ErrRemoteDisabled
		}
		var children []struct {
			ID    string `db:"id"`
			Input string `db:"input"`
		}
		if err := tx.Select(ctx, &children, `SELECT id, input FROM jobs WHERE parent_id = ? AND type=? ORDER BY id`, req.JobID, workerTask(req.Plan.Task, req.Plan.Needs)); err != nil {
			return err
		}
		for _, c := range children {
			var p struct {
				Attempt int    `json:"_parent_attempt"`
				Digest  string `json:"_request_digest"`
				Slot    int    `json:"_call_slot"`
			}
			if json.Unmarshal([]byte(c.Input), &p) != nil {
				return errors.New("ai: invalid child input")
			}
			if p.Attempt == req.Attempt && p.Slot == req.CallSlot {
				if p.Digest != digest {
					return errors.New("ai: conflicting child request")
				}
				existing = c.ID
				return errImageChildExists
			}
		}
		return nil
	}})
	if errors.Is(err, errImageChildExists) {
		child, err = s.d.Jobs.Get(ctx, existing)
	}
	if err != nil {
		return result, imageError(CodeUpstream, false, jobs.Cost{})
	}
	// On reuse the child's original inputs remain authoritative; the new
	// temporary copies can be deleted. On a fresh child they must remain until
	// the child is terminal (or canceled on every exit below).
	defer func() {
		if child != nil && !child.Finished() {
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = s.d.Jobs.Cancel(bg, child.ID)
		}
	}()
	wait := s.workerWait
	if wait <= 0 {
		wait = 10 * time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for !child.Finished() {
		select {
		case <-waitCtx.Done():
			err := imageError(CodeTimeout, true, child.Actual)
			err.Err = waitCtx.Err()
			return result, s.workerObservedError(ctx, child, err)
		case <-ticker.C:
		}
		current, err := s.d.Jobs.Get(waitCtx, child.ID)
		if err != nil {
			failed := imageError(CodeUpstream, false, child.Actual)
			failed.Err = waitCtx.Err()
			return result, s.workerObservedError(ctx, child, failed)
		}
		child = current
		result.Actual = child.Actual
		if _, err := s.dispatchGuard(waitCtx, req); err != nil {
			return result, s.workerObservedError(ctx, child, err)
		}
	}
	result.Actual = child.Actual
	if _, err := s.dispatchGuard(waitCtx, req); err != nil {
		return result, s.workerObservedError(ctx, child, err)
	}
	if child.State != jobs.StateDone {
		code := CodeUpstream
		retry := false
		if child.Error != nil {
			switch child.Error.Code {
			case jobs.ErrCodeSafety:
				code = CodeProviderRefuse
			case jobs.ErrCodeTimeout:
				code = CodeTimeout
				retry = true
			case jobs.ErrCodeBadInput:
				code = CodeBadInput
			case jobs.ErrCodeOOM:
				retry = true
			}
		}
		return result, imageError(code, retry, result.Actual)
	}
	var output struct {
		Images []struct {
			Key  string `json:"key"`
			Seed int64  `json:"seed"`
		} `json:"images"`
	}
	if json.Unmarshal(child.Output, &output) != nil || len(output.Images) != req.Count {
		return result, imageError(CodeBadOutput, false, result.Actual)
	}
	seen := map[string]bool{}
	for _, item := range output.Images {
		if !strings.HasPrefix(item.Key, "jobs/"+child.ID+"/") || strings.Contains(item.Key, "..") || seen[item.Key] {
			return result, imageError(CodeBadOutput, false, result.Actual)
		}
		seen[item.Key] = true
		var owned int
		if err := s.d.DB.Get(ctx, &owned, `SELECT COUNT(*) FROM assets WHERE key = ? AND owner_id = ? AND project_id = ? AND source = 'worker'`, item.Key, req.ActorID, req.ProjectID); err != nil || owned != 1 {
			return result, imageError(CodeBadOutput, false, result.Actual)
		}
		data, err := storage.ReadAll(ctx, s.d.Store, item.Key, maxImageBytes)
		if err != nil {
			return result, imageError(CodeBadOutput, false, result.Actual)
		}
		g, err := decodeGeneratedImage(data, req.Width, req.Height, item.Seed)
		if err != nil {
			return result, imageError(CodeBadOutput, false, result.Actual)
		}
		result.Images = append(result.Images, g)
	}
	return result, nil
}

// A completed worker child is the durable accounting record even when parent
// cancellation wins the race with the bridge's next poll. Preserve its safe
// actual in the error without returning pixels from a stale parent attempt.
func (s *imageService) workerObservedError(ctx context.Context, child *jobs.Job, cause error) *GenerateError {
	var generated *GenerateError
	if !errors.As(cause, &generated) {
		generated = imageError(CodeUpstream, false, jobs.Cost{})
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, jobs.ErrLeaseLost) {
			generated.Err = cause
		}
	}
	copy := *generated
	if child == nil {
		return &copy
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if current, err := s.d.Jobs.Get(bg, child.ID); err == nil {
		child = current
	}
	copy.Actual = child.Actual
	return &copy
}

// Slot zero preserves old single-call jobs and children without _call_slot.
// Additional slots are available only to bounded typed image jobs that already
// declare that exact component in their durable input. Validate before any
// input storage and again while holding the parent attempt's row lock.
func validWorkerCallSlot(parent *jobs.Job, req ImageGenerateRequest) bool {
	if req.CallSlot < 0 || req.CallSlot >= 384 || parent == nil {
		return false
	}
	if parent.Type == "page.generate" || parent.Type == "character.repair" {
		return validControlCallSlot(parent, req)
	}
	if req.CallSlot == 0 {
		return true
	}

	if parent.Executor != jobs.ExecutorGo || parent.Type != req.Plan.Task {
		return false
	}
	switch parent.Type {
	case TaskPanel, TaskRegionEdit, TaskCharacterSheet:
	default:
		return false
	}
	if len(parent.Input) > 1<<20 {
		return false
	}
	var payload struct {
		Version int `json:"version"`
		Plan    struct {
			Task string `json:"task"`
		} `json:"plan"`
		Components []struct {
			Prompt string `json:"prompt"`
		} `json:"components"`
	}
	if json.Unmarshal(parent.Input, &payload) != nil || payload.Version != 1 || payload.Plan.Task != parent.Type ||
		len(payload.Components) > 16 || req.CallSlot >= len(payload.Components) {
		return false
	}
	return payload.Components[req.CallSlot].Prompt != "" && strings.HasPrefix(req.Prompt, payload.Components[req.CallSlot].Prompt)
}

// A page coordinator declares each immutable image component. Slots are the
// flattened panel/component indexes, independent of worker concurrency.
func validControlCallSlot(parent *jobs.Job, req ImageGenerateRequest) bool {
	if parent.Executor != jobs.ExecutorGo || len(parent.Input) > 2<<20 {
		return false
	}
	var p struct {
		Version int `json:"version"`
		Panels  []struct {
			Scope      jobs.Scope `json:"scope"`
			PageID     string     `json:"page_id"`
			Plan       ImagePlan  `json:"plan"`
			Components []struct {
				Prompt string `json:"prompt"`
			} `json:"components"`
		} `json:"panels"`
	}
	if json.Unmarshal(parent.Input, &p) != nil || p.Version != 1 || len(p.Panels) == 0 || len(p.Panels) > 24 {
		return false
	}
	slot := 0
	for _, panel := range p.Panels {
		if panel.Scope.Validate() != nil || panel.Scope.ProjectID != parent.Scope.ProjectID {
			return false
		}
		if parent.Type == "page.generate" && (parent.Scope.Kind != jobs.ScopePage || panel.PageID != parent.Scope.PageID || panel.Scope.PageID != parent.Scope.PageID || panel.Scope.Kind != jobs.ScopePanel) {
			return false
		}
		if parent.Type == "character.repair" && (parent.Scope.Kind != jobs.ScopeChapter || panel.Scope.Kind != jobs.ScopeLayer || len(panel.Components) != 1) {
			return false
		}
		for _, c := range panel.Components {
			if slot == req.CallSlot {
				return c.Prompt != "" && strings.HasPrefix(req.Prompt, c.Prompt) && panel.Plan.Task == req.Plan.Task && panel.Plan.ModelID == req.Plan.ModelID && panel.Plan.ProviderID == req.Plan.ProviderID
			}
			slot++
		}
	}
	return false
}

// Internal page jobs declare a narrower target for each call. Worker children
// inherit that target, so the common queue validator sees descendant locks too.
func imageWorkerScope(parent *jobs.Job, req ImageGenerateRequest) jobs.Scope {
	if parent.Type != "page.generate" && parent.Type != "character.repair" {
		return parent.Scope
	}
	var p struct {
		Panels []struct {
			Scope      jobs.Scope        `json:"scope"`
			Components []json.RawMessage `json:"components"`
		} `json:"panels"`
	}
	if json.Unmarshal(parent.Input, &p) != nil {
		return parent.Scope
	}
	slot := 0
	for _, pn := range p.Panels {
		for range pn.Components {
			if slot == req.CallSlot {
				return pn.Scope
			}
			slot++
		}
	}
	return parent.Scope
}
