package pagedoc

import "testing"

func TestContentLanguages(t *testing.T) {
	for _, l := range ContentLanguages {
		if !IsContentLanguage(l) || NormalizeContentLanguage(l) != l {
			t.Fatalf("%q must be canonical and supported", l)
		}
	}
	cases := map[string]string{
		"ko-KR": "ko", "zh": "zh-Hans", "zh-CN": "zh-Hans", "zh_TW": "zh-Hant", "zh-HK": "zh-Hant",
		"zh-hant": "zh-Hant", "zh-Hans-SG": "zh-Hans", "pt-BR": "pt", "es-419": "es", "HI": "hi",
		"": "", "ar": "", "zh-XX": "", "klingon": "",
	}
	for in, want := range cases {
		if got := NormalizeContentLanguage(in); got != want {
			t.Errorf("NormalizeContentLanguage(%q) = %q, want %q", in, got, want)
		}
	}
	for lang, want := range map[string][2]bool{
		"ja": {true, true}, "zh-Hant": {true, true}, "zh-Hans": {true, false}, "ko": {true, false},
		"en": {false, false}, "hi": {false, false}, "es": {false, false},
	} {
		if VerticalAllowed(lang) != want[0] || VerticalByDefault(lang) != want[1] {
			t.Errorf("%s vertical allowed/default = %v/%v, want %v", lang, VerticalAllowed(lang), VerticalByDefault(lang), want)
		}
	}
}
