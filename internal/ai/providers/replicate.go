package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Replicate speaks the Replicate predictions API. "owner/name" model ids use
// POST /v1/models/{owner}/{name}/predictions, "owner/name:version" ids use
// POST /v1/predictions with the version. Predictions are polled through
// urls.get until they succeed, fail or are canceled; metrics.predict_time is
// the billed GPU time.
type Replicate struct{ Env Env }

// Kind implements Adapter.
func (Replicate) Kind() string { return KindReplicate }

func (a Replicate) base(cfg Config) string { return baseOr(cfg, "https://api.replicate.com/v1") }

type replicatePrediction struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Output json.RawMessage `json:"output"`
	Error  any             `json:"error"`
	Logs   string          `json:"logs"`
	URLs   struct {
		Get    string `json:"get"`
		Cancel string `json:"cancel"`
	} `json:"urls"`
	Metrics struct {
		PredictTime float64 `json:"predict_time"`
	} `json:"metrics"`
}

// run creates a prediction and waits for it.
func (a Replicate) run(ctx context.Context, cfg Config, model string, input map[string]any, progress Progress) (*replicatePrediction, error) {
	url, body := "", map[string]any{"input": input}
	if owner, version, ok := strings.Cut(model, ":"); ok {
		url = joinURL(a.base(cfg), "predictions")
		body["version"] = version
		_ = owner
	} else {
		url = joinURL(a.base(cfg), "models/"+model+"/predictions")
	}
	h := bearer(cfg.APIKey)
	h["Prefer"] = "wait=5"
	var p replicatePrediction
	if err := a.Env.call(ctx, http.MethodPost, url, h, body, &p); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(a.Env.maxWait())
	for {
		switch p.Status {
		case "succeeded":
			return &p, nil
		case "failed", "canceled":
			msg := errorMessage([]byte(jsonString(p.Error)))
			if msg == "" {
				msg = "prediction " + p.Status
			}
			if looksLikeSafety(msg) {
				return nil, Errf(CodeSafety, "%s", msg)
			}
			return nil, Errf(CodeInternal, "%s", msg)
		case "starting":
			progress.report(0.05, "starting at Replicate (the model may be cold)")
		case "processing":
			progress.report(0.5, "generating at Replicate")
		}
		if time.Now().After(deadline) {
			if p.URLs.Cancel != "" {
				_ = a.Env.call(context.WithoutCancel(ctx), http.MethodPost, p.URLs.Cancel, bearer(cfg.APIKey), nil, nil)
			}
			return nil, Errf(CodeTimeout, "Replicate did not finish in %s", a.Env.maxWait())
		}
		if err := sleep(ctx, a.Env.poll()); err != nil {
			return nil, err
		}
		get := p.URLs.Get
		if get == "" {
			get = joinURL(a.base(cfg), "predictions/"+p.ID)
		}
		if err := a.Env.call(ctx, http.MethodGet, get, bearer(cfg.APIKey), nil, &p); err != nil {
			return nil, err
		}
	}
}

func jsonString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		b, _ := json.Marshal(map[string]string{"message": t})
		return string(b)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// outputURLs returns the URL(s) in a prediction output (string, list or {url}).
func outputURLs(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		var out []string
		for _, k := range []string{"url", "image", "video", "audio", "output"} {
			if s, ok := obj[k].(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Image runs an image model.
func (a Replicate) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	in := map[string]any{"prompt": stylePrompt(req.Prompt, req.Style), "num_outputs": req.Count(), "width": w, "height": h,
		"aspect_ratio": aspect(w, h), "output_format": "png"}
	if req.Negative != "" {
		in["negative_prompt"] = req.Negative
	}
	if req.Seed != nil {
		in["seed"] = *req.Seed
	}
	if req.Steps > 0 {
		in["num_inference_steps"] = req.Steps
	}
	if req.Guidance > 0 {
		in["guidance"] = req.Guidance
	}
	if req.Image != nil {
		in["image"] = DataURI(*req.Image)
		in["input_image"] = DataURI(*req.Image)
	}
	if len(req.Refs) > 0 {
		var refs []string
		for _, r := range req.Refs {
			refs = append(refs, DataURI(r))
		}
		in["image_input"] = refs
	}
	if req.Mask != nil {
		in["mask"] = DataURI(*req.Mask)
	}
	if req.Task == "upscale" && req.Image != nil {
		in = map[string]any{"image": DataURI(*req.Image), "scale": max(req.Scale, 2)}
	}
	p, err := a.run(ctx, cfg, req.Model, in, progress)
	if err != nil {
		return nil, err
	}
	res := &ImageResult{GPUSeconds: p.Metrics.PredictTime}
	for _, u := range outputURLs(p.Output) {
		img, err := a.Env.fetch(ctx, u, nil)
		if err != nil {
			return nil, err
		}
		res.Images = append(res.Images, img)
	}
	if len(res.Images) == 0 {
		return nil, Errf(CodeInternal, "Replicate returned no images")
	}
	return res, nil
}

// Media runs a video or speech model.
func (a Replicate) Media(ctx context.Context, cfg Config, req MediaRequest, progress Progress) (*MediaResult, error) {
	in := map[string]any{}
	switch req.Task {
	case "video":
		in["prompt"] = req.Prompt
		if req.Image != nil {
			in["image"] = DataURI(*req.Image)
			in["start_image"] = DataURI(*req.Image)
		}
		if req.Seconds > 0 {
			in["duration"] = int(req.Seconds + .5)
		}
		if req.Aspect != "" {
			in["aspect_ratio"] = req.Aspect
		}
	case "voice":
		in["text"] = req.Text
		if req.Voice != "" {
			in["voice"] = req.Voice
		}
	default:
		return nil, unsupported("Replicate", req.Task)
	}
	p, err := a.run(ctx, cfg, req.Model, in, progress)
	if err != nil {
		return nil, err
	}
	urls := outputURLs(p.Output)
	if len(urls) == 0 {
		return nil, Errf(CodeInternal, "Replicate returned no media")
	}
	m, err := a.Env.fetch(ctx, urls[0], nil)
	if err != nil {
		return nil, err
	}
	return &MediaResult{Media: m, Seconds: req.Seconds}, nil
}
