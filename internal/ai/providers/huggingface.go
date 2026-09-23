package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// HuggingFace calls Hugging Face inference: a dedicated Inference Endpoint
// when BaseURL is set, otherwise the serverless router
// (https://router.huggingface.co/hf-inference/models/{id}). Text-to-image
// takes {"inputs": prompt, "parameters": {…}} and answers with the image
// bytes; text uses the OpenAI-compatible chat route. Model cards
// (https://huggingface.co/api/models/{id}) fill in the kind and licence.
type HuggingFace struct {
	Env Env
	// HubURL overrides https://huggingface.co (tests).
	HubURL string
	// RouterURL overrides https://router.huggingface.co (tests).
	RouterURL string
}

// Kind implements Adapter.
func (HuggingFace) Kind() string { return KindHuggingFace }

func (a HuggingFace) router() string {
	if a.RouterURL != "" {
		return a.RouterURL
	}
	return "https://router.huggingface.co"
}

func (a HuggingFace) hub() string {
	if a.HubURL != "" {
		return a.HubURL
	}
	return "https://huggingface.co"
}

// Image runs a text-to-image (or image-to-image) pipeline.
func (a HuggingFace) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	target := cfg.BaseURL
	if target == "" {
		target = a.router() + "/hf-inference/models/" + req.Model
	}
	params := map[string]any{"width": w / 8 * 8, "height": h / 8 * 8}
	if req.Negative != "" {
		params["negative_prompt"] = req.Negative
	}
	if req.Steps > 0 {
		params["num_inference_steps"] = req.Steps
	}
	if req.Guidance > 0 {
		params["guidance_scale"] = req.Guidance
	}
	if req.Seed != nil {
		params["seed"] = *req.Seed
	}
	in := map[string]any{"inputs": stylePrompt(req.Prompt, req.Style), "parameters": params}
	if req.Image != nil {
		in["image"] = DataURI(*req.Image)
	}
	res := &ImageResult{}
	for i := 0; i < req.Count(); i++ {
		progress.report(float64(i)/float64(req.Count()), "generating at Hugging Face")
		r, err := newRequest(ctx, http.MethodPost, target, in, bearer(cfg.APIKey))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Accept", "image/png")
		b, hdr, err := a.Env.send(r)
		if err != nil {
			return nil, err
		}
		ct := hdr.Get("Content-Type")
		if strings.HasPrefix(ct, "application/json") {
			// Some endpoints answer with {"image": "<base64>"} or a list of them.
			var one map[string]any
			var many []map[string]any
			var s string
			switch {
			case json.Unmarshal(b, &one) == nil:
				s, _ = one["image"].(string)
			case json.Unmarshal(b, &many) == nil && len(many) > 0:
				s, _ = many[0]["image"].(string)
			}
			if s == "" {
				return nil, Errf(CodeInternal, "unexpected JSON answer: %s", errorMessage(b))
			}
			img, err := decodeB64(s)
			if err != nil {
				return nil, err
			}
			res.Images = append(res.Images, img)
			continue
		}
		res.Images = append(res.Images, Image{Data: b, MIME: http.DetectContentType(b)})
		// A fixed seed would repeat the same image; vary it for the next one.
		if req.Seed != nil {
			params["seed"] = *req.Seed + int64(i+1)
		}
	}
	return res, nil
}

// Text runs a chat completion on the endpoint (or the router's OpenAI-compatible route).
func (a HuggingFace) Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error) {
	target := a.router() + "/v1/chat/completions"
	if cfg.BaseURL != "" {
		target = joinURL(cfg.BaseURL, "v1/chat/completions")
	}
	return a.Env.chatText(ctx, target, bearer(cfg.APIKey), buildChat(req, false))
}

// ModelInfo reads a model card: pipeline (kind) and licence.
func (a HuggingFace) ModelInfo(ctx context.Context, cfg Config, id string) (*RemoteModel, error) {
	var out struct {
		ID          string   `json:"id"`
		PipelineTag string   `json:"pipeline_tag"`
		Tags        []string `json:"tags"`
		CardData    struct {
			License string `json:"license"`
		} `json:"cardData"`
	}
	if err := a.Env.call(ctx, http.MethodGet, a.hub()+"/api/models/"+escapeRepo(id), bearer(cfg.APIKey), nil, &out); err != nil {
		return nil, err
	}
	rm := &RemoteModel{ID: out.ID, Name: out.ID, License: out.CardData.License}
	switch out.PipelineTag {
	case "text-to-image":
		rm.Kind, rm.OutputImages = "image", true
	case "image-to-image":
		rm.Kind, rm.OutputImages, rm.InputImages = "image", true, true
	case "text-to-video", "image-to-video":
		rm.Kind = "video"
	case "text-to-speech", "text-to-audio":
		rm.Kind = "voice"
	case "image-to-text":
		rm.Kind = "ocr"
	default:
		rm.Kind = "text"
	}
	for _, t := range out.Tags {
		if strings.HasPrefix(t, "license:") && rm.License == "" {
			rm.License = strings.TrimPrefix(t, "license:")
		}
	}
	return rm, nil
}

func escapeRepo(id string) string {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
