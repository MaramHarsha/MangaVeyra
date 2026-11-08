package social

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/pages/dashboard"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Profile bounds: F1 reads at most this many of a creator's series (pages of
// publish.MaxListLimit) and lists at most profileChapterRows recent chapters.
const (
	profileMaxSeries   = 200
	profileChapterRows = 30
)

type profileView struct {
	Href, Name, Key, Username string
	ShowRank                  bool
	Rank, RankLine            string
	Sub                       string
	Self, SignedIn, Following bool
	LoginHref                 string
	Links                     []supportLink
	SeriesCount               int
	Followers                 string
	ChapterCount              int
	Tab                       string
	Tabs                      []ui.SegLink
	Series                    []profileSeries
	Chapters                  []profileChapter
	Bio, Joined               string
}

type profileSeries struct {
	Href, Title, Meta, CoverURL string
}

type profileChapter struct {
	chapterRow
	Series      string
	publishedAt time.Time
}

// profileExtras are the public profile fields auth does not expose on User
// (users.links and the A5 creator type), read with one narrow query.
type profileExtras struct {
	Links       []string
	CreatorType string
}

func (m *Module) loadProfileExtras(ctx context.Context, userID string) profileExtras {
	var row struct {
		Links      string `db:"links"`
		Onboarding string `db:"onboarding"`
	}
	var out profileExtras
	if err := m.d.DB.Get(ctx, &row, `SELECT links, onboarding FROM users WHERE id = ?`, userID); err != nil {
		return out
	}
	_ = json.Unmarshal([]byte(row.Links), &out.Links)
	var o struct {
		CreatorType string `json:"creator_type"`
	}
	_ = json.Unmarshal([]byte(row.Onboarding), &o)
	out.CreatorType = o.CreatorType
	return out
}

// GET /u/{username}[?tab=chapters|about]
func (m *Module) profilePage(w http.ResponseWriter, r *http.Request) error {
	if m.anonymousNeedsLogin(w, r) {
		return nil
	}
	ctx := r.Context()
	u, err := m.profileUser(r, r.PathValue("username"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return core.ErrNotFound()
		}
		return err
	}
	v, err := m.buildProfile(ctx, r, u)
	if err != nil {
		return err
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: v.Name, Section: layout.SectionExplore, Tab: layout.TabExplore,
		NoSidebar: true}, ProfilePage(v))
}

func (m *Module) buildProfile(ctx context.Context, r *http.Request, u *auth.User) (profileView, error) {
	me := core.UserFrom(ctx)
	vw := viewerOf(me)
	href := "/u/" + url.PathEscape(u.Username)
	v := profileView{Href: href, Name: u.Name(), Key: u.ID, Username: u.Username, SignedIn: me != nil,
		Self: me != nil && me.ID == u.ID, LoginHref: "/login?next=" + url.QueryEscape(href), Bio: strings.TrimSpace(u.Bio),
		Joined: i18n.T(ctx, "social.profile.joined", "date", longDate(ctx, u.CreatedAt))}

	v.ShowRank = core.PrefsFrom(ctx).ShowRanksXP
	p := dashboard.ProgressFor(u.XP, u.Level)
	v.Rank = p.Rank
	roleKey := "social.profile.rank.creator"
	if u.Role == auth.RoleReader {
		roleKey = "social.profile.rank.reader"
	}
	v.RankLine = i18n.T(ctx, roleKey, "rank", p.Rank, "level", p.Level)

	extras := m.loadProfileExtras(ctx, u.ID)
	sub := []string{}
	switch extras.CreatorType {
	case "writer", "artist", "reader", "team":
		sub = append(sub, i18n.T(ctx, "social.profile.type."+extras.CreatorType))
	}
	inst := m.d.Settings.MustInstance(ctx)
	sub = append(sub, web.ServerHost(inst.PublicURL, m.d.Cfg.PublicURL, r.Host))
	if line, _, _ := strings.Cut(v.Bio, "\n"); line != "" {
		sub = append(sub, line)
	}
	v.Sub = strings.Join(sub, " · ")
	for _, raw := range extras.Links {
		if h, text, ok := externalLink(raw, ""); ok && len(v.Links) < 3 {
			v.Links = append(v.Links, supportLink{Href: h, Label: text})
		}
	}

	if me != nil && !v.Self {
		f, err := m.svc.isFollowingUser(ctx, me.ID, u.ID)
		if err != nil {
			return v, err
		}
		v.Following = f
	}
	n, err := m.svc.userFollowerCount(ctx, u.ID)
	if err != nil {
		return v, err
	}
	v.Followers = compactCount(ctx, n)

	// The creator's listed, readable series (never unlisted or private ones).
	var all []publish.Series
	cursor := ""
	for len(all) < profileMaxSeries {
		list, next, err := m.svc.pub.ListSeries(ctx, vw, publish.SeriesFilter{OwnerID: u.ID, Listed: true, Cursor: cursor,
			Limit: publish.MaxListLimit})
		if err != nil {
			return v, accessErr(err)
		}
		all = append(all, list...)
		if next == "" {
			break
		}
		cursor = next
	}
	v.SeriesCount = len(all)
	now := m.svc.now()
	var chapters []profileChapter
	for _, s := range all {
		pubs, err := m.svc.pub.Publications(ctx, vw, s.ID, "")
		if err != nil {
			if errors.Is(err, publish.ErrNotFound) {
				continue
			}
			return v, err
		}
		v.ChapterCount += len(pubs)
		sHref := "/s/" + url.PathEscape(s.Slug)
		ps := profileSeries{Href: sHref, Title: s.Title,
			Meta: scheduleOrStatus(ctx, s) + " · " + i18n.T(ctx, "social.profile.chapters_short", "count", len(pubs))}
		if s.CoverAssetID != "" {
			ps.CoverURL = m.assets.URLFor(ctx, s.CoverAssetID, 15*time.Minute)
		}
		v.Series = append(v.Series, ps)
		for _, p := range pubs {
			row := chapterRow{Href: readerHref(sHref, p, ""), Number: chapterNumber(p.Number), Title: p.Title}
			if row.Title == "" {
				row.Title = i18n.T(ctx, "social.series.chapter", "number", numberParam(p.Number))
			}
			row.Ready = ui.Ready(ctx, row.Href)
			pc := profileChapter{chapterRow: row, Series: s.Title}
			meta := []string{s.Title}
			if p.PublishedAt != nil {
				pc.publishedAt = *p.PublishedAt
				meta = append(meta, shortDate(ctx, *p.PublishedAt, now))
				pc.New = now.Sub(*p.PublishedAt) < newChapterWindow
			}
			meta = append(meta, i18n.T(ctx, "social.series.pages", "count", p.PageCount))
			pc.Meta = strings.Join(meta, " · ")
			chapters = append(chapters, pc)
		}
	}
	sort.SliceStable(chapters, func(i, j int) bool { return chapters[i].publishedAt.After(chapters[j].publishedAt) })
	if len(chapters) > profileChapterRows {
		chapters = chapters[:profileChapterRows]
	}
	v.Chapters = chapters

	v.Tab = r.URL.Query().Get("tab")
	if v.Tab != "chapters" && v.Tab != "about" {
		v.Tab = "series"
	}
	v.Tabs = []ui.SegLink{
		{Href: href, Label: i18n.T(ctx, "social.profile.tab.series"), Current: v.Tab == "series"},
		{Href: href + "?tab=chapters", Label: i18n.T(ctx, "social.profile.tab.chapters"), Current: v.Tab == "chapters"},
		{Href: href + "?tab=about", Label: i18n.T(ctx, "social.profile.tab.about"), Current: v.Tab == "about"},
	}
	return v, nil
}

// POST /u/{username}/follow
func (m *Module) followUserForm(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := allow(m.followL, r); err != nil {
		return err
	}
	u, err := m.profileUser(r, r.PathValue("username"))
	if err != nil {
		return apiError(ctx, err)
	}
	if err := m.svc.followUser(ctx, core.UserFrom(ctx).ID, u); err != nil {
		return apiError(ctx, err)
	}
	core.Redirect(w, r, "/u/"+url.PathEscape(u.Username))
	return nil
}

// POST /u/{username}/unfollow
func (m *Module) unfollowUserForm(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := allow(m.followL, r); err != nil {
		return err
	}
	u, err := m.profileUser(r, r.PathValue("username"))
	if err != nil {
		return apiError(ctx, err)
	}
	if err := m.svc.unfollowUser(ctx, core.UserFrom(ctx).ID, u.ID); err != nil {
		return err
	}
	core.Redirect(w, r, "/u/"+url.PathEscape(u.Username))
	return nil
}
