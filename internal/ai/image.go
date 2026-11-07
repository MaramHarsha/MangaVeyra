package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

const seedreamModel = "bytedance-seed/seedream-4.5"
const falSchnellModel = "fal-ai/flux/schnell"
const falInpaintModel = "fal-ai/flux-lora/inpainting"
const maxImageBytes = 24 << 20

type imageService struct {
	d          *core.Deps
	providers  *providerStore
	catalog    *modelCatalog
	spend      *textService
	doer       httpDoer
	workerWait time.Duration
}

var _ ImageService = (*imageService)(nil)

type imageCandidate struct {
	Model                      ModelManifest
	ProviderID, Kind, Executor string
	Remote                     bool
	Cost                       jobs.Cost
	Score                      float64
}
type imageDecision struct {
	Mode         string   `json:"mode"`
	Chosen       string   `json:"chosen"`
	Provider     string   `json:"provider"`
	Alternatives []string `json:"alternatives"`
	Skipped      []string `json:"skipped"`
	Reason       string   `json:"reason"`
}

func imageError(code string, retry bool, cost jobs.Cost) *GenerateError {
	messages := map[string]string{CodeBadInput: "The image request is invalid.", CodeNoProvider: "No eligible image provider is available.", CodeLocalOnly: "Image generation uses the offline placeholder while local-only mode is on.", CodeSpendLimit: "The monthly remote-spend limit has been reached.", CodeProviderAuth: "The image provider rejected its connection key.", CodeProviderRefuse: "The image provider declined this request.", CodeRateLimited: "The image provider is busy. Try again later.", CodeTimeout: "The image provider did not finish in time.", CodeBadOutput: "The image provider returned an invalid image.", CodeUpstream: "The image provider could not complete the request."}
	message := messages[code]
	if message == "" {
		code = CodeUpstream
		message = messages[code]
	}
	return &GenerateError{Code: code, SafeMessage: message, Retryable: retry, Actual: cost}
}
func normalizeNeeds(n ImageNeeds) (ImageNeeds, error) {
	if n.Width == 0 {
		n.Width = 1024
	}
	if n.Height == 0 {
		n.Height = 1024
	}
	if n.Count == 0 {
		n.Count = 1
	}
	if n.Width < 64 || n.Width > 4096 || n.Height < 64 || n.Height > 4096 || n.Count < 1 || n.Count > 4 || n.References < 0 || n.References > 32 || n.Mask && !n.Edit {
		return n, imageError(CodeBadInput, false, jobs.Cost{})
	}
	return n, nil
}
func imageTask(t string) bool {
	return t == TaskPanel || t == TaskCharacterSheet || t == TaskRegionEdit || t == TaskCover
}

// quoteImage is the sole pricing authority. Unknown routes are unusable even
// when a manifest advertises a cheap price or a supported provider kind.
func quoteImage(kind, model string, n ImageNeeds) (jobs.Cost, bool) {
	switch kind {
	case PlaceholderID:
		return jobs.Cost{}, model == PlaceholderArt
	case providerKindOpenRouter:
		if model == seedreamModel {
			refs := n.References
			if n.Edit {
				refs++
			}
			if n.Mask {
				refs++
			}
			if refs > 14 {
				return jobs.Cost{}, false
			}
			return jobs.Cost{USD: float64(n.Count) * .04}, true
		}
	case providerKindFal:
		if n.Width > 1024 || n.Height > 1024 {
			return jobs.Cost{}, false
		}
		mp := math.Ceil(float64(n.Width*n.Height) / 1_000_000)
		if model == falSchnellModel {
			return jobs.Cost{USD: float64(n.Count) * mp * .003}, true
		}
		if model == falInpaintModel && n.Edit && n.Mask {
			return jobs.Cost{USD: float64(n.Count) * mp * .035}, true
		}
	}
	return jobs.Cost{}, false
}

func (s *imageService) readSettings(ctx context.Context) (settings.Instance, error) {
	var inst settings.Instance
	err := s.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		var err error
		inst, err = s.d.Settings.InstanceTx(ctx, tx)
		if err != nil {
			return err
		}
		return inst.Validate()
	})
	return inst, err
}

func (s *imageService) PlanImage(ctx context.Context, req ImagePlanRequest) (ImagePlan, error) {
	return s.planImage(ctx, req, nil)
}
func (s *imageService) planImage(ctx context.Context, req ImagePlanRequest, exclude map[string]bool) (ImagePlan, error) {
	var zero ImagePlan
	if !imageTask(req.Task) || !db.ValidID(req.ActorID) || !db.ValidID(req.ProjectID) || req.RoutingMode != "" && !validRoutingMode(req.RoutingMode) {
		return zero, imageError(CodeBadInput, false, jobs.Cost{})
	}
	n, err := normalizeNeeds(req.Needs)
	if err != nil {
		return zero, err
	}
	req.Needs = n
	inst, err := s.readSettings(ctx)
	if err != nil {
		return zero, imageError(CodeLocalOnly, false, jobs.Cost{})
	}
	if req.RoutingMode == "" {
		req.RoutingMode = inst.RoutingMode
	}
	if err := s.resolvePins(ctx, &req); err != nil {
		return zero, err
	}
	mode := req.RoutingMode
	if req.Task == TaskRegionEdit && !n.Mask {
		return zero, imageError(CodeBadInput, false, jobs.Cost{})
	}
	if inst.LocalOnly || mode == ModeLocalOnly {
		if req.PinProviderID != "" && req.PinProviderID != PlaceholderID || req.PinModelID != "" && req.PinModelID != PlaceholderArt {
			return zero, imageError(CodeLocalOnly, false, jobs.Cost{})
		}
		req.RoutingMode = ModeLocalOnly
		return imagePlan(req, imageCandidate{Model: ModelManifest{ID: PlaceholderArt, Name: "Placeholder art"}, ProviderID: PlaceholderID, Kind: PlaceholderID, Executor: jobs.ExecutorGo}, nil, nil), nil
	}
	models, err := s.catalog.list(ctx)
	if err != nil {
		return zero, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	providers, err := s.providers.Candidates(ctx, req.ActorID)
	if err != nil {
		return zero, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	workers, err := s.d.Jobs.ListWorkers(ctx)
	if err != nil {
		return zero, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	candidates := []imageCandidate{}
	skipped := []string{}
	// A public series currently in storage cannot be made private by a caller's
	// stale plan. Phase 1's unpublished projects have no series row.
	var public int
	if err := s.d.DB.Get(ctx, &public, `SELECT COUNT(*) FROM series WHERE project_id = ? AND visibility <> 'private'`, req.ProjectID); err != nil {
		return zero, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	req.PublicSeries = req.PublicSeries || public > 0
	for _, m := range models {
		if req.PinModelID != "" && req.PinModelID != m.ID {
			continue
		}
		if !modelSupports(m, n) {
			skipped = append(skipped, m.ID+": capabilities")
			continue
		}
		if req.PublicSeries && inst.Content.LicenceGuard && !m.Commercial {
			skipped = append(skipped, m.ID+": licence")
			continue
		}
		add := func(c imageCandidate) {
			if req.PinProviderID != "" && req.PinProviderID != c.ProviderID || exclude[c.ProviderID+"\x00"+m.ID] {
				return
			}
			c.Model = m
			c.Score = modelScore(mode, m, c.Cost)
			candidates = append(candidates, c)
		}
		if m.ID == PlaceholderArt && hasProvider(m, PlaceholderID) {
			add(imageCandidate{ProviderID: PlaceholderID, Kind: PlaceholderID, Executor: jobs.ExecutorGo})
			continue
		}
		for _, p := range providers {
			if !hasProvider(m, p.Kind) || p.Kind == providerKindLocalText || mode == ModeFreeOnly {
				continue
			}
			cost, ok := quoteImage(p.Kind, m.ID, n)
			if !ok {
				skipped = append(skipped, m.ID+": unbounded route")
				continue
			}
			add(imageCandidate{ProviderID: p.ID, Kind: p.Kind, Executor: jobs.ExecutorGo, Remote: true, Cost: cost})
		}
		if hasProvider(m, providerKindWorker) {
			available := false
			for _, w := range workers {
				if w.Status != jobs.WorkerOnline || time.Since(w.LastSeenAt) > jobs.MissedHeartbeats*jobs.HeartbeatInterval {
					continue
				}
				hasModel := false
				for _, id := range w.Models.V {
					if id == m.ID {
						hasModel = true
					}
				}
				if !hasModel {
					continue
				}
				needed := workerTask(req.Task, n)
				hasType := false
				for _, t := range w.JobTypes.V {
					if t == needed {
						hasType = true
					}
				}
				if !hasType {
					continue
				}
				requiredVRAM := m.VRAMMB
				if m.MinVRAMMB > 0 {
					requiredVRAM = m.MinVRAMMB
				}
				if requiredVRAM > 0 {
					fits := false
					for _, g := range w.GPUs.V {
						if g.VRAMTotalMB >= requiredVRAM {
							fits = true
						}
					}
					if !fits {
						continue
					}
				}
				available = true
			}
			if available {
				add(imageCandidate{ProviderID: providerKindWorker, Kind: providerKindWorker, Executor: jobs.ExecutorWorker, Cost: jobs.Cost{GPUSeconds: m.GPUSeconds * float64(n.Count)}})
			} else {
				skipped = append(skipped, m.ID+": worker offline")
			}
		}
	}
	if len(candidates) == 0 {
		return zero, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Model.ID == PlaceholderArt {
			return false
		}
		if candidates[j].Model.ID == PlaceholderArt {
			return true
		}
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if candidates[i].ProviderID != candidates[j].ProviderID {
			return candidates[i].ProviderID < candidates[j].ProviderID
		}
		return candidates[i].Model.ID < candidates[j].Model.ID
	})
	// One attempt may be rerouted later by the parent handler. Advertise the
	// maximum eligible per-attempt cost now; Gen scales by its bounded attempt
	// count before approval. Free/local plans never acquire paid fallbacks.
	ceiling := candidates[0].Cost
	for _, c := range candidates {
		if candidates[0].Remote {
			ceiling.USD = math.Max(ceiling.USD, c.Cost.USD)
		}
		ceiling.GPUSeconds = math.Max(ceiling.GPUSeconds, c.Cost.GPUSeconds)
	}
	p := imagePlan(req, candidates[0], candidates, skipped)
	p.Estimate = ceiling
	return p, nil
}

func modelScore(mode string, m ModelManifest, c jobs.Cost) float64 {
	switch mode {
	case ModeBest:
		return m.Quality
	case ModeFastest:
		return m.Speed
	default:
		return m.Quality*.65 + m.Speed*.35 - c.USD*100
	}
}
func imagePlan(req ImagePlanRequest, c imageCandidate, alternatives []imageCandidate, skipped []string) ImagePlan {
	ids := []string{}
	for _, a := range alternatives {
		ids = append(ids, a.Model.ID)
	}
	decision, _ := json.Marshal(imageDecision{Mode: req.RoutingMode, Chosen: c.Model.ID, Provider: c.ProviderID, Alternatives: ids, Skipped: skipped, Reason: "eligible capabilities, licence and mode"})
	explain := "Auto → " + c.Model.Name + " · " + c.Kind
	if c.Remote {
		explain += fmt.Sprintf(" · ≤$%.3f", c.Cost.USD)
	}
	return ImagePlan{Task: req.Task, RoutingMode: req.RoutingMode, Needs: req.Needs, PublicSeries: req.PublicSeries, PinProviderID: req.PinProviderID, PinModelID: req.PinModelID, ProviderID: c.ProviderID, ModelID: c.Model.ID, Remote: c.Remote, Executor: c.Executor, Decision: decision, Estimate: c.Cost, Explain: explain}
}

func (s *imageService) resolvePins(ctx context.Context, req *ImagePlanRequest) error {
	if req.PinProviderID != "" || req.PinModelID != "" {
		return nil
	}
	// Most-specific character pin, then project, panel type, then server.
	type pin struct{ Model, Provider string }
	var chosen *pin
	look := func(kind, id string) (*pin, error) {
		var rows []struct {
			Model    string `db:"model_id"`
			Provider string `db:"provider_id"`
		}
		if err := s.d.DB.Select(ctx, &rows, `SELECT model_id, provider_id FROM routing_pins WHERE scope_kind = ? AND scope_id = ? AND task = ?`, kind, id, req.Task); err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, nil
		}
		return &pin{rows[0].Model, rows[0].Provider}, nil
	}
	if len(req.CharacterIDs) > 32 {
		return imageError(CodeBadInput, false, jobs.Cost{})
	}
	for _, id := range req.CharacterIDs {
		if !db.ValidID(id) {
			return imageError(CodeBadInput, false, jobs.Cost{})
		}
		p, err := look("character", id)
		if err != nil {
			return err
		}
		if p != nil {
			if chosen != nil && *chosen != *p {
				return imageError(CodeNoProvider, false, jobs.Cost{})
			}
			chosen = p
		}
	}
	for _, scope := range [][2]string{{"project", req.ProjectID}, {"panel_type", req.PanelType}, {"server", ""}} {
		if chosen != nil {
			break
		}
		if scope[0] == "panel_type" && scope[1] == "" {
			continue
		}
		p, err := look(scope[0], scope[1])
		if err != nil {
			return err
		}
		chosen = p
	}
	if chosen != nil {
		req.PinProviderID = chosen.Provider
		req.PinModelID = chosen.Model
	}
	return nil
}

func (s *imageService) dispatchGuard(ctx context.Context, req ImageGenerateRequest) (*jobs.Job, error) {
	inst, err := s.readSettings(ctx)
	if err != nil {
		return nil, imageError(CodeLocalOnly, false, jobs.Cost{})
	}
	if (inst.LocalOnly || req.Plan.RoutingMode == ModeLocalOnly) && req.Plan.ProviderID != PlaceholderID {
		return nil, imageError(CodeLocalOnly, false, jobs.Cost{})
	}
	j, err := s.d.Jobs.Get(ctx, req.JobID)
	if err != nil || j.State != jobs.StateRunning || j.Attempts != req.Attempt || j.CancelRequested || j.UserID != req.ActorID || j.Scope.ProjectID != req.ProjectID || j.ProviderID != req.Plan.ProviderID || j.ModelID != req.Plan.ModelID {
		return nil, imageError(CodeBadInput, false, jobs.Cost{})
	}
	return j, nil
}

func (s *imageService) GenerateImage(ctx context.Context, req ImageGenerateRequest) (ImageResult, error) {
	var zero ImageResult
	n, err := normalizeNeeds(req.Plan.Needs)
	if err != nil || req.CallSlot < 0 || req.CallSlot >= 16 || !imageTask(req.Plan.Task) || !validRoutingMode(req.Plan.RoutingMode) || !db.ValidID(req.ActorID) || !db.ValidID(req.ProjectID) || req.Width != n.Width || req.Height != n.Height || req.Count != n.Count || len(req.References) != n.References || len(req.Prompt) > 64<<10 || strings.TrimSpace(req.Prompt) == "" || !utf8.ValidString(req.Prompt) || len(req.Negative) > 16<<10 || len(req.Style) > 256 || n.Edit != (req.Init != nil) || n.Mask != (req.Mask != nil) {
		return zero, imageError(CodeBadInput, false, jobs.Cost{})
	}
	totalBytes := 0
	refs := append([]ImageRef{}, req.References...)
	if req.Init != nil {
		refs = append(refs, *req.Init)
	}
	if req.Mask != nil {
		refs = append(refs, *req.Mask)
	}
	for _, r := range refs {
		totalBytes += len(r.Data)
		if len(r.Data) == 0 || len(r.Data) > maxImageBytes || totalBytes > 48<<20 {
			return zero, imageError(CodeBadInput, false, jobs.Cost{})
		}
		im, _, err := storage.DecodeImage(r.Data, storage.ImageLimits{MaxWidth: 4096, MaxHeight: 4096, MaxPixels: 16 << 20})
		if err != nil || storage.DetectContentType(r.Data) != r.ContentType {
			return zero, imageError(CodeBadInput, false, jobs.Cost{})
		}
		if (r.Role == "init" || r.Role == "mask") && (im.Bounds().Dx() != req.Width || im.Bounds().Dy() != req.Height) {
			return zero, imageError(CodeBadInput, false, jobs.Cost{})
		}
	}
	if req.Plan.ProviderID == PlaceholderID {
		if req.Plan.ModelID != PlaceholderArt || req.Plan.Remote || req.Plan.Executor != jobs.ExecutorGo {
			return zero, imageError(CodeBadInput, false, jobs.Cost{})
		}
		images, err := placeholderImages(req)
		if err != nil {
			return zero, err
		}
		// The router chose the placeholder, so G11 explains it like any other
		// route. Placeholder art is local and free: a failed record is logged
		// and never fails the job.
		if err := s.recordDecision(ctx, req); err != nil && s.d.Log != nil {
			s.d.Log.WarnContext(ctx, "record placeholder routing decision", "err", err)
		}
		return ImageResult{Images: images, ProviderID: PlaceholderID, ModelID: PlaceholderArt}, nil
	}
	if _, err := s.dispatchGuard(ctx, req); err != nil {
		return zero, err
	}
	if err := s.recordDecision(ctx, req); err != nil {
		return zero, imageError(CodeUpstream, false, jobs.Cost{})
	}
	m, err := s.catalog.lookup(ctx, req.Plan.ModelID)
	if err != nil || !modelSupports(m, n) {
		return zero, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	inst, err := s.readSettings(ctx)
	if err != nil {
		return zero, imageError(CodeLocalOnly, false, jobs.Cost{})
	}
	var public int
	if err := s.d.DB.Get(ctx, &public, `SELECT COUNT(*) FROM series WHERE project_id = ? AND visibility <> 'private'`, req.ProjectID); err != nil {
		return zero, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	if inst.Content.LicenceGuard && (req.Plan.PublicSeries || public > 0) && !m.Commercial {
		return zero, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	if req.Plan.PinProviderID != "" && req.Plan.PinProviderID != req.Plan.ProviderID || req.Plan.PinModelID != "" && req.Plan.PinModelID != req.Plan.ModelID {
		return zero, imageError(CodeBadInput, false, jobs.Cost{})
	}
	var result ImageResult
	if req.Plan.ProviderID == providerKindWorker {
		if req.Plan.Remote || req.Plan.Executor != jobs.ExecutorWorker || !hasProvider(m, providerKindWorker) {
			return zero, imageError(CodeBadInput, false, jobs.Cost{})
		}
		result, err = s.workerImage(ctx, req)
	} else {
		if !req.Plan.Remote || req.Plan.Executor != jobs.ExecutorGo || req.Plan.RoutingMode == ModeFreeOnly {
			return zero, imageError(CodeBadInput, false, jobs.Cost{})
		}
		result, err = s.remoteImage(ctx, req, m)
	}
	if err != nil {
		return zero, err
	}
	if req.Mask != nil {
		if err := preserveMaskedImages(&result, req); err != nil {
			return zero, imageError(CodeBadOutput, false, result.Actual)
		}
	}
	return result, nil
}

// ImageFallback is opt-in integration for NEW typed image jobs. Gen must pass
// its durable plan, reconstructing the current route from the job. It never
// touches old text jobs, worker children, pins, or unapproved remote spending.
type ImageFallback func(context.Context, *jobs.Job, ImagePlan, jobs.JobError) (*jobs.Reroute, error)

func (s *imageService) fallback(ctx context.Context, j *jobs.Job, p ImagePlan, failure jobs.JobError) (*jobs.Reroute, error) {
	if j == nil || j.Executor != jobs.ExecutorGo || !imageTask(j.Type) || j.Type != p.Task || j.RoundAttempt() >= j.MaxAttempts || j.MaxAttempts > 3 || p.PinProviderID != "" || p.PinModelID != "" {
		return nil, nil
	}
	if j.ParentID != "" {
		// A page batch's panel jobs are Go orchestration parents in their own
		// right. Worker image children are never eligible for router fallback.
		parent, err := s.d.Jobs.Get(ctx, j.ParentID)
		if err != nil || j.Type != TaskPanel || parent.Type != "pages.from_script" || parent.State != jobs.StateDone ||
			parent.UserID != j.UserID || parent.Scope.ProjectID != j.Scope.ProjectID || parent.Scope.Kind != jobs.ScopeChapter {
			return nil, nil
		}
		var chapterID string
		if err := s.d.DB.Get(ctx, &chapterID, `SELECT chapter_id FROM pages WHERE id = ? AND project_id = ?`, j.Scope.PageID, j.Scope.ProjectID); err != nil || chapterID != parent.Scope.ChapterID {
			return nil, nil
		}
	}
	switch failure.Code {
	case CodeTimeout, CodeRateLimited, CodeUpstream, jobs.ErrCodeOOM, jobs.ErrCodeInternal:
	default:
		return nil, nil
	}
	exclude := map[string]bool{j.ProviderID + "\x00" + j.ModelID: true}
	var events []struct {
		Data string `db:"data"`
	}
	if err := s.d.DB.Select(ctx, &events, `SELECT data FROM job_events WHERE job_id = ? AND kind = 'fallback'`, j.ID); err != nil {
		return nil, err
	}
	for _, e := range events {
		var v struct {
			FromModel    string `json:"from_model"`
			FromProvider string `json:"from_provider"`
		}
		if json.Unmarshal([]byte(e.Data), &v) == nil {
			exclude[v.FromProvider+"\x00"+v.FromModel] = true
		}
	}
	mode := p.RoutingMode
	if !p.Remote && mode != ModeLocalOnly {
		mode = ModeFreeOnly
	}
	next, err := s.planImage(ctx, ImagePlanRequest{Task: p.Task, ActorID: j.UserID, ProjectID: j.Scope.ProjectID, RoutingMode: mode, Needs: p.Needs, PublicSeries: p.PublicSeries}, exclude)
	if err != nil {
		return nil, nil
	}
	maxUSD := j.Estimate.USD / float64(j.MaxAttempts)
	maxGPU := j.Estimate.GPUSeconds / float64(j.MaxAttempts)
	if next.Estimate.USD > maxUSD+0.0000001 || next.Estimate.GPUSeconds > maxGPU+0.0000001 || !p.Remote && next.Remote {
		return nil, nil
	}
	// Parent image handlers always remain Go jobs, including worker routes.
	return &jobs.Reroute{ModelID: next.ModelID, ProviderID: next.ProviderID, Executor: jobs.ExecutorGo, Decision: next.Decision, Reason: "Next eligible image model after a transient failure"}, nil
}

func workerTask(task string, n ImageNeeds) string {
	if n.Mask {
		return "image.inpaint"
	}
	if n.Edit {
		return "image.edit"
	}
	if task == TaskCharacterSheet {
		return "character.sheet"
	}
	return "image.generate"
}

func safeImageAdapterError(err error, cost jobs.Cost) *GenerateError {
	var a *adapterError
	if errors.As(err, &a) {
		return imageError(a.code, a.retryable, cost)
	}
	var g *GenerateError
	if errors.As(err, &g) {
		return imageError(g.Code, g.Retryable, cost)
	}
	return imageError(CodeUpstream, false, cost)
}
