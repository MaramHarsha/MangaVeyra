package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// ADR 0009 tests. Every adapter runs against queued fake transports; nothing
// here contacts a provider.

func createOpenAIImageProvider(t *testing.T, m *Module, f *aiDBFixture, key string) providerView {
	t.Helper()
	v, err := m.providers.Create(context.Background(), providerActor{UserID: f.userID, Admin: true}, providerCreate{Kind: providerKindOpenAI, Name: "OpenAI images", Key: key, Scope: providerScopeServer})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func gptImageOK(images [][]byte, usage any) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		data := []any{}
		for _, im := range images {
			data = append(data, map[string]string{"b64_json": base64.StdEncoding.EncodeToString(im)})
		}
		body := map[string]any{"created": 1, "data": data}
		if usage != nil {
			body["usage"] = usage
		}
		return httpJSON(http.StatusOK, body, nil), nil
	}
}

func decodeDataURLImage(t *testing.T, raw string) image.Image {
	t.Helper()
	prefix := "data:image/png;base64,"
	if !strings.HasPrefix(raw, prefix) {
		t.Fatalf("not a PNG data URL: %.40s", raw)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, prefix))
	if err != nil {
		t.Fatal(err)
	}
	im, _, err := storage.DecodeImage(data, storage.ImageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func planWith(t *testing.T, m *Module, f *aiDBFixture, req ImagePlanRequest) ImagePlan {
	t.Helper()
	req.ActorID, req.ProjectID = f.userID, f.projectID
	p, err := m.image.PlanImage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGPTImageOutputTokensMatchPublishedCalculator(t *testing.T) {
	for _, tc := range []struct {
		w, h    int
		quality string
		want    int64
	}{
		{1024, 1024, "low", 196}, {1024, 1024, "medium", 439}, {1024, 1024, "high", 1756},
		{1024, 1024, "xhigh", 3122}, {1024, 1024, "max", 7024}, {1536, 1024, "high", 1372},
		{1024, 1536, "high", 1372}, {1536, 864, "high", 1078}, {2880, 2880, "high", 5930},
	} {
		if got := gptImageOutputTokens(tc.w, tc.h, tc.quality); got != tc.want {
			t.Errorf("tokens(%dx%d %s) = %d, want %d", tc.w, tc.h, tc.quality, got, tc.want)
		}
	}
	if gptImageOutputTokens(1024, 1024, "ultra") != 0 {
		t.Fatal("unknown quality priced")
	}
}

func TestGPTImageSizesAreValidAndNearest(t *testing.T) {
	for _, s := range gptImageSizes {
		px := s.W * s.H
		ratio := math.Max(float64(s.W), float64(s.H)) / math.Min(float64(s.W), float64(s.H))
		if s.W%16 != 0 || s.H%16 != 0 || px < 655_360 || px > 8_294_400 || ratio > 3 || s.W > 3840 || s.H > 3840 {
			t.Errorf("size %dx%d violates the Image API rules", s.W, s.H)
		}
	}
	for _, tc := range []struct{ w, h, ww, wh int }{
		{1024, 1024, 1024, 1024}, {1024, 683, 1536, 1024}, {683, 1024, 1024, 1536}, {1024, 576, 1536, 864},
		{1000, 800, 1120, 896}, {1024, 256, 1536, 512}, {300, 1024, 512, 1536}, {1024, 410, 1536, 640},
	} {
		if got := gptImageSizeFor(tc.w, tc.h); got.W != tc.ww || got.H != tc.wh {
			t.Errorf("size for %dx%d = %dx%d, want %dx%d", tc.w, tc.h, got.W, got.H, tc.ww, tc.wh)
		}
	}
}

func TestImageRouteQuotesAreExactUpperBounds(t *testing.T) {
	for _, tc := range []struct {
		kind, model string
		n           ImageNeeds
		want        float64
	}{
		// 1,756 output tokens × $30/M + 6,144 prompt bytes × $5/M.
		{providerKindOpenAI, gptImageSunburst, ImageNeeds{Width: 1024, Height: 1024, Count: 1}, 0.0834},
		{providerKindOpenAI, gptImageFlare, ImageNeeds{Width: 1024, Height: 1024, Count: 4}, 0.24144},
		// References are capped at two per call; init and mask add two inputs.
		{providerKindOpenAI, gptImageSunburst, ImageNeeds{Width: 1024, Height: 1024, Count: 1, References: 5, Edit: true, Mask: true}, 0.301},
		// 1536×1024: 1,372 output tokens.
		{providerKindOpenAI, gptImageSunburst, ImageNeeds{Width: 1024, Height: 683, Count: 1}, 0.07188},
		// OpenRouter GPT Image bounds the largest pixel budget: 5,930 tokens.
		{providerKindOpenRouter, openRouterGPTImageSunburst, ImageNeeds{Width: 1024, Height: 1024, Count: 1}, 0.20862},
		// 1,120 × $30/M + 4,096 × $7.50/M + 6,144 × $1.50/M.
		{providerKindOpenRouter, openRouterNanoBanana21, ImageNeeds{Width: 1024, Height: 1024, Count: 1}, 0.073536},
		// 1,120 × $120/M + 4,096 × $12/M + 6,144 × $2/M + 1 reference × 560 × $2/M.
		{providerKindOpenRouter, openRouterNanoBananaPro, ImageNeeds{Width: 1024, Height: 1024, Count: 1, References: 1}, 0.19696},
	} {
		got, ok := quoteImage(tc.kind, tc.model, tc.n)
		if !ok || math.Abs(got.USD-tc.want) > 1e-9 {
			t.Errorf("quote %s %s %+v = %v %v, want %v", tc.kind, tc.model, tc.n, got.USD, ok, tc.want)
		}
	}
	for _, bad := range []struct {
		kind, model string
		n           ImageNeeds
	}{
		{providerKindOpenAI, gptImageSunburst, ImageNeeds{Width: 1024, Height: 1024, Count: 5}},
		{providerKindOpenRouter, openRouterNanoBanana21, ImageNeeds{Width: 1024, Height: 1024, Count: 2}},
		{providerKindOpenAI, seedreamModel, ImageNeeds{Width: 1024, Height: 1024, Count: 1}},
		{providerKindOpenRouter, gptImageSunburst, ImageNeeds{Width: 1024, Height: 1024, Count: 1}},
	} {
		if _, ok := quoteImage(bad.kind, bad.model, bad.n); ok {
			t.Errorf("unbounded route accepted: %+v", bad)
		}
	}
}

func TestGPTImageUsageCostIsExactAndConservative(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	split := &gptImageUsage{InputTokens: i(1000), OutputTokens: i(1756)}
	split.InputDetails = &struct {
		ImageTokens *int64 `json:"image_tokens"`
		TextTokens  *int64 `json:"text_tokens"`
	}{ImageTokens: i(800), TextTokens: i(200)}
	if got := gptImageUsageCost(split); got == nil || math.Abs(*got-0.06008) > 1e-12 {
		t.Fatalf("split usage = %v", got)
	}
	// Without a split, every input token is priced at the image rate.
	if got := gptImageUsageCost(&gptImageUsage{InputTokens: i(1000), OutputTokens: i(1756)}); got == nil || math.Abs(*got-0.06068) > 1e-12 {
		t.Fatalf("unsplit usage = %v", got)
	}
	if gptImageUsageCost(nil) != nil || gptImageUsageCost(&gptImageUsage{InputTokens: i(-1), OutputTokens: i(1)}) != nil {
		t.Fatal("missing or invalid usage must charge the estimate")
	}
}

func TestOpenAIImageGenerationRequestAndAccounting(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		out := imageTestPNG(t, 1536, 1024, color.NRGBA{30, 30, 30, 255})
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
			gptImageOK([][]byte{out, out}, map[string]any{"input_tokens": 300, "output_tokens": 2744, "input_tokens_details": map[string]any{"text_tokens": 300, "image_tokens": 0}}),
		}}
		var logs bytes.Buffer
		m, f := imageTestModule(t, d, fake)
		m.d.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		provider := createOpenAIImageProvider(t, m, f, "sk-image-secret-1234")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 1024, Height: 683, Count: 2}})
		if p.ProviderID != provider.ID || p.ModelID != gptImageSunburst || !p.Remote {
			t.Fatalf("Auto did not pick the OpenAI route: %+v", p)
		}
		var decision imageDecision
		if err := json.Unmarshal(p.Decision, &decision); err != nil || decision.Route != "OpenAI · GPT Image 2.5 Sunburst" || decision.EstimateUSD != p.Estimate.USD || !strings.Contains(decision.Reason, "OpenAI") {
			t.Fatalf("decision = %s", p.Decision)
		}
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		req.Negative = "speech bubbles"
		result, err := m.image.GenerateImage(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Images) != 2 || result.Images[0].Width != 1024 || result.Images[0].Height != 683 {
			t.Fatalf("result = %+v", result.Images)
		}
		seen := fake.requests()
		if len(seen) != 1 || seen[0].URL != openAIImageGenerateURL || seen[0].Authorization != "Bearer sk-image-secret-1234" || seen[0].ContentType != "application/json" {
			t.Fatalf("dispatch = %+v", seen)
		}
		var body map[string]any
		if err := json.Unmarshal(seen[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != gptImageSunburst || body["n"] != float64(2) || body["size"] != "1536x1024" || body["quality"] != "high" || body["background"] != "opaque" || body["output_format"] != "png" || body["moderation"] != "auto" || body["images"] != nil || body["mask"] != nil {
			t.Fatalf("body = %v", body)
		}
		if !strings.Contains(body["prompt"].(string), "Avoid: speech bubbles") {
			t.Fatalf("prompt lacks the avoid list: %q", body["prompt"])
		}
		// 300 × $5/M + 2,744 × $30/M = $0.08382; reserved at the per-call quote.
		var row struct {
			Estimate int64  `db:"estimate_microusd"`
			Actual   int64  `db:"actual_microusd"`
			State    string `db:"state"`
		}
		if err := d.Get(ctx, &row, `SELECT estimate_microusd, actual_microusd, state FROM spend_reservations WHERE job_id = ?`, id); err != nil {
			t.Fatal(err)
		}
		quote, _ := quoteImage(providerKindOpenAI, gptImageSunburst, p.Needs)
		if row.State != "reconciled" || row.Estimate != int64(math.Round(quote.USD*1e6)) || row.Actual != 83_820 || math.Abs(result.Actual.USD-0.08382) > 1e-9 {
			t.Fatalf("reservation = %+v result = %+v", row, result.Actual)
		}
		if strings.Contains(logs.String(), "sk-image-secret") {
			t.Fatal("decrypted key logged")
		}
	})
}

func TestOpenAIImageCharacterLayerIsTransparentWithReferences(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		clear := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
		clear.SetNRGBA(512, 512, color.NRGBA{0, 0, 0, 255})
		var b bytes.Buffer
		_ = png.Encode(&b, clear)
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){gptImageOK([][]byte{b.Bytes()}, nil)}}
		m, f := imageTestModule(t, d, fake)
		createOpenAIImageProvider(t, m, f, "sk-layer")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1, References: 3}})
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		req.Background = "transparent"
		big := imageTestPNG(t, 1500, 1500, color.NRGBA{200, 200, 200, 255})
		req.References = []ImageRef{{Data: big, ContentType: "image/png", Role: "reference"}, {Data: big, ContentType: "image/png", Role: "reference"}, {Data: big, ContentType: "image/png", Role: "reference"}}
		result, err := m.image.GenerateImage(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		im, _, err := storage.DecodeImage(result.Images[0].Data, storage.ImageLimits{})
		if err != nil || im.Bounds().Dx() != 512 {
			t.Fatalf("decode %v", err)
		}
		if _, _, _, a := im.At(0, 0).RGBA(); a != 0 {
			t.Fatal("transparent background was flattened")
		}
		seen := fake.requests()
		var body struct {
			Background string `json:"background"`
			Prompt     string `json:"prompt"`
			Images     []struct {
				ImageURL string `json:"image_url"`
			} `json:"images"`
			Mask any `json:"mask"`
		}
		if err := json.Unmarshal(seen[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		if seen[0].URL != openAIImageEditURL || body.Background != "transparent" || len(body.Images) != gptImageMaxRefs || body.Mask != nil {
			t.Fatalf("reference request = %s %+v", seen[0].URL, body)
		}
		if ref := decodeDataURLImage(t, body.Images[0].ImageURL); ref.Bounds().Dx() != 1024 || ref.Bounds().Dy() != 1024 {
			t.Fatalf("reference not downscaled: %v", ref.Bounds())
		}
		if !strings.Contains(body.Prompt, "reference sheets") || !strings.Contains(body.Prompt, "transparent") {
			t.Fatalf("prompt = %q", body.Prompt)
		}
	})
}

func TestOpenAIImageMaskedEditLetterboxesAndRestoresOutsidePixels(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		// 250×100 maps to the 1536×640 size and letterboxes 13 px top and bottom.
		w, h := 250, 100
		initIm := image.NewNRGBA(image.Rect(0, 0, w, h))
		maskIm := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				initIm.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 90, 255})
				maskIm.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
				if x >= 100 && x < 150 {
					maskIm.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
				}
			}
		}
		var ib, mb bytes.Buffer
		_ = png.Encode(&ib, initIm)
		_ = png.Encode(&mb, maskIm)
		edited := imageTestPNG(t, 1536, 640, color.NRGBA{250, 0, 0, 255})
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){gptImageOK([][]byte{edited}, nil)}}
		m, f := imageTestModule(t, d, fake)
		createOpenAIImageProvider(t, m, f, "sk-edit")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskRegionEdit, Needs: ImageNeeds{Width: w, Height: h, Count: 1, Edit: true, Mask: true}})
		if p.ModelID != gptImageSunburst {
			t.Fatalf("plan = %+v", p)
		}
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		req.Init = &ImageRef{Data: ib.Bytes(), ContentType: "image/png", Role: "init"}
		req.Mask = &ImageRef{Data: mb.Bytes(), ContentType: "image/png", Role: "mask"}
		result, err := m.image.GenerateImage(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		out, _, _ := storage.DecodeImage(result.Images[0].Data, storage.ImageLimits{})
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				got := color.NRGBAModel.Convert(out.At(x, y)).(color.NRGBA)
				if x < 100 || x >= 150 {
					if got != initIm.NRGBAAt(x, y) {
						t.Fatalf("outside-mask pixel changed at %d,%d", x, y)
					}
				} else if got.R < 240 {
					t.Fatalf("masked pixel not edited at %d,%d: %v", x, y, got)
				}
			}
		}
		var body struct {
			Size   string `json:"size"`
			Images []struct {
				ImageURL string `json:"image_url"`
			} `json:"images"`
			Mask struct {
				ImageURL string `json:"image_url"`
			} `json:"mask"`
		}
		seen := fake.requests()
		if err := json.Unmarshal(seen[0].Body, &body); err != nil || seen[0].URL != openAIImageEditURL || body.Size != "1536x640" || len(body.Images) != 1 {
			t.Fatalf("edit request = %s %+v %v", seen[0].URL, body.Size, err)
		}
		sentInit := decodeDataURLImage(t, body.Images[0].ImageURL)
		sentMask := decodeDataURLImage(t, body.Mask.ImageURL)
		if sentInit.Bounds() != image.Rect(0, 0, 1536, 640) || sentMask.Bounds() != sentInit.Bounds() {
			t.Fatalf("init/mask sizes %v %v", sentInit.Bounds(), sentMask.Bounds())
		}
		alpha := func(x, y int) uint8 { return color.NRGBAModel.Convert(sentMask.At(x, y)).(color.NRGBA).A }
		// Letterbox padding and unmasked content are protected; the white
		// region becomes fully transparent (the Image API's edit area).
		if alpha(768, 2) != 255 || alpha(10, 320) != 255 || alpha(768, 320) != 0 {
			t.Fatalf("mask alpha padding=%d keep=%d edit=%d", alpha(768, 2), alpha(10, 320), alpha(768, 320))
		}
	})
}

func TestOpenAIImageErrorsMapAndAccount(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		png1024 := imageTestPNG(t, 1024, 1024, color.NRGBA{1, 2, 3, 255})
		var jpg bytes.Buffer
		_ = jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 1024, 1024)), nil)
		errBody := func(status int, code, typ, stage string) func(*http.Request) (*http.Response, error) {
			return func(*http.Request) (*http.Response, error) {
				e := map[string]any{"code": code, "type": typ, "message": "provider text that must never surface"}
				if stage != "" {
					e["moderation_details"] = map[string]any{"moderation_stage": stage, "categories": []string{"violence"}}
				}
				return httpJSON(status, map[string]any{"error": e}, nil), nil
			}
		}
		for _, tc := range []struct {
			name      string
			respond   func(*http.Request) (*http.Response, error)
			code      string
			retryable bool
			charged   bool // keeps the reservation (unknown or consumed)
		}{
			{"auth", errBody(401, "invalid_api_key", "invalid_request_error", ""), CodeProviderAuth, false, false},
			{"org verification", errBody(403, "", "invalid_request_error", ""), CodeProviderAuth, false, false},
			{"rate limit", errBody(429, "rate_limit_exceeded", "requests", ""), CodeRateLimited, true, false},
			{"quota", errBody(429, "insufficient_quota", "insufficient_quota", ""), CodeProviderAuth, false, false},
			{"model access", errBody(404, "model_not_found", "invalid_request_error", ""), CodeNoProvider, false, false},
			{"input moderation", errBody(400, "moderation_blocked", "image_generation_user_error", "input"), CodeProviderRefuse, false, false},
			{"output moderation", errBody(400, "moderation_blocked", "image_generation_user_error", "output"), CodeProviderRefuse, false, true},
			{"user error", errBody(400, "invalid_image", "image_generation_user_error", ""), CodeBadInput, false, false},
			{"server", errBody(500, "server_error", "server_error", ""), CodeUpstream, true, false},
			{"timeout", func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded }, CodeTimeout, true, true},
			{"bad base64", func(*http.Request) (*http.Response, error) {
				return httpJSON(200, map[string]any{"data": []any{map[string]string{"b64_json": "%%%"}}}, nil), nil
			}, CodeBadOutput, false, true},
			{"wrong count", gptImageOK([][]byte{png1024, png1024}, nil), CodeBadOutput, false, true},
			{"not png", gptImageOK([][]byte{jpg.Bytes()}, nil), CodeBadOutput, false, true},
			{"url only", func(*http.Request) (*http.Response, error) {
				return httpJSON(200, map[string]any{"data": []any{map[string]string{"url": "https://example.invalid/a.png"}}}, nil), nil
			}, CodeBadOutput, false, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){tc.respond}}
				m, f := imageTestModule(t, d, fake)
				createOpenAIImageProvider(t, m, f, "sk-errors-secret")
				p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1}})
				id := imageTestJob(t, m, f, p)
				_, err := m.image.GenerateImage(ctx, imageTestRequest(f, p, id))
				g := requireGenerateCode(t, err, tc.code)
				if g.Retryable != tc.retryable {
					t.Fatalf("retryable = %v", g.Retryable)
				}
				if strings.Contains(err.Error(), "never surface") || strings.Contains(err.Error(), "sk-errors") {
					t.Fatalf("unsafe error text: %v", err)
				}
				var actual int64
				if err := d.Get(ctx, &actual, `SELECT actual_microusd FROM spend_reservations WHERE job_id = ?`, id); err != nil {
					t.Fatal(err)
				}
				quote, _ := quoteImage(providerKindOpenAI, gptImageSunburst, p.Needs)
				want := int64(0)
				if tc.charged {
					want = int64(math.Round(quote.USD * 1e6))
				}
				if actual != want {
					t.Fatalf("actual = %d, want %d", actual, want)
				}
			})
		}
	})
}

func TestOpenAIImagePolicyBlocksSendNothing(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake := &queuedDoer{}
		m, f := imageTestModule(t, d, fake)
		createOpenAIImageProvider(t, m, f, "sk-policy")
		for _, mode := range []string{ModeLocalOnly, ModeFreeOnly} {
			p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, RoutingMode: mode, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1}})
			if p.ProviderID != PlaceholderID || p.Remote || p.Estimate.USD != 0 {
				t.Fatalf("%s picked a paid route: %+v", mode, p)
			}
		}
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1}})
		check := func(name string, req ImageGenerateRequest, code string) {
			t.Helper()
			_, err := m.image.GenerateImage(ctx, req)
			requireGenerateCode(t, err, code)
			if fake.count() != 0 {
				t.Fatalf("%s dispatched", name)
			}
		}
		// A prompt over the route's byte bound is refused before reserving.
		id := imageTestJob(t, m, f, p)
		long := imageTestRequest(f, p, id)
		long.Prompt = strings.Repeat("ink ", imagePromptBytes/4+1)
		check("oversized prompt", long, CodeBadInput)
		// A stale attempt is fenced before reserving.
		stale := imageTestRequest(f, p, id)
		stale.Attempt = 2
		check("stale attempt", stale, CodeBadInput)
		// The monthly cap is enforced before the call.
		updateInstance(t, f.settings, func(i *settings.Instance) { i.Limits.ServerMonthlyRemoteSpendUSD = p.Estimate.USD - 0.000001 })
		check("spend limit", imageTestRequest(f, p, id), CodeSpendLimit)
		updateInstance(t, f.settings, func(i *settings.Instance) { i.Limits.ServerMonthlyRemoteSpendUSD = 0 })
		// Local-only switched on after planning blocks the stored remote plan.
		updateInstance(t, f.settings, func(i *settings.Instance) { i.LocalOnly = true })
		check("local only", imageTestRequest(f, p, id), CodeLocalOnly)
		var reserved int
		if err := d.Get(ctx, &reserved, `SELECT COUNT(*) FROM spend_reservations WHERE job_id = ?`, id); err != nil || reserved != 0 {
			t.Fatalf("blocked calls reserved %d %v", reserved, err)
		}
	})
}

func TestImageRoutingModesPreferOpenAIAndPinOnlyBackups(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		openai := createOpenAIImageProvider(t, m, f, "sk-routing")
		or := f.createProvider(t, m.providers, providerScopeServer, "or-routing")
		for _, tc := range []struct {
			task  string
			mode  string
			needs ImageNeeds
			want  string
		}{
			{TaskPanel, ModeBalanced, ImageNeeds{Width: 1024, Height: 683, Count: 1}, gptImageSunburst},
			{TaskPanel, ModeBest, ImageNeeds{Width: 1024, Height: 683, Count: 1}, gptImageSunburst},
			{TaskPanel, ModeFastest, ImageNeeds{Width: 1024, Height: 683, Count: 1}, gptImageFlare},
			{TaskCharacterSheet, ModeBalanced, ImageNeeds{Width: 1024, Height: 1024, Count: 4, References: 2}, gptImageSunburst},
			{TaskCover, ModeBalanced, ImageNeeds{Width: 1024, Height: 1536, Count: 1}, gptImageSunburst},
			{TaskRegionEdit, ModeBalanced, ImageNeeds{Width: 800, Height: 600, Count: 1, Edit: true, Mask: true}, gptImageSunburst},
		} {
			p := planWith(t, m, f, ImagePlanRequest{Task: tc.task, RoutingMode: tc.mode, Needs: tc.needs})
			if p.ProviderID != openai.ID || p.ModelID != tc.want {
				t.Fatalf("%s %s = %s/%s", tc.task, tc.mode, p.ProviderID, p.ModelID)
			}
			var dec imageDecision
			_ = json.Unmarshal(p.Decision, &dec)
			if len(dec.Fallbacks) != 3 || !strings.HasPrefix(dec.Fallbacks[0], "openai:") || dec.Fallbacks[1] != "openrouter:"+seedreamModel || dec.Fallbacks[2] != "placeholder:"+PlaceholderArt {
				t.Fatalf("fallback chain = %v", dec.Fallbacks)
			}
			// The estimate covers the most expensive route of the chain only.
			own, _ := quoteImage(providerKindOpenAI, tc.want, p.Needs)
			seed, _ := quoteImage(providerKindOpenRouter, seedreamModel, p.Needs)
			if p.Estimate.USD != math.Max(own.USD, seed.USD) {
				t.Fatalf("estimate %v, want max(%v, %v)", p.Estimate.USD, own.USD, seed.USD)
			}
			for _, pinOnly := range []string{openRouterGPTImageSunburst, openRouterNanoBanana21, openRouterNanoBananaPro} {
				if !strings.Contains(string(p.Decision), pinOnly+": pin only") && tc.needs.Count == 1 {
					t.Fatalf("%s was not held back as pin-only: %s", pinOnly, p.Decision)
				}
			}
		}
		// A project pin selects a vetted OpenRouter backup even while OpenAI is connected.
		if _, err := d.Exec(ctx, `INSERT INTO routing_pins (id,scope_kind,scope_id,task,model_id,provider_id) VALUES (?,'project',?,?,?,?)`, db.NewID(), f.projectID, TaskPanel, openRouterNanoBanana21, or.ID); err != nil {
			t.Fatal(err)
		}
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 1024, Height: 683, Count: 1}})
		if p.ProviderID != or.ID || p.ModelID != openRouterNanoBanana21 || p.PinModelID != openRouterNanoBanana21 {
			t.Fatalf("pinned plan = %+v", p)
		}
		// The licence guard still applies to the new routes.
		manifest, err := m.catalog.lookup(ctx, gptImageSunburst)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Commercial = false
		raw, _ := json.Marshal(manifest)
		if _, err := d.Exec(ctx, `INSERT INTO models (id,source,manifest,enabled) VALUES (?,'custom',?,TRUE)`, manifest.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
		guarded := planWith(t, m, f, ImagePlanRequest{Task: TaskRegionEdit, PublicSeries: true, Needs: ImageNeeds{Width: 800, Height: 600, Count: 1, Edit: true, Mask: true}})
		if guarded.ModelID == gptImageSunburst || !strings.Contains(string(guarded.Decision), gptImageSunburst+": licence") {
			t.Fatalf("licence guard skipped: %+v %s", guarded, guarded.Decision)
		}
	})
}

func TestImageFallbackLeavesTheFailedProviderKind(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		openai := createOpenAIImageProvider(t, m, f, "sk-fallback")
		or := f.createProvider(t, m.providers, providerScopeServer, "or-fallback")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1}})
		if p.ProviderID != openai.ID {
			t.Fatalf("plan = %+v", p)
		}
		id := imageTestJob(t, m, f, p)
		j, err := m.d.Jobs.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		rr, err := m.image.fallback(ctx, j, p, jobs.JobError{Code: CodeRateLimited, Retryable: true})
		if err != nil || rr == nil || rr.ProviderID != or.ID || rr.ModelID != seedreamModel {
			t.Fatalf("first fallback = %+v %v (Flare must not be tried on the same provider)", rr, err)
		}
		var dec imageDecision
		if err := json.Unmarshal(rr.Decision, &dec); err != nil || dec.SwitchedFrom != "openai:"+gptImageSunburst || dec.SwitchCode != CodeRateLimited || !strings.Contains(rr.Reason, "openai:"+gptImageSunburst+" → openrouter:"+seedreamModel) {
			t.Fatalf("switch record = %s %q", rr.Decision, rr.Reason)
		}
		// Second failure on OpenRouter: the job's own record of the first
		// switch excludes OpenAI too, so the chain ends at the placeholder.
		if _, err := d.Exec(ctx, `INSERT INTO job_events (id, job_id, kind, message, data) VALUES (?, ?, 'fallback', '', ?)`, db.NewID(), id, db.MustJSON(map[string]string{"from_model": gptImageSunburst, "from_provider": openai.ID})); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `UPDATE jobs SET provider_id = ?, model_id = ?, attempts = 2 WHERE id = ?`, or.ID, seedreamModel, id); err != nil {
			t.Fatal(err)
		}
		j, _ = m.d.Jobs.Get(ctx, id)
		rr, err = m.image.fallback(ctx, j, p, jobs.JobError{Code: CodeTimeout, Retryable: true})
		if err != nil || rr == nil || rr.ProviderID != PlaceholderID {
			t.Fatalf("second fallback = %+v %v", rr, err)
		}
		// Terminal failures and pinned plans never fall back.
		for _, code := range []string{CodeProviderRefuse, CodeProviderAuth, CodeBadInput, CodeSpendLimit} {
			if rr, err := m.image.fallback(ctx, j, p, jobs.JobError{Code: code}); err != nil || rr != nil {
				t.Fatalf("terminal %s fell back: %+v", code, rr)
			}
		}
		pinned := p
		pinned.PinModelID = gptImageSunburst
		if rr, err := m.image.fallback(ctx, j, pinned, jobs.JobError{Code: CodeTimeout}); err != nil || rr != nil {
			t.Fatal("pinned plan fell back")
		}
	})
}

func TestOpenRouterBackupRoutesSendTheirDocumentedParameters(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		out := imageTestPNG(t, 1024, 1024, color.NRGBA{9, 9, 9, 255})
		ok := func(*http.Request) (*http.Response, error) {
			return httpJSON(200, map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(out), "media_type": "image/png"}}, "usage": map[string]any{"cost": 0.031}}, nil), nil
		}
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){ok, ok}}
		m, f := imageTestModule(t, d, fake)
		or := f.createProvider(t, m.providers, providerScopeServer, "or-backup-secret")
		ref := imageTestPNG(t, 1400, 700, color.NRGBA{100, 100, 100, 255})
		for i, model := range []string{openRouterNanoBanana21, openRouterGPTImageSunburst} {
			p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, PinModelID: model, Needs: ImageNeeds{Width: 1024, Height: 576, Count: 1, References: 1}})
			if p.ProviderID != or.ID || p.ModelID != model {
				t.Fatalf("pinned plan = %+v", p)
			}
			id := imageTestJob(t, m, f, p)
			req := imageTestRequest(f, p, id)
			req.Background = "transparent"
			req.References = []ImageRef{{Data: ref, ContentType: "image/png", Role: "reference"}}
			result, err := m.image.GenerateImage(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if result.Actual.USD != 0.031 || result.Images[0].Width != 1024 || result.Images[0].Height != 576 {
				t.Fatalf("result = %+v", result)
			}
			seen := fake.requests()[i]
			var body map[string]any
			_ = json.Unmarshal(seen.Body, &body)
			refs, _ := body["input_references"].([]any)
			if seen.URL != "https://openrouter.ai/api/v1/images" || seen.Authorization != "Bearer or-backup-secret" || body["model"] != model || body["n"] != float64(1) || len(refs) != 1 || body["aspect_ratio"] != "16:9" || body["provider"].(map[string]any)["allow_fallbacks"] != false {
				t.Fatalf("%s body = %v", model, body)
			}
			if model == openRouterNanoBanana21 && (body["resolution"] != "1K" || body["background"] != nil || body["quality"] != nil) {
				t.Fatalf("nano banana params = %v", body)
			}
			if model == openRouterGPTImageSunburst && (body["background"] != "transparent" || body["quality"] != "high" || body["output_format"] != "png" || body["resolution"] != nil) {
				t.Fatalf("gpt image params = %v", body)
			}
			var estimate int64
			if err := d.Get(ctx, &estimate, `SELECT estimate_microusd FROM spend_reservations WHERE job_id = ?`, id); err != nil {
				t.Fatal(err)
			}
			if quote, _ := quoteImage(providerKindOpenRouter, model, p.Needs); estimate != int64(math.Round(quote.USD*1e6)) {
				t.Fatalf("%s reserved %d for quote %v", model, estimate, quote.USD)
			}
		}
	})
}

func TestFitGeneratedImageCoversWithoutDistortion(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 300, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 300; x++ {
			c := color.NRGBA{0, 0, 0, 255}
			if x >= 100 && x < 200 {
				c = color.NRGBA{255, 255, 255, 255}
			}
			src.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, src)
	g, err := fitGeneratedImage(b.Bytes(), 100, 100, 7, true)
	if err != nil || g.Width != 100 || g.Seed != 7 {
		t.Fatalf("fit = %+v %v", g, err)
	}
	im, _, _ := storage.DecodeImage(g.Data, storage.ImageLimits{})
	// Cover keeps the centre square: entirely the white middle band.
	if r, _, _, _ := im.At(50, 50).RGBA(); r>>8 != 255 {
		t.Fatal("cover did not centre-crop")
	}
	if _, err := fitGeneratedImage([]byte("not an image"), 10, 10, 0, true); err == nil {
		t.Fatal("garbage accepted")
	}
	var g2 *GenerateError
	if _, err := fitGeneratedImage(bytes.Repeat([]byte{0}, maxImageBytes+1), 10, 10, 0, true); !errors.As(safeImageAdapterError(err, jobs.Cost{}), &g2) || g2.Code != CodeBadOutput {
		t.Fatal("oversized output accepted")
	}
}
