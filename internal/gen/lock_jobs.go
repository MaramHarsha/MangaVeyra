package gen

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

type scoreTarget struct {
	PageID  string `json:"page_id"`
	PanelID string `json:"panel_id"`
	LayerID string `json:"layer_id"`
	AssetID string `json:"asset_id"`
	Version int    `json:"version"`
}
type lockPayload struct {
	Version           int           `json:"version"`
	Actor             storedActor   `json:"actor"`
	ProjectID         string        `json:"project_id"`
	CharacterID       string        `json:"character_id"`
	ReferenceHash     string        `json:"reference_hash"`
	ExpectedUpdatedAt time.Time     `json:"expected_updated_at"`
	Model             string        `json:"model"`
	Targets           []scoreTarget `json:"targets,omitempty"`
}
type existingLockJob struct{ ID string }

func (e existingLockJob) Error() string { return "Character Lock job already active" }

func (s *service) EnqueueTraining(ctx context.Context, a studio.Actor, p, c string, rev time.Time) (*jobs.Job, error) {
	return s.enqueueLock(ctx, a, p, c, rev, JobTrain)
}
func (s *service) EnqueueScoring(ctx context.Context, a studio.Actor, p, c string, rev time.Time) (*jobs.Job, error) {
	return s.enqueueLock(ctx, a, p, c, rev, JobScore)
}
func (s *service) enqueueLock(ctx context.Context, actor studio.Actor, projectID, characterID string, rev time.Time, kind string) (*jobs.Job, error) {
	if actor.TokenID != "" || !db.ValidID(projectID) || !db.ValidID(characterID) || rev.IsZero() {
		return nil, studio.ErrInvalid
	}
	if _, err := s.resolveImageActor(ctx, actor, projectID, jobs.Cost{}, false); err != nil {
		return nil, err
	}
	c, err := s.studio.GetCharacter(ctx, characterID)
	if err != nil {
		return nil, err
	}
	if c.ProjectID != projectID {
		return nil, studio.ErrNotFound
	}
	if !c.UpdatedAt.Equal(rev) {
		return nil, studio.ErrConflict
	}
	task := "character.score"
	if kind == JobTrain {
		task = "character.train"
	}
	model, available := s.workerFor(ctx, task)
	if !available || len(c.Refs) == 0 || kind == JobTrain && len(c.Refs) < 3 {
		return nil, ErrDeferredFeature
	}
	unique, err := distinctReferences(ctx, s.db, c)
	if err != nil {
		return nil, err
	}
	if kind == JobTrain && unique < 3 {
		return nil, ErrDeferredFeature
	}
	hash, err := referenceStamp(ctx, s.db, c)
	if err != nil {
		return nil, err
	}
	p := lockPayload{Version: 1, Actor: storedActor{actor.UserID, actor.TokenID}, ProjectID: projectID, CharacterID: characterID, ReferenceHash: hash, ExpectedUpdatedAt: rev, Model: model}
	if kind == JobScore {
		var rows []struct {
			PageID  string `db:"page_id"`
			PanelID string `db:"panel_id"`
			LayerID string `db:"layer_id"`
			AssetID string `db:"asset_id"`
			Version int    `db:"version"`
		}
		if err := s.db.Select(ctx, &rows, `SELECT pg.id AS page_id,pn.id AS panel_id,l.id AS layer_id,l.asset_id,pg.version FROM layers l JOIN panels pn ON pn.id=l.panel_id JOIN pages pg ON pg.id=pn.page_id WHERE pg.project_id=? AND l.character_id=? AND l.kind='character' AND l.asset_id IS NOT NULL ORDER BY pg.id,pn.id,l.id LIMIT 1025`, projectID, characterID); err != nil {
			return nil, err
		}
		if len(rows) == 0 || len(rows) > 1024 {
			return nil, studio.ErrInvalid
		}
		for _, r := range rows {
			p.Targets = append(p.Targets, scoreTarget{r.PageID, r.PanelID, r.LayerID, r.AssetID, r.Version})
		}
	}
	j, err := s.queue.Enqueue(ctx, jobs.EnqueueParams{Type: kind, Scope: jobs.Scope{Kind: jobs.ScopeProject, ProjectID: projectID}, UserID: actor.UserID, Executor: jobs.ExecutorGo, ModelID: model, ProviderID: "local-worker", Input: p, MaxAttempts: 1, Guard: func(ctx context.Context, tx *db.Tx) error {
		if _, err := s.checkLockTx(ctx, tx, p); err != nil {
			return err
		}
		var active []struct {
			ID    string `db:"id"`
			User  string `db:"user_id"`
			Input string `db:"input"`
		}
		if err := tx.Select(ctx, &active, `SELECT id,user_id,input FROM jobs WHERE project_id=? AND type IN (?,?) AND state IN ('queued','running') AND cancel_requested=FALSE ORDER BY id DESC LIMIT 1001`, projectID, JobTrain, JobScore); err != nil {
			return err
		}
		if len(active) > 1000 {
			return studio.ErrConflict
		}
		for _, j := range active {
			var old lockPayload
			if json.Unmarshal([]byte(j.Input), &old) == nil && old.CharacterID == characterID {
				if j.User != actor.UserID || old.Model != p.Model || old.ReferenceHash != p.ReferenceHash {
					return studio.ErrConflict
				}
				return existingLockJob{j.ID}
			}
		}
		return nil
	}})
	var existing existingLockJob
	if errors.As(err, &existing) {
		return s.queue.Get(ctx, existing.ID)
	}
	return j, err
}
func (s *service) checkLockTx(ctx context.Context, tx *db.Tx, p lockPayload) (studio.Character, error) {
	c := studio.Character{}
	actor := studio.Actor{UserID: p.Actor.UserID, TokenID: p.Actor.TokenID}
	if err := s.authorizeImageTx(ctx, tx, actor, p.ProjectID); err != nil {
		return c, err
	}
	inst, err := s.settings.InstanceTx(ctx, tx)
	if err != nil {
		return c, err
	}
	if inst.LocalOnly {
		return c, studio.ErrForbidden
	}
	c, err = s.studio.GetCharacterTx(ctx, tx, p.CharacterID)
	if err != nil {
		return c, err
	}
	if c.ProjectID != p.ProjectID || !c.UpdatedAt.Equal(p.ExpectedUpdatedAt) {
		return c, studio.ErrConflict
	}
	hash, err := referenceStamp(ctx, tx, c)
	if err != nil {
		return c, err
	}
	if hash != p.ReferenceHash {
		return c, studio.ErrConflict
	}
	for _, target := range p.Targets {
		page, err := s.pages.GetPageTx(ctx, tx, target.PageID)
		if err != nil {
			return c, err
		}
		if page.ProjectID != p.ProjectID || page.Version != target.Version {
			return c, studio.ErrConflict
		}
		pn := page.Panel(target.PanelID)
		if pn == nil {
			return c, studio.ErrNotFound
		}
		l := pn.Layer(target.LayerID)
		if l == nil || l.Kind != pagedoc.LayerCharacter || l.CharacterID != p.CharacterID || l.Asset == nil || l.Asset.ID != target.AssetID {
			return c, studio.ErrConflict
		}
		if err := s.studio.CheckScopeWritableTx(ctx, tx, jobs.Scope{Kind: jobs.ScopeLayer, ProjectID: p.ProjectID, PanelID: pn.ID, LayerID: l.ID}); err != nil {
			return c, err
		}
	}
	return c, nil
}
func (s *service) handleLock(ctx context.Context, run *jobs.Run) error {
	var p lockPayload
	j := run.Job
	if _, err := strictDecodeJSON(j.Input, 1<<20, &p); err != nil || p.Version != 1 || p.Actor.UserID != j.UserID || p.Actor.TokenID != "" || p.ProjectID != j.Scope.ProjectID || j.Scope.Kind != jobs.ScopeProject || !db.ValidID(p.CharacterID) || len(p.ReferenceHash) != 64 || j.ModelID != p.Model || len(p.Targets) > 1024 || j.Type != JobTrain && j.Type != JobScore || j.Type == JobScore && len(p.Targets) == 0 {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	var c studio.Character
	if err := s.db.WithTx(ctx, func(tx *db.Tx) error { var err error; c, err = s.checkLockTx(ctx, tx, p); return err }); err != nil {
		return mapImageJobError(ctx, err)
	}
	if j.Type == JobTrain {
		return s.trainCharacter(ctx, run, p, c)
	}
	assessments := []lockAssessment{}
	for i := 0; i < len(p.Targets); i += 8 {
		end := min(len(p.Targets), i+8)
		pixels := [][]byte{}
		ids := []string{}
		for _, target := range p.Targets[i:end] {
			data, _, err := s.readOwned(ctx, p.ProjectID, target.AssetID)
			if err != nil {
				return mapImageJobError(ctx, err)
			}
			pixels = append(pixels, data)
			ids = append(ids, target.AssetID)
		}
		found, err := s.assessPixels(ctx, run, c, p.ReferenceHash, ids, pixels, j.Scope, "score-"+p.Targets[i].LayerID, p.Model)
		if err != nil {
			return mapImageJobError(ctx, err)
		}
		if len(assessments) > 0 && found[0].Method != assessments[0].Method {
			return mapImageJobError(ctx, studio.ErrConflict)
		}
		assessments = append(assessments, found...)
		if err := run.Progress(ctx, float64(end)/float64(len(p.Targets)), end, len(p.Targets), "Assessing Character Lock"); err != nil {
			return mapImageJobError(ctx, err)
		}
	}
	err := run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
		if _, err := s.checkLockTx(ctx, tx, p); err != nil {
			return nil, err
		}
		if err := s.saveAssessmentsTx(ctx, tx, assessments); err != nil {
			return nil, err
		}
		return map[string]any{"character_id": c.ID, "assessed": len(assessments), "method": assessments[0].Method}, nil
	})
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	return nil
}
func (s *service) trainCharacter(ctx context.Context, run *jobs.Run, p lockPayload, c studio.Character) error {
	files := map[string][]byte{}
	seenRefs := map[string]bool{}
	for _, r := range orderedRefs(c.Refs) {
		if len(files) >= 20 {
			break
		}
		data, asset, err := s.readOwned(ctx, p.ProjectID, r.AssetID)
		if err != nil {
			return mapImageJobError(ctx, err)
		}
		if seenRefs[asset.SHA256] {
			continue
		}
		seenRefs[asset.SHA256] = true
		files[fmtIndex("ref_", len(files))] = data
	}
	trigger := "mv_" + strings.ToLower(c.ID[:10])
	child, err := s.runLockWorker(ctx, run, "character.train", p.Model, "train", run.Job.Scope, map[string]any{"trigger": trigger, "steps": 1000, "rank": 16, "base_model": p.Model, "seed": seedFor(run.Job.ID, 0)}, files)
	if child != nil {
		run.AddActual(child.Actual)
	}
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	var out struct {
		LoRA struct {
			Key string `json:"key"`
		} `json:"lora"`
	}
	if json.Unmarshal(child.Output, &out) != nil || !strings.HasPrefix(out.LoRA.Key, "jobs/"+child.ID+"/") {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	var assetID string
	if err := s.db.Get(ctx, &assetID, `SELECT id FROM assets WHERE key=? AND owner_id=? AND project_id=? AND source='worker'`, out.LoRA.Key, run.Job.UserID, p.ProjectID); err != nil {
		return mapImageJobError(ctx, err)
	}
	data, a, err := s.assets.Read(ctx, assetID, assets.MaxBytes)
	if err != nil || !realLoRA(data) {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	var meta struct {
		Model       string `json:"base_model"`
		Trainer     string `json:"trainer"`
		Placeholder bool   `json:"placeholder"`
	}
	if json.Unmarshal(a.Meta, &meta) != nil || meta.Model != p.Model || meta.Placeholder || meta.Trainer != "peft-sdxl" && meta.Trainer != "external" {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	staged, err := s.assets.Stage(ctx, assets.SaveParams{OwnerID: run.Job.UserID, ProjectID: p.ProjectID, Kind: "character_lora", Source: assets.SourceAI, Data: data, ContentType: "application/octet-stream", Meta: map[string]any{"reference_hash": p.ReferenceHash, "base_model": p.Model, "trigger": trigger, "trainer": meta.Trainer}})
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	defer s.assets.DiscardStaged(context.WithoutCancel(ctx), staged)
	err = run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
		if _, err := s.checkLockTx(ctx, tx, p); err != nil {
			return nil, err
		}
		a, err := s.assets.CommitStagedTx(ctx, tx, staged)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE characters SET lora_asset_id=?,trigger_word=?,lock_status='ready',lock_score=NULL,updated_at=? WHERE id=?`, a.ID, trigger, db.Now(), c.ID)
		return map[string]string{"character_id": c.ID, "lora_asset_id": a.ID, "base_model": p.Model}, err
	})
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	return nil
}
func realLoRA(data []byte) bool {
	if len(data) < 16 {
		return false
	}
	n := binary.LittleEndian.Uint64(data[:8])
	if n < 2 || n > 1<<20 || n > uint64(len(data)-8) {
		return false
	}
	var header map[string]json.RawMessage
	if json.Unmarshal(data[8:8+n], &header) != nil {
		return false
	}
	tensors := 0
	pairs := map[string]int{}
	var ranges [][2]uint64
	for name, raw := range header {
		if name == "__metadata__" {
			var meta map[string]string
			if json.Unmarshal(raw, &meta) != nil || meta["mangaveyra.mock"] == "true" {
				return false
			}
			continue
		}
		if strings.Contains(strings.ToLower(name), "mock") {
			return false
		}
		var t struct {
			Dtype   string   `json:"dtype"`
			Shape   []int64  `json:"shape"`
			Offsets []uint64 `json:"data_offsets"`
		}
		if json.Unmarshal(raw, &t) != nil || len(t.Shape) > 8 || len(t.Offsets) != 2 || t.Offsets[1] <= t.Offsets[0] || t.Offsets[1] > uint64(len(data))-8-n {
			return false
		}
		width := map[string]int64{"F32": 4, "F16": 2, "BF16": 2}[t.Dtype]
		if width == 0 {
			return false
		}
		size := width
		for _, dim := range t.Shape {
			if dim < 1 || dim > 1<<20 || size > math.MaxInt64/dim {
				return false
			}
			size *= dim
		}
		if uint64(size) != t.Offsets[1]-t.Offsets[0] {
			return false
		}
		ranges = append(ranges, [2]uint64{t.Offsets[0], t.Offsets[1]})
		lower := strings.ToLower(name)
		for _, pair := range []struct{ a, b string }{{"lora_a", "lora_b"}, {"lora.down", "lora.up"}, {"lora_down", "lora_up"}} {
			if strings.Contains(lower, pair.a) {
				pairs[strings.Replace(lower, pair.a, "lora_pair", 1)] |= 1
			}
			if strings.Contains(lower, pair.b) {
				pairs[strings.Replace(lower, pair.b, "lora_pair", 1)] |= 2
			}
		}
		tensors++
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	for i := 1; i < len(ranges); i++ {
		if ranges[i][0] < ranges[i-1][1] {
			return false
		}
	}
	if tensors < 2 || len(pairs) == 0 {
		return false
	}
	for _, bits := range pairs {
		if bits != 3 {
			return false
		}
	}
	return true
}
