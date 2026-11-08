package discover

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

var keyRe = regexp.MustCompile(`i18n\.T\(ctx, "([a-z0-9_.]+)"`)

// TestTranslations checks that every key this package renders exists in
// English and Japanese (Japanese plural keys may use only ".other").
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
			if !strings.HasSuffix(m[1], ".") {
				keys[m[1]] = true
			}
		}
	}
	for _, tab := range []string{RankRising, RankWeek, RankAll} {
		keys["discover.rank."+tab] = true
	}
	for _, v := range formats {
		keys["discover.format."+v] = true
	}
	for _, v := range statuses {
		keys["discover.status."+v] = true
	}
	for _, v := range ratings {
		keys["discover.rating."+v] = true
	}
	for _, v := range arts {
		keys["discover.art_label."+v] = true
	}
	for _, v := range pagedoc.ContentLanguages {
		keys["discover.language."+v] = true
	}
	for _, g := range searchGenres {
		keys["genre."+g] = true
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
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
}
