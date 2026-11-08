package reader

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Library tabs (E7).
const (
	TabFollowing = "following"
	TabHistory   = "history"
	TabDownloads = "downloads"
	TabBookmarks = "bookmarks"
)

var libraryTabs = []string{TabFollowing, TabHistory, TabDownloads, TabBookmarks}

// Library list sizes.
const (
	libraryPageSize = 24
	// maxFollowed caps the followed series the library resolves per request.
	maxFollowed = publish.MaxFilterIDs
	// continueCount is the number of "Continue reading" cards on the Following tab (E7).
	continueCount = 3
	// newChapterWindow marks an unread latest chapter "New ch." when the
	// reader never opened the series.
	newChapterWindow = 7 * 24 * time.Hour
)

// Following sort orders.
const (
	SortNew   = "new"
	SortRead  = "read"
	SortTitle = "title"
)

// coverCard is a 3:4 cover card of the Following grid.
type coverCard struct {
	Href     string
	Title    string
	Meta     string
	CoverURL string
	New      bool
	latest   time.Time
	read     time.Time
}

// rowCard is a continue-reading, history, download or bookmark row.
type rowCard struct {
	ID            string
	PublicationID string
	Href          string
	Title         string
	Meta          string
	CoverURL      string
	Progress      float64
	ShowProgress  bool
	Status        string
	Bytes         int64
}

// libraryView is the E7 page.
type libraryView struct {
	Tab            string
	Sort           string
	FollowsReady   bool
	FollowingCount int
	DownloadCount  int
	BookmarkCount  int
	Following      []coverCard
	Continue       []rowCard
	Rows           []rowCard
	NextURL        string
	TotalBytes     int64
	ReadyCount     int
	Props          string
}

func (m *Module) libraryIndex(w http.ResponseWriter, r *http.Request) error {
	core.Redirect(w, r, "/library/"+TabFollowing)
	return nil
}

func (m *Module) libraryPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	tab := r.PathValue("tab")
	if !slices.Contains(libraryTabs, tab) {
		return core.ErrNotFound()
	}
	v := viewer(r)
	view := &libraryView{Tab: tab, Sort: r.URL.Query().Get("sort")}
	if !slices.Contains([]string{SortNew, SortRead, SortTitle}, view.Sort) {
		view.Sort = SortNew
	}
	res := newResolver(m, v)
	followed, ready, err := m.followedSeries(ctx, v, res)
	if err != nil {
		return err
	}
	view.FollowsReady, view.FollowingCount = ready, len(followed)
	if view.DownloadCount, err = m.store.countDownloads(ctx, v.UserID); err != nil {
		return err
	}
	if view.BookmarkCount, err = m.store.countBookmarks(ctx, v.UserID); err != nil {
		return err
	}
	cursor := r.URL.Query().Get("cursor")
	switch tab {
	case TabFollowing:
		if view.Following, err = m.followingCards(ctx, v, res, followed, view.Sort); err != nil {
			return err
		}
		if view.Continue, _, err = m.historyRows(ctx, v, res, "", true, continueCount); err != nil {
			return err
		}
	case TabHistory:
		var next string
		if view.Rows, next, err = m.historyRows(ctx, v, res, cursor, false, libraryPageSize); err != nil {
			return err
		}
		if next != "" {
			view.NextURL = "/library/history?cursor=" + url.QueryEscape(next)
		}
	case TabDownloads:
		if err := m.downloadRows(ctx, v, res, view); err != nil {
			return err
		}
	case TabBookmarks:
		var next string
		if view.Rows, next, err = m.bookmarkRows(ctx, v, res, cursor); err != nil {
			return err
		}
		if next != "" {
			view.NextURL = "/library/bookmarks?cursor=" + url.QueryEscape(next)
		}
	}
	if tab == TabDownloads || tab == TabBookmarks {
		view.Props = libraryProps(ctx, tab)
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "reader.library.title"), Section: layout.SectionLibrary,
		Tab: layout.TabLibrary, NoSidebar: true}, LibraryPage(view))
}

// resolver caches publish lookups for one library request.
type resolver struct {
	m      *Module
	v      publish.Viewer
	series map[string]*publish.Series
	pubs   map[string]map[string]publish.Publication
	covers map[string]string
}

func newResolver(m *Module, v publish.Viewer) *resolver {
	return &resolver{m: m, v: v, series: map[string]*publish.Series{}, pubs: map[string]map[string]publish.Publication{},
		covers: map[string]string{}}
}

// load resolves readable series by ID through publish.Service (unreadable ones stay nil).
func (res *resolver) load(ctx context.Context, ids []string) error {
	var want []string
	for _, id := range ids {
		if _, ok := res.series[id]; !ok && db.ValidID(id) && !slices.Contains(want, id) {
			want = append(want, id)
		}
	}
	for len(want) > 0 {
		chunk := want[:min(len(want), publish.MaxListLimit)]
		want = want[len(chunk):]
		cursor := ""
		for range 20 {
			list, next, err := res.m.pub.ListSeries(ctx, res.v, publish.SeriesFilter{IDs: chunk, Cursor: cursor, Limit: publish.MaxListLimit})
			if err != nil {
				return err
			}
			for i := range list {
				s := list[i]
				res.series[s.ID] = &s
			}
			if next == "" {
				break
			}
			cursor = next
		}
		for _, id := range chunk {
			if _, ok := res.series[id]; !ok {
				res.series[id] = nil
			}
		}
	}
	return nil
}

func (res *resolver) get(ctx context.Context, id string) *publish.Series {
	if _, ok := res.series[id]; !ok {
		if err := res.load(ctx, []string{id}); err != nil {
			return nil
		}
	}
	return res.series[id]
}

// publications maps the readable publications of s (all languages) by ID.
func (res *resolver) publications(ctx context.Context, s *publish.Series) map[string]publish.Publication {
	if p, ok := res.pubs[s.ID]; ok {
		return p
	}
	out := map[string]publish.Publication{}
	langs := append([]string{s.OriginalLang}, s.Languages...)
	seen := map[string]bool{}
	for _, l := range langs {
		if seen[l] {
			continue
		}
		seen[l] = true
		list, err := res.m.pub.Publications(ctx, res.v, s.ID, l)
		if err != nil {
			continue
		}
		for _, p := range list {
			out[p.ID] = p
		}
	}
	res.pubs[s.ID] = out
	return out
}

// latest returns the newest readable chapter of s in its original language.
func (res *resolver) latest(ctx context.Context, s *publish.Series) (publish.Publication, bool) {
	var best publish.Publication
	found := false
	for _, p := range res.publications(ctx, s) {
		if p.Lang != s.OriginalLang {
			continue
		}
		if !found || p.Number > best.Number {
			best, found = p, true
		}
	}
	return best, found
}

func (res *resolver) cover(ctx context.Context, s *publish.Series) string {
	if s.CoverAssetID == "" {
		return ""
	}
	if u, ok := res.covers[s.ID]; ok {
		return u
	}
	u := res.m.assets.URLFor(ctx, s.CoverAssetID, CoverURLTTL)
	res.covers[s.ID] = u
	return u
}

// followedSeries lists the readable series the member follows, most recently
// followed first. ready is false when no social.Follows is registered.
func (m *Module) followedSeries(ctx context.Context, v publish.Viewer, res *resolver) ([]*publish.Series, bool, error) {
	f, ok := m.follows()
	if !ok {
		return nil, false, nil
	}
	var ids []string
	cursor := ""
	for len(ids) < maxFollowed {
		page, next, err := f.FollowedSeriesIDs(ctx, v.UserID, cursor, 100)
		if err != nil {
			return nil, true, fmt.Errorf("reader: followed series: %w", err)
		}
		ids = append(ids, page...)
		if next == "" || len(page) == 0 {
			break
		}
		cursor = next
	}
	if len(ids) > maxFollowed {
		ids = ids[:maxFollowed]
	}
	if err := res.load(ctx, ids); err != nil {
		return nil, true, err
	}
	out := []*publish.Series{}
	for _, id := range ids {
		if s := res.series[id]; s != nil && !slices.ContainsFunc(out, func(o *publish.Series) bool { return o.ID == id }) {
			out = append(out, s)
		}
	}
	return out, true, nil
}

func chapterWord(ctx context.Context, s *publish.Series, number float64) string {
	if isWebtoon(s.Format) {
		return i18n.T(ctx, "reader.library.ep", "number", numberText(number))
	}
	return i18n.T(ctx, "reader.library.ch", "number", numberText(number))
}

func (m *Module) followingCards(ctx context.Context, v publish.Viewer, res *resolver, followed []*publish.Series, sortBy string) ([]coverCard, error) {
	ids := make([]string, len(followed))
	for i, s := range followed {
		ids[i] = s.ID
	}
	progress, err := m.store.latestPerSeries(ctx, v.UserID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]coverCard, 0, len(followed))
	for _, s := range followed {
		c := coverCard{Title: s.Title, CoverURL: res.cover(ctx, s), Href: "/s/" + url.PathEscape(s.Slug)}
		latest, hasLatest := res.latest(ctx, s)
		p, read := progress[s.ID]
		if read {
			c.read = p.UpdatedAt
		}
		if hasLatest && latest.PublishedAt != nil {
			c.latest = *latest.PublishedAt
		}
		readLatest := false
		if hasLatest && read {
			if pr, err := m.store.progress(ctx, v.UserID, latest.ID); err == nil {
				readLatest = pr.Completed || pr.Page > 0
			}
		}
		switch {
		case hasLatest && !readLatest && ((read && c.latest.After(c.read)) || m.now().Sub(c.latest) < newChapterWindow):
			c.New = true
			c.Meta = chapterWord(ctx, s, latest.Number) + " · " + i18n.T(ctx, "reader.library.new_word")
			c.Href = chapterURL(*s, latest.Number, latest.Lang)
		case read && !p.Completed:
			if pub, ok := res.publications(ctx, s)[p.PublicationID]; ok {
				c.Meta = chapterWord(ctx, s, pub.Number) + " · " + i18n.T(ctx, "reader.library.page_short", "page", p.Page)
				if isWebtoon(s.Format) {
					c.Meta = chapterWord(ctx, s, pub.Number) + " · " + strconv.Itoa(int(p.Percent*100+0.5)) + "%"
				}
				c.Href = chapterURL(*s, pub.Number, pub.Lang) + pageQuery(chapterURL(*s, pub.Number, pub.Lang), p.Page)
			}
		case s.Status == publish.SeriesCompleted:
			c.Meta = i18n.T(ctx, "reader.library.complete")
		case hasLatest:
			c.Meta = chapterWord(ctx, s, latest.Number)
		}
		if !ui.Ready(ctx, c.Href) {
			c.Href = ""
			if hasLatest {
				c.Href = chapterURL(*s, latest.Number, latest.Lang)
			}
		}
		out = append(out, c)
	}
	switch sortBy {
	case SortRead:
		slices.SortStableFunc(out, func(a, b coverCard) int { return b.read.Compare(a.read) })
	case SortTitle:
		slices.SortStableFunc(out, func(a, b coverCard) int { return cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)) })
	default:
		slices.SortStableFunc(out, func(a, b coverCard) int {
			if a.New != b.New {
				if a.New {
					return -1
				}
				return 1
			}
			return b.latest.Compare(a.latest)
		})
	}
	return out, nil
}

// pageQuery appends ?page=n (or &page=n) to a chapter URL.
func pageQuery(base string, page int) string {
	if page <= 1 {
		return ""
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return sep + "page=" + strconv.Itoa(page)
}

// historyRows lists reading history newest first; inProgress keeps only
// unfinished chapters. Rows whose chapter is no longer readable are skipped.
func (m *Module) historyRows(ctx context.Context, v publish.Viewer, res *resolver, cursor string, inProgress bool, limit int) ([]rowCard, string, error) {
	before, beforeID, _ := decodeCursor(cursor)
	out := []rowCard{}
	next := ""
	for range 10 {
		batch, err := m.store.history(ctx, v.UserID, before, beforeID, limit)
		if err != nil {
			return nil, "", err
		}
		if len(batch) == 0 {
			break
		}
		ids := make([]string, len(batch))
		for i, p := range batch {
			ids[i] = p.SeriesID
		}
		if err := res.load(ctx, ids); err != nil {
			return nil, "", err
		}
		for _, p := range batch {
			before, beforeID = p.UpdatedAt, p.PublicationID
			if c, ok := m.historyCard(ctx, res, p, inProgress); ok {
				out = append(out, c)
			}
			if len(out) == limit {
				break
			}
		}
		if len(out) == limit {
			if more, err := m.store.history(ctx, v.UserID, before, beforeID, 1); err == nil && len(more) > 0 {
				next = encodeCursor(before, beforeID)
			}
			break
		}
		if len(batch) < limit {
			break
		}
	}
	return out, next, nil
}

func (m *Module) historyCard(ctx context.Context, res *resolver, p Progress, inProgress bool) (rowCard, bool) {
	if inProgress && p.Completed {
		return rowCard{}, false
	}
	s := res.get(ctx, p.SeriesID)
	if s == nil {
		return rowCard{}, false
	}
	pub, ok := res.publications(ctx, s)[p.PublicationID]
	if !ok {
		return rowCard{}, false
	}
	total := max(pub.PageCount, 1)
	href := chapterURL(*s, pub.Number, pub.Lang)
	c := rowCard{ID: p.PublicationID, PublicationID: pub.ID, Title: s.Title, CoverURL: res.cover(ctx, s), ShowProgress: true,
		Href: href + pageQuery(href, p.Page)}
	switch {
	case p.Completed:
		c.Progress = 1
		c.Meta = chapterWord(ctx, s, pub.Number) + " · " + i18n.T(ctx, "reader.library.finished")
		c.Href = href
	case isWebtoon(s.Format):
		c.Progress = p.Percent
		c.Meta = chapterWord(ctx, s, pub.Number) + " · " + strconv.Itoa(int(p.Percent*100+0.5)) + "%"
	default:
		c.Progress = float64(p.Page) / float64(total)
		c.Meta = chapterWord(ctx, s, pub.Number) + " · " + i18n.T(ctx, "reader.library.page_of", "page", p.Page, "total", total)
	}
	return c, true
}

func (m *Module) bookmarkRows(ctx context.Context, v publish.Viewer, res *resolver, cursor string) ([]rowCard, string, error) {
	before, beforeID, _ := decodeCursor(cursor)
	batch, err := m.store.bookmarks(ctx, v.UserID, before, beforeID, libraryPageSize+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(batch) > libraryPageSize {
		batch = batch[:libraryPageSize]
		last := batch[len(batch)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	ids := make([]string, len(batch))
	for i, b := range batch {
		ids[i] = b.SeriesID
	}
	if err := res.load(ctx, ids); err != nil {
		return nil, "", err
	}
	out := []rowCard{}
	for _, b := range batch {
		s := res.get(ctx, b.SeriesID)
		if s == nil {
			continue
		}
		pub, ok := res.publications(ctx, s)[b.PublicationID]
		if !ok {
			continue
		}
		href := chapterURL(*s, pub.Number, pub.Lang)
		c := rowCard{ID: b.ID, PublicationID: pub.ID, Title: s.Title, CoverURL: res.cover(ctx, s)}
		c.Href = href + pageQuery(href, max(b.Page, 2))
		if b.Page <= 1 {
			c.Href = href
		}
		c.Meta = chapterWord(ctx, s, pub.Number) + " · " + i18n.T(ctx, "reader.library.page_short", "page", b.Page)
		if n := b.PanelOrder(); n > 0 {
			sep := "?"
			if strings.Contains(c.Href, "?") {
				sep = "&"
			}
			c.Href += sep + "panel=" + strconv.Itoa(n)
			c.Meta += " · " + i18n.T(ctx, "reader.library.panel_short", "panel", n)
		}
		out = append(out, c)
	}
	return out, next, nil
}

func (m *Module) downloadRows(ctx context.Context, v publish.Viewer, res *resolver, view *libraryView) error {
	list, err := m.store.downloads(ctx, v.UserID, 200)
	if err != nil {
		return err
	}
	ids := make([]string, len(list))
	for i, d := range list {
		ids[i] = d.SeriesID
	}
	if err := res.load(ctx, ids); err != nil {
		return err
	}
	for _, d := range list {
		s := res.get(ctx, d.SeriesID)
		var pub publish.Publication
		ok := s != nil
		if ok {
			pub, ok = res.publications(ctx, s)[d.PublicationID]
		}
		if !ok {
			// No longer readable: never show it, and stop the device from serving it.
			if d.Status != DownloadExpired {
				_ = m.store.expireDownload(ctx, v.UserID, d.ID)
			}
			view.Rows = append(view.Rows, rowCard{ID: d.ID, PublicationID: d.PublicationID, Status: "revoked"})
			continue
		}
		status := d.Status
		if d.Expired(m.now()) {
			status = DownloadExpired
		}
		c := rowCard{ID: d.ID, PublicationID: pub.ID, Title: s.Title, CoverURL: res.cover(ctx, s), Href: chapterURL(*s, pub.Number, pub.Lang),
			Status: status, Bytes: d.Bytes}
		c.Meta = chapterWord(ctx, s, pub.Number)
		if strings.TrimSpace(pub.Title) != "" {
			c.Meta += " · " + pub.Title
		}
		if status == DownloadReady {
			view.ReadyCount++
			view.TotalBytes += d.Bytes
		}
		view.Rows = append(view.Rows, c)
	}
	return nil
}

// visibleRows hides revoked downloads from the page (the island still
// receives their IDs so it can delete the device copy).
func (v *libraryView) visibleRows() []rowCard {
	out := make([]rowCard, 0, len(v.Rows))
	for _, r := range v.Rows {
		if r.Status != "revoked" {
			out = append(out, r)
		}
	}
	return out
}

func (v *libraryView) revoked() []string {
	out := []string{}
	for _, r := range v.Rows {
		if r.Status == "revoked" {
			out = append(out, r.PublicationID)
		}
	}
	return out
}

// libraryProps boots the reader island in its library mode (download
// reconcile and removal, bookmark removal without a reload).
func libraryProps(ctx context.Context, tab string) string {
	b, err := json.Marshal(map[string]any{"view": "library", "tab": tab, "api": map[string]string{
		"downloads": "/api/v1/reader/downloads", "bookmarks": "/api/v1/reader/bookmarks"}, "messages": islandMessages(ctx)})
	if err != nil {
		return "{}"
	}
	return string(b)
}

// sizeLabel formats bytes as "412 MB".
func sizeLabel(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	case n >= 1<<20:
		return strconv.FormatInt((n+(1<<19))>>20, 10) + " MB"
	case n > 0:
		return strconv.FormatInt(max((n+512)>>10, 1), 10) + " KB"
	}
	return "0 MB"
}

// removeBookmarkForm deletes a bookmark from the library without JavaScript.
func (m *Module) removeBookmarkForm(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	id := r.PathValue("id")
	if !db.ValidID(id) {
		return core.ErrNotFound()
	}
	if err := m.store.deleteBookmark(r.Context(), viewer(r).UserID, id); err != nil {
		return notFound(err)
	}
	web.Flash(w, r, "success", i18n.T(r.Context(), "reader.bookmark.removed"), "")
	core.Redirect(w, r, "/library/bookmarks")
	return nil
}

// removeDownloadForm deletes a download record without JavaScript (the
// island also deletes the device copy when it is running).
func (m *Module) removeDownloadForm(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	id := r.PathValue("id")
	if !db.ValidID(id) {
		return core.ErrNotFound()
	}
	if err := m.store.deleteDownload(r.Context(), viewer(r).UserID, id); err != nil && !errors.Is(err, errNoRow) {
		return err
	} else if err != nil {
		return core.ErrNotFound()
	}
	web.Flash(w, r, "success", i18n.T(r.Context(), "reader.download.removed"), "")
	core.Redirect(w, r, "/library/downloads")
	return nil
}
