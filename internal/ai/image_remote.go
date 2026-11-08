package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	xdraw "golang.org/x/image/draw"
)

// imageCallTimeout bounds one image request end to end; the client timeout
// is slightly longer so the per-call deadline decides.
const (
	imageCallTimeout = 3 * time.Minute
	imageHTTPTimeout = imageCallTimeout + 30*time.Second
)

type imageCall struct {
	Images    []GeneratedImage
	ActualUSD *float64
	Sent      bool
}

func imageDataURL(r ImageRef) string {
	return "data:" + r.ContentType + ";base64," + base64.StdEncoding.EncodeToString(r.Data)
}

func (s *imageService) remoteImage(ctx context.Context, req ImageGenerateRequest, m ModelManifest) (ImageResult, error) {
	var result ImageResult
	result.ProviderID = req.Plan.ProviderID
	result.ModelID = req.Plan.ModelID
	provider, err := s.providers.ResolveDispatch(ctx, req.ActorID, req.Plan.ProviderID)
	if err != nil || !hasProvider(m, provider.Kind) {
		return result, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	expectedKind := provider.Kind
	quote, ok := quoteImage(provider.Kind, req.Plan.ModelID, req.Plan.Needs)
	if !ok {
		return result, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	// A plan made before the connection's check (or an earlier job) showed
	// the key cannot use this GPT Image model is refused before reserving, so
	// a queued batch does not repeat a request OpenAI will reject.
	if provider.Kind == providerKindOpenAI {
		if c, err := s.providers.Candidate(ctx, req.ActorID, provider.ID); err == nil && !c.Test.imageModelAvailable(req.Plan.ModelID) {
			code := CodeNoProvider
			if c.Test.Images == capabilityVerification {
				code = CodeProviderVerification
			}
			return result, imageError(code, false, jobs.Cost{})
		}
	}
	// Route-specific request ceilings are checked before anything is reserved
	// or sent (ADR 0009: composed prompt bytes bound the text-token charge).
	if err := validateRouteRequest(provider.Kind, req); err != nil {
		return result, err
	}
	purpose, err := s.spend.initialSpendPurpose(ctx, req.JobID, req.Attempt)
	if err != nil {
		return result, imageError(CodeUpstream, false, jobs.Cost{})
	}
	if req.Attempt > 1 {
		var switched int
		if err := s.d.DB.Get(ctx, &switched, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND kind = 'fallback'`, req.JobID); err != nil {
			return result, imageError(CodeUpstream, false, jobs.Cost{})
		}
		if switched > 0 {
			purpose = SpendPurposeFailover
		}
	}
	reservation, err := s.spend.reserveRemoteCall(ctx, ReserveRemoteCallParams{ReservationID: db.NewID(), JobID: req.JobID, Attempt: req.Attempt, Purpose: purpose, ProviderID: provider.ID, ModelID: req.Plan.ModelID, EstimateUSD: quote.USD, ExpiresAt: s.spend.timeNow().Add(5 * time.Minute)})
	if err != nil {
		return result, safeImageAdapterError(mapSpendError(err), jobs.Cost{})
	}
	if !reservation.Dispatch {
		return result, imageError(CodeUpstream, false, quote)
	}
	closeReservation := func(call imageCall, callErr error) (ImageResult, error) {
		outcome := SpendOutcomeSuccess
		if callErr != nil {
			outcome = SpendOutcomeFailure
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			outcome = SpendOutcomeCanceled
		}
		actual := call.ActualUSD
		if !call.Sent {
			zero := 0.
			actual = &zero
		}
		closed, err := s.spend.reconcile(reservation.ID, outcome, actual)
		result.Actual = reservationCost(closed, reservation)
		if err != nil {
			return result, imageError(CodeUpstream, false, result.Actual)
		}
		if callErr != nil {
			return result, safeImageAdapterError(callErr, result.Actual)
		}
		result.Images = call.Images
		return result, nil
	}
	if _, err := s.dispatchGuard(ctx, req); err != nil {
		return closeReservation(imageCall{}, err)
	}
	provider, err = s.providers.ResolveDispatch(ctx, req.ActorID, req.Plan.ProviderID)
	if err != nil || provider.Kind != expectedKind || !hasProvider(m, provider.Kind) {
		return closeReservation(imageCall{}, imageError(CodeNoProvider, false, jobs.Cost{}))
	}
	switch provider.Kind {
	case providerKindOpenAI:
		call, err := s.openAIImage(ctx, provider.Key, req)
		var refused *adapterError
		if errors.As(err, &refused) && refused.code == CodeProviderVerification {
			// Remember the refusal for this key so routing and G2 reflect it
			// until the next connection test. The job itself stays terminal.
			if markErr := s.providers.markImagesNeedVerification(context.WithoutCancel(ctx), provider.ID, provider.KeyEnc); markErr != nil && s.d.Log != nil {
				s.d.Log.WarnContext(ctx, "record OpenAI image verification refusal", "err", markErr)
			}
		}
		return closeReservation(call, err)
	case providerKindOpenRouter:
		call, err := s.openRouterImage(ctx, provider.Key, req)
		return closeReservation(call, err)
	}
	call, err := s.falImage(ctx, provider.Key, req)
	return closeReservation(call, err)
}

// validateRouteRequest applies the ADR 0009 per-call ceilings. ADR 0007
// routes keep their own checks inside their adapters.
func validateRouteRequest(kind string, req ImageGenerateRequest) error {
	route, ok := imageRouteFor(kind, req.Plan.ModelID)
	if !ok {
		return nil
	}
	return validateGPTImageRequest(req, kind == providerKindOpenAI, route.MaxRefs)
}

var falHeaders = map[string]string{"X-Fal-Store-IO": "0", "x-app-fal-disable-fallback": "true"}

func (s *imageService) postImage(ctx context.Context, endpoint, authorization string, payload any, headers map[string]string) ([]byte, int, bool, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, false, badInputAdapterError()
	}
	callCtx, cancel := context.WithTimeout(ctx, imageCallTimeout)
	defer cancel()
	r, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, 0, false, badInputAdapterError()
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", authorization)
	r.Header.Set("Accept", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if s.doer == nil {
		return nil, 0, false, upstreamAdapterError(false)
	}
	resp, err := s.doer.Do(r)
	if err != nil {
		return nil, 0, true, classifyTransportError(err)
	}
	if resp == nil || resp.Body == nil {
		return nil, 0, true, upstreamAdapterError(false)
	}
	defer resp.Body.Close()
	body, large, err := readBounded(resp.Body, 128<<20)
	if err != nil || large {
		return nil, resp.StatusCode, true, badOutputAdapterError(false)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.StatusCode, true, classifyHTTPStatus(resp.StatusCode)
	}
	return body, resp.StatusCode, true, nil
}

func (s *imageService) openRouterImage(ctx context.Context, key string, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	payload, err := openRouterImagePayload(req)
	if err != nil {
		return out, err
	}
	body, status, sent, err := s.postImage(ctx, "https://openrouter.ai/api/v1/images", "Bearer "+key, payload, nil)
	out.Sent = sent
	if err != nil {
		if status >= 400 {
			zero := 0.
			out.ActualUSD = &zero
		}
		return out, err
	}
	var decoded struct {
		Data []struct {
			B64   string `json:"b64_json"`
			Media string `json:"media_type"`
		} `json:"data"`
		Usage struct {
			Cost json.RawMessage `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return out, badOutputAdapterError(false)
	}
	out.ActualUSD = parseUsageCost(decoded.Usage.Cost)
	if len(decoded.Data) != req.Count {
		return out, badOutputAdapterError(false)
	}
	// Seedream keeps ADR 0007's stretch fit. ADR 0009 routes centre-crop a
	// generated image and stretch a reference-guided edit back to the init.
	_, route := imageRouteFor(providerKindOpenRouter, req.Plan.ModelID)
	for _, item := range decoded.Data {
		data, err := decodeBase64Image(item.B64)
		if err != nil {
			return out, badOutputAdapterError(false)
		}
		g, err := fitGeneratedImage(data, req.Width, req.Height, 0, route && req.Init == nil)
		if err != nil {
			return out, err
		}
		out.Images = append(out.Images, g)
	}
	return out, nil
}

func openRouterReference(data string) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]string{"url": data}}
}

// openRouterImagePayload builds the documented Image API body for one frozen
// OpenRouter route. Seedream's body is unchanged from ADR 0007.
func openRouterImagePayload(req ImageGenerateRequest) (map[string]any, error) {
	if req.Plan.ModelID == seedreamModel {
		return seedreamPayload(req)
	}
	route, ok := imageRouteFor(providerKindOpenRouter, req.Plan.ModelID)
	if !ok {
		return nil, badInputAdapterError()
	}
	refs := routeReferences(req.References, route.MaxRefs)
	references := []map[string]any{}
	// Every input is re-encoded and bounded: references to gptImageRefEdge,
	// the init and mask together to gptImageEditEdge so they stay aligned.
	add := func(data []byte, edge int) error {
		scaled, err := downscaleReference(data, edge)
		if err != nil {
			return badInputAdapterError()
		}
		references = append(references, openRouterReference(pngDataURL(scaled)))
		return nil
	}
	if req.Init != nil {
		if err := add(req.Init.Data, gptImageEditEdge); err != nil {
			return nil, err
		}
	}
	for _, r := range refs {
		data, err := r.encode(gptImageRefEdge)
		if err != nil {
			return nil, badInputAdapterError()
		}
		references = append(references, openRouterReference(pngDataURL(data)))
	}
	if req.Mask != nil {
		if err := add(req.Mask.Data, gptImageEditEdge); err != nil {
			return nil, err
		}
	}
	prompt := gptImagePrompt(req, refs, false)
	if len(prompt) > imagePromptBytes {
		return nil, badInputAdapterError()
	}
	payload := map[string]any{"model": route.Model, "prompt": prompt, "n": req.Count, "stream": false, "input_references": references, "provider": map[string]any{"allow_fallbacks": false}}
	if route.Model == openRouterGPTImageSunburst {
		payload["aspect_ratio"] = nearestAspectFrom(req.Width, req.Height, gptImageAspects)
		payload["quality"] = gptImageQuality
		payload["background"] = gptImageBackground(req.Background)
		payload["output_format"] = "png"
	} else {
		payload["aspect_ratio"] = nearestAspectFrom(req.Width, req.Height, nanoBananaAspects)
		payload["resolution"] = "1K"
	}
	return payload, nil
}

func seedreamPayload(req ImageGenerateRequest) (map[string]any, error) {
	references := []map[string]any{}
	for _, r := range req.References {
		references = append(references, openRouterReference(imageDataURL(r)))
	}
	if req.Init != nil {
		references = append([]map[string]any{openRouterReference(imageDataURL(*req.Init))}, references...)
	}
	prompt := req.Prompt
	if req.Negative != "" {
		prompt += "\nAvoid: " + req.Negative
	}
	if req.Style != "" {
		prompt += "\nStyle: " + req.Style
	}
	// Seedream receives every reference; name the pose guide by its position
	// so it controls pose only (the adapter describes what it sends).
	for i, r := range req.References {
		if r.Role == "pose" {
			position := i + 1
			if req.Init != nil {
				position++
			}
			prompt += fmt.Sprintf("\nReference image %d is a pose skeleton: follow it for body pose only; the character reference images define identity and outfit.", position)
			break
		}
	}
	if req.Mask != nil {
		references = append(references, openRouterReference(imageDataURL(*req.Mask)))
		prompt += "\nThe final reference is an edit mask: change only its white area in the first image; preserve its black area. Return the full image."
	}
	if len(references) > 14 {
		return nil, badInputAdapterError()
	}
	resolution := "1K"
	if req.Width > 2048 || req.Height > 2048 {
		resolution = "4K"
	} else if req.Width > 1024 || req.Height > 1024 {
		resolution = "2K"
	}
	ratio := nearestAspect(req.Width, req.Height)
	return map[string]any{"model": seedreamModel, "prompt": prompt, "n": req.Count, "resolution": resolution, "aspect_ratio": ratio, "output_format": "png", "stream": false, "input_references": references, "provider": map[string]any{"allow_fallbacks": false}}, nil
}

// Aspect ratios each OpenRouter route documents (GET /api/v1/images/models).
var (
	gptImageAspects   = []string{"1:1", "3:2", "2:3", "4:3", "3:4", "16:9", "9:16", "21:9"}
	nanoBananaAspects = []string{"1:1", "1:4", "1:8", "2:3", "3:2", "3:4", "4:1", "4:3", "4:5", "5:4", "8:1", "9:16", "16:9", "21:9"}
)

func nearestAspectFrom(w, h int, ratios []string) string {
	chosen, best := ratios[0], math.Inf(1)
	target := math.Log(float64(w) / float64(h))
	for _, r := range ratios {
		var a, b float64
		if _, err := fmt.Sscanf(r, "%g:%g", &a, &b); err != nil || a <= 0 || b <= 0 {
			continue
		}
		if d := math.Abs(math.Log(a/b) - target); d < best-1e-12 {
			chosen, best = r, d
		}
	}
	return chosen
}

func nearestAspect(w, h int) string {
	ratios := []struct {
		n    string
		a, b int
	}{{"1:1", 1, 1}, {"4:3", 4, 3}, {"3:4", 3, 4}, {"16:9", 16, 9}, {"9:16", 9, 16}, {"2:3", 2, 3}, {"3:2", 3, 2}}
	chosen := "1:1"
	best := 1e100
	for _, r := range ratios {
		d := float64(w)/float64(h) - float64(r.a)/float64(r.b)
		if d*d < best {
			best = d * d
			chosen = r.n
		}
	}
	return chosen
}

func (s *imageService) falImage(ctx context.Context, key string, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	payload := map[string]any{"prompt": req.Prompt, "image_size": map[string]int{"width": req.Width, "height": req.Height}, "num_images": req.Count, "seed": req.Seed, "output_format": "png", "sync_mode": true, "enable_safety_checker": true}
	if req.Plan.ModelID == falSchnellModel {
		payload["num_inference_steps"] = 4
	} else {
		if req.Init == nil || req.Mask == nil {
			return out, badInputAdapterError()
		}
		payload["image_url"] = imageDataURL(*req.Init)
		payload["mask_url"] = imageDataURL(*req.Mask)
		payload["num_inference_steps"] = 28
		payload["loras"] = []any{}
	}
	body, status, sent, err := s.postImage(ctx, "https://fal.run/"+req.Plan.ModelID, "Key "+key, payload, falHeaders)
	out.Sent = sent
	if err != nil {
		if status >= 500 || status == 400 || status == 401 || status == 403 || status == 422 || status == 429 {
			zero := 0.
			out.ActualUSD = &zero
		}
		return out, err
	}
	var decoded struct {
		Images []struct {
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
		} `json:"images"`
		Seed   int64  `json:"seed"`
		Safety []bool `json:"has_nsfw_concepts"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return out, badOutputAdapterError(false)
	}
	quote, _ := quoteImage(providerKindFal, req.Plan.ModelID, req.Plan.Needs)
	out.ActualUSD = &quote.USD
	for _, unsafe := range decoded.Safety {
		if unsafe {
			return out, providerAdapterError(CodeProviderRefuse, false, false)
		}
	}
	if len(decoded.Images) != req.Count {
		return out, badOutputAdapterError(false)
	}
	for _, item := range decoded.Images {
		data, err := s.falImageBytes(ctx, item.URL)
		if err != nil {
			return out, err
		}
		g, err := decodeGeneratedImage(data, req.Width, req.Height, decoded.Seed)
		if err != nil {
			return out, err
		}
		out.Images = append(out.Images, g)
	}
	return out, nil
}

func (s *imageService) falImageBytes(ctx context.Context, raw string) ([]byte, error) {
	if strings.HasPrefix(raw, "data:image/") {
		parts := strings.SplitN(raw, ",", 2)
		if len(parts) != 2 || !strings.HasSuffix(parts[0], ";base64") || len(parts[1]) > (maxImageBytes*4/3)+4 {
			return nil, badOutputAdapterError(false)
		}
		data, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, badOutputAdapterError(false)
		}
		return data, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !(u.Hostname() == "fal.media" || strings.HasSuffix(u.Hostname(), ".fal.media")) {
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
	if err != nil || large || resp.StatusCode != 200 {
		return nil, badOutputAdapterError(false)
	}
	return data, nil
}

func decodeGeneratedImage(data []byte, w, h int, seed int64) (GeneratedImage, error) {
	var zero GeneratedImage
	if len(data) > maxImageBytes {
		return zero, badOutputAdapterError(false)
	}
	ct := storage.DetectContentType(data)
	if ct != "image/png" && ct != "image/jpeg" && ct != "image/webp" {
		return zero, badOutputAdapterError(false)
	}
	im, _, err := storage.DecodeImage(data, storage.ImageLimits{MaxWidth: 4096, MaxHeight: 4096, MaxPixels: 16 << 20})
	if err != nil {
		return zero, badOutputAdapterError(false)
	}
	if im.Bounds().Dx() != w || im.Bounds().Dy() != h {
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), im, im.Bounds(), xdraw.Src, nil)
		var b bytes.Buffer
		if png.Encode(&b, dst) != nil {
			return zero, badOutputAdapterError(false)
		}
		data = b.Bytes()
		ct = "image/png"
	}
	return GeneratedImage{Data: data, ContentType: ct, Width: w, Height: h, Seed: seed}, nil
}
