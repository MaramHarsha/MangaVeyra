package publish

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// errText translates a field error key ("" stays empty).
func errText(ctx context.Context, key string) string {
	if key == "" {
		return ""
	}
	return i18n.T(ctx, key)
}

func langName(ctx context.Context, lang string) string {
	if slices.Contains(pagedoc.ContentLanguages, lang) {
		return i18n.T(ctx, "publish.language."+lang)
	}
	return lang
}

// langOptions lists the chapter's languages; ones with unlettered bubbles are
// shown but cannot be chosen.
func langOptions(ctx context.Context, v publishView) []ui.Option {
	out := make([]ui.Option, 0, len(v.Languages))
	for _, l := range v.Languages {
		label := langName(ctx, l.Value)
		if l.Missing > 0 {
			label = i18n.T(ctx, "publish.language.missing", "language", label, "count", l.Missing)
		}
		out = append(out, ui.Option{Value: l.Value, Label: label, Disabled: l.Missing > 0 && l.Value != v.Lang})
	}
	return out
}

func contentLangOptions(ctx context.Context) []ui.Option {
	out := make([]ui.Option, 0, len(pagedoc.ContentLanguages))
	for _, l := range pagedoc.ContentLanguages {
		out = append(out, ui.Option{Value: l, Label: langName(ctx, l)})
	}
	return out
}

// readyAside names another language that is ready to publish ("+ Japanese ready").
func readyAside(ctx context.Context, v publishView) string {
	if len(v.OtherReady) == 0 {
		return ""
	}
	return i18n.T(ctx, "publish.language.ready", "language", langName(ctx, v.OtherReady[0]))
}

func choiceOptions(ctx context.Context, prefix string, values []string) []ui.Option {
	out := make([]ui.Option, 0, len(values))
	for _, v := range values {
		out = append(out, ui.Option{Value: v, Label: i18n.T(ctx, prefix+v)})
	}
	return out
}

// aiOptions offers the recorded AI label and anything declaring more AI
// involvement; honest provenance never goes below the project history.
func aiOptions(ctx context.Context, recorded string) []ui.Option {
	out := []ui.Option{}
	for _, v := range aiLevels {
		if aiRank(v) >= aiRank(recorded) {
			out = append(out, ui.Option{Value: v, Label: i18n.T(ctx, "publish.ai."+v)})
		}
	}
	return out
}

// compactCount writes 12400 as 12.4k.
func compactCount(n int) string {
	switch {
	case n >= 1_000_000:
		return trimZero(float64(n)/1_000_000) + "M"
	case n >= 1_000:
		return trimZero(float64(n)/1_000) + "k"
	default:
		return strconv.Itoa(n)
	}
}

func trimZero(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(float64(int(f*10))/10, 'f', 1, 64), ".0")
}

var visibilityRank = map[string]int{VisibilityPublic: 0, VisibilityUnlisted: 1, VisibilityMembers: 2, VisibilityPrivate: 3}

// stricter reports whether the series visibility hides more than the chapter's.
func stricter(series, chapter string) bool { return visibilityRank[series] > visibilityRank[chapter] }

// locFrom is the signed-in user's time zone (UTC when unset).
func locFrom(ctx context.Context) *time.Location {
	if u := core.UserFrom(ctx); u != nil && u.Timezone != "" {
		if loc, err := time.LoadLocation(u.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

func jobErrorKey(j *jobs.Job) string {
	if j == nil || j.Error == nil {
		return jobs.PublicErrorKey(jobs.ErrCodeInternal)
	}
	return jobs.PublicErrorKey(j.Error.Code)
}

func (v publishView) seriesTitle() string {
	if v.HasSeries {
		return v.Series.Title
	}
	return v.Project.Title
}

// publishedMeta is "12.4k followers notified · English · Japanese ready to publish".
func publishedMeta(ctx context.Context, v publishView) string {
	var parts []string
	switch {
	case !v.live() && v.Pub.ScheduledAt != nil:
		parts = append(parts, i18n.T(ctx, "publish.live.goes_live", "date", v.Pub.ScheduledAt.In(locFrom(ctx)).Format("Mon 2 Jan 2006 15:04")))
	case v.Pub.Flags.Notified != nil:
		parts = append(parts, i18n.T(ctx, "publish.live.notified", "count", compactCount(*v.Pub.Flags.Notified)))
	case v.Pub.Flags.Notify:
		parts = append(parts, i18n.T(ctx, "publish.live.notifying"))
	}
	parts = append(parts, langName(ctx, v.Lang))
	if len(v.OtherReady) > 0 {
		parts = append(parts, i18n.T(ctx, "publish.live.ready", "language", langName(ctx, v.OtherReady[0])))
	}
	return strings.Join(parts, " · ")
}

func joinList(v []string) string { return strings.Join(v, ", ") }

func tagList(s string) []string { return normalizeTags([]string{s}, false) }
