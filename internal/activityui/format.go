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

func gpuTime(ctx context.Context, seconds float64) string {
	minutes := int(math.Ceil(max(0, seconds) / 60))
	return i18n.T(ctx, "activity.time.gpu", "hours", minutes/60, "minutes", minutes%60)
}

func spend(usd float64) string { return fmt.Sprintf("$%.6f", max(0, usd)) }
func percent(p float64) int {
	if math.IsNaN(p) || math.IsInf(p, 0) {
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
