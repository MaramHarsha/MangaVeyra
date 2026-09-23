package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // decode sizes of provider outputs
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/providers"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Input is an input file: a storage key, raw bytes, or both.
type Input struct {
	Key  string `json:"key,omitempty"`
	Data []byte `json:"-"`
	MIME string `json:"mime,omitempty"`
}

// Request is what to generate. Image fields apply to image tasks, Text to
// text and translate, Media fields to video and voice.
type Request struct {
	Prompt   string
	Negative string
	Width    int
	Height   int
	// N is the number of variations (1–4).
	N        int
	Seed     *int64
	Steps    int
	Guidance float64
	// Style is bw_screentone, flat_grey or colour.
	Style     string
	Image     *Input
	Mask      *Input
	Refs      []Input
	PoseImage *Input
	// Pose is OpenPose JSON for workers.
	Pose  json.RawMessage
	LoRAs []providers.LoRA
	// Strength for edits (0–1) and FeatherPx for inpaints.
	Strength  float64
	FeatherPx int
	Scale     int
	// Label is written on placeholder art ("p.7 panel 3").
	Label string
	// Text is the text request for text and translate tasks.
	Text *TextRequest
	// Media fields (video / voice).
	Seconds float64
	Voice   string
	Speech  string
	Aspect  string
	// Extra is merged into the worker job input (e.g. character.train settings).
	Extra map[string]any
}

// TextRequest asks a text model.
type TextRequest struct {
	System    string
	Prompt    string
	Messages  []providers.Message
	MaxTokens int
	JSON      bool
	// Lang is the target language for translate.
	Lang        string
	Temperature *float64
}

// Output is one stored result file.
type Output struct {
	Key    string `json:"key"`
	MIME   string `json:"mime,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Seed   int64  `json:"seed,omitempty"`
}

// Attempt records one candidate that was tried.
type Attempt struct {
	ModelID    string `json:"model_id"`
	ProviderID string `json:"provider_id"`
	Code       string `json:"code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Result is what Run produced.
type Result struct {
	ModelID      string           `json:"model_id"`
	ModelName    string           `json:"model_name"`
	ProviderID   string           `json:"provider_id"`
	ProviderKind string           `json:"provider_kind"`
	ProviderName string           `json:"provider_name"`
	Placeholder  bool             `json:"placeholder,omitempty"`
	Images       []Output         `json:"images,omitempty"`
	Media        *Output          `json:"media,omitempty"`
	Text         string           `json:"text,omitempty"`
	Cost         jobs.Cost        `json:"cost"`
	Seconds      float64          `json:"seconds"`
	Note         string           `json:"note,omitempty"`
	Attempts     []Attempt        `json:"attempts,omitempty"`
	Decision     *router.Decision `json:"decision,omitempty"`
	// WorkerOutput is the raw output of a worker job (training, scoring, detection).
	WorkerOutput json.RawMessage `json:"worker_output,omitempty"`
	DecisionID   string          `json:"decision_id,omitempty"`
}

// TextResult is generated text.
type TextResult struct {
	Text        string    `json:"text"`
	ModelID     string    `json:"model_id"`
	ProviderID  string    `json:"provider_id"`
	Placeholder bool      `json:"placeholder,omitempty"`
	Cost        jobs.Cost `json:"cost"`
}

// managedDecision is stored on jobs; Managed marks worker jobs whose fallback
// the parent Run handles (the queue's failover leaves them alone).
type managedDecision struct {
	router.Decision
	Managed bool     `json:"managed,omitempty"`
	Tried   []string `json:"tried,omitempty"`
}

// ErrNoModel is returned when no model can do the task.
var ErrNoModel = errors.New("ai: no model can do this task")

// Run routes need, calls the picked provider (or re-dispatches to a worker),
// streams progress to run, records spend and falls back to the next candidate
// on failure, timeout or a safety block, logging every switch in the job log.
// run may be nil for synchronous calls outside the queue.
func (s *Service) Run(ctx context.Context, run *jobs.Run, need router.Need, req Request) (*Result, error) {
	if run != nil {
		if need.UserID == "" {
			need.UserID = run.Job.UserID
		}
		if need.ProjectID == "" {
			need.ProjectID = run.Job.Scope.ProjectID
		}
		if need.JobType == "" {
			need.JobType = run.Job.Type
		}
	}
	if need.Count == 0 {
		need.Count = max(1, req.N)
	}
	if need.Width == 0 && need.Height == 0 {
		need.Width, need.Height = req.Width, req.Height
	}
	if need.Refs == 0 {
		need.Refs = len(req.Refs)
	}
	prompt := req.Prompt
	if req.Text != nil && prompt == "" {
		prompt = req.Text.Prompt
	}
	dec, err := s.Route(ctx, need, prompt)
	if err != nil {
		return nil, err
	}
	decID := s.saveDecision(ctx, run, need, dec)
	pick := dec.Pick()
	if pick == nil {
		return nil, jobs.Permanent(jobs.ErrCodeBadInput, ErrNoModel)
	}
	if run != nil {
		s.markJob(ctx, run.Job.ID, pick, dec)
	}
	tried := map[string]bool{}
	var attempts []Attempt
	var lastErr error
	for cand := pick; cand != nil; {
		tried[cand.ModelID+"|"+cand.ProviderID] = true
		res, err := s.runCandidate(ctx, run, *cand, need, req)
		if err == nil {
			res.Attempts, res.Decision, res.DecisionID = attempts, dec, decID
			s.finishDecision(ctx, decID, cand, "done")
			return res, nil
		}
		if errors.Is(err, jobs.ErrCanceled) || errors.Is(err, context.Canceled) && ctx.Err() != nil {
			s.finishDecision(ctx, decID, cand, "canceled")
			return nil, jobs.ErrCanceled
		}
		code := providers.CodeOf(err)
		attempts = append(attempts, Attempt{ModelID: cand.ModelID, ProviderID: cand.ProviderID, Code: code, Error: err.Error()})
		s.bumpStats(ctx, cand.ModelID, cand.ProviderID, need.Task, false, 0, 0)
		lastErr = err
		next := dec.Next(cand.ModelID, cand.ProviderID, tried)
		if next == nil {
			break
		}
		if run != nil {
			msg := fmt.Sprintf("%s via %s failed (%s); switching to %s via %s", cand.ModelName, cand.ProviderName, code, next.ModelName, next.ProviderName)
			_ = s.d.Jobs.Log(ctx, run.Job.ID, "fallback", msg, map[string]any{"from_model": cand.ModelID, "from_provider": cand.ProviderID,
				"to_model": next.ModelID, "to_provider": next.ProviderID, "code": code, "error": truncate(err.Error(), 300)})
			s.markJob(ctx, run.Job.ID, next, dec)
		}
		cand = next
	}
	s.finishDecision(ctx, decID, nil, "failed")
	code := providers.CodeOf(lastErr)
	jcode := jobs.ErrCodeInternal
	switch code {
	case providers.CodeSafety:
		jcode = jobs.ErrCodeSafety
	case providers.CodeTimeout:
		jcode = jobs.ErrCodeTimeout
	case providers.CodeBadRequest:
		jcode = jobs.ErrCodeBadInput
	}
	return nil, jobs.Permanent(jcode, fmt.Errorf("every candidate failed; last: %w", lastErr))
}

// Text runs a text task synchronously (script, translation, alt-text…).
func (s *Service) Text(ctx context.Context, need router.Need, req TextRequest) (*TextResult, error) {
	if need.Task == "" {
		need.Task = catalog.TaskText
	}
	res, err := s.Run(ctx, nil, need, Request{Text: &req})
	if err != nil {
		return nil, err
	}
	return &TextResult{Text: res.Text, ModelID: res.ModelID, ProviderID: res.ProviderID, Placeholder: res.Placeholder, Cost: res.Cost}, nil
}

// runCandidate executes one candidate.
func (s *Service) runCandidate(ctx context.Context, run *jobs.Run, c router.Candidate, need router.Need, req Request) (*Result, error) {
	m, ok := s.Catalog(ctx).Get(c.ModelID)
	if !ok {
		return nil, providers.Errf(providers.CodeUnavailable, "model %s is no longer in the catalogue", c.ModelID)
	}
	p, err := s.GetProvider(ctx, c.ProviderID)
	if err != nil {
		return nil, providers.Errf(providers.CodeUnavailable, "provider %s is gone", c.ProviderID)
	}
	if !p.Enabled {
		return nil, providers.Errf(providers.CodeUnavailable, "provider %s is disabled", p.Name)
	}
	if !p.Local() && s.d.Settings.MustInstance(ctx).LocalOnly {
		return nil, providers.Errf(providers.CodeUnavailable, "local only mode blocks remote providers")
	}
	res := &Result{ModelID: m.ID, ModelName: m.Name, ProviderID: p.ID, ProviderKind: p.Kind, ProviderName: p.Name, Placeholder: p.Kind == providers.KindPlaceholder}
	start := s.Now()
	if p.Kind == providers.KindLocalWorker {
		if err := s.viaWorker(ctx, run, c, m, need, req, res); err != nil {
			return nil, err
		}
		s.bumpStats(ctx, m.ID, p.ID, need.Task, true, res.Seconds, 0)
		return res, nil
	}
	cfg, err := s.adapterConfig(p)
	if err != nil {
		return nil, providers.Errf(providers.CodeAuth, "%v", err)
	}
	adapter, ok := s.registryFor(p).Get(p.Kind)
	if !ok {
		return nil, providers.Errf(providers.CodeUnsupported, "no adapter for %s", p.Kind)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	progress := func(v float64, msg string) {
		if run == nil {
			return
		}
		if err := run.Progress(ctx, math.Min(0.95, v), 0, 0, msg); errors.Is(err, jobs.ErrCanceled) {
			cancel()
		}
	}
	progress(0.02, "sending to "+p.Name)
	var reported float64
	kind := m.Kind
	switch {
	case kind == catalog.KindText:
		tg, ok := adapter.(providers.TextGenerator)
		if !ok || req.Text == nil {
			return nil, providers.Errf(providers.CodeUnsupported, "%s cannot generate text", p.Name)
		}
		tctx, tcancel := context.WithTimeout(cctx, 3*time.Minute)
		defer tcancel()
		tr := providers.TextRequest{Model: c.ProviderModel, System: req.Text.System, Messages: req.Text.Messages, MaxTokens: req.Text.MaxTokens,
			JSON: req.Text.JSON, Temperature: req.Text.Temperature, Task: need.Task, Lang: req.Text.Lang}
		if req.Text.Prompt != "" {
			tr.Messages = append(tr.Messages, providers.Message{Role: "user", Content: req.Text.Prompt})
		}
		out, err := tg.Text(tctx, cfg, tr)
		if err != nil {
			return nil, ctxErr(ctx, err)
		}
		res.Text = out.Text
		reported = out.CostUSD
		if reported == 0 {
			if o, ok := offerFor(m, p); ok {
				reported = o.Price.Estimate(0, 0, 0, out.TokensIn, out.TokensOut)
			}
		}
	case kind == catalog.KindVideo || kind == catalog.KindVoice:
		mg, ok := adapter.(providers.MediaGenerator)
		if !ok {
			return nil, providers.Errf(providers.CodeUnsupported, "%s cannot generate %s", p.Name, kind)
		}
		mctx, mcancel := context.WithTimeout(cctx, 15*time.Minute)
		defer mcancel()
		mr := providers.MediaRequest{Task: need.Task, Model: c.ProviderModel, Prompt: req.Prompt, Text: req.Speech, Voice: req.Voice,
			Seconds: req.Seconds, Aspect: req.Aspect, Width: req.Width, Height: req.Height}
		if req.Image != nil {
			img, err := s.load(ctx, req.Image)
			if err != nil {
				return nil, err
			}
			mr.Image = img
		}
		out, err := mg.Media(mctx, cfg, mr, progress)
		if err != nil {
			return nil, ctxErr(ctx, err)
		}
		o, err := s.store(ctx, run, "media", out.Media, 0)
		if err != nil {
			return nil, err
		}
		res.Media = &o
		reported = out.CostUSD
		if reported == 0 {
			if off, ok := offerFor(m, p); ok {
				reported = off.Price.Estimate(req.Width, req.Height, max(req.Seconds, out.Seconds), 0, 0)
			}
		}
	default:
		ig, ok := adapter.(providers.ImageGenerator)
		if !ok {
			return nil, providers.Errf(providers.CodeUnsupported, "%s cannot generate images", p.Name)
		}
		ir, err := s.imageRequest(ctx, c, need, req)
		if err != nil {
			return nil, err
		}
		ictx, icancel := context.WithTimeout(cctx, 8*time.Minute)
		defer icancel()
		out, err := ig.Image(ictx, cfg, ir, progress)
		if err != nil {
			return nil, ctxErr(ctx, err)
		}
		for i, img := range out.Images {
			o, err := s.store(ctx, run, fmt.Sprintf("image-%d", i+1), img, img.Seed)
			if err != nil {
				return nil, err
			}
			res.Images = append(res.Images, o)
		}
		res.Note = out.Note
		reported = out.CostUSD
		if reported == 0 && !p.Local() {
			if off, ok := offerFor(m, p); ok {
				w, h := ir.Size()
				reported = off.Price.Estimate(w, h, 0, 0, 0) * float64(len(out.Images))
			} else if p.Config.V.CostPerImage > 0 {
				reported = p.Config.V.CostPerImage * float64(len(out.Images))
			}
		}
	}
	res.Seconds = s.Now().Sub(start).Seconds()
	if p.Local() {
		// Local runtimes (ComfyUI, Ollama, a LAN vLLM) cost GPU time, not money.
		res.Cost = jobs.Cost{GPUSeconds: math.Round(res.Seconds*10) / 10}
		s.ledger(ctx, run, need, m.ID, p.ID, "gpu", 0, res.Cost.GPUSeconds)
	} else {
		res.Cost = jobs.Cost{USD: math.Round(reported*10000) / 10000}
		s.ledger(ctx, run, need, m.ID, p.ID, "remote", res.Cost.USD, 0)
	}
	if run != nil {
		run.AddActual(res.Cost)
	}
	s.bumpStats(ctx, m.ID, p.ID, need.Task, true, res.Seconds, res.Cost.USD)
	return res, nil
}

// ctxErr turns the caller's cancellation into ErrCanceled and keeps other errors.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return jobs.ErrCanceled
	}
	if errors.Is(err, context.Canceled) {
		return jobs.ErrCanceled
	}
	return err
}

// offerFor returns the manifest offer served by provider p.
func offerFor(m *catalog.Manifest, p *Provider) (catalog.Offer, bool) {
	if o, ok := m.Offer(p.ID); ok {
		return o, true
	}
	return m.Offer(p.Kind)
}

// imageRequest loads inputs and builds the adapter request.
func (s *Service) imageRequest(ctx context.Context, c router.Candidate, need router.Need, req Request) (providers.ImageRequest, error) {
	ir := providers.ImageRequest{Task: need.Task, Model: c.ProviderModel, Prompt: req.Prompt, Negative: req.Negative, Width: req.Width,
		Height: req.Height, N: req.N, Seed: req.Seed, Steps: req.Steps, Guidance: req.Guidance, Style: req.Style, LoRAs: req.LoRAs,
		Scale: req.Scale, Label: req.Label}
	if ir.Width == 0 && need.Width > 0 {
		ir.Width, ir.Height = need.Width, need.Height
	}
	var err error
	if req.Image != nil {
		if ir.Image, err = s.load(ctx, req.Image); err != nil {
			return ir, err
		}
	}
	if req.Mask != nil {
		if ir.Mask, err = s.load(ctx, req.Mask); err != nil {
			return ir, err
		}
	}
	if req.PoseImage != nil {
		if ir.PoseImage, err = s.load(ctx, req.PoseImage); err != nil {
			return ir, err
		}
	}
	for i := range req.Refs {
		img, err := s.load(ctx, &req.Refs[i])
		if err != nil {
			return ir, err
		}
		ir.Refs = append(ir.Refs, *img)
	}
	return ir, nil
}

// load reads an input's bytes (from storage when only a key is given).
func (s *Service) load(ctx context.Context, in *Input) (*providers.Image, error) {
	if len(in.Data) > 0 {
		mime := in.MIME
		if mime == "" {
			mime = http.DetectContentType(in.Data)
		}
		return &providers.Image{Data: in.Data, MIME: mime}, nil
	}
	if in.Key == "" {
		return nil, providers.Errf(providers.CodeBadRequest, "empty input")
	}
	obj, info, err := s.d.Store.Get(ctx, in.Key)
	if err != nil {
		return nil, providers.Errf(providers.CodeBadRequest, "input %s: %v", in.Key, err)
	}
	defer obj.Close()
	b, err := io.ReadAll(io.LimitReader(obj, 64<<20))
	if err != nil {
		return nil, err
	}
	mime := info.ContentType
	if mime == "" {
		mime = http.DetectContentType(b)
	}
	return &providers.Image{Data: b, MIME: mime}, nil
}

// ensureKey stores an input that only has bytes so a worker can download it.
func (s *Service) ensureKey(ctx context.Context, prefix, name string, in *Input) (string, error) {
	if in.Key != "" {
		return in.Key, nil
	}
	mime := in.MIME
	if mime == "" {
		mime = http.DetectContentType(in.Data)
	}
	key := prefix + "/" + name + extFor(mime)
	if err := s.d.Store.Put(ctx, key, bytes.NewReader(in.Data), int64(len(in.Data)), mime); err != nil {
		return "", err
	}
	in.Key = key
	return key, nil
}

func extFor(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/png"):
		return ".png"
	case strings.HasPrefix(mime, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mime, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mime, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mime, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(mime, "video/webm"):
		return ".webm"
	case strings.HasPrefix(mime, "audio/mpeg"):
		return ".mp3"
	case strings.HasPrefix(mime, "audio/wav"), strings.HasPrefix(mime, "audio/x-wav"):
		return ".wav"
	case strings.HasPrefix(mime, "audio/ogg"):
		return ".ogg"
	}
	return ".bin"
}

// outputPrefix is where a run's files go.
func outputPrefix(run *jobs.Run) string {
	if run != nil {
		return "ai/jobs/" + run.Job.ID
	}
	return "ai/runs/" + db.NewID()
}

// store saves one produced file.
func (s *Service) store(ctx context.Context, run *jobs.Run, name string, img providers.Image, seed int64) (Output, error) {
	mime := img.MIME
	if mime == "" || mime == "application/octet-stream" {
		mime = http.DetectContentType(img.Data)
	}
	key := outputPrefix(run) + "/" + name + "-" + strings.ToLower(db.NewID()[18:]) + extFor(mime)
	if err := s.d.Store.Put(ctx, key, bytes.NewReader(img.Data), int64(len(img.Data)), mime); err != nil {
		return Output{}, fmt.Errorf("ai: store output: %w", err)
	}
	o := Output{Key: key, MIME: mime, Seed: seed}
	if strings.HasPrefix(mime, "image/") {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data)); err == nil {
			o.Width, o.Height = cfg.Width, cfg.Height
		}
	}
	return o, nil
}

// workerInput builds the worker protocol input for a task (sdk/api/worker.md).
func workerInput(need router.Need, req Request) map[string]any {
	in := map[string]any{"prompt": req.Prompt}
	n := max(1, min(req.N, 4))
	seed := any(nil)
	if req.Seed != nil {
		seed = *req.Seed
	}
	switch need.Task {
	case catalog.TaskGenerate:
		w, h := req.Width, req.Height
		if w == 0 || h == 0 {
			w, h = 1024, 1024
		}
		in["negative"], in["width"], in["height"], in["n"], in["seed"] = req.Negative, w, h, n, seed
		if req.Steps > 0 {
			in["steps"] = req.Steps
		}
		if req.Guidance > 0 {
			in["guidance"] = req.Guidance
		}
		if req.Style != "" {
			in["style"] = req.Style
		}
		if len(req.LoRAs) > 0 {
			in["loras"] = req.LoRAs
		}
		if len(req.Pose) > 0 {
			in["pose"] = req.Pose
		}
	case catalog.TaskEdit:
		strength := req.Strength
		if strength == 0 {
			strength = 0.6
		}
		in["n"], in["seed"], in["strength"] = n, seed, strength
	case catalog.TaskInpaint:
		in["n"], in["seed"], in["feather_px"] = n, seed, max(req.FeatherPx, 0)
	case catalog.TaskSheet:
		in["n"], in["seed"] = n, seed
		if req.Style != "" {
			in["style"] = req.Style
		}
	case catalog.TaskUpscale:
		in = map[string]any{"scale": max(2, min(req.Scale, 4))}
	case catalog.TaskOCR:
		in = map[string]any{"lang": req.Aspect}
	case catalog.TaskScore, catalog.TaskTrain:
		in = map[string]any{}
	case catalog.TaskVideo:
		in = map[string]any{"width": req.Width, "height": req.Height, "fps": 24}
	case catalog.TaskVoice:
		in = map[string]any{"text": req.Speech, "voice": req.Voice}
	}
	for k, v := range req.Extra {
		in[k] = v
	}
	return in
}

// viaWorker re-dispatches the job to an AI worker as a child job and waits for it.
func (s *Service) viaWorker(ctx context.Context, run *jobs.Run, c router.Candidate, m *catalog.Manifest, need router.Need, req Request, res *Result) error {
	jobType := catalog.JobTypeForTask(need.Task)
	if jobType == "" {
		return providers.Errf(providers.CodeUnsupported, "workers do not run %s", need.Task)
	}
	prefix := "ai/inputs/" + db.NewID()
	if run != nil {
		prefix = "ai/jobs/" + run.Job.ID + "/inputs"
	}
	files := map[string]string{}
	put := func(name string, in *Input) error {
		if in == nil {
			return nil
		}
		key, err := s.ensureKey(ctx, prefix, name, in)
		if err != nil {
			return err
		}
		files[name] = key
		return nil
	}
	if err := put("image", req.Image); err != nil {
		return err
	}
	if err := put("mask", req.Mask); err != nil {
		return err
	}
	if err := put("pose_image", req.PoseImage); err != nil {
		return err
	}
	for i := range req.Refs {
		name := fmt.Sprintf("ref_%d", i)
		if need.Task == catalog.TaskScore {
			name = fmt.Sprintf("reference_%d", i)
		}
		if err := put(name, &req.Refs[i]); err != nil {
			return err
		}
	}
	modelID := m.ID
	if m.Hardware.AnyWorker {
		modelID = ""
	}
	p := jobs.EnqueueParams{Type: jobType, Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: need.UserID, Executor: jobs.ExecutorWorker,
		ModelID: modelID, ProviderID: c.ProviderID, Decision: managedDecision{Managed: true}, Input: workerInput(need, req), Files: files,
		Estimate: jobs.Cost{GPUSeconds: c.Estimate.GPUSeconds}, MaxAttempts: 1, Priority: 1}
	if run != nil {
		p.Scope, p.ParentID, p.Priority, p.Batch = run.Job.Scope, run.Job.ID, run.Job.Priority, run.Job.Batch
		if p.UserID == "" {
			p.UserID = run.Job.UserID
		}
	}
	child, err := s.d.Jobs.Enqueue(ctx, p)
	if err != nil {
		return providers.Errf(providers.CodeBadRequest, "cannot queue the worker job: %v", err)
	}
	if run != nil {
		_ = s.d.Jobs.Log(ctx, run.Job.ID, "dispatched", "sent to the local worker", map[string]any{"child": child.ID, "model_id": modelID})
	}
	timeout := 30 * time.Minute
	if need.Task == catalog.TaskTrain {
		timeout = 6 * time.Hour
	}
	deadline := time.Now().Add(timeout)
	lastProgress := -1.0
	for {
		j, err := s.d.Jobs.Get(ctx, child.ID)
		if err != nil {
			return err
		}
		if j.Finished() {
			res.Seconds = j.Actual.GPUSeconds
			res.Cost = j.Actual
			if run != nil {
				run.AddActual(j.Actual)
			}
			switch j.State {
			case jobs.StateDone:
				return s.collectWorker(ctx, j, res)
			case jobs.StateCanceled:
				return jobs.ErrCanceled
			default:
				code := providers.CodeInternal
				msg := "the worker job failed"
				if j.Error != nil {
					msg = j.Error.Message
					switch j.Error.Code {
					case jobs.ErrCodeSafety:
						code = providers.CodeSafety
					case jobs.ErrCodeTimeout:
						code = providers.CodeTimeout
					case jobs.ErrCodeBadInput:
						code = providers.CodeBadRequest
					case jobs.ErrCodeOOM:
						code = providers.CodeUnavailable
					}
				}
				return &providers.Error{Code: code, Message: msg}
			}
		}
		if run != nil {
			if j.Progress != lastProgress || j.State == jobs.StateQueued {
				msg := j.Message
				if j.State == jobs.StateQueued {
					pos, _ := s.d.Jobs.Position(ctx, j)
					msg = fmt.Sprintf("waiting for the local worker (#%d)", pos+1)
				}
				if err := run.Progress(ctx, math.Min(0.97, j.Progress), j.Step, j.Steps, msg); errors.Is(err, jobs.ErrCanceled) {
					_, _ = s.d.Jobs.Cancel(context.WithoutCancel(ctx), child.ID)
					return jobs.ErrCanceled
				}
				lastProgress = j.Progress
			}
		}
		if time.Now().After(deadline) {
			_, _ = s.d.Jobs.Cancel(context.WithoutCancel(ctx), child.ID)
			return providers.Errf(providers.CodeTimeout, "the worker did not finish in %s", timeout)
		}
		select {
		case <-ctx.Done():
			_, _ = s.d.Jobs.Cancel(context.WithoutCancel(ctx), child.ID)
			return jobs.ErrCanceled
		case <-time.After(s.Wait):
		}
	}
}

// collectWorker reads a finished worker job's output into res.
func (s *Service) collectWorker(ctx context.Context, j *jobs.Job, res *Result) error {
	res.WorkerOutput = j.Output
	var out struct {
		Images []struct {
			Key  string `json:"key"`
			Seed int64  `json:"seed"`
		} `json:"images"`
		Video *struct {
			Key string `json:"key"`
		} `json:"video"`
		Audio *struct {
			Key string `json:"key"`
		} `json:"audio"`
	}
	if err := j.DecodeOutput(&out); err != nil {
		return providers.Errf(providers.CodeInternal, "unreadable worker output: %v", err)
	}
	for _, im := range out.Images {
		o := Output{Key: im.Key, Seed: im.Seed}
		if info, err := s.d.Store.Stat(ctx, im.Key); err == nil {
			o.MIME = info.ContentType
		}
		if obj, _, err := s.d.Store.Get(ctx, im.Key); err == nil {
			if cfg, _, err := image.DecodeConfig(obj); err == nil {
				o.Width, o.Height = cfg.Width, cfg.Height
			}
			obj.Close()
		}
		res.Images = append(res.Images, o)
	}
	if out.Video != nil {
		res.Media = &Output{Key: out.Video.Key}
	} else if out.Audio != nil {
		res.Media = &Output{Key: out.Audio.Key}
	}
	return nil
}

// ledger records spend: remote USD on own keys or local GPU seconds.
func (s *Service) ledger(ctx context.Context, run *jobs.Run, need router.Need, modelID, providerID, kind string, usd, gpu float64) {
	if usd == 0 && gpu == 0 {
		return
	}
	var jobID any
	if run != nil {
		jobID = run.Job.ID
	}
	if _, err := s.d.DB.Exec(ctx, `INSERT INTO spend_ledger (id, user_id, project_id, job_id, provider_id, model_id, kind, usd, gpu_seconds, month, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, db.NewID(), db.NullString(need.UserID), need.ProjectID, jobID, providerID, modelID, kind, usd, gpu,
		s.monthKey(), db.Now()); err != nil {
		s.log.Warn("ai: spend ledger", "err", err)
	}
}

// bumpStats updates model_stats.
func (s *Service) bumpStats(ctx context.Context, modelID, providerID, task string, ok bool, secs, usd float64) {
	fail := 0
	if !ok {
		fail = 1
	}
	if _, err := s.d.DB.Exec(ctx, `INSERT INTO model_stats (model_id, provider_id, task, runs, failures, total_secs, total_usd, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?)
		ON CONFLICT (model_id, provider_id, task) DO UPDATE SET runs = model_stats.runs + 1, failures = model_stats.failures + excluded.failures,
		total_secs = model_stats.total_secs + excluded.total_secs, total_usd = model_stats.total_usd + excluded.total_usd, updated_at = excluded.updated_at`,
		modelID, providerID, task, fail, secs, usd, db.Now()); err != nil {
		s.log.Warn("ai: model stats", "err", err)
	}
}

// Feedback records that a member kept or rerolled a result (keep/reroll learning).
func (s *Service) Feedback(ctx context.Context, modelID, providerID, task string, kept bool) error {
	col := "rerolls"
	if kept {
		col = "keeps"
	}
	_, err := s.d.DB.Exec(ctx, `INSERT INTO model_stats (model_id, provider_id, task, `+col+`, updated_at) VALUES (?, ?, ?, 1, ?)
		ON CONFLICT (model_id, provider_id, task) DO UPDATE SET `+col+` = model_stats.`+col+` + 1, updated_at = excluded.updated_at`,
		modelID, providerID, task, db.Now())
	return err
}

// saveDecision stores the decision for G11 and returns its id.
func (s *Service) saveDecision(ctx context.Context, run *jobs.Run, need router.Need, dec *router.Decision) string {
	id := db.NewID()
	var jobID any
	if run != nil {
		jobID = run.Job.ID
	}
	chosenModel, chosenProvider := "", ""
	if p := dec.Pick(); p != nil {
		chosenModel, chosenProvider = p.ModelID, p.ProviderID
	}
	cands := struct {
		Chain   []router.Candidate `json:"chain"`
		Skipped []router.Candidate `json:"skipped"`
		Warn    []router.Reason    `json:"warnings,omitempty"`
		PinID   string             `json:"pin_id,omitempty"`
	}{dec.Chain, dec.Skipped, dec.Warnings, dec.PinID}
	if _, err := s.d.DB.Exec(ctx, `INSERT INTO routing_decisions (id, job_id, user_id, project_id, task, mode, chosen_model_id, chosen_provider_id,
		candidates, explanation, overridden, label, need, why, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, jobID, db.NullString(need.UserID), need.ProjectID, need.Task, dec.Mode, chosenModel, chosenProvider, db.MustJSON(cands),
		"", dec.Overridden, truncate(need.Label, 120), db.MustJSON(need), db.MustJSON(dec.Why), "running", db.Now()); err != nil {
		s.log.Warn("ai: save decision", "err", err)
	}
	return id
}

func (s *Service) finishDecision(ctx context.Context, id string, c *router.Candidate, status string) {
	q, args := `UPDATE routing_decisions SET status = ?`, []any{status}
	if c != nil {
		q += `, chosen_model_id = ?, chosen_provider_id = ?`
		args = append(args, c.ModelID, c.ProviderID)
	}
	if _, err := s.d.DB.Exec(ctx, q+` WHERE id = ?`, append(args, id)...); err != nil {
		s.log.Warn("ai: finish decision", "err", err)
	}
}

// markJob stores the current model, provider and decision on the job row.
func (s *Service) markJob(ctx context.Context, jobID string, c *router.Candidate, dec *router.Decision) {
	if _, err := s.d.DB.Exec(ctx, `UPDATE jobs SET model_id = ?, provider_id = ?, decision = ? WHERE id = ?`,
		c.ModelID, c.ProviderID, db.MustJSON(dec), jobID); err != nil {
		s.log.Warn("ai: mark job", "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
