package social

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// E5 shows this many series comments per page.
const seriesCommentsPage = 5

// newChapterWindow marks chapters published this recently as New.
const newChapterWindow = 7 * 24 * time.Hour

// seriesView is everything the E5 template draws.
type seriesView struct {
	Series    publish.Series
	Href      string
	CoverURL  string
	BannerURL string
	// By credits the series' creators in credit order, each linked to their
	// F1 profile while the account is active.
	By        []credit
	Meta      []string
	Genres    []string
	AI        []ui.BadgeSpec
	Followers string
	Chapters  int
	Languages []langLink
	Sort      string
	SortLinks []ui.SegLink
	Rows      []chapterRow
	// Read is the primary action ("Read ch. 1"); ReadHref is "" without chapters.
	ReadLabel, ReadHref string
	SignedIn            bool
	Following           bool
	LoginHref           string
	Support             []supportLink
	Comments            commentsBox
	AgeGate             *ageGate
}

type langLink struct {
	Code, Href, Name string
	Current          bool
}

type chapterRow struct {
	Href, Number, Title, Meta string
	New, Ready                bool
}

type supportLink struct{ Href, Label string }

// commentsBox is E5's series comments panel.
type commentsBox struct {
	Count      int
	Items      []commentView
	OlderHref  string
	LatestHref string
	// PostAction is the no-JS form target; "" for anonymous viewers.
	PostAction string
	LoginHref  string
	Draft      string
	Error      string
	Reasons    []ui.Option
}

type commentView struct {
	Comment
	Name, Key, AuthorHref string
	Base                  string // /s/{slug}/comments/{id}
	Editing, Reporting    bool
	Draft, Error          string
	Menu                  []ui.MenuItem
}

// ageGate is the confirmation step for mature and adult series.
type ageGate struct {
	Title, Rating, Action, Next, Age string
}

// seriesForm carries a failed no-JS submission back into the page.
type seriesForm struct {
	Draft, Error                 string
	EditID, EditDraft, EditError string
	ReportID, ReportError        string
}

// anonymousNeedsLogin sends anonymous visitors of personal and private
// servers to sign in (ADR 0012 §1) without saying whether the page exists.
func (m *Module) anonymousNeedsLogin(w http.ResponseWriter, r *http.Request) bool {
	if core.UserFrom(r.Context()) != nil {
		return false
	}
	inst, err := m.d.Settings.Instance(r.Context())
	if err == nil && inst.ExplorePublic() {
		return false
	}
	core.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()))
	return true
}

func (m *Module) seriesFromPath(r *http.Request) (publish.Series, error) {
	s, err := m.svc.pub.SeriesBySlug(r.Context(), viewer(r), r.PathValue("series"))
	if err != nil {
		if errors.Is(accessErr(err), ErrNotFound) {
			return publish.Series{}, core.ErrNotFound()
		}
		return publish.Series{}, err
	}
	return s, nil
}

// GET /s/{series}[?lang=&sort=&comments=&edit=&report=]
func (m *Module) seriesPage(w http.ResponseWriter, r *http.Request) error {
	if m.anonymousNeedsLogin(w, r) {
		return nil
	}
	return m.renderSeries(w, r, http.StatusOK, seriesForm{EditID: r.URL.Query().Get("edit"), ReportID: r.URL.Query().Get("report")})
}

func (m *Module) renderSeries(w http.ResponseWriter, r *http.Request, status int, form seriesForm) error {
	ctx := r.Context()
	s, err := m.seriesFromPath(r)
	if err != nil {
		return err
	}
	v, err := m.buildSeriesView(ctx, r, s, form)
	if err != nil {
		return err
	}
	// Only public series may be indexed; an unlisted, members-only or private
	// series page must not reach search engines through a shared link.
	if s.Visibility != publish.VisibilityPublic {
		w.Header().Set("X-Robots-Tag", "noindex")
	}
	return m.ui.Page(w, r, status, web.PageOpts{Title: s.Title, Section: layout.SectionExplore, Tab: layout.TabExplore,
		NoSidebar: true}, SeriesPage(v))
}

func (m *Module) buildSeriesView(ctx context.Context, r *http.Request, s publish.Series, form seriesForm) (seriesView, error) {
	u := core.UserFrom(ctx)
	vw := viewerOf(u)
	href := "/s/" + url.PathEscape(s.Slug)
	v := seriesView{Series: s, Href: href, SignedIn: u != nil, LoginHref: "/login?next=" + url.QueryEscape(href)}

	if gate, err := m.ageGateFor(ctx, u, s, href); err != nil || gate != nil {
		v.AgeGate = gate
		return v, err
	}

	if s.CoverAssetID != "" {
		v.CoverURL = m.assets.URLFor(ctx, s.CoverAssetID, 15*time.Minute)
	}
	if s.BannerAssetID != "" {
		v.BannerURL = m.assets.URLFor(ctx, s.BannerAssetID, 15*time.Minute)
	}
	by, err := m.credits(ctx, s)
	if err != nil {
		return v, err
	}
	v.By = by
	v.Meta = []string{scheduleOrStatus(ctx, s), formatLabel(ctx, s.Format), ratingLabel(ctx, s.AgeRating), licenceLabel(ctx, s.Licence)}
	for _, g := range s.Genres {
		if g = genreText(g); g != "" {
			v.Genres = append(v.Genres, g)
		}
	}
	v.AI = aiBadges(ctx, s.AILabel)
	v.Followers = compactCount(ctx, s.FollowerCount)

	// Language switch: the original language first, then the others.
	langs := []string{s.OriginalLang}
	for _, l := range s.Languages {
		if l != "" && !slices.Contains(langs, l) {
			langs = append(langs, l)
		}
	}
	lang := r.URL.Query().Get("lang")
	if !slices.Contains(langs, lang) {
		lang = s.OriginalLang
	}
	sortKey := r.URL.Query().Get("sort")
	if sortKey != "oldest" {
		sortKey = "newest"
	}
	langParam := ""
	if lang != s.OriginalLang {
		langParam = lang
	}
	for _, l := range langs {
		lp := l
		if l == s.OriginalLang {
			lp = ""
		}
		v.Languages = append(v.Languages, langLink{Code: langCode(l), Current: l == lang,
			Href: withQuery(href, "lang", lp, "sort", sortParam(sortKey)),
			Name: i18n.T(ctx, "social.series.read_in", "lang", langCode(l))})
	}
	v.Sort = sortKey
	v.SortLinks = []ui.SegLink{
		{Href: withQuery(href, "lang", langParam) + "#chapters", Label: i18n.T(ctx, "social.series.newest"), Current: sortKey == "newest"},
		{Href: withQuery(href, "lang", langParam, "sort", "oldest") + "#chapters", Label: i18n.T(ctx, "social.series.oldest"), Current: sortKey == "oldest"},
	}

	pubs, err := m.svc.pub.Publications(ctx, vw, s.ID, lang)
	if err != nil {
		return v, accessErr(err)
	}
	v.Chapters = len(pubs)
	now := m.svc.now()
	if len(pubs) > 0 {
		first := pubs[0]
		v.ReadLabel = i18n.T(ctx, "social.series.read", "number", numberParam(first.Number))
		v.ReadHref = readerHref(href, first, langParam)
	} else {
		v.ReadLabel = i18n.T(ctx, "social.series.no_chapters")
	}
	ordered := append([]publish.Publication(nil), pubs...)
	if sortKey == "newest" {
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Number > ordered[j].Number })
	}
	for _, p := range ordered {
		row := chapterRow{Href: readerHref(href, p, langParam), Number: chapterNumber(p.Number), Title: p.Title}
		row.Ready = ui.Ready(ctx, row.Href)
		meta := []string{}
		if p.PublishedAt != nil {
			meta = append(meta, shortDate(ctx, *p.PublishedAt, now))
			row.New = now.Sub(*p.PublishedAt) < newChapterWindow
		}
		meta = append(meta, i18n.T(ctx, "social.series.pages", "count", p.PageCount))
		row.Meta = strings.Join(meta, " · ")
		if row.Title == "" {
			row.Title = i18n.T(ctx, "social.series.chapter", "number", numberParam(p.Number))
		}
		v.Rows = append(v.Rows, row)
	}

	if u != nil {
		if v.Following, err = m.svc.IsFollowing(ctx, u.ID, s.ID); err != nil {
			return v, err
		}
	}
	for _, l := range s.SupportLinks {
		if h, text, ok := externalLink(l.URL, l.Label); ok {
			v.Support = append(v.Support, supportLink{Href: h, Label: text})
		}
	}
	box, err := m.seriesComments(ctx, r, s, href, form)
	if err != nil {
		return v, err
	}
	v.Comments = box
	return v, nil
}

func sortParam(s string) string {
	if s == "oldest" {
		return s
	}
	return ""
}

// readerHref is the reader route agent R serves: /s/{series}/{chapter}[?lang=].
func readerHref(seriesHref string, p publish.Publication, langParam string) string {
	return withQuery(seriesHref+"/"+url.PathEscape(numberParam(p.Number)), "lang", langParam)
}

// ageGateFor returns the confirmation step while a signed-in reader has not
// confirmed their age for a mature or adult series. Anonymous visitors never
// get here (publish.Service hides such series from them) and the owner
// previews without confirming.
func (m *Module) ageGateFor(ctx context.Context, u *auth.User, s publish.Series, href string) (*ageGate, error) {
	if u == nil || u.ID == s.OwnerID || (s.AgeRating != publish.RatingMature && s.AgeRating != publish.RatingAdult) {
		return nil, nil
	}
	ok, err := m.svc.ageConfirmed(ctx, u.ID, s.ID)
	if err != nil || ok {
		return nil, err
	}
	age := "17"
	if s.AgeRating == publish.RatingAdult {
		age = "18"
	}
	return &ageGate{Title: s.Title, Rating: ratingLabel(ctx, s.AgeRating), Action: href + "/confirm-age", Next: href, Age: age}, nil
}

// seriesComments builds E5's series comment panel for the viewer.
func (m *Module) seriesComments(ctx context.Context, r *http.Request, s publish.Series, href string, form seriesForm) (commentsBox, error) {
	u := core.UserFrom(ctx)
	vw := viewerOf(u)
	t := target{series: s}
	box := commentsBox{LoginHref: "/login?next=" + url.QueryEscape(href+"#comments"), Draft: form.Draft, Error: form.Error}
	n, err := m.svc.countComments(ctx, vw, t, TargetSeries)
	if err != nil {
		return box, err
	}
	box.Count = n
	cursor := r.URL.Query().Get("comments")
	items, next, err := m.svc.listComments(ctx, vw, CommentQuery{SeriesID: s.ID, TargetKind: TargetSeries, TargetID: s.ID,
		Cursor: cursor, Limit: seriesCommentsPage})
	if errors.Is(err, ErrInvalid) && cursor != "" {
		cursor = ""
		items, next, err = m.svc.listComments(ctx, vw, CommentQuery{SeriesID: s.ID, TargetKind: TargetSeries, TargetID: s.ID,
			Limit: seriesCommentsPage})
	}
	if err != nil {
		return box, err
	}
	keep := keepQuery(r)
	if next != "" {
		box.OlderHref = withQuery(href, append(keep, "comments", next)...) + "#comments"
	}
	if cursor != "" {
		box.LatestHref = withQuery(href, keep...) + "#comments"
	}
	if u != nil {
		box.PostAction = href + "/comments"
	}
	for _, reason := range ReportReasons {
		box.Reasons = append(box.Reasons, ui.Option{Value: reason, Label: i18n.T(ctx, "social.report.reason."+reason)})
	}
	for _, c := range items {
		cv := commentView{Comment: c, Name: c.Author.DisplayName, Key: c.Author.ID, Base: href + "/comments/" + url.PathEscape(c.ID)}
		if cv.Name == "" {
			cv.Name = i18n.T(ctx, "social.comment.deleted_account")
		}
		if c.Author.Username != "" {
			cv.AuthorHref = "/u/" + url.PathEscape(c.Author.Username)
		}
		if c.CanEdit && form.EditID == c.ID {
			cv.Editing, cv.Draft, cv.Error = true, c.Body, form.EditError
			if form.EditDraft != "" {
				cv.Draft = form.EditDraft
			}
		}
		if c.CanReport && form.ReportID == c.ID {
			cv.Reporting, cv.Error = true, form.ReportError
		}
		frag := "#comment-" + c.ID
		if c.CanEdit {
			cv.Menu = append(cv.Menu, ui.MenuItem{Href: withQuery(href, append(keep, "comments", cursor, "edit", c.ID)...) + frag,
				Label: i18n.T(ctx, "social.comment.edit"), Icon: "pencil"})
		}
		if c.CanHide {
			if c.Hidden {
				cv.Menu = append(cv.Menu, ui.MenuItem{Href: cv.Base + "/unhide", Post: true, Label: i18n.T(ctx, "social.comment.unhide"), Icon: "eye"})
			} else {
				cv.Menu = append(cv.Menu, ui.MenuItem{Href: cv.Base + "/hide", Post: true, Label: i18n.T(ctx, "social.comment.hide"), Icon: "eye-off"})
			}
		}
		if c.CanReport {
			cv.Menu = append(cv.Menu, ui.MenuItem{Href: withQuery(href, append(keep, "comments", cursor, "report", c.ID)...) + frag,
				Label: i18n.T(ctx, "social.comment.report"), Icon: "flag"})
		}
		if c.CanDelete {
			cv.Menu = append(cv.Menu, ui.MenuItem{Href: cv.Base + "/delete", Post: true, Danger: true,
				Label: i18n.T(ctx, "social.comment.delete"), Icon: "trash"})
		}
		box.Items = append(box.Items, cv)
	}
	return box, nil
}

// keepQuery keeps the language and chapter order across comment links.
func keepQuery(r *http.Request) []string {
	q := r.URL.Query()
	return []string{"lang", q.Get("lang"), "sort", sortParam(q.Get("sort"))}
}

// ---- E5 forms ---------------------------------------------------------------------

// formError turns a service or rate-limit error into the message a no-JS
// form shows; ok is false for unexpected errors.
func formError(ctx context.Context, err error) (string, bool) {
	var ie *invalidError
	var ae *core.APIError
	switch {
	case errors.As(err, &ie):
		return i18n.T(ctx, ie.key), true
	case errors.Is(err, ErrForbidden):
		return i18n.T(ctx, "social.error.forbidden"), true
	case errors.As(err, &ae) && ae.Status < 500:
		return web.ErrorText(ctx, ae), true
	}
	return "", false
}

func (m *Module) backToComments(w http.ResponseWriter, r *http.Request, s publish.Series, frag string) {
	core.Redirect(w, r, "/s/"+url.PathEscape(s.Slug)+frag)
}

// backToNext returns to the local page that posted a follow form (Explore and
// search send "next"), or to the series page.
func (m *Module) backToNext(w http.ResponseWriter, r *http.Request, s publish.Series) {
	core.Redirect(w, r, core.SafeRedirectTarget(r.PostFormValue("next"), "/s/"+url.PathEscape(s.Slug)))
}

// POST /s/{series}/follow
func (m *Module) followSeriesForm(w http.ResponseWriter, r *http.Request) error {
	if err := allow(m.followL, r); err != nil {
		return err
	}
	s, err := m.seriesFromPath(r)
	if err != nil {
		return err
	}
	if err := m.svc.follow(r.Context(), core.UserFrom(r.Context()).ID, s, true); err != nil {
		return apiError(r.Context(), err)
	}
	m.backToNext(w, r, s)
	return nil
}

// POST /s/{series}/unfollow
func (m *Module) unfollowSeriesForm(w http.ResponseWriter, r *http.Request) error {
	if err := allow(m.followL, r); err != nil {
		return err
	}
	s, err := m.seriesFromPath(r)
	if err != nil {
		return err
	}
	if err := m.svc.unfollow(r.Context(), core.UserFrom(r.Context()).ID, s.ID); err != nil {
		return err
	}
	m.backToNext(w, r, s)
	return nil
}

// POST /s/{series}/comments
func (m *Module) postSeriesComment(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("")
	}
	s, err := m.seriesFromPath(r)
	if err != nil {
		return err
	}
	body := r.PostFormValue("body")
	if err := allow(m.commentL, r); err != nil {
		msg, _ := formError(ctx, err)
		return m.renderSeries(w, r, http.StatusTooManyRequests, seriesForm{Draft: body, Error: msg})
	}
	if _, err := m.svc.createComment(ctx, viewer(r), NewComment{SeriesID: s.ID, TargetKind: TargetSeries, TargetID: s.ID, Body: body}); err != nil {
		if msg, ok := formError(ctx, err); ok {
			return m.renderSeries(w, r, http.StatusUnprocessableEntity, seriesForm{Draft: body, Error: msg})
		}
		return apiError(ctx, err)
	}
	web.Flash(w, r, "success", i18n.T(ctx, "social.comment.posted"), "")
	m.backToComments(w, r, s, "#comments")
	return nil
}

// POST /s/{series}/comments/{id}/edit
func (m *Module) editSeriesComment(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("")
	}
	s, err := m.seriesFromPath(r)
	if err != nil {
		return err
	}
	id, body := r.PathValue("id"), r.PostFormValue("body")
	if err := allow(m.editL, r); err != nil {
		msg, _ := formError(ctx, err)
		return m.renderSeries(w, r, http.StatusTooManyRequests, seriesForm{EditID: id, EditDraft: body, EditError: msg})
	}
	if _, err := m.svc.editComment(ctx, viewer(r), id, body); err != nil {
		var ie *invalidError
		if errors.As(err, &ie) {
			return m.renderSeries(w, r, http.StatusUnprocessableEntity, seriesForm{EditID: id, EditDraft: body, EditError: i18n.T(ctx, ie.key)})
		}
		return apiError(ctx, err)
	}
	web.Flash(w, r, "success", i18n.T(ctx, "social.comment.saved"), "")
	m.backToComments(w, r, s, "#comment-"+id)
	return nil
}

// POST /s/{series}/comments/{id}/delete
func (m *Module) deleteSeriesComment(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	s, err := m.seriesFromPath(r)
	if err != nil {
		return err
	}
	if err := allow(m.editL, r); err != nil {
		return err
	}
	if _, err := m.svc.deleteComment(ctx, viewer(r), r.PathValue("id")); err != nil {
		return apiError(ctx, err)
	}
	web.Flash(w, r, "success", i18n.T(ctx, "social.comment.deleted"), "")
	m.backToComments(w, r, s, "#comments")
	return nil
}

func (m *Module) setHiddenForm(w http.ResponseWriter, r *http.Request, hidden bool) error {
	ctx := r.Context()
	s, err := m.seriesFromPath(r)
	if err != nil {
		return err
	}
	if err := allow(m.editL, r); err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := m.svc.setHidden(ctx, viewer(r), id, hidden); err != nil {
		return apiError(ctx, err)
	}
	key := "social.comment.hidden_done"
	if !hidden {
		key = "social.comment.shown_done"
	}
	web.Flash(w, r, "success", i18n.T(ctx, key), "")
	m.backToComments(w, r, s, "#comment-"+id)
	return nil
}

// POST /s/{series}/comments/{id}/hide
func (m *Module) hideSeriesComment(w http.ResponseWriter, r *http.Request) error {
	return m.setHiddenForm(w, r, true)
}

// POST /s/{series}/comments/{id}/unhide
func (m *Module) unhideSeriesComment(w http.ResponseWriter, r *http.Request) error {
	return m.setHiddenForm(w, r, false)
}

// POST /s/{series}/comments/{id}/report
func (m *Module) reportSeriesComment(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("")
	}
	s, err := m.seriesFromPath(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err := allow(m.reportL, r); err != nil {
		msg, _ := formError(ctx, err)
		return m.renderSeries(w, r, http.StatusTooManyRequests, seriesForm{ReportID: id, ReportError: msg})
	}
	if _, err := m.svc.report(ctx, viewer(r), NewReport{TargetKind: "comment", TargetID: id,
		Reason: r.PostFormValue("reason"), Details: r.PostFormValue("details")}); err != nil {
		var ie *invalidError
		if errors.As(err, &ie) {
			return m.renderSeries(w, r, http.StatusUnprocessableEntity, seriesForm{ReportID: id, ReportError: i18n.T(ctx, ie.key)})
		}
		return apiError(ctx, err)
	}
	web.Flash(w, r, "success", i18n.T(ctx, "social.report.sent"), i18n.T(ctx, "social.report.sent_body"))
	m.backToComments(w, r, s, "#comment-"+id)
	return nil
}

// credit is one name on E5's creators line.
type credit struct{ Name, Href string }

// credits lists the series' creators in credit order (publish puts the owner
// first), one entry per account. A credit name replaces the display name;
// suspended or deleted accounts keep their credit but lose the profile link.
func (m *Module) credits(ctx context.Context, s publish.Series) ([]credit, error) {
	list := s.Creators
	if len(list) == 0 {
		list = []publish.Creator{{UserID: s.OwnerID, Role: "owner"}}
	}
	var out []credit
	seen := map[string]bool{}
	for _, c := range list {
		if c.UserID == "" || seen[c.UserID] {
			continue
		}
		seen[c.UserID] = true
		u, err := m.d.Auth.UserByID(ctx, c.UserID)
		if err != nil {
			if errors.Is(err, auth.ErrUserNotFound) {
				if c.CreditName != "" {
					out = append(out, credit{Name: c.CreditName})
				}
				continue
			}
			return nil, err
		}
		cr := credit{Name: u.Name()}
		if c.CreditName != "" {
			cr.Name = c.CreditName
		}
		if u.Status == auth.StatusActive {
			cr.Href = "/u/" + url.PathEscape(u.Username)
		}
		out = append(out, cr)
	}
	return out, nil
}

// creditSep is the separator before the i-th of n credits: ", " between
// names and " & " before the last ("Jerry Okafor & Jun Sato").
func creditSep(ctx context.Context, i, n int) string {
	switch {
	case i == 0:
		return ""
	case i == n-1:
		return i18n.T(ctx, "social.series.credit_last")
	default:
		return i18n.T(ctx, "social.series.credit_sep")
	}
}
