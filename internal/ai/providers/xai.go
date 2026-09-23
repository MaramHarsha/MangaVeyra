package providers

import (
	"context"
	"net/http"
)

// XAI speaks the xAI API (OpenAI-compatible): /v1/images/generations and /v1/chat/completions.
type XAI struct{ Env Env }

// Kind implements Adapter.
func (XAI) Kind() string { return KindXAI }

func (a XAI) base(cfg Config) string { return baseOr(cfg, "https://api.x.ai/v1") }

// Image generates images (xAI picks the size; edits with a source image use image_url).
func (a XAI) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	in := map[string]any{"model": req.Model, "prompt": stylePrompt(req.Prompt, req.Style), "n": req.Count(),
		"response_format": "b64_json", "aspect_ratio": aspect(w, h)}
	endpoint := "images/generations"
	if req.Image != nil {
		in["image"] = map[string]any{"url": DataURI(*req.Image)}
		endpoint = "images/edits"
	}
	progress.report(0.1, "sending to xAI")
	var out imagesResponse
	if err := a.Env.call(ctx, http.MethodPost, joinURL(a.base(cfg), endpoint), bearer(cfg.APIKey), in, &out); err != nil {
		return nil, err
	}
	return a.Env.collectImages(ctx, &out)
}

// Text runs a chat completion.
func (a XAI) Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error) {
	return a.Env.chatText(ctx, joinURL(a.base(cfg), "chat/completions"), bearer(cfg.APIKey), buildChat(req, false))
}

// Models lists models.
func (a XAI) Models(ctx context.Context, cfg Config) ([]RemoteModel, error) {
	return a.Env.listModels(ctx, joinURL(a.base(cfg), "models"), bearer(cfg.APIKey))
}
