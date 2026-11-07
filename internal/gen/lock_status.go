package gen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

const lockModel = "character-lock-dinov2-v1"
const lockMethod = "dinov2-small-v1"
const lockCCIPModel = "character-lock-ccip-v1"
const lockCCIPMethod = "ccip-caformer24-v1"
const lockOutfitModel = "character-lock-ccip-outfit-v3"
const lockOutfitMethod = "ccip-outfit-pair-v3"
const lockMultiviewModel = "character-lock-ccip-outfit-multiview-v1"
const lockMultiviewMethod = "ccip-outfit-multiview-v1"

// The fingerprint includes immutable image checksums and authored identity /
// outfit descriptions. Changing the bible invalidates old evidence and LoRAs.
func referenceStamp(ctx context.Context, q db.Querier, c studio.Character) (string, error) {
	var hashes []string
	for _, r := range c.Refs {
		var sha string
		if err := q.Get(ctx, &sha, `SELECT sha256 FROM assets WHERE id=? AND project_id=?`, r.AssetID, c.ProjectID); err != nil {
			return "", err
		}
		entry := r.Kind + ":" + r.AssetID + ":" + sha
		if r.OutfitID != "" {
			entry += ":outfit=" + r.OutfitID
		}
		hashes = append(hashes, entry)
	}
	for _, o := range c.Outfits {
		if o.IsDefault {
			sha := ""
			if o.AssetID != "" {
				if err := q.Get(ctx, &sha, `SELECT sha256 FROM assets WHERE id=? AND project_id=?`, o.AssetID, c.ProjectID); err != nil {
					return "", err
				}
			}
			hashes = append(hashes, "outfit:"+o.ID+":"+o.Name+":"+o.Description+":"+sha)
		}
	}
	slices.Sort(hashes)
	raw, _ := json.Marshal([]any{c.Name, c.Look, hashes})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (s *service) workerFor(ctx context.Context, kind string) (string, bool) {
	inst, err := s.settings.Instance(ctx)
	if err != nil || inst.LocalOnly {
		return "", false
	}
	workers, err := s.queue.ListWorkers(ctx)
	if err != nil {
		return "", false
	}
	if kind == "character.score" {
		// CCIP is advertised only by an explicitly configured CCIP worker.
		// Auto/DINO-only installations keep their existing model selection.
		for _, model := range []string{lockMultiviewModel, lockOutfitModel, lockCCIPModel, lockModel} {
			for _, w := range workers {
				if w.Status == jobs.WorkerOnline && time.Since(w.LastSeenAt) <= jobs.HeartbeatInterval*jobs.MissedHeartbeats && slices.Contains(w.JobTypes.V, kind) && slices.Contains(w.Backends.V, "vision") && slices.Contains(w.Models.V, model) {
					return model, true
				}
			}
		}
		return "", false
	}
	for _, w := range workers {
		if w.Status != jobs.WorkerOnline || time.Since(w.LastSeenAt) > jobs.HeartbeatInterval*jobs.MissedHeartbeats || !slices.Contains(w.JobTypes.V, kind) {
			continue
		}
		if kind == "character.train" && slices.Contains(w.Backends.V, "diffusers") {
			for _, id := range []string{"illustrious-xl", "sdxl-base"} {
				if slices.Contains(w.Models.V, id) {
					return id, true
				}
			}
		}
	}
	return "", false
}
func (s *service) LockStatus(ctx context.Context, actor studio.Actor, projectID, characterID string) (LockStatus, error) {
	return s.lockStatus(ctx, actor, projectID, characterID, "")
}
func (s *service) LockChapterStatus(ctx context.Context, actor studio.Actor, projectID, characterID, chapterID string) (LockStatus, error) {
	return s.lockStatus(ctx, actor, projectID, characterID, chapterID)
}
func (s *service) lockStatus(ctx context.Context, actor studio.Actor, projectID, characterID, chapterID string) (LockStatus, error) {
	out := LockStatus{CharacterID: characterID, Status: "unscored", Threshold: LockThreshold, Drift: []DriftPanel{}}
	if err := s.studio.Authorize(ctx, actor, projectID, studio.ActRead); err != nil {
		return out, err
	}
	c, err := s.studio.GetCharacter(ctx, characterID)
	if err != nil {
		return out, err
	}
	if c.ProjectID != projectID {
		return out, studio.ErrNotFound
	}
	hash, err := referenceStamp(ctx, s.db, c)
	if err != nil {
		return out, err
	}
	out.ReferenceFingerprint = hash
	out.References = len(c.Refs)
	_, out.CanScore = s.workerFor(ctx, "character.score")
	out.TrainingModel, out.CanTrain = s.workerFor(ctx, "character.train")
	unique, err := distinctReferences(ctx, s.db, c)
	if err != nil {
		return out, err
	}
	out.CanTrain = out.CanTrain && unique >= 3
	out.CanScore = out.CanScore && len(c.Refs) > 0
	// Only evidence for current raster assets and this current bible participates.
	method, err := s.currentAssessmentMethod(ctx, projectID, characterID, hash)
	if err != nil {
		return out, err
	}
	query := ` FROM layers l JOIN panels pn ON pn.id=l.panel_id JOIN pages pg ON pg.id=pn.page_id LEFT JOIN character_assessments a ON a.asset_id=l.asset_id AND a.character_id=l.character_id AND a.reference_hash=? AND a.method=? WHERE pg.project_id=? AND l.character_id=? AND l.kind='character' AND l.asset_id IS NOT NULL`
	args := []any{hash, method, projectID, characterID}
	if chapterID != "" {
		chapter, err := s.studio.GetChapter(ctx, chapterID)
		if err != nil || chapter.ProjectID != projectID {
			return out, studio.ErrNotFound
		}
		query += " AND pg.chapter_id=?"
		args = append(args, chapterID)
	}
	var totals struct {
		N      int             `db:"n"`
		Scored int             `db:"scored"`
		Outfit int             `db:"outfit"`
		Score  sql.NullFloat64 `db:"score"`
	}
	err = s.db.Get(ctx, &totals, `SELECT COUNT(*) AS n, COUNT(a.id) AS scored, COUNT(a.outfit_score) AS outfit, AVG(CASE WHEN a.outfit_score IS NOT NULL AND a.outfit_score<a.identity_score THEN a.outfit_score ELSE a.identity_score END) AS score`+query, args...)
	if err != nil {
		return out, err
	}
	out.Panels = totals.Scored
	out.Unscored = totals.N - totals.Scored
	out.OutfitPending = totals.Scored - totals.Outfit
	if totals.Score.Valid {
		v := totals.Score.Float64
		out.Score = &v
		out.Status = "assessed"
		out.Method = method
	}
	// C3's designed bars show up to twelve actual current measurements,
	// oldest to newest. Never synthesize or pad missing measurements.
	if err := s.db.Select(ctx, &out.RecentScores, `SELECT CASE WHEN a.outfit_score IS NOT NULL AND a.outfit_score<a.identity_score THEN a.outfit_score ELSE a.identity_score END`+query+` AND a.id IS NOT NULL ORDER BY a.created_at DESC,a.id DESC LIMIT 12`, args...); err != nil {
		return out, err
	}
	slices.Reverse(out.RecentScores)
	var drift []struct {
		PageID      string          `db:"page_id"`
		PanelID     string          `db:"panel_id"`
		LayerID     string          `db:"layer_id"`
		Version     int             `db:"version"`
		ChapterID   string          `db:"chapter_id"`
		PageNumber  int             `db:"page_number"`
		PanelNumber int             `db:"panel_number"`
		AssetID     string          `db:"asset_id"`
		Identity    float64         `db:"identity"`
		Outfit      sql.NullFloat64 `db:"outfit"`
		Locked      bool            `db:"locked"`
	}
	err = s.db.Select(ctx, &drift, `SELECT pg.id AS page_id,pn.id AS panel_id,l.id AS layer_id,pg.version,pg.chapter_id,pg.number AS page_number,pn.order_idx+1 AS panel_number,l.asset_id,a.identity_score AS identity,a.outfit_score AS outfit,(pg.locked OR pn.locked OR l.locked) AS locked`+query+` AND (a.identity_score<? OR a.outfit_score<?) ORDER BY pg.chapter_id,pg.number,pn.order_idx,l.id LIMIT 100`, append(args, LockThreshold, LockThreshold)...)
	if err != nil {
		return out, err
	}
	if err := s.db.Get(ctx, &out.DriftTotal, `SELECT COUNT(*)`+query+` AND (a.identity_score<? OR a.outfit_score<?)`, append(args, LockThreshold, LockThreshold)...); err != nil {
		return out, err
	}
	for _, r := range drift {
		d := DriftPanel{RepairTarget: RepairTarget{r.PageID, r.PanelID, r.LayerID, r.Version}, ChapterID: r.ChapterID, PageNumber: r.PageNumber, PanelNumber: r.PanelNumber, AssetID: r.AssetID, Identity: r.Identity, Score: r.Identity, Locked: r.Locked, URL: s.assets.URLFor(ctx, r.AssetID, 15*time.Minute)}
		if r.Outfit.Valid {
			v := r.Outfit.Float64
			d.Outfit = &v
			d.Score = min(d.Score, v)
		}
		out.Drift = append(out.Drift, d)
	}
	list, err := s.queue.List(ctx, jobs.Filter{ProjectID: projectID, Types: []string{JobTrain, JobScore}, Limit: 100})
	if err != nil {
		return out, err
	}
	for _, j := range list {
		var p lockPayload
		if j.DecodeInput(&p) == nil && p.CharacterID == characterID && p.ReferenceHash == hash {
			if j.State == jobs.StateQueued || j.State == jobs.StateRunning {
				out.Status = "training"
				if j.Type == JobScore {
					out.Status = "scoring"
				}
				out.JobID = j.ID
				out.Progress = j.Progress
				out.CanScore = false
				out.TrainingJobID = j.ID
				out.TrainingProgress = j.Progress
				out.CanTrain = false
			} else if j.State == jobs.StateFailed || j.State == jobs.StateCanceled {
				out.Status = j.State
			}
			break
		}
	}
	if c.LoraAssetID != "" {
		if a, err := s.assets.Get(ctx, c.LoraAssetID); err == nil {
			var meta struct {
				Hash  string `json:"reference_hash"`
				Model string `json:"base_model"`
			}
			if json.Unmarshal(a.Meta, &meta) == nil && meta.Hash == hash && meta.Model != "" && out.JobID == "" {
				out.Status = "ready"
				out.TrainingModel = meta.Model
			}
		}
	}
	if actor.TokenID != "" || s.studio.Authorize(ctx, actor, projectID, studio.ActGenerate) != nil || s.studio.Authorize(ctx, actor, projectID, studio.ActEditArt) != nil {
		out.CanTrain = false
		out.CanScore = false
	}
	return out, nil
}

func validLockMethod(method string) bool {
	for _, prefix := range []string{lockMethod, lockCCIPMethod, lockOutfitMethod, lockMultiviewMethod} {
		if validFeatureMethod(method, prefix) {
			return true
		}
	}
	return false
}

func validFeatureMethod(method, prefix string) bool {
	if !strings.HasPrefix(method, prefix+":") {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(method, prefix+":"))
	return err == nil && len(b) == 32
}

func lockMethodForModel(model string) string {
	if model == lockMultiviewModel {
		return lockMultiviewMethod
	}
	if model == lockOutfitModel {
		return lockOutfitMethod
	}
	if model == lockCCIPModel {
		return lockCCIPMethod
	}
	return lockMethod
}

func distinctReferences(ctx context.Context, q db.Querier, c studio.Character) (int, error) {
	seen := map[string]bool{}
	for _, r := range c.Refs {
		var sha string
		if err := q.Get(ctx, &sha, `SELECT sha256 FROM assets WHERE id=? AND project_id=?`, r.AssetID, c.ProjectID); err != nil {
			return 0, err
		}
		seen[sha] = true
	}
	return len(seen), nil
}
