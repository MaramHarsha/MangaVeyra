package discover

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// Catalogue bounds: Explore and browse consider the newest listed series.
const (
	maxListed  = 300
	coverTTL   = time.Hour
	newChapter = 7 * 24 * time.Hour
)

// AI label roles; a series is hand-drawn when every role is human (ADR 0012 §4).
var aiRoles = []string{"script", "art", "lettering"}

// HandDrawn reports whether every AI label role of s is human. A missing
// role is not hand-drawn: provenance is never assumed.
func HandDrawn(s publish.Series) bool {
	for _, r := range aiRoles {
		if s.AILabel[r] != "human" {
			return false
		}
	}
	return true
}

// listed pages through publish.Service.ListSeries (Listed: only public and
// members series the viewer may read; never unlisted, private, unpublished
// or, for anonymous visitors, mature) up to maxListed series.
func (m *Module) listed(ctx context.Context, v publish.Viewer, f publish.SeriesFilter) ([]publish.Series, error) {
	f.Listed, f.Limit = true, publish.MaxListLimit
	var out []publish.Series
	for len(out) < maxListed {
		list, next, err := m.publish.ListSeries(ctx, v, f)
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
		if next == "" {
			break
		}
		f.Cursor = next
	}
	if len(out) > maxListed {
		out = out[:maxListed]
	}
	return out, nil
}

// byIDs returns the listed readable series among ids, in the order of ids.
func (m *Module) byIDs(ctx context.Context, v publish.Viewer, ids []string, genre string) ([]publish.Series, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list, err := m.listed(ctx, v, publish.SeriesFilter{IDs: ids, Genre: genre})
	if err != nil {
		return nil, err
	}
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		pos[id] = i
	}
	out := make([]publish.Series, len(ids))
	found := make([]bool, len(ids))
	for _, s := range list {
		if i, ok := pos[s.ID]; ok {
			out[i], found[i] = s, true
		}
	}
	ordered := out[:0]
	for i := range out {
		if found[i] {
			ordered = append(ordered, out[i])
		}
	}
	return ordered, nil
}

// latestPublic returns the newest published_at of each series' public
// chapters (read-only over the publish module's chapter_publications). It is
// used only to order and badge series ListSeries already returned for the
// viewer; public chapters of a readable series are readable by everyone who
// can read the series, so the badge reveals nothing about restricted chapters.
func (m *Module) latestPublic(ctx context.Context, series []publish.Series) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(series) == 0 {
		return out, nil
	}
	ids := make([]any, len(series))
	for i, s := range series {
		ids[i] = s.ID
	}
	var rows []struct {
		ID string    `db:"series_id"`
		At time.Time `db:"published_at"`
	}
	in := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	if err := m.d.DB.Select(ctx, &rows, `SELECT p.series_id, p.published_at FROM chapter_publications p
		WHERE p.series_id IN (`+in+`) AND p.status = 'published' AND p.visibility = 'public'
		AND p.published_at IS NOT NULL AND p.published_at <= ? ORDER BY p.published_at DESC LIMIT 3000`,
		append(ids, m.now())...); err != nil {
		return nil, fmt.Errorf("discover: latest chapters: %w", err)
	}
	for _, r := range rows {
		if _, ok := out[r.ID]; !ok {
			out[r.ID] = r.At.UTC()
		}
	}
	return out, nil
}

// latestChapter is the newest readable chapter of s for the viewer.
func (m *Module) latestChapter(ctx context.Context, v publish.Viewer, s publish.Series) (publish.Publication, bool, error) {
	pubs, err := m.publish.Publications(ctx, v, s.ID, "")
	if errors.Is(err, publish.ErrNotFound) {
		return publish.Publication{}, false, nil
	}
	if err != nil || len(pubs) == 0 {
		return publish.Publication{}, false, err
	}
	return pubs[len(pubs)-1], true, nil
}

type creator struct {
	ID       string `db:"id"`
	Username string `db:"username"`
	Name     string `db:"display_name"`
}

// creators loads owner names for the given user IDs.
func (m *Module) creators(ctx context.Context, ids []string) (map[string]creator, error) {
	out := map[string]creator{}
	ids = dedupe(ids, maxListed)
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	var rows []creator
	in := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	if err := m.d.DB.Select(ctx, &rows, `SELECT id, username, display_name FROM users WHERE id IN (`+in+`)`, args...); err != nil {
		return nil, fmt.Errorf("discover: creators: %w", err)
	}
	for _, c := range rows {
		if strings.TrimSpace(c.Name) == "" {
			c.Name = c.Username
		}
		out[c.ID] = c
	}
	return out, nil
}

// coverURL signs the cover of a series the viewer may read ("" without one).
func (m *Module) coverURL(ctx context.Context, s publish.Series) string {
	if s.CoverAssetID == "" {
		return ""
	}
	return m.assets.URLFor(ctx, s.CoverAssetID, coverTTL)
}

// cardView is a series card (shelves, rankings and search rows).
type cardView struct {
	ID, Title, Href, CoverURL, Meta, Badge string
	Rank                                   int
}

func seriesHref(s publish.Series) string { return "/s/" + url.PathEscape(s.Slug) }

func chapterHref(s publish.Series, p publish.Publication) string {
	return seriesHref(s) + "/" + strconv.FormatFloat(p.Number, 'f', -1, 64)
}

// genreLabel names a genre slug in the interface language (the onboarding
// genre catalogue), or title-cases an unknown slug.
func genreLabel(ctx context.Context, slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return ""
	}
	// The onboarding genre catalogue, then genres only discover names.
	for _, key := range []string{"genre." + slug, "discover.genre." + slug} {
		if v := i18n.T(ctx, key); v != key {
			return v
		}
	}
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(slug))
	for i, w := range words {
		r := []rune(w)
		words[i] = strings.ToUpper(string(r[:1])) + string(r[1:])
	}
	return strings.Join(words, " ")
}

func firstGenre(ctx context.Context, s publish.Series) string {
	if len(s.Genres) == 0 {
		return ""
	}
	return genreLabel(ctx, s.Genres[0])
}

func (m *Module) card(ctx context.Context, s publish.Series, meta string) cardView {
	return cardView{ID: s.ID, Title: s.Title, Href: seriesHref(s), CoverURL: m.coverURL(ctx, s), Meta: meta}
}
