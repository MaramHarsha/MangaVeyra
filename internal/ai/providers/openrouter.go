package providers

import (
	"context"
	"net/http"
	"slices"
	"strconv"
)

// OpenRouter speaks the OpenRouter API: one key for many text and image
// models. Text uses /api/v1/chat/completions; images use the same endpoint
// with modalities ["image","text"] (images come back as data URLs in
// message.images); /api/v1/models publishes input/output modalities and
// pricing, which the add-provider flow uses to fill in capabilities.
type OpenRouter struct{ Env Env }

// Kind implements Adapter.
func (OpenRouter) Kind() string { return KindOpenRouter }

func (a OpenRouter) base(cfg Config) string { return baseOr(cfg, "https://openrouter.ai/api/v1") }

func (a OpenRouter) headers(cfg Config) map[string]string {
	h := bearer(cfg.APIKey)
	h["X-Title"] = "MangaVeyra"
	h["HTTP-Referer"] = "https://github.com/MaramHarsha/MangaVeyra"
	return h
}

// Image generates images through chat completions with image output.
func (a OpenRouter) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	parts := []map[string]any{{"type": "text", "text": stylePrompt(req.Prompt, req.Style)}}
	if req.Negative != "" {
		parts[0]["text"] = parts[0]["text"].(string) + "\nAvoid: " + req.Negative
	}
	if req.Image != nil {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": DataURI(*req.Image)}})
	}
	for _, r := range req.Refs {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": DataURI(r)}})
	}
	cr := chatRequest{Model: req.Model, Messages: []chatMessage{{Role: "user", Content: parts}}, Modalities: []string{"image", "text"},
		Usage: map[string]any{"include": true}, ImageCfg: map[string]any{"aspect_ratio": aspect(w, h)}}
	res := &ImageResult{}
	for i := 0; i < req.Count(); i++ {
		progress.report(float64(i)/float64(req.Count()), "image "+strconv.Itoa(i+1))
		out, err := a.Env.chatCall(ctx, joinURL(a.base(cfg), "chat/completions"), a.headers(cfg), cr)
		if err != nil {
			return nil, err
		}
		for _, im := range out.Choices[0].Message.Images {
			img, err := a.Env.fetch(ctx, im.ImageURL.URL, nil)
			if err != nil {
				return nil, err
			}
			res.Images = append(res.Images, img)
		}
		res.CostUSD += out.Usage.Cost
		if len(res.Images) == 0 {
			if t := contentText(out.Choices[0].Message.Content); t != "" && looksLikeSafety(t) {
				return nil, Errf(CodeSafety, "%s", t)
			}
			return nil, Errf(CodeInternal, "the model returned no image")
		}
	}
	return res, nil
}

// Text runs a chat completion (cost reported through usage accounting).
func (a OpenRouter) Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error) {
	cr := buildChat(req, false)
	cr.Usage = map[string]any{"include": true}
	return a.Env.chatText(ctx, joinURL(a.base(cfg), "chat/completions"), a.headers(cfg), cr)
}

// Models lists every model with its modalities and published pricing.
func (a OpenRouter) Models(ctx context.Context, cfg Config) ([]RemoteModel, error) {
	var out struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Description   string `json:"description"`
			ContextLength int    `json:"context_length"`
			Architecture  struct {
				InputModalities  []string `json:"input_modalities"`
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
				Image      string `json:"image"`
			} `json:"pricing"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := a.Env.call(ctx, http.MethodGet, joinURL(a.base(cfg), "models"), a.headers(cfg), nil, &out); err != nil {
		return nil, err
	}
	res := make([]RemoteModel, 0, len(out.Data))
	for _, m := range out.Data {
		rm := RemoteModel{ID: m.ID, Name: m.Name, Context: m.ContextLength, Description: m.Description,
			InputImages:   slices.Contains(m.Architecture.InputModalities, "image"),
			OutputImages:  slices.Contains(m.Architecture.OutputModalities, "image"),
			SupportsTools: slices.Contains(m.SupportedParameters, "tools")}
		prompt, _ := strconv.ParseFloat(m.Pricing.Prompt, 64)
		completion, _ := strconv.ParseFloat(m.Pricing.Completion, 64)
		if rm.OutputImages {
			rm.Kind = "image"
			if img, _ := strconv.ParseFloat(m.Pricing.Image, 64); img > 0 {
				rm.PriceUnit, rm.PriceUSD = "per_image", img
			}
		} else {
			rm.Kind = "text"
			// OpenRouter publishes USD per token.
			rm.PriceUnit, rm.PriceUSD, rm.PriceUSDOut = "per_1k_tokens", prompt*1000, completion*1000
		}
		res = append(res, rm)
	}
	return res, nil
}
