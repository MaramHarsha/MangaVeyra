package reader_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/reader"
)

var readerKeyRe = regexp.MustCompile(`i18n\.T\(ctx, "(reader\.[a-z0-9_.]+)"`)

// dynamicReaderKeys are built from a prefix and a value at run time.
var dynamicReaderKeys = map[string][]string{
	"reader.mode.":                {"paged", "spread", "guided", "webtoon"},
	"reader.dir.":                 {"rtl", "ltr", "vertical"},
	"reader.rating.":              {"all", "teen", "mature", "adult"},
	"reader.gate.title.":          {"mature", "adult"},
	"reader.ai.script.":           {"human", "ai_assisted", "ai"},
	"reader.ai.art.":              {"human", "ai_assisted", "ai"},
	"reader.ai.lettering.":        {"human", "ai_assisted", "ai"},
	"reader.library.":             {"history.empty.title", "history.empty.body", "bookmarks.empty.title", "bookmarks.empty.body", "downloads.empty.title", "downloads.empty.body"},
	"reader.chapter.label":        {"", ".untitled"},
	"reader.episode.label":        {"", ".untitled"},
	"reader.lang.":                {"en", "ja"},
	"reader.library.downloads.":   {"summary"},
	"reader.pages":                {""},
	"reader.library.status.":      {"ready", "pending", "expired"},
	"reader.settings.read_aloud.": {"none", "voice"},
}

// sameInEveryLanguage are values that are intentionally identical in en and ja.
var sameInEveryLanguage = map[string]bool{"reader.library.size": true}

func usedReaderKeys(t *testing.T) map[string]bool {
	t.Helper()
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
		for _, m := range readerKeyRe.FindAllStringSubmatch(string(b), -1) {
			if !strings.HasSuffix(m[1], ".") {
				keys[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for prefix, rest := range dynamicReaderKeys {
		for _, r := range rest {
			keys[prefix+r] = true
		}
	}
	for _, k := range reader.IslandKeys {
		keys["reader."+k] = true
	}
	return keys
}

func lookup(b *i18n.Bundle, lang, key string) (string, bool) {
	for _, k := range []string{key, key + ".other"} {
		if v, ok := b.Lookup(lang, k); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// Every reader string exists in English and in Japanese (not only through
// the English fallback).
func TestReaderStringsAreTranslated(t *testing.T) {
	b := i18n.Default()
	keys := usedReaderKeys(t)
	if len(keys) < 150 {
		t.Fatalf("found only %d reader keys; is the scanner broken?", len(keys))
	}
	for k := range keys {
		en, ok := lookup(b, "en", k)
		if !ok {
			t.Errorf("missing en %q", k)
			continue
		}
		ja, ok := lookup(b, "ja", k)
		if !ok {
			t.Errorf("missing ja %q", k)
			continue
		}
		if ja == en && !sameInEveryLanguage[k] && !strings.HasPrefix(k, "reader.lang.") {
			t.Errorf("ja %q is the English text %q", k, en)
		}
	}
}

// forbiddenWords keep payment UI out of the open-source app (product rule 1).
var forbiddenWords = regexp.MustCompile(`(?i)\b(price|prices|pricing|checkout|credits?|wallet|tips?|tipping|subscriptions?|subscribe|payouts?|upgrade to|buy now|purchase)\b`)

func TestReaderCataloguesHaveNoPaymentWords(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		raw, err := fs.ReadFile(i18n.Embedded(), lang+"/reader.json")
		if err != nil {
			t.Fatal(err)
		}
		cat := map[string]string{}
		if err := json.Unmarshal(raw, &cat); err != nil {
			t.Fatalf("%s/reader.json: %v", lang, err)
		}
		for k, v := range cat {
			if forbiddenWords.MatchString(v) {
				t.Errorf("%s %s uses a payment word: %q", lang, k, v)
			}
		}
	}
}
