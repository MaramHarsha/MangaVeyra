package pagedoc

import "strings"

// ContentLanguages lists the manga content languages a project, its bubble text
// and its exports may use, in the order the interface offers them. These are
// manuscript and lettering languages; the interface itself is localised
// separately (English and Japanese).
var ContentLanguages = []string{"en", "ja", "ko", "zh-Hans", "zh-Hant", "es", "fr", "de", "pt", "hi"}

// IsContentLanguage reports whether lang is exactly one of ContentLanguages.
func IsContentLanguage(lang string) bool {
	for _, l := range ContentLanguages {
		if l == lang {
			return true
		}
	}
	return false
}

// NormalizeContentLanguage maps common spellings of a supported language to its
// canonical tag ("ko-KR" → "ko", "zh-TW" → "zh-Hant", "zh" → "zh-Hans"). It
// returns "" for an unsupported language.
func NormalizeContentLanguage(lang string) string {
	lang = strings.TrimSpace(lang)
	if IsContentLanguage(lang) {
		return lang
	}
	lower := strings.ToLower(strings.ReplaceAll(lang, "_", "-"))
	switch {
	case lower == "zh-hant" || strings.HasPrefix(lower, "zh-hant-") || lower == "zh-tw" || lower == "zh-hk" || lower == "zh-mo":
		return "zh-Hant"
	case lower == "zh" || lower == "zh-hans" || strings.HasPrefix(lower, "zh-hans-") || lower == "zh-cn" || lower == "zh-sg":
		return "zh-Hans"
	}
	base := strings.SplitN(lower, "-", 2)[0]
	if base != "zh" && IsContentLanguage(base) {
		return base
	}
	return ""
}

// VerticalAllowed reports whether lettering in lang may be set vertically.
func VerticalAllowed(lang string) bool {
	switch NormalizeContentLanguage(lang) {
	case "ja", "ko", "zh-Hans", "zh-Hant":
		return true
	}
	return false
}

// VerticalByDefault reports whether new and auto-placed lettering in lang is
// vertical unless the creator changes it (Japanese and Traditional Chinese).
func VerticalByDefault(lang string) bool {
	switch NormalizeContentLanguage(lang) {
	case "ja", "zh-Hant":
		return true
	}
	return false
}
