package reader

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/reader/agegate"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Reader limits.
const (
	// PageURLTTL is the lifetime of the signed page image URLs the reader
	// hands out; the island refreshes them shortly before they expire.
	PageURLTTL = 10 * time.Minute
	// CoverURLTTL is the lifetime of signed cover URLs in the library.
	CoverURLTTL = time.Hour
	// DownloadTTL is how long an offline chapter stays readable on a device
	// without the server confirming access again (ADR 0012 §7).
	DownloadTTL = 30 * 24 * time.Hour
	// maxAsyncRecords bounds server-side "open" recordings in flight.
	maxAsyncRecords = 16
)

// Module is the reader feature module: the E3/E4 reader at /s/{series}/{chapter},
// the E7 library, reading progress, per-series settings, bookmarks, offline
// download records, age confirmations and reading-event recording.
type Module struct {
	d      *core.Deps
	ui     *web.UI
	pub    publish.Service
	assets *assets.Service
	store  *store
	now    func() time.Time
	// recSlots bounds the asynchronous recorder calls.
	recSlots chan struct{}
}

// New creates the module and provides agegate.Gate.
func New(d *core.Deps) *Module {
	m := &Module{d: d, now: db.Now, recSlots: make(chan struct{}, maxAsyncRecords)}
	m.store = &store{db: d.DB, now: func() time.Time { return m.now() }}
	core.Provide[agegate.Gate](d, gate{m: m})
	return m
}

// Name implements core.Module.
func (m *Module) Name() string { return "reader" }

// Init resolves the services the reader reads through.
func (m *Module) Init(context.Context) error {
	m.ui = web.From(m.d)
	m.pub = core.MustUse[publish.Service](m.d)
	m.assets = core.MustUse[*assets.Service](m.d)
	return nil
}

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {
	r.Page("GET /s/{series}/{chapter}", m.readerPage)
	r.Page("POST /s/{series}/confirm-age", m.confirmAge, core.RequireSession)

	r.Page("GET /library", m.libraryIndex, core.RequireSession)
	r.Page("GET /library/{tab}", m.libraryPage, core.RequireSession)
	r.Page("POST /library/bookmarks/{id}/delete", m.removeBookmarkForm, core.RequireSession)
	r.Page("POST /library/downloads/{id}/delete", m.removeDownloadForm, core.RequireSession)

	r.API("GET /api/v1/reader/series/{series}/chapters/{chapter}", m.apiChapter)
	r.API("PUT /api/v1/reader/progress", m.apiProgress, core.RequireSession)
	r.API("PUT /api/v1/reader/prefs/{series}", m.apiPrefs, core.RequireSession)
	r.API("GET /api/v1/reader/bookmarks", m.apiBookmarks, core.RequireSession)
	r.API("POST /api/v1/reader/bookmarks", m.apiAddBookmark, core.RequireSession)
	r.API("DELETE /api/v1/reader/bookmarks/{id}", m.apiDeleteBookmark, core.RequireSession)
	r.API("GET /api/v1/reader/downloads", m.apiDownloads, core.RequireSession)
	r.API("POST /api/v1/reader/downloads", m.apiStartDownload, core.RequireSession)
	r.API("PUT /api/v1/reader/downloads/{id}", m.apiFinishDownload, core.RequireSession)
	r.API("DELETE /api/v1/reader/downloads/{id}", m.apiDeleteDownload, core.RequireSession)
	r.API("DELETE /api/v1/reader/downloads", m.apiClearDownloads, core.RequireSession)
}

// viewer is the publish viewer of the request (anonymous when signed out).
func viewer(r *http.Request) publish.Viewer {
	u := core.UserFrom(r.Context())
	if u == nil {
		return publish.Viewer{}
	}
	return publish.Viewer{UserID: u.ID, Admin: u.IsAdmin()}
}

// follows returns social.Follows when the social module provides it.
func (m *Module) follows() (social.Follows, bool) { return core.Use[social.Follows](m.d) }

// recorder returns analytics.Recorder when the analytics module provides it.
func (m *Module) recorder() (analytics.Recorder, bool) { return core.Use[analytics.Recorder](m.d) }

// recordAsync records one event without delaying the response. When too many
// recordings are in flight the event is dropped: analytics never slows reading.
func (m *Module) recordAsync(v publish.Viewer, sessionID string, e analytics.ReadEvent) {
	rec, ok := m.recorder()
	if !ok {
		return
	}
	select {
	case m.recSlots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() {
			<-m.recSlots
			_ = recover() // a recorder bug must never take the server down
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = rec.Record(ctx, v, sessionID, e)
	}()
}

// notFound maps publish.ErrNotFound (and our own missing rows) to a 404.
func notFound(err error) error {
	if errors.Is(err, publish.ErrNotFound) || errors.Is(err, errNoRow) {
		return core.ErrNotFound()
	}
	return err
}

// gate implements agegate.Gate over age_confirmations.
type gate struct{ m *Module }

func (g gate) Required(ctx context.Context, v publish.Viewer, s publish.Series) (bool, error) {
	if v.UserID == "" || !agegate.Applies(s.AgeRating) {
		return false, nil
	}
	ok, err := g.m.store.ageConfirmed(ctx, v.UserID, s.ID)
	return !ok, err
}
