// Package i18n holds the interface message catalogs and the helpers every
// template and handler uses to translate text.
//
// Catalogs live in locales/<lang>/<module>.json as flat {"key": "text"} maps
// and are embedded into the binary. Every file of a language is merged into
// one catalog. English is complete and is the fallback for any missing key;
// Japanese ships as the second language. Modules outside this package can
// contribute catalogs with Default().AddFS (the web layer embeds its own), and
// admins can drop community translations into <data>/locales.
//
// Text uses ICU-lite placeholders: "Hello, {name}". Plural forms use key
// suffixes selected by the "count" argument: "pages.one", "pages.other"
// (plus "zero", "two", "few", "many" where the language needs them).
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed locales
var embedded embed.FS

// Fallback is the language every other catalog falls back to.
const Fallback = "en"

// Args are named placeholder values.
type Args map[string]any

// Bundle is a set of catalogs, one per language.
type Bundle struct {
	mu       sync.RWMutex
	catalogs map[string]map[string]string
	sources  map[string]map[string]string // lang → key → file that defined it
}

// NewBundle returns an empty bundle.
func NewBundle() *Bundle {
	return &Bundle{catalogs: map[string]map[string]string{}, sources: map[string]map[string]string{}}
}

var (
	defaultOnce   sync.Once
	defaultBundle *Bundle
	defaultErr    error
)

// Default returns the process-wide bundle loaded from the embedded catalogs.
// It panics if the embedded catalogs are malformed (caught by tests).
func Default() *Bundle {
	defaultOnce.Do(func() {
		defaultBundle = NewBundle()
		sub, _ := fs.Sub(embedded, "locales")
		defaultErr = defaultBundle.AddFS(sub)
	})
	if defaultErr != nil {
		panic(defaultErr)
	}
	return defaultBundle
}

// Embedded returns the embedded locales directory (locales/<lang>/*.json).
func Embedded() fs.FS {
	sub, _ := fs.Sub(embedded, "locales")
	return sub
}

var langPattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// AddFS merges every <lang>/*.json file of fsys into the bundle. Later files
// override earlier keys. Malformed files are reported with their path.
func (b *Bundle) AddFS(fsys fs.FS) error {
	var errs []error
	langs, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("i18n: %w", err)
	}
	for _, l := range langs {
		if !l.IsDir() {
			continue
		}
		lang := l.Name()
		if !langPattern.MatchString(lang) {
			errs = append(errs, fmt.Errorf("i18n: %q is not a language tag", lang))
			continue
		}
		files, err := fs.ReadDir(fsys, lang)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, f := range files {
			if f.IsDir() || path.Ext(f.Name()) != ".json" {
				continue
			}
			p := lang + "/" + f.Name()
			data, err := fs.ReadFile(fsys, p)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if err := b.AddJSON(lang, p, data); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// AddDir merges catalogs from a directory on disk (e.g. <data>/locales). A missing directory is not an error.
func (b *Bundle) AddDir(dir string) error {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return b.AddFS(os.DirFS(dir))
}

// AddJSON merges one flat JSON catalog for lang. source names the file in errors.
func (b *Bundle) AddJSON(lang, source string, data []byte) error {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("i18n: %s: malformed catalog (want a flat {\"key\": \"text\"} object): %w", source, err)
	}
	for k, v := range m {
		if strings.TrimSpace(k) == "" || strings.ContainsAny(k, " \t\n") {
			return fmt.Errorf("i18n: %s: invalid key %q", source, k)
		}
		if err := checkBraces(v); err != nil {
			return fmt.Errorf("i18n: %s: key %q: %w", source, k, err)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.catalogs[lang] == nil {
		b.catalogs[lang] = map[string]string{}
		b.sources[lang] = map[string]string{}
	}
	for k, v := range m {
		b.catalogs[lang][k] = v
		b.sources[lang][k] = source
	}
	return nil
}

func checkBraces(s string) error {
	depth := 0
	for _, r := range s {
		switch r {
		case '{':
			depth++
			if depth > 1 {
				return errors.New("nested '{' in placeholder")
			}
		case '}':
			depth--
			if depth < 0 {
				return errors.New("unbalanced '}'")
			}
		}
	}
	if depth != 0 {
		return errors.New("unclosed '{'")
	}
	return nil
}

// Languages returns the loaded language tags, English first.
func (b *Bundle) Languages() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.catalogs))
	for l := range b.catalogs {
		if l != Fallback {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	if _, ok := b.catalogs[Fallback]; ok {
		out = append([]string{Fallback}, out...)
	}
	return out
}

// Has reports whether lang has a catalog.
func (b *Bundle) Has(lang string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.catalogs[lang]
	return ok
}

// Lookup returns the raw text of key in lang, falling back to the base
// language ("pt-BR" → "pt") and then English.
func (b *Bundle) Lookup(lang, key string) (string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, l := range fallbackChain(lang) {
		if v, ok := b.catalogs[l][key]; ok {
			return v, true
		}
	}
	return "", false
}

func fallbackChain(lang string) []string {
	chain := []string{}
	if lang != "" {
		chain = append(chain, lang)
		if i := strings.IndexByte(lang, '-'); i > 0 {
			chain = append(chain, lang[:i])
		}
	}
	return append(chain, Fallback)
}

// Translate renders key in lang with args (see T for the argument forms).
// A missing key renders as the key itself so gaps are visible, never blank.
func (b *Bundle) Translate(lang, key string, args ...any) string {
	a := toArgs(args)
	if n, ok := countOf(a); ok {
		form := PluralForm(lang, n)
		if v, ok := b.Lookup(lang, key+"."+form); ok {
			return format(v, a, lang)
		}
		if v, ok := b.Lookup(lang, key+".other"); ok {
			return format(v, a, lang)
		}
	}
	v, ok := b.Lookup(lang, key)
	if !ok {
		return key
	}
	return format(v, a, lang)
}

func toArgs(args []any) Args {
	if len(args) == 0 {
		return nil
	}
	if len(args) == 1 {
		switch m := args[0].(type) {
		case Args:
			return m
		case map[string]any:
			return m
		case map[string]string:
			out := make(Args, len(m))
			for k, v := range m {
				out[k] = v
			}
			return out
		}
	}
	out := Args{}
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok {
			out[k] = args[i+1]
		}
	}
	return out
}

func countOf(a Args) (float64, bool) {
	v, ok := a["count"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	}
	return 0, false
}

func format(text string, a Args, lang string) string {
	if len(a) == 0 || !strings.Contains(text, "{") {
		return text
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(text, '{')
		if i < 0 {
			b.WriteString(text)
			break
		}
		j := strings.IndexByte(text[i:], '}')
		if j < 0 {
			b.WriteString(text)
			break
		}
		name := text[i+1 : i+j]
		b.WriteString(text[:i])
		if v, ok := a[name]; ok {
			b.WriteString(formatValue(v, lang))
		} else {
			b.WriteString(text[i : i+j+1])
		}
		text = text[i+j+1:]
	}
	return b.String()
}

func formatValue(v any, lang string) string {
	switch n := v.(type) {
	case int:
		return FormatInt(int64(n), lang)
	case int64:
		return FormatInt(n, lang)
	case float64:
		if n == float64(int64(n)) {
			return FormatInt(int64(n), lang)
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	case string:
		return n
	case fmt.Stringer:
		return n.String()
	}
	return fmt.Sprint(v)
}

// FormatInt formats an integer with the language's digit grouping (1,234 / 1.234 / 1 234).
func FormatInt(n int64, lang string) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	sep := ","
	switch base(lang) {
	case "de", "es", "it", "pt", "nl", "id", "tr", "da":
		sep = "."
	case "fr", "ru", "uk", "pl", "cs", "sv", "fi", "nb":
		sep = " "
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func base(lang string) string {
	if i := strings.IndexByte(lang, '-'); i > 0 {
		return strings.ToLower(lang[:i])
	}
	return strings.ToLower(lang)
}

// PluralForm returns the CLDR-style plural category (zero, one, two, few, many, other)
// for n in lang, using simplified rules for the common language families.
func PluralForm(lang string, n float64) string {
	integer := n == float64(int64(n))
	i := int64(n)
	switch base(lang) {
	case "ja", "zh", "ko", "th", "vi", "id", "ms":
		return "other"
	case "fr", "pt":
		if n >= 0 && n < 2 {
			return "one"
		}
		return "other"
	case "ru", "uk", "be", "sr", "hr", "bs":
		if !integer {
			return "other"
		}
		switch {
		case i%10 == 1 && i%100 != 11:
			return "one"
		case i%10 >= 2 && i%10 <= 4 && (i%100 < 12 || i%100 > 14):
			return "few"
		default:
			return "many"
		}
	case "pl":
		if !integer {
			return "other"
		}
		switch {
		case i == 1:
			return "one"
		case i%10 >= 2 && i%10 <= 4 && (i%100 < 12 || i%100 > 14):
			return "few"
		default:
			return "many"
		}
	case "ar":
		if !integer {
			return "other"
		}
		switch {
		case i == 0:
			return "zero"
		case i == 1:
			return "one"
		case i == 2:
			return "two"
		case i%100 >= 3 && i%100 <= 10:
			return "few"
		case i%100 >= 11:
			return "many"
		default:
			return "other"
		}
	case "he":
		if i == 1 && integer {
			return "one"
		}
		if i == 2 && integer {
			return "two"
		}
		return "other"
	default:
		if n == 1 {
			return "one"
		}
		return "other"
	}
}

var rtlLangs = map[string]bool{"ar": true, "he": true, "fa": true, "ur": true, "yi": true, "ps": true, "dv": true, "ckb": true, "sd": true, "ug": true}

// IsRTL reports whether lang is written right to left.
func IsRTL(lang string) bool { return rtlLangs[base(lang)] }

// Dir returns "rtl" or "ltr" for the <html dir> attribute.
func Dir(lang string) string {
	if IsRTL(lang) {
		return "rtl"
	}
	return "ltr"
}

// ---- context -------------------------------------------------------------

type ctxKey struct{}

// WithLocale returns a context carrying lang.
func WithLocale(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, ctxKey{}, lang)
}

// LocaleFrom returns the language set by WithLocale, or English.
func LocaleFrom(ctx context.Context) string {
	if ctx != nil {
		if l, ok := ctx.Value(ctxKey{}).(string); ok && l != "" {
			return l
		}
	}
	return Fallback
}

// T translates key into the context's language using the default bundle.
//
//	i18n.T(ctx, "jobs.state.queued")
//	i18n.T(ctx, "greeting", "name", "Rin")               // name/value pairs
//	i18n.T(ctx, "pages", i18n.Args{"count": 3})          // plural: pages.one / pages.other
func T(ctx context.Context, key string, args ...any) string {
	return Default().Translate(LocaleFrom(ctx), key, args...)
}

// ---- negotiation ---------------------------------------------------------

// Negotiate picks the best supported language. Candidates are tried in
// order: the explicit user preference, the cookie value, then the
// Accept-Language header; the first supported match wins, else fallback.
func Negotiate(supported []string, fallback, userPref, cookie, acceptLanguage string) string {
	for _, c := range []string{userPref, cookie} {
		if m := match(supported, c); m != "" {
			return m
		}
	}
	for _, tag := range ParseAcceptLanguage(acceptLanguage) {
		if m := match(supported, tag); m != "" {
			return m
		}
	}
	if fallback != "" {
		return fallback
	}
	return Fallback
}

func match(supported []string, tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" || tag == "*" {
		return ""
	}
	lower := strings.ToLower(tag)
	for _, s := range supported {
		if strings.ToLower(s) == lower {
			return s
		}
	}
	b := base(tag)
	for _, s := range supported {
		if strings.ToLower(s) == b {
			return s
		}
	}
	for _, s := range supported {
		if base(s) == b {
			return s
		}
	}
	return ""
}

// ParseAcceptLanguage returns the tags of an Accept-Language header ordered by quality.
func ParseAcceptLanguage(h string) []string {
	type tq struct {
		tag string
		q   float64
		i   int
	}
	var list []tq
	for i, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, q := part, 1.0
		if j := strings.IndexByte(part, ';'); j >= 0 {
			tag = strings.TrimSpace(part[:j])
			params := part[j+1:]
			if k := strings.Index(params, "q="); k >= 0 {
				if f, err := strconv.ParseFloat(strings.TrimSpace(params[k+2:]), 64); err == nil {
					q = f
				}
			}
		}
		if q <= 0 {
			continue
		}
		list = append(list, tq{tag, q, i})
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].q > list[b].q })
	out := make([]string, len(list))
	for i, t := range list {
		out[i] = t.tag
	}
	return out
}

// ---- validation ----------------------------------------------------------

var placeholder = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// Validate checks catalog consistency: every key of a non-English catalog
// must exist in English (or be a plural form of an English key) and use the
// same placeholders, and no English text may be empty.
func (b *Bundle) Validate() error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	en := b.catalogs[Fallback]
	if len(en) == 0 {
		return errors.New("i18n: English catalog is missing")
	}
	var errs []error
	for k, v := range en {
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("i18n: en: %q is empty (%s)", k, b.sources[Fallback][k]))
		}
	}
	for lang, cat := range b.catalogs {
		if lang == Fallback {
			continue
		}
		for k, v := range cat {
			ref, ok := en[k]
			if !ok {
				if pk, ok2 := pluralBase(k); ok2 {
					ref, ok = en[pk+".other"]
				}
			}
			if !ok {
				errs = append(errs, fmt.Errorf("i18n: %s: key %q (%s) does not exist in English", lang, k, b.sources[lang][k]))
				continue
			}
			if !samePlaceholders(ref, v) {
				errs = append(errs, fmt.Errorf("i18n: %s: key %q uses placeholders %v, English uses %v", lang, k, placeholders(v), placeholders(ref)))
			}
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errors.Join(errs...)
}

// Missing returns the English keys lang does not translate (for the translation status page).
func (b *Bundle) Missing(lang string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []string
	for k := range b.catalogs[Fallback] {
		if _, ok := b.catalogs[lang][k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func pluralBase(k string) (string, bool) {
	for _, f := range []string{".zero", ".one", ".two", ".few", ".many", ".other"} {
		if strings.HasSuffix(k, f) {
			return strings.TrimSuffix(k, f), true
		}
	}
	return "", false
}

func placeholders(s string) []string {
	set := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		set[m[1]] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func samePlaceholders(a, b string) bool {
	pa, pb := placeholders(a), placeholders(b)
	// Translations may drop {count} (e.g. Japanese counters) but must not invent names.
	set := map[string]bool{}
	for _, p := range pa {
		set[p] = true
	}
	for _, p := range pb {
		if !set[p] {
			return false
		}
	}
	return true
}
