package studioui

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/dashboard"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// projectStatuses are the values projects.status allows.
var projectStatuses = []string{"draft", "active", "archived"}

// RecentProjects adapts Studio's real membership-scoped summaries to the
// dashboard DTO. Project summaries contain no private adaptation source.
func (m *Module) RecentProjects(ctx context.Context, userID string, limit int) ([]dashboard.Project, error) {
	projects, err := m.studio.ListProjects(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]dashboard.Project, 0, len(projects))
	for _, project := range projects {
		href := storyPath(project.ID, project.LatestChapterID)
		status := projectStatus(project.Status)
		statusLabel := i18n.T(ctx, "studioui.status."+status)
		meta := i18n.T(ctx, "studioui.dashboard.chapters", "count", project.ChapterCount) + " · " + statusLabel
		chapterLine := ""
		if project.LatestChapterID != "" {
			chapterLine = i18n.T(ctx, "studioui.dashboard.chapter", "number", project.LatestChapterNumber)
		}
		// B1 cards carry a status badge and a reading-format badge (Draft · RTL).
		badges := []ui.BadgeSpec{{Tone: statusTone(status), Text: statusLabel}}
		if contains(formatValues, project.Format) {
			badges = append(badges, ui.BadgeSpec{Tone: ui.ToneMuted, Text: i18n.T(ctx, "studioui.format_badge."+project.Format)})
		}
		out = append(out, dashboard.Project{
			ID: project.ID, Title: project.Title, Href: href, Meta: meta,
			ThumbLabel: i18n.T(ctx, "studioui.dashboard.thumb"), Badges: badges,
			ChapterLine: chapterLine, EditorHref: href, EditorLabel: i18n.T(ctx, "studioui.dashboard.continue"),
			PageLabel:  i18n.T(ctx, "studioui.dashboard.page_label"),
			MobileMeta: meta, UpdatedAt: project.UpdatedAt,
		})
	}
	return out, nil
}

// projectStatus normalises a stored status to one with a translated label.
func projectStatus(status string) string {
	if contains(projectStatuses, status) {
		return status
	}
	return "draft"
}

func statusTone(status string) ui.Tone {
	switch status {
	case "active":
		return ui.ToneInfo
	case "archived":
		return ui.ToneMuted
	default:
		return ui.ToneNeutral
	}
}
