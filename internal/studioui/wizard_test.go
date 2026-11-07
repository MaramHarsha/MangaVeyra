package studioui

import (
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

func TestWizardOpenStep(t *testing.T) {
	for _, tc := range []struct {
		step   string
		errors web.FormErrors
		want   string
		steps  []string
	}{
		{"", web.FormErrors{}, "premise", nil},
		{"format", web.FormErrors{}, "format", nil},
		{"bogus", web.FormErrors{}, "premise", nil},
		{"premise", web.FormErrors{"pages_target": "x", "genres": "x"}, "genre", []string{"genre", "format"}},
		{"generate", web.FormErrors{"source_file": "x"}, "premise", []string{"premise"}},
		{"premise", web.FormErrors{"": "x"}, "generate", []string{"generate"}},
		{"premise", web.FormErrors{"unmapped": "x"}, "generate", []string{"generate"}},
	} {
		v := newProjectView{Step: tc.step, Errors: tc.errors}
		if got := v.openStep(); got != tc.want {
			t.Errorf("openStep(%q, %v) = %q, want %q", tc.step, tc.errors, got, tc.want)
		}
		if got := v.errorSteps(); len(got) != len(tc.steps) || (len(got) > 0 && got[0] != tc.steps[0]) {
			t.Errorf("errorSteps(%v) = %v, want %v", tc.errors, got, tc.steps)
		}
	}
	for field, step := range stepOfField {
		if !contains(wizardSteps, step) {
			t.Errorf("field %q maps to unknown step %q", field, step)
		}
	}
}

// Keys built from a prefix and a value are invisible to the web package's
// key scanner, so check every expansion here in both languages.
func TestDynamicKeysAreTranslated(t *testing.T) {
	var keys []string
	add := func(prefix string, values []string, suffixes ...string) {
		if len(suffixes) == 0 {
			suffixes = []string{""}
		}
		for _, v := range values {
			for _, s := range suffixes {
				keys = append(keys, prefix+v+s)
			}
		}
	}
	add("genre.", genreValues)
	add("style.", styleValues, "", ".desc")
	add("studioui.format.", formatValues)
	add("studioui.format_badge.", formatValues)
	add("studioui.new.format.", formatValues, ".desc")
	add("studioui.status.", projectStatuses)
	add("studioui.language.", languageValues)
	add("studioui.dialogue.", []string{"speech", "thought", "caption"})
	add("studioui.new.try.", premiseSuggestions)
	add("studioui.new.step.", wizardSteps)
	add("studioui.new.", wizardSteps, ".heading", ".lead")
	add("studioui.new.next.", wizardSteps[1:])
	for _, size := range pageSizes {
		keys = append(keys, "studioui.page_size."+size.Value)
	}
	keys = append(keys, "studioui.dashboard.chapters.other", "studioui.dashboard.chapters.one")
	b := i18n.Default()
	for _, key := range keys {
		en, ok := b.Lookup("en", key)
		if !ok || en == "" {
			t.Errorf("missing en %q", key)
			continue
		}
		if key == "studioui.dashboard.chapters.one" {
			continue // Japanese has no singular form
		}
		if ja, ok := b.Lookup("ja", key); !ok || ja == "" || ja == en {
			t.Errorf("missing ja translation for %q", key)
		}
	}
}
