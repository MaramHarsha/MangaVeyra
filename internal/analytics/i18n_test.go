package analytics

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

var keyRe = regexp.MustCompile(`i18n\.T\(ctx, "([a-z0-9_.]+)"`)

// TestTranslations checks that every key F2 renders exists in English and
// Japanese (Japanese plural keys may use only ".other").
func TestTranslations(t *testing.T) {
	keys := map[string]bool{}
	files, _ := filepath.Glob("*.go")
	templs, _ := filepath.Glob("*.templ")
	for _, f := range append(files, templs...) {
		if strings.HasSuffix(f, "_test.go") || strings.HasSuffix(f, "_templ.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range keyRe.FindAllStringSubmatch(string(b), -1) {
			keys[m[1]] = true
		}
	}
	if len(keys) < 30 {
		t.Fatalf("found only %d keys; is the scanner broken?", len(keys))
	}
	b := i18n.Default()
	jaMissing := map[string]bool{}
	for _, k := range b.Missing("ja") {
		jaMissing[k] = true
	}
	for k := range keys {
		_, en := b.Lookup("en", k)
		_, enPlural := b.Lookup("en", k+".other")
		if !en && !enPlural {
			t.Errorf("en: missing %q", k)
			continue
		}
		if (en && jaMissing[k]) || (!en && jaMissing[k+".other"]) {
			t.Errorf("ja: missing %q", k)
		}
	}
}
