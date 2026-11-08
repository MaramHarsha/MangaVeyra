package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// xAI direct routes (ADR 0013 §9). Facts frozen from xAI's documentation,
// accessed 2026-11-09:
//   - chat completions and usage.cost_in_usd_ticks (10^10 ticks = USD 1):
//     https://docs.x.ai/developers/rest-api-reference/inference/chat-completions
//   - structured outputs (json_schema, strict, ≤256 items):
//     https://docs.x.ai/docs/guides/structured-outputs
//   - grok-4.20-0309-non-reasoning: 1M context, structured outputs, no
//     reasoning; USD 1.25 / 2.50 per 1M tokens below 200k prompt tokens:
//     https://docs.x.ai/developers/models/grok-4.20-0309-non-reasoning
//   - images: POST /v1/images/generations, grok-imagine-image USD 0.02 per
//     image, n 1–10, aspect_ratio, resolution 1k, b64_json; edits take source
//     images but no mask: https://docs.x.ai/developers/rest-api-reference/inference/images,
//     https://docs.x.ai/developers/pricing
//   - key check: GET /v1/api-key (api_key_blocked, api_key_disabled,
//     team_blocked): https://docs.x.ai/developers/rest-api-reference/inference/other
//
// The text model is non-reasoning on purpose: xAI documents that
// max_completion_tokens caps only visible output, so a reasoning model's
// billed reasoning tokens would have no ceiling. Prompts are capped at
// 131,072 tokens, well below the 200k long-context tariff. xAI keeps API
// requests for 30 days unless the team enables zero data retention; Chat
// Completions has no per-request store switch.
// Status: fake-transport tested, not live-verified.
const (
	xaiTextModel    = "grok-4.20-0309-non-reasoning"
	xaiChatURL      = "https://api.x.ai/v1/chat/completions"
	xaiKeyURL       = "https://api.x.ai/v1/api-key"
	xaiImageURL     = "https://api.x.ai/v1/images/generations"
	xaiImageModel   = "grok-imagine-image"
	xaiInputTokens  = 131_072
	xaiOutputTokens = 16_384
	xaiInputNano    = 1_250 // USD 1.25 / 1M input tokens (<200k prompt)
	xaiOutputNano   = 2_500 // USD 2.50 / 1M output tokens
	xaiReserveUSD   = 0.2048
	xaiPlanUSD      = 2 * xaiReserveUSD
	// xaiImageUSD is the published per-image tariff of grok-imagine-image.
	xaiImageUSD = 0.02
	xaiMaxCount = 4
)

// xaiAspects are the aspect ratios the image API documents (auto excluded).
var xaiAspects = []string{"1:1", "3:4", "4:3", "9:16", "16:9", "2:3", "3:2", "9:19.5", "19.5:9", "9:20", "20:9", "1:2", "2:1", "21:9", "5:2"}

func newXAITextClient(doer httpDoer) *chatCompatClient {
	return &chatCompatClient{doer: doer, url: xaiChatURL, model: xaiTextModel, tokensField: "max_completion_tokens",
		maxTokens: xaiOutputTokens, inputNano: xaiInputNano, outputNano: xaiOutputNano, reserveUSD: xaiReserveUSD}
}

type xaiKeyTester struct{ doer httpDoer }

// TestKey reads the key's own status; it generates nothing.
func (t xaiKeyTester) TestKey(ctx context.Context, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", providerAdapterError(CodeProviderAuth, false, false)
	}
	data, header, status, err := getJSON(ctx, t.doer, xaiKeyURL, "Authorization", "Bearer "+key, 64<<10)
	if err != nil {
		return "", err
	}
	requestID := sanitizeRequestID(header.Get("X-Request-Id"))
	if status < 200 || status >= 300 {
		return requestID, classifyChatStatus(status, data)
	}
	var state struct {
		KeyBlocked  bool `json:"api_key_blocked"`
		KeyDisabled bool `json:"api_key_disabled"`
		TeamBlocked bool `json:"team_blocked"`
	}
	if json.Unmarshal(data, &state) != nil {
		return requestID, upstreamAdapterError(false)
	}
	if state.KeyBlocked || state.KeyDisabled || state.TeamBlocked {
		return requestID, providerAdapterError(CodeProviderAuth, false, false)
	}
	return requestID, nil
}

// quoteXAIImage is the published per-image tariff times the output count.
func quoteXAIImage(n ImageNeeds) (jobs.Cost, bool) {
	if n.Count < 1 || n.Count > xaiMaxCount || n.Edit || n.Mask || n.References > 0 {
		return jobs.Cost{}, false
	}
	return nanoToCost(int64(n.Count) * 20_000_000), true
}

// plainImagePrompt is the composed prompt of a route without image inputs:
// the description, the avoid list and the style line.
func plainImagePrompt(req ImageGenerateRequest) string {
	prompt := req.Prompt
	if req.Negative != "" {
		prompt += "\nAvoid: " + req.Negative
	}
	if req.Style != "" {
		prompt += "\nStyle: " + req.Style
	}
	return prompt
}

func validatePlainPrompt(req ImageGenerateRequest, limit int) error {
	if len(plainImagePrompt(req)) > limit {
		return imageError(CodeBadInput, false, jobs.Cost{})
	}
	return nil
}

func xaiImagePayload(req ImageGenerateRequest) map[string]any {
	return map[string]any{"model": xaiImageModel, "prompt": plainImagePrompt(req), "n": req.Count,
		"aspect_ratio": nearestAspectFrom(req.Width, req.Height, xaiAspects), "resolution": "1k", "response_format": "b64_json"}
}

func (s *imageService) xaiImage(ctx context.Context, key string, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	if req.Plan.ModelID != xaiImageModel || req.Init != nil || req.Mask != nil {
		return out, badInputAdapterError()
	}
	body, status, sent, err := s.postImage(ctx, xaiImageURL, "Bearer "+key, xaiImagePayload(req), nil)
	out.Sent = sent
	if err != nil {
		out.ActualUSD = xaiImageFailureCost(status)
		if status == http.StatusPaymentRequired {
			err = quotaAdapterError()
		}
		return out, err
	}
	var decoded struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
		Usage *struct {
			CostTicks *int64 `json:"cost_in_usd_ticks"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return out, badOutputAdapterError(false)
	}
	// Reported cost wins and is not clamped (ADR 0009 image policy);
	// without it the generation is charged at the published tariff.
	quote, _ := quoteXAIImage(req.Plan.Needs)
	out.ActualUSD = &quote.USD
	if decoded.Usage != nil && decoded.Usage.CostTicks != nil && *decoded.Usage.CostTicks >= 0 {
		usd := float64((*decoded.Usage.CostTicks+9_999)/10_000) / costMicro
		out.ActualUSD = &usd
	}
	if len(decoded.Data) != req.Count {
		return out, badOutputAdapterError(false)
	}
	for _, item := range decoded.Data {
		data, err := decodeBase64Image(item.B64)
		if err != nil {
			return out, badOutputAdapterError(false)
		}
		g, err := fitGeneratedImage(data, req.Width, req.Height, 0, true)
		if err != nil {
			return out, err
		}
		out.Images = append(out.Images, g)
	}
	return out, nil
}

// xaiImageFailureCost: a definite rejection (auth, access, rate limit,
// invalid fields) generated nothing and reconciles at zero. xAI charges for
// a generation it moderated, and a 400 or server error does not say whether
// generation ran, so those keep the reservation (nil = estimate).
func xaiImageFailureCost(status int) *float64 {
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound,
		http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		zero := 0.0
		return &zero
	}
	return nil
}
