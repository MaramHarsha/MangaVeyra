package editioncheck

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

func TestLocaleCatalogsLoad(t *testing.T) {
	n := 0
	err := fs.WalkDir(i18n.Embedded(), ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil || n == 0 {
		t.Fatalf("no locale catalogs found (%d): %v", n, err)
	}
}

// forbidden matches payment words product rule 1 keeps out of the open-source
// edition (the same list as internal/web's locale test).
var forbidden = regexp.MustCompile(`(?i)\b(price|prices|pricing|checkout|credits?|wallet|tips?|tipping|subscriptions?|subscribe|payouts?|upgrade to|buy now|purchase)\b`)

// allowed lists locale keys whose match is not about payment: a book's credits
// page, and the tip of a speech-bubble tail.
var allowed = map[string]bool{
	"exportui.credits": true,
	"editor.tail_x":    true,
	"editor.tail_y":    true,
}

// TestLocaleCatalogsHaveNoPaymentWords scans every value of every embedded
// locale catalog, so new modules' strings are covered without being listed.
func TestLocaleCatalogsHaveNoPaymentWords(t *testing.T) {
	err := fs.WalkDir(i18n.Embedded(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		b, err := fs.ReadFile(i18n.Embedded(), path)
		if err != nil {
			return err
		}
		var cat map[string]any
		if err := json.Unmarshal(b, &cat); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		walkStrings("", cat, func(key, value string) {
			if m := forbidden.FindString(value); m != "" && !allowed[key] {
				t.Errorf("%s: %s contains %q", path, key, m)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func walkStrings(prefix string, v any, fn func(key, value string)) {
	switch x := v.(type) {
	case string:
		fn(prefix, x)
	case map[string]any:
		for k, child := range x {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			walkStrings(key, child, fn)
		}
	case []any:
		for _, child := range x {
			walkStrings(prefix, child, fn)
		}
	}
}

// TestWebsiteAndInstallGuideHaveNoPaymentWords scans the static landing page
// and the install guide, which are not locale catalogs.
func TestWebsiteAndInstallGuideHaveNoPaymentWords(t *testing.T) {
	root := filepath.Join("..", "..")
	n := 0
	for _, dir := range []string{"site", filepath.Join("docs", "install")} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			switch filepath.Ext(path) {
			case ".html", ".md", ".css":
			default:
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			n++
			if m := forbidden.FindString(string(b)); m != "" {
				t.Errorf("%s contains %q", path, m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if n == 0 {
		t.Fatal("no website or install guide files found")
	}
}
