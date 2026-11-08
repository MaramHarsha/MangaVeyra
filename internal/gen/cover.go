package gen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Chapter cover generation (the E1 Publish flow's "Generate cover"). One
// chapter-scoped job makes candidate 3:4 covers; a human keeps one, which
// becomes the project cover (projects.cover_asset_id). Chapters have no cover
// column of their own.

// JobCover is the chapter cover job type, equal to the image router task.
const JobCover = ai.TaskCover

const (
	coverPayloadVersion = 1
	// coverWidth × coverHeight is the 3:4 chapter cover. GPT Image routes
	// request the nearest frozen size (864×1152, ADR 0009) and fit it back.
	coverWidth, coverHeight = 768, 1024
	coverVariations         = 4
	// coverLeads lead ("main") characters appear on a cover, carrying at most
	// coverRefsPerLead kept references each and coverRefs in total.
	coverLeads, coverRefsPerLead, coverRefs = 3, 2, 4
	// coverAssetKind labels stored candidates; the asset source stays "ai".
	coverAssetKind = "cover_candidate"
)

// CoverRequest asks for candidate covers of one chapter.
type CoverRequest struct {
	ProjectID string `json:"project_id"`
	ChapterID string `json:"chapter_id"`
	// Variations is how many candidates to make (1–4, default 4).
	Variations  int    `json:"variations"`
	RoutingMode string `json:"routing_mode,omitempty"`
}

// CoverResult is a cover job's candidates and the one a human kept, if any.
type CoverResult struct {
	Candidates []Candidate `json:"candidates"`
	KeptIndex  *int        `json:"kept_index,omitempty"`
}

// CoverService is chapter cover generation. It is provided separately so the
// frozen Service and ControlService contracts (and their mocks) stay narrow.
// Every method authorizes the actor and fences the chapter like panel art.
type CoverService interface {
	// EnqueueCover queues candidate generation. An identical active request of
	// the same user returns that job with Deduplicated set.
	EnqueueCover(ctx context.Context, actor studio.Actor, req CoverRequest) (*jobs.Job, error)
	// CoverCandidates returns a finished job's candidates with signed URLs.
	CoverCandidates(ctx context.Context, actor studio.Actor, jobID string) (CoverResult, error)
	// KeepCover is the explicit human choice: candidate index becomes the
	// project cover. It returns the kept asset ID.
	KeepCover(ctx context.Context, actor studio.Actor, jobID string, index int) (string, error)
}

var _ CoverService = (*service)(nil)

// Keep conflicts. Both match studio.ErrConflict.
var (
	// ErrCoverKept: a candidate of this job was already kept.
	ErrCoverKept = fmt.Errorf("%w: a cover from this job was already kept", studio.ErrConflict)
	// ErrCoverPending: the job has not finished, so there is nothing to keep.
	ErrCoverPending = fmt.Errorf("%w: the cover job has not finished", studio.ErrConflict)
)

type coverCast struct {
	CharacterID   string `json:"character_id"`
	ReferenceHash string `json:"reference_hash"`
}
type coverRef struct {
	AssetID string `json:"asset_id"`
	Label   string `json:"label"`
}
type coverPayload struct {
	Version    int          `json:"version"`
	Request    CoverRequest `json:"request"`
	Actor      storedActor  `json:"actor"`
	Scope      jobs.Scope   `json:"scope"`
	Plan       ai.ImagePlan `json:"plan"`
	Basis      string       `json:"basis"`
	Cast       []coverCast  `json:"cast"`
	References []coverRef   `json:"references"`
	Prompt     string       `json:"prompt"`
	Negative   string       `json:"negative"`
	Style      string       `json:"style"`
}
type coverOutput struct {
	Candidates []Candidate `json:"candidates"`
	ChapterID  string      `json:"chapter_id"`
	KeptIndex  *int        `json:"kept_index,omitempty"`
	KeptBy     string      `json:"kept_by,omitempty"`
	KeptAt     *time.Time  `json:"kept_at,omitempty"`
}

// coverBasis is everything a cover is derived from. Its fingerprint fences
// the job and the keep: a changed style, chapter title or synopsis, lead
// cast or kept reference makes earlier candidates stale.
type coverBasis struct {
	Prompt, Negative, Style string
	Cast                    []coverCast
	References              []coverRef
	CharacterIDs            []string
	Fingerprint             string
}

// coverBasisTx derives the deterministic cover prompt and references inside
// tx, locking the lead characters (after the project and chapter rows, the
// order every generation path uses).
func (s *service) coverBasisTx(ctx context.Context, tx *db.Tx, projectID, chapterID string) (coverBasis, error) {
	var b coverBasis
	var project struct {
		Style   string `db:"style"`
		Logline string `db:"logline"`
		Premise string `db:"premise"`
	}
	if err := tx.Get(ctx, &project, `SELECT style, logline, premise FROM projects WHERE id = ?`, projectID); err != nil {
		if db.IsNotFound(err) {
			return b, studio.ErrNotFound
		}
		return b, err
	}
	var chapter struct {
		ProjectID string `db:"project_id"`
		Title     string `db:"title"`
		Summary   string `db:"summary"`
	}
	if err := tx.Get(ctx, &chapter, `SELECT project_id, title, summary FROM chapters WHERE id = ?`, chapterID); err != nil {
		if db.IsNotFound(err) {
			return b, studio.ErrNotFound
		}
		return b, err
	}
	if chapter.ProjectID != projectID {
		return b, studio.ErrNotFound
	}
	var leads []string
	if err := tx.Select(ctx, &leads, `SELECT id FROM characters WHERE project_id = ? AND category = 'main' ORDER BY order_idx, id LIMIT ?`, projectID, coverLeads); err != nil {
		return b, err
	}
	cast := []castMember{}
	perLead := [][]coverRef{}
	for _, id := range leads {
		c, err := s.studio.GetCharacterTx(ctx, tx, id)
		if err != nil {
			return b, err
		}
		if c.ProjectID != projectID {
			return b, studio.ErrForbidden
		}
		hash, err := referenceStamp(ctx, tx, c)
		if err != nil {
			return b, err
		}
		b.CharacterIDs = append(b.CharacterIDs, c.ID)
		b.Cast = append(b.Cast, coverCast{CharacterID: c.ID, ReferenceHash: hash})
		cast = append(cast, castMember{Name: c.Name, Look: c.Look, Outfit: defaultOutfit(c)})
		refs := []coverRef{}
		for _, r := range generationRefs(c) {
			if len(refs) == coverRefsPerLead {
				break
			}
			refs = append(refs, coverRef{AssetID: r.AssetID, Label: clip(c.Name, 80) + " · kept reference"})
		}
		perLead = append(perLead, refs)
	}
	// Interleave so a route that sends only its first references still sees
	// every lead's first (sheet-first) reference.
	seen := map[string]bool{}
	for k := 0; k < coverRefsPerLead; k++ {
		for _, refs := range perLead {
			if k < len(refs) && !seen[refs[k].AssetID] && len(b.References) < coverRefs {
				seen[refs[k].AssetID] = true
				b.References = append(b.References, refs[k])
			}
		}
	}
	synopsis := chapter.Summary
	if strings.TrimSpace(synopsis) == "" {
		synopsis = strings.Join(nonEmpty(project.Logline, project.Premise), " ")
	}
	b.Style = project.Style
	b.Prompt = coverPrompt(project.Style, chapter.Title, synopsis, cast, len(b.References) > 0)
	b.Negative = coverNegative(project.Style)
	raw, _ := json.Marshal([]any{b.Prompt, b.Negative, b.Style, b.Cast, b.References})
	sum := sha256.Sum256(raw)
	b.Fingerprint = hex.EncodeToString(sum[:])
	return b, nil
}

// coverPrompt builds the cover prompt from the project style, the chapter
// title and synopsis and the lead cast (ADR 0009 prompt rules apply).
func coverPrompt(style, title, synopsis string, cast []castMember, refs bool) string {
	spec := styleFor(style)
	head := []string{"Original manga chapter cover illustration, portrait 3:4. " + spec.Lines + spec.palette()}
	details := []string{sentence("Story: ", synopsis, 700)}
	for _, c := range cast {
		details = append(details, sentence("Lead character: ", strings.Join(nonEmpty(clip(c.Name, 80), clip(c.Look, 300), clip(c.Outfit, 160)), "; "), 560))
	}
	if refs {
		details = append(details, "Each lead character's face, hair, build and outfit come only from that character's kept references.")
	}
	if len(cast) > 0 {
		details = append(details, "Composition: one striking cover image with the lead characters as the clear focal point, dramatic lighting and depth, filling the whole frame edge to edge on an opaque background; leave calm, empty space near the top for the chapter title, which is added later outside the artwork.")
	} else {
		details = append(details, "Composition: one striking cover image that evokes the chapter with a clear focal point, dramatic lighting and depth, filling the whole frame edge to edge on an opaque background; leave calm, empty space near the top for the chapter title, which is added later outside the artwork.")
	}
	return assemblePrompt(head, "Chapter theme (never write it as text): ", title, details)
}

func coverNegative(style string) string {
	n := negativeArt + ", title text, cover lettering, barcode, frame border, multiple images, collage"
	if styleFor(style).BW {
		n += ", colour, color"
	}
	return clip(n, negativeBytes)
}

func coverDedupeKey(p coverPayload) string {
	return dedupeKey("cover", p.Actor.UserID, p.Scope.ProjectID, p.Scope.ChapterID, p.Basis, p.Request)
}

func decodeCoverPayload(raw json.RawMessage) (coverPayload, error) {
	var p coverPayload
	if _, err := strictDecodeJSON(raw, 1<<20, &p); err != nil {
		return p, studio.ErrInvalid
	}
	n, req, plan := p.Plan.Needs, p.Request, p.Plan
	if p.Version != coverPayloadVersion || !db.ValidID(p.Actor.UserID) || p.Actor.TokenID != "" && !db.ValidID(p.Actor.TokenID) ||
		p.Scope.Validate() != nil || p.Scope.Kind != jobs.ScopeChapter || req.ProjectID != p.Scope.ProjectID || req.ChapterID != p.Scope.ChapterID ||
		req.Variations < 1 || req.Variations > 4 || req.RoutingMode != "" && !validRoutingMode(req.RoutingMode) ||
		validateImagePlan(plan) != nil || plan.Task != ai.TaskCover || n.Width != coverWidth || n.Height != coverHeight || n.Count != req.Variations ||
		n.References != len(p.References) || n.Edit || n.Mask || n.Pose || len(p.Basis) != 64 || len(p.Cast) > coverLeads || len(p.References) > coverRefs ||
		strings.TrimSpace(p.Prompt) == "" || !utf8.ValidString(p.Prompt) || len([]rune(p.Prompt)) > pagedoc.MaxPromptRunes ||
		!utf8.ValidString(p.Negative) || len(p.Negative) > negativeBytes || !utf8.ValidString(p.Style) || len(p.Style) > 256 {
		return p, studio.ErrInvalid
	}
	for _, c := range p.Cast {
		if !db.ValidID(c.CharacterID) || len(c.ReferenceHash) != 64 {
			return p, studio.ErrInvalid
		}
	}
	for _, r := range p.References {
		if !db.ValidID(r.AssetID) || !utf8.ValidString(r.Label) || len(r.Label) > 160 {
			return p, studio.ErrInvalid
		}
	}
	return p, nil
}

func coverActor(p coverPayload) studio.Actor {
	return studio.Actor{UserID: p.Actor.UserID, TokenID: p.Actor.TokenID}
}

// checkCoverTx repeats the panel-art checks for a chapter scope: membership
// and art permissions, the chapter lock and the basis fingerprint.
func (s *service) checkCoverTx(ctx context.Context, tx *db.Tx, p coverPayload, actor studio.Actor) error {
	if err := s.authorizeImageTx(ctx, tx, actor, p.Scope.ProjectID); err != nil {
		return err
	}
	if err := s.studio.CheckScopeWritableTx(ctx, tx, p.Scope); err != nil {
		return err
	}
	basis, err := s.coverBasisTx(ctx, tx, p.Scope.ProjectID, p.Scope.ChapterID)
	if err != nil {
		return err
	}
	if basis.Fingerprint != p.Basis {
		return studio.ErrConflict
	}
	return nil
}

// coverDispatchTx refuses anything but the in-process placeholder while
// local_only is on, read uncached inside the dispatch check. The image
// service checks again before any request leaves the server.
func (s *service) coverDispatchTx(ctx context.Context, tx *db.Tx, plan ai.ImagePlan) error {
	inst, err := s.settings.InstanceTx(ctx, tx)
	if err != nil {
		return err
	}
	if (inst.LocalOnly || plan.RoutingMode == ai.ModeLocalOnly) && plan.ProviderID != ai.PlaceholderID {
		return &ai.GenerateError{Code: ai.CodeLocalOnly, SafeMessage: "Local only mode keeps cover generation on this server."}
	}
	return nil
}

func (s *service) EnqueueCover(ctx context.Context, actor studio.Actor, req CoverRequest) (*jobs.Job, error) {
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.ChapterID) || req.RoutingMode != "" && !validRoutingMode(req.RoutingMode) {
		return nil, studio.ErrInvalid
	}
	if req.Variations == 0 {
		req.Variations = coverVariations
	}
	n, err := countImages(req.Variations)
	if err != nil {
		return nil, err
	}
	req.Variations = n
	if _, err := s.resolveImageActor(ctx, actor, req.ProjectID, jobs.Cost{}, false); err != nil {
		return nil, err
	}
	chapter, err := s.studio.GetChapter(ctx, req.ChapterID)
	if err != nil {
		return nil, err
	}
	if chapter.ProjectID != req.ProjectID {
		return nil, studio.ErrNotFound
	}
	if chapter.Locked {
		return nil, studio.ErrLocked
	}
	var basis coverBasis
	if err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		var err error
		basis, err = s.coverBasisTx(ctx, tx, req.ProjectID, req.ChapterID)
		return err
	}); err != nil {
		return nil, err
	}
	needs := ai.ImageNeeds{Width: coverWidth, Height: coverHeight, Count: n, References: len(basis.References), BW: strings.Contains(basis.Style, "bw")}
	plan, err := s.images.PlanImage(ctx, ai.ImagePlanRequest{Task: ai.TaskCover, ActorID: actor.UserID, ProjectID: req.ProjectID, RoutingMode: req.RoutingMode, CharacterIDs: basis.CharacterIDs, Needs: needs})
	if err != nil {
		return nil, sanitizePlanningError(ctx, err)
	}
	if req.RoutingMode != "" && plan.RoutingMode != req.RoutingMode && plan.RoutingMode != ai.ModeLocalOnly || validateImagePlan(plan) != nil || plan.Task != ai.TaskCover || plan.Needs != needs {
		return nil, studio.ErrInvalid
	}
	scope := jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: req.ProjectID, ChapterID: req.ChapterID}
	p := coverPayload{Version: coverPayloadVersion, Request: req, Actor: storedActor{actor.UserID, actor.TokenID}, Scope: scope, Plan: plan, Basis: basis.Fingerprint,
		Cast: basis.Cast, References: basis.References, Prompt: basis.Prompt, Negative: basis.Negative, Style: basis.Style}
	if p.Cast == nil {
		p.Cast = []coverCast{}
	}
	if p.References == nil {
		p.References = []coverRef{}
	}
	if _, err := decodeCoverPayload(requestJSON(p)); err != nil {
		return nil, err
	}
	// Like panel art: the job may make at most imageAttempts route calls, and
	// that whole envelope is approved before it is queued.
	cost, err := scaleCost(plan.Estimate, imageAttempts)
	if err != nil {
		return nil, studio.ErrInvalid
	}
	actor, err = s.resolveImageActor(ctx, actor, req.ProjectID, cost, plan.Remote)
	if err != nil {
		return nil, err
	}
	return s.queue.Enqueue(ctx, jobs.EnqueueParams{Type: JobCover, Scope: scope, UserID: actor.UserID, Executor: jobs.ExecutorGo, ProviderID: plan.ProviderID, ModelID: plan.ModelID,
		Decision: json.RawMessage(plan.Decision), Input: p, Estimate: cost, MaxAttempts: imageAttempts, Priority: 10, DedupeKey: coverDedupeKey(p),
		Guard: func(ctx context.Context, tx *db.Tx) error {
			if err := s.checkCoverTx(ctx, tx, p, actor); err != nil {
				return err
			}
			return s.approvalTx(ctx, tx, actor, cost, plan.Remote)
		}})
}

func (s *service) handleCover(ctx context.Context, run *jobs.Run) error {
	j := run.Job
	if j == nil || j.Type != JobCover || j.Executor != jobs.ExecutorGo || j.ParentID != "" || j.MaxAttempts != imageAttempts || j.RoundAttempt() < 1 || j.RoundAttempt() > imageAttempts {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	p, err := decodeCoverPayload(j.Input)
	if err != nil || p.Actor.UserID != j.UserID || stringJSON(p.Scope) != stringJSON(j.Scope) {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	plan := routePlan(p.Plan, j)
	if plan.Remote && !p.Plan.Remote || validateImagePlan(plan) != nil {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	cost, err := scaleCost(p.Plan.Estimate, imageAttempts)
	if err != nil || cost != j.Estimate {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	actor, err := s.resolveImageActor(ctx, coverActor(p), p.Scope.ProjectID, cost, plan.Remote)
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	if err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.checkCoverTx(ctx, tx, p, actor); err != nil {
			return err
		}
		return s.coverDispatchTx(ctx, tx, plan)
	}); err != nil {
		return mapImageJobError(ctx, err)
	}
	if err := run.Progress(ctx, 0, 0, 2, "Generating cover"); err != nil {
		return mapImageJobError(ctx, err)
	}
	refs := make([]ai.ImageRef, 0, len(p.References))
	total := 0
	for _, r := range p.References {
		data, a, err := s.readOwned(ctx, p.Scope.ProjectID, r.AssetID)
		if err != nil {
			return mapImageJobError(ctx, err)
		}
		if total += len(data); total > 48<<20 {
			return mapImageJobError(ctx, studio.ErrInvalid)
		}
		refs = append(refs, ai.ImageRef{Data: data, ContentType: a.ContentType, Role: "reference", Label: r.Label})
	}
	result, err := s.images.GenerateImage(ctx, ai.ImageGenerateRequest{Plan: plan, ActorID: actor.UserID, JobID: j.ID, ProjectID: p.Scope.ProjectID, Attempt: j.Attempts, CallSlot: 0,
		Prompt: p.Prompt, Negative: p.Negative, Width: coverWidth, Height: coverHeight, Count: plan.Needs.Count, Seed: seedFor(j.ID, 0), Style: p.Style, Background: "opaque", References: refs})
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
	staged := []*assets.StagedAsset{}
	defer func() {
		for _, a := range staged {
			_ = s.assets.DiscardStaged(context.WithoutCancel(ctx), a)
		}
	}()
	out := coverOutput{ChapterID: p.Scope.ChapterID, Candidates: make([]Candidate, 0, len(result.Images))}
	for k, im := range result.Images {
		pixels, err := decodePixels(im.Data)
		if err != nil || pixels.Bounds().Dx() != coverWidth || pixels.Bounds().Dy() != coverHeight {
			return mapImageJobError(ctx, studio.ErrInvalid)
		}
		var b bytes.Buffer
		if err := png.Encode(&b, pixels); err != nil {
			return mapImageJobError(ctx, err)
		}
		// The stored candidate records that AI made it, on which route and job.
		a, err := s.assets.Stage(ctx, assets.SaveParams{OwnerID: actor.UserID, ProjectID: p.Scope.ProjectID, Kind: coverAssetKind, Source: assets.SourceAI, ContentType: "image/png", Data: b.Bytes(),
			Meta: map[string]string{"job_id": j.ID, "chapter_id": p.Scope.ChapterID, "provider_id": plan.ProviderID, "model_id": plan.ModelID}})
		if err != nil {
			return mapImageJobError(ctx, err)
		}
		staged = append(staged, a)
		out.Candidates = append(out.Candidates, Candidate{Index: k, AssetID: a.Asset().ID, Seed: im.Seed})
	}
	if err := run.Progress(ctx, .5, 1, 2, "Generating cover"); err != nil {
		return mapImageJobError(ctx, err)
	}
	err = run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
		if err := s.checkCoverTx(ctx, tx, p, actor); err != nil {
			return nil, err
		}
		for _, a := range staged {
			if _, err := s.assets.CommitStagedTx(ctx, tx, a); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	return nil
}

// coverJob loads the actor's own finished cover job.
func (s *service) coverJob(ctx context.Context, actor studio.Actor, id string) (*jobs.Job, coverPayload, coverOutput, error) {
	var p coverPayload
	var out coverOutput
	if !db.ValidID(id) || !db.ValidID(actor.UserID) {
		return nil, p, out, studio.ErrNotFound
	}
	j, err := s.queue.Get(ctx, id)
	if err != nil {
		return nil, p, out, err
	}
	if j.Type != JobCover || j.UserID != actor.UserID {
		return nil, p, out, studio.ErrNotFound
	}
	if err := s.studio.Authorize(ctx, actor, j.Scope.ProjectID, studio.ActRead); err != nil {
		return nil, p, out, err
	}
	p, err = decodeCoverPayload(j.Input)
	if err != nil || p.Actor.UserID != j.UserID || stringJSON(p.Scope) != stringJSON(j.Scope) {
		return nil, p, out, studio.ErrInvalid
	}
	if j.State != jobs.StateDone {
		return j, p, out, nil
	}
	if _, err := strictDecodeJSON(j.Output, 1<<20, &out); err != nil || len(out.Candidates) < 1 || len(out.Candidates) > 4 || out.ChapterID != p.Scope.ChapterID {
		return nil, p, out, studio.ErrInvalid
	}
	return j, p, out, nil
}

func (s *service) CoverCandidates(ctx context.Context, actor studio.Actor, id string) (CoverResult, error) {
	j, p, out, err := s.coverJob(ctx, actor, id)
	if err != nil {
		return CoverResult{}, err
	}
	res := CoverResult{Candidates: []Candidate{}}
	if j.State != jobs.StateDone {
		return res, nil
	}
	for _, c := range out.Candidates {
		a, err := s.assets.Get(ctx, c.AssetID)
		if err != nil || a.ProjectID == nil || *a.ProjectID != p.Scope.ProjectID {
			return CoverResult{}, studio.ErrForbidden
		}
		c.URL = s.assets.URL(a, time.Hour)
		res.Candidates = append(res.Candidates, c)
	}
	res.KeptIndex = out.KeptIndex
	return res, nil
}

func (s *service) KeepCover(ctx context.Context, actor studio.Actor, id string, index int) (string, error) {
	// Choosing the cover readers see is a human decision, never an agent's.
	if actor.TokenID != "" {
		return "", studio.ErrForbidden
	}
	j, p, out, err := s.coverJob(ctx, actor, id)
	if err != nil {
		return "", err
	}
	if j.State != jobs.StateDone {
		return "", ErrCoverPending
	}
	if index < 0 || index >= len(out.Candidates) {
		return "", studio.ErrInvalid
	}
	if out.KeptIndex != nil {
		return "", ErrCoverKept
	}
	if _, err := s.resolveImageActor(ctx, actor, p.Scope.ProjectID, jobs.Cost{}, false); err != nil {
		return "", err
	}
	kept := ""
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		var raw string
		if err := tx.Get(ctx, &raw, `SELECT output FROM jobs WHERE id = ? AND state = 'done'`+lockSQL(tx), id); err != nil {
			return err
		}
		var fresh coverOutput
		if _, err := strictDecodeJSON([]byte(raw), 1<<20, &fresh); err != nil || len(fresh.Candidates) != len(out.Candidates) {
			return studio.ErrInvalid
		}
		if fresh.KeptIndex != nil {
			return ErrCoverKept
		}
		if err := s.checkCoverTx(ctx, tx, p, actor); err != nil {
			return err
		}
		c := fresh.Candidates[index]
		a, err := s.assets.GetTx(ctx, tx, c.AssetID)
		if err != nil || c.Index != index || a.ProjectID == nil || *a.ProjectID != p.Scope.ProjectID || a.Source != assets.SourceAI || a.Kind != coverAssetKind {
			return studio.ErrInvalid
		}
		now := db.Now()
		if _, err := tx.Exec(ctx, `UPDATE projects SET cover_asset_id = ?, updated_at = ? WHERE id = ?`, c.AssetID, now, p.Scope.ProjectID); err != nil {
			return err
		}
		fresh.KeptIndex, fresh.KeptBy, fresh.KeptAt = &index, actor.UserID, &now
		if _, err := tx.Exec(ctx, `UPDATE jobs SET output = ?, updated_at = ? WHERE id = ?`, db.MustJSON(fresh), now, id); err != nil {
			return err
		}
		kept = c.AssetID
		return nil
	})
	if err != nil {
		return "", err
	}
	return kept, nil
}
