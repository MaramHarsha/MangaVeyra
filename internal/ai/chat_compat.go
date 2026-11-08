package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
)

// chatCompatClient sends one structured script request to an OpenAI-
// compatible chat completions endpoint (xAI, Together, Hugging Face). Each
// route freezes its URL, model, completion ceiling and tariff; the request
// carries the same strict script schema as ADR 0006.
type chatCompatClient struct {
	doer  httpDoer
	url   string
	model string
	// tokensField is the completion ceiling's name ("max_tokens" or
	// "max_completion_tokens") and maxTokens its value.
	tokensField string
	maxTokens   int
	// extra fields are merged into the body (for example Together's
	// reasoning switch). They never carry user content.
	extra map[string]any
	// Frozen tariff in nano-USD per token, and the per-call reservation the
	// derived cost is clamped to (a request that fits cannot legitimately
	// cost more at the frozen tariff, as in ADR 0006).
	inputNano, outputNano int64
	reserveUSD            float64
	// headers are added to every request (never the key).
	headers map[string]string
	timeout time.Duration
}

type chatCompatResponse struct {
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
		// CostTicks is xAI's reported cost: 10,000,000,000 ticks = USD 1.
		CostTicks *int64 `json:"cost_in_usd_ticks"`
	} `json:"usage"`
}

// chatCompatBody is the exact request body: the shared script messages and
// strict schema, the route's model and its completion ceiling.
func (c *chatCompatClient) body(in ScriptInput, attempt scriptAttempt) (map[string]any, error) {
	base, err := buildOpenRouterRequest(in, attempt)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":           c.model,
		"messages":        base.Messages,
		c.tokensField:     c.maxTokens,
		"response_format": base.ResponseFormat,
		"stream":          false,
	}
	for k, v := range c.extra {
		body[k] = v
	}
	return body, nil
}

// fitsInputCap keeps every request (initial and repair) within a route's
// input ceiling, counting UTF-8 bytes as an upper bound on tokens plus the
// chat envelope allowance (ADR 0005/0006).
func fitsInputCap(inputTokens int) func(ScriptInput) bool {
	return func(in ScriptInput) bool {
		for _, attempt := range []scriptAttempt{{}, repairFitAttempt} {
			req, err := buildOpenRouterRequest(in, attempt)
			if err != nil || len(req.Messages) != 2 {
				return false
			}
			visible := len(req.Messages[0].Content) + len(req.Messages[1].Content) + len(req.ResponseFormat.JSONSchema.Schema)
			if visible+openAIEnvelopeTokens > inputTokens {
				return false
			}
		}
		return true
	}
}

// reserveFor is the exact per-call bound of a token-priced text route:
// the input ceiling at the input tariff plus the completion ceiling at the
// output tariff, rounded up to whole microdollars.
func reserveFor(inputTokens, outputTokens int, inputNano, outputNano int64) float64 {
	return nanoToCost(int64(inputTokens)*inputNano + int64(outputTokens)*outputNano).USD
}

func (c *chatCompatClient) usageCost(resp chatCompatResponse) *float64 {
	if resp.Usage == nil {
		return nil
	}
	clamp := func(usd float64) *float64 {
		v := math.Min(usd, c.reserveUSD)
		return &v
	}
	if t := resp.Usage.CostTicks; t != nil && *t >= 0 {
		// Round reported ticks up to whole microdollars (1 µUSD = 10,000 ticks).
		return clamp(float64((*t+9_999)/10_000) / costMicro)
	}
	p, o := resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	if p == nil || o == nil || *p < 0 || *o < 0 {
		return nil
	}
	return clamp(nanoToCost(*p*c.inputNano + *o*c.outputNano).USD)
}

func (c *chatCompatClient) Complete(ctx context.Context, key string, in ScriptInput, attempt scriptAttempt) (textCall, error) {
	var out textCall
	if c == nil || c.doer == nil || strings.TrimSpace(key) == "" {
		return out, providerAdapterError(CodeProviderAuth, false, false)
	}
	payload, err := c.body(in, attempt)
	if err != nil {
		return out, badInputAdapterError()
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return out, badInputAdapterError()
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = openAITimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		return out, badInputAdapterError()
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	out.Sent = true
	resp, err := c.doer.Do(req)
	if err != nil {
		return out, classifyTransportError(err)
	}
	if resp == nil || resp.Body == nil {
		return out, upstreamAdapterError(true)
	}
	defer resp.Body.Close()
	data, tooLarge, err := readBounded(resp.Body, openAIResponseLimit)
	if err != nil {
		return out, upstreamAdapterError(true)
	}
	if tooLarge {
		return out, badOutputAdapterError(true)
	}
	var decoded chatCompatResponse
	decodeErr := json.Unmarshal(data, &decoded)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A rejected request processed no tokens (ADR 0006).
		zero := 0.0
		out.ActualUSD = &zero
		return out, classifyChatStatus(resp.StatusCode, data)
	}
	if decodeErr == nil {
		out.ActualUSD = c.usageCost(decoded)
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

// classifyChatStatus maps an OpenAI-shaped error response to a safe code.
// Only the status and the error's machine-readable code/type are read; the
// provider's message is never kept or shown. 402 is an exhausted account
// (Together, Hugging Face), not a transient failure.
func classifyChatStatus(status int, body []byte) error {
	var decoded openAIResponse
	_ = json.Unmarshal(body, &decoded)
	if status == http.StatusPaymentRequired {
		return quotaAdapterError()
	}
	switch status {
	case 524, 529:
		return providerAdapterError(CodeTimeout, true, false)
	}
	return classifyOpenAIStatus(status, decoded)
}

var errNoListedModels = errors.New("ai: provider models unavailable")

func quotaAdapterError() error {
	return &adapterError{code: CodeProviderAuth, safe: "The provider account has no usable quota.",
		cause: errors.New("ai: provider quota exhausted")}
}

// getJSON sends one authenticated, non-generating metadata request (a key
// or model-list check) and returns the bounded body. It never sends user
// content. auth is the complete header value, or "" for none.
func getJSON(ctx context.Context, doer httpDoer, url, authHeader, auth string, limit int64) ([]byte, http.Header, int, error) {
	if doer == nil {
		return nil, nil, 0, upstreamAdapterError(false)
	}
	callCtx, cancel := context.WithTimeout(ctx, openAITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, 0, upstreamAdapterError(false)
	}
	if auth != "" {
		req.Header.Set(authHeader, auth)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := doer.Do(req)
	if err != nil {
		return nil, nil, 0, classifyTransportError(err)
	}
	if resp == nil || resp.Body == nil {
		return nil, nil, 0, upstreamAdapterError(false)
	}
	defer resp.Body.Close()
	data, tooLarge, err := readBounded(resp.Body, limit)
	if err != nil || tooLarge {
		return nil, resp.Header, resp.StatusCode, upstreamAdapterError(false)
	}
	return data, resp.Header, resp.StatusCode, nil
}

// listedModelIDs reads an OpenAI-style model list ({"data":[{"id"}]}) or a
// bare array of {"id"} (Together does not document which it returns).
func listedModelIDs(data []byte) (map[string]bool, bool) {
	var wrapped struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	var bare []struct {
		ID string `json:"id"`
	}
	out := map[string]bool{}
	switch {
	case json.Unmarshal(data, &wrapped) == nil && wrapped.Data != nil:
		for _, m := range wrapped.Data {
			out[m.ID] = true
		}
	case json.Unmarshal(data, &bare) == nil && bare != nil:
		for _, m := range bare {
			out[m.ID] = true
		}
	default:
		return nil, false
	}
	return out, true
}
