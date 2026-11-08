package discover

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// exploreGenres are the E6 chips, in the artboard's order.
var exploreGenres = []string{"action", "fantasy", "romance", "mystery", "horror", "slice_of_life", "scifi", "comedy"}

// Ranking tabs.
const (
	RankRising = "rising"
	RankWeek   = "week"
	RankAll    = "all"
)

const (
	shelfSize   = 7
	risingSize  = 4
	rankingSize = 4
)

var genrePattern = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

func parseGenre(raw string) string {
	g := strings.ToLower(strings.TrimSpace(raw))
	if !genrePattern.MatchString(g) {
		return ""
	}
	return g
}

// activity holds the recorded reads and follows Explore ranks by. Every
// number comes from read_events or follows on this server; nothing is
// estimated.
type activity struct {
	reads7, readsPrev7, follows7, reads30, follows30 map[string]int64
}

func (m *Module) activity(ctx context.Context, ids []string, now time.Time) (activity, error) {
	var a activity
	var err error
	d := m.d.DB
	day := 24 * time.Hour
	if a.reads7, err = analytics.SeriesReads(ctx, d, ids, now.Add(-7*day), now); err != nil {
		return a, err
	}
	if a.readsPrev7, err = analytics.SeriesReads(ctx, d, ids, now.Add(-14*day), now.Add(-7*day)); err != nil {
		return a, err
	}
	if a.follows7, err = analytics.SeriesNewFollows(ctx, d, ids, now.Add(-7*day), now); err != nil {
		return a, err
	}
	if a.reads30, err = analytics.SeriesReads(ctx, d, ids, now.Add(-30*day), now); err != nil {
		return a, err
	}
	if a.follows30, err = analytics.SeriesNewFollows(ctx, d, ids, now.Add(-30*day), now); err != nil {
		return a, err
	}
	return a, nil
}

// Rank orders series for one ranking tab and keeps only those with recorded
// activity:
//   - rising: this week's growth (reads in the last 7 days minus the 7 days
//     before, plus new followers in the last 7 days), growth > 0;
//   - week: reads in the last 7 days;
//   - all: all-time views (series.view_count, kept by the analytics recorder).
//
// Ties go to the newer series.
func (a activity) Rank(list []publish.Series, tab string) []publish.Series {
	score := func(s publish.Series) int64 {
		switch tab {
		case RankWeek:
			return a.reads7[s.ID]
		case RankAll:
			return s.ViewCount
		}
		return a.reads7[s.ID] - a.readsPrev7[s.ID] + a.follows7[s.ID]
	}
	var out []publish.Series
	for _, s := range list {
		if score(s) > 0 {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	return out
}

// ---- view ------------------------------------------------------------------

type featuredView struct {
	Eyebrow, Title, Synopsis, ArtURL string
	ReadHref, ReadLabel              string
	Follow                           followView
}

type rankRow struct {
	Rank                         int
	Title, Genre, Href, CoverURL string
}

type chipView struct {
	Label, Href string
	Current     bool
}

type shelfView struct {
	Title, MoreHref, Empty string
	Cards                  []cardView
}

type exploreView struct {
	Empty     bool
	CanCreate bool
	Featured  *featuredView
	RankTabs  []ui.SegLink
	Rankings  []rankRow
	Rank      string
	Genre     string
	Hand      bool
	ArtLabel  string
	Chips     []chipView
	Shelf     shelfView
	Rising    shelfView
}

func exploreURL(rank, genre string, hand bool) string {
	q := url.Values{}
	if rank != "" && rank != RankRising {
		q.Set("rank", rank)
	}
	if genre != "" {
		q.Set("genre", genre)
	}
	if hand {
		q.Set("hand", "1")
	}
	if len(q) == 0 {
		return "/explore"
	}
	return "/explore?" + q.Encode()
}

func (m *Module) explorePage(w http.ResponseWriter, r *http.Request) error {
	v, ok, err := m.viewer(w, r)
	if err != nil || !ok {
		return err
	}
	ctx := r.Context()
	q := r.URL.Query()
	rank := q.Get("rank")
	if rank != RankWeek && rank != RankAll {
		rank = RankRising
	}
	genre, hand := parseGenre(q.Get("genre")), q.Get("hand") == "1"
	view, err := m.buildExplore(ctx, v, rank, genre, hand)
	if err != nil {
		return err
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "nav.explore"), Section: layout.SectionExplore,
		Tab: layout.TabExplore, NoSidebar: true}, exploreBody(view))
}

func (m *Module) buildExplore(ctx context.Context, v publish.Viewer, rank, genre string, hand bool) (exploreView, error) {
	view := exploreView{Rank: rank, Genre: genre, Hand: hand, CanCreate: core.UserFrom(ctx).CanCreate()}
	all, err := m.listed(ctx, v, publish.SeriesFilter{})
	if err != nil {
		return view, err
	}
	if len(all) == 0 {
		view.Empty = true
		return view, nil
	}
	now := m.now()
	latest, err := m.latestPublic(ctx, all)
	if err != nil {
		return view, err
	}
	ids := make([]string, len(all))
	for i, s := range all {
		ids[i] = s.ID
	}
	act, err := m.activity(ctx, ids, now)
	if err != nil {
		return view, err
	}

	// Featured: the series with the newest public chapter.
	featured := all[0]
	var newest time.Time
	for _, s := range all {
		if t, ok := latest[s.ID]; ok && t.After(newest) {
			featured, newest = s, t
		}
	}
	if view.Featured, err = m.featured(ctx, v, featured, now); err != nil {
		return view, err
	}

	// Rankings (over every listed series; the filters below apply to shelves).
	for _, tab := range []string{RankRising, RankWeek, RankAll} {
		view.RankTabs = append(view.RankTabs, ui.SegLink{Label: i18n.T(ctx, "discover.rank."+tab),
			Href: exploreURL(tab, genre, hand), Current: tab == rank})
	}
	for i, s := range first(act.Rank(all, rank), rankingSize) {
		view.Rankings = append(view.Rankings, rankRow{Rank: i + 1, Title: s.Title, Genre: firstGenre(ctx, s),
			Href: seriesHref(s), CoverURL: m.coverURL(ctx, s)})
	}

	view.Chips = append(view.Chips, chipView{Label: i18n.T(ctx, "discover.genre.all"), Href: exploreURL(rank, "", hand), Current: genre == ""})
	for _, g := range exploreGenres {
		view.Chips = append(view.Chips, chipView{Label: genreLabel(ctx, g), Href: exploreURL(rank, g, hand), Current: genre == g})
	}
	view.ArtLabel = i18n.T(ctx, "discover.art.any")
	if hand {
		view.ArtLabel = i18n.T(ctx, "discover.art.hand")
	}

	filtered := filterShelf(all, genre, hand)
	if view.Shelf, err = m.firstShelf(ctx, v, all, filtered, latest, act, genre, now); err != nil {
		return view, err
	}
	if view.Rising, err = m.risingShelf(ctx, v, all, filtered, act); err != nil {
		return view, err
	}
	return view, nil
}

func first(list []publish.Series, n int) []publish.Series {
	if len(list) > n {
		return list[:n]
	}
	return list
}

func filterShelf(list []publish.Series, genre string, hand bool) []publish.Series {
	var out []publish.Series
	for _, s := range list {
		if genre != "" && !hasFold(s.Genres, genre) {
			continue
		}
		if hand && !HandDrawn(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func hasFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), v) {
			return true
		}
	}
	return false
}

func (m *Module) featured(ctx context.Context, v publish.Viewer, s publish.Series, now time.Time) (*featuredView, error) {
	f := &featuredView{Title: s.Title, Synopsis: excerpt(s.Synopsis, 140), Follow: m.follow(ctx, v, s, "/explore")}
	f.ArtURL = m.assets.URLFor(ctx, s.BannerAssetID, coverTTL)
	if f.ArtURL == "" {
		f.ArtURL = m.coverURL(ctx, s)
	}
	p, ok, err := m.latestChapter(ctx, v, s)
	if err != nil {
		return nil, err
	}
	f.Eyebrow = i18n.T(ctx, "discover.featured.series")
	if ok {
		f.ReadHref = chapterHref(s, p)
		f.ReadLabel = i18n.T(ctx, "discover.featured.read", "number", strconv.FormatFloat(p.Number, 'f', -1, 64))
		switch at := p.PublishedAt; {
		case at != nil && sameDay(*at, now):
			f.Eyebrow = i18n.T(ctx, "discover.featured.today")
		case at != nil && now.Sub(*at) < newChapter:
			f.Eyebrow = i18n.T(ctx, "discover.featured.new")
		default:
			f.Eyebrow = i18n.T(ctx, "discover.featured.latest")
		}
	}
	return f, nil
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

// firstShelf is "Because you read …" for members with reading history on this
// server, otherwise "New & updated" (newest public chapter first).
func (m *Module) firstShelf(ctx context.Context, v publish.Viewer, all, filtered []publish.Series, latest map[string]time.Time,
	act activity, genre string, now time.Time) (shelfView, error) {
	badge := func(s publish.Series) string {
		if t, ok := latest[s.ID]; ok && now.Sub(t) < newChapter {
			return i18n.T(ctx, "discover.new_chapter")
		}
		return ""
	}
	if v.UserID != "" {
		recent, err := analytics.RecentlyReadSeries(ctx, m.d.DB, v.UserID, 20)
		if err != nil {
			return shelfView{}, err
		}
		if base, ok := firstListed(recent, all); ok {
			read := map[string]bool{}
			for _, id := range recent {
				read[id] = true
			}
			recs := recommend(base, filtered, read, act)
			if len(recs) > 0 {
				sh := shelfView{Title: i18n.T(ctx, "discover.because", "series", base.Title), MoreHref: searchURL(searchParams{Genre: firstOr(base.Genres, genre)})}
				for _, s := range first(recs, shelfSize) {
					c := m.card(ctx, s, firstGenre(ctx, s))
					c.Badge = badge(s)
					sh.Cards = append(sh.Cards, c)
				}
				return sh, nil
			}
		}
	}
	sh := shelfView{Title: i18n.T(ctx, "discover.new_updated"), MoreHref: searchURL(searchParams{Genre: genre}),
		Empty: i18n.T(ctx, "discover.shelf.empty")}
	ordered := append([]publish.Series(nil), filtered...)
	when := func(s publish.Series) time.Time {
		if t, ok := latest[s.ID]; ok {
			return t
		}
		if s.PublishedAt != nil {
			return *s.PublishedAt
		}
		return time.Time{}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return when(ordered[i]).After(when(ordered[j])) })
	for _, s := range first(ordered, shelfSize) {
		c := m.card(ctx, s, firstGenre(ctx, s))
		c.Badge = badge(s)
		sh.Cards = append(sh.Cards, c)
	}
	return sh, nil
}

func firstOr(list []string, def string) string {
	if len(list) > 0 {
		return strings.ToLower(list[0])
	}
	return def
}

func firstListed(ids []string, all []publish.Series) (publish.Series, bool) {
	byID := make(map[string]publish.Series, len(all))
	for _, s := range all {
		byID[s.ID] = s
	}
	for _, id := range ids {
		if s, ok := byID[id]; ok {
			return s, true
		}
	}
	return publish.Series{}, false
}

// recommend ranks series sharing a genre with base by shared genres, then
// shared tags, then reads in the last 30 days, skipping what the member read.
func recommend(base publish.Series, list []publish.Series, read map[string]bool, act activity) []publish.Series {
	type scored struct {
		s             publish.Series
		genres, tags  int
		recentReaders int64
	}
	var out []scored
	for _, s := range list {
		if s.ID == base.ID || read[s.ID] {
			continue
		}
		g := overlap(base.Genres, s.Genres)
		if g == 0 {
			continue
		}
		out = append(out, scored{s, g, overlap(base.Tags, s.Tags), act.reads30[s.ID]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.genres != b.genres {
			return a.genres > b.genres
		}
		if a.tags != b.tags {
			return a.tags > b.tags
		}
		return a.recentReaders > b.recentReaders
	})
	res := make([]publish.Series, len(out))
	for i, s := range out {
		res[i] = s.s
	}
	return res
}

func overlap(a, b []string) int {
	n := 0
	for _, x := range a {
		if hasFold(b, x) {
			n++
		}
	}
	return n
}

// risingShelf ranks creators' first series — owners with exactly one listed
// series — by follows plus reads in the last 30 days (recorded only).
func (m *Module) risingShelf(ctx context.Context, v publish.Viewer, all, filtered []publish.Series, act activity) (shelfView, error) {
	sh := shelfView{Title: i18n.T(ctx, "discover.rising"), Empty: i18n.T(ctx, "discover.rising.empty")}
	owned := map[string]int{}
	for _, s := range all {
		owned[s.OwnerID]++
	}
	score := func(s publish.Series) int64 { return act.reads30[s.ID] + act.follows30[s.ID] }
	var picks []publish.Series
	for _, s := range filtered {
		if owned[s.OwnerID] == 1 && score(s) > 0 {
			picks = append(picks, s)
		}
	}
	sort.SliceStable(picks, func(i, j int) bool { return score(picks[i]) > score(picks[j]) })
	for i, s := range first(picks, risingSize) {
		meta := ""
		p, ok, err := m.latestChapter(ctx, v, s)
		if err != nil {
			return sh, err
		}
		if ok {
			meta = i18n.T(ctx, "discover.chapter_short", "number", strconv.FormatFloat(p.Number, 'f', -1, 64))
		}
		c := m.card(ctx, s, meta)
		c.Rank = i + 1
		sh.Cards = append(sh.Cards, c)
	}
	return sh, nil
}

// excerpt shortens text to at most n runes on a word boundary.
func excerpt(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	r := []rune(text)[:n]
	s := string(r)
	if i := strings.LastIndexByte(s, ' '); i > n/2 {
		s = s[:i]
	}
	return strings.TrimRight(s, " ,.;:") + "…"
}
