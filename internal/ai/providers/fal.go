package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Fal speaks the fal.ai queue API: POST https://queue.fal.run/{model} submits
// a request, its status_url reports IN_QUEUE / IN_PROGRESS / COMPLETED and its
// response_url returns the result ({"images":[{"url"}]}, {"video":{"url"}} or
// {"audio":{"url"}}). Inputs that are files are sent as data URIs.
type Fal struct{ Env Env }

// Kind implements Adapter.
func (Fal) Kind() string { return KindFal }

func (a Fal) base(cfg Config) string { return baseOr(cfg, "https://queue.fal.run") }

func (a Fal) headers(cfg Config) map[string]string {
	return map[string]string{"Authorization": "Key " + cfg.APIKey}
}

type falSubmit struct {
	RequestID   string `json:"request_id"`
	StatusURL   string `json:"status_url"`
	ResponseURL string `json:"response_url"`
}

type falStatus struct {
	Status        string `json:"status"`
	QueuePosition *int   `json:"queue_position"`
	Logs          []struct {
		Message string `json:"message"`
	} `json:"logs"`
	Error string `json:"error"`
}

// run submits input to model and waits for the JSON result.
func (a Fal) run(ctx context.Context, cfg Config, model string, input map[string]any, progress Progress) (json.RawMessage, error) {
	var sub falSubmit
	if err := a.Env.call(ctx, http.MethodPost, joinURL(a.base(cfg), model), a.headers(cfg), input, &sub); err != nil {
		return nil, err
	}
	if sub.StatusURL == "" || sub.ResponseURL == "" {
		base := joinURL(a.base(cfg), appID(model)+"/requests/"+sub.RequestID)
		sub.StatusURL, sub.ResponseURL = base+"/status", base
	}
	deadline := time.Now().Add(a.Env.maxWait())
	for {
		var st falStatus
		if err := a.Env.call(ctx, http.MethodGet, sub.StatusURL+"?logs=1", a.headers(cfg), nil, &st); err != nil {
			return nil, err
		}
		switch st.Status {
		case "COMPLETED":
			progress.report(0.95, "downloading")
			var raw json.RawMessage
			if err := a.Env.call(ctx, http.MethodGet, sub.ResponseURL, a.headers(cfg), nil, &raw); err != nil {
				return nil, err
			}
			if st.Error != "" {
				if looksLikeSafety(st.Error) {
					return nil, Errf(CodeSafety, "%s", st.Error)
				}
				return nil, Errf(CodeInternal, "%s", st.Error)
			}
			return raw, nil
		case "IN_QUEUE":
			msg := "queued at fal"
			if st.QueuePosition != nil {
				msg = "queued at fal (#" + itoa(*st.QueuePosition+1) + ")"
			}
			progress.report(0.05, msg)
		case "IN_PROGRESS":
			msg := "generating at fal"
			if n := len(st.Logs); n > 0 && st.Logs[n-1].Message != "" {
				msg = st.Logs[n-1].Message
			}
			progress.report(0.5, msg)
		default:
			if st.Error != "" {
				return nil, Errf(CodeInternal, "%s", st.Error)
			}
		}
		if time.Now().After(deadline) {
			_ = a.Env.call(context.WithoutCancel(ctx), http.MethodPut, strings.TrimSuffix(sub.StatusURL, "/status")+"/cancel", a.headers(cfg), nil, nil)
			return nil, Errf(CodeTimeout, "fal did not finish in %s", a.Env.maxWait())
		}
		if err := sleep(ctx, a.Env.poll()); err != nil {
			return nil, err
		}
	}
}

// appID is the "owner/app" part of a model path ("fal-ai/flux-2/klein" → "fal-ai/flux-2").
func appID(model string) string {
	parts := strings.Split(model, "/")
	if len(parts) > 2 {
		return parts[0] + "/" + parts[1]
	}
	return model
}

// Image runs an image model.
func (a Fal) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	in := map[string]any{"prompt": stylePrompt(req.Prompt, req.Style), "num_images": req.Count(),
		"image_size": map[string]int{"width": w, "height": h}, "output_format": "png", "enable_safety_checker": true}
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
		in["guidance_scale"] = req.Guidance
	}
	var urls []string
	if req.Image != nil {
		in["image_url"] = DataURI(*req.Image)
		urls = append(urls, DataURI(*req.Image))
	}
	for _, r := range req.Refs {
		urls = append(urls, DataURI(r))
	}
	if len(urls) > 0 {
		in["image_urls"] = urls
	}
	if req.Mask != nil {
		in["mask_url"] = DataURI(*req.Mask)
	}
	if len(req.LoRAs) > 0 {
		var ls []map[string]any
		for _, l := range req.LoRAs {
			if l.URL != "" {
				ls = append(ls, map[string]any{"path": l.URL, "scale": l.Weight})
			}
		}
		if len(ls) > 0 {
			in["loras"] = ls
		}
	}
	if req.Task == "upscale" {
		in = map[string]any{"image_url": DataURI(*req.Image), "scale": max(req.Scale, 2)}
	}
	raw, err := a.run(ctx, cfg, req.Model, in, progress)
	if err != nil {
		return nil, err
	}
	var out struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
		Image *struct {
			URL string `json:"url"`
		} `json:"image"`
		Seed            int64  `json:"seed"`
		HasNSFWConcepts []bool `json:"has_nsfw_concepts"`
		RevisedPrompt   string `json:"revised_prompt"`
	}
	if err := jsonUnmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.Image != nil {
		out.Images = append(out.Images, struct {
			URL string `json:"url"`
		}{out.Image.URL})
	}
	res := &ImageResult{Note: out.RevisedPrompt}
	for i, im := range out.Images {
		if i < len(out.HasNSFWConcepts) && out.HasNSFWConcepts[i] {
			continue
		}
		img, err := a.Env.fetch(ctx, im.URL, nil)
		if err != nil {
			return nil, err
		}
		img.Seed = out.Seed
		res.Images = append(res.Images, img)
	}
	if len(res.Images) == 0 {
		if len(out.HasNSFWConcepts) > 0 {
			return nil, Errf(CodeSafety, "fal's safety checker blocked every image")
		}
		return nil, Errf(CodeInternal, "fal returned no images")
	}
	return res, nil
}

// Media runs a video or text-to-speech model.
func (a Fal) Media(ctx context.Context, cfg Config, req MediaRequest, progress Progress) (*MediaResult, error) {
	in := map[string]any{}
	switch req.Task {
	case "video":
		in["prompt"] = req.Prompt
		if req.Image != nil {
			in["image_url"] = DataURI(*req.Image)
		}
		if req.Seconds > 0 {
			in["duration"] = strings.TrimSuffix(strings.TrimSuffix(ftoa(req.Seconds), ".0"), ".00")
		}
		if req.Aspect != "" {
			in["aspect_ratio"] = req.Aspect
		}
	case "voice":
		in["prompt"] = req.Text
		in["text"] = req.Text
		if req.Voice != "" {
			in["voice"] = req.Voice
		}
	default:
		return nil, unsupported("fal", req.Task)
	}
	raw, err := a.run(ctx, cfg, req.Model, in, progress)
	if err != nil {
		return nil, err
	}
	var out struct {
		Video *struct {
			URL string `json:"url"`
		} `json:"video"`
		Audio *struct {
			URL string `json:"url"`
		} `json:"audio"`
		AudioURL *struct {
			URL string `json:"url"`
		} `json:"audio_url"`
	}
	if err := jsonUnmarshal(raw, &out); err != nil {
		return nil, err
	}
	url := ""
	switch {
	case out.Video != nil:
		url = out.Video.URL
	case out.Audio != nil:
		url = out.Audio.URL
	case out.AudioURL != nil:
		url = out.AudioURL.URL
	}
	if url == "" {
		return nil, Errf(CodeInternal, "fal returned no media")
	}
	m, err := a.Env.fetch(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	return &MediaResult{Media: m, Seconds: req.Seconds}, nil
}
