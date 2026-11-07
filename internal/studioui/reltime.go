package studioui

import (
	"context"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// relativeTime is the compact "when" of the B2 project cards ("2 h ago",
// "yesterday", "last week"), localized; older edits show the date.
func relativeTime(ctx context.Context, t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return i18n.T(ctx, "bible.time.now")
	case d < time.Hour:
		return i18n.T(ctx, "bible.time.minutes", "count", int(d/time.Minute))
	case d < 24*time.Hour:
		return i18n.T(ctx, "bible.time.hours", "count", int(d/time.Hour))
	case d < 48*time.Hour:
		return i18n.T(ctx, "bible.time.yesterday")
	case d < 7*24*time.Hour:
		return i18n.T(ctx, "bible.time.days", "count", int(d/(24*time.Hour)))
	case d < 14*24*time.Hour:
		return i18n.T(ctx, "bible.time.last_week")
	case d < 35*24*time.Hour:
		return i18n.T(ctx, "bible.time.weeks", "count", int(d/(7*24*time.Hour)))
	}
	layout := "Jan 2, 2006"
	if strings.HasPrefix(i18n.LocaleFrom(ctx), "ja") {
		layout = "2006年1月2日"
	}
	return t.UTC().Format(layout)
}

// projectMeta is the card meta line: "Ch. 8 · 2 h ago", or "Premise only"
// before the first chapter exists.
func projectMeta(ctx context.Context, p studio.ProjectSummary, now time.Time) string {
	if p.LatestChapterNumber <= 0 {
		return i18n.T(ctx, "bible.projects.premise")
	}
	return i18n.T(ctx, "bible.projects.chapter_when", "number", p.LatestChapterNumber, "when", relativeTime(ctx, p.UpdatedAt, now))
}

// projectBadge uses the words the B2 artboard uses: the role on a project
// shared with you, "Script" while a chapter has a script but no pages yet,
// otherwise "Draft" (publishing does not exist yet, so nothing is published).
// "Archived" is the one state the artboard does not draw.
func projectBadge(ctx context.Context, p studio.ProjectSummary) ui.BadgeSpec {
	switch projectState(p) {
	case "shared":
		return ui.BadgeSpec{Tone: ui.ToneMuted, Text: i18n.T(ctx, "bible.member."+p.Role)}
	case "archived":
		return ui.BadgeSpec{Tone: ui.ToneMuted, Text: i18n.T(ctx, "studioui.status.archived")}
	case "script":
		return ui.BadgeSpec{Tone: ui.ToneInfo, Text: i18n.T(ctx, "bible.projects.badge.script")}
	default:
		return ui.BadgeSpec{Tone: ui.ToneNeutral, Text: i18n.T(ctx, "studioui.status.draft")}
	}
}

// projectStates are the B2 status filter values, matching the card badges.
var projectStates = []string{"draft", "script", "shared", "archived"}

// projectState is the state a B2 card shows and the status filter matches.
func projectState(p studio.ProjectSummary) string {
	switch {
	case p.Role != "" && p.Role != studio.RoleOwner:
		return "shared"
	case p.Status == "archived":
		return "archived"
	case p.ChapterCount > 0 && p.PageCount == 0:
		return "script"
	default:
		return "draft"
	}
}
