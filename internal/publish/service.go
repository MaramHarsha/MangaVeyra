package publish

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// List and URL bounds.
const (
	DefaultListLimit = 24
	MaxListLimit     = 100
	// MaxFilterIDs caps SeriesFilter.IDs (the library's followed series arrive in pages).
	MaxFilterIDs = 500
	// DefaultPageURLTTL and MaxPageURLTTL bound PageImageURL lifetimes.
	DefaultPageURLTTL = 10 * time.Minute
	MaxPageURLTTL     = time.Hour
)

// service implements Service over series, chapter_publications and
// publication_pages. Every read applies the access rules in access.go.
type service struct {
	db       *db.DB
	settings *settings.Service
	studio   studio.Service
	assets   *assets.Service
	now      func() time.Time
}

var _ Service = (*service)(nil)

func (s *service) SeriesBySlug(ctx context.Context, v Viewer, slug string) (Series, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" || len(slug) > 200 {
		return Series{}, ErrNotFound
	}
	return s.seriesWhere(ctx, v, "s.slug = ?", slug)
}

func (s *service) SeriesByID(ctx context.Context, v Viewer, id string) (Series, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 64 {
		return Series{}, ErrNotFound
	}
	return s.seriesWhere(ctx, v, "s.id = ?", id)
}

func (s *service) seriesWhere(ctx context.Context, v Viewer, cond string, arg any) (Series, error) {
	a, err := s.resolve(ctx, v)
	if err != nil {
		return Series{}, err
	}
	return getSeries(ctx, s.db, a, cond, arg)
}

func getSeries(ctx context.Context, q db.Querier, a access, cond string, arg any) (Series, error) {
	where := and(clause{sql: cond, args: []any{arg}}, a.seriesReadable())
	var row seriesRow
	if err := q.Get(ctx, &row, `SELECT `+seriesCols+` FROM series s WHERE `+where.sql, where.args...); err != nil {
		if db.IsNotFound(err) {
			return Series{}, ErrNotFound
		}
		return Series{}, fmt.Errorf("publish: get series: %w", err)
	}
	return row.series(), nil
}

func (s *service) ListSeries(ctx context.Context, v Viewer, f SeriesFilter) ([]Series, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	parts := []clause{}
	if f.Cursor != "" {
		c, err := decodeSeriesCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		parts = append(parts, clause{sql: "(s.published_at < ? OR (s.published_at = ? AND s.id < ?))",
			args: []any{c.PublishedAt, c.PublishedAt, c.ID}})
	}
	if owner := strings.TrimSpace(f.OwnerID); owner != "" {
		parts = append(parts, clause{sql: "s.owner_id = ?", args: []any{owner}})
	}
	if f.IDs != nil {
		ids := uniqueTrimmed(f.IDs)
		if len(ids) > MaxFilterIDs {
			return nil, "", fmt.Errorf("%w: at most %d series ids", ErrInvalid, MaxFilterIDs)
		}
		if len(ids) == 0 {
			return []Series{}, "", nil
		}
		parts = append(parts, inStrings("s.id", ids))
	}
	if genre := strings.ToLower(strings.TrimSpace(f.Genre)); genre != "" {
		parts = append(parts, genreClause(genre))
	}

	a, err := s.resolve(ctx, v)
	if err != nil {
		return nil, "", err
	}
	parts = append(parts, a.seriesListed(f.Listed))
	where := and(parts...)
	var rows []seriesRow
	if err := s.db.Select(ctx, &rows, `SELECT `+seriesCols+` FROM series s WHERE `+where.sql+
		` ORDER BY s.published_at DESC, s.id DESC LIMIT ?`, append(where.args, limit+1)...); err != nil {
		return nil, "", fmt.Errorf("publish: list series: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = encodeSeriesCursor(last.PublishedAt.Time, last.ID)
	}
	out := make([]Series, len(rows))
	for i, r := range rows {
		out[i] = r.series()
	}
	return out, next, nil
}

// genreClause matches one element of the genres JSON array, case-insensitively
// (genres are lowercase slugs) and portably on both dialects.
func genreClause(genre string) clause {
	quoted := db.MustJSON(genre)
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(quoted)
	return clause{sql: `LOWER(s.genres) LIKE ? ESCAPE '\'`, args: []any{"%" + esc + "%"}}
}

func uniqueTrimmed(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func (s *service) Publications(ctx context.Context, v Viewer, seriesID, lang string) ([]Publication, error) {
	seriesID, lang = strings.TrimSpace(seriesID), strings.TrimSpace(lang)
	if seriesID == "" || len(seriesID) > 64 || len(lang) > 35 {
		return nil, ErrNotFound
	}
	a, err := s.resolve(ctx, v)
	if err != nil {
		return nil, err
	}
	var out []Publication
	err = s.db.WithReadTx(ctx, func(tx *db.Tx) error {
		series, err := getSeries(ctx, tx, a, "s.id = ?", seriesID)
		if err != nil {
			return err
		}
		if lang == "" {
			lang = series.OriginalLang
		}
		where := and(clause{sql: "p.series_id = ? AND p.lang = ?", args: []any{series.ID, lang}},
			a.seriesReadable(), a.publicationReadable())
		var rows []publicationRow
		if err := tx.Select(ctx, &rows, `SELECT `+publicationCols+` FROM chapter_publications p
			JOIN series s ON s.id = p.series_id WHERE `+where.sql+` ORDER BY p.number, p.published_at, p.id`, where.args...); err != nil {
			return fmt.Errorf("publish: list publications: %w", err)
		}
		out = make([]Publication, len(rows))
		for i, r := range rows {
			out[i] = r.publication()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *service) Publication(ctx context.Context, v Viewer, seriesID string, number float64, lang string) (Publication, []PublicationPage, error) {
	seriesID, lang = strings.TrimSpace(seriesID), strings.TrimSpace(lang)
	if seriesID == "" || len(seriesID) > 64 || len(lang) > 35 || math.IsNaN(number) || math.IsInf(number, 0) {
		return Publication{}, nil, ErrNotFound
	}
	a, err := s.resolve(ctx, v)
	if err != nil {
		return Publication{}, nil, err
	}
	var pub Publication
	var pages []PublicationPage
	err = s.db.WithReadTx(ctx, func(tx *db.Tx) error {
		series, err := getSeries(ctx, tx, a, "s.id = ?", seriesID)
		if err != nil {
			return err
		}
		if lang == "" {
			lang = series.OriginalLang
		}
		where := and(clause{sql: "p.series_id = ? AND p.lang = ? AND p.number = ?", args: []any{series.ID, lang, number}},
			a.seriesReadable(), a.publicationReadable())
		// Two chapters may share a number; readers get the most recently published one.
		var row publicationRow
		if err := tx.Get(ctx, &row, `SELECT `+publicationCols+` FROM chapter_publications p
			JOIN series s ON s.id = p.series_id WHERE `+where.sql+` ORDER BY p.published_at DESC, p.id DESC LIMIT 1`, where.args...); err != nil {
			if db.IsNotFound(err) {
				return ErrNotFound
			}
			return fmt.Errorf("publish: get publication: %w", err)
		}
		pub = row.publication()
		var rows []pageRow
		if err := tx.Select(ctx, &rows, `SELECT `+pageCols+` FROM publication_pages pg
			WHERE pg.publication_id = ? ORDER BY pg.number, pg.id`, pub.ID); err != nil {
			return fmt.Errorf("publish: list publication pages: %w", err)
		}
		pages = make([]PublicationPage, len(rows))
		for i, r := range rows {
			pages[i] = r.page()
		}
		return nil
	})
	if err != nil {
		return Publication{}, nil, err
	}
	return pub, pages, nil
}

func (s *service) PageImageURL(ctx context.Context, v Viewer, page PublicationPage, ttl time.Duration) (string, error) {
	pageID := strings.TrimSpace(page.ID)
	if pageID == "" || len(pageID) > 64 {
		return "", ErrNotFound
	}
	a, err := s.resolve(ctx, v)
	if err != nil {
		return "", err
	}
	// The page, its publication and its series are re-read and re-checked here:
	// the caller's copy of the page (asset ID included) is never trusted.
	where := and(clause{sql: "pg.id = ?", args: []any{pageID}}, a.seriesReadable(), a.publicationReadable())
	var row pageRow
	if err := s.db.Get(ctx, &row, `SELECT `+pageCols+` FROM publication_pages pg
		JOIN chapter_publications p ON p.id = pg.publication_id
		JOIN series s ON s.id = p.series_id WHERE `+where.sql, where.args...); err != nil {
		if db.IsNotFound(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("publish: get page: %w", err)
	}
	if !row.AssetID.Valid || row.AssetID.String == "" ||
		(page.PublicationID != "" && page.PublicationID != row.PublicationID) ||
		(page.AssetID != "" && page.AssetID != row.AssetID.String) {
		return "", ErrNotFound
	}
	asset, err := s.assets.Get(ctx, row.AssetID.String)
	if err != nil {
		if errors.Is(err, assets.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("publish: page asset: %w", err)
	}
	if ttl <= 0 {
		ttl = DefaultPageURLTTL
	}
	if ttl > MaxPageURLTTL {
		ttl = MaxPageURLTTL
	}
	return s.assets.URL(asset, ttl), nil
}

func (s *service) CanRead(ctx context.Context, v Viewer, series Series, p Publication) bool {
	seriesID, pubID := strings.TrimSpace(series.ID), strings.TrimSpace(p.ID)
	if seriesID == "" || pubID == "" || len(seriesID) > 64 || len(pubID) > 64 ||
		(p.SeriesID != "" && p.SeriesID != seriesID) {
		return false
	}
	a, err := s.resolve(ctx, v)
	if err != nil {
		return false
	}
	// The stored rows decide, never the caller's copies.
	where := and(clause{sql: "p.id = ? AND s.id = ?", args: []any{pubID, seriesID}}, a.seriesReadable(), a.publicationReadable())
	var n int
	if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM chapter_publications p
		JOIN series s ON s.id = p.series_id WHERE `+where.sql, where.args...); err != nil {
		return false
	}
	return n == 1
}
