package admin

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// formatBytes renders a size with decimal units ("212 GB", "14.6 GB", "0 B").
func formatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v, i := float64(n), 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	switch {
	case i == 0:
		return strconv.FormatInt(n, 10) + " B"
	case v >= 100:
		return strconv.FormatFloat(v, 'f', 0, 64) + " " + units[i]
	default:
		s := strconv.FormatFloat(v, 'f', 1, 64)
		s = strings.TrimSuffix(s, ".0")
		return s + " " + units[i]
	}
}

// formatMB renders a megabyte limit ("50 GB", "512 MB").
func formatMB(mb int) string { return formatBytes(int64(mb) * 1_000_000) }

// ago is a compact relative time ("2 h ago", "yesterday"), or the date.
func ago(ctx context.Context, t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return i18n.T(ctx, "admin.time.now")
	case d < time.Hour:
		return i18n.T(ctx, "admin.time.minutes", "count", int(d/time.Minute))
	case d < 24*time.Hour:
		return i18n.T(ctx, "admin.time.hours", "count", int(d/time.Hour))
	case d < 48*time.Hour:
		return i18n.T(ctx, "admin.time.yesterday")
	case d < 30*24*time.Hour:
		return i18n.T(ctx, "admin.time.days", "count", int(d/(24*time.Hour)))
	}
	return dateText(ctx, t)
}

// dateText is a short localized date.
func dateText(ctx context.Context, t time.Time) string {
	if strings.HasPrefix(i18n.LocaleFrom(ctx), "ja") {
		return t.UTC().Format("2006年1月2日")
	}
	return t.UTC().Format("2 Jan 2006")
}

// duration renders an uptime ("3 d 4 h", "2 h 12 min", "4 min").
func duration(ctx context.Context, d time.Duration) string {
	days, hours, mins := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case days > 0:
		return i18n.T(ctx, "admin.uptime.days", "days", days, "hours", hours)
	case hours > 0:
		return i18n.T(ctx, "admin.uptime.hours", "hours", hours, "minutes", mins)
	}
	return i18n.T(ctx, "admin.uptime.minutes", "minutes", mins)
}

// number renders a chapter number without a trailing ".0".
func number(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// clip shortens s to max runes with an ellipsis.
func clip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}

// money renders a USD limit for the limits card ("$5", "$2.50").
func money(v float64) string {
	if v == float64(int64(v)) {
		return "$" + strconv.FormatInt(int64(v), 10)
	}
	return "$" + strconv.FormatFloat(v, 'f', 2, 64)
}

// amount renders a USD limit for an input value ("5", "2.5").
func amount(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func itoa(n int) string { return strconv.Itoa(n) }
