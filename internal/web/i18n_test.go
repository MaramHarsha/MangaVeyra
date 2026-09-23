package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// sourceDirs are the packages built on the web layer whose strings must be translated.
var sourceDirs = []string{".", "../authui", "../setup", "../account"}

var keyRe = regexp.MustCompile(`i18n\.T\(ctx, "([a-z0-9_.]+)"`)

// dynamicKeys are keys built from a prefix plus a value; every expansion must exist.
var dynamicKeys = map[string][]string{
	"instance.mode.":                {"personal", "private", "public", "personal.title", "private.title", "public.title", "personal.desc", "private.desc", "public.desc"},
	"instance.signup.":              {"closed", "invite", "open"},
	"setup.step.":                   {"admin", "instance", "models", "finish", "admin.short", "instance.short", "models.short", "finish.short"},
	"settings.nav.":                 {"account", "appearance", "language", "reading", "notifications", "accessibility", "world", "data"},
	"settings.theme.":               {"dark", "light", "system"},
	"settings.notifications.group.": {"generation", "comments", "review", "chapter", "follow", "system", "update"},
	"settings.notifications.":       {"in_app", "email", "push"},
	"onboarding.creator.":           {"writer", "artist", "reader", "team", "writer.desc", "artist.desc", "reader.desc", "team.desc"},
	"genre.":                        {"action", "fantasy", "mystery", "romance", "horror", "scifi", "slice_of_life", "comedy", "sports", "historical", "isekai", "thriller", "drama", "mecha", "supernatural"},
	"style.":                        {"seinen_bw", "shonen_ink", "shojo_soft", "webtoon_color", "chibi", "sketch"},
	"xp.title.":                     {"novice", "apprentice", "inker", "storyteller", "mangaka", "veteran", "legend"},
	"dash.idea.":                    {"template.title", "template.desc", "sketch.title", "sketch.desc", "translate.title", "translate.desc"},
	"setup.provider.":               {"local.name", "local.desc", "openrouter.name", "openrouter.desc", "fal.name", "fal.desc", "vendor.name", "vendor.desc", "ollama.name", "ollama.desc", "custom.name", "custom.desc"},
}

func usedKeys(t *testing.T) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	for _, dir := range sourceDirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "static" || d.Name() == "node_modules") {
				return filepath.SkipDir
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
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for prefix, rest := range dynamicKeys {
		for _, r := range rest {
			keys[prefix+r] = true
		}
	}
	return keys
}

func hasKey(b *i18n.Bundle, lang, key string) bool {
	for _, k := range []string{key, key + ".other"} {
		if v, ok := b.Lookup(lang, k); ok && v != "" {
			if lang == "en" {
				return true
			}
			// A translation must exist in the language itself, not only via the English fallback.
			en, _ := b.Lookup("en", k)
			return v != en || isUniversal(v)
		}
	}
	return false
}

// isUniversal reports strings that are the same in every language (brand names, "OK", codes).
func isUniversal(s string) bool {
	switch s {
	case "OK", "GPU", "fal", "OpenRouter", "OpenAI · Google · xAI", "Gate night", "Paper", "LV. {level}", "{gpu} · {used} / {total} GB", "{name} {state} · {queue}":
		return true
	}
	return false
}

func TestEveryUsedKeyIsTranslated(t *testing.T) {
	b := i18n.Default()
	keys := usedKeys(t)
	if len(keys) < 300 {
		t.Fatalf("found only %d keys; is the scanner broken?", len(keys))
	}
	for k := range keys {
		for _, lang := range []string{"en", "ja"} {
			if !hasKey(b, lang, k) {
				t.Errorf("missing %s translation for %q", lang, k)
			}
		}
	}
}

// forbidden are words product rule 1 keeps out of the open-source UI (no payment UI).
var forbidden = regexp.MustCompile(`(?i)\b(price|prices|pricing|checkout|credits?|wallet|tips?|tipping|subscriptions?|subscribe|payouts?|upgrade to|buy now|purchase)\b`)

func TestLocaleFilesHaveNoPaymentWords(t *testing.T) {
	for _, name := range []string{"web", "authui", "setup", "account"} {
		for _, lang := range []string{"en", "ja"} {
			b, err := fs.ReadFile(i18n.Embedded(), lang+"/"+name+".json")
			if err != nil {
				t.Fatalf("read %s/%s: %v", lang, name, err)
			}
			if m := forbidden.FindString(string(b)); m != "" {
				t.Errorf("%s/%s.json contains %q", lang, name, m)
			}
		}
	}
}
