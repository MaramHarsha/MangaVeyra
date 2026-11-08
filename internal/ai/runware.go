package ai

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Runware image route (ADR 0013 §9). Facts frozen from Runware's
// documentation, accessed 2026-11-09:
//   - POST https://api.runware.ai/v1, "Every request is an array" of tasks,
//     Authorization: Bearer or a leading authentication task:
//     https://runware.ai/docs/models-api/authentication, https://runware.ai/docs/models-api/introduction
//   - imageInference for FLUX.1 [schnell] (runware:100@1): positivePrompt
//     2–3000 characters, width/height 128–2048 in multiples of 64,
//     numberResults, steps (default 4), outputType base64Data, outputFormat
//     PNG, includeCost, safety.checkContent, inputs.seedImage/maskImage
//     ("White marks the area to edit"), strength:
//     https://runware.ai/docs/models/bfl-flux-1-schnell (schema.json)
//   - errors {errors:[{code, message, taskUUID, retryAfter}]}, 402 for an
//     insufficient balance, invalidApiKey: https://runware.ai/docs/models-api/errors
//   - sync requests wait up to 120 s, then the task continues and is polled
//     with getResponse; "resending the task starts a second generation":
//     https://runware.ai/docs/models-api/task-polling
//   - billing is by compute time ("~$0.0018 per megapixel" for schnell at four
//     steps, USD 0.0013 at 1024²); failed generations are not charged:
//     https://runware.ai/docs/models-api/pricing
//
// Runware publishes no per-request ceiling, so the quote is an application
// allowance (as ADR 0009's input-image allowance): USD 0.006 per output
// megapixel, about 4.8 times the measured price at 1024². The reported cost
// (includeCost) is recorded unclamped, so an overrun blocks later
// reservations (ADR 0003).
// Status: fake-transport tested, not live-verified.
const (
	runwareURL         = "https://api.runware.ai/v1"
	runwareModel       = "runware/flux-schnell" // catalogue id
	runwareAIR         = "runware:100@1"        // Runware model identifier
	runwareNanoPerPx   = 6                      // USD 0.006 per megapixel
	runwareEdge        = 1024
	runwareMaxResults  = 4
	runwarePromptRunes = 3000
)

func runwareSize(v int) int {
	v = int(math.Round(float64(v)/64)) * 64
	return min(max(v, 128), runwareEdge)
}

func quoteRunware(n ImageNeeds) (jobs.Cost, bool) {
	if n.Count < 1 || n.Count > runwareMaxResults || n.References > 0 || n.Width > runwareEdge || n.Height > runwareEdge {
		return jobs.Cost{}, false
	}
	w, h := runwareSize(n.Width), runwareSize(n.Height)
	return nanoToCost(int64(n.Count) * int64(w*h) * runwareNanoPerPx), true
}

func runwarePrompt(req ImageGenerateRequest) string {
	// FLUX schnell ignores negativePrompt (CFG ≤ 1); the avoid list stays
	// in the description, which already ends with the fixed rules.
	prompt := req.Prompt
	if req.Style != "" {
		prompt += "\nStyle: " + req.Style
	}
	return prompt
}

func validateRunwareRequest(req ImageGenerateRequest) error {
	if n := utf8.RuneCountInString(runwarePrompt(req)); n < 2 || n > runwarePromptRunes {
		return imageError(CodeBadInput, false, jobs.Cost{})
	}
	return nil
}

// newTaskUUID is a random UUID v4, as every Runware task requires.
func newTaskUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func runwareTask(req ImageGenerateRequest, taskUUID string) (map[string]any, error) {
	w, h := runwareSize(req.Width), runwareSize(req.Height)
	task := map[string]any{"taskType": "imageInference", "taskUUID": taskUUID, "model": runwareAIR,
		"positivePrompt": runwarePrompt(req), "width": w, "height": h, "numberResults": req.Count, "steps": 4,
		"seed": req.Seed & math.MaxInt64, "outputType": "base64Data", "outputFormat": "PNG", "includeCost": true,
		"deliveryMethod": "sync", "safety": map[string]bool{"checkContent": true}}
	if req.Init != nil {
		init, err := resizedDataURL(req.Init.Data, w, h, true)
		if err != nil {
			return nil, badInputAdapterError()
		}
		inputs := map[string]string{"seedImage": init}
		if req.Mask != nil {
			mask, err := resizedDataURL(req.Mask.Data, w, h, false)
			if err != nil {
				return nil, badInputAdapterError()
			}
			inputs["maskImage"] = mask
		}
		task["inputs"] = inputs
		strength := 0.8
		if req.Strength > 0 {
			strength = req.Strength
		}
		task["strength"] = strength
	}
	return task, nil
}

type runwareResponse struct {
	Data []struct {
		TaskType  string   `json:"taskType"`
		TaskUUID  string   `json:"taskUUID"`
		Status    string   `json:"status"`
		Image     string   `json:"imageBase64Data"`
		NSFW      bool     `json:"NSFWContent"`
		Cost      *float64 `json:"cost"`
		ImageUUID string   `json:"imageUUID"`
	} `json:"data"`
	Errors []struct {
		Code     string `json:"code"`
		TaskUUID string `json:"taskUUID"`
	} `json:"errors"`
}

// classifyRunware maps Runware's error codes and statuses to safe codes.
func classifyRunware(status int, r runwareResponse) error {
	code := ""
	if len(r.Errors) > 0 {
		code = r.Errors[0].Code
	}
	switch {
	case code == "invalidApiKey" || status == http.StatusUnauthorized || status == http.StatusForbidden:
		return providerAdapterError(CodeProviderAuth, false, false)
	case status == http.StatusPaymentRequired || strings.Contains(strings.ToLower(code), "credit") || strings.Contains(strings.ToLower(code), "balance"):
		return quotaAdapterError()
	case code == "timeoutProvider":
		return providerAdapterError(CodeTimeout, true, false)
	case code == "providerRateLimitExceeded" || code == "serviceOverloaded" || status == http.StatusTooManyRequests:
		return providerAdapterError(CodeRateLimited, true, false)
	case status >= 500:
		return upstreamAdapterError(true)
	case status == http.StatusNotFound:
		return &adapterError{code: CodeNoProvider, safe: "The provider key cannot use the configured model.", cause: errNoListedModels}
	}
	return &adapterError{code: CodeBadInput, safe: "The image request is invalid.", cause: errInvalidScriptInput}
}

func (s *imageService) runwareImage(ctx context.Context, key string, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	if req.Plan.ModelID != runwareModel {
		return out, badInputAdapterError()
	}
	taskUUID := newTaskUUID()
	task, err := runwareTask(req, taskUUID)
	if err != nil {
		return out, err
	}
	callCtx, cancel := context.WithTimeout(ctx, imageCallTimeout)
	defer cancel()
	body, status, sent, err := s.postImage(callCtx, runwareURL, "Bearer "+key, []any{task}, nil)
	out.Sent = sent
	if err != nil {
		if status >= 400 {
			// Failed generations are not charged.
			zero := 0.0
			out.ActualUSD = &zero
			var decoded runwareResponse
			_ = json.Unmarshal(body, &decoded)
			err = classifyRunware(status, decoded)
		}
		return out, err
	}
	images := map[string][]byte{}
	var cost float64
	costKnown, refused := true, false
	collect := func(raw []byte) (bool, error) {
		var decoded runwareResponse
		if json.Unmarshal(raw, &decoded) != nil {
			return false, badOutputAdapterError(false)
		}
		for _, e := range decoded.Errors {
			if e.TaskUUID == "" || e.TaskUUID == taskUUID {
				return false, classifyRunware(http.StatusBadRequest, decoded)
			}
		}
		pending := false
		for _, d := range decoded.Data {
			if d.TaskUUID != taskUUID {
				continue
			}
			if d.Status == "processing" {
				pending = true
				continue
			}
			if d.Image == "" || images[d.ImageUUID] != nil {
				continue
			}
			data, err := decodeBase64Image(d.Image)
			if err != nil {
				return false, badOutputAdapterError(false)
			}
			key := d.ImageUUID
			if key == "" {
				key = fmt.Sprint(len(images))
			}
			images[key] = data
			refused = refused || d.NSFW
			if d.Cost == nil || *d.Cost < 0 {
				costKnown = false
			} else {
				cost += *d.Cost
			}
		}
		return pending || len(images) < req.Count, nil
	}
	pending, err := collect(body)
	for err == nil && pending {
		// The task continues after the sync window: poll, never resend.
		if err = sleepCtx(callCtx, s.pollInterval()); err != nil {
			err = classifyTransportError(err)
			break
		}
		var raw []byte
		var code int
		raw, code, _, err = s.postImage(callCtx, runwareURL, "Bearer "+key, []any{map[string]string{"taskType": "getResponse", "taskUUID": taskUUID}}, nil)
		if err != nil {
			var decoded runwareResponse
			_ = json.Unmarshal(raw, &decoded)
			if code >= 400 {
				err = classifyRunware(code, decoded)
			}
			break
		}
		pending, err = collect(raw)
	}
	if costKnown && len(images) > 0 {
		out.ActualUSD = &cost
	}
	if err != nil {
		return out, err
	}
	if refused {
		return out, providerAdapterError(CodeProviderRefuse, false, false)
	}
	if len(images) != req.Count {
		return out, badOutputAdapterError(false)
	}
	for _, data := range images {
		g, err := fitGeneratedImage(data, req.Width, req.Height, 0, req.Init == nil)
		if err != nil {
			return out, err
		}
		out.Images = append(out.Images, g)
	}
	return out, nil
}

type runwareKeyTester struct{ doer httpDoer }

// TestKey sends only the authentication task: no generation, no content.
func (t runwareKeyTester) TestKey(ctx context.Context, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", providerAdapterError(CodeProviderAuth, false, false)
	}
	s := &imageService{doer: t.doer}
	body, status, _, err := s.postImage(ctx, runwareURL, "", []any{map[string]string{"taskType": "authentication", "apiKey": key}}, nil)
	var decoded runwareResponse
	_ = json.Unmarshal(body, &decoded)
	if err != nil {
		if status >= 400 {
			return "", classifyRunware(status, decoded)
		}
		return "", err
	}
	if len(decoded.Errors) > 0 {
		return "", classifyRunware(http.StatusUnauthorized, decoded)
	}
	if len(decoded.Data) == 0 || decoded.Data[0].TaskType != "authentication" {
		return "", upstreamAdapterError(false)
	}
	return "", nil
}
