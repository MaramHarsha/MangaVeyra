package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// ADR 0013 §9 image routes. Request shapes follow each provider's public
// documentation (cited in the adapters); all responses are fake.

func TestPhase4ImageQuotesAreExactBounds(t *testing.T) {
	for _, tc := range []struct {
		kind, model string
		n           ImageNeeds
		want        float64
		ok          bool
	}{
		// (1,120 + 4,096) × $30/M + 6,144 × $1.50/M.
		{providerKindGoogle, googleImageModel, ImageNeeds{Width: 1024, Height: 1024, Count: 1}, 0.165696, true},
		// Two references add 2 × 1,120 × $1.50/M.
		{providerKindGoogle, googleImageModel, ImageNeeds{Width: 1024, Height: 1024, Count: 1, References: 2}, 0.169056, true},
		{providerKindGoogle, googleImageModel, ImageNeeds{Width: 1024, Height: 1024, Count: 2}, 0, false},
		{providerKindGoogle, googleImageModel, ImageNeeds{Width: 1024, Height: 1024, Count: 1, Edit: true, Mask: true}, 0, false},
		{providerKindXAI, xaiImageModel, ImageNeeds{Width: 1024, Height: 1024, Count: 3}, 0.06, true},
		{providerKindXAI, xaiImageModel, ImageNeeds{Width: 1024, Height: 1024, Count: 1, Edit: true}, 0, false},
		// 1024² = 1.05 MP → 2 whole MP × $0.04.
		{providerKindTogether, togetherImageModel, ImageNeeds{Width: 1024, Height: 1024, Count: 1}, 0.08, true},
		{providerKindTogether, togetherImageModel, ImageNeeds{Width: 1000, Height: 1000, Count: 2}, 0.08, true},
		{providerKindTogether, togetherImageModel, ImageNeeds{Width: 2048, Height: 1024, Count: 1}, 0, false},
		{providerKindReplicate, replicateFluxModel, ImageNeeds{Width: 1024, Height: 1024, Count: 4}, 0.012, true},
		// 120 s Cancel-After × $0.000975/s.
		{providerKindReplicate, replicateSDXLModel, ImageNeeds{Width: 1024, Height: 1024, Count: 4, Edit: true, Mask: true}, 0.117, true},
		// 1024² × 2 × $0.006/MP.
		{providerKindRunware, runwareModel, ImageNeeds{Width: 1024, Height: 1024, Count: 2}, 0.012583, true},
	} {
		got, ok := quoteImage(tc.kind, tc.model, tc.n)
		if ok != tc.ok || ok && math.Abs(got.USD-tc.want) > 1e-12 {
			t.Errorf("quote %s %s %+v = %v %v, want %v %v", tc.kind, tc.model, tc.n, got.USD, ok, tc.want, tc.ok)
		}
	}
	// Only the time-billed SDXL route is pin-only.
	for _, r := range imageRoutes {
		if r.Kind == providerKindReplicate && r.Model == replicateSDXLModel != r.PinOnly {
			t.Errorf("route %s pin-only = %v", r.Model, r.PinOnly)
		}
	}
}

func phase4PNG(t *testing.T, w, h int) []byte {
	return imageTestPNG(t, w, h, color.NRGBA{40, 50, 60, 255})
}

func b64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

type imageRouteCase struct {
	name, kind, model string
	needs             ImageNeeds
	pin               bool
	setup             func(t *testing.T, doer *routeDoer)
	check             func(t *testing.T, seen []recordedRequest)
	actualMicro       int64
}

func phase4ImageCases(t *testing.T) []imageRouteCase {
	out := phase4PNG(t, 1024, 1024)
	return []imageRouteCase{
		{name: "google", kind: providerKindGoogle, model: googleImageModel, needs: ImageNeeds{Width: 768, Height: 1024, Count: 1, References: 1},
			setup: func(t *testing.T, doer *routeDoer) {
				doer.on(http.MethodPost, googleAPIBase+"/models/"+googleImageModel+":generateContent", reply(200, map[string]any{
					"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
						map[string]any{"text": "plan", "thought": true}, map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": b64(out)}}}},
						"finishReason": "STOP"}},
					"usageMetadata": map[string]any{"promptTokenCount": 1500, "candidatesTokenCount": 1200, "thoughtsTokenCount": 300,
						"candidatesTokensDetails": []any{map[string]any{"modality": "IMAGE", "tokenCount": 1120}, map[string]any{"modality": "TEXT", "tokenCount": 80}}},
				}))
			},
			check: func(t *testing.T, seen []recordedRequest) {
				r := seen[0]
				body := r.json(t)
				gc, _ := body["generationConfig"].(map[string]any)
				ic, _ := gc["imageConfig"].(map[string]any)
				modalities, _ := json.Marshal(gc["responseModalities"])
				contents, _ := body["contents"].([]any)
				parts := contents[0].(map[string]any)["parts"].([]any)
				if r.Header.Get("x-goog-api-key") != "sk-google-test" || r.Header.Get("Authorization") != "" || string(modalities) != `["IMAGE"]` ||
					ic["aspectRatio"] != "3:4" || ic["imageSize"] != "1K" || gc["maxOutputTokens"] != float64(googleImageMaxOutput) || len(parts) != 2 ||
					body["safetySettings"] == nil || strings.Contains(parts[0].(map[string]any)["text"].(string), "transparent") {
					t.Fatalf("google image body = %.600s", r.Body)
				}
				inline := parts[1].(map[string]any)["inlineData"].(map[string]any)
				if inline["mimeType"] != "image/png" || inline["data"] == "" {
					t.Fatalf("reference part = %v", parts[1])
				}
			},
			// 1,500 × $1.50/M + 1,120 × $30/M + (80 + 300) × $7.50/M.
			actualMicro: 38_700},
		{name: "xai", kind: providerKindXAI, model: xaiImageModel, needs: ImageNeeds{Width: 1024, Height: 576, Count: 2},
			setup: func(t *testing.T, doer *routeDoer) {
				doer.on(http.MethodPost, xaiImageURL, reply(200, map[string]any{"data": []any{map[string]any{"b64_json": b64(out)}, map[string]any{"b64_json": b64(out)}},
					"usage": map[string]any{"cost_in_usd_ticks": 400_000_000}}))
			},
			check: func(t *testing.T, seen []recordedRequest) {
				body := seen[0].json(t)
				if seen[0].Header.Get("Authorization") != "Bearer sk-xai-test" || body["model"] != xaiImageModel || body["n"] != float64(2) ||
					body["aspect_ratio"] != "16:9" || body["resolution"] != "1k" || body["response_format"] != "b64_json" || body["size"] != nil {
					t.Fatalf("xai image body = %v", body)
				}
			},
			actualMicro: 40_000},
		{name: "together", kind: providerKindTogether, model: togetherImageModel, needs: ImageNeeds{Width: 1022, Height: 683, Count: 1},
			setup: func(t *testing.T, doer *routeDoer) {
				doer.on(http.MethodPost, togetherImageURL, reply(200, map[string]any{"data": []any{map[string]any{"index": 0, "b64_json": b64(out), "type": "image"}}}))
			},
			check: func(t *testing.T, seen []recordedRequest) {
				body := seen[0].json(t)
				if seen[0].Header.Get("Authorization") != "Bearer sk-together-test" || body["model"] != togetherImageModel || body["width"] != float64(1024) ||
					body["height"] != float64(680) || body["n"] != float64(1) || body["response_format"] != "base64" || body["output_format"] != "png" ||
					body["negative_prompt"] != "speech bubbles" || body["disable_safety_checker"] != nil || body["steps"] != nil {
					t.Fatalf("together image body = %v", body)
				}
			},
			actualMicro: 40_000},
		{name: "replicate flux", kind: providerKindReplicate, model: replicateFluxModel, needs: ImageNeeds{Width: 1024, Height: 1024, Count: 2},
			setup: func(t *testing.T, doer *routeDoer) {
				pending := map[string]any{"id": "p1", "status": "starting", "urls": map[string]string{"get": replicateAPI + "/predictions/p1", "cancel": replicateAPI + "/predictions/p1/cancel"}}
				done := map[string]any{"id": "p1", "status": "succeeded", "output": []string{"https://replicate.delivery/xezq/a.png", "https://replicate.delivery/xezq/b.png"},
					"urls": pending["urls"], "metrics": map[string]any{"predict_time": 1.2}}
				doer.on(http.MethodPost, replicateAPI+"/models/"+replicateFluxModel+"/predictions", reply(201, pending))
				doer.once(http.MethodGet, replicateAPI+"/predictions/p1", reply(200, map[string]any{"id": "p1", "status": "processing", "urls": pending["urls"]}))
				doer.on(http.MethodGet, replicateAPI+"/predictions/p1", reply(200, done))
				doer.on(http.MethodGet, "https://replicate.delivery/", replyBytes(200, "image/png", out))
			},
			check: func(t *testing.T, seen []recordedRequest) {
				create := seen[0]
				body := create.json(t)
				input, _ := body["input"].(map[string]any)
				if create.Header.Get("Authorization") != "Bearer sk-replicate-test" || create.Header.Get("Prefer") != "wait=60" ||
					create.Header.Get("Cancel-After") != "120s" || body["version"] != nil || input["num_outputs"] != float64(2) ||
					input["aspect_ratio"] != "1:1" || input["output_format"] != "png" || input["megapixels"] != "1" || input["num_inference_steps"] != float64(4) ||
					input["disable_safety_checker"] != false {
					t.Fatalf("replicate create = %s %v", create.URL, body)
				}
				for _, r := range seen[1:] {
					if strings.HasPrefix(r.URL, "https://replicate.delivery/") && r.Header.Get("Authorization") != "" {
						t.Fatal("output download carried the provider key")
					}
				}
				if len(seen) != 5 {
					t.Fatalf("requests = %d", len(seen))
				}
			},
			actualMicro: 6_000},
		{name: "replicate sdxl pinned", kind: providerKindReplicate, model: replicateSDXLModel, pin: true,
			needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1},
			setup: func(t *testing.T, doer *routeDoer) {
				doer.on(http.MethodPost, replicateAPI+"/predictions", reply(201, map[string]any{"id": "s1", "status": "succeeded",
					"output": []string{"https://xyz.replicate.delivery/out.png"}, "metrics": map[string]any{"predict_time": 12.5},
					"urls": map[string]string{"get": replicateAPI + "/predictions/s1"}}))
				doer.on(http.MethodGet, "https://xyz.replicate.delivery/", replyBytes(200, "image/png", out))
			},
			check: func(t *testing.T, seen []recordedRequest) {
				body := seen[0].json(t)
				input, _ := body["input"].(map[string]any)
				if seen[0].URL != replicateAPI+"/predictions" || body["version"] != replicateSDXLModel+":"+replicateSDXLVersion ||
					input["width"] != float64(1024) || input["num_outputs"] != float64(1) || input["negative_prompt"] != "speech bubbles" {
					t.Fatalf("sdxl create = %v", body)
				}
			},
			// 12.5 s × $0.000975/s, rounded up.
			actualMicro: 12_188},
		{name: "runware", kind: providerKindRunware, model: runwareModel, needs: ImageNeeds{Width: 1000, Height: 700, Count: 2},
			setup: func(t *testing.T, doer *routeDoer) {
				doer.on(http.MethodPost, runwareURL, func(_ *http.Request, body []byte) (*http.Response, error) {
					var tasks []map[string]any
					_ = json.Unmarshal(body, &tasks)
					id := tasks[0]["taskUUID"]
					return httpJSON(200, map[string]any{"data": []any{
						map[string]any{"taskType": "imageInference", "taskUUID": id, "imageUUID": "i1", "imageBase64Data": b64(out), "cost": 0.0013, "NSFWContent": false},
						map[string]any{"taskType": "imageInference", "taskUUID": id, "imageUUID": "i2", "imageBase64Data": b64(out), "cost": 0.0013, "NSFWContent": false},
					}}, nil), nil
				})
			},
			check: func(t *testing.T, seen []recordedRequest) {
				var tasks []map[string]any
				if err := json.Unmarshal(seen[0].Body, &tasks); err != nil || len(tasks) != 1 {
					t.Fatalf("runware body = %s", seen[0].Body)
				}
				task := tasks[0]
				safety, _ := task["safety"].(map[string]any)
				uuid, _ := task["taskUUID"].(string)
				if seen[0].Header.Get("Authorization") != "Bearer sk-runware-test" || task["taskType"] != "imageInference" || task["model"] != runwareAIR ||
					task["width"] != float64(1024) || task["height"] != float64(704) || task["numberResults"] != float64(2) || task["steps"] != float64(4) ||
					task["outputType"] != "base64Data" || task["outputFormat"] != "PNG" || task["includeCost"] != true || task["deliveryMethod"] != "sync" ||
					safety["checkContent"] != true || len(uuid) != 36 || uuid[14] != '4' || task["negativePrompt"] != nil {
					t.Fatalf("runware task = %v", task)
				}
			},
			actualMicro: 2_600},
	}
}

func TestPhase4ImageRoutesRequestShapeAndAccounting(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		for _, tc := range phase4ImageCases(t) {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				doer := &routeDoer{}
				tc.setup(t, doer)
				var logs bytes.Buffer
				m, f := imageTestModule(t, d, doer)
				m.image.pollEvery = time.Millisecond
				m.d.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				provider := f.createKindProvider(t, m.providers, tc.kind, providerScopeUser, tc.name)
				req := ImagePlanRequest{Task: TaskPanel, Needs: tc.needs}
				if tc.pin {
					req.PinModelID = tc.model
				}
				p := planWith(t, m, f, req)
				quote, _ := quoteImage(tc.kind, tc.model, p.Needs)
				if p.ProviderID != provider.ID || p.ModelID != tc.model || !p.Remote || p.Estimate.USD != quote.USD {
					t.Fatalf("plan = %+v, quote %v", p, quote)
				}
				id := imageTestJob(t, m, f, p)
				r := imageTestRequest(f, p, id)
				r.Negative = "speech bubbles"
				for i := 0; i < tc.needs.References; i++ {
					r.References = append(r.References, ImageRef{Data: phase4PNG(t, 300, 300), ContentType: "image/png", Role: "reference"})
				}
				result, err := m.image.GenerateImage(ctx, r)
				if err != nil {
					t.Fatalf("generate: %v (requests %+v)", err, doer.requests())
				}
				if len(result.Images) != tc.needs.Count || result.Images[0].Width != tc.needs.Width || result.Images[0].Height != tc.needs.Height {
					t.Fatalf("images = %+v", result.Images)
				}
				tc.check(t, doer.requests())
				var row struct {
					Estimate int64  `db:"estimate_microusd"`
					Actual   int64  `db:"actual_microusd"`
					State    string `db:"state"`
				}
				if err := d.Get(ctx, &row, `SELECT estimate_microusd, actual_microusd, state FROM spend_reservations WHERE job_id = ?`, id); err != nil {
					t.Fatal(err)
				}
				if row.State != "reconciled" || row.Estimate != int64(math.Round(quote.USD*1e6)) || row.Actual != tc.actualMicro {
					t.Fatalf("reservation = %+v, want actual %d", row, tc.actualMicro)
				}
				if strings.Contains(logs.String(), "sk-"+tc.kind) {
					t.Fatal("decrypted key logged")
				}
			})
		}
	})
}

// Error responses map to safe codes; definite rejections reconcile at zero,
// unknown outcomes and charged refusals keep the reservation.
func TestPhase4ImageErrorsMapAndAccount(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		out := phase4PNG(t, 1024, 1024)
		openAIShaped := func(status int, code string) func(*http.Request, []byte) (*http.Response, error) {
			return reply(status, map[string]any{"error": map[string]any{"code": code, "type": "test", "message": "hidden provider detail"}})
		}
		for _, tc := range []struct {
			name, kind, model, url string
			respond                func(*http.Request, []byte) (*http.Response, error)
			code                   string
			retry, charged         bool
		}{
			{"google auth", providerKindGoogle, googleImageModel, googleAPIBase, func(*http.Request, []byte) (*http.Response, error) { return googleFail(400, "API_KEY_INVALID"), nil }, CodeProviderAuth, false, false},
			{"google rate", providerKindGoogle, googleImageModel, googleAPIBase, func(*http.Request, []byte) (*http.Response, error) { return googleFail(429, "RESOURCE_EXHAUSTED"), nil }, CodeRateLimited, true, false},
			{"google quota", providerKindGoogle, googleImageModel, googleAPIBase, func(*http.Request, []byte) (*http.Response, error) { return googleFail(402, "RESOURCE_EXHAUSTED"), nil }, CodeProviderAuth, false, false},
			{"google image safety", providerKindGoogle, googleImageModel, googleAPIBase, reply(200, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{}}, "finishReason": "IMAGE_SAFETY"}}}), CodeProviderRefuse, false, true},
			{"google no image", providerKindGoogle, googleImageModel, googleAPIBase, reply(200, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "sorry"}}}, "finishReason": "STOP"}}}), CodeBadOutput, false, true},
			{"google timeout", providerKindGoogle, googleImageModel, googleAPIBase, replyErr(context.DeadlineExceeded), CodeTimeout, true, true},
			{"xai auth", providerKindXAI, xaiImageModel, xaiImageURL, openAIShaped(401, "invalid_api_key"), CodeProviderAuth, false, false},
			{"xai rate", providerKindXAI, xaiImageModel, xaiImageURL, openAIShaped(429, ""), CodeRateLimited, true, false},
			// xAI charges a moderated generation; a 400 does not say whether one ran.
			{"xai bad request", providerKindXAI, xaiImageModel, xaiImageURL, openAIShaped(400, ""), CodeUpstream, false, true},
			{"xai server", providerKindXAI, xaiImageModel, xaiImageURL, openAIShaped(500, ""), CodeUpstream, true, true},
			{"together safety", providerKindTogether, togetherImageModel, togetherImageURL, openAIShaped(422, ""), CodeProviderRefuse, false, false},
			{"together quota", providerKindTogether, togetherImageModel, togetherImageURL, openAIShaped(402, ""), CodeProviderAuth, false, false},
			{"together server", providerKindTogether, togetherImageModel, togetherImageURL, openAIShaped(503, ""), CodeUpstream, true, false},
			{"together wrong count", providerKindTogether, togetherImageModel, togetherImageURL, reply(200, map[string]any{"data": []any{map[string]any{"b64_json": b64(out)}, map[string]any{"b64_json": b64(out)}}}), CodeBadOutput, false, true},
			{"replicate quota", providerKindReplicate, replicateFluxModel, replicateAPI, reply(402, map[string]any{"detail": "hidden"}), CodeProviderAuth, false, false},
			{"replicate invalid", providerKindReplicate, replicateFluxModel, replicateAPI, reply(422, map[string]any{"detail": "hidden"}), CodeBadInput, false, false},
			{"replicate throttled", providerKindReplicate, replicateFluxModel, replicateAPI, reply(429, map[string]any{"detail": "Request was throttled."}), CodeRateLimited, true, false},
			// "if a run fails, we don't charge you"
			{"replicate failed", providerKindReplicate, replicateFluxModel, replicateAPI, reply(201, map[string]any{"id": "f", "status": "failed", "error": "CUDA error", "urls": map[string]string{"get": replicateAPI + "/predictions/f"}}), CodeUpstream, true, false},
			{"replicate nsfw", providerKindReplicate, replicateFluxModel, replicateAPI, reply(201, map[string]any{"id": "f", "status": "failed", "error": "NSFW content detected. Try running it again.", "urls": map[string]string{"get": replicateAPI + "/predictions/f"}}), CodeProviderRefuse, false, false},
			{"replicate canceled", providerKindReplicate, replicateFluxModel, replicateAPI, reply(201, map[string]any{"id": "f", "status": "canceled", "urls": map[string]string{"get": replicateAPI + "/predictions/f"}}), CodeUpstream, true, true},
			{"replicate foreign output", providerKindReplicate, replicateFluxModel, replicateAPI, reply(201, map[string]any{"id": "f", "status": "succeeded", "output": []string{"https://example.com/a.png"}, "urls": map[string]string{"get": replicateAPI + "/predictions/f"}}), CodeBadOutput, false, true},
			{"runware key", providerKindRunware, runwareModel, runwareURL, reply(401, map[string]any{"errors": []any{map[string]any{"code": "invalidApiKey", "message": "hidden"}}}), CodeProviderAuth, false, false},
			{"runware balance", providerKindRunware, runwareModel, runwareURL, reply(402, map[string]any{"errors": []any{map[string]any{"code": "insufficientCredits"}}}), CodeProviderAuth, false, false},
			{"runware overloaded", providerKindRunware, runwareModel, runwareURL, reply(429, map[string]any{"errors": []any{map[string]any{"code": "serviceOverloaded", "retryAfter": 3}}}), CodeRateLimited, true, false},
			{"runware nsfw", providerKindRunware, runwareModel, runwareURL, func(_ *http.Request, body []byte) (*http.Response, error) {
				var tasks []map[string]any
				_ = json.Unmarshal(body, &tasks)
				return httpJSON(200, map[string]any{"data": []any{map[string]any{"taskType": "imageInference", "taskUUID": tasks[0]["taskUUID"], "imageUUID": "x", "imageBase64Data": b64(out), "NSFWContent": true, "cost": 0.0013}}}, nil), nil
			}, CodeProviderRefuse, false, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				doer := (&routeDoer{}).on(http.MethodPost, tc.url, tc.respond)
				m, f := imageTestModule(t, d, doer)
				m.image.pollEvery = time.Millisecond
				f.createKindProvider(t, m.providers, tc.kind, providerScopeUser, tc.name)
				p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, PinModelID: tc.model, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1}})
				id := imageTestJob(t, m, f, p)
				_, err := m.image.GenerateImage(context.Background(), imageTestRequest(f, p, id))
				g := requireGenerateCode(t, err, tc.code)
				if g.Retryable != tc.retry || strings.Contains(err.Error(), "hidden") || strings.Contains(err.Error(), "sk-") {
					t.Fatalf("error = %+v %q", g, err)
				}
				var row struct {
					Estimate int64 `db:"estimate_microusd"`
					Actual   int64 `db:"actual_microusd"`
				}
				if err := d.Get(context.Background(), &row, `SELECT estimate_microusd, actual_microusd FROM spend_reservations WHERE job_id = ?`, id); err != nil {
					t.Fatal(err)
				}
				if charged := row.Actual > 0; charged != tc.charged {
					t.Fatalf("reservation %+v charged=%v, want %v", row, charged, tc.charged)
				}
			})
		}
	})
}

// A Runware task that outlives the 120 s sync window is polled with
// getResponse for the same task UUID; it is never resent.
func TestPhase4RunwarePollsWithoutResending(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		out := phase4PNG(t, 512, 512)
		var taskUUID string
		inferences := 0
		doer := (&routeDoer{}).on(http.MethodPost, runwareURL, func(_ *http.Request, body []byte) (*http.Response, error) {
			var tasks []map[string]any
			_ = json.Unmarshal(body, &tasks)
			switch tasks[0]["taskType"] {
			case "imageInference":
				inferences++
				taskUUID, _ = tasks[0]["taskUUID"].(string)
				return httpJSON(200, map[string]any{"data": []any{map[string]any{"taskType": "imageInference", "taskUUID": taskUUID, "status": "processing"}}}, nil), nil
			case "getResponse":
				if tasks[0]["taskUUID"] != taskUUID {
					t.Errorf("polled another task: %v", tasks[0])
				}
				return httpJSON(200, map[string]any{"data": []any{map[string]any{"taskType": "imageInference", "taskUUID": taskUUID, "status": "success",
					"imageUUID": "a", "imageBase64Data": b64(out), "cost": 0.0011}}}, nil), nil
			}
			return httpJSON(400, map[string]any{}, nil), nil
		})
		m, f := imageTestModule(t, d, doer)
		m.image.pollEvery = time.Millisecond
		f.createKindProvider(t, m.providers, providerKindRunware, providerScopeServer, "Runware")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1}})
		id := imageTestJob(t, m, f, p)
		result, err := m.image.GenerateImage(context.Background(), imageTestRequest(f, p, id))
		if err != nil || len(result.Images) != 1 || inferences != 1 || math.Abs(result.Actual.USD-0.0011) > 1e-12 {
			t.Fatalf("result = %+v %v inferences=%d", result, err, inferences)
		}
	})
}

// Region edits through Replicate SDXL and Runware send the init and mask and
// the server still restores every pixel outside the mask.
func TestPhase4MaskedEditsRestoreOutsidePixels(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		w, h := 256, 128
		initIm := image.NewNRGBA(image.Rect(0, 0, w, h))
		maskIm := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				initIm.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 7, 255})
				maskIm.SetNRGBA(x, y, color.NRGBA{A: 255})
				if x >= 64 && x < 128 {
					maskIm.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
				}
			}
		}
		var ib, mb bytes.Buffer
		_ = png.Encode(&ib, initIm)
		_ = png.Encode(&mb, maskIm)
		edited := imageTestPNG(t, 256, 128, color.NRGBA{250, 0, 0, 255})
		for _, tc := range []struct {
			kind, model string
			setup       func(*routeDoer)
			inputs      func(t *testing.T, body []byte)
		}{
			{providerKindReplicate, replicateSDXLModel, func(doer *routeDoer) {
				doer.on(http.MethodPost, replicateAPI+"/predictions", reply(201, map[string]any{"id": "e", "status": "succeeded", "output": "https://replicate.delivery/e.png", "urls": map[string]string{"get": replicateAPI + "/predictions/e"}}))
				doer.on(http.MethodGet, "https://replicate.delivery/", replyBytes(200, "image/png", edited))
			}, func(t *testing.T, body []byte) {
				var v struct {
					Input map[string]any `json:"input"`
				}
				_ = json.Unmarshal(body, &v)
				img, _ := v.Input["image"].(string)
				mask, _ := v.Input["mask"].(string)
				if !strings.HasPrefix(img, "data:image/jpeg;base64,") || !strings.HasPrefix(mask, "data:image/png;base64,") || v.Input["prompt_strength"] != 0.8 {
					t.Fatalf("sdxl edit input = %.200v", v.Input)
				}
			}},
			{providerKindRunware, runwareModel, func(doer *routeDoer) {
				doer.on(http.MethodPost, runwareURL, func(_ *http.Request, body []byte) (*http.Response, error) {
					var tasks []map[string]any
					_ = json.Unmarshal(body, &tasks)
					return httpJSON(200, map[string]any{"data": []any{map[string]any{"taskType": "imageInference", "taskUUID": tasks[0]["taskUUID"], "imageUUID": "e", "imageBase64Data": b64(edited), "cost": 0.002}}}, nil), nil
				})
			}, func(t *testing.T, body []byte) {
				var tasks []map[string]any
				_ = json.Unmarshal(body, &tasks)
				inputs, _ := tasks[0]["inputs"].(map[string]any)
				if !strings.HasPrefix(inputs["seedImage"].(string), "data:image/jpeg;base64,") || !strings.HasPrefix(inputs["maskImage"].(string), "data:image/png;base64,") || tasks[0]["strength"] != 0.8 {
					t.Fatalf("runware edit task = %.200v", tasks[0])
				}
			}},
		} {
			t.Run(tc.kind, func(t *testing.T) {
				doer := &routeDoer{}
				tc.setup(doer)
				m, f := imageTestModule(t, d, doer)
				f.createKindProvider(t, m.providers, tc.kind, providerScopeUser, "edits")
				p := planWith(t, m, f, ImagePlanRequest{Task: TaskRegionEdit, PinModelID: tc.model, Needs: ImageNeeds{Width: w, Height: h, Count: 1, Edit: true, Mask: true}})
				id := imageTestJob(t, m, f, p)
				req := imageTestRequest(f, p, id)
				req.Init = &ImageRef{Data: ib.Bytes(), ContentType: "image/png", Role: "init"}
				req.Mask = &ImageRef{Data: mb.Bytes(), ContentType: "image/png", Role: "mask"}
				result, err := m.image.GenerateImage(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				tc.inputs(t, doer.requests()[0].Body)
				im, err := decodeBounded(result.Images[0].Data)
				if err != nil {
					t.Fatal(err)
				}
				for y := 0; y < h; y++ {
					for x := 0; x < w; x++ {
						got := color.NRGBAModel.Convert(im.At(x, y)).(color.NRGBA)
						if (x < 64 || x >= 128) && got != initIm.NRGBAAt(x, y) {
							t.Fatalf("outside-mask pixel changed at %d,%d", x, y)
						}
					}
				}
			})
		}
	})
}

// The router uses the Phase 4 image routes in paid modes only, never in
// free_only or local_only, refuses the pin-only SDXL route unless pinned, and
// keeps the licence guard and capability filters.
func TestPhase4ImageRouterModesAndCapabilities(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &routeDoer{})
		for _, kind := range []string{providerKindGoogle, providerKindXAI, providerKindTogether, providerKindReplicate, providerKindRunware} {
			f.createKindProvider(t, m.providers, kind, providerScopeServer, kind)
		}
		paid := map[string]bool{googleImageModel: true, xaiImageModel: true, togetherImageModel: true, replicateFluxModel: true, runwareModel: true}
		for _, mode := range []string{ModeBalanced, ModeBest, ModeFastest} {
			p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, RoutingMode: mode, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1}})
			if !paid[p.ModelID] || !p.Remote {
				t.Fatalf("%s chose %s", mode, p.ModelID)
			}
			var decision imageDecision
			_ = json.Unmarshal(p.Decision, &decision)
			if !strings.Contains(strings.Join(decision.Skipped, ","), replicateSDXLModel+": pin only") {
				t.Fatalf("%s did not skip the pin-only SDXL route: %v", mode, decision.Skipped)
			}
		}
		for _, mode := range []string{ModeFreeOnly, ModeLocalOnly} {
			p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, RoutingMode: mode, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1}})
			if p.ModelID != PlaceholderArt || p.Remote {
				t.Fatalf("%s chose %s", mode, p.ModelID)
			}
		}
		if _, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, RoutingMode: ModeLocalOnly,
			PinModelID: xaiImageModel, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1}}); !isCode(err, CodeLocalOnly) {
			t.Fatalf("local-only pin = %v", err)
		}
		// Masked edits go only to routes with native masks (or the placeholder).
		edit := planWith(t, m, f, ImagePlanRequest{Task: TaskRegionEdit, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1, Edit: true, Mask: true}})
		if edit.ModelID != runwareModel {
			t.Fatalf("masked edit chose %s", edit.ModelID)
		}
		// Character references rule out the routes without reference inputs.
		refs := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1, References: 2}})
		if refs.ModelID != googleImageModel {
			t.Fatalf("reference job chose %s", refs.ModelID)
		}
		// Google's model list without the image model skips the route.
		var google providerCandidate
		cands, _ := m.providers.Candidates(ctx, f.userID)
		for _, c := range cands {
			if c.Kind == providerKindGoogle {
				google = c
			}
		}
		if err := m.providers.recordTestResult(ctx, google.ID, providerKindGoogle, keyEncOf(t, d, google.ID), providerTestState{State: "ok", Text: capabilityReady, Images: capabilityUnavailable}); err != nil {
			t.Fatal(err)
		}
		refs = planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1, References: 2}})
		if refs.ModelID != PlaceholderArt {
			t.Fatalf("reference job after the check chose %s", refs.ModelID)
		}
		var fallback struct {
			Fallbacks []string `json:"fallbacks"`
		}
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1}})
		_ = json.Unmarshal(p.Decision, &fallback)
		if len(fallback.Fallbacks) != maxImageAttempts {
			t.Fatalf("fallback chain = %v", fallback.Fallbacks)
		}
		_ = jobs.Cost{}
	})
}
