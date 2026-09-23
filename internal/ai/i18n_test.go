package ai_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

var keyRe = regexp.MustCompile(`i18n\.T\(ctx, "([a-z0-9_.]+)"`)

// TestEveryUsedKeyIsTranslated fails when a key used by the ai module or a
// dynamic expansion (modes, kinds, tasks, reasons…) is missing in en or ja.
func TestEveryUsedKeyIsTranslated(t *testing.T) {
	keys := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, "_templ.go") || strings.HasSuffix(path, "_test.go") ||
			!(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".templ")) {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range keyRe.FindAllStringSubmatch(string(b), -1) {
			if !strings.HasSuffix(m[1], ".") {
				keys[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := catalog.Load("")
	dyn := map[string][]string{
		"ai.mode.":    router.Modes,
		"ai.kindtab.": catalog.Kinds,
		"ai.task.":    catalog.Tasks,
		"ai.status.":  {"ready", "licence", "disabled", "not_connected", "not_installed", "vram", "placeholder"},
		"ai.kind.": {"openrouter", "fal", "openai", "google", "xai", "anthropic", "replicate", "together", "runware", "huggingface", "ollama",
			"openai_compatible", "comfyui", "http", "local_worker", "placeholder"},
		"ai.conn.":        {"openai_compatible", "comfyui", "huggingface", "http"},
		"ai.pin.scope.":   router.PinScopes,
		"ai.pin.for.":     router.PinScopes,
		"ai.pin.text.":    {"server", "project", "series", "character", "panel_type", "job_type"},
		"ai.series.":      {"public", "commercial"},
		"ai.rating.":      {"all", "teen", "mature", "adult"},
		"ai.card.state.":  {"ok", "warn", "off"},
		"ai.changes.":     {"added", "updated", "removed"},
		"ai.wizard.step.": {"connect", "test", "describe", "route"},
		"ai.scope.":       {"server", "user"},
		"ai.map.":         {"prompt", "negative", "reference", "pose", "seed", "image", "mask", "width", "height", "steps", "batch"},
		"ai.cap.":         {"refs", "lora", "edit", "inpaint", "layered", "text_in_art", "bw_screentone", "colour", "tall_strips", "pose"},
		"ai.local_only.":  {"on", "off"},
		"ai.models.":      {"enabled", "disabled"},
		"ai.reason.": {router.RDisabled, router.RNotConnected, router.RNotInstalled, router.RVRAM, router.RNoRefs, router.RNoLoRA, router.RRating,
			router.RLicence, router.RLocalOnly, router.RFreeOnly, router.RSpendLimit, router.RNewCustom, router.RDeprecated, router.RLowerScore,
			router.RPinned, router.ROverride, router.RLocked, router.RTextInArt, router.RBW, router.RTall, router.RFits, router.RModeBest,
			router.RModeFastest, router.RModeLocal, router.RModeFree, router.RBalanced, router.RFallback, router.RPlaceholder, router.RLayered,
			router.RRefs, router.WLicence, router.WPinSkipped, router.WDownscale, router.WRefsCapped, router.WPlaceholder},
		"ai.error.": {"kind", "name", "url_required", "url_blocked", "url_private", "key_required", "key_long", "model_taken", "pin_scope",
			"pin_scope_id", "pin_task", "pin_mode", "pin_model"},
	}
	for _, m := range c.All() {
		for _, u := range m.Uses {
			dyn["ai.use."] = append(dyn["ai.use."], u)
		}
	}
	for prefix, rest := range dyn {
		for _, r := range rest {
			keys[prefix+r] = true
		}
	}
	if len(keys) < 300 {
		t.Fatalf("only %d keys found; is the scanner broken?", len(keys))
	}
	for _, lang := range []string{"en", "ja"} {
		raw, err := fs.ReadFile(i18n.Embedded(), lang+"/ai.json")
		if err != nil {
			t.Fatal(err)
		}
		cat := map[string]string{}
		if err := json.Unmarshal(raw, &cat); err != nil {
			t.Fatal(err)
		}
		for k := range keys {
			if cat[k] == "" && cat[k+".other"] == "" {
				t.Errorf("missing %s translation for %q", lang, k)
			}
		}
	}
}

var forbidden = regexp.MustCompile(`(?i)\b(price|prices|pricing|checkout|credits?|wallet|tips?|tipping|subscriptions?|subscribe|payouts?|upgrade to|buy now|purchase)\b`)

func TestLocaleFileHasNoPaymentWords(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		b, err := fs.ReadFile(i18n.Embedded(), lang+"/ai.json")
		if err != nil {
			t.Fatal(err)
		}
		if m := forbidden.FindString(string(b)); m != "" {
			t.Errorf("%s/ai.json contains %q", lang, m)
		}
	}
	// Templates must not show payment words either.
	_ = filepath.WalkDir("views", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !strings.HasSuffix(path, ".templ") {
			return err
		}
		b, _ := os.ReadFile(path)
		if m := forbidden.FindString(string(b)); m != "" {
			t.Errorf("%s contains %q", path, m)
		}
		return nil
	})
}
