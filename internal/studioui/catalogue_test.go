package studioui_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// Japanese has no plural categories, so a Japanese catalogue missing an
// English plural form falls back to English ("1 references"). The C2–C5,
// Studio and export catalogues therefore carry exactly the same keys.
func TestBilingualCataloguesHaveTheSameKeys(t *testing.T) {
	for _, module := range []string{"studiobible", "studioui", "exportui"} {
		keys := map[string][]string{}
		for _, lang := range []string{"en", "ja"} {
			raw, err := os.ReadFile(filepath.Join("..", "i18n", "locales", lang, module+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var catalogue map[string]string
			if err := json.Unmarshal(raw, &catalogue); err != nil {
				t.Fatal(err)
			}
			for key := range catalogue {
				keys[lang] = append(keys[lang], key)
			}
			slices.Sort(keys[lang])
		}
		for _, key := range keys["en"] {
			if !slices.Contains(keys["ja"], key) {
				t.Errorf("%s: ja lacks %q", module, key)
			}
		}
		for _, key := range keys["ja"] {
			if !slices.Contains(keys["en"], key) {
				t.Errorf("%s: en lacks %q", module, key)
			}
		}
	}
}

// Every C3 Character Lock count reads correctly for one and many, and the
// Japanese card neither falls back to English nor mixes in the English label.
func TestCharacterLockCountsArePluralizedInBothLanguages(t *testing.T) {
	en := i18n.WithLocale(context.Background(), "en")
	ja := i18n.WithLocale(context.Background(), "ja")
	for _, tt := range []struct {
		key       string
		one, many string
	}{
		{"bible.lock.references", "1 reference", "2 references"},
		{"bible.lock.unscored_panels", "1 panel still needs assessment.", "2 panels still need assessment."},
		{"bible.lock.outfit_pending", "1 assessed panel has no kept outfit image to compare.", "2 assessed panels have no kept outfit image to compare."},
		{"bible.lock.recent", "1 recent assessed panel measurement", "2 recent assessed panel measurements"},
		{"bible.lock.measured", "DINOv2 similarity · average across 1 assessed panel.", "DINOv2 similarity · average across 2 assessed panels."},
		{"bible.lock.measured_outfit_views", "CCIP identity + learned matching to kept outfit views · average across 1 assessed panel.", "CCIP identity + learned matching to kept outfit views · average across 2 assessed panels."},
		{"bible.lock.drift", "1 drifted panel in Ch. 8", "2 drifted panels in Ch. 8"},
	} {
		if got := i18n.T(en, tt.key, "count", 1, "method", "DINOv2", "chapter", 8); got != tt.one {
			t.Errorf("%s(1) = %q, want %q", tt.key, got, tt.one)
		}
		if got := i18n.T(en, tt.key, "count", 2, "method", "DINOv2", "chapter", 8); got != tt.many {
			t.Errorf("%s(2) = %q, want %q", tt.key, got, tt.many)
		}
		for _, n := range []int{1, 2} {
			got := i18n.T(ja, tt.key, "count", n, "method", "DINOv2", "chapter", 8)
			if got == i18n.T(en, tt.key, "count", n, "method", "DINOv2", "chapter", 8) || strings.Contains(got, "panel") || strings.Contains(got, "reference") {
				t.Errorf("ja %s(%d) fell back to English: %q", tt.key, n, got)
			}
		}
	}
	if got := i18n.T(ja, "bible.lock.drift", "count", 1, "chapter", 8); got != "第8話にずれのあるコマが1個" {
		t.Errorf("ja drift line = %q", got)
	}
	for _, key := range []string{"bible.lock.title", "bible.lock.train", "bible.lock.progress", "bible.lock.open_status", "bible.lock.training_hint"} {
		if got := i18n.T(ja, key); strings.Contains(got, "Character Lock") || !strings.Contains(got, "キャラクターロック") {
			t.Errorf("ja %s mixes languages: %q", key, got)
		}
	}
}
