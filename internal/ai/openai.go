package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// OpenAI direct text route (ADR 0006). The endpoint, model and price ceilings
// are frozen like the OpenRouter route in ADR 0005; there is no user-supplied
// base URL.
const (
	providerKindOpenAI = "openai"
	openAIModel        = "gpt-6-luna"
	openAIChatURL      = "https://api.openai.com/v1/chat/completions"
	// The connection check lists the key's models: authenticated, no
	// generation, no user content. One response shows both the frozen text
	// model (ADR 0006) and the GPT Image route models (ADR 0009).
	openAIModelsURL = "https://api.openai.com/v1/models"
	// Prompts above 272,000 input tokens are billed at the long-context rate,
	// so MangaVeyra never sends one: the reservation below stays an upper bound.
	openAIInputTokens        = 272_000
	openAICompletionTokens   = 16_384
	openAIEnvelopeTokens     = 4_096
	openAIPromptPrice        = 0.10 // USD per 1M input tokens
	openAICompletionPrice    = 0.50 // USD per 1M output tokens (reasoning included)
	openAIReserveUSD         = 0.035392
	openAIPlanUSD            = 2 * openAIReserveUSD
	openAIResponseLimit      = 8 << 20
	openAIModelResponseLimit = 4 << 20
	openAITimeout            = 2 * time.Minute
)

type openAIClient struct {
	doer httpDoer
}

func newOpenAIClient(doer httpDoer) *openAIClient { return &openAIClient{doer: doer} }

type openAIRequest struct {
	Model               string                   `json:"model"`
	Messages            []openRouterMessage      `json:"messages"`
	MaxCompletionTokens int                      `json:"max_completion_tokens"`
	ReasoningEffort     string                   `json:"reasoning_effort"`
	Store               bool                     `json:"store"`
	ResponseFormat      openRouterResponseFormat `json:"response_format"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"error"`
}

func buildOpenAIRequest(in ScriptInput, attempt scriptAttempt) (openAIRequest, error) {
	input, err := json.Marshal(struct {
		Task  string      `json:"task"`
		Input ScriptInput `json:"input"`
	}{Task: TaskScript, Input: in})
	if err != nil {
		return openAIRequest{}, fmt.Errorf("ai: encode script prompt: %w", err)
	}
	system := scriptInstruction(attempt)
	return openAIRequest{
		Model: openAIModel,
		Messages: []openRouterMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: string(input)},
		},
		MaxCompletionTokens: openAICompletionTokens,
		// Reasoning tokens are billed as output and share the completion
		// budget; "none" keeps the whole budget for the JSON document.
		ReasoningEffort: "none",
		// Never keep prompts (premise, private adaptation source) at OpenAI.
		Store: false,
		ResponseFormat: openRouterResponseFormat{Type: "json_schema", JSONSchema: openRouterJSONSchema{
			Name: "mangaveyra_script_v1", Strict: true, Schema: strictScriptSchema(in),
		}},
	}, nil
}

// openAIInputFits keeps every request (initial and repair) below the
// long-context threshold, counting UTF-8 bytes as an upper bound on tokens.
func openAIInputFits(in ScriptInput) bool {
	for _, attempt := range []scriptAttempt{{}, repairFitAttempt} {
		req, err := buildOpenAIRequest(in, attempt)
		if err != nil || len(req.Messages) != 2 {
			return false
		}
		visible := len(req.Messages[0].Content) + len(req.Messages[1].Content) + len(req.ResponseFormat.JSONSchema.Schema)
		if visible+openAIEnvelopeTokens > openAIInputTokens {
			return false
		}
	}
	return true
}

// openAIUsageCost prices reported token usage at the frozen list prices,
// rounded up to whole microdollars and clamped to the per-call reservation
// (a request that fits cannot legitimately cost more). Missing usage returns
// nil, which reconciliation charges at the reserved estimate.
func openAIUsageCost(prompt, completion *int64) *float64 {
	if prompt == nil || completion == nil || *prompt < 0 || *completion < 0 {
		return nil
	}
	micro := math.Ceil(float64(*prompt)*openAIPromptPrice + float64(*completion)*openAICompletionPrice)
	usd := math.Min(micro/costMicro, openAIReserveUSD)
	return &usd
}

const costMicro = 1_000_000

func (c *openAIClient) Complete(ctx context.Context, key string, in ScriptInput, attempt scriptAttempt) (textCall, error) {
	var out textCall
	if c == nil || c.doer == nil || strings.TrimSpace(key) == "" {
		return out, providerAdapterError(CodeProviderAuth, false, false)
	}
	payload, err := buildOpenAIRequest(in, attempt)
	if err != nil {
		return out, badInputAdapterError()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return out, badInputAdapterError()
	}
	callCtx, cancel := context.WithTimeout(ctx, openAITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, openAIChatURL, bytes.NewReader(body))
	if err != nil {
		return out, badInputAdapterError()
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	out.Sent = true
	resp, err := c.doer.Do(req)
	if err != nil {
		return out, classifyTransportError(err)
	}
	if resp == nil || resp.Body == nil {
		return out, upstreamAdapterError(true)
	}
	defer resp.Body.Close()
	raw, tooLarge, err := readBounded(resp.Body, openAIResponseLimit)
	if err != nil {
		return out, upstreamAdapterError(true)
	}
	if tooLarge {
		return out, badOutputAdapterError(true)
	}
	var decoded openAIResponse
	decodeErr := json.Unmarshal(raw, &decoded)
	if decodeErr == nil && decoded.Usage != nil {
		out.ActualUSD = openAIUsageCost(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A rejected request is not billed: OpenAI charges for processed tokens.
		zero := 0.0
		out.ActualUSD = &zero
		return out, classifyOpenAIStatus(resp.StatusCode, decoded)
	}
	if decodeErr != nil || len(decoded.Choices) == 0 {
		return out, badOutputAdapterError(true)
	}
	choice := decoded.Choices[0]
	if strings.TrimSpace(choice.Message.Refusal) != "" || choice.FinishReason == "content_filter" {
		return out, providerAdapterError(CodeProviderRefuse, false, false)
	}
	if choice.FinishReason == "length" || strings.TrimSpace(choice.Message.Content) == "" {
		return out, badOutputAdapterError(true)
	}
	out.Content = json.RawMessage(choice.Message.Content)
	return out, nil
}

// TestKey checks the key with the model list (TestCapabilities) and fails
// only when the key can use neither the text model nor a GPT Image model.
func (c *openAIClient) TestKey(ctx context.Context, key string) (string, error) {
	test, err := c.TestCapabilities(ctx, key)
	return test.RequestID, err
}

// TestCapabilities sends one GET /v1/models and reports, from that single
// non-generating response, whether the key lists the frozen text model and
// each GPT Image route model. It never sends a generation request. A key that
// lists neither fails as no_provider and still reports both as unavailable.
func (c *openAIClient) TestCapabilities(ctx context.Context, key string) (providerTestState, error) {
	var out providerTestState
	if c == nil || c.doer == nil || strings.TrimSpace(key) == "" {
		return out, providerAdapterError(CodeProviderAuth, false, false)
	}
	callCtx, cancel := context.WithTimeout(ctx, openAITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, openAIModelsURL, nil)
	if err != nil {
		return out, upstreamAdapterError(false)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.doer.Do(req)
	if err != nil {
		return out, classifyTransportError(err)
	}
	if resp == nil || resp.Body == nil {
		return out, upstreamAdapterError(false)
	}
	defer resp.Body.Close()
	raw, tooLarge, readErr := readBounded(resp.Body, openAIModelResponseLimit)
	if readErr != nil || tooLarge {
		return out, upstreamAdapterError(false)
	}
	out.RequestID = sanitizeRequestID(resp.Header.Get("X-Request-Id"))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var decoded openAIResponse
		_ = json.Unmarshal(raw, &decoded)
		return out, classifyOpenAIStatus(resp.StatusCode, decoded)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Data == nil {
		return out, upstreamAdapterError(false)
	}
	listed := make(map[string]bool, len(list.Data))
	for _, m := range list.Data {
		listed[m.ID] = true
	}
	out.Text, out.Images = capabilityUnavailable, capabilityUnavailable
	if listed[openAIModel] {
		out.Text = capabilityReady
	}
	for _, r := range imageRoutes {
		if r.Kind == providerKindOpenAI && listed[r.Model] {
			out.Images = capabilityReady
			out.ImageModels = append(out.ImageModels, r.Model)
		}
	}
	if out.Text != capabilityReady && out.Images != capabilityReady {
		return out, &adapterError{code: CodeNoProvider, safe: "The OpenAI key cannot use the configured models.",
			cause: errors.New("ai: provider models unavailable")}
	}
	return out, nil
}

// classifyOpenAIStatus maps OpenAI errors to safe codes. A 429 with
// insufficient_quota is an account problem, not a transient rate limit, so it
// is not retried; a 404 means the key cannot use the frozen model.
func classifyOpenAIStatus(status int, decoded openAIResponse) error {
	if decoded.Error != nil {
		switch decoded.Error.Code {
		case "insufficient_quota", "billing_hard_limit_reached":
			return &adapterError{code: CodeProviderAuth, safe: "The text provider account has no usable quota.",
				cause: errors.New("ai: provider quota exhausted")}
		case "model_not_found":
			return &adapterError{code: CodeNoProvider, safe: "The text provider key cannot use the configured model.",
				cause: errors.New("ai: provider model unavailable")}
		}
	}
	if status == http.StatusNotFound {
		return &adapterError{code: CodeNoProvider, safe: "The text provider key cannot use the configured model.",
			cause: errors.New("ai: provider model unavailable")}
	}
	return classifyHTTPStatus(status)
}
