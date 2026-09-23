package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/providers"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// workerTaskTypes maps worker job types that can fall back to a remote
// provider to their routed task.
var workerTaskTypes = map[string]string{
	"image.generate":  catalog.TaskGenerate,
	"image.edit":      catalog.TaskEdit,
	"image.inpaint":   catalog.TaskInpaint,
	"character.sheet": catalog.TaskSheet,
	"upscale":         catalog.TaskUpscale,
}

// Failover is installed as the queue's fallback: when a routed job (one whose
// decision carries a chain) fails, the next candidate that has not been tried
// takes over — on a worker, or on the Go executor for remote providers. Jobs
// dispatched by Run are managed by Run itself and are left alone.
func (s *Service) Failover(ctx context.Context, j *jobs.Job, jerr jobs.JobError) (*jobs.Reroute, error) {
	if len(j.Decision) == 0 {
		return nil, nil
	}
	var d managedDecision
	if err := json.Unmarshal(j.Decision, &d); err != nil || d.Managed || len(d.Chain) == 0 {
		return nil, nil
	}
	task, ok := workerTaskTypes[j.Type]
	tried := map[string]bool{}
	for _, t := range d.Tried {
		tried[t] = true
	}
	tried[j.ModelID+"|"+j.ProviderID] = true
	for {
		next := d.Next(j.ModelID, j.ProviderID, tried)
		if next == nil {
			return nil, nil
		}
		tried[next.ModelID+"|"+next.ProviderID] = true
		exec := jobs.ExecutorGo
		if next.ProviderKind == providers.KindLocalWorker {
			exec = jobs.ExecutorWorker
		} else if !ok {
			// Only image job types have a Go handler for remote providers.
			continue
		}
		d.Tried = sortedKeys(tried)
		reason := fmt.Sprintf("%s failed (%s: %s); falling back to %s via %s", j.ModelID, jerr.Code, truncate(jerr.Message, 120), next.ModelName, next.ProviderName)
		modelID := next.ModelID
		if m, ok := s.Catalog(ctx).Get(next.ModelID); ok && m.Hardware.AnyWorker && exec == jobs.ExecutorWorker {
			modelID = ""
		}
		_ = task
		s.bumpStats(ctx, j.ModelID, j.ProviderID, task, false, 0, 0)
		return &jobs.Reroute{ModelID: modelID, ProviderID: next.ProviderID, Executor: exec, Decision: mustRaw(d), Reason: reason}, nil
	}
}

func mustRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// JobHandlers runs worker image job types on the Go executor when routing (or
// a fallback) sends them to a remote provider. The job's model and provider
// were chosen by the router; a failure goes back to Failover for the next one.
func (m *Module) JobHandlers() map[string]jobs.Handler {
	out := map[string]jobs.Handler{}
	for typ, task := range workerTaskTypes {
		out[typ] = m.remoteHandler(task)
	}
	return out
}

func (m *Module) remoteHandler(task string) jobs.Handler {
	return func(ctx context.Context, run *jobs.Run) error {
		s := m.svc
		j := run.Job
		var in struct {
			Prompt    string           `json:"prompt"`
			Negative  string           `json:"negative"`
			Width     int              `json:"width"`
			Height    int              `json:"height"`
			N         int              `json:"n"`
			Seed      *int64           `json:"seed"`
			Steps     int              `json:"steps"`
			Guidance  float64          `json:"guidance"`
			Style     string           `json:"style"`
			Strength  float64          `json:"strength"`
			Scale     int              `json:"scale"`
			LoRAs     []providers.LoRA `json:"loras"`
			FeatherPx int              `json:"feather_px"`
		}
		if err := run.DecodeInput(&in); err != nil {
			return jobs.Permanent(jobs.ErrCodeBadInput, err)
		}
		req := Request{Prompt: in.Prompt, Negative: in.Negative, Width: in.Width, Height: in.Height, N: in.N, Seed: in.Seed, Steps: in.Steps,
			Guidance: in.Guidance, Style: in.Style, Strength: in.Strength, Scale: in.Scale, LoRAs: in.LoRAs, FeatherPx: in.FeatherPx}
		for name, key := range j.Files {
			switch {
			case name == "image":
				req.Image = &Input{Key: key}
			case name == "mask":
				req.Mask = &Input{Key: key}
			case name == "pose_image":
				req.PoseImage = &Input{Key: key}
			case len(name) > 4 && name[:4] == "ref_":
				req.Refs = append(req.Refs, Input{Key: key})
			}
		}
		slices.SortFunc(req.Refs, func(a, b Input) int {
			if a.Key < b.Key {
				return -1
			}
			return 1
		})
		var d managedDecision
		_ = json.Unmarshal(j.Decision, &d)
		var cand *router.Candidate
		for i := range d.Chain {
			if d.Chain[i].ModelID == j.ModelID && d.Chain[i].ProviderID == j.ProviderID {
				cand = &d.Chain[i]
			}
		}
		if cand == nil {
			// No routed candidate: route now with the job's model as an override.
			need := router.Need{Task: task, UserID: j.UserID, ProjectID: j.Scope.ProjectID, JobType: j.Type, ModelID: j.ModelID, ProviderID: j.ProviderID,
				Width: in.Width, Height: in.Height, Count: max(1, in.N), Refs: len(req.Refs)}
			res, err := s.Run(ctx, run, need, req)
			if err != nil {
				return err
			}
			run.SetOutput(workerStyleOutput(res))
			return nil
		}
		need := router.Need{Task: task, UserID: j.UserID, ProjectID: j.Scope.ProjectID, JobType: j.Type, Width: in.Width, Height: in.Height,
			Count: max(1, in.N), Refs: len(req.Refs)}
		res, err := s.runCandidate(ctx, run, *cand, need, req)
		if err != nil {
			if err == jobs.ErrCanceled {
				return err
			}
			code := jobs.ErrCodeInternal
			switch providers.CodeOf(err) {
			case providers.CodeSafety:
				code = jobs.ErrCodeSafety
			case providers.CodeTimeout:
				code = jobs.ErrCodeTimeout
			}
			s.bumpStats(ctx, cand.ModelID, cand.ProviderID, task, false, 0, 0)
			return &jobs.JobError{Code: code, Message: err.Error(), Retryable: true}
		}
		run.SetOutput(workerStyleOutput(res))
		return nil
	}
}

// workerStyleOutput shapes a result like a worker's output ({"images": [{"key","seed"}]}).
func workerStyleOutput(res *Result) map[string]any {
	imgs := make([]map[string]any, 0, len(res.Images))
	for _, im := range res.Images {
		imgs = append(imgs, map[string]any{"key": im.Key, "seed": im.Seed})
	}
	return map[string]any{"images": imgs, "model_id": res.ModelID, "provider_id": res.ProviderID, "placeholder": res.Placeholder}
}

// onFinish records GPU time and run counts for worker jobs that did not go through Run.
func (s *Service) onFinish(ctx context.Context, j *jobs.Job) {
	if j.Executor != jobs.ExecutorWorker {
		return
	}
	if j.Actual.GPUSeconds > 0 {
		var need router.Need
		need.UserID, need.ProjectID = j.UserID, j.Scope.ProjectID
		rn := &jobs.Run{Job: j}
		s.ledger(ctx, rn, need, j.ModelID, j.ProviderID, "gpu", 0, j.Actual.GPUSeconds)
	}
	var d managedDecision
	if len(j.Decision) > 0 && json.Unmarshal(j.Decision, &d) == nil && d.Managed {
		return
	}
	if j.ModelID != "" && (j.State == jobs.StateDone || j.State == jobs.StateFailed) {
		s.bumpStats(ctx, j.ModelID, j.ProviderID, workerTaskTypes[j.Type], j.State == jobs.StateDone, j.Actual.GPUSeconds, 0)
	}
}

// spendGuard rejects jobs whose estimated remote spend exceeds the monthly limit.
func (s *Service) spendGuard(ctx context.Context, p *jobs.EnqueueParams) error {
	if p.Estimate.USD <= 0 || p.UserID == "" {
		return nil
	}
	rem := s.remaining(ctx, s.d.Settings.MustInstance(ctx), p.UserID)
	if rem >= 0 && p.Estimate.USD > rem+1e-9 {
		return core.NewError(422, "spend_limit", fmt.Sprintf("this job would pass the monthly remote spend limit (%.2f left)", rem))
	}
	return nil
}

// ManifestJSON returns the manifest handed to workers with a claimed job.
func (s *Service) ManifestJSON(ctx context.Context, modelID string) (json.RawMessage, error) {
	m, ok := s.Catalog(ctx).Get(modelID)
	if !ok {
		return nil, core.ErrNotFound()
	}
	return json.Marshal(m)
}
