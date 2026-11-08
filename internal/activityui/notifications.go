package activityui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

const listLimit = 20

var studioKinds = []string{notify.KindJobDone, notify.KindJobFailed, notify.KindComment, notify.KindMention, notify.KindReview, notify.KindLockTraining}
var readingKinds = []string{notify.KindChapter, notify.KindFollow}

type notificationView struct {
	ID, Title, Body, Icon, Tone, Age, Timestamp, ISOTime, Action string
	Unread                                                       bool
}

type notificationsView struct {
	Past                          bool
	Category, NextURL, RefreshURL string
	Unread                        int
	Items                         []notificationView
}

func category(s string) string {
	switch s {
	case "studio", "reading", "system":
		return s
	}
	return "all"
}

// categoryKinds selects one G4 category: the kinds it includes (any when
// empty) and the kinds it excludes (System is everything else).
func categoryKinds(cat string) (kinds, notKinds []string) {
	switch cat {
	case "studio":
		return studioKinds, nil
	case "reading":
		return readingKinds, nil
	case "system":
		return nil, append(append([]string{}, studioKinds...), readingKinds...)
	}
	return nil, nil
}

func notificationsURL(cat, cursor string) string {
	q := url.Values{}
	if cat != "all" {
		q.Set("category", cat)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if len(q) == 0 {
		return "/notifications"
	}
	return "/notifications?" + q.Encode()
}

func (m *Module) notificationsPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	cat, cursor := category(r.URL.Query().Get("category")), r.URL.Query().Get("cursor")
	if cursor != "" && !db.ValidID(cursor) {
		return core.ErrBadRequest(i18n.T(ctx, "activity.error.cursor"))
	}
	o := notify.ListOptions{Before: cursor, Limit: listLimit + 1}
	o.Kinds, o.NotKinds = categoryKinds(cat)
	uid := core.UserFrom(ctx).ID
	items, err := m.d.Notify.List(ctx, uid, o)
	if err != nil {
		return err
	}
	v := notificationsView{Category: cat, RefreshURL: notificationsURL(cat, ""), Past: cursor != ""}
	if len(items) > listLimit {
		items = items[:listLimit]
		v.NextURL = notificationsURL(cat, items[len(items)-1].ID)
	}
	// Mark all read covers the selected category, so it is enabled only when
	// that category has unread notifications.
	v.Unread, err = m.d.Notify.UnreadCountKinds(ctx, uid, o.Kinds, o.NotKinds)
	if err != nil {
		return err
	}
	for _, n := range items {
		n.Body = notificationBody(ctx, n)
		icon, tone := notificationIcon(n.Kind)
		action := i18n.T(ctx, "activity.notifications.mark_read")
		if safeNotificationLink(ctx, n.Link) != "" {
			action = i18n.T(ctx, "activity.notifications.open")
		}
		v.Items = append(v.Items, notificationView{ID: n.ID, Title: n.Title, Body: n.Body, Icon: icon, Tone: tone,
			Age: age(ctx, n.CreatedAt, m.d.Notify.Now()), Timestamp: timestamp(ctx, n.CreatedAt), ISOTime: n.CreatedAt.UTC().Format(time.RFC3339),
			Unread: n.ReadAt == nil, Action: action})
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "activity.notifications.title"), Section: layout.SectionStudio,
		NoSidebar: true, Tab: layout.TabMe}, notificationsBody(v))
}

func (m *Module) readAll(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return core.ErrBadRequest(i18n.T(r.Context(), "activity.error.form"))
	}
	cat := category(r.PostForm.Get("category"))
	kinds, notKinds := categoryKinds(cat)
	if _, err := m.d.Notify.MarkKindsRead(r.Context(), core.UserFrom(r.Context()).ID, kinds, notKinds); err != nil {
		return err
	}
	done := i18n.T(r.Context(), "activity.notifications.read_all_done")
	if cat != "all" {
		done = i18n.T(r.Context(), "activity.notifications.read_category_done", "category", i18n.T(r.Context(), "activity.notifications."+cat))
	}
	web.Flash(w, r, "success", done, "")
	core.Redirect(w, r, notificationsURL(cat, ""))
	return nil
}

func (m *Module) openNotification(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return core.ErrBadRequest(i18n.T(r.Context(), "activity.error.form"))
	}
	uid, id := core.UserFrom(r.Context()).ID, r.PathValue("id")
	n, err := m.d.Notify.Get(r.Context(), uid, id)
	if db.IsNotFound(err) {
		return core.ErrNotFound()
	}
	if err != nil {
		return err
	}
	if _, err := m.d.Notify.MarkRead(r.Context(), uid, []string{id}); err != nil {
		return err
	}
	next := safeNotificationLink(r.Context(), n.Link)
	if next == "" {
		next = notificationsURL(category(r.PostForm.Get("category")), "")
	}
	core.Redirect(w, r, next)
	return nil
}

// Only existing, local pages can be opened from notifications. No provider-
// supplied external URL, script URL, protocol-relative URL or missing route.
func safeNotificationLink(ctx context.Context, link string) string {
	if core.SafeRedirectTarget(link, "") == "" {
		return ""
	}
	decoded, err := url.PathUnescape(link)
	if err != nil || strings.HasPrefix(decoded, "//") || strings.ContainsRune(decoded, '\\') || strings.ContainsFunc(decoded, unicode.IsControl) {
		return ""
	}
	if !ui.Ready(ctx, link) {
		return ""
	}
	return link
}

func notificationIcon(kind string) (string, string) {
	switch kind {
	case notify.KindJobDone:
		return "sparkles", "success"
	case notify.KindJobFailed:
		return "alert-triangle", "error"
	case notify.KindLockTraining:
		return "lock", "success"
	case notify.KindComment, notify.KindMention, notify.KindReview:
		return "message-circle", "info"
	case notify.KindChapter:
		return "book", "info"
	case notify.KindFollow:
		return "user-plus", "info"
	case notify.KindSpendLimit:
		return "alert-triangle", "warning"
	case notify.KindUpdate:
		return "download", "info"
	}
	return "bell", "info"
}
