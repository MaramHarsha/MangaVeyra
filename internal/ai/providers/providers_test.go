package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tinyPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func testEnv() Env { return Env{PollInterval: time.Millisecond, MaxWait: 5 * time.Second} }

func readJSON(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Errorf("decode body: %v", err)
	}
	return m
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func assertPNG(t *testing.T, res *ImageResult, n int) {
	t.Helper()
	if res == nil || len(res.Images) != n {
		t.Fatalf("want %d images, got %+v", n, res)
	}
	for _, im := range res.Images {
		if _, err := png.Decode(bytes.NewReader(im.Data)); err != nil {
			t.Fatalf("not a PNG: %v", err)
		}
	}
}

func TestOpenAIImagesChatSpeechAndErrors(t *testing.T) {
	pngData := tinyPNG(t, 4, 6)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(401)
			writeJSON(w, map[string]any{"error": map[string]any{"message": "Incorrect API key provided", "type": "invalid_request_error"}})
			return
		}
		switch r.URL.Path {
		case "/v1/images/generations":
			in := readJSON(t, r)
			if in["size"] != "1024x1536" || in["model"] != "gpt-image-2" || in["n"].(float64) != 2 {
				t.Errorf("generation body %v", in)
			}
			if strings.Contains(in["prompt"].(string), "forbidden") {
				w.WriteHeader(400)
				writeJSON(w, map[string]any{"error": map[string]any{"message": "Your request was rejected as a result of our safety system.", "code": "moderation_blocked"}})
				return
			}
			writeJSON(w, map[string]any{"created": 1, "data": []any{map[string]any{"b64_json": b64(pngData)}, map[string]any{"b64_json": b64(pngData)}}})
		case "/v1/images/edits":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			if len(r.MultipartForm.File["image[]"]) != 2 || len(r.MultipartForm.File["mask"]) != 1 || r.FormValue("model") != "gpt-image-2" {
				t.Errorf("edit form %v", r.MultipartForm)
			}
			writeJSON(w, map[string]any{"data": []any{map[string]any{"b64_json": b64(pngData)}}})
		case "/v1/chat/completions":
			in := readJSON(t, r)
			if in["max_completion_tokens"].(float64) != 100 {
				t.Errorf("chat body %v", in)
			}
			msgs := in["messages"].([]any)
			if msgs[0].(map[string]any)["role"] != "system" {
				t.Errorf("system first: %v", msgs)
			}
			writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "Rin runs."}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 3}})
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("ID3fake"))
		case "/v1/models":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "gpt-image-2"}, map[string]any{"id": "gpt-5"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := OpenAI{Env: testEnv()}
	cfg := Config{Kind: KindOpenAI, BaseURL: srv.URL + "/v1", APIKey: "sk-test"}
	ctx := context.Background()

	res, err := a.Image(ctx, cfg, ImageRequest{Model: "gpt-image-2", Prompt: "Rin on the pier", Width: 800, Height: 1200, N: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 2)
	src := Image{Data: pngData, MIME: "image/png"}
	res, err = a.Image(ctx, cfg, ImageRequest{Task: "inpaint", Model: "gpt-image-2", Prompt: "fix the sign", Image: &src, Mask: &src, Refs: []Image{src}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	txt, err := a.Text(ctx, cfg, TextRequest{Model: "gpt-5", System: "Be brief.", Messages: []Message{{Role: "user", Content: "Hi"}}, MaxTokens: 100})
	if err != nil || txt.Text != "Rin runs." || txt.TokensIn != 12 {
		t.Fatalf("text %+v %v", txt, err)
	}
	media, err := a.Media(ctx, cfg, MediaRequest{Task: "voice", Model: "gpt-4o-mini-tts", Text: "Hello"}, nil)
	if err != nil || media.Media.MIME != "audio/mpeg" {
		t.Fatalf("speech %+v %v", media, err)
	}
	models, err := a.Models(ctx, cfg)
	if err != nil || len(models) != 2 || models[0].Kind != "image" {
		t.Fatalf("models %+v %v", models, err)
	}
	_, err = a.Image(ctx, cfg, ImageRequest{Model: "gpt-image-2", Prompt: "forbidden", Width: 800, Height: 1200, N: 2}, nil)
	if CodeOf(err) != CodeSafety {
		t.Fatalf("safety: %v", err)
	}
	_, err = a.Text(ctx, Config{BaseURL: srv.URL + "/v1", APIKey: "wrong"}, TextRequest{Model: "gpt-5"})
	if CodeOf(err) != CodeAuth || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("auth: %v", err)
	}
}

func TestOpenRouterImagesTextAndDiscovery(t *testing.T) {
	pngData := tinyPNG(t, 3, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/chat/completions":
			in := readJSON(t, r)
			if r.Header.Get("X-Title") != "MangaVeyra" {
				t.Error("missing X-Title")
			}
			if mods, ok := in["modalities"].([]any); ok && len(mods) == 2 {
				if in["image_config"].(map[string]any)["aspect_ratio"] != "2:3" {
					t.Errorf("image_config %v", in["image_config"])
				}
				writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "Here you go",
					"images": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + b64(pngData)}}}}}},
					"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1290, "cost": 0.039}})
				return
			}
			writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}}},
				"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "cost": 0.0001}})
		case "/api/v1/models":
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "google/gemini-3-pro-image-preview", "name": "Nano Banana Pro", "context_length": 65536,
					"architecture": map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"image", "text"}},
					"pricing":      map[string]any{"prompt": "0.000002", "completion": "0.000012", "image": "0.134"}},
				map[string]any{"id": "anthropic/claude-opus-5", "name": "Claude Opus 5", "context_length": 1000000,
					"architecture": map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"text"}},
					"pricing":      map[string]any{"prompt": "0.000005", "completion": "0.000025"}, "supported_parameters": []string{"tools"}},
			}})
		}
	}))
	defer srv.Close()
	a := OpenRouter{Env: testEnv()}
	cfg := Config{BaseURL: srv.URL + "/api/v1", APIKey: "or-key"}
	res, err := a.Image(context.Background(), cfg, ImageRequest{Model: "google/gemini-3-pro-image-preview", Prompt: "cover", Width: 1000, Height: 1500, N: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 2)
	if res.CostUSD < 0.077 || res.CostUSD > 0.079 {
		t.Fatalf("reported cost %v", res.CostUSD)
	}
	txt, err := a.Text(context.Background(), cfg, TextRequest{Model: "anthropic/claude-opus-5", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil || txt.Text != "ok" || txt.CostUSD != 0.0001 {
		t.Fatalf("text %+v %v", txt, err)
	}
	ms, err := a.Models(context.Background(), cfg)
	if err != nil || len(ms) != 2 {
		t.Fatal(err)
	}
	if ms[0].Kind != "image" || !ms[0].InputImages || ms[0].PriceUnit != "per_image" || ms[0].PriceUSD != 0.134 {
		t.Fatalf("image model %+v", ms[0])
	}
	if ms[1].Kind != "text" || ms[1].PriceUSD != 0.005 || ms[1].PriceUSDOut != 0.025 || !ms[1].SupportsTools {
		t.Fatalf("text model %+v", ms[1])
	}
}

func TestFalQueue(t *testing.T) {
	pngData := tinyPNG(t, 2, 2)
	var polls atomic.Int32
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Key fal-key" && !strings.HasPrefix(r.URL.Path, "/files/") {
			w.WriteHeader(401)
			writeJSON(w, map[string]any{"detail": "Unauthorized"})
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/fal-ai/flux-2/klein":
			in := readJSON(t, r)
			size := in["image_size"].(map[string]any)
			if size["width"].(float64) != 768 || in["num_images"].(float64) != 1 || in["seed"].(float64) != 7 {
				t.Errorf("fal input %v", in)
			}
			writeJSON(w, map[string]any{"request_id": "req-1", "status_url": srvURL + "/fal-ai/flux-2/requests/req-1/status",
				"response_url": srvURL + "/fal-ai/flux-2/requests/req-1"})
		case r.URL.Path == "/fal-ai/flux-2/requests/req-1/status":
			if polls.Add(1) < 3 {
				writeJSON(w, map[string]any{"status": "IN_QUEUE", "queue_position": 0})
				return
			}
			writeJSON(w, map[string]any{"status": "COMPLETED"})
		case r.URL.Path == "/fal-ai/flux-2/requests/req-1":
			writeJSON(w, map[string]any{"images": []any{map[string]any{"url": srvURL + "/files/out.png", "width": 768, "height": 1024}}, "seed": 7,
				"has_nsfw_concepts": []bool{false}})
		case r.URL.Path == "/files/out.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	seed := int64(7)
	var msgs []string
	res, err := Fal{Env: testEnv()}.Image(context.Background(), Config{BaseURL: srv.URL, APIKey: "fal-key"},
		ImageRequest{Model: "fal-ai/flux-2/klein", Prompt: "Pip the gull", Width: 768, Height: 1024, Seed: &seed},
		func(p float64, m string) { msgs = append(msgs, m) })
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	if res.Images[0].Seed != 7 || len(msgs) < 2 {
		t.Fatalf("seed %d progress %v", res.Images[0].Seed, msgs)
	}
}

func TestReplicatePredictions(t *testing.T) {
	pngData := tinyPNG(t, 2, 2)
	var srvURL string
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models/black-forest-labs/flux-2-pro/predictions":
			in := readJSON(t, r)
			if in["input"].(map[string]any)["prompt"] == nil || r.Header.Get("Prefer") != "wait=5" {
				t.Errorf("replicate body %v", in)
			}
			writeJSON(w, map[string]any{"id": "p1", "status": "starting", "urls": map[string]any{"get": srvURL + "/v1/predictions/p1", "cancel": srvURL + "/v1/predictions/p1/cancel"}})
		case "/v1/predictions":
			in := readJSON(t, r)
			if in["version"] != "abc123" {
				t.Errorf("version %v", in)
			}
			writeJSON(w, map[string]any{"id": "p2", "status": "failed", "error": "NSFW content detected. Try running it again, or try a different prompt."})
		case "/v1/predictions/p1":
			if gets.Add(1) < 2 {
				writeJSON(w, map[string]any{"id": "p1", "status": "processing", "urls": map[string]any{"get": srvURL + "/v1/predictions/p1"}})
				return
			}
			writeJSON(w, map[string]any{"id": "p1", "status": "succeeded", "output": []string{srvURL + "/out/0.png"}, "metrics": map[string]any{"predict_time": 4.2}})
		case "/out/0.png":
			_, _ = w.Write(pngData)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	a := Replicate{Env: testEnv()}
	cfg := Config{BaseURL: srv.URL + "/v1", APIKey: "r8_key"}
	res, err := a.Image(context.Background(), cfg, ImageRequest{Model: "black-forest-labs/flux-2-pro", Prompt: "splash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	if res.GPUSeconds != 4.2 {
		t.Fatalf("gpu seconds %v", res.GPUSeconds)
	}
	_, err = a.Image(context.Background(), cfg, ImageRequest{Model: "owner/model:abc123", Prompt: "x"}, nil)
	if CodeOf(err) != CodeSafety {
		t.Fatalf("want safety, got %v", err)
	}
}

func TestTogetherXAIOllamaCompatible(t *testing.T) {
	pngData := tinyPNG(t, 2, 2)
	chat := func(w http.ResponseWriter, content string) {
		writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/together/v1/images/generations":
			in := readJSON(t, r)
			if in["width"].(float64) != 1008 || in["response_format"] != "base64" || in["steps"].(float64) != 4 {
				t.Errorf("together %v", in)
			}
			writeJSON(w, map[string]any{"data": []any{map[string]any{"b64_json": b64(pngData)}}})
		case "/together/v1/models":
			writeJSON(w, []any{map[string]any{"id": "black-forest-labs/FLUX.2-klein", "type": "image"},
				map[string]any{"id": "meta-llama/Llama-3.3-70B-Instruct-Turbo", "type": "chat", "context_length": 131072, "pricing": map[string]any{"input": 0.88, "output": 0.88}},
				map[string]any{"id": "embed", "type": "embedding"}})
		case "/xai/v1/images/generations":
			in := readJSON(t, r)
			if in["response_format"] != "b64_json" || in["aspect_ratio"] != "9:16" {
				t.Errorf("xai %v", in)
			}
			writeJSON(w, map[string]any{"data": []any{map[string]any{"b64_json": b64(pngData), "revised_prompt": "a gull"}}})
		case "/xai/v1/chat/completions", "/ollama/v1/chat/completions", "/compat/v1/chat/completions":
			in := readJSON(t, r)
			if in["max_tokens"] == nil {
				t.Errorf("max_tokens missing: %v", in)
			}
			chat(w, "hello from "+strings.Split(r.URL.Path, "/")[1])
		case "/ollama/api/tags":
			writeJSON(w, map[string]any{"models": []any{map[string]any{"name": "llama3.3:70b", "details": map[string]any{"family": "llama", "parameter_size": "70.6B"}}}})
		case "/compat/v1/images/generations":
			in := readJSON(t, r)
			if in["size"] != "512x768" {
				t.Errorf("compat %v", in)
			}
			writeJSON(w, map[string]any{"data": []any{map[string]any{"url": "data:image/png;base64," + b64(pngData)}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	res, err := Together{Env: testEnv()}.Image(ctx, Config{BaseURL: srv.URL + "/together/v1"}, ImageRequest{Model: "flux", Prompt: "x", Width: 1010, Height: 1010, Steps: 4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	tm, err := Together{Env: testEnv()}.Models(ctx, Config{BaseURL: srv.URL + "/together/v1"})
	if err != nil || len(tm) != 2 || tm[1].PriceUSD != 0.00088 {
		t.Fatalf("together models %+v %v", tm, err)
	}
	res, err = XAI{Env: testEnv()}.Image(ctx, Config{BaseURL: srv.URL + "/xai/v1"}, ImageRequest{Model: "grok-imagine-image-2", Prompt: "x", Width: 900, Height: 1600}, nil)
	if err != nil || res.Note != "a gull" {
		t.Fatalf("xai %+v %v", res, err)
	}
	for name, a := range map[string]TextGenerator{"xai": XAI{Env: testEnv()}, "ollama": Ollama{Env: testEnv()}, "compat": OpenAICompatible{Env: testEnv()}} {
		base := srv.URL + "/" + name + "/v1"
		if name == "ollama" {
			base = srv.URL + "/ollama"
		}
		out, err := a.Text(ctx, Config{BaseURL: base}, TextRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
		if err != nil || out.Text != "hello from "+name {
			t.Fatalf("%s: %+v %v", name, out, err)
		}
	}
	om, err := Ollama{Env: testEnv()}.Models(ctx, Config{BaseURL: srv.URL + "/ollama"})
	if err != nil || len(om) != 1 || om[0].ID != "llama3.3:70b" || om[0].Description != "llama 70.6B" {
		t.Fatalf("ollama models %+v %v", om, err)
	}
	res, err = OpenAICompatible{Env: testEnv()}.Image(ctx, Config{BaseURL: srv.URL + "/compat/v1"}, ImageRequest{Model: "sd", Prompt: "x", Width: 512, Height: 768}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	if _, err := (OpenAICompatible{Env: testEnv()}).Text(ctx, Config{}, TextRequest{}); CodeOf(err) != CodeBadRequest {
		t.Fatalf("missing base URL: %v", err)
	}
}

func TestRunwareTasks(t *testing.T) {
	pngData := tinyPNG(t, 2, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tasks []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&tasks); err != nil || len(tasks) != 1 {
			t.Fatalf("tasks: %v", err)
		}
		task := tasks[0]
		if task["positivePrompt"] == "bad key" {
			writeJSON(w, map[string]any{"errors": []any{map[string]any{"code": "invalidApiKey", "message": "Invalid API key"}}})
			return
		}
		if task["taskType"] != "imageInference" || task["width"].(float64) != 832 || task["includeCost"] != true {
			t.Errorf("task %v", task)
		}
		writeJSON(w, map[string]any{"data": []any{map[string]any{"taskType": "imageInference", "taskUUID": task["taskUUID"], "imageBase64Data": b64(pngData), "seed": 11, "cost": 0.0019}}})
	}))
	defer srv.Close()
	a := Runware{Env: testEnv()}
	res, err := a.Image(context.Background(), Config{BaseURL: srv.URL, APIKey: "rw"}, ImageRequest{Model: "runware:100@1", Prompt: "x", Width: 830, Height: 1216}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	if res.CostUSD != 0.0019 || res.Images[0].Seed != 11 {
		t.Fatalf("%+v", res)
	}
	_, err = a.Image(context.Background(), Config{BaseURL: srv.URL}, ImageRequest{Model: "m", Prompt: "bad key"}, nil)
	if CodeOf(err) != CodeAuth {
		t.Fatalf("auth: %v", err)
	}
}

func TestHuggingFaceEndpointAndModelCard(t *testing.T) {
	pngData := tinyPNG(t, 2, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hf-inference/models/Qwen/Qwen-Image":
			in := readJSON(t, r)
			if in["inputs"] == nil || in["parameters"].(map[string]any)["width"].(float64) != 1024 {
				t.Errorf("hf body %v", in)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData)
		case "/endpoint":
			writeJSON(w, map[string]any{"image": b64(pngData)})
		case "/api/models/Qwen/Qwen-Image":
			writeJSON(w, map[string]any{"id": "Qwen/Qwen-Image", "pipeline_tag": "text-to-image", "tags": []string{"license:apache-2.0"}})
		case "/v1/chat/completions":
			writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "hf text"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := HuggingFace{Env: testEnv(), RouterURL: srv.URL, HubURL: srv.URL}
	ctx := context.Background()
	res, err := a.Image(ctx, Config{APIKey: "hf_x"}, ImageRequest{Model: "Qwen/Qwen-Image", Prompt: "x", N: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 2)
	res, err = a.Image(ctx, Config{BaseURL: srv.URL + "/endpoint"}, ImageRequest{Model: "any", Prompt: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	info, err := a.ModelInfo(ctx, Config{}, "Qwen/Qwen-Image")
	if err != nil || info.Kind != "image" || info.License != "apache-2.0" {
		t.Fatalf("card %+v %v", info, err)
	}
	out, err := a.Text(ctx, Config{}, TextRequest{Model: "m"})
	if err != nil || out.Text != "hf text" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestGoogleGeminiImageTextVeoAndSafety(t *testing.T) {
	pngData := tinyPNG(t, 2, 2)
	var opPolls atomic.Int32
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "g-key" {
			w.WriteHeader(403)
			writeJSON(w, map[string]any{"error": map[string]any{"code": 403, "message": "API key not valid", "status": "PERMISSION_DENIED"}})
			return
		}
		switch r.URL.Path {
		case "/v1beta/models/gemini-3-pro-image-preview:generateContent":
			in := readJSON(t, r)
			gc := in["generationConfig"].(map[string]any)
			parts := in["contents"].([]any)[0].(map[string]any)["parts"].([]any)
			if strings.Contains(parts[0].(map[string]any)["text"].(string), "blocked") {
				writeJSON(w, map[string]any{"candidates": []any{map[string]any{"finishReason": "IMAGE_SAFETY", "content": map[string]any{"parts": []any{}}}}})
				return
			}
			if gc["imageConfig"].(map[string]any)["imageSize"] != "2K" || len(parts) != 2 {
				t.Errorf("gemini body %v", in)
			}
			writeJSON(w, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
				map[string]any{"text": "Here is the cover."}, map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": b64(pngData)}}}}, "finishReason": "STOP"}}})
		case "/v1beta/models/gemini-3-pro-preview:generateContent":
			in := readJSON(t, r)
			if in["system_instruction"] == nil {
				t.Errorf("system missing %v", in)
			}
			writeJSON(w, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "Konnichiwa"}}}}},
				"usageMetadata": map[string]any{"promptTokenCount": 5, "candidatesTokenCount": 2}})
		case "/v1beta/models/veo-3.1-generate-preview:predictLongRunning":
			writeJSON(w, map[string]any{"name": "operations/op1"})
		case "/v1beta/operations/op1":
			if opPolls.Add(1) < 2 {
				writeJSON(w, map[string]any{"name": "operations/op1", "done": false})
				return
			}
			writeJSON(w, map[string]any{"name": "operations/op1", "done": true, "response": map[string]any{"generateVideoResponse": map[string]any{
				"generatedSamples": []any{map[string]any{"video": map[string]any{"uri": srvURL + "/files/v.mp4"}}}}}})
		case "/files/v.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("mp4data"))
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	a := Google{Env: testEnv()}
	cfg := Config{BaseURL: srv.URL + "/v1beta", APIKey: "g-key"}
	ctx := context.Background()
	ref := Image{Data: pngData, MIME: "image/png"}
	res, err := a.Image(ctx, cfg, ImageRequest{Model: "gemini-3-pro-image-preview", Prompt: "cover", Width: 1400, Height: 2000, Refs: []Image{ref}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	if _, err := a.Image(ctx, cfg, ImageRequest{Model: "gemini-3-pro-image-preview", Prompt: "blocked", Refs: []Image{ref}}, nil); CodeOf(err) != CodeSafety {
		t.Fatalf("safety: %v", err)
	}
	txt, err := a.Text(ctx, cfg, TextRequest{Model: "gemini-3-pro-preview", System: "Translate.", Messages: []Message{{Role: "user", Content: "Hello"}}})
	if err != nil || txt.Text != "Konnichiwa" || txt.TokensOut != 2 {
		t.Fatalf("%+v %v", txt, err)
	}
	v, err := a.Media(ctx, cfg, MediaRequest{Task: "video", Model: "veo-3.1-generate-preview", Prompt: "wave", Seconds: 4}, nil)
	if err != nil || string(v.Media.Data) != "mp4data" {
		t.Fatalf("veo %+v %v", v, err)
	}
	if _, err := a.Text(ctx, Config{BaseURL: srv.URL + "/v1beta", APIKey: "no"}, TextRequest{Model: "x"}); CodeOf(err) != CodeAuth {
		t.Fatalf("auth: %v", err)
	}
}

func TestAnthropicMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "sk-ant-test" {
			w.WriteHeader(401)
			writeJSON(w, map[string]any{"type": "error", "error": map[string]any{"type": "authentication_error", "message": "invalid x-api-key"}})
			return
		}
		in := readJSON(t, r)
		if in["model"] != "claude-opus-5" || in["temperature"] != nil {
			t.Errorf("body %v", in)
		}
		stop := "end_turn"
		text := "Rin leaps."
		if msgs := in["messages"].([]any); strings.Contains(msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string), "refuse") {
			stop, text = "refusal", ""
		}
		writeJSON(w, map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5", "stop_reason": stop,
			"content": []any{map[string]any{"type": "text", "text": text}}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 4}})
	}))
	defer srv.Close()
	a := Anthropic{Env: testEnv()}
	temp := 0.3
	out, err := a.Text(context.Background(), Config{BaseURL: srv.URL, APIKey: "sk-ant-test"},
		TextRequest{Model: "claude-opus-5", System: "Write manga dialogue.", Messages: []Message{{Role: "user", Content: "Rin jumps"}}, Temperature: &temp})
	if err != nil || out.Text != "Rin leaps." || out.TokensIn != 20 {
		t.Fatalf("%+v %v", out, err)
	}
	if err != nil {
		t.Fatalf("%+v %v", out, err)
	}
	_, err = a.Text(context.Background(), Config{BaseURL: srv.URL, APIKey: "sk-ant-test"}, TextRequest{Model: "claude-opus-5", Messages: []Message{{Role: "user", Content: "refuse"}}})
	if CodeOf(err) != CodeSafety {
		t.Fatalf("refusal: %v", err)
	}
	_, err = a.Text(context.Background(), Config{BaseURL: srv.URL, APIKey: "bad"}, TextRequest{Model: "claude-opus-5", Messages: []Message{{Role: "user", Content: "x"}}})
	if CodeOf(err) != CodeAuth {
		t.Fatalf("auth: %v", err)
	}
}

const testWorkflow = `{
 "3": {"class_type": "KSampler", "inputs": {"seed": 1, "steps": 20, "cfg": 7, "model": ["4", 0], "positive": ["6", 0], "negative": ["7", 0], "latent_image": ["5", 0]}},
 "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "illustrious.safetensors"}},
 "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 512, "height": 512, "batch_size": 1}},
 "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "", "clip": ["4", 1]}, "_meta": {"title": "Positive"}},
 "7": {"class_type": "CLIPTextEncode", "inputs": {"text": "", "clip": ["4", 1]}, "_meta": {"title": "Negative prompt"}},
 "12": {"class_type": "LoadImage", "inputs": {"image": "ref.png"}, "_meta": {"title": "Reference"}},
 "15": {"class_type": "LoadImage", "inputs": {"image": "pose.png"}, "_meta": {"title": "OpenPose"}},
 "9": {"class_type": "SaveImage", "inputs": {"filename_prefix": "mv", "images": ["8", 0]}}
}`

func TestComfyUIWorkflow(t *testing.T) {
	wf, err := ParseWorkflow([]byte(testWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.Nodes) != 8 || wf.Nodes[0].ID != "3" || wf.Nodes[0].Label() != "KSampler #3" {
		t.Fatalf("nodes %+v", wf.Nodes)
	}
	m := wf.GuessMapping()
	for field, want := range map[string]string{MapPrompt: "6.text", MapNegative: "7.text", MapSeed: "3.seed", MapWidth: "5.width",
		MapBatch: "5.batch_size", MapRef: "12.image", MapPose: "15.image", MapSteps: "3.steps"} {
		if m[field].String() != want {
			t.Errorf("guess %s = %s, want %s", field, m[field], want)
		}
	}
	if !wf.Has(ParseNodeInput("6.text")) || wf.Has(ParseNodeInput("6.clip")) {
		t.Fatal("Has")
	}
	if _, err := ParseWorkflow([]byte(`{"nodes": [{"id": 1}], "links": []}`)); err == nil || !strings.Contains(err.Error(), "API Format") {
		t.Fatalf("editor format: %v", err)
	}

	pngData := tinyPNG(t, 2, 2)
	var submitted map[string]map[string]any
	var histPolls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload/image":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			f := r.MultipartForm.File["image"][0]
			writeJSON(w, map[string]any{"name": f.Filename, "subfolder": "mangaveyra", "type": "input"})
		case "/prompt":
			var body struct {
				Prompt map[string]map[string]any `json:"prompt"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			submitted = body.Prompt
			writeJSON(w, map[string]any{"prompt_id": "abc", "number": 1, "node_errors": map[string]any{}})
		case "/history/abc":
			if histPolls.Add(1) < 2 {
				writeJSON(w, map[string]any{})
				return
			}
			writeJSON(w, map[string]any{"abc": map[string]any{"status": map[string]any{"status_str": "success", "completed": true},
				"outputs": map[string]any{"9": map[string]any{"images": []any{
					map[string]any{"filename": "mv_0001.png", "subfolder": "", "type": "output"},
					map[string]any{"filename": "mv_0002.png", "subfolder": "", "type": "output"},
					map[string]any{"filename": "preview.png", "subfolder": "", "type": "temp"}}}}}})
		case "/view":
			if r.URL.Query().Get("type") != "output" {
				t.Errorf("view %v", r.URL.Query())
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	opts, _ := json.Marshal(ComfyOptions{Workflow: json.RawMessage(testWorkflow), Mapping: m})
	seed := int64(42)
	ref := Image{Data: pngData, MIME: "image/png"}
	res, err := ComfyUI{Env: testEnv()}.Image(context.Background(), Config{BaseURL: srv.URL, Options: opts},
		ImageRequest{Model: "workflow", Prompt: "courier on a rooftop", Negative: "blurry", Width: 700, Height: 1000, N: 2, Seed: &seed, Refs: []Image{ref}, PoseImage: &ref}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 2)
	in := func(node, key string) any { return submitted[node]["inputs"].(map[string]any)[key] }
	if !strings.HasPrefix(in("6", "text").(string), "courier on a rooftop") || in("7", "text") != "blurry" || in("3", "seed").(float64) != 42 ||
		in("5", "width").(float64) != 696 || in("5", "batch_size").(float64) != 2 || !strings.HasPrefix(in("12", "image").(string), "mangaveyra/reference-") ||
		!strings.HasPrefix(in("15", "image").(string), "mangaveyra/pose-") {
		t.Fatalf("submitted graph %v", submitted)
	}
	if _, ok := in("6", "clip").([]any); !ok {
		t.Fatal("links must be kept")
	}
}

func TestGenericHTTPSyncAndPoll(t *testing.T) {
	pngData := tinyPNG(t, 2, 2)
	var srvURL string
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync":
			if r.Header.Get("X-Key") != "k1" {
				w.WriteHeader(401)
				return
			}
			in := readJSON(t, r)
			if in["prompt"] != "a \"quoted\" line" || in["w"].(float64) != 640 {
				t.Errorf("sync body %v", in)
			}
			writeJSON(w, map[string]any{"result": map[string]any{"images": []any{b64(pngData)}}, "billing": map[string]any{"usd": 0.02}})
		case "/jobs":
			writeJSON(w, map[string]any{"job": map[string]any{"id": "j9"}})
		case "/jobs/j9":
			if polls.Add(1) < 2 {
				writeJSON(w, map[string]any{"state": "running"})
				return
			}
			writeJSON(w, map[string]any{"state": "done", "output": []any{map[string]any{"url": srvURL + "/img.png"}}})
		case "/img.png":
			_, _ = w.Write(pngData)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	a := HTTPAdapter{Env: testEnv()}
	opts, _ := json.Marshal(HTTPOptions{AuthHeader: "X-Key", BodyTemplate: `{"prompt": "{{prompt}}", "w": {{width}}, "h": {{height}}}`,
		ResultPath: "result.images", ResultType: "base64", CostPath: "billing.usd"})
	res, err := a.Image(context.Background(), Config{BaseURL: srv.URL + "/sync", APIKey: "k1", Options: opts},
		ImageRequest{Model: "m", Prompt: `a "quoted" line`, Width: 640, Height: 960}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	if res.CostUSD != 0.02 {
		t.Fatalf("cost %v", res.CostUSD)
	}
	opts, _ = json.Marshal(HTTPOptions{BodyTemplate: `{"p": "{{prompt}}"}`, ResultPath: "output", Mode: "poll", IDPath: "job.id",
		PollURL: srv.URL + "/jobs/{{id}}", StatusPath: "state", DoneValues: []string{"done"}})
	res, err = a.Image(context.Background(), Config{BaseURL: srv.URL + "/jobs", Options: opts}, ImageRequest{Prompt: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 1)
	if _, err := ParseHTTPOptions(json.RawMessage(`{"body_template": "{\"a\": {{width}", "result_path": "x"}`)); err == nil {
		t.Fatal("broken template accepted")
	}
	if v, ok := JSONPath(map[string]any{"a": []any{map[string]any{"b": "c"}}}, "$.a[0].b"); !ok || v != "c" {
		t.Fatalf("path %v", v)
	}
}

func TestPlaceholder(t *testing.T) {
	p := Placeholder{}
	res, err := p.Image(context.Background(), Config{}, ImageRequest{Prompt: "Rin on the rooftop", Width: 400, Height: 1400, N: 2, Label: "p.7 panel 3"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPNG(t, res, 2)
	cfg, _ := png.DecodeConfig(bytes.NewReader(res.Images[0].Data))
	if cfg.Width != 400 || cfg.Height != 1400 {
		t.Fatalf("size %dx%d", cfg.Width, cfg.Height)
	}
	again, _ := p.Image(context.Background(), Config{}, ImageRequest{Prompt: "Rin on the rooftop", Width: 400, Height: 1400, N: 2, Label: "p.7 panel 3"}, nil)
	if !bytes.Equal(again.Images[1].Data, res.Images[1].Data) {
		t.Fatal("placeholder art must be deterministic")
	}
	src := tinyPNG(t, 20, 10)
	maskImg := image.NewGray(image.Rect(0, 0, 20, 10))
	for x := 0; x < 10; x++ {
		for y := 0; y < 10; y++ {
			maskImg.SetGray(x, y, color.Gray{255})
		}
	}
	var mb bytes.Buffer
	_ = png.Encode(&mb, maskImg)
	res, err = p.Image(context.Background(), Config{}, ImageRequest{Task: "inpaint", Prompt: "x", Image: &Image{Data: src}, Mask: &Image{Data: mb.Bytes()}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := png.Decode(bytes.NewReader(res.Images[0].Data))
	orig, _ := png.Decode(bytes.NewReader(src))
	if out.At(19, 0) != orig.At(19, 0) {
		t.Fatal("pixels outside the mask must be untouched")
	}
	res, err = p.Image(context.Background(), Config{}, ImageRequest{Task: "upscale", Image: &Image{Data: src}, Scale: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ = png.DecodeConfig(bytes.NewReader(res.Images[0].Data))
	if cfg.Width != 40 {
		t.Fatalf("upscale width %d", cfg.Width)
	}
	txt, _ := p.Text(context.Background(), Config{}, TextRequest{Messages: []Message{{Role: "user", Content: "Outline chapter 3"}}})
	txt2, _ := p.Text(context.Background(), Config{}, TextRequest{Messages: []Message{{Role: "user", Content: "Outline chapter 3"}}})
	if txt.Text != txt2.Text || !strings.Contains(txt.Text, PlaceholderLabel) {
		t.Fatalf("text %q", txt.Text)
	}
	tr, _ := p.Text(context.Background(), Config{}, TextRequest{Task: "translate", Lang: "ja", Messages: []Message{{Role: "user", Content: "Hello"}}})
	if !strings.Contains(tr.Text, "ja") {
		t.Fatalf("translate %q", tr.Text)
	}
	js, _ := p.Text(context.Background(), Config{}, TextRequest{JSON: true, Messages: []Message{{Role: "user", Content: "x"}}})
	if !json.Valid([]byte(js.Text)) {
		t.Fatalf("json %q", js.Text)
	}
}

func TestErrorsAndRegistry(t *testing.T) {
	cases := map[int]string{401: CodeAuth, 403: CodeAuth, 429: CodeRateLimit, 504: CodeTimeout, 500: CodeUnavailable, 422: CodeBadRequest}
	for status, want := range cases {
		if got := statusError(status, []byte(`{"error":{"message":"x"}}`)).Code; got != want {
			t.Errorf("%d → %s, want %s", status, got, want)
		}
	}
	if statusError(400, []byte(`{"detail":"prompt flagged by moderation"}`)).Code != CodeSafety {
		t.Fatal("safety detection")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, "{}")
	}))
	defer slow.Close()
	env := Env{HTTP: &http.Client{Timeout: 20 * time.Millisecond}}
	err := env.call(context.Background(), http.MethodGet, slow.URL, nil, nil, nil)
	if CodeOf(err) != CodeTimeout {
		t.Fatalf("timeout: %v", err)
	}
	r := NewRegistry(testEnv())
	if !r.Can(KindAnthropic, "text") || r.Can(KindAnthropic, "image") || !r.Can(KindFal, "media") || !r.Can(KindOpenRouter, "models") || !r.Can(KindLocalWorker, "image") {
		t.Fatal("capabilities")
	}
	if len(r.Kinds()) != 15 {
		t.Fatalf("kinds %v", r.Kinds())
	}
	if aspect(900, 1600) != "9:16" || aspect(1000, 1500) != "2:3" || aspect(100, 800) != "1:8" {
		t.Fatal("aspect")
	}
}
