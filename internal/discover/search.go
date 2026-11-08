package discover

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Search tabs.
const (
	TabSeries   = "series"
	TabCreators = "creators"
	TabTags     = "tags"
)

const (
	resultsPerPage = 20
	maxTags        = 40
)

// searchGenres are the genre filter options (the onboarding genre catalogue).
var searchGenres = []string{"action", "fantasy", "mystery", "romance", "horror", "scifi", "slice_of_life", "comedy", "sports",
	"historical", "isekai", "thriller", "drama", "mecha", "supernatural"}

var (
	formats  = []string{"manga_rtl", "comic_ltr", "webtoon"}
	statuses = []string{publish.SeriesOngoing, publish.SeriesCompleted, publish.SeriesHiatus}
	ratings  = []string{publish.RatingAll, publish.RatingTeen, publish.RatingMature, publish.RatingAdult}
	arts     = []string{"human", "ai_assisted", "ai"}
)

// searchParams are E8's URL parameters. Unknown values read as "Any".
type searchParams struct {
	Q, Tab, Genre, Format, Status, Lang, Rating, Art, Tag string
	Hand, JA                                              bool
	Page                                                  int
}

func oneOf(v string, allowed []string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return ""
}

func parseSearch(q url.Values) searchParams {
	p := searchParams{
		Q:      ParseQuery(q.Get("q")).Text,
		Genre:  parseGenre(q.Get("genre")),
		Format: oneOf(q.Get("format"), formats),
		Status: oneOf(q.Get("status"), statuses),
		Lang:   oneOf(q.Get("lang"), pagedoc.ContentLanguages),
		Rating: oneOf(q.Get("rating"), ratings),
		Art:    oneOf(q.Get("art"), arts),
		Hand:   q.Get("hand") == "1",
		JA:     q.Get("ja") == "1",
		Tab:    oneOf(q.Get("tab"), []string{TabSeries, TabCreators, TabTags}),
		Page:   1,
	}
	if t := strings.TrimSpace(q.Get("tag")); t != "" && utf8.RuneCountInString(t) <= 60 {
		p.Tag = strings.ToValidUTF8(t, "")
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 && n <= maxCandidates/resultsPerPage {
		p.Page = n
	}
	if p.Tab == "" {
		p.Tab = TabSeries
	}
	return p
}

func searchURL(p searchParams) string {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("q", p.Q)
	if p.Tab != TabSeries {
		set("tab", p.Tab)
	}
	set("genre", p.Genre)
	set("format", p.Format)
	set("status", p.Status)
	set("lang", p.Lang)
	set("rating", p.Rating)
	set("art", p.Art)
	set("tag", p.Tag)
	if p.Hand {
		q.Set("hand", "1")
	}
	if p.JA {
		q.Set("ja", "1")
	}
	if p.Page > 1 {
		q.Set("page", strconv.Itoa(p.Page))
	}
	if len(q) == 0 {
		return "/search"
	}
	return "/search?" + q.Encode()
}

// matches applies E8's filters that ListSeries does not (genre already did).
func (p searchParams) matches(s publish.Series) bool {
	switch {
	case p.Format != "" && s.Format != p.Format,
		p.Status != "" && s.Status != p.Status,
		p.Rating != "" && s.AgeRating != p.Rating,
		p.Art != "" && s.AILabel["art"] != p.Art,
		p.Hand && !HandDrawn(s),
		p.Lang != "" && !hasLang(s, p.Lang),
		p.JA && !hasLang(s, "ja"),
		p.Tag != "" && !hasFold(s.Tags, p.Tag):
		return false
	}
	return true
}

func hasLang(s publish.Series, lang string) bool {
	return s.OriginalLang == lang || hasFold(s.Languages, lang)
}

// Search returns the listed series the viewer may read that match q and the
// filters, best match first (newest first without words). It is the E8
// result list; exported for tests.
func (m *Module) Search(ctx context.Context, v publish.Viewer, q Query, p searchParams) ([]publish.Series, error) {
	var list []publish.Series
	var err error
	if q.Empty() {
		list, err = m.listed(ctx, v, publish.SeriesFilter{Genre: p.Genre})
	} else {
		var ids []string
		if ids, err = candidates(ctx, m.d.DB, q); err == nil {
			list, err = m.byIDs(ctx, v, ids, p.Genre)
		}
	}
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, s := range list {
		if p.matches(s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// ---- view ------------------------------------------------------------------

type tabView struct {
	Label, Count, Href string
	Current, Soon      bool
}

func tabText(t tabView) string {
	if t.Count == "" {
		return t.Label
	}
	return t.Label + " " + t.Count
}

type segment struct {
	Text string
	Mark bool
}

type resultView struct {
	Card    cardView
	Meta    string
	Snippet []segment
	Follow  followView
}

type creatorView struct {
	Name, Username, Href, Key, Meta string
}

type tagView struct {
	Tag, Href, Count string
}

type searchView struct {
	Params        searchParams
	Tabs          []tabView
	Results       []resultView
	Creators      []creatorView
	Tags          []tagView
	PrevHref      string
	NextHref      string
	EmptyTitle    string
	EmptyBody     string
	GenreOptions  []ui.Option
	FormatOptions []ui.Option
	StatusOptions []ui.Option
	LangOptions   []ui.Option
	RatingOptions []ui.Option
	ArtOptions    []ui.Option
	Total         int
	HasFilters    bool
	ClearHref     string
}

func (m *Module) searchPage(w http.ResponseWriter, r *http.Request) error {
	v, ok, err := m.viewer(w, r)
	if err != nil || !ok {
		return err
	}
	ctx := r.Context()
	p := parseSearch(r.URL.Query())
	q := ParseQuery(p.Q)
	list, err := m.Search(ctx, v, q, p)
	if err != nil {
		return err
	}
	view, err := m.buildSearch(ctx, v, q, p, list, r.URL.RequestURI())
	if err != nil {
		return err
	}
	return m.ui.Page(w, r, http.StatusOK, web.PageOpts{Title: i18n.T(ctx, "discover.search.title"), Section: layout.SectionExplore,
		Tab: layout.TabExplore, NoSidebar: true}, searchBody(view))
}

func (m *Module) buildSearch(ctx context.Context, v publish.Viewer, q Query, p searchParams, list []publish.Series, here string) (searchView, error) {
	view := searchView{Params: p, Total: len(list)}
	view.HasFilters = p.Genre != "" || p.Format != "" || p.Status != "" || p.Lang != "" || p.Rating != "" || p.Art != "" ||
		p.Tag != "" || p.Hand || p.JA
	view.ClearHref = searchURL(searchParams{Q: p.Q, Tab: p.Tab})
	view.GenreOptions = options(ctx, "discover.filter.any", searchGenres, func(g string) string { return genreLabel(ctx, g) })
	view.FormatOptions = options(ctx, "discover.filter.any", formats, func(f string) string { return i18n.T(ctx, "discover.format."+f) })
	view.StatusOptions = options(ctx, "discover.filter.any", statuses, func(s string) string { return i18n.T(ctx, "discover.status."+s) })
	view.LangOptions = options(ctx, "discover.filter.any", pagedoc.ContentLanguages, func(l string) string { return i18n.T(ctx, "discover.language."+l) })
	view.RatingOptions = options(ctx, "discover.filter.any", ratings, func(r string) string { return i18n.T(ctx, "discover.rating."+r) })
	view.ArtOptions = options(ctx, "discover.filter.any", arts, func(a string) string { return i18n.T(ctx, "discover.art_label."+a) })

	owners := make([]string, 0, len(list))
	for _, s := range list {
		owners = append(owners, s.OwnerID)
	}
	names, err := m.creators(ctx, owners)
	if err != nil {
		return view, err
	}
	creators := m.creatorRows(ctx, q, list, names)
	tags := tagRows(list, p)

	tab := func(key, label string, count int, soon bool) tabView {
		t := tabView{Label: label, Current: p.Tab == key, Soon: soon}
		if !soon {
			t.Count = strconv.Itoa(count)
			np := p
			np.Tab, np.Page = key, 1
			t.Href = searchURL(np)
		}
		return t
	}
	view.Tabs = []tabView{
		tab(TabSeries, i18n.T(ctx, "discover.tab.series"), len(list), false),
		tab(TabCreators, i18n.T(ctx, "discover.tab.creators"), len(creators), false),
		tab("chapters", i18n.T(ctx, "discover.tab.chapters"), 0, true),
		tab(TabTags, i18n.T(ctx, "discover.tab.tags"), len(tags), false),
	}
	view.Tabs[3].Count = "" // the artboard's Tags tab has no count

	switch p.Tab {
	case TabCreators:
		view.Creators = creators
	case TabTags:
		view.Tags = tags
	default:
		start := (p.Page - 1) * resultsPerPage
		if start > len(list) {
			start = len(list)
		}
		end := min(start+resultsPerPage, len(list))
		for _, s := range list[start:end] {
			row, err := m.resultRow(ctx, v, q, s, names, here)
			if err != nil {
				return view, err
			}
			view.Results = append(view.Results, row)
		}
		if p.Page > 1 {
			pp := p
			pp.Page--
			view.PrevHref = searchURL(pp)
		}
		if end < len(list) {
			np := p
			np.Page++
			view.NextHref = searchURL(np)
		}
	}
	switch {
	case !q.Empty():
		view.EmptyTitle = i18n.T(ctx, "discover.search.empty_query", "query", p.Q)
	case view.HasFilters:
		view.EmptyTitle = i18n.T(ctx, "discover.search.empty_filters")
	default:
		view.EmptyTitle = i18n.T(ctx, "discover.search.empty_server")
	}
	view.EmptyBody = i18n.T(ctx, "discover.search.empty_body")
	return view, nil
}

func options(ctx context.Context, anyKey string, values []string, label func(string) string) []ui.Option {
	out := []ui.Option{{Value: "", Label: i18n.T(ctx, anyKey)}}
	for _, v := range values {
		out = append(out, ui.Option{Value: v, Label: label(v)})
	}
	return out
}

func (m *Module) resultRow(ctx context.Context, v publish.Viewer, q Query, s publish.Series, names map[string]creator, here string) (resultView, error) {
	pubs, err := m.publish.Publications(ctx, v, s.ID, "")
	if err != nil && !errors.Is(err, publish.ErrNotFound) {
		return resultView{}, err
	}
	var meta []string
	if c, ok := names[s.OwnerID]; ok {
		meta = append(meta, c.Name)
	}
	if g := firstGenre(ctx, s); g != "" {
		meta = append(meta, g)
	}
	meta = append(meta, i18n.T(ctx, "discover.chapters", "count", len(pubs)))
	return resultView{Card: m.card(ctx, s, ""), Meta: strings.Join(meta, " · "), Snippet: Snippet(s.Synopsis, q.Terms, 28),
		Follow: m.follow(ctx, v, s, here)}, nil
}

// creatorRows lists the owners of the results; with words, only those whose
// name or username contains every word.
func (m *Module) creatorRows(ctx context.Context, q Query, list []publish.Series, names map[string]creator) []creatorView {
	counts := map[string]int{}
	var order []string
	for _, s := range list {
		if _, ok := names[s.OwnerID]; !ok {
			continue
		}
		if counts[s.OwnerID] == 0 {
			order = append(order, s.OwnerID)
		}
		counts[s.OwnerID]++
	}
	match := map[string]bool{}
	if !q.Empty() {
		match = creatorNameMatches(q, names)
	}
	var out []creatorView
	for _, id := range order {
		if !q.Empty() && !match[id] {
			continue
		}
		c := names[id]
		out = append(out, creatorView{Name: c.Name, Username: c.Username, Key: c.ID, Href: "/u/" + url.PathEscape(c.Username),
			Meta: i18n.T(ctx, "discover.creator_series", "count", counts[id])})
	}
	sort.SliceStable(out, func(i, j int) bool { return counts[out[i].Key] > counts[out[j].Key] })
	return out
}

// tagRows counts the tags of the results, most used first.
func tagRows(list []publish.Series, p searchParams) []tagView {
	counts := map[string]int{}
	label := map[string]string{}
	for _, s := range list {
		seen := map[string]bool{}
		for _, t := range s.Tags {
			k := strings.ToLower(strings.TrimSpace(t))
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			counts[k]++
			if _, ok := label[k]; !ok {
				label[k] = strings.TrimSpace(t)
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > maxTags {
		keys = keys[:maxTags]
	}
	out := make([]tagView, 0, len(keys))
	for _, k := range keys {
		np := p
		np.Tab, np.Page, np.Tag = TabSeries, 1, label[k]
		out = append(out, tagView{Tag: label[k], Href: searchURL(np), Count: strconv.Itoa(counts[k])})
	}
	return out
}

// Snippet quotes the part of text around the first matching word, with
// every word occurrence marked. Without a match it is the start of text.
func Snippet(text string, terms []string, radius int) []segment {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil
	}
	runes := []rune(text)
	lower := []rune(strings.ToLower(text))
	if len(lower) != len(runes) { // case mapping changed the length; skip marks
		return []segment{{Text: "“" + excerpt(text, 2*radius) + "”"}}
	}
	at := -1
	for _, t := range terms {
		if i := runeIndex(lower, []rune(t)); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	start, end := 0, min(len(runes), 2*radius)
	if at >= 0 {
		start = max(0, at-radius)
		end = min(len(runes), at+radius)
	}
	// Cut at word boundaries so the quote starts and ends with whole words.
	if start > 0 {
		for i := start; i < at && i < len(runes); i++ {
			if runes[i] == ' ' {
				start = i + 1
				break
			}
		}
	}
	if end < len(runes) {
		for i := end; i > max(start, at+1); i-- {
			if runes[i] == ' ' {
				end = i
				break
			}
		}
	}
	// Marks for every occurrence inside the window.
	marked := make([]bool, len(runes))
	for _, t := range terms {
		tr := []rune(t)
		for i := start; i+len(tr) <= end; {
			j := runeIndex(lower[i:end], tr)
			if j < 0 {
				break
			}
			for k := i + j; k < i+j+len(tr); k++ {
				marked[k] = true
			}
			i += j + len(tr)
		}
	}
	var out []segment
	open := "“"
	if start > 0 {
		open += "…"
	}
	out = append(out, segment{Text: open})
	for i := start; i < end; {
		j := i
		for j < end && marked[j] == marked[i] {
			j++
		}
		out = append(out, segment{Text: string(runes[i:j]), Mark: marked[i]})
		i = j
	}
	closing := "”"
	if end < len(runes) {
		closing = "…" + closing
	}
	return append(out, segment{Text: closing})
}

func runeIndex(s, sub []rune) int {
	if len(sub) == 0 {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for k := range sub {
			if s[i+k] != sub[k] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
