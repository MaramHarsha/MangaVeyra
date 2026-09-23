package router

import (
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
)

func cat(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load("")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func baseInputs(t *testing.T) Inputs {
	return Inputs{
		Catalog: cat(t),
		Providers: []Provider{
			{ID: "ph", Kind: "placeholder", Name: "Built-in", Local: true, Enabled: true, Ready: true},
			{ID: "lw", Kind: "local_worker", Name: "Local worker", Local: true, Enabled: true, Ready: true},
			{ID: "fal1", Kind: "fal", Name: "fal", Enabled: true, Ready: true},
			{ID: "or1", Kind: "openrouter", Name: "OpenRouter", Enabled: true, Ready: true},
		},
		Workers: []Worker{{ID: "w1", Name: "studio-pc", VRAMGB: 24, Models: []string{"qwen-image-edit", "flux2-klein", "illustrious-xl", "flux2-dev"},
			JobTypes: []string{"image.generate", "image.edit", "image.inpaint", "character.sheet"}}},
		DefaultMode:  ModeBalanced,
		LicenceGuard: true,
		RemainingUSD: -1,
	}
}

func skippedReason(d *Decision, model string) string {
	for _, s := range d.Skipped {
		if s.ModelID == model && len(s.Reasons) > 0 {
			return s.Reasons[0].Code
		}
	}
	return ""
}

func hasWhy(d *Decision, code string) bool {
	for _, r := range d.Why {
		if r.Code == code {
			return true
		}
	}
	return false
}

func TestDesignExampleLocalOnly(t *testing.T) {
	in := baseInputs(t)
	need := Need{Task: catalog.TaskEdit, LockedCharacters: 1, Refs: 3, Characters: 1, TextInArt: true, BW: true, Aspect: "2:3", Public: true, Mode: ModeLocalOnly}
	d := Route(in, need)
	p := d.Pick()
	if p == nil || p.ModelID != "qwen-image-edit" || p.ProviderKind != "local_worker" {
		t.Fatalf("pick %+v chain %+v", p, d.Chain)
	}
	if p.Estimate.USD != 0 || p.Estimate.GPUSeconds != 40 {
		t.Fatalf("estimate %+v", p.Estimate)
	}
	if !hasWhy(d, RLocked) || !hasWhy(d, RTextInArt) || !hasWhy(d, RFits) || !hasWhy(d, RModeLocal) {
		t.Fatalf("why %+v", d.Why)
	}
	if got := skippedReason(d, "flux2-dev"); got != RLicence {
		t.Fatalf("flux2-dev skipped for %q (%+v)", got, d.Skipped)
	}
	if got := skippedReason(d, "gpt-image-2"); got != RLocalOnly && got != RNotConnected {
		t.Fatalf("gpt-image-2: %q", got)
	}
	if got := skippedReason(d, "nano-banana-pro"); got != RLocalOnly {
		t.Fatalf("nano banana pro (OpenRouter is connected) should be skipped by local only: %q", got)
	}
	if d.Skipped[0].Reasons[0].Code != RLicence {
		t.Fatalf("policy skips first: %+v", d.Skipped[0])
	}
	last := d.Chain[len(d.Chain)-1]
	if !last.Placeholder {
		t.Fatalf("the placeholder closes the fallback chain: %+v", last)
	}

	gen := Route(in, Need{Task: catalog.TaskGenerate, Mode: ModeLocalOnly, Aspect: "2:3"})
	if got := skippedReason(gen, "hunyuan-image-3"); got != RVRAM {
		t.Fatalf("hunyuan should need more VRAM: %q", got)
	}
	if got := skippedReason(gen, "sd35-large"); got != RLocalOnly {
		t.Fatalf("sd35: %q", got)
	}
}

func TestModesChangeThePick(t *testing.T) {
	in := baseInputs(t)
	cover := Need{Task: catalog.TaskGenerate, PanelType: "cover", Width: 2400, Height: 3600, TextInArt: true}
	best := Route(in, Need{Task: cover.Task, PanelType: "cover", Width: 2400, Height: 3600, TextInArt: true, Mode: ModeBest})
	if best.Pick().Local {
		t.Fatalf("best quality for a 4K cover should pick an API model, got %+v", best.Pick())
	}
	if !hasWhy(best, RModeBest) {
		t.Fatalf("why %+v", best.Why)
	}
	fast := Route(in, Need{Task: catalog.TaskGenerate, Mode: ModeFastest})
	if fast.Pick().ModelID != "flux2-klein" && fast.Pick().ModelID != "z-image-turbo" {
		t.Fatalf("fastest pick %+v", fast.Pick())
	}
	if fast.Pick().Placeholder {
		t.Fatal("the placeholder must never win on speed")
	}
	free := Route(in, Need{Task: catalog.TaskGenerate, Mode: ModeFreeOnly})
	for _, c := range free.Chain {
		if !c.Local {
			t.Fatalf("free only picked a paid remote model: %+v", c)
		}
	}
	if skippedReason(free, "gpt-image-2") == "" {
		t.Fatal("gpt-image-2 should be listed as skipped")
	}
	bal := Route(in, cover)
	if bal.Mode != ModeBalanced || bal.Pick() == nil {
		t.Fatalf("balanced %+v", bal)
	}
}

func TestPinsAndModeResolution(t *testing.T) {
	in := baseInputs(t)
	in.Pins = []Pin{
		{ID: "p1", ScopeKind: PinCharacter, ScopeID: "rin", Task: catalog.TaskGenerate, ModelID: "illustrious-xl", Label: "Rin Asagiri"},
		{ID: "p2", ScopeKind: PinSeries, ScopeID: "glass-orchard", Mode: ModeLocalOnly, Label: "Glass Orchard"},
		{ID: "p3", ScopeKind: PinPanelType, ScopeID: "cover", ModelID: "nano-banana-pro"},
	}
	d := Route(in, Need{Task: catalog.TaskGenerate, CharacterID: "rin", LockedCharacters: 1})
	if d.Pick().ModelID != "illustrious-xl" || !d.Pick().Pinned || d.PinID != "p1" || !hasWhy(d, RPinned) {
		t.Fatalf("character pin: %+v why %+v", d.Pick(), d.Why)
	}
	cover := Route(in, Need{Task: catalog.TaskGenerate, PanelType: "cover"})
	if cover.Pick().ModelID != "nano-banana-pro" {
		t.Fatalf("cover pin: %+v", cover.Pick())
	}
	series := Route(in, Need{Task: catalog.TaskGenerate, SeriesID: "glass-orchard", PanelType: "cover"})
	if series.Mode != ModeLocalOnly || !series.Pick().Local {
		t.Fatalf("series mode pin: mode %s pick %+v", series.Mode, series.Pick())
	}
	if len(series.Warnings) == 0 || series.Warnings[0].Code != WPinSkipped {
		t.Fatalf("a pinned model that cannot run must warn: %+v", series.Warnings)
	}
	in.UserMode = ModeFastest
	if m, _ := ResolveMode(in, Need{Task: catalog.TaskGenerate}); m != ModeFastest {
		t.Fatalf("user override: %s", m)
	}
	in.LocalOnly = true
	if m, _ := ResolveMode(in, Need{Task: catalog.TaskGenerate, Mode: ModeBest}); m != ModeLocalOnly {
		t.Fatalf("instance local only wins: %s", m)
	}
	over := Route(baseInputs(t), Need{Task: catalog.TaskGenerate, ModelID: "flux2-klein", ProviderID: "fal1"})
	if !over.Overridden || over.Pick().ModelID != "flux2-klein" || over.Pick().ProviderID != "fal1" {
		t.Fatalf("override %+v", over.Pick())
	}
}

func TestGuardsSpendAndFallback(t *testing.T) {
	in := baseInputs(t)
	in.RemainingUSD = 0.01
	d := Route(in, Need{Task: catalog.TaskGenerate, Mode: ModeBest, Count: 4})
	if got := skippedReason(d, "gpt-image-2"); got != RSpendLimit && got != RNotConnected {
		t.Fatalf("spend limit: %q", got)
	}
	if got := skippedReason(d, "nano-banana-pro"); got != RSpendLimit {
		t.Fatalf("nano-banana-pro over the limit: %q", got)
	}
	in = baseInputs(t)
	in.LicenceGuard = false
	d = Route(in, Need{Task: catalog.TaskGenerate, Public: true, ModelID: "flux2-dev", ProviderID: "lw"})
	if d.Pick().ModelID != "flux2-dev" || len(d.Warnings) == 0 || d.Warnings[0].Code != WLicence {
		t.Fatalf("guard off must warn: %+v %+v", d.Pick(), d.Warnings)
	}
	d = Route(baseInputs(t), Need{Task: catalog.TaskGenerate})
	first := d.Pick()
	next := d.Next(first.ModelID, first.ProviderID, map[string]bool{})
	if next == nil || (next.ModelID == first.ModelID && next.ProviderID == first.ProviderID) {
		t.Fatalf("fallback %+v", next)
	}
	tried := map[string]bool{}
	for _, c := range d.Chain[:len(d.Chain)-1] {
		tried[c.ModelID+"|"+c.ProviderID] = true
	}
	if n := d.Next("", "", tried); n == nil || !n.Placeholder {
		t.Fatalf("placeholder is the last resort: %+v", n)
	}
	adult := Route(baseInputs(t), Need{Task: catalog.TaskGenerate, Rating: "adult", Mode: ModeBest})
	if got := skippedReason(adult, "nano-banana-pro"); got != RRating {
		t.Fatalf("rating: %q", got)
	}
	in = baseInputs(t)
	in.Disabled = map[string]bool{"qwen-image-edit": true}
	d = Route(in, Need{Task: catalog.TaskEdit, Mode: ModeLocalOnly})
	if d.Pick().ModelID == "qwen-image-edit" || skippedReason(d, "qwen-image-edit") != RDisabled {
		t.Fatalf("disabled model: %+v", d.Pick())
	}
}

func TestPlaceholderOnlyAndCustomModels(t *testing.T) {
	in := baseInputs(t)
	in.Providers = in.Providers[:1]
	in.Workers = nil
	d := Route(in, Need{Task: catalog.TaskGenerate})
	if p := d.Pick(); p == nil || !p.Placeholder || !hasWhy(d, RPlaceholder) {
		t.Fatalf("placeholder only: %+v", p)
	}
	if len(d.Warnings) == 0 || d.Warnings[0].Code != WPlaceholder {
		t.Fatalf("warnings %+v", d.Warnings)
	}
	txt := Route(in, Need{Task: catalog.TaskText})
	if txt.Pick() == nil || txt.Pick().ModelID != "placeholder-text" {
		t.Fatalf("text %+v", txt.Pick())
	}
	custom := &catalog.Manifest{ID: "ink-koma", Name: "Ink Koma", Kind: catalog.KindImage, Source: catalog.SourceCustom,
		Capabilities: catalog.Capabilities{Tasks: []string{catalog.TaskGenerate}, BWScreentone: true},
		Licence:      catalog.Licence{ID: "mit", Commercial: true}, Hardware: catalog.Hardware{Local: true},
		Providers: []catalog.Offer{{Provider: "comfyui", ProviderID: "cf1", ModelID: "workflow", Seconds: 18}}}
	in = baseInputs(t)
	in.Catalog = in.Catalog.With(custom)
	in.Providers = append(in.Providers, Provider{ID: "cf1", Kind: "comfyui", Name: "Studio ComfyUI", Local: true, Enabled: true, Ready: true})
	d = Route(in, Need{Task: catalog.TaskGenerate})
	if d.Pick().ModelID == "ink-koma" || skippedReason(d, "ink-koma") != RNewCustom {
		t.Fatalf("new custom models wait for results: %+v", d.Pick())
	}
	in.Stats = map[string]Stat{"ink-koma": {Runs: 5, Keeps: 5}}
	d = Route(in, Need{Task: catalog.TaskGenerate})
	found := false
	for _, c := range d.Chain {
		found = found || c.ModelID == "ink-koma"
	}
	if !found || skippedReason(d, "ink-koma") == RNewCustom {
		t.Fatal("custom model with results joins routing")
	}
	in.Pins = []Pin{{ID: "x", ScopeKind: PinServer, Task: catalog.TaskGenerate, ModelID: "ink-koma"}}
	in.Stats = nil
	if d := Route(in, Need{Task: catalog.TaskGenerate}); d.Pick().ModelID != "ink-koma" {
		t.Fatalf("pinned custom model: %+v", d.Pick())
	}
}

func TestLearningAndPromptReading(t *testing.T) {
	in := baseInputs(t)
	need := Need{Task: catalog.TaskGenerate, Mode: ModeLocalOnly}
	before := Route(in, need).Pick().ModelID
	in.Learn = true
	in.Stats = map[string]Stat{before: {Runs: 50, Failures: 25, Rerolls: 40, Keeps: 2}}
	after := Route(in, need).Pick().ModelID
	if after == before {
		t.Fatalf("learning from rerolls should demote %s", before)
	}
	n := Need{}
	ReadPrompt(&n, `Rin shouts; the sign behind her reads "NO ENTRY", black and white`)
	if !n.TextInArt || !n.BW {
		t.Fatalf("%+v", n)
	}
	if w, h := aspectSize("2:3"); w != 832 || h != 1248 {
		t.Fatalf("aspect size %d×%d", w, h)
	}
	if !(Need{Width: 800, Height: 3200}).tall() {
		t.Fatal("tall strip")
	}
}
