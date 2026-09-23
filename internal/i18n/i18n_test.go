package i18n

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

// TestEmbeddedCatalogsAreValid fails the build when a shipped catalog is
// malformed, a translation uses a key English does not have, or placeholders drift.
func TestEmbeddedCatalogsAreValid(t *testing.T) {
	b := NewBundle()
	if err := b.AddFS(Embedded()); err != nil {
		t.Fatalf("malformed catalog: %v", err)
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	langs := b.Languages()
	if len(langs) < 2 || langs[0] != "en" || !b.Has("ja") {
		t.Fatalf("languages %v", langs)
	}
}

func TestMalformedCatalogsAreRejected(t *testing.T) {
	cases := map[string]string{
		"not json":      `{"a": `,
		"nested":        `{"a": {"b": "c"}}`,
		"bad key":       `{"a b": "c"}`,
		"unbalanced":    `{"a": "hello {name"}`,
		"nested braces": `{"a": "{{x}}"}`,
	}
	for name, body := range cases {
		b := NewBundle()
		err := b.AddFS(fstest.MapFS{"en/core.json": {Data: []byte(body)}})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	b := NewBundle()
	_ = b.AddFS(fstest.MapFS{
		"en/core.json": {Data: []byte(`{"hi": "Hi {name}", "pages.one": "{count} page", "pages.other": "{count} pages"}`)},
		"ja/core.json": {Data: []byte(`{"hi": "こんにちは {nom}", "extra": "x", "pages.other": "{count} ページ"}`)},
	})
	err := b.Validate()
	if err == nil || !strings.Contains(err.Error(), `"extra"`) || !strings.Contains(err.Error(), "placeholders") {
		t.Fatalf("validate: %v", err)
	}
}

func TestTranslate(t *testing.T) {
	b := NewBundle()
	if err := b.AddFS(fstest.MapFS{
		"en/a.json": {Data: []byte(`{"hi": "Hi {name}", "pages.one": "{count} page", "pages.other": "{count} pages", "only.en": "English only"}`)},
		"ja/a.json": {Data: []byte(`{"hi": "こんにちは {name}", "pages.other": "{count} ページ"}`)},
		"ru/a.json": {Data: []byte(`{"pages.one": "{count} страница", "pages.few": "{count} страницы", "pages.many": "{count} страниц"}`)},
	}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		lang, key string
		args      []any
		want      string
	}{
		{"en", "hi", []any{"name", "Rin"}, "Hi Rin"},
		{"ja", "hi", []any{Args{"name": "凛"}}, "こんにちは 凛"},
		{"ja-JP", "only.en", nil, "English only"},
		{"en", "pages", []any{"count", 1}, "1 page"},
		{"en", "pages", []any{"count", 1200}, "1,200 pages"},
		{"ja", "pages", []any{"count", 1}, "1 ページ"},
		{"ru", "pages", []any{"count", 3}, "3 страницы"},
		{"ru", "pages", []any{"count", 11}, "11 страниц"},
		{"ru", "pages", []any{"count", 21}, "21 страница"},
		{"en", "missing.key", nil, "missing.key"},
		{"en", "hi", nil, "Hi {name}"},
	}
	for _, c := range cases {
		if got := b.Translate(c.lang, c.key, c.args...); got != c.want {
			t.Errorf("%s %s → %q, want %q", c.lang, c.key, got, c.want)
		}
	}
	ctx := WithLocale(context.Background(), "ja")
	if got := T(ctx, "jobs.state.done"); got != "完了" {
		t.Fatalf("T with default bundle: %q", got)
	}
	if got := T(context.Background(), "error.rate_limited", "seconds", 30); got != "Too many attempts. Try again in 30 seconds." {
		t.Fatal(got)
	}
}

func TestNegotiate(t *testing.T) {
	sup := []string{"en", "ja", "pt-BR"}
	cases := []struct{ user, cookie, accept, want string }{
		{"", "", "ja-JP,ja;q=0.9,en;q=0.8", "ja"},
		{"", "", "fr-FR, de;q=0.5", "en"},
		{"", "", "pt;q=0.4, fr", "pt-BR"},
		{"", "ja", "en", "ja"},
		{"en", "ja", "ja", "en"},
		{"xx", "", "de;q=0, ja;q=0.1", "ja"},
	}
	for _, c := range cases {
		if got := Negotiate(sup, "en", c.user, c.cookie, c.accept); got != c.want {
			t.Errorf("%+v → %s", c, got)
		}
	}
	if !IsRTL("ar-EG") || Dir("he") != "rtl" || Dir("ja") != "ltr" {
		t.Fatal("rtl detection")
	}
	if FormatInt(1234567, "de") != "1.234.567" || FormatInt(-1234, "en") != "-1,234" || FormatInt(999, "en") != "999" {
		t.Fatal("number formatting")
	}
}
