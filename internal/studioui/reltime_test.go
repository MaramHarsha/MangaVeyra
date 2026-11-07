package studioui

import (
	"context"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func TestRelativeTimeAndProjectMeta(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	en := i18n.WithLocale(context.Background(), "en")
	ja := i18n.WithLocale(context.Background(), "ja")
	for _, tc := range []struct {
		ago    time.Duration
		en, ja string
	}{
		{20 * time.Second, "just now", "たった今"},
		{5 * time.Minute, "5 min ago", "5分前"},
		{2 * time.Hour, "2 h ago", "2時間前"},
		{30 * time.Hour, "yesterday", "昨日"},
		{3 * 24 * time.Hour, "3 days ago", "3日前"},
		{9 * 24 * time.Hour, "last week", "先週"},
		{21 * 24 * time.Hour, "3 weeks ago", "3週間前"},
		{60 * 24 * time.Hour, "Sep 8, 2026", "2026年9月8日"},
	} {
		if got := relativeTime(en, now.Add(-tc.ago), now); got != tc.en {
			t.Errorf("en %v = %q, want %q", tc.ago, got, tc.en)
		}
		if got := relativeTime(ja, now.Add(-tc.ago), now); got != tc.ja {
			t.Errorf("ja %v = %q, want %q", tc.ago, got, tc.ja)
		}
	}
	p := studio.ProjectSummary{Project: studio.Project{UpdatedAt: now.Add(-2 * time.Hour)}, LatestChapterNumber: 8}
	if got := projectMeta(en, p, now); got != "Ch. 8 · 2 h ago" {
		t.Errorf("meta = %q", got)
	}
	if got := projectMeta(ja, p, now); got != "第8話 · 2時間前" {
		t.Errorf("ja meta = %q", got)
	}
	if got := projectMeta(en, studio.ProjectSummary{}, now); got != "Premise only" {
		t.Errorf("premise meta = %q", got)
	}
}

func TestProjectBadgeUsesTheDesignWords(t *testing.T) {
	ctx := i18n.WithLocale(context.Background(), "en")
	for _, tc := range []struct {
		name string
		p    studio.ProjectSummary
		want ui.BadgeSpec
	}{
		{"premise only", studio.ProjectSummary{Role: studio.RoleOwner, Project: studio.Project{Status: "draft"}}, ui.BadgeSpec{Tone: ui.ToneNeutral, Text: "Draft"}},
		{"script, no pages", studio.ProjectSummary{Role: studio.RoleOwner, ChapterCount: 1, Project: studio.Project{Status: "active"}}, ui.BadgeSpec{Tone: ui.ToneInfo, Text: "Script"}},
		{"pages", studio.ProjectSummary{Role: studio.RoleOwner, ChapterCount: 2, PageCount: 12, Project: studio.Project{Status: "active"}}, ui.BadgeSpec{Tone: ui.ToneNeutral, Text: "Draft"}},
		{"shared", studio.ProjectSummary{Role: studio.RoleEditor, ChapterCount: 2, PageCount: 3}, ui.BadgeSpec{Tone: ui.ToneMuted, Text: "Editor"}},
		{"archived", studio.ProjectSummary{Role: studio.RoleOwner, Project: studio.Project{Status: "archived"}}, ui.BadgeSpec{Tone: ui.ToneMuted, Text: "Archived"}},
	} {
		if got := projectBadge(ctx, tc.p); got != tc.want {
			t.Errorf("%s: badge = %+v, want %+v", tc.name, got, tc.want)
		}
		if got := projectBadge(ctx, tc.p).Text; got == "In progress" {
			t.Errorf("%s: undesigned badge word", tc.name)
		}
	}
}
