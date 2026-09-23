package studio

import (
	"context"
	"net/url"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func arcExists(arcs []Arc, id string) bool {
	for _, a := range arcs {
		if a.ID == id {
			return true
		}
	}
	return false
}

func arcIcon(v StoryView, arc Arc) string {
	if v.Chapter != nil && v.Chapter.ArcID == arc.ID {
		return "folder-open"
	}
	return "folder"
}

func chapterLabel(ctx context.Context, ch Chapter) string {
	if ch.Title == "" {
		return i18n.T(ctx, "studio.story.chapter_n", "n", ch.Number)
	}
	return i18n.T(ctx, "studio.story.chapter_label", "n", ch.Number, "title", ch.Title)
}

func chapterArc(v StoryView) string {
	if v.Chapter != nil {
		return v.Chapter.ArcID
	}
	if len(v.Arcs) > 0 {
		return v.Arcs[len(v.Arcs)-1].ID
	}
	return ""
}

func arcOptions(ctx context.Context, arcs []Arc) []ui.Option {
	out := []ui.Option{{Value: "", Label: i18n.T(ctx, "studio.story.no_arc")}}
	for _, a := range arcs {
		out = append(out, ui.Option{Value: a.ID, Label: a.Title})
	}
	return out
}

func treeMenuItems(ctx context.Context, v StoryView, path, id string) []ui.MenuItem {
	here := v.Here()
	rename := here
	if strings.Contains(rename, "?") {
		rename += "&rename=" + id
	} else {
		rename += "?rename=" + id
	}
	next := "?next=" + url.QueryEscape(here)
	items := []ui.MenuItem{
		{Href: rename, Label: i18n.T(ctx, "studio.story.rename"), Icon: "pencil"},
		{Href: v.Base() + path + "/move" + next + "&dir=up", Label: i18n.T(ctx, "studio.story.move_up"), Icon: "arrow-up", Post: true},
		{Href: v.Base() + path + "/move" + next + "&dir=down", Label: i18n.T(ctx, "studio.story.move_down"), Icon: "arrow-down", Post: true},
	}
	if strings.HasPrefix(path, "/chapters/") {
		for _, c := range v.Chapters {
			if c.ID == id {
				label, icon, val := i18n.T(ctx, "studio.story.lock_chapter"), "lock", "on"
				if c.Locked {
					label, icon, val = i18n.T(ctx, "studio.story.unlock_chapter"), "lock-open", "off"
				}
				if v.Access.Can(EditPages) || v.Access.Can(EditStory) {
					if !(c.Locked && v.Access.Actor.IsAgent()) {
						items = append(items, ui.MenuItem{Href: v.Base() + path + next + "&locked=" + val, Label: label, Icon: icon, Post: true})
					}
				}
			}
		}
	}
	return append(items, ui.MenuItem{Separator: true},
		ui.MenuItem{Href: v.Base() + path + "/delete" + next, Label: i18n.T(ctx, "studio.story.delete"), Icon: "trash", Post: true, Danger: true})
}

func joinComma(v []string) string { return strings.Join(v, ", ") }

// editLines returns the dialogue lines plus two blank rows for new lines.
func editLines(ls []DialogueLine) []DialogueLine {
	out := append([]DialogueLine{}, ls...)
	return append(out, DialogueLine{Kind: LineSpeech}, DialogueLine{Kind: LineSpeech})
}

func speakerOptions(ctx context.Context, chars []Character) []ui.Option {
	out := []ui.Option{{Value: "", Label: i18n.T(ctx, "studio.script.caption")}}
	for _, c := range chars {
		out = append(out, ui.Option{Value: c.ID, Label: c.Name})
	}
	return out
}

func lineKindOptions(ctx context.Context) []ui.Option {
	out := make([]ui.Option, 0, len(LineKinds))
	for _, k := range LineKinds {
		out = append(out, ui.Option{Value: k, Label: i18n.T(ctx, "studio.line."+k)})
	}
	return out
}

// TimesOfDay are the time-of-day choices of a scene.
var TimesOfDay = []string{"dawn", "morning", "noon", "afternoon", "dusk", "evening", "night", "midnight"}

func timeOptions(ctx context.Context, cur string) []ui.Option {
	out := []ui.Option{{Value: "", Label: i18n.T(ctx, "studio.story.time_unset")}}
	found := cur == ""
	for _, t := range TimesOfDay {
		out = append(out, ui.Option{Value: t, Label: i18n.T(ctx, "studio.time_of_day."+t)})
		found = found || t == cur
	}
	if !found {
		out = append(out, ui.Option{Value: cur, Label: cur})
	}
	return out
}

func locationOptions(ctx context.Context, items []SettingItem) []ui.Option {
	out := []ui.Option{{Value: "", Label: i18n.T(ctx, "studio.story.location_unset")}}
	for _, it := range items {
		out = append(out, ui.Option{Value: it.ID, Label: it.Name})
	}
	return out
}

func wordCount(s string) int { return len(strings.Fields(s)) }

func scriptMap(list []ScriptPanel) map[string]ScriptPanel {
	out := make(map[string]ScriptPanel, len(list))
	for _, p := range list {
		out[p.ID] = p
	}
	return out
}
