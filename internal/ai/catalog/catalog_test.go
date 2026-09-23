package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedCatalogLoads(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Problems) > 0 {
		t.Fatalf("problems: %v", c.Problems)
	}
	want := []string{
		"gpt-image-2", "gpt-image-2.5", "nano-banana-pro", "nano-banana-2", "grok-imagine-image-2", "mai-image-2.5",
		"seedream-4.5", "flux2-pro", "flux2-flex", "flux2-dev", "flux2-klein", "qwen-image", "qwen-image-edit",
		"qwen-image-layered", "qwen-image-2", "qwen-image-2.1", "z-image-turbo", "hunyuan-image-3", "glm-image", "hidream-o1",
		"sd35-large", "sana", "illustrious-xl", "noobai-xl", "animagine-xl-4", "diffsensei",
		"claude-opus-5", "gpt-5", "gemini-3-pro", "llama-3.3-70b", "qwen3-32b",
		"veo-3.1", "kling-3-turbo", "seedance-2.5", "runway-gen-4.5", "wan-3",
		"kokoro-82m", "manga-ocr", "real-esrgan-anime", "placeholder-image", "placeholder-text",
	}
	for _, id := range want {
		m, ok := c.Get(id)
		if !ok {
			t.Errorf("missing %s", id)
			continue
		}
		if m.Source != SourceBuiltin {
			t.Errorf("%s source %q", id, m.Source)
		}
		for _, o := range m.Providers {
			if o.Remote() && o.Price.Unit != "" && !strings.Contains(o.Notes, "verify") {
				t.Errorf("%s/%s: remote costs must say 'verify before use'", id, o.Provider)
			}
		}
	}
	dev, _ := c.Get("flux2-dev")
	if dev.Licence.Commercial {
		t.Fatal("FLUX.2 [dev] must be non-commercial")
	}
	if len(c.ByKind(KindImage)) < 20 {
		t.Fatalf("image models: %d", len(c.ByKind(KindImage)))
	}
	all := c.All()
	if all[0].Kind != KindImage || all[len(all)-1].Kind != KindUpscale {
		t.Fatalf("order: %s … %s", all[0].Kind, all[len(all)-1].Kind)
	}
	klein, _ := c.Get("flux2-klein")
	if klein.Worker["diffusers"] == nil {
		t.Fatal("worker section lost")
	}
	if _, err := json.Marshal(klein); err != nil {
		t.Fatalf("manifest must marshal to JSON for workers: %v", err)
	}
}

func TestOverridesMergeAndBadFilesAreReported(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a-override.yaml", "id: flux2-klein\nname: FLUX.2 klein (house build)\nscores: {speed: 99}\n")
	write("b-new.json", `{"models":[{"id":"ink-koma","name":"Ink Koma","maker":"Studio","kind":"image","capabilities":{"tasks":["generate"]},"licence":{"id":"mit","commercial":true},"hardware":{"local":true},"providers":[{"provider":"comfyui","model_id":"ink-koma-v3"}]}]}`)
	write("c-bad.yaml", "id: broken\nkind: spaceship\n")
	write("d-unknown-field.yaml", "id: typo\nname: T\nkind: image\nlicense: {id: mit}\n")
	write("notes.txt", "ignored")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := c.Get("flux2-klein")
	if k.Name != "FLUX.2 klein (house build)" || k.Score(ScoreSpeed) != 99 || k.Score(ScorePanel) != 70 || k.Source != SourceFile {
		t.Fatalf("override not merged: %+v", k)
	}
	if len(k.Providers) != 3 {
		t.Fatalf("providers should be kept: %d", len(k.Providers))
	}
	if _, ok := c.Get("ink-koma"); !ok {
		t.Fatal("new file manifest missing")
	}
	if _, ok := c.Get("broken"); ok {
		t.Fatal("invalid manifest loaded")
	}
	if len(c.Problems) != 2 {
		t.Fatalf("problems: %v", c.Problems)
	}
}

func TestPriceEstimate(t *testing.T) {
	cases := []struct {
		p    Price
		want float64
	}{
		{Price{Unit: PerImage, USD: 0.13}, 0.13},
		{Price{Unit: PerMegapixel, USD: 0.03}, 0.03 * 1024 * 1536 / 1e6},
		{Price{Unit: PerSecond, USD: 0.1}, 0.5},
		{Price{Unit: Per1kTokens, USD: 0.005, USDOut: 0.025}, 0.005*2 + 0.025},
		{Price{}, 0},
	}
	for _, c := range cases {
		if got := c.p.Estimate(1024, 1536, 5, 2000, 1000); abs(got-c.want) > 1e-9 {
			t.Errorf("%+v → %v, want %v", c.p, got, c.want)
		}
	}
}

func TestValidateAndHelpers(t *testing.T) {
	m := &Manifest{ID: "Bad ID", Kind: "x"}
	err := m.Validate()
	for _, want := range []string{"id", "name is required", "kind", "tasks", "licence", "provider"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("validate lacks %q: %v", want, err)
		}
	}
	if KindForTask(TaskTranslate) != KindText || KindForTask(TaskInpaint) != KindImage || JobTypeForTask(TaskSheet) != "character.sheet" {
		t.Fatal("task mapping")
	}
	c, _ := Load("")
	g, _ := c.Get("gpt-image-2")
	if g.AllowsRating("adult") || !g.AllowsRating("teen") {
		t.Fatal("ratings")
	}
	if g.Local() {
		t.Fatal("gpt-image-2 is remote")
	}
	cl := g.Clone()
	cl.Scores["panel"] = 1
	if g.Score("panel") == 1 {
		t.Fatal("clone shares scores")
	}
	if _, err := Parse([]byte("id: x\nname: X\nkind: text\ncapabilities: {tasks: [text]}\nlicence: {id: mit, commercial: true}\nproviders: [{provider: ollama, model_id: x}]\n")); err != nil {
		t.Fatal(err)
	}
	if Hash(g) == Hash(cl) {
		t.Fatal("hash ignores changes")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
