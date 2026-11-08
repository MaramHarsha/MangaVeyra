package backup

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// formatSize renders bytes as the design does ("18.2 GB").
func formatSize(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatInt(n, 10) + " B"
	}
	s := strconv.FormatFloat(f, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + units[i]
}

// viewerLocation is the signed-in member's time zone, else the schedule's, else UTC.
func viewerLocation(ctx context.Context, fallback string) *time.Location {
	if u := core.UserFrom(ctx); u != nil && u.Timezone != "" {
		if loc, err := time.LoadLocation(u.Timezone); err == nil {
			return loc
		}
	}
	if fallback != "" {
		if loc, err := time.LoadLocation(fallback); err == nil {
			return loc
		}
	}
	return time.UTC
}

// formatWhen renders "Today 03:00", "Yesterday 03:00" or "14 Sep 03:00".
func formatWhen(ctx context.Context, t, now time.Time, loc *time.Location) string {
	lt, ln := t.In(loc), now.In(loc)
	clock := lt.Format("15:04")
	ja := i18n.LocaleFrom(ctx) == "ja"
	sameDay := func(a, b time.Time) bool { return a.Year() == b.Year() && a.YearDay() == b.YearDay() }
	switch {
	case sameDay(lt, ln):
		return i18n.T(ctx, "backup.when.today", "time", clock)
	case sameDay(lt, ln.AddDate(0, 0, -1)):
		return i18n.T(ctx, "backup.when.yesterday", "time", clock)
	case lt.Year() == ln.Year():
		if ja {
			return lt.Format("1月2日 15:04")
		}
		return lt.Format("2 Jan 15:04")
	}
	if ja {
		return lt.Format("2006年1月2日 15:04")
	}
	return lt.Format("2 Jan 2006 15:04")
}

// formatDay renders "14 Sep" (Settings → Data "Last export").
func formatDay(ctx context.Context, t, now time.Time, loc *time.Location) string {
	lt, ln := t.In(loc), now.In(loc)
	ja := i18n.LocaleFrom(ctx) == "ja"
	if lt.Year() == ln.Year() {
		if ja {
			return lt.Format("1月2日")
		}
		return lt.Format("2 Jan")
	}
	if ja {
		return lt.Format("2006年1月2日")
	}
	return lt.Format("2 Jan 2006")
}

// LastExportText is "14 Sep · 6.2 GB" for Settings → Data.
func LastExportText(ctx context.Context, b Backup) string {
	return formatDay(ctx, b.CreatedAt, time.Now(), viewerLocation(ctx, "")) + " · " + formatSize(b.Bytes)
}

// LastExportURL downloads a member's own export.
func LastExportURL(b Backup) string { return "/settings/data/exports/" + b.ID }

func scheduleText(ctx context.Context, sc Schedule) string {
	switch sc.Frequency {
	case FrequencyDaily:
		return i18n.T(ctx, "backup.schedule.daily_keep", "keep", sc.Keep)
	case FrequencyWeekly:
		return i18n.T(ctx, "backup.schedule.weekly_keep", "keep", sc.Keep)
	}
	return i18n.T(ctx, "backup.schedule.off")
}

func kindLabel(ctx context.Context, kind string) string {
	return i18n.T(ctx, "backup.kind."+kind)
}
