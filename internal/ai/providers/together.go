package providers

import (
	"context"
	"net/http"
)

// Together speaks Together AI's OpenAI-compatible API: /v1/images/generations
// (with width/height/steps/seed extensions) and /v1/chat/completions.
type Together struct{ Env Env }

// Kind implements Adapter.
func (Together) Kind() string { return KindTogether }

func (a Together) base(cfg Config) string { return baseOr(cfg, "https://api.together.xyz/v1") }

// Image generates images; a source image is passed as image_url (Kontext-style edit models).
func (a Together) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	in := map[string]any{"model": req.Model, "prompt": stylePrompt(req.Prompt, req.Style), "width": w / 16 * 16, "height": h / 16 * 16,
		"n": req.Count(), "response_format": "base64"}
	if req.Negative != "" {
		in["negative_prompt"] = req.Negative
	}
	if req.Steps > 0 {
		in["steps"] = req.Steps
	}
	if req.Seed != nil {
		in["seed"] = *req.Seed
	}
	if req.Image != nil {
		in["image_url"] = DataURI(*req.Image)
	} else if len(req.Refs) > 0 {
		in["image_url"] = DataURI(req.Refs[0])
	}
	progress.report(0.1, "sending to Together")
	var out imagesResponse
	if err := a.Env.call(ctx, http.MethodPost, joinURL(a.base(cfg), "images/generations"), bearer(cfg.APIKey), in, &out); err != nil {
		return nil, err
	}
	return a.Env.collectImages(ctx, &out)
}

// Text runs a chat completion.
func (a Together) Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error) {
	return a.Env.chatText(ctx, joinURL(a.base(cfg), "chat/completions"), bearer(cfg.APIKey), buildChat(req, false))
}

// Models lists models with their type (image, chat…).
func (a Together) Models(ctx context.Context, cfg Config) ([]RemoteModel, error) {
	var out []struct {
		ID            string `json:"id"`
		DisplayName   string `json:"display_name"`
		Type          string `json:"type"`
		ContextLength int    `json:"context_length"`
		Pricing       struct {
			Input  float64 `json:"input"`
			Output float64 `json:"output"`
		} `json:"pricing"`
	}
	if err := a.Env.call(ctx, http.MethodGet, joinURL(a.base(cfg), "models"), bearer(cfg.APIKey), nil, &out); err != nil {
		return nil, err
	}
	res := make([]RemoteModel, 0, len(out))
	for _, m := range out {
		rm := RemoteModel{ID: m.ID, Name: m.DisplayName, Context: m.ContextLength}
		switch m.Type {
		case "image":
			rm.Kind, rm.OutputImages = "image", true
		case "chat", "language", "code":
			rm.Kind = "text"
			// Together publishes USD per million tokens.
			rm.PriceUnit, rm.PriceUSD, rm.PriceUSDOut = "per_1k_tokens", m.Pricing.Input/1000, m.Pricing.Output/1000
		default:
			continue
		}
		res = append(res, rm)
	}
	return res, nil
}
