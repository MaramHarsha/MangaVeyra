package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	xdraw "golang.org/x/image/draw"
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
	if provider.Kind == providerKindOpenRouter {
		call, err := s.openRouterImage(ctx, provider.Key, req)
		return closeReservation(call, err)
	}
	call, err := s.falImage(ctx, provider.Key, req)
	return closeReservation(call, err)
}

func (s *imageService) postImage(ctx context.Context, endpoint, authorization string, payload any) ([]byte, int, bool, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, false, badInputAdapterError()
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	r, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, 0, false, badInputAdapterError()
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", authorization)
	r.Header.Set("X-Fal-Store-IO", "0")
	r.Header.Set("x-app-fal-disable-fallback", "true")
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
	references := []map[string]any{}
	for _, r := range req.References {
		references = append(references, map[string]any{"type": "image_url", "image_url": map[string]string{"url": imageDataURL(r)}})
	}
	if req.Init != nil {
		references = append([]map[string]any{{"type": "image_url", "image_url": map[string]string{"url": imageDataURL(*req.Init)}}}, references...)
	}
	prompt := req.Prompt
	if req.Negative != "" {
		prompt += "\nAvoid: " + req.Negative
	}
	if req.Style != "" {
		prompt += "\nStyle: " + req.Style
	}
	if req.Mask != nil {
		references = append(references, map[string]any{"type": "image_url", "image_url": map[string]string{"url": imageDataURL(*req.Mask)}})
		prompt += "\nThe final reference is an edit mask: change only its white area in the first image; preserve its black area. Return the full image."
	}
	if len(references) > 14 {
		return out, badInputAdapterError()
	}
	resolution := "1K"
	if req.Width > 2048 || req.Height > 2048 {
		resolution = "4K"
	} else if req.Width > 1024 || req.Height > 1024 {
		resolution = "2K"
	}
	ratio := nearestAspect(req.Width, req.Height)
	payload := map[string]any{"model": seedreamModel, "prompt": prompt, "n": req.Count, "resolution": resolution, "aspect_ratio": ratio, "output_format": "png", "stream": false, "input_references": references, "provider": map[string]any{"allow_fallbacks": false}}
	body, status, sent, err := s.postImage(ctx, "https://openrouter.ai/api/v1/images", "Bearer "+key, payload)
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
	for _, item := range decoded.Data {
		data, err := base64.StdEncoding.DecodeString(item.B64)
		if err != nil {
			return out, badOutputAdapterError(false)
		}
		g, err := decodeGeneratedImage(data, req.Width, req.Height, 0)
		if err != nil {
			return out, err
		}
		out.Images = append(out.Images, g)
	}
	return out, nil
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
	body, status, sent, err := s.postImage(ctx, "https://fal.run/"+req.Plan.ModelID, "Key "+key, payload)
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
