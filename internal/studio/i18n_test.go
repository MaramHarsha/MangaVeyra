package studio_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

var keyRe = regexp.MustCompile(`i18n\.T\((?:ctx|tctx|r\.Context\(\)), "([a-z0-9_.]+)"`)

// dynamicKeys expands keys built from a prefix plus a value.
func dynamicKeys() []string {
	var out []string
	add := func(prefix string, vals ...string) {
		for _, v := range vals {
			out = append(out, prefix+v)
		}
	}
	add("studio.badge.", studio.BadgeDraft, studio.BadgePublished, studio.BadgeScript, studio.BadgeWebtoon, studio.BadgeImported)
	add("studio.role.", studio.Roles...)
	add("studio.role_word.", studio.CharacterRoles...)
	add("studio.category.", studio.Categories...)
	add("studio.format.", studio.Formats...)
	for _, f := range studio.Formats {
		out = append(out, "studio.format."+f+".desc")
	}
	add("studio.line.", studio.LineKinds...)
	add("studio.ref.", studio.RefKinds...)
	add("studio.lang.", studio.ContentLanguages...)
	add("studio.time_of_day.", studio.TimesOfDay...)
	add("studio.visibility.", studio.VisibilityPrivate, studio.VisibilityPublic, studio.VisibilityPrivate+".desc", studio.VisibilityPublic+".desc")
	add("studio.routing.", "default", "balanced", "best", "fastest", "local_only", "free_only")
	add("studio.wizard.step.", studio.WizardSteps...)
	add("studio.wizard.q.", studio.WizardSteps...)
	add("studio.wizard.lead.", studio.WizardSteps[1:]...)
	add("studio.assist.op.", studio.OpAdd, studio.OpUpdate, studio.OpRemove, studio.OpMove)
	add("studio.character.tab.", "profile", "outfits", "relations", "refs")
	add("studio.setting.add_kind.", studio.SettingKinds...)
	add("studio.setting.description_ph.", studio.SettingKinds...)
	add("studio.setting.empty.", studio.SettingKinds...)
	add("studio.setting.ref.", studio.SettingKinds...)
	add("", studio.PremiseExamples...)
	add("studio.assist.ph.", "empty", "affects_none", "summary", "affects")
	add("studio.character.", "lock_ready", "lock_failed")
	add("genre.", studio.Genres...)
	add("style.", studio.Styles...)
	for _, s := range studio.Styles {
		out = append(out, "style."+s+".desc")
	}
	return out
}

func usedKeys(t *testing.T) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(path, "_templ.go") || strings.HasSuffix(path, "_test.go") ||
			!(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".templ")) {
			return nil
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
		// Keys listed as data (NameKey: "studio.tpl...", "studio.tpl.beat.ki").
		for _, m := range regexp.MustCompile(`"(studio\.tpl\.[a-z0-9_.]+)"`).FindAllStringSubmatch(string(b), -1) {
			if !strings.HasSuffix(m[1], ".") {
				keys[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range dynamicKeys() {
		keys[k] = true
	}
	return keys
}

func has(b *i18n.Bundle, lang, key string) bool {
	for _, k := range []string{key, key + ".other"} {
		if v, ok := b.Lookup(lang, k); ok && v != "" {
			return true
		}
	}
	return false
}

func TestStudioKeysAreTranslated(t *testing.T) {
	b := i18n.Default()
	keys := usedKeys(t)
	if len(keys) < 400 {
		t.Fatalf("found only %d keys; is the scanner broken?", len(keys))
	}
	for k := range keys {
		for _, lang := range []string{"en", "ja"} {
			if !has(b, lang, k) {
				t.Errorf("missing %s translation for %q", lang, k)
			}
		}
		if strings.HasPrefix(k, "studio.") {
			en, _ := b.Lookup("en", k)
			ja, _ := b.Lookup("ja", k)
			if en == "" {
				en, _ = b.Lookup("en", k+".other")
				ja, _ = b.Lookup("ja", k+".other")
			}
			if en != "" && en == ja && strings.ContainsAny(en, "abcdefghijklmnopqrstuvwxyz") {
				t.Errorf("ja text of %q is the English text", k)
			}
		}
	}
}

// forbidden are the payment words product rule 1 keeps out of the open-source UI.
var forbidden = regexp.MustCompile(`(?i)\b(price|prices|pricing|checkout|credits?|wallet|tips?|tipping|subscriptions?|subscribe|payouts?|upgrade to|buy now|purchase)\b`)

func TestStudioHasNoPaymentWords(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		b, err := fs.ReadFile(i18n.Embedded(), lang+"/studio.json")
		if err != nil {
			t.Fatal(err)
		}
		if m := forbidden.FindString(string(b)); m != "" {
			t.Errorf("%s/studio.json contains %q", lang, m)
		}
	}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		b, _ := os.ReadFile(path)
		// Tip is the tooltip field of the UI kit's button props, not a payment word.
		text := strings.ReplaceAll(string(b), "Tip:", "")
		if m := forbidden.FindString(text); m != "" {
			t.Errorf("%s contains %q", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
