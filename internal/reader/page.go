package reader

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/reader/agegate"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// denyHidden answers a read the viewer may not make. Anonymous visitors on a
// personal or private server see the sign-in page (ADR 0012 §1); everyone
// else gets a 404 that never reveals whether the series exists.
func (m *Module) denyHidden(w http.ResponseWriter, r *http.Request, err error) error {
	if !errors.Is(err, publish.ErrNotFound) {
		return err
	}
	if core.UserFrom(r.Context()) == nil {
		if inst, ierr := m.d.Settings.Instance(r.Context()); ierr == nil && !inst.ExplorePublic() {
			core.Redirect(w, r, httpx.LoginURL(r))
			return nil
		}
	}
	return core.ErrNotFound()
}

// readerPage serves E3/E4: the complete chapter as stacked page images with
// alt-text (readable without JavaScript) that the reader island turns into
// the paged, spread, guided and webtoon views.
func (m *Module) readerPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	v := viewer(r)
	number, ok := chapterNumber(r.PathValue("chapter"))
	if !ok {
		return m.denyHidden(w, r, publish.ErrNotFound)
	}
	series, err := m.pub.SeriesBySlug(ctx, v, r.PathValue("series"))
	if err != nil {
		return m.denyHidden(w, r, err)
	}
	lang, explicit := pickLanguage(r, series)
	pub, pages, err := m.loadChapter(ctx, v, series, number, lang, explicit)
	if err != nil {
		return m.denyHidden(w, r, err)
	}
	if need, err := (gate{m: m}).Required(ctx, v, series); err != nil {
		return err
	} else if need {
		return m.renderAgeGate(w, r, series, pub)
	}
	view, err := m.buildView(ctx, r, v, series, pub, pages)
	if err != nil {
		return err
	}
	sid := sessionID(w, r)
	m.recordAsync(v, sid, analytics.ReadEvent{SeriesID: series.ID, PublicationID: pub.ID, Kind: analytics.EventOpen, Page: 1})
	w.Header().Set("X-Robots-Tag", robotsFor(series, pub))
	c := m.ui.Chrome(w, r, web.PageOpts{Title: series.Title + " · " + view.Label, Section: layout.SectionExplore,
		Tab: layout.TabExplore, NoSidebar: true})
	return web.Render(w, r, http.StatusOK, ReaderDocument(c, view), nil)
}

// robotsFor keeps unlisted and non-public chapters out of search engines.
func robotsFor(s publish.Series, p publish.Publication) string {
	if s.Visibility == publish.VisibilityPublic && p.Visibility == publish.VisibilityPublic {
		return "noarchive"
	}
	return "noindex, noarchive"
}

func (m *Module) buildView(ctx context.Context, r *http.Request, v publish.Viewer, s publish.Series, pub publish.Publication, pages []publish.PublicationPage) (*readerView, error) {
	pv, err := m.signPages(ctx, v, pages)
	if err != nil {
		return nil, err
	}
	chapters, prev, next, err := m.buildChapters(ctx, v, s, pub)
	if err != nil {
		return nil, err
	}
	view := &readerView{Series: s, Pub: pub, Pages: pv, Chapters: chapters, Prev: prev, Next: next,
		Langs: m.buildLanguages(ctx, v, s, pub), SignedIn: v.UserID != "", Label: chapterLabel(ctx, s, pub)}
	view.BackURL = backURL(ctx, s, view.SignedIn)
	if p := "/s/" + url.PathEscape(s.Slug); view.BackURL == p {
		view.SeriesURL = p
	}
	label := pub.AILabel
	if len(label) == 0 {
		label = s.AILabel
	}
	view.Labels = provenance(ctx, label)
	motion := core.PrefsFrom(ctx).ReducedMotion == "on"
	if view.SignedIn {
		up := core.PrefsFrom(ctx)
		var row *SeriesPrefs
		if sp, err := m.store.seriesPrefs(ctx, v.UserID, s.ID); err == nil {
			row = &sp
		} else if !errors.Is(err, errNoRow) {
			return nil, err
		}
		view.Prefs = resolvePrefs(s, &up, row, motion)
		if p, err := m.store.progress(ctx, v.UserID, pub.ID); err == nil {
			view.Progress = &p
		} else if !errors.Is(err, errNoRow) {
			return nil, err
		}
		if view.Bookmarks, err = m.store.bookmarksFor(ctx, v.UserID, pub.ID); err != nil {
			return nil, err
		}
		if d, err := m.store.downloadFor(ctx, v.UserID, pub.ID); err == nil {
			view.Download = &d
		} else if !errors.Is(err, errNoRow) {
			return nil, err
		}
	} else {
		view.Prefs = resolvePrefs(s, nil, nil, motion)
		view.Bookmarks = []Bookmark{}
	}
	view.StartPage, view.StartPanel = startPosition(r, view.Progress, len(pv))
	view.Props = m.props(ctx, r, view)
	return view, nil
}

// renderAgeGate shows the once-per-series confirmation for mature and adult series.
func (m *Module) renderAgeGate(w http.ResponseWriter, r *http.Request, s publish.Series, pub publish.Publication) error {
	ctx := r.Context()
	c := m.ui.Chrome(w, r, web.PageOpts{Title: s.Title, Section: layout.SectionExplore, Tab: layout.TabExplore, NoSidebar: true})
	g := ageGateView{Series: s, Label: chapterLabel(ctx, s, pub), Action: agegate.ConfirmPath(s.Slug),
		Next: r.URL.RequestURI(), BackURL: backURL(ctx, s, true)}
	w.Header().Set("X-Robots-Tag", "noindex, noarchive")
	return web.Render(w, r, http.StatusOK, AgeGateDocument(c, g), nil)
}

// ageGateView is the confirmation step.
type ageGateView struct {
	Series  publish.Series
	Label   string
	Action  string
	Next    string
	BackURL string
}

func ratingLabel(ctx context.Context, rating string) string {
	return i18n.T(ctx, "reader.rating."+rating)
}

// confirmAge stores the signed-in reader's confirmation (an explicit POST)
// and returns to the chapter.
func (m *Module) confirmAge(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	v := viewer(r)
	s, err := m.pub.SeriesBySlug(ctx, v, r.PathValue("series"))
	if err != nil {
		return notFound(err)
	}
	if agegate.Applies(s.AgeRating) {
		if err := m.store.confirmAge(ctx, v.UserID, s.ID); err != nil {
			return err
		}
	}
	next := core.SafeRedirectTarget(r.PostFormValue("next"), backURL(ctx, s, true))
	if !strings.HasPrefix(next, "/") {
		next = "/"
	}
	core.Redirect(w, r, next)
	return nil
}

// chapterResponse is the island's chapter data with fresh signed page URLs
// (used to refresh URLs before they expire and cached for offline reading).
type chapterResponse struct {
	SeriesID      string     `json:"series_id"`
	PublicationID string     `json:"publication_id"`
	Number        float64    `json:"number"`
	Lang          string     `json:"lang"`
	Pages         []pageView `json:"pages"`
	URLTTL        int        `json:"url_ttl"`
	SignedAt      int64      `json:"signed_at"`
}

// apiChapter returns a readable chapter's frozen pages, guided data and fresh signed URLs.
func (m *Module) apiChapter(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	v := viewer(r)
	number, ok := chapterNumber(r.PathValue("chapter"))
	if !ok {
		return core.ErrNotFound()
	}
	s, err := m.pub.SeriesByID(ctx, v, r.PathValue("series"))
	if err != nil {
		return notFound(err)
	}
	if need, err := (gate{m: m}).Required(ctx, v, s); err != nil {
		return err
	} else if need {
		return core.ErrNotFound()
	}
	pub, pages, err := m.pub.Publication(ctx, v, s.ID, number, strings.TrimSpace(r.URL.Query().Get("lang")))
	if err != nil {
		return notFound(err)
	}
	pv, err := m.signPages(ctx, v, pages)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, http.StatusOK, chapterResponse{SeriesID: s.ID, PublicationID: pub.ID, Number: pub.Number, Lang: pub.Lang,
		Pages: pv, URLTTL: int(PageURLTTL.Seconds()), SignedAt: m.now().Unix()})
	return nil
}
