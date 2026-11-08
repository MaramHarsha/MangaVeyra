package activityui

import (
	"context"
	"strconv"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func boolString(v bool) string { return strconv.FormatBool(v) }

func notificationTabs(ctx context.Context, cat string) []ui.SegLink {
	var tabs []ui.SegLink
	for _, key := range []string{"all", "studio", "reading", "system"} {
		tabs = append(tabs, ui.SegLink{Href: notificationsURL(key, ""), Label: i18n.T(ctx, "activity.notifications."+key), Current: key == cat})
	}
	return tabs
}

func jobMenu(ctx context.Context, j jobView) []ui.MenuItem {
	items := []ui.MenuItem{{Href: j.DetailURL, Label: i18n.T(ctx, "activity.queue.details"), Icon: "list-details"}}
	if j.StudioURL != "" {
		items = append(items, ui.MenuItem{Href: j.StudioURL, Label: i18n.T(ctx, "activity.queue.open_studio"), Icon: "arrow-right"})
	}
	if j.Cancelable {
		items = append(items, ui.MenuItem{Href: j.CancelURL, Label: i18n.T(ctx, "activity.queue.cancel"), Icon: "x", Post: true, Danger: true})
	}
	return items
}

func workerMenu(actions []workerAction) []ui.MenuItem {
	var items []ui.MenuItem
	for _, a := range actions {
		items = append(items, ui.MenuItem{Href: a.URL, Label: a.Label, Icon: "cpu", Post: true})
	}
	return items
}
