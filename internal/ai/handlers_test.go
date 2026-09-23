package ai_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestModelsPageAndMobileLayout(t *testing.T) {
	ta, _ := newApp(t)
	c := ta.Login(t, "admin")
	r := c.Get("/models")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /models: %d", r.StatusCode)
	}
	contains(t, r, "Models &amp; providers", "Everything auto-routing can use on", "Add provider", "Add custom",
		"Built-in placeholder", "Always on · no AI", "No worker online", "Image 1", "ROUTER USES IT FOR",
		"Routing mode", "Monthly remote limit", "Local only mode", `class="mv-mtable"`)
	all := c.Get("/models?kind=image&all=1")
	contains(t, all, "FLUX.2 [dev]", "Nano Banana Pro", "Not connected", "Not installed", "Show connected models only")
	txt := c.Get("/models?kind=text")
	contains(t, txt, "Built-in placeholder text")
	// Members see the page without admin controls.
	_, member := ta.UserClient(t, auth.RoleCreator)
	mr := member.Get("/models")
	contains(t, mr, "Models &amp; providers")
	lacks(t, mr, "Add custom", "/models/local-only")
	if r := ta.NewClient().Get("/models"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", r.StatusCode)
	}
	// Japanese.
	c.Header.Set("Accept-Language", "ja")
	defer c.Header.Del("Accept-Language")
	ja := c.Get("/models?lang=ja")
	if ja.StatusCode != http.StatusOK {
		t.Fatal(ja.StatusCode)
	}
}

func TestToggleModelLocalOnlyAndChanges(t *testing.T) {
	ta, svc := newApp(t)
	ctx := context.Background()
	c := ta.Login(t, "admin")
	r := htmx(c, "/models/models/placeholder-image/toggle", url.Values{})
	if r.StatusCode != http.StatusNoContent || !strings.Contains(r.Header.Get("HX-Trigger"), "mv:toast") {
		t.Fatalf("toggle off: %d %v", r.StatusCode, r.Header)
	}
	if !svc.Disabled(ctx)["placeholder-image"] {
		t.Fatal("model not disabled")
	}
	location(t, post(c, "/models/models/placeholder-image/toggle", url.Values{"enabled": {"on"}}), "/models")
	if svc.Disabled(ctx)["placeholder-image"] {
		t.Fatal("model not enabled again")
	}
	_, member := ta.UserClient(t, auth.RoleCreator)
	if r := post(member, "/models/models/placeholder-image/toggle", url.Values{}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member toggle: %d", r.StatusCode)
	}
	location(t, post(c, "/models/local-only", url.Values{"local_only": {"on"}}), "/models")
	if !ta.Deps.Settings.MustInstance(ctx).LocalOnly {
		t.Fatal("local only")
	}
	// A new manifest file shows up as a catalogue change for admins.
	dir := ta.Deps.Cfg.ManifestDir()
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "ink.yaml"), []byte("id: ink-koma\nname: Ink Koma\nmaker: Studio\nkind: image\n"+
		"capabilities: {tasks: [generate]}\nlicence: {id: mit, commercial: true}\nhardware: {local: true}\n"+
		"providers: [{provider: local_worker, model_id: ink-koma}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReloadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	r = c.Get("/models")
	contains(t, r, "Catalogue changes", "Ink Koma", "Added")
	location(t, post(c, "/models/changes/ack", nil), "/models")
	changes, _ := svc.ManifestChanges(ctx, 5)
	if len(changes) != 1 || changes[0].AckedAt == nil {
		t.Fatalf("changes %+v", changes)
	}
}

// fakeOpenRouter answers chat, image and model-list calls like OpenRouter.
func fakeOpenRouter(t *testing.T) *httptest.Server {
	pngData := tinyPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer or-secret-key-1234" {
			w.WriteHeader(401)
			writeJSON(w, map[string]any{"error": map[string]any{"message": "No auth credentials found"}})
			return
		}
		switch r.URL.Path {
		case "/api/v1/chat/completions":
			var in map[string]any
			_ = jsonDecode(r, &in)
			if mods, ok := in["modalities"].([]any); ok && len(mods) > 0 {
				writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "",
					"images": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + b64(pngData)}}}}}},
					"usage": map[string]any{"cost": 0.13}})
				return
			}
			writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "Rain drums on the rooftop as the courier leaps."}}},
				"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 12, "cost": 0.0004}})
		case "/api/v1/models":
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "google/gemini-3-pro-image-preview", "name": "Nano Banana Pro",
					"architecture": map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"image", "text"}},
					"pricing":      map[string]any{"prompt": "0.000002", "completion": "0.000012", "image": "0.134"}},
				map[string]any{"id": "studio/ink-model", "name": "Studio Ink",
					"architecture": map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"image"}},
					"pricing":      map[string]any{"image": "0.02"}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNamedProviderWizard(t *testing.T) {
	for _, dialect := range dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta, svc := newApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			srv := fakeOpenRouter(t)
			c := ta.Login(t, "admin")
			contains(t, c.Get("/models/providers/new"), "Add a provider", "OpenRouter", "ComfyUI workflow", "Local worker")
			location(t, c.Get("/models/providers/new?kind=openrouter&next=/setup/models"), "/models/providers/new/connect?kind=openrouter&next=/setup/models")
			contains(t, c.Get("/models/providers/new/connect?kind=openrouter"), "Connect OpenRouter", "Step 1 of 4 · Connect", `name="key"`, "Get a OpenRouter key")
			// Missing key.
			r := post(c, "/models/providers/new/connect", url.Values{"kind": {"openrouter"}})
			if r.StatusCode != http.StatusUnprocessableEntity && r.StatusCode != http.StatusSeeOther {
				t.Fatalf("connect without key: %d", r.StatusCode)
			}
			loc := location(t, post(c, "/models/providers/new/connect?next=/setup/models", url.Values{"kind": {"openrouter"}, "name": {"OpenRouter"},
				"base_url": {srv.URL + "/api/v1"}, "key": {"or-secret-key-1234"}}), "/models/providers/new/test?id=")
			id := strings.TrimPrefix(strings.Split(loc, "&")[0], "/models/providers/new/test?id=")
			p, err := svc.GetProvider(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if p.KeyEnc == "" || strings.Contains(p.KeyEnc, "or-secret") || p.KeyHint != "…1234" || !p.Config.V.Draft {
				t.Fatalf("stored provider %+v", p)
			}
			contains(t, c.Get(loc), "Step 2 of 4 · Test", "Test result", "Not run", "•••• …1234")
			// Test (HTMX fragment).
			r = htmx(c, "/models/providers/new/test?id="+id, url.Values{})
			contains(t, r, "Success", "Rain drums on the rooftop", "$0.0004")
			lacks(t, r, "<html")
			p, _ = svc.GetProvider(ctx, id)
			if !p.LastTest.V.OK || p.LastTest.V.CostUSD != 0.0004 {
				t.Fatalf("last test %+v", p.LastTest.V)
			}
			// Image test on request.
			location(t, post(c, "/models/providers/new/test?id="+id, url.Values{"mode": {"image"}}), "/models/providers/new/test")
			p, _ = svc.GetProvider(ctx, id)
			if p.LastTest.V.ImageKey == "" || p.LastTest.V.CostUSD != 0.13 {
				t.Fatalf("image test %+v", p.LastTest.V)
			}
			contains(t, c.Get("/models/providers/new/test?id="+id), "TEST IMAGE", "/media/")
			// Describe: catalogue models plus discovered ones.
			d := c.Get("/models/providers/new/describe?id=" + id)
			contains(t, d, "Catalogue models OpenRouter serves", "Nano Banana Pro", "Studio Ink", `value="studio/ink-model"`)
			location(t, post(c, "/models/providers/new/describe?id="+id, url.Values{"add_model": {"studio/ink-model"}}), "/models/providers/new/route?id="+id)
			if m, ok := svc.Catalog(ctx).Get("openrouter-studio-ink-model"); !ok || m.Kind != "image" || !m.Capabilities.Edit {
				t.Fatalf("discovered model not added: %+v", m)
			}
			contains(t, c.Get("/models/providers/new/route?id="+id), "joins the catalogue", "Finish")
			location(t, post(c, "/models/providers/new/route?id="+id+"&next=/setup/models", nil), "/setup/models")
			p, _ = svc.GetProvider(ctx, id)
			if p.Config.V.Draft || !p.Ready() {
				t.Fatalf("provider not finished: %+v", p.Config.V)
			}
			// The key never comes back.
			api := c.Get("/api/v1/ai/providers")
			lacks(t, api, "or-secret-key-1234", "key_enc")
			contains(t, api, `"key_hint":"…1234"`)
			// The G2 card and the setup hook show it as connected.
			contains(t, c.Get("/models"), "OpenRouter", "2 image · 0 text")
			contains(t, c.Get("/setup/models"), "Connected")
			// Remote spend from tests is on the ledger.
			if spent, _ := svc.MonthSpend(ctx, ""); spent < 0.13 {
				t.Fatalf("ledger %v", spent)
			}
		})
	}
}

func TestProviderURLGuards(t *testing.T) {
	ta := testutil.NewTestApp(t) // private addresses NOT allowed
	svc := core.MustUse[*ai.Service](ta.Deps)
	ctx := context.Background()
	c := ta.Login(t, "admin")
	// A remote kind may not point at loopback.
	r := post(c, "/models/providers/new/connect", url.Values{"kind": {"openai"}, "base_url": {"http://127.0.0.1:9/v1"}, "key": {"sk-x"}})
	contains(t, r, "private network")
	// Metadata addresses are always blocked, even for local runtimes.
	r = post(c, "/models/providers/new/connect", url.Values{"kind": {"ollama"}, "base_url": {"http://169.254.169.254/"}})
	if r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("metadata: %d", r.StatusCode)
	}
	// An admin connecting Ollama on the LAN approves the private address.
	loc := location(t, post(c, "/models/providers/new/connect", url.Values{"kind": {"ollama"}, "base_url": {"http://127.0.0.1:11434"}}), "/models/providers/new/test")
	id := strings.TrimPrefix(strings.Split(loc, "&")[0], "/models/providers/new/test?id=")
	p, _ := svc.GetProvider(ctx, id)
	if !p.Config.V.PrivateApproved || !p.Local() {
		t.Fatalf("ollama %+v", p.Config.V)
	}
	// Members cannot add providers until the admin allows it; then only for themselves.
	u, member := ta.UserClient(t, auth.RoleCreator)
	if r := member.Get("/models/providers/new"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member chooser: %d", r.StatusCode)
	}
	location(t, post(c, "/models/routing/guards", url.Values{"users_may_add": {"on"}, "licence_guard": {"on"}}), "/models/routing")
	r = post(member, "/models/providers/new/connect", url.Values{"kind": {"ollama"}, "base_url": {"http://127.0.0.1:11434"}})
	contains(t, r, "private network")
	r = post(member, "/models/providers/new/connect", url.Values{"kind": {"fal"}, "key": {"fal-member-key"}, "scope": {"server"}})
	loc = location(t, r, "/models/providers/new/test")
	mid := strings.TrimPrefix(strings.Split(loc, "&")[0], "/models/providers/new/test?id=")
	mp, _ := svc.GetProvider(ctx, mid)
	if mp.Scope != ai.ScopeUser || mp.Owner() != u.ID {
		t.Fatalf("member provider %+v", mp)
	}
	// Other members do not see it; the admin cannot see its key either.
	_, other := ta.UserClient(t, auth.RoleCreator)
	lacks(t, other.Get("/api/v1/ai/providers"), mid)
	if r := other.Get("/models/providers/new/test?id=" + mid); r.StatusCode != http.StatusNotFound {
		t.Fatalf("other member: %d", r.StatusCode)
	}
	lacks(t, c.Get("/api/v1/ai/providers"), "fal-member-key")
}

const workflow = `{
 "3": {"class_type": "KSampler", "inputs": {"seed": 1, "steps": 20, "model": ["4", 0], "positive": ["6", 0], "negative": ["7", 0], "latent_image": ["5", 0]}},
 "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "ink.safetensors"}},
 "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 512, "height": 512, "batch_size": 1}},
 "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "", "clip": ["4", 1]}},
 "7": {"class_type": "CLIPTextEncode", "inputs": {"text": "", "clip": ["4", 1]}, "_meta": {"title": "Negative"}},
 "12": {"class_type": "LoadImage", "inputs": {"image": "ref.png"}, "_meta": {"title": "Reference"}},
 "9": {"class_type": "SaveImage", "inputs": {"filename_prefix": "mv", "images": ["8", 0]}}
}`

func fakeComfy(t *testing.T) *httptest.Server {
	pngData := tinyPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload/image":
			writeJSON(w, map[string]any{"name": "x.png", "subfolder": "mangaveyra"})
		case "/prompt":
			writeJSON(w, map[string]any{"prompt_id": "p1", "node_errors": map[string]any{}})
		case "/history/p1":
			writeJSON(w, map[string]any{"p1": map[string]any{"status": map[string]any{"status_str": "success", "completed": true},
				"outputs": map[string]any{"9": map[string]any{"images": []any{map[string]any{"filename": "mv_1.png", "type": "output"}}}}}})
		case "/view":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestComfyUICustomWizard(t *testing.T) {
	ta, svc := newApp(t)
	ctx := context.Background()
	srv := fakeComfy(t)
	c := ta.Login(t, "admin")
	contains(t, c.Get("/models/providers/new/connect?kind=comfyui"), "Add a custom provider", "OpenAI-compatible", "ComfyUI workflow", "Hugging Face", "HTTP", `name="workflow"`)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("kind", "comfyui")
	_ = mw.WriteField("name", "Studio ComfyUI")
	_ = mw.WriteField("base_url", srv.URL)
	fw, _ := mw.CreateFormFile("workflow", "ink-koma-v3.json")
	_, _ = fw.Write([]byte(workflow))
	_ = mw.Close()
	r := c.Do(http.MethodPost, "/models/providers/new/connect", &body, mw.FormDataContentType())
	loc := location(t, r, "/models/providers/new/test?id=")
	id := strings.TrimPrefix(strings.Split(loc, "&")[0], "/models/providers/new/test?id=")
	page := c.Get(loc)
	contains(t, page, "Map inputs", "ink-koma-v3.json · 7 nodes", "CLIPTextEncode #6 · text", "LoadImage #12 · image", "Step 2 of 4 · Test")
	location(t, post(c, "/models/providers/new/mapping?id="+id, url.Values{"map_prompt": {"6.text"}, "map_negative": {"7.text"}, "map_seed": {"3.seed"},
		"map_reference": {"12.image"}, "map_width": {"5.width"}, "map_height": {"5.height"}, "map_pose": {"99.bogus"}}), "/models/providers/new/test")
	r = htmx(c, "/models/providers/new/test?id="+id, url.Values{})
	contains(t, r, "Success", "Local GPU")
	// Describe the custom model and route it.
	contains(t, c.Get("/models/providers/new/describe?id="+id), "Catalogue id", "Reference images", `value="studio-comfyui"`)
	r = post(c, "/models/providers/new/describe?id="+id, url.Values{"model_name": {"Ink Koma"}, "catalog_id": {"ink-koma"}, "model_kind": {"image"},
		"task": {"generate"}, "cap": {"refs", "bw_screentone"}, "max_refs": {"1"}, "licence_id": {"mit"}, "commercial": {"on"}})
	location(t, r, "/models/providers/new/route")
	m, ok := svc.Catalog(ctx).Get("ink-koma")
	if !ok || !m.Capabilities.Refs || !m.Licence.Commercial || m.Providers[0].ProviderID != id {
		t.Fatalf("custom model %+v", m)
	}
	location(t, post(c, "/models/providers/new/route?id="+id, url.Values{"pin_scope": {"server"}, "pin_task": {"generate"}}), "/models")
	pins, _ := svc.Pins(ctx)
	if len(pins) != 1 || pins[0].ModelID != "ink-koma" {
		t.Fatalf("pins %+v", pins)
	}
	// The pinned custom model is picked and runs through ComfyUI.
	res, err := svc.Run(ctx, nil, needGenerate(ta.Admin.ID), ai.Request{Prompt: "Pip the gull on a lamp post", Width: 512, Height: 768, N: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.ModelID != "ink-koma" || len(res.Images) != 1 || res.Cost.USD != 0 {
		t.Fatalf("run %+v", res)
	}
	// Bad workflow files are refused with a clear message.
	body.Reset()
	mw = multipart.NewWriter(&body)
	_ = mw.WriteField("kind", "comfyui")
	_ = mw.WriteField("base_url", srv.URL)
	fw, _ = mw.CreateFormFile("workflow", "ui.json")
	_, _ = fw.Write([]byte(`{"nodes": [{"id": 1}], "links": []}`))
	_ = mw.Close()
	r = c.Do(http.MethodPost, "/models/providers/new/connect", &body, mw.FormDataContentType())
	contains(t, r, "Save (API Format)")
}

func TestHTTPAndCompatibleCustomConnect(t *testing.T) {
	ta, _ := newApp(t)
	c := ta.Login(t, "admin")
	r := post(c, "/models/providers/new/connect", url.Values{"kind": {"http"}, "base_url": {"http://127.0.0.1:9/gen"}, "body_template": {`{"p": "{{prompt}}"`},
		"result_path": {"data"}})
	contains(t, r, "not valid JSON")
	location(t, post(c, "/models/providers/new/connect", url.Values{"kind": {"http"}, "base_url": {"http://127.0.0.1:9/gen"},
		"body_template": {`{"p": "{{prompt}}"}`}, "result_path": {"data.0.url"}, "mode": {"sync"}}), "/models/providers/new/test")
	location(t, post(c, "/models/providers/new/connect", url.Values{"kind": {"openai_compatible"}, "base_url": {"http://127.0.0.1:9/v1"},
		"model_id": {"qwen3:32b"}}), "/models/providers/new/test")
	contains(t, post(c, "/models/providers/new/connect", url.Values{"kind": {"huggingface"}}), "Enter the Hugging Face model id")
}

func TestRoutingPagePinsGuardsAndModes(t *testing.T) {
	ta, svc := newApp(t)
	ctx := context.Background()
	c := ta.Login(t, "admin")
	r := c.Get("/models/routing")
	contains(t, r, "Routing", "How jobs pick a model on this server", "Recent decisions", "No routing decisions yet", "Pins", "Guards",
		"Licence guard (non-commercial models on public series)", "Monthly remote spend limit · $10", "Learn from keep / reroll (v2)",
		"Users may add their own providers", `name="mode"`, "Balanced", "Free only")
	location(t, post(c, "/models/routing/mode", url.Values{"mode": {"fastest"}}), "/models/routing")
	if ta.Deps.Settings.MustInstance(ctx).RoutingMode != "fastest" {
		t.Fatal("instance mode")
	}
	if r := post(c, "/models/routing/mode", url.Values{"mode": {"warp"}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad mode: %d", r.StatusCode)
	}
	u, member := ta.UserClient(t, auth.RoleCreator)
	contains(t, member.Get("/models/routing"), "Your own mode", "Only admins can change guards.")
	location(t, post(member, "/models/routing/mode", url.Values{"mode": {"local_only"}}), "/models/routing")
	if svc.UserMode(ctx, u.ID) != "local_only" || ta.Deps.Settings.MustInstance(ctx).RoutingMode != "fastest" {
		t.Fatal("member override")
	}
	if r := post(member, "/models/routing/guards", url.Values{"learn": {"on"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member guards: %d", r.StatusCode)
	}
	// Guards.
	location(t, post(c, "/models/routing/guards", url.Values{"limit_on": {"on"}, "limit_usd": {"12.5"}, "user_limit_usd": {"5"}, "learn": {"on"}}), "/models/routing")
	inst := ta.Deps.Settings.MustInstance(ctx)
	if inst.Limits.ServerMonthlyRemoteSpendUSD != 12.5 || inst.Limits.MonthlyRemoteSpendUSD != 5 || inst.Content.LicenceGuard || !svc.Guards(ctx).Learn {
		t.Fatalf("guards %+v", inst.Limits)
	}
	contains(t, post(c, "/models/routing/guards", url.Values{"limit_usd": {"lots"}}), "Enter an amount")
	// Pins.
	location(t, post(c, "/models/routing/pins", url.Values{"scope_kind": {"series"}, "scope_id": {"glass-orchard"}, "label": {"Glass Orchard"},
		"mode": {"local_only"}}), "/models/routing")
	location(t, post(c, "/models/routing/pins", url.Values{"scope_kind": {"panel_type"}, "scope_id": {"cover"}, "model_id": {"nano-banana-pro"}}), "/models/routing")
	location(t, post(c, "/models/routing/pins", url.Values{"scope_kind": {"job_type"}, "scope_id": {"character.train"}, "model_id": {"illustrious-xl"}}), "/models/routing")
	contains(t, post(c, "/models/routing/pins", url.Values{"scope_kind": {"project"}, "model_id": {"illustrious-xl"}}), "Say which project")
	r = c.Get("/models/routing")
	contains(t, r, "Series “Glass Orchard”", "Local only", "Covers", "Nano Banana Pro", "Character Lock jobs", "Illustrious XL")
	pins, _ := svc.Pins(ctx)
	if len(pins) != 3 {
		t.Fatalf("pins %+v", pins)
	}
	if r := post(member, "/models/routing/pins", url.Values{"scope_kind": {"server"}, "model_id": {"illustrious-xl"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member pin: %d", r.StatusCode)
	}
	location(t, post(c, "/models/routing/pins/"+pins[0].ID+"/delete", nil), "/models/routing")
	if pins, _ := svc.Pins(ctx); len(pins) != 2 {
		t.Fatalf("pin not deleted: %d", len(pins))
	}
	// A decision shows with its reason.
	if _, err := svc.Run(ctx, nil, needGenerate(ta.Admin.ID), ai.Request{Prompt: "Rin on the pier", N: 1}); err != nil {
		t.Fatal(err)
	}
	contains(t, c.Get("/models/routing"), "Built-in placeholder · no AI", "No model connected yet")
}

func TestWorkerTokensAndLocalSet(t *testing.T) {
	ta, svc := newApp(t)
	ctx := context.Background()
	c := ta.Login(t, "admin")
	contains(t, c.Get("/models/workers/new"), "Local workers", "Add worker", "No worker has registered yet.")
	contains(t, post(c, "/models/workers/new", url.Values{"name": {"bad name!"}}), "Use letters, digits")
	r := post(c, "/models/workers/new", url.Values{"name": {"studio-pc"}})
	contains(t, r, "Token for studio-pc created", "mvw_", "MV_WORKER_TOKEN=mvw_", "docker run", "mangaveyra-worker")
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("token page must not be cached")
	}
	toks, _ := ta.Deps.Auth.ListTokens(ctx, "", auth.TokenWorker)
	if len(toks) != 1 {
		t.Fatalf("tokens %d", len(toks))
	}
	_, member := ta.UserClient(t, auth.RoleCreator)
	if r := member.Get("/models/workers/new"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member: %d", r.StatusCode)
	}
	// A registered 24 GB worker gets the recommended local set.
	if _, err := ta.Deps.Jobs.RegisterWorker(ctx, toks[0].ID, "10.0.0.2", jobs.Registration{Name: "studio-pc",
		GPUs: []jobs.GPU{{Name: "NVIDIA GeForce RTX 4090", VRAMTotalMB: 24576, VRAMFreeMB: 20000}}, JobTypes: []string{"image.generate"},
		Models: []string{"flux2-klein"}, MaxConcurrent: 1}); err != nil {
		t.Fatal(err)
	}
	contains(t, c.Get("/models/workers/new"), "RTX 4090 · 24 GB", "Online", "Qwen-Image Edit, FLUX.2 [klein], Illustrious XL")
	contains(t, c.Get("/models"), "RTX 4090 · 1 model")
	l := c.Get("/models/local")
	contains(t, l, "NVIDIA GeForce RTX 4090 · 24 GB detected", "Recommended", "Installed", "41 GB to download", "MV_MODELS=qwen-image-edit,flux2-klein,illustrious-xl")
	location(t, post(c, "/models/local", url.Values{"model": {"flux2-klein", "sdxl-base", "gpt-image-2"}}), "/models/local?saved=1")
	if g := svc.Guards(ctx); len(g.LocalModels) != 2 {
		t.Fatalf("local set %v", g.LocalModels)
	}
	location(t, post(c, "/models/workers/tokens/"+toks[0].ID+"/revoke", nil), "/models/workers/new")
	if toks, _ := ta.Deps.Auth.ListTokens(ctx, "", auth.TokenWorker); len(toks) != 0 {
		t.Fatal("token not revoked")
	}
}

func TestAPIRouteMeterAndDashboard(t *testing.T) {
	ta, svc := newApp(t)
	ctx := context.Background()
	c := ta.Login(t, "admin")
	r := c.PostJSON("/api/v1/ai/route", map[string]any{"task": "generate", "aspect": "2:3", "prompt": `The sign reads "NO ENTRY"`})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("route: %d %s", r.StatusCode, r.Body)
	}
	body := r.Map(t)
	ex := body["explanation"].(map[string]any)
	if ex["label"] != "Auto → Built-in placeholder · no AI" || ex["placeholder"] != true {
		t.Fatalf("explanation %v", ex)
	}
	if r := c.PostJSON("/api/v1/ai/estimate", map[string]any{"task": "text"}); r.StatusCode != http.StatusOK {
		t.Fatalf("estimate: %d", r.StatusCode)
	}
	mr := c.Get("/api/v1/ai/models?kind=image")
	contains(t, mr, `"id":"flux2-dev"`, `"available":false`, `"unavailable_reason"`)
	// Record remote spend and check the meter and the dashboard.
	if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO spend_ledger (id, user_id, kind, usd, month, created_at) VALUES ('01TESTLEDGER', ?, 'remote', 3.2, ?, CURRENT_TIMESTAMP)`,
		ta.Admin.ID, svc.Now().UTC().Format("2006-01")); err != nil {
		t.Fatal(err)
	}
	m := c.Get("/api/v1/ai/meter").Map(t)
	if m["month_usd"].(float64) != 3.2 || m["limit_usd"].(float64) != 20 {
		t.Fatalf("meter %v", m)
	}
	contains(t, c.Get("/studio"), "$3.20 of $20 this month · your own keys")
	// Pins and feedback over the API.
	pr := c.PostJSON("/api/v1/ai/pins", map[string]any{"scope_kind": "character", "scope_id": "rin", "model_id": "illustrious-xl", "task": "generate"})
	if pr.StatusCode != http.StatusCreated {
		t.Fatalf("pin: %d %s", pr.StatusCode, pr.Body)
	}
	if r := c.PostJSON("/api/v1/ai/pins", map[string]any{"scope_kind": "moon"}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad pin: %d", r.StatusCode)
	}
	if r := c.PostJSON("/api/v1/ai/feedback", map[string]any{"model_id": "flux2-klein", "task": "generate", "kept": true}); r.StatusCode != http.StatusNoContent {
		t.Fatalf("feedback: %d", r.StatusCode)
	}
	contains(t, c.Get("/api/v1/ai/pins"), "illustrious-xl")
	contains(t, c.Get("/api/v1/ai/decisions"), `"items"`)
	// Provider CRUD over the API.
	cr := c.PostJSON("/api/v1/ai/providers", map[string]any{"kind": "replicate", "key": "r8_abcdef"})
	if cr.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", cr.StatusCode, cr.Body)
	}
	id := cr.Map(t)["id"].(string)
	lacks(t, cr, "r8_abcdef")
	if r := c.SendJSON(http.MethodPatch, "/api/v1/ai/providers/"+id, map[string]any{"enabled": false}); r.StatusCode != http.StatusOK || r.Map(t)["enabled"] != false {
		t.Fatalf("patch: %d %s", r.StatusCode, r.Body)
	}
	if r := c.SendJSON(http.MethodDelete, "/api/v1/ai/providers/"+id, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if r := c.PostJSON("/api/v1/ai/providers", map[string]any{"kind": "fal"}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing key: %d", r.StatusCode)
	}
}
