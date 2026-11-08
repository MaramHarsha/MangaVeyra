package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Hugging Face connections (G12 "Hugging Face", ADR 0013 §9). Facts from
// Hugging Face's documentation, accessed 2026-11-09:
//   - Inference Providers chat: POST https://router.huggingface.co/v1/chat/completions,
//     "Drop-in replacement for the OpenAI chat completions API", Bearer hf_
//     token with "Make calls to Inference Providers", "org/model:provider"
//     pins a provider, response_format json_schema:
//     https://huggingface.co/docs/inference-providers/tasks/chat-completion,
//     https://huggingface.co/docs/inference-providers/guides/structured-output
//   - GET https://router.huggingface.co/v1/models/{model} lists each
//     provider's status, context_length, pricing {input, output} in "USD per
//     million tokens" and supports_structured_output:
//     https://huggingface.co/docs/inference-providers/hub-api
//   - "Hugging Face charges you the same rates as the provider, with no
//     additional fees": https://huggingface.co/docs/inference-providers/pricing
//   - Inference Endpoints (dedicated): Bearer token, an hourly rate charged
//     while the endpoint initialises and runs, scale to zero, 502 during a
//     cold start; diffusers text-to-image takes {"inputs": prompt} and
//     returns the image bytes: https://huggingface.co/docs/inference-endpoints/pricing,
//     https://huggingface.co/docs/inference-endpoints/autoscaling,
//     https://huggingface.co/docs/inference-endpoints/supported_tasks,
//     https://huggingface.co/docs/inference-providers/tasks/text-to-image
//   - key check: GET https://huggingface.co/api/whoami-v2:
//     https://huggingface.co/.well-known/openapi.json
//
// Serverless text reserves the context and output ceilings at the provider
// tariff the router listed at the last passed test (frozen into the
// connection, refreshed by every test); without a listed tariff the
// connection does not route. A dedicated endpoint is billed per hour, not per
// call, so its calls reserve zero: the remote-spend cap cannot bound it and
// G2 says so. Serverless text-to-image is billed by compute time with no
// published ceiling and is not offered.
// Status: fake-transport tested, not live-verified.
const (
	hfTaskText      = "text"
	hfTaskImage     = "image"
	hfServerless    = "serverless"
	hfDedicated     = "dedicated"
	hfRouterBase    = "https://router.huggingface.co/v1"
	hfWhoAmIURL     = "https://huggingface.co/api/whoami-v2"
	hfEndpointsHost = ".endpoints.huggingface.cloud"
	// hfMaxNano bounds a listed tariff (USD 1,000 per 1M tokens).
	hfMaxNano = 1_000_000
)

type hfConfig struct {
	Endpoint      string `json:"endpoint"`
	Task          string `json:"task"`
	Model         string `json:"model"`
	ContextTokens int    `json:"context_tokens,omitempty"`
	OutputTokens  int    `json:"output_tokens,omitempty"`
	// Priced is true once a passed test read the provider's tariff from the
	// router listing (serverless text); InputNano/OutputNano are that tariff
	// in nano-USD per token, rounded up.
	Priced     bool            `json:"priced,omitempty"`
	InputNano  int64           `json:"input_nano,omitempty"`
	OutputNano int64           `json:"output_nano,omitempty"`
	Image      customImageSpec `json:"image"`
	Modes      []string        `json:"modes,omitempty"`
}

// hfDefaultModes leaves Free only out: every Hugging Face call is paid.
var hfDefaultModes = []string{ModeBalanced, ModeFastest}

func (c hfConfig) routeModes() []string { return customModes(c.Modes, hfDefaultModes) }

func (c hfConfig) allowsMode(mode string) bool { return modesAllow(c.routeModes(), mode) }

func decodeHFConfig(raw string) (hfConfig, error) {
	var c hfConfig
	return c, decodeStrict(raw, &c)
}

// validHFRepo accepts "owner/name", optionally ":provider" for serverless.
func validHFRepo(model string, provider bool) bool {
	repo, suffix, hasSuffix := strings.Cut(model, ":")
	if hasSuffix != provider {
		return false
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") || !validModelID(repo) || len(model) > 200 {
		return false
	}
	if provider {
		for _, r := range suffix {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
		return suffix != ""
	}
	return true
}

// validHFDedicatedURL accepts only an Inference Endpoints host over HTTPS.
func validHFDedicatedURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		strings.HasSuffix(u.Hostname(), hfEndpointsHost) && len(u.Hostname()) > len(hfEndpointsHost) && u.Port() == ""
}

func validateHFConfig(base string, c hfConfig) error {
	switch c.Endpoint {
	case hfServerless:
		if base != hfRouterBase || c.Task != hfTaskText || !validHFRepo(c.Model, true) {
			return errProviderInvalid
		}
	case hfDedicated:
		if !validHFDedicatedURL(base) || (c.Task != hfTaskText && c.Task != hfTaskImage) || !validHFRepo(c.Model, false) {
			return errProviderInvalid
		}
	default:
		return errProviderInvalid
	}
	if c.Task == hfTaskText {
		if validateLocalTextConfig("http://x/v1", localTextConfig{Model: "m", ContextTokens: c.ContextTokens, OutputTokens: c.OutputTokens}) != nil {
			return errProviderInvalid
		}
	} else if !c.Image.valid() || c.Image.MaxCount != 1 || c.ContextTokens != 0 || c.OutputTokens != 0 {
		return errProviderInvalid
	}
	if c.InputNano < 0 || c.OutputNano < 0 || c.InputNano > hfMaxNano || c.OutputNano > hfMaxNano || (c.Endpoint == hfDedicated && (c.Priced || c.InputNano != 0 || c.OutputNano != 0)) {
		return errProviderInvalid
	}
	if !validLocalModes(c.Modes) || modesAllow(c.Modes, ModeFreeOnly) {
		return errProviderInvalid
	}
	return nil
}

// hfTextRoute is the per-connection text route: the context and output
// ceilings at the frozen tariff (serverless) or zero (dedicated).
func hfTextRoute(c hfConfig) (remoteRoute, bool) {
	if c.Task != hfTaskText {
		return remoteRoute{}, false
	}
	reserve := 0.0
	if c.Endpoint == hfServerless {
		if !c.Priced {
			return remoteRoute{}, false
		}
		reserve = reserveFor(c.ContextTokens, c.OutputTokens, c.InputNano, c.OutputNano)
	}
	return remoteRoute{Kind: providerKindHuggingFace, Model: c.Model, ReserveUSD: reserve, PlanUSD: 2 * reserve,
		Label: "Hugging Face", fits: func(in ScriptInput) bool {
			return localTextFits(in, localTextConfig{ContextTokens: c.ContextTokens, OutputTokens: c.OutputTokens})
		}}, true
}

func hfChatURL(base string, c hfConfig) string {
	if c.Endpoint == hfServerless {
		return hfRouterBase + "/chat/completions"
	}
	return strings.TrimRight(base, "/") + "/v1/chat/completions"
}

func newHFTextClient(doer httpDoer, base string, c hfConfig) *chatCompatClient {
	route, _ := hfTextRoute(c)
	return &chatCompatClient{doer: doer, url: hfChatURL(base, c), model: c.Model, tokensField: "max_tokens", maxTokens: c.OutputTokens,
		inputNano: c.InputNano, outputNano: c.OutputNano, reserveUSD: route.ReserveUSD}
}

// hfListedTariff reads one provider's entry from the router model listing.
type hfListedTariff struct {
	Context                  int
	InputNano, OutputNano    int64
	StructuredOutput, Priced bool
}

func hfTariff(data []byte, model string) (hfListedTariff, bool) {
	_, provider, _ := strings.Cut(model, ":")
	var listing struct {
		Providers []struct {
			Provider       string `json:"provider"`
			Status         string `json:"status"`
			ContextLength  int    `json:"context_length"`
			SupportsStruct *bool  `json:"supports_structured_output"`
			Pricing        *struct {
				Input  *float64 `json:"input"`
				Output *float64 `json:"output"`
			} `json:"pricing"`
		} `json:"providers"`
	}
	if json.Unmarshal(data, &listing) != nil {
		return hfListedTariff{}, false
	}
	nano := func(usdPerM float64) int64 { return int64(math.Ceil(usdPerM * 1000)) }
	for _, p := range listing.Providers {
		if p.Provider != provider || p.Status != "live" {
			continue
		}
		out := hfListedTariff{Context: p.ContextLength, StructuredOutput: p.SupportsStruct != nil && *p.SupportsStruct}
		if pr := p.Pricing; pr != nil && pr.Input != nil && pr.Output != nil && *pr.Input >= 0 && *pr.Output >= 0 &&
			!math.IsInf(*pr.Input, 0) && !math.IsInf(*pr.Output, 0) {
			out.InputNano, out.OutputNano, out.Priced = nano(*pr.Input), nano(*pr.Output), true
		}
		return out, true
	}
	return hfListedTariff{}, false
}

// testHF is the G12 test of a Hugging Face connection. It checks the token
// (whoami), and for serverless text reads the pinned provider's live entry:
// it must support structured output and list a tariff, which the returned
// config freezes. A dedicated text endpoint must answer its OpenAI model
// list. Nothing is generated and no user content is sent.
func testHF(ctx context.Context, doer httpDoer, base, key string, c hfConfig) (hfConfig, error) {
	if strings.TrimSpace(key) == "" {
		return c, providerAdapterError(CodeProviderAuth, false, false)
	}
	data, _, status, err := getJSON(ctx, doer, hfWhoAmIURL, "Authorization", "Bearer "+key, 256<<10)
	if err != nil {
		return c, err
	}
	if status < 200 || status >= 300 {
		return c, classifyChatStatus(status, data)
	}
	var who struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &who) != nil || who.Name == "" {
		return c, upstreamAdapterError(false)
	}
	switch {
	case c.Endpoint == hfServerless:
		repo, _, _ := strings.Cut(c.Model, ":")
		data, _, status, err = getJSON(ctx, doer, hfRouterBase+"/models/"+repo, "Authorization", "Bearer "+key, 1<<20)
		if err != nil {
			return c, err
		}
		if status < 200 || status >= 300 {
			return c, classifyChatStatus(status, data)
		}
		tariff, ok := hfTariff(data, c.Model)
		if !ok || !tariff.StructuredOutput || !tariff.Priced {
			c.Priced, c.InputNano, c.OutputNano = false, 0, 0
			return c, &adapterError{code: CodeNoProvider, safe: "The provider cannot serve structured scripts at a listed rate.", cause: errNoListedModels}
		}
		if tariff.Context > 0 && tariff.Context < c.ContextTokens {
			c.ContextTokens = tariff.Context
			if c.OutputTokens >= c.ContextTokens {
				return c, &adapterError{code: CodeNoProvider, safe: "The provider's context window is too small.", cause: errNoListedModels}
			}
		}
		c.Priced, c.InputNano, c.OutputNano = true, tariff.InputNano, tariff.OutputNano
	case c.Task == hfTaskText:
		data, _, status, err = getJSON(ctx, doer, strings.TrimRight(base, "/")+"/v1/models", "Authorization", "Bearer "+key, 1<<20)
		if err != nil {
			return c, err
		}
		if status < 200 || status >= 300 {
			return c, classifyChatStatus(status, data)
		}
		if _, ok := listedModelIDs(data); !ok {
			return c, upstreamAdapterError(false)
		}
	}
	return c, nil
}

// hfImage sends one text-to-image request to a dedicated endpoint and reads
// the image bytes it returns.
func hfImage(ctx context.Context, doer httpDoer, base, key string, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	params := map[string]any{"width": req.Width, "height": req.Height, "seed": req.Seed & math.MaxInt32}
	if req.Negative != "" {
		params["negative_prompt"] = req.Negative
	}
	raw, err := json.Marshal(map[string]any{"inputs": plainImagePromptNoAvoid(req), "parameters": params})
	if err != nil || doer == nil {
		return out, badInputAdapterError()
	}
	callCtx, cancel := context.WithTimeout(ctx, imageCallTimeout)
	defer cancel()
	r, err := http.NewRequestWithContext(callCtx, http.MethodPost, base, bytes.NewReader(raw))
	if err != nil {
		return out, badInputAdapterError()
	}
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "image/png")
	out.Sent = true
	resp, err := doer.Do(r)
	if err != nil {
		return out, classifyTransportError(err)
	}
	if resp == nil || resp.Body == nil {
		return out, upstreamAdapterError(true)
	}
	defer resp.Body.Close()
	data, large, err := readBounded(resp.Body, maxImageBytes)
	if err != nil || large {
		return out, badOutputAdapterError(false)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A cold start answers 502 while the replica initialises.
		return out, classifyChatStatus(resp.StatusCode, data)
	}
	g, err := fitGeneratedImage(data, req.Width, req.Height, req.Seed, true)
	if err != nil {
		return out, err
	}
	out.Images = []GeneratedImage{g}
	return out, nil
}

func plainImagePromptNoAvoid(req ImageGenerateRequest) string {
	if req.Style != "" {
		return req.Prompt + "\nStyle: " + req.Style
	}
	return req.Prompt
}

// Cost markers a Hugging Face connection's view carries for G2 (never a
// price the provider set: a per-call bound, or how it is billed).
const (
	hfCostHourly   = "hourly"
	hfCostUnpriced = "unpriced"
)

func hfCostLabel(c hfConfig) string {
	if c.Endpoint == hfDedicated {
		return hfCostHourly
	}
	route, ok := hfTextRoute(c)
	if !ok {
		return hfCostUnpriced
	}
	return strconv.FormatFloat(route.ReserveUSD, 'f', 6, 64)
}

// hfManifest describes a dedicated image endpoint for the router.
func hfManifest(id, name string, c hfConfig) ModelManifest {
	return ModelManifest{ID: customModelPrefix + id, Name: name, Maker: "Hugging Face", Kind: "image",
		Licence: c.Model, Commercial: c.Image.Commercial, Providers: []string{providerKindHuggingFace}, BW: true,
		MaxWidth: c.Image.MaxWidth, MaxHeight: c.Image.MaxHeight, MaxCount: 1, Quality: 60, Speed: 50}
}
