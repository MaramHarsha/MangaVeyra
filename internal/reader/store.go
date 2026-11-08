package reader

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// store reads and writes the reader's own tables: read_progress,
// reader_prefs, bookmarks, downloads and age_confirmations. Every query is
// scoped to one user; access to the series and publications those rows point
// at is always decided by publish.Service before anything is written or shown.
type store struct {
	db  *db.DB
	now func() time.Time
}

// errNoRow marks a missing row the caller may treat as "not yet stored".
var errNoRow = errors.New("reader: no row")

// Progress is one row of read_progress.
type Progress struct {
	UserID        string    `db:"user_id" json:"-"`
	PublicationID string    `db:"publication_id" json:"publication_id"`
	SeriesID      string    `db:"series_id" json:"series_id"`
	Page          int       `db:"page" json:"page"`
	Percent       float64   `db:"percent" json:"percent"`
	Completed     bool      `db:"completed" json:"completed"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
}

// SeriesPrefs is one row of reader_prefs (the per-series reading settings a
// member chose; settings without a column stay on the device, see prefs.go).
type SeriesPrefs struct {
	Mode       string  `db:"mode" json:"mode"`
	Direction  string  `db:"direction" json:"direction"`
	Background string  `db:"background" json:"background"`
	Zoom       float64 `db:"zoom" json:"zoom"`
}

// Bookmark is one row of bookmarks. Panel is the frozen panel's reading order
// on the page ("" for a page bookmark), because frozen panels carry no IDs.
type Bookmark struct {
	ID            string    `db:"id" json:"id"`
	UserID        string    `db:"user_id" json:"-"`
	SeriesID      string    `db:"series_id" json:"series_id"`
	PublicationID string    `db:"publication_id" json:"publication_id"`
	Page          int       `db:"page" json:"page"`
	Panel         string    `db:"panel_id" json:"panel"`
	Note          string    `db:"note" json:"note"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

// PanelOrder returns the bookmarked panel's reading order (0 for a page bookmark).
func (b Bookmark) PanelOrder() int {
	n, err := strconv.Atoi(b.Panel)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Download statuses (downloads.status).
const (
	DownloadPending = "pending"
	DownloadReady   = "ready"
	DownloadExpired = "expired"
)

// Download is one row of downloads, with the publication's series resolved
// through the foreign key (the downloads table has no series column).
type Download struct {
	ID            string       `db:"id" json:"id"`
	UserID        string       `db:"user_id" json:"-"`
	PublicationID string       `db:"publication_id" json:"publication_id"`
	SeriesID      string       `db:"series_id" json:"series_id"`
	Bytes         int64        `db:"bytes" json:"bytes"`
	Status        string       `db:"status" json:"status"`
	CreatedAt     time.Time    `db:"created_at" json:"created_at"`
	ExpiresAt     sql.NullTime `db:"expires_at" json:"-"`
}

// Expired reports whether the download is past its expiry or marked expired.
func (d Download) Expired(now time.Time) bool {
	return d.Status == DownloadExpired || (d.ExpiresAt.Valid && !d.ExpiresAt.Time.After(now))
}

// ---- progress -----------------------------------------------------------------

func (s *store) progress(ctx context.Context, userID, publicationID string) (Progress, error) {
	var p Progress
	err := s.db.Get(ctx, &p, `SELECT user_id, publication_id, series_id, page, percent, completed, updated_at
		FROM read_progress WHERE user_id = ? AND publication_id = ?`, userID, publicationID)
	if db.IsNotFound(err) {
		return Progress{}, errNoRow
	}
	return p, err
}

// saveProgress upserts the reader's position. Completion is sticky: rereading
// an early page of a finished chapter keeps it completed.
func (s *store) saveProgress(ctx context.Context, p Progress) (Progress, error) {
	p.UpdatedAt = s.now()
	_, err := s.db.Exec(ctx, `INSERT INTO read_progress (user_id, publication_id, series_id, page, percent, completed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, publication_id) DO UPDATE SET page = excluded.page, percent = excluded.percent,
			completed = CASE WHEN excluded.completed THEN excluded.completed ELSE read_progress.completed END,
			updated_at = excluded.updated_at`,
		p.UserID, p.PublicationID, p.SeriesID, p.Page, p.Percent, p.Completed, p.UpdatedAt)
	if err != nil {
		return Progress{}, fmt.Errorf("reader: save progress: %w", err)
	}
	return s.progress(ctx, p.UserID, p.PublicationID)
}

// history lists the user's progress rows, most recently read first. before
// is the exclusive cursor (updated_at, publication id); zero starts at the top.
func (s *store) history(ctx context.Context, userID string, before time.Time, beforeID string, limit int) ([]Progress, error) {
	q := `SELECT user_id, publication_id, series_id, page, percent, completed, updated_at FROM read_progress WHERE user_id = ?`
	args := []any{userID}
	if !before.IsZero() {
		q += ` AND (updated_at < ? OR (updated_at = ? AND publication_id < ?))`
		args = append(args, before, before, beforeID)
	}
	q += ` ORDER BY updated_at DESC, publication_id DESC LIMIT ?`
	var out []Progress
	if err := s.db.Select(ctx, &out, q, append(args, limit)...); err != nil {
		return nil, fmt.Errorf("reader: history: %w", err)
	}
	return out, nil
}

// latestPerSeries returns the most recent progress row of each listed series.
func (s *store) latestPerSeries(ctx context.Context, userID string, seriesIDs []string) (map[string]Progress, error) {
	out := map[string]Progress{}
	if len(seriesIDs) == 0 {
		return out, nil
	}
	q, args, err := db.In(`SELECT user_id, publication_id, series_id, page, percent, completed, updated_at FROM read_progress
		WHERE user_id = ? AND series_id IN (?) ORDER BY updated_at DESC`, userID, seriesIDs)
	if err != nil {
		return nil, err
	}
	var rows []Progress
	if err := s.db.Select(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("reader: latest progress: %w", err)
	}
	for _, r := range rows {
		if _, ok := out[r.SeriesID]; !ok {
			out[r.SeriesID] = r
		}
	}
	return out, nil
}

// completedIn returns the publication IDs of seriesID the user finished.
func (s *store) completedIn(ctx context.Context, userID, seriesID string) (map[string]bool, error) {
	var ids []string
	if err := s.db.Select(ctx, &ids, `SELECT publication_id FROM read_progress WHERE user_id = ? AND series_id = ? AND completed = ?`,
		userID, seriesID, true); err != nil {
		return nil, fmt.Errorf("reader: completed chapters: %w", err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (s *store) countHistory(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM read_progress WHERE user_id = ?`, userID)
	return n, err
}

// ---- per-series preferences ---------------------------------------------------

func (s *store) seriesPrefs(ctx context.Context, userID, seriesID string) (SeriesPrefs, error) {
	var p SeriesPrefs
	err := s.db.Get(ctx, &p, `SELECT mode, direction, background, zoom FROM reader_prefs WHERE user_id = ? AND series_id = ?`,
		userID, seriesID)
	if db.IsNotFound(err) {
		return SeriesPrefs{}, errNoRow
	}
	return p, err
}

func (s *store) saveSeriesPrefs(ctx context.Context, userID, seriesID string, p SeriesPrefs) error {
	_, err := s.db.Exec(ctx, `INSERT INTO reader_prefs (user_id, series_id, mode, direction, background, zoom, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, series_id) DO UPDATE SET mode = excluded.mode, direction = excluded.direction,
			background = excluded.background, zoom = excluded.zoom, updated_at = excluded.updated_at`,
		userID, seriesID, p.Mode, p.Direction, p.Background, p.Zoom, s.now())
	if err != nil {
		return fmt.Errorf("reader: save prefs: %w", err)
	}
	return nil
}

// ---- bookmarks ----------------------------------------------------------------

const bookmarkCols = `id, user_id, series_id, publication_id, page, panel_id, note, created_at`

func (s *store) bookmarksFor(ctx context.Context, userID, publicationID string) ([]Bookmark, error) {
	out := []Bookmark{}
	if err := s.db.Select(ctx, &out, `SELECT `+bookmarkCols+` FROM bookmarks WHERE user_id = ? AND publication_id = ?
		ORDER BY page, panel_id, created_at`, userID, publicationID); err != nil {
		return nil, fmt.Errorf("reader: bookmarks: %w", err)
	}
	return out, nil
}

// bookmarks lists the user's bookmarks newest first with an exclusive cursor.
func (s *store) bookmarks(ctx context.Context, userID string, before time.Time, beforeID string, limit int) ([]Bookmark, error) {
	q := `SELECT ` + bookmarkCols + ` FROM bookmarks WHERE user_id = ?`
	args := []any{userID}
	if !before.IsZero() {
		q += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, before, before, beforeID)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	out := []Bookmark{}
	if err := s.db.Select(ctx, &out, q, append(args, limit)...); err != nil {
		return nil, fmt.Errorf("reader: bookmarks: %w", err)
	}
	return out, nil
}

func (s *store) countBookmarks(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM bookmarks WHERE user_id = ?`, userID)
	return n, err
}

// addBookmark stores a bookmark unless the same page/panel is already
// bookmarked, in which case the existing one is returned (created=false).
func (s *store) addBookmark(ctx context.Context, b Bookmark) (Bookmark, bool, error) {
	var out Bookmark
	created := false
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		err := tx.Get(ctx, &out, `SELECT `+bookmarkCols+` FROM bookmarks WHERE user_id = ? AND publication_id = ? AND page = ? AND panel_id = ?
			ORDER BY created_at LIMIT 1`, b.UserID, b.PublicationID, b.Page, b.Panel)
		if err == nil {
			return nil
		}
		if !db.IsNotFound(err) {
			return err
		}
		b.ID, b.CreatedAt = db.NewID(), s.now()
		if _, err := tx.Exec(ctx, `INSERT INTO bookmarks (`+bookmarkCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			b.ID, b.UserID, b.SeriesID, b.PublicationID, b.Page, b.Panel, b.Note, b.CreatedAt); err != nil {
			return err
		}
		out, created = b, true
		return nil
	})
	if err != nil {
		return Bookmark{}, false, fmt.Errorf("reader: add bookmark: %w", err)
	}
	return out, created, nil
}

func (s *store) bookmark(ctx context.Context, userID, id string) (Bookmark, error) {
	var b Bookmark
	err := s.db.Get(ctx, &b, `SELECT `+bookmarkCols+` FROM bookmarks WHERE id = ? AND user_id = ?`, id, userID)
	if db.IsNotFound(err) {
		return Bookmark{}, errNoRow
	}
	return b, err
}

// deleteBookmark removes the user's bookmark; another user's ID is errNoRow.
func (s *store) deleteBookmark(ctx context.Context, userID, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM bookmarks WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("reader: delete bookmark: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNoRow
	}
	return nil
}

// publicationKey returns the series, number and language of a publication:
// the key publish.Service.Publication reads it by, for a chapter the member
// opened by its link that Publications does not list (unlisted). Like
// downloadCols it reads the row by foreign key only; whether the viewer may
// read it is still decided by publish.Service.
func (s *store) publicationKey(ctx context.Context, id string) (seriesID string, number float64, lang string, err error) {
	var row struct {
		SeriesID string  `db:"series_id"`
		Number   float64 `db:"number"`
		Lang     string  `db:"lang"`
	}
	err = s.db.Get(ctx, &row, `SELECT series_id, number, lang FROM chapter_publications WHERE id = ?`, id)
	if db.IsNotFound(err) {
		return "", 0, "", errNoRow
	}
	return row.SeriesID, row.Number, row.Lang, err
}

// ---- downloads ----------------------------------------------------------------

// downloadCols joins the publication's series ID by foreign key only; access
// to that series is still decided by publish.Service.
const downloadCols = `d.id, d.user_id, d.publication_id, p.series_id, d.bytes, d.status, d.created_at, d.expires_at`
const downloadFrom = ` FROM downloads d JOIN chapter_publications p ON p.id = d.publication_id`

func (s *store) download(ctx context.Context, userID, id string) (Download, error) {
	var d Download
	err := s.db.Get(ctx, &d, `SELECT `+downloadCols+downloadFrom+` WHERE d.id = ? AND d.user_id = ?`, id, userID)
	if db.IsNotFound(err) {
		return Download{}, errNoRow
	}
	return d, err
}

func (s *store) downloadFor(ctx context.Context, userID, publicationID string) (Download, error) {
	var d Download
	err := s.db.Get(ctx, &d, `SELECT `+downloadCols+downloadFrom+` WHERE d.user_id = ? AND d.publication_id = ?`, userID, publicationID)
	if db.IsNotFound(err) {
		return Download{}, errNoRow
	}
	return d, err
}

func (s *store) downloads(ctx context.Context, userID string, limit int) ([]Download, error) {
	out := []Download{}
	if err := s.db.Select(ctx, &out, `SELECT `+downloadCols+downloadFrom+` WHERE d.user_id = ? ORDER BY d.created_at DESC, d.id DESC LIMIT ?`,
		userID, limit); err != nil {
		return nil, fmt.Errorf("reader: downloads: %w", err)
	}
	return out, nil
}

func (s *store) countDownloads(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM downloads WHERE user_id = ? AND status <> ? AND (expires_at IS NULL OR expires_at > ?)`,
		userID, DownloadExpired, s.now())
	return n, err
}

// startDownload records (or restarts) the user's download of a publication
// as pending with a fresh expiry.
func (s *store) startDownload(ctx context.Context, userID, publicationID string, ttl time.Duration) (Download, error) {
	now := s.now()
	_, err := s.db.Exec(ctx, `INSERT INTO downloads (id, user_id, publication_id, bytes, status, created_at, expires_at)
		VALUES (?, ?, ?, 0, ?, ?, ?)
		ON CONFLICT (user_id, publication_id) DO UPDATE SET status = excluded.status, bytes = 0,
			created_at = excluded.created_at, expires_at = excluded.expires_at`,
		db.NewID(), userID, publicationID, DownloadPending, now, now.Add(ttl))
	if err != nil {
		return Download{}, fmt.Errorf("reader: start download: %w", err)
	}
	return s.downloadFor(ctx, userID, publicationID)
}

// finishDownload marks the user's download ready with its cached size.
func (s *store) finishDownload(ctx context.Context, userID, id string, bytes int64) (Download, error) {
	res, err := s.db.Exec(ctx, `UPDATE downloads SET status = ?, bytes = ? WHERE id = ? AND user_id = ? AND status <> ?`,
		DownloadReady, bytes, id, userID, DownloadExpired)
	if err != nil {
		return Download{}, fmt.Errorf("reader: finish download: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Download{}, errNoRow
	}
	return s.download(ctx, userID, id)
}

// expireDownload marks a download expired (access lost or past its expiry).
func (s *store) expireDownload(ctx context.Context, userID, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE downloads SET status = ? WHERE id = ? AND user_id = ?`, DownloadExpired, id, userID)
	return err
}

func (s *store) deleteDownload(ctx context.Context, userID, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM downloads WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("reader: delete download: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNoRow
	}
	return nil
}

// clearDownloads removes every download row of the user ("Clear offline downloads").
func (s *store) clearDownloads(ctx context.Context, userID string) (int64, error) {
	res, err := s.db.Exec(ctx, `DELETE FROM downloads WHERE user_id = ?`, userID)
	if err != nil {
		return 0, fmt.Errorf("reader: clear downloads: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---- age confirmations ----------------------------------------------------------

func (s *store) ageConfirmed(ctx context.Context, userID, seriesID string) (bool, error) {
	var n int
	if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM age_confirmations WHERE user_id = ? AND series_id = ?`, userID, seriesID); err != nil {
		return false, fmt.Errorf("reader: age confirmation: %w", err)
	}
	return n > 0, nil
}

func (s *store) confirmAge(ctx context.Context, userID, seriesID string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO age_confirmations (user_id, series_id, confirmed_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id, series_id) DO NOTHING`, userID, seriesID, s.now())
	if err != nil {
		return fmt.Errorf("reader: confirm age: %w", err)
	}
	return nil
}

// cursor encodes a (time, id) pair for library paging.
func encodeCursor(t time.Time, id string) string {
	return strconv.FormatInt(t.UTC().UnixMicro(), 36) + "." + id
}

func decodeCursor(c string) (time.Time, string, bool) {
	ts, id, ok := strings.Cut(c, ".")
	if !ok || !db.ValidID(id) {
		return time.Time{}, "", false
	}
	n, err := strconv.ParseInt(ts, 36, 64)
	if err != nil || n <= 0 {
		return time.Time{}, "", false
	}
	return time.UnixMicro(n).UTC(), id, true
}
