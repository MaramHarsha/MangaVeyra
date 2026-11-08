package reader

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// chapterNumber parses the {chapter} path segment ("8", "8.5").
func chapterNumber(s string) (float64, bool) {
	if s == "" || len(s) > 16 {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0, false
	}
	return n, true
}

// numberText formats a chapter number for URLs and labels ("8", "8.5").
func numberText(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// chapterURL is the reader URL of a chapter; lang is omitted for the original language.
func chapterURL(s publish.Series, number float64, lang string) string {
	u := "/s/" + url.PathEscape(s.Slug) + "/" + numberText(number)
	if lang != "" && lang != s.OriginalLang {
		u += "?lang=" + url.QueryEscape(lang)
	}
	return u
}

// pageView is one frozen page as the reader shows it.
type pageView struct {
	ID      string                `json:"id"`
	Number  int                   `json:"number"`
	Width   int                   `json:"width"`
	Height  int                   `json:"height"`
	Src     string                `json:"src"`
	Alt     string                `json:"alt"`
	Panels  []publish.GuidedPanel `json:"panels"`
	Missing bool                  `json:"missing,omitempty"`
}

// chapterLink is one chapter of the series in the reader's chapter navigation.
type chapterLink struct {
	ID        string  `json:"id"`
	Number    float64 `json:"number"`
	Title     string  `json:"title"`
	Label     string  `json:"label"`
	Href      string  `json:"href"`
	Completed bool    `json:"completed"`
	Current   bool    `json:"current"`
}

// langLink is one published language of the current chapter.
type langLink struct {
	Lang    string `json:"lang"`
	Label   string `json:"label"`
	Href    string `json:"href"`
	Current bool   `json:"current"`
}

// provenanceRow is one role of the per-role AI label.
type provenanceRow struct {
	Role  string `json:"role"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// readerView is everything the reader page and island need.
type readerView struct {
	Series     publish.Series
	Pub        publish.Publication
	Pages      []pageView
	Chapters   []chapterLink
	Prev       *chapterLink
	Next       *chapterLink
	Langs      []langLink
	Prefs      Prefs
	SignedIn   bool
	Progress   *Progress
	Bookmarks  []Bookmark
	Download   *Download
	StartPage  int
	StartPanel int
	BackURL    string
	SeriesURL  string
	Label      string
	Labels     []provenanceRow
	Props      string
}

// webtoon reports whether the series reads as a vertical strip.
func (v *readerView) webtoon() bool { return isWebtoon(v.Series.Format) }

// chapterLabel is "Ch. 8 · The Lighthouse Debt" (or "Ep. 12 · Static" for webtoons).
func chapterLabel(ctx context.Context, s publish.Series, p publish.Publication) string {
	key := "reader.chapter.label"
	if isWebtoon(s.Format) {
		key = "reader.episode.label"
	}
	if strings.TrimSpace(p.Title) == "" {
		return i18n.T(ctx, key+".untitled", "number", numberText(p.Number))
	}
	return i18n.T(ctx, key, "number", numberText(p.Number), "title", p.Title)
}

// shortLabel is "12 · Static" for the episode list.
func shortLabel(p publish.Publication) string {
	if strings.TrimSpace(p.Title) == "" {
		return numberText(p.Number)
	}
	return numberText(p.Number) + " · " + p.Title
}

// provenance lists the per-role AI label in the designed order.
func provenance(ctx context.Context, label map[string]string) []provenanceRow {
	out := []provenanceRow{}
	for _, role := range []string{"script", "art", "lettering"} {
		kind := label[role]
		switch kind {
		case ui.ProvenanceHuman, ui.ProvenanceAIAssisted, ui.ProvenanceAI:
		default:
			continue
		}
		out = append(out, provenanceRow{Role: role, Kind: kind, Label: i18n.T(ctx, "reader.ai."+role+"."+kind)})
	}
	return out
}

// languageName is the reader-facing name of a content language.
func languageName(ctx context.Context, lang string) string {
	key := "reader.lang." + lang
	if v := i18n.T(ctx, key); v != key {
		return v
	}
	return strings.ToUpper(lang)
}

// backURL picks where the reader's back arrow goes: the series page when it
// exists, otherwise the library or the root.
func backURL(ctx context.Context, s publish.Series, signedIn bool) string {
	if p := "/s/" + url.PathEscape(s.Slug); ui.Ready(ctx, p) {
		return p
	}
	if signedIn && ui.Ready(ctx, "/library/history") {
		return "/library/history"
	}
	if ui.Ready(ctx, "/explore") {
		return "/explore"
	}
	return "/"
}

// pickLanguage chooses the chapter language: the explicit ?lang=, else the
// first of the member's reading languages the series is published in, else
// the series' original language.
func pickLanguage(r *http.Request, s publish.Series) (lang string, explicit bool) {
	if l := strings.TrimSpace(r.URL.Query().Get("lang")); l != "" {
		return l, true
	}
	if core.UserFrom(r.Context()) != nil {
		for _, l := range core.PrefsFrom(r.Context()).Languages.Reading {
			if slices.Contains(s.Languages, l) {
				return l, false
			}
		}
	}
	return s.OriginalLang, false
}

// loadChapter reads a readable chapter in lang (falling back to the
// original language when lang was only a preference).
func (m *Module) loadChapter(ctx context.Context, v publish.Viewer, s publish.Series, number float64, lang string, explicit bool) (publish.Publication, []publish.PublicationPage, error) {
	pub, pages, err := m.pub.Publication(ctx, v, s.ID, number, lang)
	if errors.Is(err, publish.ErrNotFound) && !explicit && lang != s.OriginalLang {
		return m.pub.Publication(ctx, v, s.ID, number, "")
	}
	return pub, pages, err
}

// signPages returns the page views with fresh signed image URLs. A page whose
// image is gone keeps its alt-text and is marked missing.
func (m *Module) signPages(ctx context.Context, v publish.Viewer, pages []publish.PublicationPage) ([]pageView, error) {
	out := make([]pageView, 0, len(pages))
	for _, pg := range pages {
		pv := pageView{ID: pg.ID, Number: pg.Number, Width: pg.Width, Height: pg.Height, Alt: pg.AltText, Panels: pg.Panels}
		if pv.Panels == nil {
			pv.Panels = []publish.GuidedPanel{}
		}
		src, err := m.pub.PageImageURL(ctx, v, pg, PageURLTTL)
		switch {
		case err == nil:
			pv.Src = src
		case errors.Is(err, publish.ErrNotFound):
			pv.Missing = true
		default:
			return nil, err
		}
		out = append(out, pv)
	}
	return out, nil
}

// buildChapters lists the readable chapters of the series in the
// publication's language and marks the current and finished ones.
func (m *Module) buildChapters(ctx context.Context, v publish.Viewer, s publish.Series, cur publish.Publication) ([]chapterLink, *chapterLink, *chapterLink, error) {
	pubs, err := m.pub.Publications(ctx, v, s.ID, cur.Lang)
	if err != nil {
		return nil, nil, nil, err
	}
	done := map[string]bool{}
	if v.UserID != "" {
		if done, err = m.store.completedIn(ctx, v.UserID, s.ID); err != nil {
			return nil, nil, nil, err
		}
	}
	out := make([]chapterLink, 0, len(pubs))
	idx := -1
	for _, p := range pubs {
		c := chapterLink{ID: p.ID, Number: p.Number, Title: p.Title, Label: shortLabel(p), Href: chapterURL(s, p.Number, p.Lang),
			Completed: done[p.ID], Current: p.ID == cur.ID}
		if c.Current {
			idx = len(out)
		}
		out = append(out, c)
	}
	var prev, next *chapterLink
	if idx > 0 {
		c := out[idx-1]
		prev = &c
	}
	if idx >= 0 && idx < len(out)-1 {
		c := out[idx+1]
		next = &c
	}
	return out, prev, next, nil
}

// buildLanguages lists the languages the current chapter number is published in.
func (m *Module) buildLanguages(ctx context.Context, v publish.Viewer, s publish.Series, cur publish.Publication) []langLink {
	out := []langLink{}
	for _, l := range s.Languages {
		if l != cur.Lang {
			if _, _, err := m.pub.Publication(ctx, v, s.ID, cur.Number, l); err != nil {
				continue
			}
		}
		out = append(out, langLink{Lang: l, Label: languageName(ctx, l), Href: chapterURL(s, cur.Number, l), Current: l == cur.Lang})
	}
	return out
}

// startPosition picks the opening page: ?page=, then saved progress, then 1.
func startPosition(r *http.Request, p *Progress, pageCount int) (page, panel int) {
	page = 1
	if p != nil && !p.Completed && p.Page > 0 {
		page = p.Page
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
		page = n
	}
	if pageCount > 0 && page > pageCount {
		page = pageCount
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("panel")); err == nil && n > 0 && n < 1000 {
		panel = n
	}
	return page, panel
}

// readerSessionCookie is the anonymous reader's first-party session
// identifier for analytics; the recorder stores only a salted, daily-rotated
// hash of it (ADR 0012 §8).
const readerSessionCookie = "mv_rsid"

// sessionID returns the first-party reading session of the request, creating
// the anonymous cookie when needed (w may be nil to only read it).
func sessionID(w http.ResponseWriter, r *http.Request) string {
	if id := core.IdentityFrom(r.Context()).SessionID; id != "" {
		return id
	}
	if c, err := r.Cookie(readerSessionCookie); err == nil && len(c.Value) >= 16 && len(c.Value) <= 64 {
		return c.Value
	}
	if w == nil {
		return ""
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: readerSessionCookie, Value: id, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Mv-Https") == "1"})
	return id
}

// islandProps is the JSON the reader island boots from.
type islandProps struct {
	View        string            `json:"view"`
	Series      seriesProps       `json:"series"`
	Publication pubProps          `json:"publication"`
	Pages       []pageView        `json:"pages"`
	URLTTL      int               `json:"url_ttl"`
	SignedAt    int64             `json:"signed_at"`
	Chapters    []chapterLink     `json:"chapters"`
	Prev        *chapterLink      `json:"prev"`
	Next        *chapterLink      `json:"next"`
	Langs       []langLink        `json:"langs"`
	Prefs       Prefs             `json:"prefs"`
	SignedIn    bool              `json:"signed_in"`
	Progress    *Progress         `json:"progress"`
	Bookmarks   []Bookmark        `json:"bookmarks"`
	Download    *downloadProps    `json:"download"`
	StartPage   int               `json:"start_page"`
	StartPanel  int               `json:"start_panel"`
	BackURL     string            `json:"back_url"`
	SeriesURL   string            `json:"series_url"`
	LoginURL    string            `json:"login_url"`
	LibraryURL  string            `json:"library_url"`
	Provenance  []provenanceRow   `json:"provenance"`
	API         apiProps          `json:"api"`
	Comments    bool              `json:"comments"`
	Events      bool              `json:"events"`
	UILang      string            `json:"ui_lang"`
	Messages    map[string]string `json:"messages"`
}

type seriesProps struct {
	ID        string            `json:"id"`
	Slug      string            `json:"slug"`
	Title     string            `json:"title"`
	Format    string            `json:"format"`
	AgeRating string            `json:"age_rating"`
	AILabel   map[string]string `json:"ai_label"`
}

type pubProps struct {
	ID        string            `json:"id"`
	Number    float64           `json:"number"`
	Title     string            `json:"title"`
	Label     string            `json:"label"`
	Lang      string            `json:"lang"`
	PageCount int               `json:"page_count"`
	AILabel   map[string]string `json:"ai_label"`
}

type downloadProps struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ExpiresAt int64  `json:"expires_at"`
}

type apiProps struct {
	Chapter   string `json:"chapter"`
	Progress  string `json:"progress"`
	Prefs     string `json:"prefs"`
	Bookmarks string `json:"bookmarks"`
	Downloads string `json:"downloads"`
	Events    string `json:"events"`
	Comments  string `json:"comments"`
}

func toDownloadProps(d *Download) *downloadProps {
	if d == nil {
		return nil
	}
	out := &downloadProps{ID: d.ID, Status: d.Status}
	if d.ExpiresAt.Valid {
		out.ExpiresAt = d.ExpiresAt.Time.Unix()
	}
	return out
}

// commentsAPI is agent S's comment list/create endpoint (ADR 0012 §9).
const commentsAPI = "/api/v1/social/comments"

func (m *Module) props(ctx context.Context, r *http.Request, v *readerView) string {
	p := islandProps{
		View: "reader",
		Series: seriesProps{ID: v.Series.ID, Slug: v.Series.Slug, Title: v.Series.Title, Format: v.Series.Format,
			AgeRating: v.Series.AgeRating, AILabel: v.Series.AILabel},
		Publication: pubProps{ID: v.Pub.ID, Number: v.Pub.Number, Title: v.Pub.Title, Label: v.Label, Lang: v.Pub.Lang,
			PageCount: len(v.Pages), AILabel: v.Pub.AILabel},
		Pages: v.Pages, URLTTL: int(PageURLTTL / time.Second), SignedAt: m.now().Unix(),
		Chapters: v.Chapters, Prev: v.Prev, Next: v.Next, Langs: v.Langs, Prefs: v.Prefs,
		SignedIn: v.SignedIn, Progress: v.Progress, Bookmarks: v.Bookmarks, Download: toDownloadProps(v.Download),
		StartPage: v.StartPage, StartPanel: v.StartPanel, BackURL: v.BackURL, SeriesURL: v.SeriesURL,
		LoginURL: "/login?next=" + url.QueryEscape(r.URL.RequestURI()), LibraryURL: "/library/downloads",
		Provenance: v.Labels,
		API: apiProps{
			Chapter:   "/api/v1/reader/series/" + url.PathEscape(v.Series.ID) + "/chapters/" + numberText(v.Pub.Number) + "?lang=" + url.QueryEscape(v.Pub.Lang),
			Progress:  "/api/v1/reader/progress",
			Prefs:     "/api/v1/reader/prefs/" + url.PathEscape(v.Series.ID),
			Bookmarks: "/api/v1/reader/bookmarks",
			Downloads: "/api/v1/reader/downloads",
			Events:    "/api/v1/reader/events",
			Comments:  commentsAPI,
		},
		Comments: ui.Ready(ctx, commentsAPI),
		Events:   true,
		UILang:   i18n.LocaleFrom(ctx),
		Messages: islandMessages(ctx),
	}
	if _, ok := m.recorder(); !ok {
		p.Events = false
	}
	if p.Bookmarks == nil {
		p.Bookmarks = []Bookmark{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// islandKeys are the reader.* catalogue keys the island displays.
var islandKeys = []string{
	"back", "mode", "mode.paged", "mode.spread", "mode.guided", "mode.webtoon", "translate", "translate.soon",
	"bookmark", "bookmark.add", "bookmark.remove", "bookmark.added", "bookmark.removed", "bookmark.signin",
	"download", "download.signin", "download.start", "download.progress", "download.done", "download.failed",
	"download.remove", "download.removed", "download.offline", "download.unsupported", "download.expires",
	"settings", "settings.title", "settings.close", "settings.direction", "settings.rtl", "settings.ltr",
	"settings.background", "settings.bg.ink", "settings.bg.grey", "settings.bg.paper", "settings.zoom",
	"settings.brightness", "settings.tap_zones", "settings.reduced_motion", "settings.panel_comments",
	"settings.read_aloud", "settings.read_aloud.none", "settings.read_aloud.voice", "settings.living_panels",
	"settings.language", "settings.saved", "settings.saved_device", "soon", "soon_hint",
	"page.of", "pages.of", "panel.of", "panel.label", "alt_text", "page.alt", "prev", "next",
	"prev.page", "next.page", "prev.panel", "next.panel", "zone.prev", "zone.next", "page.missing",
	"episodes", "episode.reading", "episode.read", "next_episode", "next_chapter", "prev_chapter",
	"end.title", "end.title.episode", "end.next", "end.series", "end.provenance", "end.last",
	"comments", "comments.title", "comments.close", "comments.empty", "comments.unavailable", "comments.loading",
	"comments.placeholder", "comments.post", "comments.signin", "comments.panel", "comments.chapter", "comments.failed",
	"comments.posted", "offline.banner", "error.network", "loading", "dir.rtl", "dir.ltr", "dir.vertical",
	"refresh.failed", "speak.start", "speak.stop", "zoom.in", "zoom.out", "zoom.reset",
	"library.remove", "library.not_on_device", "library.on_device", "library.download_again",
	"library.cleared", "library.size", "library.checking",
}

// pageAlt is the page image's alt-text (the frozen page summary, or "Page n").
func pageAlt(ctx context.Context, p pageView) string {
	if strings.TrimSpace(p.Alt) != "" {
		return p.Alt
	}
	return i18n.T(ctx, "reader.page.alt", "page", p.Number)
}

// bubbleLine reads one lettered line for screen readers ("Rin Asagiri: Keep moving!").
func bubbleLine(ctx context.Context, b publish.GuidedBubble) string {
	text := strings.TrimSpace(b.Text)
	if text == "" {
		return ""
	}
	switch {
	case b.Kind == "sfx":
		return i18n.T(ctx, "reader.line.sfx", "text", text)
	case b.Kind == "caption":
		return i18n.T(ctx, "reader.line.caption", "text", text)
	case b.Speaker != "" && b.Kind == "thought":
		return i18n.T(ctx, "reader.line.thought", "speaker", b.Speaker, "text", text)
	case b.Speaker != "":
		return i18n.T(ctx, "reader.line.speech", "speaker", b.Speaker, "text", text)
	}
	return text
}

func islandMessages(ctx context.Context) map[string]string {
	out := make(map[string]string, len(islandKeys))
	for _, k := range islandKeys {
		out[k] = i18n.T(ctx, "reader."+k)
	}
	return out
}
