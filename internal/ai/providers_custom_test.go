package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// ADR 0013 §9 custom connections: ComfyUI and generic HTTP servers on this
// machine (local httptest servers) and Hugging Face (fake transport).

const testComfyWorkflow = `{
 "3": {"class_type": "KSampler", "inputs": {"seed": 156680208700286, "steps": 20, "cfg": 8, "sampler_name": "euler", "scheduler": "normal", "denoise": 1,
   "model": ["4", 0], "positive": ["6", 0], "negative": ["7", 0], "latent_image": ["5", 0]}},
 "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "ink-koma-v3.safetensors"}},
 "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 512, "height": 512, "batch_size": 1}},
 "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "courier on a rooftop, screentone", "clip": ["4", 1]}},
 "7": {"class_type": "CLIPTextEncode", "inputs": {"text": "text, watermark", "clip": ["4", 1]}},
 "8": {"class_type": "VAEDecode", "inputs": {"samples": ["3", 0], "vae": ["4", 2]}},
 "9": {"class_type": "SaveImage", "inputs": {"filename_prefix": "ComfyUI", "images": ["8", 0]}},
 "12": {"class_type": "LoadImage", "inputs": {"image": "ref.png"}}
}`

func testComfyConfig(t *testing.T) comfyConfig {
	t.Helper()
	nodes, err := parseComfyWorkflow([]byte(testComfyWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	return comfyConfig{Workflow: json.RawMessage(testComfyWorkflow), WorkflowName: "ink-koma-v3.json", Map: detectComfyMap(nodes),
		Image: customImageSpec{MaxWidth: 1024, MaxHeight: 1024, MaxCount: 2, Commercial: true}}
}

func TestPhase4ComfyMapDetection(t *testing.T) {
	m := testComfyConfig(t).Map
	if m.Prompt != (comfyInput{"6", "text"}) || *m.Negative != (comfyInput{"7", "text"}) || *m.Seed != (comfyInput{"3", "seed"}) ||
		*m.Width != (comfyInput{"5", "width"}) || *m.Height != (comfyInput{"5", "height"}) || *m.Batch != (comfyInput{"5", "batch_size"}) ||
		*m.Reference != (comfyInput{"12", "image"}) || m.Pose != nil {
		t.Fatalf("detected map = %+v", m)
	}
}

func TestPhase4CustomConfigsFailClosed(t *testing.T) {
	good := testComfyConfig(t)
	enc := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	linkPrompt := good
	linkPrompt.Map.Prompt = comfyInput{"3", "positive"}
	noBatch := good
	noBatch.Map.Batch = nil
	uiFormat := good
	uiFormat.Workflow = json.RawMessage(`{"nodes": [], "links": []}`)
	freeHF := hfConfig{Endpoint: hfDedicated, Task: hfTaskText, Model: "acme/ink-8b", ContextTokens: 8192, OutputTokens: 1024, Modes: []string{ModeFreeOnly}}
	httpGood := httpConfig{Request: json.RawMessage(`{"prompt":"{{prompt}}","w":"{{width}}"}`), Mode: httpModeSync, ImagePath: "images.0", ImageKind: httpBase64, Image: defaultImageSpec}
	unknownPlaceholder := httpGood
	unknownPlaceholder.Request = json.RawMessage(`{"prompt":"{{prompt}}","k":"{{api_key}}"}`)
	noPrompt := httpGood
	noPrompt.Request = json.RawMessage(`{"w":"{{width}}"}`)
	pollMissing := httpGood
	pollMissing.Mode = httpModePoll
	foreignCheck := httpGood
	foreignCheck.CheckURL = "http://10.0.0.9:7000/health"
	for _, tc := range []struct {
		name, kind, base, raw string
		ok                    bool
	}{
		{"comfy good", providerKindComfyUI, "http://192.168.1.40:8188", enc(good), true},
		{"comfy metadata host", providerKindComfyUI, "http://169.254.169.254:8188", enc(good), false},
		{"comfy path", providerKindComfyUI, "http://192.168.1.40:8188/api", enc(good), false},
		{"comfy link input", providerKindComfyUI, "http://192.168.1.40:8188", enc(linkPrompt), false},
		{"comfy count without batch", providerKindComfyUI, "http://192.168.1.40:8188", enc(noBatch), false},
		{"comfy UI format", providerKindComfyUI, "http://192.168.1.40:8188", enc(uiFormat), false},
		{"comfy unknown field", providerKindComfyUI, "http://192.168.1.40:8188", strings.Replace(enc(good), `"modes"`, `"extra"`, 1)[:len(enc(good))-1] + `,"extra":1}`, false},
		{"hf serverless good", providerKindHuggingFace, hfRouterBase, enc(testHFTextConfig()), true},
		{"hf serverless foreign base", providerKindHuggingFace, "https://example.com/v1", enc(testHFTextConfig()), false},
		{"hf serverless needs provider", providerKindHuggingFace, hfRouterBase, enc(hfConfig{Endpoint: hfServerless, Task: hfTaskText, Model: "Qwen/Qwen3-32B", ContextTokens: 8192, OutputTokens: 1024}), false},
		{"hf serverless image", providerKindHuggingFace, hfRouterBase, enc(hfConfig{Endpoint: hfServerless, Task: hfTaskImage, Model: "a/b:fal-ai", Image: defaultImageSpec}), false},
		{"hf dedicated good", providerKindHuggingFace, "https://j3z5luu0ooo76jnl.us-east-1.aws.endpoints.huggingface.cloud", enc(hfConfig{Endpoint: hfDedicated, Task: hfTaskImage, Model: "acme/ink-xl", Image: defaultImageSpec}), true},
		{"hf dedicated foreign host", providerKindHuggingFace, "https://evil.example.com", enc(hfConfig{Endpoint: hfDedicated, Task: hfTaskImage, Model: "acme/ink-xl", Image: defaultImageSpec}), false},
		{"hf dedicated http", providerKindHuggingFace, "http://x.endpoints.huggingface.cloud", enc(hfConfig{Endpoint: hfDedicated, Task: hfTaskImage, Model: "acme/ink-xl", Image: defaultImageSpec}), false},
		{"hf free only", providerKindHuggingFace, "https://x.endpoints.huggingface.cloud", enc(freeHF), false},
		{"http good", providerKindHTTP, "http://10.0.0.8:7000/generate", enc(httpGood), true},
		{"http unknown placeholder", providerKindHTTP, "http://10.0.0.8:7000/generate", enc(unknownPlaceholder), false},
		{"http no prompt", providerKindHTTP, "http://10.0.0.8:7000/generate", enc(noPrompt), false},
		{"http poll without status", providerKindHTTP, "http://10.0.0.8:7000/generate", enc(pollMissing), false},
		{"http foreign check url", providerKindHTTP, "http://10.0.0.8:7000/generate", enc(foreignCheck), false},
	} {
		if err := validateCustomConfig(tc.kind, tc.base, tc.raw); (err == nil) != tc.ok {
			t.Errorf("%s: validate = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

// fakeComfy is a ComfyUI server on loopback with the documented routes.
type fakeComfy struct {
	mu        sync.Mutex
	submitted map[string]comfyNode
	uploads   int
	partner   bool
	calls     atomic.Int64
	polls     int
	png       []byte
}

func (f *fakeComfy) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/system_stats":
			_ = json.NewEncoder(w).Encode(map[string]any{"system": map[string]any{"comfyui_version": "0.44.0"}, "devices": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/object_info":
			spec := func(inputs ...string) map[string]any {
				req := map[string]any{}
				for _, in := range inputs {
					req[in] = []any{"STRING"}
				}
				return map[string]any{"input": map[string]any{"required": req}, "api_node": false}
			}
			info := map[string]any{
				"KSampler":               spec("seed", "steps", "cfg", "sampler_name", "scheduler", "denoise", "model", "positive", "negative", "latent_image"),
				"CheckpointLoaderSimple": spec("ckpt_name"), "EmptyLatentImage": spec("width", "height", "batch_size"),
				"CLIPTextEncode": spec("text", "clip"), "VAEDecode": spec("samples", "vae"), "SaveImage": spec("images", "filename_prefix"),
				"LoadImage": spec("image"),
			}
			if f.partner {
				info["SaveImage"] = map[string]any{"input": map[string]any{"required": map[string]any{"images": []any{}}}, "api_node": true}
			}
			_ = json.NewEncoder(w).Encode(info)
		case r.Method == http.MethodPost && r.URL.Path == "/upload/image":
			if err := r.ParseMultipartForm(8 << 20); err != nil || r.FormValue("type") != "input" || r.FormValue("subfolder") != comfyUploadDir {
				t.Errorf("upload form = %v %v", err, r.MultipartForm)
			}
			f.uploads++
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "ref-1.png", "subfolder": comfyUploadDir, "type": "input"})
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			var body struct {
				Prompt   map[string]comfyNode `json:"prompt"`
				ClientID string               `json:"client_id"`
				Extra    any                  `json:"extra_data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ClientID != comfyClientID || body.Extra != nil {
				t.Errorf("prompt body = %v %+v", err, body)
			}
			f.submitted = body.Prompt
			_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": "6f4b8f9e-1d2c-4b1a-9c3d-2e5f6a7b8c9d", "number": 1, "node_errors": map[string]any{}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/history/"):
			f.polls++
			if f.polls == 1 {
				_, _ = io.WriteString(w, `{}`)
				return
			}
			images := []any{map[string]string{"filename": "ComfyUI_00001_.png", "subfolder": "", "type": "output"},
				map[string]string{"filename": "ComfyUI_00002_.png", "subfolder": "", "type": "output"}}
			_ = json.NewEncoder(w).Encode(map[string]any{"6f4b8f9e-1d2c-4b1a-9c3d-2e5f6a7b8c9d": map[string]any{
				"outputs": map[string]any{"9": map[string]any{"images": images}},
				"status":  map[string]any{"status_str": "success", "completed": true}}})
		case r.Method == http.MethodGet && r.URL.Path == "/view":
			if r.URL.Query().Get("type") != "output" || !strings.HasPrefix(r.URL.Query().Get("filename"), "ComfyUI_") {
				t.Errorf("view query = %v", r.URL.Query())
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(f.png)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func allowPrivate(t *testing.T, m *Module, allow bool) {
	t.Helper()
	if err := m.private.Set(context.Background(), allow, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPhase4ComfyUIRunsTheWorkflowOnAPrivateServer(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake := &fakeComfy{png: phase4PNG(t, 512, 512)}
		server := httptest.NewServer(fake.handler(t))
		t.Cleanup(server.Close)
		m, f := imageTestModule(t, d, &routeDoer{})
		m.image.pollEvery = time.Millisecond
		allowPrivate(t, m, true)
		cfg := testComfyConfig(t)
		view := f.createConfiguredProvider(t, m.providers, providerKindComfyUI, server.URL, "", cfg)

		// The connection test reads system_stats and object_info only.
		p, err := m.providers.ResolveDispatch(ctx, f.userID, view.ID)
		if err != nil {
			t.Fatal(err)
		}
		res, err := checkCustomConnection(ctx, m.providers, m.text.localDoer, nil, p)
		if err != nil || res.Nodes != 8 || fake.submitted != nil {
			t.Fatalf("test = %+v %v", res, err)
		}
		fake.partner = true
		if res, err := checkCustomConnection(ctx, m.providers, m.text.localDoer, nil, p); err == nil || len(res.Partner) != 1 {
			t.Fatalf("partner node accepted: %+v %v", res, err)
		}
		fake.partner = false

		for _, mode := range []string{ModeBalanced, ModeFreeOnly} {
			plan := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, RoutingMode: mode, Needs: ImageNeeds{Width: 600, Height: 400, Count: 2, References: 1}})
			if plan.ModelID != customModelPrefix+view.ID || plan.Remote || plan.Estimate.USD != 0 || plan.Executor != "go" {
				t.Fatalf("%s plan = %+v", mode, plan)
			}
		}
		plan := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 600, Height: 400, Count: 2, References: 1}})
		id := imageTestJob(t, m, f, plan)
		req := imageTestRequest(f, plan, id)
		req.Negative = "speech bubbles"
		req.Seed = 77
		req.References = []ImageRef{{Data: phase4PNG(t, 200, 200), ContentType: "image/png", Role: "reference"}}
		result, err := m.image.GenerateImage(ctx, req)
		if err != nil || len(result.Images) != 2 || result.Images[0].Width != 600 || result.Actual.USD != 0 {
			t.Fatalf("generate = %+v %v", result, err)
		}
		in := func(node, input string) any { return fake.submitted[node].Inputs[input] }
		if in("6", "text") != req.Prompt || in("7", "text") != "speech bubbles" || in("3", "seed") != float64(77) || in("5", "width") != float64(600) ||
			in("5", "height") != float64(400) || in("5", "batch_size") != float64(2) || in("12", "image") != comfyUploadDir+"/ref-1.png" ||
			in("4", "ckpt_name") != "ink-koma-v3.safetensors" || fake.uploads != 1 {
			t.Fatalf("submitted workflow = %+v uploads=%d", fake.submitted, fake.uploads)
		}
		var reservations int
		if err := d.Get(ctx, &reservations, `SELECT COUNT(*) FROM spend_reservations WHERE job_id = ?`, id); err != nil || reservations != 0 {
			t.Fatalf("a local server call reserved remote spend: %d %v", reservations, err)
		}
		if name, provider := m.RouteNames(ctx, plan.ModelID, plan.ProviderID); name != view.Name || provider != "Local" && provider != "local" && !strings.Contains(strings.ToLower(provider), "local") {
			t.Fatalf("route names = %q %q", name, provider)
		}

		// Turning private endpoints off stops the next call before it dials.
		before := fake.calls.Load()
		allowPrivate(t, m, false)
		id = imageTestJob(t, m, f, plan)
		_, err = m.image.GenerateImage(ctx, imageTestRequest(f, plan, id))
		if !isCode(err, CodeNoProvider) || fake.calls.Load() != before {
			t.Fatalf("private off = %v calls %d→%d", err, before, fake.calls.Load())
		}
		allowPrivate(t, m, true)
		// Local only (ADR 0007): the placeholder, and a queued plan is refused.
		updateInstance(t, f.settings, func(inst *settings.Instance) { inst.LocalOnly = true })
		if p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 600, Height: 400, Count: 1}}); p.ModelID != PlaceholderArt {
			t.Fatalf("local-only plan = %+v", p)
		}
		id = imageTestJob(t, m, f, plan)
		if _, err := m.image.GenerateImage(ctx, imageTestRequest(f, plan, id)); !isCode(err, CodeLocalOnly) || fake.calls.Load() != before {
			t.Fatalf("local-only dispatch = %v", err)
		}
	})
}

func TestPhase4CustomImageRoutingModesAndLicenceGuard(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &routeDoer{})
		cfg := testComfyConfig(t)
		cfg.Modes = []string{ModeBest}
		cfg.Image.Commercial = false
		view := f.createConfiguredProvider(t, m.providers, providerKindComfyUI, "http://192.168.1.40:8188", "", cfg)
		for mode, want := range map[string]string{ModeBest: customModelPrefix + view.ID, ModeBalanced: PlaceholderArt, ModeFreeOnly: PlaceholderArt} {
			if p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, RoutingMode: mode, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1}}); p.ModelID != want {
				t.Fatalf("%s chose %s, want %s", mode, p.ModelID, want)
			}
		}
		// A pin overrides the modes; the licence guard still refuses a
		// non-commercial workflow for a public series.
		pinned := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, RoutingMode: ModeFastest, PinProviderID: view.ID, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1}})
		if pinned.ModelID != customModelPrefix+view.ID {
			t.Fatalf("pinned plan = %+v", pinned)
		}
		if _, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, RoutingMode: ModeBest,
			PinProviderID: view.ID, PublicSeries: true, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1}}); !isCode(err, CodeNoProvider) {
			t.Fatalf("licence guard = %v", err)
		}
		// Larger than the described maximum, or more outputs: capabilities.
		if p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, RoutingMode: ModeBest, Needs: ImageNeeds{Width: 2048, Height: 512, Count: 1}}); p.ModelID != PlaceholderArt {
			t.Fatalf("oversized job chose %s", p.ModelID)
		}
	})
}

// fakeImageAPI is a generic image endpoint on loopback.
func fakeImageAPI(t *testing.T, png []byte, poll bool) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	bodies := []map[string]any{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/generate":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies = append(bodies, body)
			if r.Header.Get("Authorization") != "Bearer local-token" {
				t.Errorf("auth header = %q", r.Header.Get("Authorization"))
			}
			if poll {
				_, _ = io.WriteString(w, `{"job":{"status_url":"/jobs/1"}}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"images": []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}})
		case r.Method == http.MethodGet && r.URL.Path == "/jobs/1":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "done", "result": map[string]string{"url": "/files/1.png"}})
		case r.Method == http.MethodGet && r.URL.Path == "/files/1.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &bodies
}

func TestPhase4GenericHTTPTemplatesSyncAndPoll(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		png := phase4PNG(t, 512, 512)
		for _, poll := range []bool{false, true} {
			server, bodies := fakeImageAPI(t, png, poll)
			m, f := imageTestModule(t, d, &routeDoer{})
			m.image.pollEvery = time.Millisecond
			allowPrivate(t, m, true)
			cfg := httpConfig{Request: json.RawMessage(`{"input":{"prompt":"{{prompt}}","negative":"{{negative}}","size":["{{width}}","{{height}}"],"seed":"{{seed}}","note":"seed {{seed}} for {{prompt}}"}}`),
				Mode: httpModeSync, ImagePath: "images.0", ImageKind: httpBase64, CheckURL: "/health", Image: defaultImageSpec}
			if poll {
				cfg.Mode, cfg.StatusURLPath, cfg.StatusPath, cfg.Done, cfg.Failed = httpModePoll, "job.status_url", "state", "done", "failed"
				cfg.ImagePath, cfg.ImageKind = "result.url", httpURL
			}
			view := f.createConfiguredProvider(t, m.providers, providerKindHTTP, server.URL+"/generate", "local-token", cfg)
			p, _ := m.providers.ResolveDispatch(ctx, f.userID, view.ID)
			if _, err := checkCustomConnection(ctx, m.providers, m.text.localDoer, nil, p); err != nil {
				t.Fatalf("check = %v", err)
			}
			plan := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 640, Height: 480, Count: 1}})
			if plan.ModelID != customModelPrefix+view.ID {
				t.Fatalf("plan = %+v", plan)
			}
			id := imageTestJob(t, m, f, plan)
			req := imageTestRequest(f, plan, id)
			req.Prompt = `Rin says "hi", } ] {{seed}}`
			req.Negative = "speech bubbles"
			result, err := m.image.GenerateImage(ctx, req)
			if err != nil || len(result.Images) != 1 || result.Images[0].Width != 640 {
				t.Fatalf("poll=%v generate = %+v %v", poll, result, err)
			}
			input := (*bodies)[0]["input"].(map[string]any)
			size := input["size"].([]any)
			// Values are typed and inserted into the document: a prompt with
			// quotes, brackets or a placeholder cannot change its structure.
			if input["prompt"] != req.Prompt || input["negative"] != "speech bubbles" || size[0] != float64(640) || size[1] != float64(480) ||
				input["seed"] != float64(42) || input["note"] != `seed 42 for `+req.Prompt {
				t.Fatalf("rendered body = %v", input)
			}
			// The server connection is shared with everyone: remove it before
			// the next round's fixture routes.
			if _, err := d.Exec(ctx, `DELETE FROM providers WHERE id = ?`, view.ID); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestPhase4GenericHTTPRefusesForeignOrigins(t *testing.T) {
	png := phase4PNG(t, 64, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"url": "http://169.254.169.254/latest/meta-data", "status_url": "https://example.com/x"})
	}))
	t.Cleanup(server.Close)
	private := &privateEndpointPolicy{fallback: true}
	client := httpClient{doer: newLocalTextHTTPClient(private), base: server.URL + "/generate", poll: time.Millisecond}
	req := ImageGenerateRequest{Prompt: "a harbour", Width: 64, Height: 64, Count: 1}
	for _, cfg := range []httpConfig{
		{Request: json.RawMessage(`{"p":"{{prompt}}"}`), Mode: httpModeSync, ImagePath: "url", ImageKind: httpURL, Image: defaultImageSpec},
		{Request: json.RawMessage(`{"p":"{{prompt}}"}`), Mode: httpModePoll, StatusURLPath: "status_url", StatusPath: "s", Done: "ok", ImagePath: "url", ImageKind: httpURL, Image: defaultImageSpec},
	} {
		if _, err := httpImage(context.Background(), client, cfg, req); err == nil {
			t.Fatalf("foreign URL followed for %s", cfg.Mode)
		}
	}
	_ = png
	// The private-network client refuses a public address outright.
	public := httpClient{doer: newLocalTextHTTPClient(private), base: "http://93.184.216.34/generate"}
	if err := testHTTP(context.Background(), public, httpConfig{}); err == nil {
		t.Fatal("public address dialled through the private client")
	}
}

func TestPhase4HuggingFaceServerlessTariffAndDedicatedImages(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		in := testScriptInput()
		doer := (&routeDoer{}).
			on(http.MethodGet, hfWhoAmIURL, reply(200, map[string]any{"type": "user", "name": "harbour", "auth": map[string]any{"type": "access_token"}})).
			on(http.MethodGet, hfRouterBase+"/models/Qwen/Qwen3-32B", reply(200, map[string]any{"id": "Qwen/Qwen3-32B", "providers": []any{
				map[string]any{"provider": "novita", "status": "live", "pricing": map[string]any{"input": 0.1, "output": 0.2}, "supports_structured_output": false},
				map[string]any{"provider": "together", "status": "live", "context_length": 40960, "pricing": map[string]any{"input": 0.8, "output": 1.6}, "supports_structured_output": true},
			}})).
			on(http.MethodPost, hfRouterBase+"/chat/completions", func(*http.Request, []byte) (*http.Response, error) {
				return chatOK(string(testScriptDocument(t, in)), 3000, 1000), nil
			})
		m, f := imageTestModule(t, d, doer)
		m.text.hfDoer = doer
		m.image.hfDoer = doer
		cfg := testHFTextConfig()
		cfg.Priced, cfg.InputNano, cfg.OutputNano = false, 0, 0
		view := f.createConfiguredProvider(t, m.providers, providerKindHuggingFace, hfRouterBase, "hf_token", cfg)
		base := TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: f.userID, ProjectID: f.projectID, Input: testScriptInputJSON(t)}
		// Without a listed tariff the connection does not route.
		if plan, err := m.text.PlanText(ctx, base); err != nil || plan.Remote {
			t.Fatalf("unpriced plan = %+v %v", plan, err)
		}
		p, _ := m.providers.ResolveDispatch(ctx, f.userID, view.ID)
		if _, err := checkCustomConnection(ctx, m.providers, nil, doer, p); err != nil {
			t.Fatalf("check = %v", err)
		}
		plan, err := m.text.PlanText(ctx, base)
		// 32,768 × $0.80/M + 4,096 × $1.60/M = $0.0327680 (frozen from the listing).
		if err != nil || !plan.Remote || plan.ProviderID != view.ID || plan.ModelID != "Qwen/Qwen3-32B:together" || math.Abs(plan.Estimate.USD-2*0.0327680) > 1e-12 {
			t.Fatalf("priced plan = %+v %v", plan, err)
		}
		jobID := f.seedRunningModelJob(t, view.ID, plan.ModelID)
		if _, err := m.text.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: f.userID, ProjectID: f.projectID, JobID: jobID, Attempt: 1,
			Input: testScriptInputJSON(t)}); err != nil {
			t.Fatal(err)
		}
		var row struct {
			Estimate int64 `db:"estimate_microusd"`
			Actual   int64 `db:"actual_microusd"`
		}
		if err := d.Get(ctx, &row, `SELECT estimate_microusd, actual_microusd FROM spend_reservations WHERE job_id = ?`, jobID); err != nil {
			t.Fatal(err)
		}
		// 3,000 × $0.80/M + 1,000 × $1.60/M = $0.004.
		if row.Estimate != 32_768 || row.Actual != 4_000 {
			t.Fatalf("reservation = %+v", row)
		}
		for _, r := range doer.requests() {
			if r.Header.Get("Authorization") != "Bearer hf_token" {
				t.Fatalf("request %s without the token", r.URL)
			}
		}

		// A dedicated image endpoint: remote, paid per hour, zero per call.
		endpoint := "https://j3z5luu0ooo76jnl.us-east-1.aws.endpoints.huggingface.cloud"
		png := phase4PNG(t, 512, 512)
		doer.on(http.MethodPost, endpoint, replyBytes(200, "image/png", png))
		image := f.createConfiguredProvider(t, m.providers, providerKindHuggingFace, endpoint, "hf_token",
			hfConfig{Endpoint: hfDedicated, Task: hfTaskImage, Model: "acme/ink-xl", Image: customImageSpec{MaxWidth: 1024, MaxHeight: 1024, MaxCount: 1, Commercial: true}})
		if p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, RoutingMode: ModeFreeOnly, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1}}); p.ModelID != PlaceholderArt {
			t.Fatalf("free-only used a paid endpoint: %+v", p)
		}
		ip := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, PinProviderID: image.ID, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1}})
		if !ip.Remote || ip.Estimate.USD != 0 || ip.ModelID != customModelPrefix+image.ID {
			t.Fatalf("image plan = %+v", ip)
		}
		id := imageTestJob(t, m, f, ip)
		ir := imageTestRequest(f, ip, id)
		ir.Negative = "lettering"
		if result, err := m.image.GenerateImage(ctx, ir); err != nil || len(result.Images) != 1 {
			t.Fatalf("image = %+v %v", result, err)
		}
		last := doer.requests()[len(doer.requests())-1]
		body := last.json(t)
		params, _ := body["parameters"].(map[string]any)
		if last.URL != endpoint || last.Header.Get("Accept") != "image/png" || body["inputs"] != ir.Prompt || params["negative_prompt"] != "lettering" || params["width"] != float64(512) {
			t.Fatalf("endpoint request = %s %v", last.URL, body)
		}
		var state string
		if err := d.Get(ctx, &state, `SELECT state FROM spend_reservations WHERE job_id = ? AND estimate_microusd = 0 AND actual_microusd = 0`, id); err != nil || state != "reconciled" {
			t.Fatalf("zero reservation = %q %v", state, err)
		}
		// Local only blocks Hugging Face like every remote route.
		updateInstance(t, f.settings, func(inst *settings.Instance) { inst.LocalOnly = true })
		id = imageTestJob(t, m, f, ip)
		if _, err := m.image.GenerateImage(ctx, imageTestRequest(f, ip, id)); !isCode(err, CodeLocalOnly) {
			t.Fatalf("local-only = %v", err)
		}
	})
}
