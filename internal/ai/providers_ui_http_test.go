package ai_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// ADR 0013 §9 screens: G2's Add provider entries and rows for the Phase 4
// providers, and G12's ComfyUI, Hugging Face and HTTP types. No test here
// contacts a provider: key forms only save keys, local servers are httptest
// servers on loopback, and Hugging Face is exercised with Local only on.

func TestPhase4KeyFormProvidersConnectAndRender(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		for _, tc := range []struct{ kind, title, name, key string }{
			{"google", "Connect Google", "Gemini harbour", "AIza-google-secret-1"},
			{"xai", "Connect xAI", "Grok harbour", "xai-secret-2"},
			{"together", "Connect Together", "Together harbour", "tg-secret-3"},
			{"replicate", "Connect Replicate", "Replicate harbour", "r8_secret-4"},
			{"runware", "Connect Runware", "Runware harbour", "rw-secret-5"},
		} {
			page := admin.Get("/models/providers/new/" + tc.kind)
			if page.StatusCode != http.StatusOK {
				t.Fatalf("%s form = %d", tc.kind, page.StatusCode)
			}
			body := mainOf(t, page)
			mustContain(t, body, tc.title, `name="kind" value="`+tc.kind+`"`, `type="password"`)
			assertEveryTargetIsServed(t, ta, body)
			created := postModelsForm(admin, "/models/providers", url.Values{"kind": {tc.kind}, "name": {tc.name}, "key": {tc.key}, "scope": {"server"}})
			if created.StatusCode != http.StatusSeeOther {
				t.Fatalf("%s create = %d %.800s", tc.kind, created.StatusCode, created.Body)
			}
		}
		// Custom kinds are never created from the key form.
		if r := postModelsForm(admin, "/models/providers", url.Values{"kind": {"comfyui"}, "name": {"x"}, "key": {"k"}, "scope": {"server"}}); r.StatusCode != http.StatusNotFound {
			t.Fatalf("custom kind through the key form = %d", r.StatusCode)
		}
		if r := admin.Get("/models/providers/new?kind=huggingface"); r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/models/providers/new/connect?type=huggingface" {
			t.Fatalf("hugging face key form = %d %s", r.StatusCode, r.Header.Get("Location"))
		}
		text := mainOf(t, admin.Get("/models?cap=text"))
		mustAppearInOrder(t, text, "gemini-3.8-flash", "Fake-transport tested · not live-verified", "Google · Gemini harbour", "≤ $0.45 / call")
		mustAppearInOrder(t, text, "grok-4.20-0309-non-reasoning", "xAI · Grok harbour", "≤ $0.21 / call")
		mustAppearInOrder(t, text, "Qwen/Qwen3.5-9B", "Together · Together harbour", "≤ $0.03 / call")
		mustContain(t, text, `href="/models?cap=text" aria-current="true">Text 4</a>`)
		mustNotContain(t, text, "Replicate harbour", "Runware harbour")
		image := mainOf(t, admin.Get("/models?cap=image"))
		for _, row := range []string{"Nano Banana 2.1 (Google)", "Grok Imagine Image", "FLUX1.1 [pro] (Together)", "FLUX.1 schnell (Replicate)",
			"SDXL (Replicate)", "FLUX.1 schnell (Runware)"} {
			mustAppearInOrder(t, image, row, "Fake-transport tested · not live-verified")
		}
		mustContain(t, image, "Replicate harbour", "Runware harbour", "~$0.166 / img", "~$0.02 / img", "~$0.003 / img")
		for _, page := range []string{text, image} {
			mustNotContain(t, page, "AIza-google-secret-1", "xai-secret-2", "tg-secret-3", "r8_secret-4", "rw-secret-5")
		}
		// Local only pauses them like every remote route.
		updateModelsInstance(t, ta, func(inst *settings.Instance) { inst.LocalOnly = true })
		mustContain(t, mainOf(t, admin.Get("/models?cap=image")), "Paused by Local only")
	})
}

func multipartForm(t *testing.T, fields map[string]string, file, name string, data []byte) (io.Reader, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if file != "" {
		part, err := w.CreateFormFile(file, name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(data)
	}
	_ = w.Close()
	return &b, w.FormDataContentType()
}

const uiComfyWorkflow = `{
 "3": {"class_type": "KSampler", "inputs": {"seed": 1, "steps": 20, "cfg": 8, "sampler_name": "euler", "scheduler": "normal", "denoise": 1,
   "model": ["4", 0], "positive": ["6", 0], "negative": ["7", 0], "latent_image": ["5", 0]}},
 "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "ink-koma-v3.safetensors"}},
 "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 512, "height": 512, "batch_size": 1}},
 "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "courier on a rooftop, screentone", "clip": ["4", 1]}},
 "7": {"class_type": "CLIPTextEncode", "inputs": {"text": "text", "clip": ["4", 1]}},
 "8": {"class_type": "VAEDecode", "inputs": {"samples": ["3", 0], "vae": ["4", 2]}},
 "9": {"class_type": "SaveImage", "inputs": {"filename_prefix": "ComfyUI", "images": ["8", 0]}},
 "12": {"class_type": "LoadImage", "inputs": {"image": "ref.png"}}
}`

func TestPhase4G12ComfyUIWizard(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			var mu sync.Mutex
			seen := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Method+" "+r.URL.Path)
				mu.Unlock()
				switch r.URL.Path {
				case "/system_stats":
					_, _ = io.WriteString(w, `{"system":{"comfyui_version":"0.44.0"},"devices":[]}`)
				case "/object_info":
					info := map[string]any{}
					for class, inputs := range map[string][]string{"KSampler": {"seed", "steps", "cfg", "sampler_name", "scheduler", "denoise", "model", "positive", "negative", "latent_image"},
						"CheckpointLoaderSimple": {"ckpt_name"}, "EmptyLatentImage": {"width", "height", "batch_size"}, "CLIPTextEncode": {"text", "clip"},
						"VAEDecode": {"samples", "vae"}, "SaveImage": {"images", "filename_prefix"}, "LoadImage": {"image"}} {
						req := map[string]any{}
						for _, in := range inputs {
							req[in] = []any{"X"}
						}
						info[class] = map[string]any{"input": map[string]any{"required": req}}
					}
					_ = json.NewEncoder(w).Encode(info)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect), testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true"))
			admin := ta.Login(t, ta.Admin.Username)
			connect := mainOf(t, admin.Get("/models/providers/new/connect?type=comfyui"))
			mustContain(t, connect, `href="/models/providers/new/connect?type=comfyui" aria-current="true">ComfyUI workflow</a>`,
				`href="/models/providers/new/connect?type=huggingface"`, `href="/models/providers/new/connect?type=http"`,
				`enctype="multipart/form-data"`, `name="workflow"`, `name="type" value="comfyui"`, "ComfyUI server")
			mustNotContain(t, connect, "mv-soon")
			assertEveryTargetIsServed(t, ta, connect)

			body, ct := multipartForm(t, map[string]string{"type": "comfyui", "name": "Ink koma", "endpoint": server.URL, "scope": "server"},
				"workflow", "ink-koma-v3.json", []byte(uiComfyWorkflow))
			created := admin.Do(http.MethodPost, "/models/providers/new/connect", body, ct)
			if created.StatusCode != http.StatusSeeOther || !strings.Contains(created.Header.Get("Location"), "/test?id=") {
				t.Fatalf("connect = %d %.1500s", created.StatusCode, created.Body)
			}
			id := providerIDByName(t, ta, "Ink koma")
			mu.Lock()
			if strings.Join(seen, ",") != "GET /system_stats,GET /object_info" {
				t.Fatalf("the test did more than read metadata: %v", seen)
			}
			mu.Unlock()
			test := mainOf(t, admin.Get("/models/providers/new/test?id="+id))
			mustContain(t, test, "Step 2 of 4", `<span class="is-current">ComfyUI workflow</span>`, "ink-koma-v3.json · 8 nodes", "Map inputs",
				"<dt>Prompt</dt><dd>CLIPTextEncode #6 · text</dd>", "<dt>Reference image</dt><dd>LoadImage #12</dd>", "<dt>Seed</dt><dd>KSampler #3 · seed</dd>",
				"NODE CHECK · 8 nodes on the server", `<dd class="mv-aicustom__status--ok">Success</dd>`, "<dd>Local GPU</dd>", "Next: describe")
			// Back reopens step 1 with the map as editable choices.
			back := mainOf(t, admin.Get("/models/providers/new/connect?id="+id))
			mustContain(t, back, `name="map_prompt"`, `<option value="6.text" selected>CLIPTextEncode #6 · text</option>`, "ink-koma-v3.json · 8 nodes. Choose a file only to replace it.")
			if r := postModelsForm(admin, "/models/providers/new/test", url.Values{"id": {id}, "action": {"next"}}); r.StatusCode != http.StatusSeeOther {
				t.Fatalf("next = %d", r.StatusCode)
			}
			describe := mainOf(t, admin.Get("/models/providers/new/describe?id="+id))
			mustContain(t, describe, `name="max_width"`, `name="max_count"`, "Its model licence allows commercial use")
			if r := postModelsForm(admin, "/models/providers/new/describe", url.Values{"id": {id}, "name": {"Ink koma"}, "max_width": {"9000"}, "max_height": {"1024"}, "max_count": {"2"}}); r.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("oversized describe = %d", r.StatusCode)
			}
			if r := postModelsForm(admin, "/models/providers/new/describe", url.Values{"id": {id}, "name": {"Ink koma"}, "max_width": {"1024"}, "max_height": {"1536"}, "max_count": {"2"}, "commercial": {"true"}}); r.StatusCode != http.StatusSeeOther {
				t.Fatalf("describe = %d %.800s", r.StatusCode, r.Body)
			}
			route := mainOf(t, admin.Get("/models/providers/new/route?id="+id))
			mustContain(t, route, `name="modes" value="free_only" checked`, "Panels")
			done := postModelsForm(admin, "/models/providers/new/route", url.Values{"id": {id}, "modes": {"balanced", "free_only"}})
			if done.StatusCode != http.StatusSeeOther || done.Header.Get("Location") != "/models?cap=image" {
				t.Fatalf("route = %d %s", done.StatusCode, done.Header.Get("Location"))
			}
			image := mainOf(t, admin.Get("/models?cap=image"))
			mustAppearInOrder(t, image, ">Ink koma</strong>", "Fake-transport tested · not live-verified", "ComfyUI", "GPU time")
			var config string
			if err := ta.Deps.DB.Get(t.Context(), &config, `SELECT config FROM providers WHERE id = ?`, id); err != nil ||
				!strings.Contains(config, `"max_height":1536`) || !strings.Contains(config, `"commercial":true`) || !strings.Contains(config, `"modes":["balanced","free_only"]`) {
				t.Fatalf("config = %s %v", config, err)
			}
		})
	}
}

func TestPhase4G12HTTPWizard(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			var gets []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gets = append(gets, r.Method+" "+r.URL.Path)
				if r.URL.Path == "/health" {
					_, _ = io.WriteString(w, `{"ok":true}`)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect), testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true"))
			admin := ta.Login(t, ta.Admin.Username)
			form := mainOf(t, admin.Get("/models/providers/new/connect?type=http"))
			mustContain(t, form, `name="template"`, "{{prompt}}, {{negative}}", `name="image_path"`, `name="check_url"`)
			bad := postModelsForm(admin, "/models/providers/new/connect", url.Values{"type": {"http"}, "name": {"Local images"}, "endpoint": {server.URL + "/generate"},
				"template": {`{"prompt":"{{prompt}}","key":"{{api_key}}"}`}, "mode": {"sync"}, "image_path": {"images.0"}, "image_kind": {"base64"}, "scope": {"server"}})
			if bad.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(bad.Body), `id="custom-template-error"`) || len(gets) != 0 {
				t.Fatalf("unknown placeholder = %d gets=%v", bad.StatusCode, gets)
			}
			ok := postModelsForm(admin, "/models/providers/new/connect", url.Values{"type": {"http"}, "name": {"Local images"}, "endpoint": {server.URL + "/generate"},
				"template": {`{"prompt":"{{prompt}}","size":["{{width}}","{{height}}"]}`}, "mode": {"sync"}, "image_path": {"images.0"}, "image_kind": {"base64"},
				"check_url": {"/health"}, "scope": {"server"}})
			if ok.StatusCode != http.StatusSeeOther {
				t.Fatalf("connect = %d %.1500s", ok.StatusCode, ok.Body)
			}
			id := providerIDByName(t, ta, "Local images")
			if strings.Join(gets, ",") != "GET /health" {
				t.Fatalf("test requests = %v", gets)
			}
			mustContain(t, mainOf(t, admin.Get("/models/providers/new/test?id="+id)), "CHECK URL · answered", `<dd class="mv-aicustom__status--ok">Success</dd>`, "<dd>Local</dd>")
		})
	}
}

func TestPhase4G12HuggingFaceWizardWithoutNetwork(t *testing.T) {
	forEachModelsApp(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, ta.Admin.Username)
		// Local only: the draft is saved but nothing is tested or sent.
		updateModelsInstance(t, ta, func(inst *settings.Instance) { inst.LocalOnly = true })
		form := mainOf(t, admin.Get("/models/providers/new/connect?type=huggingface"))
		mustContain(t, form, `name="hf_endpoint"`, `value="serverless"`, `value="dedicated"`, `name="task"`, "Access token", `value="Qwen/Qwen3-32B:together"`)
		for _, tc := range []struct {
			values url.Values
			field  string
		}{
			{url.Values{"hf_endpoint": {"serverless"}, "task": {"text"}, "model": {"Qwen/Qwen3-32B"}, "key": {"hf_x"}}, "custom-model-error"},
			{url.Values{"hf_endpoint": {"dedicated"}, "task": {"image"}, "model": {"acme/ink-xl"}, "endpoint": {"https://evil.example.com"}, "key": {"hf_x"}}, "custom-endpoint-error"},
			{url.Values{"hf_endpoint": {"serverless"}, "task": {"text"}, "model": {"Qwen/Qwen3-32B:together"}}, "custom-key-error"},
		} {
			tc.values.Set("type", "huggingface")
			tc.values.Set("name", "HF")
			tc.values.Set("scope", "server")
			r := postModelsForm(admin, "/models/providers/new/connect", tc.values)
			if r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), `id="`+tc.field+`"`) {
				t.Fatalf("%v = %d, want %s", tc.values, r.StatusCode, tc.field)
			}
		}
		saved := postModelsForm(admin, "/models/providers/new/connect", url.Values{"type": {"huggingface"}, "name": {"HF serverless"}, "hf_endpoint": {"serverless"},
			"task": {"text"}, "model": {"Qwen/Qwen3-32B:together"}, "key": {"hf_secret_token"}, "scope": {"server"}})
		if saved.StatusCode != http.StatusSeeOther {
			t.Fatalf("save = %d %.1500s", saved.StatusCode, saved.Body)
		}
		id := providerIDByName(t, ta, "HF serverless")
		test := mainOf(t, admin.Get("/models/providers/new/test?id="+id))
		mustContain(t, test, `<span class="is-current">Hugging Face</span>`, "Token and model check", "Listed rate at the test", "Run test", "disabled")
		mustNotContain(t, test, "hf_secret_token")
	})
}
