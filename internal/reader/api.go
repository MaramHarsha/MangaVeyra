package reader

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// readable re-checks through publish.Service that the viewer may read the
// publication of the series, and returns the series. Every write of a
// progress, bookmark, download or event row starts here, so rows never point
// at something the viewer cannot read.
func (m *Module) readable(ctx context.Context, v publish.Viewer, seriesID, publicationID string) (publish.Series, error) {
	if !db.ValidID(seriesID) || !db.ValidID(publicationID) {
		return publish.Series{}, core.ErrNotFound()
	}
	s, err := m.pub.SeriesByID(ctx, v, seriesID)
	if err != nil {
		return publish.Series{}, notFound(err)
	}
	if !m.pub.CanRead(ctx, v, s, publish.Publication{ID: publicationID, SeriesID: s.ID}) {
		return publish.Series{}, core.ErrNotFound()
	}
	if need, err := (gate{m: m}).Required(ctx, v, s); err != nil {
		return publish.Series{}, err
	} else if need {
		return publish.Series{}, core.ErrNotFound()
	}
	return s, nil
}

// publicationOf finds a readable publication of s by ID across its languages.
func (m *Module) publicationOf(ctx context.Context, v publish.Viewer, s publish.Series, id string) (publish.Publication, bool) {
	langs := append([]string{s.OriginalLang}, s.Languages...)
	seen := map[string]bool{}
	for _, l := range langs {
		if seen[l] {
			continue
		}
		seen[l] = true
		pubs, err := m.pub.Publications(ctx, v, s.ID, l)
		if err != nil {
			continue
		}
		for _, p := range pubs {
			if p.ID == id {
				return p, true
			}
		}
	}
	return m.publicationByLink(ctx, v, s, id)
}

// publicationByLink reads a publication that Publications does not list for
// the viewer (an unlisted chapter is read by its link only) the way its link
// does: through publish.Service.Publication by number and language.
func (m *Module) publicationByLink(ctx context.Context, v publish.Viewer, s publish.Series, id string) (publish.Publication, bool) {
	if !db.ValidID(id) {
		return publish.Publication{}, false
	}
	seriesID, number, lang, err := m.store.publicationKey(ctx, id)
	if err != nil || seriesID != s.ID {
		return publish.Publication{}, false
	}
	p, _, err := m.pub.Publication(ctx, v, s.ID, number, lang)
	if err != nil || p.ID != id {
		return publish.Publication{}, false
	}
	return p, true
}

// ---- progress -----------------------------------------------------------------

type progressInput struct {
	SeriesID      string  `json:"series_id"`
	PublicationID string  `json:"publication_id"`
	Page          int     `json:"page"`
	Percent       float64 `json:"percent"`
	Completed     bool    `json:"completed"`
}

func (m *Module) apiProgress(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var in progressInput
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return core.ErrBadRequest("invalid JSON")
	}
	v := viewer(r)
	s, err := m.readable(ctx, v, in.SeriesID, in.PublicationID)
	if err != nil {
		return err
	}
	if in.Page < 1 || in.Page > 10000 || math.IsNaN(in.Percent) || in.Percent < 0 || in.Percent > 1 {
		return core.ErrValidation(map[string]string{"page": "invalid"})
	}
	p, err := m.store.saveProgress(ctx, Progress{UserID: v.UserID, PublicationID: in.PublicationID, SeriesID: s.ID,
		Page: in.Page, Percent: in.Percent, Completed: in.Completed})
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, p)
	return nil
}

// ---- per-series settings --------------------------------------------------------

func (m *Module) apiPrefs(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var in SeriesPrefs
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return core.ErrBadRequest("invalid JSON")
	}
	v := viewer(r)
	s, err := m.pub.SeriesByID(ctx, v, r.PathValue("series"))
	if err != nil {
		return notFound(err)
	}
	row, ok := validPrefsInput(s, in)
	if !ok {
		return core.ErrValidation(map[string]string{"prefs": "invalid"})
	}
	if err := m.store.saveSeriesPrefs(ctx, v.UserID, s.ID, row); err != nil {
		return err
	}
	up := core.PrefsFrom(ctx)
	core.JSON(w, http.StatusOK, resolvePrefs(s, &up, &row, up.ReducedMotion == "on"))
	return nil
}

// ---- bookmarks ----------------------------------------------------------------

func (m *Module) apiBookmarks(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	v := viewer(r)
	pubID := r.URL.Query().Get("publication")
	seriesID := r.URL.Query().Get("series")
	if pubID == "" {
		return core.ErrBadRequest("publication is required")
	}
	if _, err := m.readable(ctx, v, seriesID, pubID); err != nil {
		return err
	}
	list, err := m.store.bookmarksFor(ctx, v.UserID, pubID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": list})
	return nil
}

type bookmarkInput struct {
	SeriesID      string `json:"series_id"`
	PublicationID string `json:"publication_id"`
	Page          int    `json:"page"`
	Panel         int    `json:"panel"`
	Note          string `json:"note"`
}

func (m *Module) apiAddBookmark(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var in bookmarkInput
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return core.ErrBadRequest("invalid JSON")
	}
	v := viewer(r)
	s, err := m.readable(ctx, v, in.SeriesID, in.PublicationID)
	if err != nil {
		return err
	}
	if in.Page < 1 || in.Page > 10000 || in.Panel < 0 || in.Panel > 999 {
		return core.ErrValidation(map[string]string{"page": "invalid"})
	}
	note := strings.TrimSpace(in.Note)
	if len([]rune(note)) > 280 {
		return core.ErrValidation(map[string]string{"note": "too long"})
	}
	panel := ""
	if in.Panel > 0 {
		panel = strconv.Itoa(in.Panel)
	}
	b, created, err := m.store.addBookmark(ctx, Bookmark{UserID: v.UserID, SeriesID: s.ID, PublicationID: in.PublicationID,
		Page: in.Page, Panel: panel, Note: note})
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	core.JSON(w, status, b)
	return nil
}

func (m *Module) apiDeleteBookmark(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !db.ValidID(id) {
		return core.ErrNotFound()
	}
	if err := m.store.deleteBookmark(r.Context(), viewer(r).UserID, id); err != nil {
		return notFound(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- downloads ----------------------------------------------------------------

// downloadManifest tells the island what to cache for offline reading.
type downloadManifest struct {
	Download  downloadProps `json:"download"`
	Cache     string        `json:"cache"`
	ReaderURL string        `json:"reader_url"`
	DataURL   string        `json:"data_url"`
	Pages     []pageView    `json:"pages"`
}

// OfflineCacheName is the service-worker cache of a publication (ADR 0012 §7).
func OfflineCacheName(publicationID string) string { return "mv-offline-" + publicationID }

type downloadInput struct {
	SeriesID      string `json:"series_id"`
	PublicationID string `json:"publication_id"`
}

// apiStartDownload re-checks access, records a pending download with a fresh
// expiry and returns the pages to cache.
func (m *Module) apiStartDownload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var in downloadInput
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return core.ErrBadRequest("invalid JSON")
	}
	v := viewer(r)
	s, err := m.readable(ctx, v, in.SeriesID, in.PublicationID)
	if err != nil {
		return err
	}
	pub, ok := m.publicationOf(ctx, v, s, in.PublicationID)
	if !ok {
		return core.ErrNotFound()
	}
	_, pages, err := m.pub.Publication(ctx, v, s.ID, pub.Number, pub.Lang)
	if err != nil {
		return notFound(err)
	}
	pv, err := m.signPages(ctx, v, pages)
	if err != nil {
		return err
	}
	d, err := m.store.startDownload(ctx, v.UserID, pub.ID, DownloadTTL)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, downloadManifest{Download: *toDownloadProps(&d), Cache: OfflineCacheName(pub.ID),
		ReaderURL: chapterURL(s, pub.Number, pub.Lang),
		DataURL:   "/api/v1/reader/series/" + s.ID + "/chapters/" + numberText(pub.Number) + "?lang=" + pub.Lang,
		Pages:     pv})
	return nil
}

type finishInput struct {
	Bytes int64 `json:"bytes"`
}

func (m *Module) apiFinishDownload(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id := r.PathValue("id")
	if !db.ValidID(id) {
		return core.ErrNotFound()
	}
	var in finishInput
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return core.ErrBadRequest("invalid JSON")
	}
	if in.Bytes < 0 || in.Bytes > 1<<36 {
		return core.ErrValidation(map[string]string{"bytes": "invalid"})
	}
	v := viewer(r)
	d, err := m.store.download(ctx, v.UserID, id)
	if err != nil {
		return notFound(err)
	}
	if _, err := m.readable(ctx, v, d.SeriesID, d.PublicationID); err != nil {
		_ = m.store.expireDownload(ctx, v.UserID, id)
		return err
	}
	d, err = m.store.finishDownload(ctx, v.UserID, id, in.Bytes)
	if err != nil {
		return notFound(err)
	}
	core.JSON(w, http.StatusOK, toDownloadProps(&d))
	return nil
}

func (m *Module) apiDeleteDownload(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !db.ValidID(id) {
		return core.ErrNotFound()
	}
	if err := m.store.deleteDownload(r.Context(), viewer(r).UserID, id); err != nil {
		return notFound(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiClearDownloads removes every download record of the member (paired with
// the device-side "Clear offline downloads").
func (m *Module) apiClearDownloads(w http.ResponseWriter, r *http.Request) error {
	n, err := m.store.clearDownloads(r.Context(), viewer(r).UserID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]int64{"removed": n})
	return nil
}

// downloadItem is one download in the island's reconcile list.
type downloadItem struct {
	downloadProps
	PublicationID string `json:"publication_id"`
	Cache         string `json:"cache"`
	Readable      bool   `json:"readable"`
}

// apiDownloads lists the member's downloads; ones they may no longer read
// (or that expired) come back expired so the island can drop their caches.
func (m *Module) apiDownloads(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	v := viewer(r)
	list, err := m.store.downloads(ctx, v.UserID, 200)
	if err != nil {
		return err
	}
	out := make([]downloadItem, 0, len(list))
	for _, d := range list {
		item := downloadItem{downloadProps: *toDownloadProps(&d), PublicationID: d.PublicationID, Cache: OfflineCacheName(d.PublicationID)}
		_, rerr := m.readable(ctx, v, d.SeriesID, d.PublicationID)
		item.Readable = rerr == nil && !d.Expired(m.now())
		if !item.Readable && d.Status != DownloadExpired {
			_ = m.store.expireDownload(ctx, v.UserID, d.ID)
			item.Status = DownloadExpired
		}
		out = append(out, item)
	}
	core.JSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}
