package providers

import (
	"context"
	"net/http"
)

// OpenAICompatible is the generic OpenAI-format endpoint: LiteLLM, vLLM, LM
// Studio, llama.cpp, a company proxy or any server that speaks the OpenAI
// API. BaseURL is the API root including its version ("http://host:8000/v1").
type OpenAICompatible struct{ Env Env }

// Kind implements Adapter.
func (OpenAICompatible) Kind() string { return KindOpenAICompatible }

// Image calls /images/generations with an explicit size and base64 output.
func (a OpenAICompatible) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	if cfg.BaseURL == "" {
		return nil, Errf(CodeBadRequest, "base URL is required")
	}
	w, h := req.Size()
	in := map[string]any{"model": req.Model, "prompt": stylePrompt(req.Prompt, req.Style), "n": req.Count(),
		"size": sizeString(w, h), "response_format": "b64_json"}
	if req.Seed != nil {
		in["seed"] = *req.Seed
	}
	if req.Negative != "" {
		in["negative_prompt"] = req.Negative
	}
	progress.report(0.1, "sending")
	var out imagesResponse
	if err := a.Env.call(ctx, http.MethodPost, joinURL(cfg.BaseURL, "images/generations"), bearer(cfg.APIKey), in, &out); err != nil {
		return nil, err
	}
	return a.Env.collectImages(ctx, &out)
}

// Text runs a chat completion.
func (a OpenAICompatible) Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error) {
	if cfg.BaseURL == "" {
		return nil, Errf(CodeBadRequest, "base URL is required")
	}
	return a.Env.chatText(ctx, joinURL(cfg.BaseURL, "chat/completions"), bearer(cfg.APIKey), buildChat(req, false))
}

// Models lists /models.
func (a OpenAICompatible) Models(ctx context.Context, cfg Config) ([]RemoteModel, error) {
	if cfg.BaseURL == "" {
		return nil, Errf(CodeBadRequest, "base URL is required")
	}
	return a.Env.listModels(ctx, joinURL(cfg.BaseURL, "models"), bearer(cfg.APIKey))
}
