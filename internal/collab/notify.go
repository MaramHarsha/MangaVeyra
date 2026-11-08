package collab

import (
	"context"
	"net/url"
	"strconv"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/ws"
)

// changed tells open editors of the page that its review changed. The event
// carries ids only; clients refetch through the API, which re-authorizes.
func (m *Module) changed(pageID, commentID, change string) {
	if m.d.Hub == nil {
		return
	}
	data := map[string]string{"page_id": pageID, "change": change}
	if commentID != "" {
		data["comment_id"] = commentID
	}
	m.d.Hub.Publish(ws.DocPagePrefix+pageID, "collab.changed", data)
}

// reviewLink opens the page in the editor with the review drawer.
func reviewLink(projectID string, p pagedoc.Page) string {
	q := url.Values{"page": {strconv.Itoa(p.Number)}, "panel": {"review"}}
	return "/studio/p/" + projectID + "/chapters/" + p.ChapterID + "/edit?" + q.Encode()
}

// notifyMembers sends a review notification to each recipient except the
// actor, in the recipient's language. status is the new status, if any.
// Failures are logged: they never undo the review change.
func (m *Module) notifyMembers(ctx context.Context, c caller, projectID string, p pagedoc.Page, label string, to []string, key, status string) {
	if m.d.Notify == nil {
		return
	}
	inst := m.d.Settings.MustInstance(ctx)
	sent := map[string]bool{c.user.ID: true}
	for _, uid := range to {
		if uid == "" || sent[uid] {
			continue
		}
		sent[uid] = true
		// Only members who can still read the project hear about it.
		if role, err := m.roleOf(ctx, projectID, uid); err != nil || role == "" {
			continue
		}
		locale := ""
		if u, err := m.d.Auth.UserByID(ctx, uid); err == nil && u != nil {
			locale = u.Locale
		}
		lctx := i18n.WithLocale(ctx, i18n.Negotiate(m.d.I18n.Languages(), inst.DefaultLocale, locale, "", ""))
		args := []any{"name", c.user.Name(), "panel", label}
		if status != "" {
			args = append(args, "status", i18n.T(lctx, "collab.status."+status))
		}
		title := i18n.T(lctx, key, args...)
		if _, err := m.d.Notify.Notify(ctx, notify.Input{UserID: uid, Kind: notify.KindReview, Title: title, Link: reviewLink(projectID, p),
			ActorID: c.user.ID, GroupKey: "review:" + p.ID, Data: map[string]any{"project_id": projectID, "page_id": p.ID, "panel": label}}); err != nil {
			m.d.Log.WarnContext(ctx, "collab: notify", "page", p.ID, "err", err)
		}
	}
}
