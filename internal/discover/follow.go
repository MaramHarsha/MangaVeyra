package discover

import (
	"context"
	"net/url"

	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// followView is the Follow button of E6's featured series and E8's rows.
// Follows belong to the social module: signed-in members post to its
// POST /s/{series}/follow (with a local "next" to return to); until that
// route exists the button renders in the Soon state. Anonymous visitors are
// sent to sign in first.
type followView struct {
	Action, Next, LoginHref, SeriesHref string
	Following                           bool
}

func (m *Module) follow(ctx context.Context, v publish.Viewer, s publish.Series, next string) followView {
	f := followView{Next: next, SeriesHref: seriesHref(s), Action: seriesHref(s) + "/follow"}
	if v.UserID == "" {
		f.LoginHref = "/login?next=" + url.QueryEscape(next)
		return f
	}
	if m.follows != nil {
		if ok, err := m.follows.IsFollowing(ctx, v.UserID, s.ID); err == nil && ok {
			f.Following = true
		}
	}
	return f
}
