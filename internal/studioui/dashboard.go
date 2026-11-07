package studioui

import (
	"context"
	"strconv"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/dashboard"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

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
		meta := i18n.T(ctx, "studioui.dashboard.meta", "chapters", project.ChapterCount, "status", project.Status)
		chapterLine := ""
		if project.LatestChapterID != "" {
			chapterLine = i18n.T(ctx, "studioui.dashboard.chapter", "number", project.LatestChapterNumber)
		}
		out = append(out, dashboard.Project{
			ID: project.ID, Title: project.Title, Href: href, Meta: meta,
			ThumbLabel:  i18n.T(ctx, "studioui.dashboard.thumb"),
			Badges:      []ui.BadgeSpec{{Tone: ui.ToneNeutral, Icon: "file-pencil", Text: i18n.T(ctx, "studioui.dashboard.script")}},
			ChapterLine: chapterLine, EditorHref: href, EditorLabel: i18n.T(ctx, "studioui.dashboard.continue"),
			PageLabel:  i18n.T(ctx, "studioui.dashboard.page_label"),
			MobileMeta: strconv.Itoa(project.ChapterCount), UpdatedAt: project.UpdatedAt,
		})
	}
	return out, nil
}
