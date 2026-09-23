package studio

import (
	"context"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func wizardTitle(v WizardView) string {
	if v.Data().Title != "" {
		return v.Data().Title
	}
	if v.Data().Premise == "" {
		return "?"
	}
	return SuggestTitle(v.Data().Premise)
}

func genreList(ctx context.Context, g []string) string {
	if len(g) == 0 {
		return "–"
	}
	out := make([]string, len(g))
	for i, x := range g {
		out[i] = i18n.T(ctx, "genre."+x)
	}
	return strings.Join(out, " · ")
}

func formatLine(ctx context.Context, d DraftData) string {
	if d.Format == "" {
		return "–"
	}
	return i18n.T(ctx, "studio.format."+d.Format)
}

func pageSizeOptions() []ui.Option {
	out := make([]ui.Option, len(PageSizes))
	for i, p := range PageSizes {
		out[i] = ui.Option{Value: p.Key, Label: p.Label}
	}
	return out
}

func storyOptions(ctx context.Context, list []StoryTemplate) []ui.Option {
	out := []ui.Option{{Value: "", Label: i18n.T(ctx, "studio.wizard.structure.auto")}}
	for _, t := range list {
		out = append(out, ui.Option{Value: t.ID, Label: t.Name})
	}
	return out
}

func statusOptions(ctx context.Context) []ui.Option {
	out := []ui.Option{{Value: "", Label: i18n.T(ctx, "studio.projects.status_any")}}
	for _, s := range projectStatuses {
		label := i18n.T(ctx, "studio.badge."+s)
		if s == "shared" {
			label = i18n.T(ctx, "studio.projects.shared")
		}
		out = append(out, ui.Option{Value: s, Label: i18n.T(ctx, "studio.projects.status_is", "status", label)})
	}
	return out
}

func formatOptions(ctx context.Context) []ui.Option {
	out := []ui.Option{{Value: "", Label: i18n.T(ctx, "studio.projects.format_any")}}
	for _, f := range Formats {
		out = append(out, ui.Option{Value: f, Label: i18n.T(ctx, "studio.projects.format_is", "format", i18n.T(ctx, "studio.format."+f))})
	}
	return out
}

func sortOptions(ctx context.Context) []ui.Option {
	return []ui.Option{
		{Value: "", Label: i18n.T(ctx, "studio.projects.sort_updated")},
		{Value: "created", Label: i18n.T(ctx, "studio.projects.sort_created")},
		{Value: "title", Label: i18n.T(ctx, "studio.projects.sort_title")},
	}
}

// layoutBadge is the mono tag of a layout card: its own word (SPREAD, STRIP,
// SPLASH, GRID) or the real panel count.
func layoutBadge(t LayoutTemplate) string {
	if t.Badge == "" || strings.HasSuffix(t.Badge, "PANELS") {
		return itoa(t.PanelCount()) + " PANELS"
	}
	if t.Badge == "GRID" && strings.HasPrefix(t.ID, "grid:") {
		return "GRID · " + itoa(t.PanelCount()) + " PANELS"
	}
	return t.Badge
}

func jobErr(j *jobs.Job) string {
	if j == nil || j.Error == nil {
		return ""
	}
	return j.Error.Message
}

func templateTabs(ctx context.Context, v TemplatesView) []ui.SegLink {
	return []ui.SegLink{
		{Href: "/studio/templates?tab=story", Label: i18n.T(ctx, "studio.tpl.tab.story"), Current: v.Tab == TabStory},
		{Href: "/studio/templates?tab=layout", Label: i18n.T(ctx, "studio.tpl.tab.layout"), Current: v.Tab == TabLayout},
		{Href: "/studio/templates?tab=characters", Label: i18n.T(ctx, "studio.tpl.tab.characters"), Current: v.Tab == TabCharacters},
		{Href: "/studio/templates?tab=styles", Label: i18n.T(ctx, "studio.tpl.tab.styles"), Current: v.Tab == TabStyles},
	}
}

func mobileTemplateTabs(ctx context.Context, v TemplatesView) []ui.SegLink {
	return []ui.SegLink{
		{Href: "/studio/templates?tab=story", Label: i18n.T(ctx, "studio.tpl.tab.story"), Current: v.Tab == TabStory},
		{Href: "/studio/templates?tab=layout", Label: i18n.T(ctx, "studio.tpl.tab.layout"), Current: v.Tab == TabLayout},
		{Href: "/studio/templates?tab=characters", Label: i18n.T(ctx, "studio.tpl.tab.packs"), Current: v.Tab == TabCharacters || v.Tab == TabStyles},
	}
}

func packKind(tab string) string {
	if tab == TabStyles {
		return "style"
	}
	return "character"
}

func centroidX(p Polygon) float64 {
	x0, _, x1, _ := bbox(p)
	return round1((x0 + x1) / 2)
}

func centroidY(p Polygon) float64 {
	_, y0, _, y1 := bbox(p)
	return round1((y0+y1)/2 + 2)
}

func pageChoiceOptions(ctx context.Context, list []PageChoice) []ui.Option {
	out := make([]ui.Option, len(list))
	for i, p := range list {
		out[i] = ui.Option{Value: p.PageID, Label: i18n.T(ctx, "studio.tpl.page_choice", "title", p.Title, "ch", p.Chapter, "page", p.Page)}
	}
	return out
}

func spreadOptions(ctx context.Context, n int) []ui.Option {
	out := make([]ui.Option, n)
	for i := range out {
		out[i] = ui.Option{Value: itoa(i), Label: i18n.T(ctx, "studio.tpl.spread_page", "n", i+1)}
	}
	return out
}
