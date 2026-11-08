package activityui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

func age(ctx context.Context, then, now time.Time) string {
	d := max(time.Duration(0), now.Sub(then))
	switch {
	case d < time.Minute:
		return i18n.T(ctx, "activity.time.now")
	case d < time.Hour:
		return i18n.T(ctx, "activity.time.minutes", "count", int(d/time.Minute))
	case d < 24*time.Hour:
		return i18n.T(ctx, "activity.time.hours", "count", int(d/time.Hour))
	default:
		return i18n.T(ctx, "activity.time.days", "count", int(d/(24*time.Hour)))
	}
}

func timestamp(ctx context.Context, t time.Time) string {
	if i18n.LocaleFrom(ctx) == "ja" {
		return t.UTC().Format("2006年1月2日 15:04:05 UTC")
	}
	return t.UTC().Format("02 Jan 2006 15:04:05 UTC")
}

// gpuTime is the weekly GPU total, "3 h 10 m" as in the designed summary.
func gpuTime(ctx context.Context, seconds float64) string {
	if nonFinite(seconds) {
		seconds = 0
	}
	minutes := int(math.Ceil(max(0, seconds) / 60))
	return i18n.T(ctx, "activity.time.gpu", "hours", minutes/60, "minutes", minutes%60)
}

// shortDuration is one job's time: "38 s", "3 min" or "1 h 5 m".
func shortDuration(ctx context.Context, seconds float64) string {
	if nonFinite(seconds) {
		seconds = 0
	}
	s := max(0, seconds)
	switch {
	case s < 59.5:
		return i18n.T(ctx, "activity.time.seconds", "count", max(1, int(math.Round(s))))
	case s < 3570:
		return i18n.T(ctx, "activity.time.minutes_long", "count", int(math.Round(s/60)))
	}
	minutes := int(math.Round(s / 60))
	return i18n.T(ctx, "activity.time.gpu", "hours", minutes/60, "minutes", minutes%60)
}

// spend is remote spend on the member's own keys in the design's two-decimal
// form ("$4.18"); a recorded amount below one cent reads "<$0.01".
func spend(usd float64) string {
	switch {
	case nonFinite(usd) || !(usd > 0):
		return "$0.00"
	case usd < 0.005:
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", usd)
}

// nonFinite reports a non-finite value (NaN or ±Inf).
func nonFinite(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) }

func percent(p float64) int {
	if nonFinite(p) {
		return 0
	}
	return int(math.Round(min(1, max(0, p)) * 100))
}
func integer(n int) string { return strconv.Itoa(n) }

func weekStart(t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
}
