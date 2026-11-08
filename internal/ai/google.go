package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Google Gemini API direct routes (ADR 0013 §9). Facts frozen from the Gemini
// API documentation, accessed 2026-11-09:
//   - generateContent (still "fully supported"; Interactions is the newer
//     API), x-goog-api-key, systemInstruction, generationConfig
//     responseMimeType + responseJsonSchema, maxOutputTokens, thinkingConfig,
//     safetySettings, finishReason and promptFeedback.blockReason enums,
//     usageMetadata: https://aistudio.google.com/api-reference/generate-content/generate-content.md,
//     https://aistudio.google.com/docs/structured-output.md?generate-content=true
//   - thinking: max_output_tokens "sets the maximum number of tokens a response
//     can generate, including thought tokens" and is "a hard cutoff"; the
//     response price is output plus thinking tokens; gemini-3.8-flash cannot
//     turn thinking off (LOW is its floor): https://aistudio.google.com/docs/thinking.md
//   - gemini-3.8-flash: 1,048,576 input / 65,536 output tokens; USD 0.75 /
//     3.75 per 1M through 2026-12-31 and USD 1.50 / 7.50 from 2027-01-01:
//     https://aistudio.google.com/docs/models/gemini-3.8-flash.md,
//     https://aistudio.google.com/docs/pricing.md
//   - images: Imagen is shut down on the Gemini API (2026-08-17); its
//     replacement is gemini-nano-banana-2.1 through generateContent with
//     responseModalities IMAGE and imageConfig {aspectRatio, imageSize}; input
//     images can guide it but there is no mask parameter, so masked editing is
//     absent; 1K output is 1,120 tokens at USD 30 per 1M, input USD 1.50,
//     text and thinking USD 7.50: https://aistudio.google.com/docs/imagen.md,
//     https://aistudio.google.com/docs/deprecations.md,
//     https://aistudio.google.com/docs/image-generation.md?generate-content=true
//   - key check: GET /v1beta/models (models[].name "models/…"); errors
//     {"error":{"code","message","status","details"}}, API_KEY_INVALID, 402
//     RESOURCE_EXHAUSTED for exhausted prepay: https://aistudio.google.com/api-reference/models/list-models.md,
//     https://aistudio.google.com/docs/api-errors.md?generate-content=true
//
// Reservations use the 2027 tariff (the higher one), so they stay upper
// bounds across the documented price change. Prompts are capped at 131,072
// tokens and the completion (thinking included) at 32,768 tokens.
// Status: fake-transport tested, not live-verified.
const (
	googleAPIBase      = "https://generativelanguage.googleapis.com/v1beta"
	googleTextModel    = "gemini-3.8-flash"
	googleImageModel   = "gemini-nano-banana-2.1"
	googleInputTokens  = 131_072
	googleOutputTokens = 32_768
	googleInputNano    = 1_500 // USD 1.50 / 1M input tokens (2027 tariff)
	googleOutputNano   = 7_500 // USD 7.50 / 1M output and thinking tokens
	googleReserveUSD   = 0.442368
	googlePlanUSD      = 2 * googleReserveUSD

	// Nano Banana 2.1 (direct): one 1K image of 1,120 output tokens plus a
	// 4,096-token maxOutputTokens ceiling. The quote prices all of those
	// tokens at the image rate, so it holds whether or not the ceiling also
	// counts image tokens (not documented).
	googleImageOutputTokens = 1_120
	googleImageMaxOutput    = 4_096
	googleImageOutNano      = 30_000 // USD 30 / 1M image output tokens
	googleImageTextOutNano  = 7_500  // USD 7.50 / 1M text and thinking output
	googleImageInNano       = 1_500  // USD 1.50 / 1M text or image input
	googleImageInputTokens  = 1_120  // allowance per input image (as ADR 0009)
)

// googleSafety is sent with every request. The Gemini default for current
// models is no blocking; manga action stays allowed while sexual content is
// refused from a medium probability (the prompts already forbid it).
var googleSafety = []map[string]string{
	{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_ONLY_HIGH"},
	{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_ONLY_HIGH"},
	{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_MEDIUM_AND_ABOVE"},
	{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_ONLY_HIGH"},
}

type googlePart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *googleInlineData `json:"inlineData,omitempty"`
}

type googleInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type googleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []googlePart `json:"parts"`
}

type googleRequest struct {
	SystemInstruction *googleContent      `json:"systemInstruction,omitempty"`
	Contents          []googleContent     `json:"contents"`
	GenerationConfig  map[string]any      `json:"generationConfig"`
	SafetySettings    []map[string]string `json:"safetySettings"`
}

type googleResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text       string            `json:"text"`
				Thought    bool              `json:"thought"`
				InlineData *googleInlineData `json:"inlineData"`
				// The REST samples write inline_data; accept both spellings.
				InlineDataSnake *struct {
					MimeType string `json:"mime_type"`
					Data     string `json:"data"`
				} `json:"inline_data"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *struct {
		PromptTokenCount        *int64 `json:"promptTokenCount"`
		CandidatesTokenCount    *int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount      *int64 `json:"thoughtsTokenCount"`
		CandidatesTokensDetails []struct {
			Modality   string `json:"modality"`
			TokenCount int64  `json:"tokenCount"`
		} `json:"candidatesTokensDetails"`
	} `json:"usageMetadata"`
}

// googleRefusal reports a finish or block reason that is a safety or policy
// refusal rather than a malformed answer.
func googleRefusal(reason string) bool {
	switch reason {
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY",
		"IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
		return true
	}
	return false
}

func (r googleResponse) blocked() bool {
	if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
		return true
	}
	return len(r.Candidates) > 0 && googleRefusal(r.Candidates[0].FinishReason)
}

type googleTextClient struct{ doer httpDoer }

func newGoogleTextClient(doer httpDoer) *googleTextClient { return &googleTextClient{doer: doer} }

func googleTextURL() string { return googleAPIBase + "/models/" + googleTextModel + ":generateContent" }

func buildGoogleTextRequest(in ScriptInput, attempt scriptAttempt) (googleRequest, error) {
	base, err := buildOpenRouterRequest(in, attempt)
	if err != nil || len(base.Messages) != 2 {
		return googleRequest{}, errInvalidScriptInput
	}
	return googleRequest{
		SystemInstruction: &googleContent{Parts: []googlePart{{Text: base.Messages[0].Content}}},
		Contents:          []googleContent{{Role: "user", Parts: []googlePart{{Text: base.Messages[1].Content}}}},
		GenerationConfig: map[string]any{
			"responseMimeType":   "application/json",
			"responseJsonSchema": base.ResponseFormat.JSONSchema.Schema,
			"maxOutputTokens":    googleOutputTokens,
			// The lowest level gemini-3.8-flash accepts; thinking shares
			// maxOutputTokens, which bounds the charge.
			"thinkingConfig": map[string]string{"thinkingLevel": "LOW"},
		},
		SafetySettings: googleSafety,
	}, nil
}

// googleTextCost prices reported usage at the frozen tariff, rounded up and
// clamped to the per-call reservation like ADR 0006.
func googleTextCost(r googleResponse) *float64 {
	u := r.UsageMetadata
	if u == nil || u.PromptTokenCount == nil || u.CandidatesTokenCount == nil || *u.PromptTokenCount < 0 || *u.CandidatesTokenCount < 0 {
		return nil
	}
	thoughts := int64(0)
	if u.ThoughtsTokenCount != nil && *u.ThoughtsTokenCount > 0 {
		thoughts = *u.ThoughtsTokenCount
	}
	usd := nanoToCost(*u.PromptTokenCount*googleInputNano + (*u.CandidatesTokenCount+thoughts)*googleOutputNano).USD
	if usd > googleReserveUSD {
		usd = googleReserveUSD
	}
	return &usd
}

func (c *googleTextClient) Complete(ctx context.Context, key string, in ScriptInput, attempt scriptAttempt) (textCall, error) {
	var out textCall
	if c == nil || c.doer == nil || strings.TrimSpace(key) == "" {
		return out, providerAdapterError(CodeProviderAuth, false, false)
	}
	payload, err := buildGoogleTextRequest(in, attempt)
	if err != nil {
		return out, badInputAdapterError()
	}
	data, status, sent, err := googlePost(ctx, c.doer, googleTextURL(), key, payload, openAITimeout, openAIResponseLimit)
	out.Sent = sent
	if err != nil {
		if status >= 400 {
			zero := 0.0
			out.ActualUSD = &zero
		}
		return out, err
	}
	var decoded googleResponse
	if json.Unmarshal(data, &decoded) != nil {
		return out, badOutputAdapterError(true)
	}
	out.ActualUSD = googleTextCost(decoded)
	if decoded.blocked() {
		return out, providerAdapterError(CodeProviderRefuse, false, false)
	}
	if len(decoded.Candidates) == 0 {
		return out, badOutputAdapterError(true)
	}
	var text strings.Builder
	for _, p := range decoded.Candidates[0].Content.Parts {
		if !p.Thought {
			text.WriteString(p.Text)
		}
	}
	if decoded.Candidates[0].FinishReason != "STOP" || strings.TrimSpace(text.String()) == "" {
		return out, badOutputAdapterError(true)
	}
	out.Content = json.RawMessage(text.String())
	return out, nil
}

// googlePost sends one generateContent request. The key travels only in the
// x-goog-api-key header, never in the URL.
func googlePost(ctx context.Context, doer httpDoer, url, key string, payload any, timeout time.Duration, limit int64) ([]byte, int, bool, error) {
	raw, err := json.Marshal(payload)
	if err != nil || doer == nil {
		return nil, 0, false, badInputAdapterError()
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, false, badInputAdapterError()
	}
	req.Header.Set("x-goog-api-key", key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := doer.Do(req)
	if err != nil {
		return nil, 0, true, classifyTransportError(err)
	}
	if resp == nil || resp.Body == nil {
		return nil, 0, true, upstreamAdapterError(true)
	}
	defer resp.Body.Close()
	data, tooLarge, err := readBounded(resp.Body, limit)
	if err != nil {
		return nil, resp.StatusCode, true, upstreamAdapterError(true)
	}
	if tooLarge {
		return nil, resp.StatusCode, true, badOutputAdapterError(false)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, true, classifyGoogleStatus(resp.StatusCode, data)
	}
	return data, resp.StatusCode, true, nil
}

// classifyGoogleStatus maps a Gemini API error from its HTTP status, gRPC
// status and ErrorInfo reason only; the message is never read or kept.
func classifyGoogleStatus(status int, body []byte) error {
	var decoded struct {
		Error *struct {
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &decoded)
	if e := decoded.Error; e != nil {
		for _, d := range e.Details {
			if d.Reason == "API_KEY_INVALID" {
				return providerAdapterError(CodeProviderAuth, false, false)
			}
		}
		switch e.Status {
		case "UNAUTHENTICATED", "PERMISSION_DENIED", "FAILED_PRECONDITION":
			return providerAdapterError(CodeProviderAuth, false, false)
		case "NOT_FOUND":
			return &adapterError{code: CodeNoProvider, safe: "The provider key cannot use the configured model.", cause: errNoListedModels}
		case "RESOURCE_EXHAUSTED":
			if status == http.StatusPaymentRequired {
				return quotaAdapterError()
			}
			return providerAdapterError(CodeRateLimited, true, false)
		case "DEADLINE_EXCEEDED":
			return providerAdapterError(CodeTimeout, true, false)
		}
	}
	switch status {
	case http.StatusPaymentRequired:
		return quotaAdapterError()
	case http.StatusNotFound:
		return &adapterError{code: CodeNoProvider, safe: "The provider key cannot use the configured model.", cause: errNoListedModels}
	}
	return classifyHTTPStatus(status)
}

type googleKeyTester struct{ doer httpDoer }

func (t googleKeyTester) TestKey(ctx context.Context, key string) (string, error) {
	test, err := t.TestCapabilities(ctx, key)
	return test.RequestID, err
}

// TestCapabilities lists the models the key can use (one GET, no
// generation) and reports the frozen text and image models.
func (t googleKeyTester) TestCapabilities(ctx context.Context, key string) (providerTestState, error) {
	var out providerTestState
	if strings.TrimSpace(key) == "" {
		return out, providerAdapterError(CodeProviderAuth, false, false)
	}
	data, _, status, err := getJSON(ctx, t.doer, googleAPIBase+"/models?pageSize=1000", "x-goog-api-key", key, openAIModelResponseLimit)
	if err != nil {
		return out, err
	}
	if status < 200 || status >= 300 {
		return out, classifyGoogleStatus(status, data)
	}
	var list struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &list) != nil || list.Models == nil {
		return out, upstreamAdapterError(false)
	}
	listed := map[string]bool{}
	for _, m := range list.Models {
		listed[strings.TrimPrefix(m.Name, "models/")] = true
	}
	return capabilitiesFrom(out, listed, googleTextModel, providerKindGoogle)
}

// quoteGoogleImage bounds one direct Nano Banana 2.1 call: exactly one 1K
// image, the composed prompt bound and the reference allowance (ADR 0009's
// Nano Banana rule with a real output ceiling instead of an allowance).
func quoteGoogleImage(n ImageNeeds) (jobs.Cost, bool) {
	if n.Count != 1 || n.Edit || n.Mask {
		return jobs.Cost{}, false
	}
	nano := int64(googleImageOutputTokens+googleImageMaxOutput)*googleImageOutNano + imagePromptBytes*googleImageInNano +
		routeInputImages(n, nanoBananaMaxRefs)*googleImageInputTokens*googleImageInNano
	return nanoToCost(nano), true
}

// googleImageCost prices reported usage: image output tokens at the image
// rate, other output and thinking at the text rate; without a modality split
// every output token is priced at the image rate. Not clamped (ADR 0009).
func googleImageCost(r googleResponse) *float64 {
	u := r.UsageMetadata
	if u == nil || u.PromptTokenCount == nil || u.CandidatesTokenCount == nil || *u.PromptTokenCount < 0 || *u.CandidatesTokenCount < 0 {
		return nil
	}
	thoughts := int64(0)
	if u.ThoughtsTokenCount != nil && *u.ThoughtsTokenCount > 0 {
		thoughts = *u.ThoughtsTokenCount
	}
	nano := *u.PromptTokenCount * googleImageInNano
	if len(u.CandidatesTokensDetails) == 0 {
		nano += (*u.CandidatesTokenCount + thoughts) * googleImageOutNano
	} else {
		image := int64(0)
		for _, d := range u.CandidatesTokensDetails {
			if d.Modality == "IMAGE" && d.TokenCount > 0 {
				image += d.TokenCount
			}
		}
		other := max(*u.CandidatesTokenCount-image, 0)
		nano += image*googleImageOutNano + (other+thoughts)*googleImageTextOutNano
	}
	usd := nanoToCost(nano).USD
	return &usd
}

func googleImagePrompt(req ImageGenerateRequest, refs []routeRef) string {
	// Nano Banana has no alpha output (ADR 0009), so the prompt never asks
	// for transparency; the layer is composited as an opaque image.
	opaque := req
	opaque.Background = ""
	return gptImagePrompt(opaque, refs, false)
}

func validateGoogleImageRequest(req ImageGenerateRequest) error {
	if req.Count != 1 || req.Init != nil || req.Mask != nil || len(googleImagePrompt(req, routeReferences(req.References, nanoBananaMaxRefs))) > imagePromptBytes {
		return imageError(CodeBadInput, false, jobs.Cost{})
	}
	return nil
}

func googleImagePayload(req ImageGenerateRequest) (googleRequest, error) {
	refs := routeReferences(req.References, nanoBananaMaxRefs)
	parts := []googlePart{{Text: googleImagePrompt(req, refs)}}
	for _, r := range refs {
		data, err := r.encode(gptImageRefEdge)
		if err != nil {
			return googleRequest{}, badInputAdapterError()
		}
		parts = append(parts, googlePart{InlineData: &googleInlineData{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(data)}})
	}
	return googleRequest{
		Contents: []googleContent{{Role: "user", Parts: parts}},
		GenerationConfig: map[string]any{
			"responseModalities": []string{"IMAGE"},
			"imageConfig":        map[string]string{"aspectRatio": nearestAspectFrom(req.Width, req.Height, nanoBananaAspects), "imageSize": "1K"},
			"maxOutputTokens":    googleImageMaxOutput,
		},
		SafetySettings: googleSafety,
	}, nil
}

func (s *imageService) googleImage(ctx context.Context, key string, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	if req.Plan.ModelID != googleImageModel || strings.TrimSpace(key) == "" {
		return out, badInputAdapterError()
	}
	payload, err := googleImagePayload(req)
	if err != nil {
		return out, err
	}
	data, status, sent, err := googlePost(ctx, s.doer, googleAPIBase+"/models/"+googleImageModel+":generateContent", key, payload, imageCallTimeout, 128<<20)
	out.Sent = sent
	if err != nil {
		if status >= 400 {
			zero := 0.0
			out.ActualUSD = &zero
		}
		return out, err
	}
	var decoded googleResponse
	if json.Unmarshal(data, &decoded) != nil {
		return out, badOutputAdapterError(false)
	}
	out.ActualUSD = googleImageCost(decoded)
	var image []byte
	if len(decoded.Candidates) > 0 {
		for _, p := range decoded.Candidates[0].Content.Parts {
			inline := p.InlineData
			if inline == nil && p.InlineDataSnake != nil {
				inline = &googleInlineData{MimeType: p.InlineDataSnake.MimeType, Data: p.InlineDataSnake.Data}
			}
			if p.Thought || inline == nil || !strings.HasPrefix(inline.MimeType, "image/") {
				continue
			}
			if image, err = decodeBase64Image(inline.Data); err != nil {
				return out, badOutputAdapterError(false)
			}
			break
		}
	}
	if image == nil {
		if decoded.blocked() {
			return out, providerAdapterError(CodeProviderRefuse, false, false)
		}
		return out, badOutputAdapterError(false)
	}
	g, err := fitGeneratedImage(image, req.Width, req.Height, 0, true)
	if err != nil {
		return out, err
	}
	out.Images = []GeneratedImage{g}
	return out, nil
}
