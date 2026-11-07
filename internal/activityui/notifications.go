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
	switch cat {
	case "studio":
		o.Kinds = studioKinds
	case "reading":
		o.Kinds = readingKinds
	case "system":
		o.NotKinds = append(append([]string{}, studioKinds...), readingKinds...)
	}
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
	v.Unread, err = m.d.Notify.UnreadCount(ctx, uid)
	if err != nil {
		return err
	}
	for _, n := range items {
		if n.Kind == notify.KindJobFailed {
			// Older records may contain raw worker errors from before G4 existed.
			// Details are reviewed through the queue's safe error-code mapping.
			n.Body = i18n.T(ctx, "activity.error.job")
		}
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
	if _, err := m.d.Notify.MarkRead(r.Context(), core.UserFrom(r.Context()).ID, nil); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(r.Context(), "activity.notifications.read_all_done"), "")
	core.Redirect(w, r, notificationsURL(category(r.PostForm.Get("category")), ""))
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
