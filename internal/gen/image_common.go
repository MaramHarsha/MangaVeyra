package gen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

const imagePayloadVersion = 1
const imageAttempts = 3
const maxComponents = 15

type storedActor struct {
	UserID  string `json:"user_id"`
	TokenID string `json:"token_id,omitempty"`
}
type component struct {
	ReferenceHash string `json:"reference_hash,omitempty"`
	LayerID       string `json:"layer_id"`
	Kind          string `json:"kind"`
	CharacterID   string `json:"character_id,omitempty"`
	Name          string `json:"name"`
	Prompt        string `json:"prompt"`
	// ADR 0009 fields. OwnRefs means References lists exactly this
	// component's reference assets (possibly none); payloads queued before
	// it send every job reference, as they always did.
	OwnRefs    bool     `json:"own_refs,omitempty"`
	References []string `json:"references,omitempty"`
	Background string   `json:"background,omitempty"`
	Negative   string   `json:"negative,omitempty"`
}
type imagePayload struct {
	Version           int             `json:"version"`
	Request           json.RawMessage `json:"request"`
	Actor             storedActor     `json:"actor"`
	Plan              ai.ImagePlan    `json:"plan"`
	Scope             jobs.Scope      `json:"scope"`
	PageID            string          `json:"page_id,omitempty"`
	PanelID           string          `json:"panel_id,omitempty"`
	CharacterID       string          `json:"character_id,omitempty"`
	ExpectedVersion   int             `json:"expected_version,omitempty"`
	ExpectedUpdatedAt time.Time       `json:"expected_updated_at,omitempty"`
	TargetHash        string          `json:"target_hash,omitempty"`
	InitAssetID       string          `json:"init_asset_id,omitempty"`
	MaskAssetID       string          `json:"mask_asset_id,omitempty"`
	References        []string        `json:"references"`
	Components        []component     `json:"components"`
	Style             string          `json:"style"`
	PanelType         string          `json:"panel_type,omitempty"`
	BatchID           string          `json:"batch_id,omitempty"`
	ContextPages      []contextPage   `json:"context_pages,omitempty"`
	Pose              map[string]any  `json:"pose,omitempty"`
	SketchAssetID     string          `json:"sketch_asset_id,omitempty"`
	Strength          float64         `json:"strength,omitempty"`
}

type contextPage struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}
type imageOutput struct {
	Candidates   []Candidate `json:"candidates"`
	AppliedIndex *int        `json:"applied_index,omitempty"`
	PageID       string      `json:"page_id,omitempty"`
	CharacterID  string      `json:"character_id,omitempty"`
	Version      int         `json:"version,omitempty"`
}

func changeFor(actor studio.Actor, jobID, action string) pagestore.Change {
	kind := "user"
	if actor.TokenID != "" {
		kind = "agent"
	}
	return pagestore.Change{ActorKind: kind, ActorID: actor.UserID, TokenID: actor.TokenID, JobID: jobID, Action: action, Summary: action}
}
func imageActor(p imagePayload) studio.Actor {
	return studio.Actor{UserID: p.Actor.UserID, TokenID: p.Actor.TokenID}
}
func (s *service) resolveImageActor(ctx context.Context, actor studio.Actor, projectID string, cost jobs.Cost, remote bool) (studio.Actor, error) {
	if !db.ValidID(actor.UserID) {
		return actor, studio.ErrForbidden
	}
	if err := s.studio.Authorize(ctx, actor, projectID, studio.ActGenerate); err != nil {
		return actor, err
	}
	if err := s.studio.Authorize(ctx, actor, projectID, studio.ActEditArt); err != nil {
		return actor, err
	}
	if actor.TokenID != "" {
		tok, user, err := s.auth.ResolveStoredToken(ctx, actor.TokenID)
		if err != nil || tok == nil || user == nil || tok.UserID != actor.UserID || !tok.HasScope("generate") || !tok.HasScope("projects:write") || !tok.AllowsProject(projectID) {
			return actor, studio.ErrForbidden
		}
		if remote && tok.ApproveOverUSD != nil {
			over, err := approvalExceeded(cost.USD, *tok.ApproveOverUSD)
			if err != nil || over {
				return actor, approvalGenerateError()
			}
		}
	}
	actor.SiteRole = ""
	return actor, nil
}
func (s *service) authorizeImageTx(ctx context.Context, tx *db.Tx, actor studio.Actor, projectID string) error {
	for _, action := range []studio.Action{studio.ActGenerate, studio.ActEditArt} {
		if err := s.studio.AuthorizeTx(ctx, tx, actor, projectID, action); err != nil {
			return err
		}
	}
	return nil
}
func hashPanel(p pagedoc.Panel) string {
	p.Points = append([]pagedoc.Point(nil), p.Points...)
	for i := range p.Points {
		p.Points[i].X = math.Round(p.Points[i].X*1e6) / 1e6
		p.Points[i].Y = math.Round(p.Points[i].Y*1e6) / 1e6
	}
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func lockSQL(tx *db.Tx) string {
	if tx.Dialect() == db.Postgres {
		return " FOR UPDATE"
	}
	return ""
}
func validateImagePlan(p ai.ImagePlan) error {
	n := p.Needs
	if !boundedIdentifier(p.PinProviderID) || !boundedIdentifier(p.PinModelID) || p.PinProviderID != "" && p.PinProviderID != p.ProviderID || p.PinModelID != "" && p.PinModelID != p.ModelID || p.RoutingMode == ai.ModeLocalOnly && p.ProviderID != ai.PlaceholderID || p.RoutingMode == ai.ModeFreeOnly && p.Remote {
		return studio.ErrInvalid
	}
	if p.Task != ai.TaskPanel && p.Task != ai.TaskCharacterSheet && p.Task != ai.TaskRegionEdit {
		return studio.ErrInvalid
	}
	if !validRoutingMode(p.RoutingMode) || p.ProviderID == "" || p.ModelID == "" || !boundedIdentifier(p.ProviderID) || !boundedIdentifier(p.ModelID) || !validCost(p.Estimate) || p.Estimate.USD > maxPlanUSD || p.Estimate.GPUSeconds > maxPlanGPUSeconds || n.Width < 64 || n.Height < 64 || n.Width > 4096 || n.Height > 4096 || n.Count < 1 || n.Count > 4 || n.References < 0 || n.References > 32 || n.Mask && !n.Edit || len(p.Decision) == 0 || len(p.Decision) > maxDecisionBytes || !json.Valid(p.Decision) {
		return studio.ErrInvalid
	}
	if p.ProviderID == ai.PlaceholderID || p.ModelID == ai.PlaceholderArt {
		if p.ProviderID != ai.PlaceholderID || p.ModelID != ai.PlaceholderArt || p.Remote || p.Executor != jobs.ExecutorGo {
			return studio.ErrInvalid
		}
	} else if p.ProviderID == "local-worker" {
		if p.Remote || p.Executor != jobs.ExecutorWorker {
			return studio.ErrInvalid
		}
	} else if !p.Remote || p.Executor != jobs.ExecutorGo || p.Estimate.USD <= 0 {
		return studio.ErrInvalid
	}
	return nil
}
func decodeImagePayload(raw json.RawMessage) (imagePayload, error) {
	var p imagePayload
	if _, err := strictDecodeJSON(raw, 1<<20, &p); err != nil {
		return p, studio.ErrInvalid
	}
	if p.Version != imagePayloadVersion || !db.ValidID(p.Actor.UserID) || (p.Actor.TokenID != "" && !db.ValidID(p.Actor.TokenID)) || p.Scope.Validate() != nil || len(p.Components) < 1 || len(p.Components) > maxComponents || validateImagePlan(p.Plan) != nil || imageRefCount(p) != p.Plan.Needs.References || p.Plan.Needs.Edit != (p.InitAssetID != "" || p.SketchAssetID != "") || p.Plan.Needs.Mask != (p.MaskAssetID != "") {
		return p, studio.ErrInvalid
	}
	if err := validatePayloadTarget(p); err != nil {
		return p, err
	}
	return p, nil
}
func currentImagePlan(p imagePayload, j *jobs.Job) ai.ImagePlan {
	plan := p.Plan
	plan.ProviderID = j.ProviderID
	plan.ModelID = j.ModelID
	plan.Decision = j.Decision
	plan.Remote = j.ProviderID != ai.PlaceholderID && j.ProviderID != "local-worker"
	plan.Executor = jobs.ExecutorGo
	if j.ProviderID == "local-worker" {
		plan.Executor = jobs.ExecutorWorker
	}
	return plan
}
func (s *service) failoverImage(ctx context.Context, j *jobs.Job, failure jobs.JobError) (*jobs.Reroute, error) {
	if s.fallback == nil || j == nil || j.MaxAttempts != imageAttempts || j.RoundAttempt() >= imageAttempts || j.Type != JobPanel && j.Type != JobRegionEdit && j.Type != JobCharacterSheet {
		return nil, nil
	}
	p, err := decodeImagePayload(j.Input)
	if err != nil {
		return nil, nil
	}
	if p.BatchID != j.ParentID || stringJSON(p.Scope) != stringJSON(j.Scope) {
		return nil, nil
	}
	plan := currentImagePlan(p, j)
	if validateImagePlan(plan) != nil {
		return nil, nil
	}
	rr, err := s.fallback(ctx, j, plan, failure)
	if err != nil || rr == nil {
		return rr, err
	}
	remote := rr.ProviderID != ai.PlaceholderID && rr.ProviderID != "local-worker"
	if !p.Plan.Remote && remote || rr.Executor != "" && rr.Executor != jobs.ExecutorGo {
		return nil, nil
	}
	quoted, err := s.images.PlanImage(ctx, ai.ImagePlanRequest{Task: p.Plan.Task, ActorID: j.UserID, ProjectID: j.Scope.ProjectID, RoutingMode: p.Plan.RoutingMode, Needs: p.Plan.Needs, PublicSeries: p.Plan.PublicSeries, PinProviderID: rr.ProviderID, PinModelID: rr.ModelID})
	if err != nil || validateImagePlan(quoted) != nil || quoted.ProviderID != rr.ProviderID || quoted.ModelID != rr.ModelID || quoted.Needs != p.Plan.Needs || quoted.Estimate.USD > p.Plan.Estimate.USD+.0000001 || quoted.Estimate.GPUSeconds > p.Plan.Estimate.GPUSeconds+.0000001 {
		return nil, nil
	}
	return rr, nil
}
func stringJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func (s *service) checkImageTx(ctx context.Context, tx *db.Tx, p imagePayload, actor studio.Actor) error {
	if err := s.authorizeImageTx(ctx, tx, actor, p.Scope.ProjectID); err != nil {
		return err
	}
	if err := s.studio.CheckScopeWritableTx(ctx, tx, p.Scope); err != nil {
		return err
	}
	for _, neighbour := range p.ContextPages {
		var row struct {
			Version   int    `db:"version"`
			ChapterID string `db:"chapter_id"`
			ProjectID string `db:"project_id"`
		}
		if err := tx.Get(ctx, &row, `SELECT version, chapter_id, project_id FROM pages WHERE id=?`, neighbour.ID); err != nil {
			return studio.ErrConflict
		}
		var chapterID string
		if err := tx.Get(ctx, &chapterID, `SELECT chapter_id FROM pages WHERE id=?`, p.PageID); err != nil {
			return err
		}
		if row.Version != neighbour.Version || row.ChapterID != chapterID || row.ProjectID != p.Scope.ProjectID {
			return studio.ErrConflict
		}
	}
	if p.CharacterID != "" {
		c, err := s.studio.GetCharacterTx(ctx, tx, p.CharacterID)
		if err != nil {
			return err
		}
		if c.ProjectID != p.Scope.ProjectID {
			return studio.ErrForbidden
		}
		if !c.UpdatedAt.UTC().Equal(p.ExpectedUpdatedAt.UTC()) {
			return studio.ErrConflict
		}
		return nil
	}
	page, err := s.pages.GetPageTx(ctx, tx, p.PageID)
	if err != nil {
		return err
	}
	if page.ProjectID != p.Scope.ProjectID {
		return studio.ErrForbidden
	}
	pn := page.Panel(p.PanelID)
	if pn == nil || hashPanel(*pn) != p.TargetHash {
		return pagestore.ErrVersion
	}
	for _, c := range p.Components {
		if c.ReferenceHash != "" {
			character, err := s.studio.GetCharacterTx(ctx, tx, c.CharacterID)
			if err != nil {
				return err
			}
			hash, err := referenceStamp(ctx, tx, character)
			if err != nil {
				return err
			}
			if hash != c.ReferenceHash {
				return studio.ErrConflict
			}
		}
		if l := pn.Layer(c.LayerID); l != nil && l.Locked {
			return pagestore.ErrLocked
		}
	}
	if page.Version == p.ExpectedVersion {
		return nil
	}
	if p.BatchID == "" || page.Version < p.ExpectedVersion {
		return pagestore.ErrVersion
	}
	// Only successful sibling jobs of this internal pages batch may account
	// for intervening revisions. Human, lock, lettering and unrelated AI edits
	// make the entire batch candidate stale.
	var rows []struct {
		JobID    string `db:"job_id"`
		Number   int    `db:"number"`
		Type     string `db:"type"`
		ParentID string `db:"parent_id"`
		State    string `db:"state"`
	}
	err = tx.Select(ctx, &rows, `SELECT COALESCE(v.job_id,'') AS job_id,v.number,COALESCE(j.type,'') AS type,COALESCE(j.parent_id,'') AS parent_id,COALESCE(j.state,'') AS state FROM versions v LEFT JOIN jobs j ON j.id=v.job_id WHERE v.page_id=? AND v.number>? AND v.number<=? ORDER BY v.number`, page.ID, p.ExpectedVersion, page.Version)
	if err != nil {
		return err
	}
	if len(rows) != page.Version-p.ExpectedVersion {
		return pagestore.ErrVersion
	}
	for i, r := range rows {
		if r.Number != p.ExpectedVersion+i+1 || r.Type != JobPanel || r.ParentID != p.BatchID || r.State != jobs.StateDone {
			return pagestore.ErrVersion
		}
	}
	return nil
}
func (s *service) enqueueImage(ctx context.Context, p imagePayload) (*jobs.Job, error) {
	cost, err := scaleCost(p.Plan.Estimate, imageAttempts*len(p.Components))
	if err != nil {
		return nil, studio.ErrInvalid
	}
	actor, err := s.resolveImageActor(ctx, imageActor(p), p.Scope.ProjectID, cost, p.Plan.Remote)
	if err != nil {
		return nil, err
	}
	return s.queue.Enqueue(ctx, s.imageParams(p, actor, cost))
}
func (s *service) imageParams(p imagePayload, actor studio.Actor, cost jobs.Cost) jobs.EnqueueParams {
	priority := 10
	if p.BatchID != "" {
		priority = -10
	}
	return jobs.EnqueueParams{Priority: priority, Batch: p.BatchID != "", Type: p.Plan.Task, Scope: p.Scope, UserID: actor.UserID, Executor: jobs.ExecutorGo, ProviderID: p.Plan.ProviderID, ModelID: p.Plan.ModelID, Decision: json.RawMessage(p.Plan.Decision), Input: p, Estimate: cost, MaxAttempts: imageAttempts, ParentID: p.BatchID, Guard: func(ctx context.Context, tx *db.Tx) error {
		if err := s.checkImageTx(ctx, tx, p, actor); err != nil {
			return err
		}
		return s.approvalTx(ctx, tx, actor, cost, p.Plan.Remote)
	}}
}
func mapImageJobError(ctx context.Context, err error) error {
	if errors.Is(err, jobs.ErrLeaseLost) {
		return jobs.ErrLeaseLost
	}
	if ctx.Err() != nil || errors.Is(err, jobs.ErrCanceled) {
		return jobs.Permanent(jobs.ErrCodeCanceled, errors.New("Generation canceled."))
	}
	if errors.Is(err, pagestore.ErrLocked) || errors.Is(err, studio.ErrLocked) || errors.Is(err, pagestore.ErrVersion) || errors.Is(err, studio.ErrConflict) || errors.Is(err, studio.ErrForbidden) || errors.Is(err, studio.ErrInvalid) || errors.Is(err, pagestore.ErrInvalid) || errors.Is(err, studio.ErrNotFound) {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("The generation target changed, is locked or is no longer available."))
	}
	if db.IsTransient(err) {
		return jobs.Retryable(jobs.ErrCodeInternal, errors.New("The generation save was interrupted."))
	}
	var ge *ai.GenerateError
	if errors.As(err, &ge) {
		return &jobs.JobError{Code: ge.Code, Message: safeAIMessage(ge.Code, ge.SafeMessage), Retryable: ge.Retryable}
	}
	return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("The image could not be generated or saved."))
}

func (s *service) approvalTx(ctx context.Context, tx *db.Tx, actor studio.Actor, cost jobs.Cost, remote bool) error {
	if actor.TokenID == "" || !remote {
		return nil
	}
	tok, user, err := s.auth.ResolveStoredTokenTx(ctx, tx, actor.TokenID)
	if err != nil || tok == nil || user == nil || user.ID != actor.UserID {
		return studio.ErrForbidden
	}
	if tok.ApproveOverUSD != nil {
		over, err := approvalExceeded(cost.USD, *tok.ApproveOverUSD)
		if err != nil || over {
			return approvalGenerateError()
		}
	}
	return nil
}
