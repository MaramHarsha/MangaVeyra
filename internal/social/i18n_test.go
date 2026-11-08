package social

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

var keyRe = regexp.MustCompile(`i18n\.T\(ctx, "([a-z0-9_.]+)"`)

// dynamicKeys are built from a prefix plus a value; every expansion must exist.
var dynamicKeys = map[string][]string{
	"social.format.":           {"manga_rtl", "comic_ltr", "webtoon"},
	"social.rating.":           {"all", "teen", "mature", "adult"},
	"social.status.":           {"ongoing", "completed", "hiatus", "draft"},
	"social.licence.":          {"all_rights", "cc_by", "cc_by_sa", "cc_by_nc", "cc_by_nc_sa", "cc0"},
	"social.report.reason.":    ReportReasons,
	"social.profile.type.":     {"writer", "artist", "reader", "team"},
	"social.ai.art.":           {"human", "ai_assisted", "ai"},
	"social.ai.script.":        {"human", "ai_assisted", "ai"},
	"social.ai.lettering.":     {"human", "ai_assisted", "ai"},
	"social.profile.rank.":     {"creator", "reader"},
	"social.comment.":          {"hidden_done", "shown_done"},
	"social.error.":            {"self_follow", "target_kind", "anchor", "body_empty", "body_long", "parent", "reason", "details_long", "report_self", "invalid", "forbidden"},
	"social.profile.chapters_": {"short"},
}

// universal values may equal English in Japanese (codes and licence names).
func universal(v string) bool { return strings.HasPrefix(v, "CC") }

func usedKeys(t *testing.T) map[string]bool {
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
	for prefix, rest := range dynamicKeys {
		for _, r := range rest {
			keys[prefix+r] = true
		}
	}
	return keys
}

// TestEveryUsedKeyIsTranslated keeps E5 and F1 complete in English and Japanese.
func TestEveryUsedKeyIsTranslated(t *testing.T) {
	b := i18n.Default()
	keys := usedKeys(t)
	if len(keys) < 100 {
		t.Fatalf("found only %d keys; is the scanner broken?", len(keys))
	}
	for k := range keys {
		for _, lang := range []string{"en", "ja"} {
			v, ok := b.Lookup(lang, k)
			if !ok {
				v, ok = b.Lookup(lang, k+".other")
			}
			if !ok || v == "" {
				t.Errorf("missing %s text for %q", lang, k)
				continue
			}
			if lang == "ja" && !strings.HasPrefix(k, "ui.") {
				en, _ := b.Lookup("en", k)
				if en2, ok := b.Lookup("en", k+".other"); ok && en == "" {
					en = en2
				}
				if v == en && !universal(v) {
					t.Errorf("%q has no Japanese translation (%q)", k, v)
				}
			}
		}
	}
}

// forbidden are the words product rule 1 keeps out of the open-source UI.
var forbidden = regexp.MustCompile(`(?i)\b(price|prices|pricing|checkout|credits?|wallet|tips?|tipping|subscriptions?|subscribe|payouts?|upgrade to|buy now|purchase)\b`)

func TestSocialCatalogsHaveNoPaymentWords(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		b, err := fs.ReadFile(i18n.Embedded(), lang+"/social.json")
		if err != nil {
			t.Fatalf("read %s/social.json: %v", lang, err)
		}
		if m := forbidden.FindString(string(b)); m != "" {
			t.Errorf("%s/social.json contains %q", lang, m)
		}
	}
	for _, name := range []string{"series.templ", "profile.templ"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// data-tip is the kit's tooltip attribute, not copy.
		text := strings.ReplaceAll(string(b), "Tip:", "")
		if m := forbidden.FindString(text); m != "" {
			t.Errorf("%s contains %q", name, m)
		}
	}
}
