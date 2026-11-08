package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	xdraw "golang.org/x/image/draw"
)

// Replicate image routes (ADR 0013 §9). Facts frozen from Replicate's
// documentation, accessed 2026-11-09:
//   - predictions: POST /v1/predictions {version, input} where version is
//     "owner/name:<64-hex>" for an exact pin, POST /v1/models/{owner}/{name}/predictions
//     for official models (which "do not support versions" but keep a stable
//     input/output API), Prefer: wait=n (1–60 s), Cancel-After, GET
//     /v1/predictions/{id}, POST …/cancel, statuses starting | processing |
//     succeeded | failed | canceled | aborted, metrics.predict_time:
//     https://replicate.com/docs/reference/http, https://api.replicate.com/openapi.json,
//     https://replicate.com/docs/topics/predictions/create-a-prediction,
//     https://replicate.com/docs/topics/models/official-models
//   - outputs are served from replicate.delivery and expire after an hour:
//     https://replicate.com/docs/topics/predictions/output-files
//   - "if a run fails, we don't charge you"; a canceled official run may still
//     be charged: https://replicate.com/docs/topics/billing
//   - black-forest-labs/flux-schnell: USD 0.003 per output image, Apache-2.0,
//     num_outputs 1–4, aspect_ratio enum, megapixels "1":
//     https://replicate.com/black-forest-labs/flux-schnell/api/schema
//   - stability-ai/sdxl version 7762fd07…9bdc: billed by time on an L40S at
//     USD 0.000975 per second; mask "white areas will be inpainted":
//     https://replicate.com/stability-ai/sdxl/api/schema, https://replicate.com/pricing
//   - key check: GET /v1/account: https://api.replicate.com/openapi.json
//
// flux-schnell is an official model, so its API is stable but it cannot be
// pinned to a version; the SDXL route pins an exact version. SDXL is billed
// by time: its quote is the Cancel-After deadline at the hardware rate, an
// application bound (a public model's queue and boot time are not billed),
// so the SDXL route is pin-only and never widens an unpinned estimate.
// Status: fake-transport tested, not live-verified.
const (
	replicateAPI          = "https://api.replicate.com/v1"
	replicateFluxModel    = "black-forest-labs/flux-schnell"
	replicateSDXLModel    = "stability-ai/sdxl"
	replicateSDXLVersion  = "7762fd07cf82c948538e41f63f77d685e02b063e37e496e96eefd46c929f9bdc"
	replicateFluxImageUSD = 0.003
	// replicateCancelAfter bounds a time-billed prediction; replicateWait is
	// how long the create call may hold the connection.
	replicateCancelAfter    = 120
	replicateWait           = 60
	replicateL40SNanoPerSec = 975_000 // USD 0.000975 per second
	replicateSDXLEdge       = 1024
	replicateMaxOutputs     = 4
)

var replicateFluxAspects = []string{"1:1", "16:9", "21:9", "3:2", "2:3", "4:5", "5:4", "3:4", "4:3", "9:16", "9:21"}

func quoteReplicateFlux(n ImageNeeds) (jobs.Cost, bool) {
	if n.Count < 1 || n.Count > replicateMaxOutputs || n.Edit || n.Mask || n.References > 0 {
		return jobs.Cost{}, false
	}
	return nanoToCost(int64(n.Count) * 3_000_000), true
}

// quoteReplicateSDXL is the whole Cancel-After window at the L40S rate: a
// prediction cannot run longer before Replicate cancels it.
func quoteReplicateSDXL(n ImageNeeds) (jobs.Cost, bool) {
	if n.Count < 1 || n.Count > replicateMaxOutputs || n.References > 0 || n.Width > replicateSDXLEdge || n.Height > replicateSDXLEdge {
		return jobs.Cost{}, false
	}
	return nanoToCost(replicateCancelAfter * replicateL40SNanoPerSec), true
}

func replicateSize(v int) int {
	v = int(math.Round(float64(v)/8)) * 8
	return min(max(v, 128), replicateSDXLEdge)
}

type replicatePrediction struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Output json.RawMessage `json:"output"`
	Error  json.RawMessage `json:"error"`
	URLs   struct {
		Get    string `json:"get"`
		Cancel string `json:"cancel"`
	} `json:"urls"`
	Metrics *struct {
		PredictTime *float64 `json:"predict_time"`
	} `json:"metrics"`
}

func (p replicatePrediction) terminal() bool {
	switch p.Status {
	case "succeeded", "failed", "canceled", "aborted":
		return true
	}
	return false
}

// replicateRequest is the create URL and body of one route.
func replicateRequest(req ImageGenerateRequest) (string, map[string]any, error) {
	seed := req.Seed & math.MaxInt32
	switch req.Plan.ModelID {
	case replicateFluxModel:
		if req.Init != nil || req.Mask != nil {
			return "", nil, badInputAdapterError()
		}
		return replicateAPI + "/models/" + replicateFluxModel + "/predictions", map[string]any{"input": map[string]any{
			"prompt": plainImagePrompt(req), "aspect_ratio": nearestAspectFrom(req.Width, req.Height, replicateFluxAspects),
			"num_outputs": req.Count, "num_inference_steps": 4, "seed": seed, "output_format": "png", "megapixels": "1",
			"disable_safety_checker": false,
		}}, nil
	case replicateSDXLModel:
		input := map[string]any{"prompt": req.Prompt, "width": replicateSize(req.Width), "height": replicateSize(req.Height),
			"num_outputs": req.Count, "num_inference_steps": 30, "seed": seed, "disable_safety_checker": false}
		if req.Negative != "" {
			input["negative_prompt"] = req.Negative
		}
		if req.Init != nil {
			w, h := replicateSize(req.Width), replicateSize(req.Height)
			init, err := resizedDataURL(req.Init.Data, w, h, true)
			if err != nil {
				return "", nil, badInputAdapterError()
			}
			input["image"] = init
			strength := 0.8
			if req.Strength > 0 {
				strength = req.Strength
			}
			input["prompt_strength"] = strength
			if req.Mask != nil {
				mask, err := resizedDataURL(req.Mask.Data, w, h, false)
				if err != nil {
					return "", nil, badInputAdapterError()
				}
				input["mask"] = mask
			}
		}
		return replicateAPI + "/predictions", map[string]any{"version": replicateSDXLModel + ":" + replicateSDXLVersion, "input": input}, nil
	}
	return "", nil, badInputAdapterError()
}

// resizedDataURL stretches an input to the request size and encodes it as a
// data URL: JPEG for the init image (Replicate recommends small data URLs),
// lossless PNG for a mask so its edges stay exact.
func resizedDataURL(data []byte, w, h int, photo bool) (string, error) {
	src, err := decodeBounded(data)
	if err != nil {
		return "", err
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	scaler := xdraw.Interpolator(xdraw.CatmullRom)
	if !photo {
		scaler = xdraw.NearestNeighbor
	}
	scaler.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	if !photo {
		out, err := encodePNG(dst)
		return pngDataURL(out), err
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, dst, &jpeg.Options{Quality: 92}); err != nil {
		return "", err
	}
	return imageDataURL(ImageRef{Data: b.Bytes(), ContentType: "image/jpeg"}), nil
}

func (s *imageService) replicateImage(ctx context.Context, key string, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	endpoint, payload, err := replicateRequest(req)
	if err != nil {
		return out, err
	}
	callCtx, cancel := context.WithTimeout(ctx, imageCallTimeout)
	defer cancel()
	headers := map[string]string{"Prefer": "wait=" + strconv.Itoa(replicateWait), "Cancel-After": strconv.Itoa(replicateCancelAfter) + "s"}
	body, status, sent, err := s.postImage(callCtx, endpoint, "Bearer "+key, payload, headers)
	out.Sent = sent
	if err != nil {
		if status >= 400 {
			// The prediction was refused before it started.
			zero := 0.0
			out.ActualUSD = &zero
		}
		return out, replicateStatusError(status, err)
	}
	var pred replicatePrediction
	if json.Unmarshal(body, &pred) != nil || pred.ID == "" {
		return out, badOutputAdapterError(false)
	}
	for !pred.terminal() {
		if !strings.HasPrefix(pred.URLs.Get, replicateAPI+"/predictions/") {
			return out, badOutputAdapterError(false)
		}
		if err := sleepCtx(callCtx, s.pollInterval()); err != nil {
			s.replicateCancel(ctx, key, pred)
			return out, classifyTransportError(err)
		}
		data, _, code, err := getJSON(callCtx, s.doer, pred.URLs.Get, "Authorization", "Bearer "+key, 1<<20)
		if err != nil || code < 200 || code >= 300 {
			s.replicateCancel(ctx, key, pred)
			if err == nil {
				err = replicateStatusError(code, classifyHTTPStatus(code))
			}
			return out, err
		}
		var next replicatePrediction
		if json.Unmarshal(data, &next) != nil || next.ID != pred.ID {
			s.replicateCancel(ctx, key, pred)
			return out, badOutputAdapterError(false)
		}
		pred = next
	}
	switch pred.Status {
	case "failed", "aborted":
		zero := 0.0
		out.ActualUSD = &zero
		// Classified from the error text without keeping or showing it.
		var text string
		_ = json.Unmarshal(pred.Error, &text)
		if strings.Contains(strings.ToLower(text), "nsfw") {
			return out, providerAdapterError(CodeProviderRefuse, false, false)
		}
		return out, upstreamAdapterError(true)
	case "canceled":
		return out, upstreamAdapterError(true)
	}
	out.ActualUSD = replicateActual(req, pred)
	urls, ok := replicateOutputs(pred.Output)
	if !ok || len(urls) != req.Count {
		return out, badOutputAdapterError(false)
	}
	for _, u := range urls {
		data, err := s.deliveryBytes(callCtx, u, replicateDeliveryHost)
		if err != nil {
			return out, err
		}
		g, err := fitGeneratedImage(data, req.Width, req.Height, 0, req.Init == nil)
		if err != nil {
			return out, err
		}
		out.Images = append(out.Images, g)
	}
	return out, nil
}

// replicateActual is the per-image tariff for flux-schnell and the reported
// predict time at the hardware rate for SDXL (nil without metrics, which
// charges the reservation).
func replicateActual(req ImageGenerateRequest, pred replicatePrediction) *float64 {
	if req.Plan.ModelID == replicateFluxModel {
		quote, _ := quoteReplicateFlux(req.Plan.Needs)
		return &quote.USD
	}
	if pred.Metrics == nil || pred.Metrics.PredictTime == nil || *pred.Metrics.PredictTime < 0 || math.IsNaN(*pred.Metrics.PredictTime) {
		return nil
	}
	usd := nanoToCost(int64(math.Ceil(*pred.Metrics.PredictTime * replicateL40SNanoPerSec))).USD
	return &usd
}

// replicateOutputs accepts the two documented output shapes: an array of
// file URLs or a single URL.
func replicateOutputs(raw json.RawMessage) ([]string, bool) {
	var many []string
	if json.Unmarshal(raw, &many) == nil && len(many) > 0 {
		return many, true
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}, true
	}
	return nil, false
}

func replicateStatusError(status int, err error) error {
	switch status {
	case http.StatusPaymentRequired:
		return quotaAdapterError()
	case http.StatusNotFound:
		return &adapterError{code: CodeNoProvider, safe: "The provider key cannot use the configured model.", cause: errNoListedModels}
	case http.StatusUnprocessableEntity:
		return &adapterError{code: CodeBadInput, safe: "The image request is invalid.", cause: errInvalidScriptInput}
	}
	return err
}

// replicateCancel stops a prediction MangaVeyra no longer waits for, so it
// cannot keep running (and billing) unobserved. Best effort.
func (s *imageService) replicateCancel(ctx context.Context, key string, pred replicatePrediction) {
	if !strings.HasPrefix(pred.URLs.Cancel, replicateAPI+"/predictions/") || s.doer == nil {
		return
	}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(cancelCtx, http.MethodPost, pred.URLs.Cancel, nil)
	if err != nil {
		return
	}
	r.Header.Set("Authorization", "Bearer "+key)
	if resp, err := s.doer.Do(r); err == nil && resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
}

func replicateDeliveryHost(host string) bool {
	return host == "replicate.delivery" || strings.HasSuffix(host, ".replicate.delivery")
}

// deliveryBytes downloads one generated file from a provider's delivery CDN:
// HTTPS only, an allowed host, no Authorization header, bounded size.
func (s *imageService) deliveryBytes(ctx context.Context, raw string, allowed func(string) bool) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !allowed(u.Hostname()) || s.doer == nil {
		return nil, badOutputAdapterError(false)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, badOutputAdapterError(false)
	}
	resp, err := s.doer.Do(r)
	if err != nil {
		return nil, upstreamAdapterError(true)
	}
	if resp == nil || resp.Body == nil {
		return nil, badOutputAdapterError(false)
	}
	defer resp.Body.Close()
	data, large, err := readBounded(resp.Body, maxImageBytes)
	if err != nil || large || resp.StatusCode != http.StatusOK {
		return nil, badOutputAdapterError(false)
	}
	return data, nil
}

type replicateKeyTester struct{ doer httpDoer }

// TestKey reads the account the key belongs to; it starts no prediction.
func (t replicateKeyTester) TestKey(ctx context.Context, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", providerAdapterError(CodeProviderAuth, false, false)
	}
	data, _, status, err := getJSON(ctx, t.doer, replicateAPI+"/account", "Authorization", "Bearer "+key, 64<<10)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", replicateStatusError(status, classifyHTTPStatus(status))
	}
	var account struct {
		Username string `json:"username"`
	}
	if json.Unmarshal(data, &account) != nil || account.Username == "" {
		return "", upstreamAdapterError(false)
	}
	return "", nil
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
