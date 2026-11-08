package ai

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Together AI direct routes (ADR 0013 §9). Facts frozen from Together's
// documentation, accessed 2026-11-09:
//   - chat completions, max_tokens, response_format json_schema (strict
//     defaults to false), reasoning {enabled}: https://docs.together.ai/reference/chat-completions.md,
//     https://docs.together.ai/docs/inference/chat/structured-outputs.md,
//     https://docs.together.ai/docs/inference/chat/reasoning.md ("ensure your
//     max_tokens accommodates both reasoning and content")
//   - Qwen/Qwen3.5-9B: 262,144 context, structured outputs, USD 0.17 / 0.25
//     per 1M tokens: https://docs.together.ai/docs/serverless/models.md,
//     https://www.together.ai/pricing
//   - images: POST /v1/images/generations; FLUX.1.1 [pro] USD 0.04 per
//     megapixel; n 1–4; width/height multiples of 8; response_format
//     "base64" → b64_json; the safety checker answers 422; no mask input:
//     https://docs.together.ai/reference/post-images-generations.md,
//     https://docs.together.ai/docs/inference/images/parameters.md
//     (FLUX.1 [schnell] was removed on 2026-08-19: https://docs.together.ai/docs/deprecations.md)
//   - errors and 402 for an exhausted account: https://docs.together.ai/docs/error-codes.md
//   - GET /v1/models is "Supported" for OpenAI compatibility, its shape is
//     not documented: https://docs.together.ai/docs/openai-api-compatibility.md
//
// Reasoning is switched off, and max_tokens bounds any reasoning that still
// runs, so the reservation is an upper bound. Together returns no cost: the
// actual is derived from reported tokens (ADR 0006 rule). Image megapixel
// rounding is not documented, so a call reserves whole megapixels (rounded
// up, as fal in ADR 0007) and is charged that reservation.
// Status: fake-transport tested, not live-verified.
const (
	togetherTextModel    = "Qwen/Qwen3.5-9B"
	togetherChatURL      = "https://api.together.ai/v1/chat/completions"
	togetherModelsURL    = "https://api.together.ai/v1/models"
	togetherImageURL     = "https://api.together.ai/v1/images/generations"
	togetherImageModel   = "black-forest-labs/FLUX.1.1-pro"
	togetherInputTokens  = 131_072
	togetherOutputTokens = 16_384
	togetherInputNano    = 170 // USD 0.17 / 1M input tokens
	togetherOutputNano   = 250 // USD 0.25 / 1M output tokens
	togetherReserveUSD   = 0.026379
	togetherPlanUSD      = 2 * togetherReserveUSD
	// togetherImageMPUSD is FLUX.1.1 [pro]'s published tariff per megapixel.
	togetherImageMPUSD = 0.04
	togetherImageEdge  = 1024
)

func newTogetherTextClient(doer httpDoer) *chatCompatClient {
	return &chatCompatClient{doer: doer, url: togetherChatURL, model: togetherTextModel, tokensField: "max_tokens",
		maxTokens: togetherOutputTokens, extra: map[string]any{"reasoning": map[string]bool{"enabled": false}},
		inputNano: togetherInputNano, outputNano: togetherOutputNano, reserveUSD: togetherReserveUSD}
}

type togetherKeyTester struct{ doer httpDoer }

func (t togetherKeyTester) TestKey(ctx context.Context, key string) (string, error) {
	test, err := t.TestCapabilities(ctx, key)
	return test.RequestID, err
}

// TestCapabilities lists the key's models (no generation, no user content)
// and reports whether the frozen text and image models are listed. A list
// that cannot be read still proves the key; capabilities then stay unknown.
func (t togetherKeyTester) TestCapabilities(ctx context.Context, key string) (providerTestState, error) {
	var out providerTestState
	if strings.TrimSpace(key) == "" {
		return out, providerAdapterError(CodeProviderAuth, false, false)
	}
	data, header, status, err := getJSON(ctx, t.doer, togetherModelsURL, "Authorization", "Bearer "+key, openAIModelResponseLimit)
	if err != nil {
		return out, err
	}
	out.RequestID = sanitizeRequestID(header.Get("X-Request-Id"))
	if status < 200 || status >= 300 {
		return out, classifyChatStatus(status, data)
	}
	listed, ok := listedModelIDs(data)
	if !ok {
		return out, nil
	}
	return capabilitiesFrom(out, listed, togetherTextModel, providerKindTogether)
}

// capabilitiesFrom fills Text and Images from a model list, as ADR 0006 does
// for OpenAI, and fails as no_provider when the key lists neither.
func capabilitiesFrom(out providerTestState, listed map[string]bool, textModel, kind string) (providerTestState, error) {
	out.Text, out.Images = capabilityUnavailable, capabilityUnavailable
	if listed[textModel] {
		out.Text = capabilityReady
	}
	for _, r := range imageRoutes {
		if r.Kind == kind && listed[r.Model] {
			out.Images = capabilityReady
			out.ImageModels = append(out.ImageModels, r.Model)
		}
	}
	if out.Text != capabilityReady && out.Images != capabilityReady {
		return out, &adapterError{code: CodeNoProvider, safe: "The key cannot use the configured models.",
			cause: errNoListedModels}
	}
	return out, nil
}

// togetherImageSize is the request size: each side rounded to a multiple of
// 8 within 256–1024. The result is fitted back into the layer.
func togetherImageSize(w, h int) (int, int) {
	side := func(v int) int {
		v = int(math.Round(float64(v)/8)) * 8
		return min(max(v, 256), togetherImageEdge)
	}
	return side(w), side(h)
}

// quoteTogetherImage reserves whole megapixels (rounded up) per output at
// the published FLUX.1.1 [pro] tariff.
func quoteTogetherImage(n ImageNeeds) (jobs.Cost, bool) {
	if n.Count < 1 || n.Count > 4 || n.Edit || n.Mask || n.References > 0 || n.Width > togetherImageEdge || n.Height > togetherImageEdge {
		return jobs.Cost{}, false
	}
	w, h := togetherImageSize(n.Width, n.Height)
	mp := int64(math.Ceil(float64(w*h) / 1_000_000))
	return nanoToCost(int64(n.Count) * mp * 40_000_000), true
}

func togetherImagePayload(req ImageGenerateRequest) map[string]any {
	w, h := togetherImageSize(req.Width, req.Height)
	payload := map[string]any{"model": togetherImageModel, "prompt": req.Prompt, "width": w, "height": h, "n": req.Count,
		"seed": req.Seed, "response_format": "base64", "output_format": "png"}
	if req.Negative != "" {
		payload["negative_prompt"] = req.Negative
	}
	if req.Style != "" {
		payload["prompt"] = req.Prompt + "\nStyle: " + req.Style
	}
	return payload
}

func (s *imageService) togetherImage(ctx context.Context, key string, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	if req.Plan.ModelID != togetherImageModel || req.Init != nil || req.Mask != nil {
		return out, badInputAdapterError()
	}
	body, status, sent, err := s.postImage(ctx, togetherImageURL, "Bearer "+key, togetherImagePayload(req), nil)
	out.Sent = sent
	if err != nil {
		if status >= 400 {
			// Definite rejections and server errors generate nothing (ADR 0007 fal rule).
			zero := 0.0
			out.ActualUSD = &zero
		}
		switch status {
		case http.StatusPaymentRequired:
			err = quotaAdapterError()
		case http.StatusUnprocessableEntity:
			err = providerAdapterError(CodeProviderRefuse, false, false)
		}
		return out, err
	}
	quote, _ := quoteTogetherImage(req.Plan.Needs)
	out.ActualUSD = &quote.USD
	var decoded struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &decoded) != nil || len(decoded.Data) != req.Count {
		return out, badOutputAdapterError(false)
	}
	for _, item := range decoded.Data {
		data, err := decodeBase64Image(item.B64)
		if err != nil {
			return out, badOutputAdapterError(false)
		}
		g, err := fitGeneratedImage(data, req.Width, req.Height, req.Seed, true)
		if err != nil {
			return out, err
		}
		out.Images = append(out.Images, g)
	}
	return out, nil
}
