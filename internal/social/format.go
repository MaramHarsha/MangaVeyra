package social

import (
	"context"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func isJA(ctx context.Context) bool { return strings.HasPrefix(i18n.LocaleFrom(ctx), "ja") }

// compactCount renders follower counts the way E5 and F1 do ("12.4k").
// Japanese uses 万 from ten thousand.
func compactCount(ctx context.Context, n int) string {
	if n < 0 {
		n = 0
	}
	lang := i18n.LocaleFrom(ctx)
	one := func(v float64, unit string) string {
		v = math.Floor(v*10) / 10
		s := strconv.FormatFloat(v, 'f', 1, 64)
		return strings.TrimSuffix(s, ".0") + unit
	}
	if isJA(ctx) {
		if n >= 10000 {
			return one(float64(n)/10000, "万")
		}
		return i18n.FormatInt(int64(n), lang)
	}
	switch {
	case n >= 1_000_000:
		return one(float64(n)/1_000_000, "M")
	case n >= 1000:
		return one(float64(n)/1000, "k")
	}
	return strconv.Itoa(n)
}

// chapterNumber is the big row number: two digits for whole numbers ("08"),
// the decimal otherwise ("7.5").
func chapterNumber(n float64) string {
	if n == math.Trunc(n) && n >= 0 && n < 10 {
		return "0" + strconv.FormatFloat(n, 'f', 0, 64)
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// numberParam is the chapter number as it appears in reader URLs and labels.
func numberParam(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// shortDate is the chapter-row date: "Sat 27 Sep" for the last week,
// "20 Sep" this year, "20 Sep 2025" before (Japanese: 9月27日(土)).
func shortDate(ctx context.Context, t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t, now = t.UTC(), now.UTC()
	recent := now.Sub(t) < 7*24*time.Hour
	if isJA(ctx) {
		wd := []string{"日", "月", "火", "水", "木", "金", "土"}[t.Weekday()]
		switch {
		case recent:
			return t.Format("1月2日") + "(" + wd + ")"
		case t.Year() == now.Year():
			return t.Format("1月2日")
		}
		return t.Format("2006年1月2日")
	}
	switch {
	case recent:
		return t.Format("Mon 2 Jan")
	case t.Year() == now.Year():
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
}

// longDate is "8 November 2026" / "2026年11月8日".
func longDate(ctx context.Context, t time.Time) string {
	if isJA(ctx) {
		return t.UTC().Format("2006年1月2日")
	}
	return t.UTC().Format("2 January 2006")
}

func formatLabel(ctx context.Context, format string) string {
	switch format {
	case "manga_rtl", "comic_ltr", "webtoon":
		return i18n.T(ctx, "social.format."+format)
	}
	return format
}

func ratingLabel(ctx context.Context, rating string) string {
	switch rating {
	case publish.RatingAll, publish.RatingTeen, publish.RatingMature, publish.RatingAdult:
		return i18n.T(ctx, "social.rating."+rating)
	}
	return rating
}

func statusLabel(ctx context.Context, status string) string {
	switch status {
	case publish.SeriesOngoing, publish.SeriesCompleted, publish.SeriesHiatus, publish.SeriesDraft:
		return i18n.T(ctx, "social.status."+status)
	}
	return status
}

func licenceLabel(ctx context.Context, licence string) string {
	switch licence {
	case "all_rights", "cc-by", "cc-by-sa", "cc-by-nc", "cc-by-nc-sa", "cc0":
		return i18n.T(ctx, "social.licence."+strings.ReplaceAll(licence, "-", "_"))
	}
	return licence
}

// aiRoles is the badge order of the per-role AI labels (E5 shows art first).
var aiRoles = []string{"art", "script", "lettering"}

// aiBadges turns the per-role AI labels into E5's provenance badges: a
// pencil for human work and sparkles for AI-assisted or AI work.
func aiBadges(ctx context.Context, label map[string]string) []ui.BadgeSpec {
	var out []ui.BadgeSpec
	for _, role := range aiRoles {
		v, ok := label[role]
		if !ok {
			continue
		}
		switch v {
		case ui.ProvenanceHuman:
			out = append(out, ui.BadgeSpec{Tone: ui.ToneText, Icon: "pencil", Text: i18n.T(ctx, "social.ai."+role+".human")})
		case ui.ProvenanceAIAssisted:
			out = append(out, ui.BadgeSpec{Tone: ui.ToneText, Icon: "sparkles", Text: i18n.T(ctx, "social.ai."+role+".ai_assisted")})
		case ui.ProvenanceAI:
			out = append(out, ui.BadgeSpec{Tone: ui.ToneText, Icon: "sparkles", Text: i18n.T(ctx, "social.ai."+role+".ai")})
		}
	}
	return out
}

// genreText is a readable genre from its slug ("slice_of_life" → "slice of life");
// badges set the case.
func genreText(slug string) string {
	return strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(slug))
}

// externalLink accepts only absolute http(s) links for support and profile
// links and returns the label to show (the given label, else the host).
func externalLink(raw, label string) (href, text string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", "", false
	}
	text = strings.TrimSpace(label)
	if text == "" {
		text = strings.TrimPrefix(u.Hostname(), "www.")
	}
	if r := []rune(text); len(r) > 60 {
		text = string(r[:60]) + "…"
	}
	return u.String(), text, true
}

// langCode is the short upper-case language label of E5's language stat.
func langCode(lang string) string {
	if i := strings.IndexByte(lang, '-'); i > 0 {
		return strings.ToUpper(lang[:i]) + lang[i:]
	}
	return strings.ToUpper(lang)
}

func withQuery(path string, kv ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			q.Set(kv[i], kv[i+1])
		}
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
