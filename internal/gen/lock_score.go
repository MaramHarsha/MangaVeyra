package gen

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strconv"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

type lockAssessment struct {
	ProjectID, CharacterID, AssetID, ReferenceHash, Method, JobID string
	Identity                                                      float64
	Outfit                                                        *float64
}

func fmtIndex(prefix string, n int) string { return prefix + strconv.Itoa(n) }
func (s *service) assessPixels(ctx context.Context, run *jobs.Run, c studio.Character, hash string, ids []string, pixels [][]byte, scope jobs.Scope, slot string) ([]lockAssessment, error) {
	if len(ids) == 0 || len(ids) != len(pixels) || len(ids) > 8 || len(c.Refs) == 0 {
		return nil, studio.ErrInvalid
	}
	if _, ok := s.workerFor(ctx, "character.score"); !ok {
		return nil, ErrDeferredFeature
	}
	files := map[string][]byte{}
	for i, r := range scoringRefs(c.Refs) {
		if i >= 4 {
			break
		}
		data, _, err := s.readOwned(ctx, c.ProjectID, r.AssetID)
		if err != nil {
			return nil, err
		}
		files[fmtIndex("reference_", i)] = data
	}
	for _, outfit := range c.Outfits {
		if outfit.IsDefault && outfit.AssetID != "" {
			data, _, err := s.readOwned(ctx, c.ProjectID, outfit.AssetID)
			if err != nil {
				return nil, err
			}
			files["outfit_0"] = data
			break
		}
	}
	for _, r := range c.Refs {
		if r.Kind == "outfit" && files["outfit_0"] == nil {
			data, _, err := s.readOwned(ctx, c.ProjectID, r.AssetID)
			if err != nil {
				return nil, err
			}
			files["outfit_0"] = data
			break
		}
	}
	for i, data := range pixels {
		files[fmtIndex("candidate_", i)] = data
	}
	child, err := s.runLockWorker(ctx, run, "character.score", lockModel, slot, scope, map[string]any{"threshold": LockThreshold}, files)
	if child != nil {
		run.AddActual(child.Actual)
	}
	if err != nil {
		return nil, err
	}
	var out struct {
		Scores  []float64  `json:"scores"`
		Outfits []*float64 `json:"outfit_scores"`
		Method  string     `json:"method"`
	}
	if json.Unmarshal(child.Output, &out) != nil || !validLockMethod(out.Method) || len(out.Scores) != len(ids) || len(out.Outfits) != len(ids) {
		return nil, studio.ErrInvalid
	}
	found := make([]lockAssessment, 0, len(ids))
	for i, identity := range out.Scores {
		outfit := out.Outfits[i]
		if !finiteScore(identity) || outfit != nil && !finiteScore(*outfit) || files["outfit_0"] != nil && outfit == nil || files["outfit_0"] == nil && outfit != nil {
			return nil, studio.ErrInvalid
		}
		found = append(found, lockAssessment{ProjectID: c.ProjectID, CharacterID: c.ID, AssetID: ids[i], ReferenceHash: hash, Method: out.Method, JobID: run.Job.ID, Identity: identity, Outfit: outfit})
	}
	return found, nil
}

// The generation sheet may contain several views and expression close-ups.
// Compare a single appearance to kept individual body views when available;
// embedding an entire montage introduces layout similarity into the score.
// Generation still receives the complete sheet first (ADR 0009).
func scoringRefs(refs []studio.CharacterRef) []studio.CharacterRef {
	var body []studio.CharacterRef
	seen := map[string]bool{}
	for _, r := range orderedRefs(refs) {
		if r.Kind != "front" && r.Kind != "side" && r.Kind != "back" {
			continue
		}
		if !seen[r.AssetID] {
			body = append(body, r)
			seen[r.AssetID] = true
		}
	}
	if len(body) > 0 {
		return body
	}
	for _, r := range orderedRefs(refs) {
		if r.Kind != "outfit" && !seen[r.AssetID] {
			body = append(body, r)
			seen[r.AssetID] = true
		}
	}
	return body
}
func finiteScore(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1 }
func (s *service) saveAssessmentsTx(ctx context.Context, tx *db.Tx, all []lockAssessment) error {
	for _, a := range all {
		if !validLockMethod(a.Method) || !finiteScore(a.Identity) || a.Outfit != nil && !finiteScore(*a.Outfit) {
			return studio.ErrInvalid
		}
		c, err := s.studio.GetCharacterTx(ctx, tx, a.CharacterID)
		if err != nil {
			return err
		}
		hash, err := referenceStamp(ctx, tx, c)
		if err != nil {
			return err
		}
		if hash != a.ReferenceHash || c.ProjectID != a.ProjectID {
			return studio.ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO character_assessments(id,project_id,character_id,asset_id,reference_hash,method,identity_score,outfit_score,job_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(character_id,asset_id,reference_hash) DO UPDATE SET method=excluded.method,identity_score=excluded.identity_score,outfit_score=excluded.outfit_score,job_id=excluded.job_id,created_at=excluded.created_at`, db.NewID(), a.ProjectID, a.CharacterID, a.AssetID, a.ReferenceHash, a.Method, a.Identity, a.Outfit, a.JobID, db.Now())
		if err != nil {
			return err
		}
	}
	return nil
}
func assessmentScore(a lockAssessment) *float64 {
	n := a.Identity
	if a.Outfit != nil {
		n = min(n, *a.Outfit)
	}
	return &n
}

// WithLockScores decorates a read-only document. Persisted legacy percentages
// are not evidence: only current asset/reference/method matches reach the UI.
func (s *service) WithLockScores(ctx context.Context, actor studio.Actor, p *pagedoc.Page) error {
	if err := s.studio.Authorize(ctx, actor, p.ProjectID, studio.ActRead); err != nil {
		return err
	}
	hashes := map[string]string{}
	for i := range p.Panels {
		for j := range p.Panels[i].Layers {
			l := &p.Panels[i].Layers[j]
			l.LockScore = nil
			if l.Kind != pagedoc.LayerCharacter || l.Asset == nil || l.CharacterID == "" {
				continue
			}
			hash, ok := hashes[l.CharacterID]
			if !ok {
				c, err := s.studio.GetCharacter(ctx, l.CharacterID)
				if err != nil {
					return err
				}
				if c.ProjectID != p.ProjectID {
					return studio.ErrForbidden
				}
				hash, err = referenceStamp(ctx, s.db, c)
				if err != nil {
					return err
				}
				hashes[c.ID] = hash
			}
			var row struct {
				Identity float64         `db:"identity_score"`
				Outfit   sql.NullFloat64 `db:"outfit_score"`
				Method   string          `db:"method"`
			}
			err := s.db.Get(ctx, &row, `SELECT identity_score,outfit_score,method FROM character_assessments WHERE project_id=? AND character_id=? AND asset_id=? AND reference_hash=?`, p.ProjectID, l.CharacterID, l.Asset.ID, hash)
			if db.IsNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !validLockMethod(row.Method) || !finiteScore(row.Identity) {
				continue
			}
			score := row.Identity
			if row.Outfit.Valid {
				if !finiteScore(row.Outfit.Float64) {
					continue
				}
				score = min(score, row.Outfit.Float64)
			}
			l.LockScore = &score
		}
	}
	return nil
}
