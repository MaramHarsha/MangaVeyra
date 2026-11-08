package publish

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Licences a series may carry: all rights reserved or a Creative Commons variant.
var Licences = []string{"all_rights", "cc-by", "cc-by-sa", "cc-by-nc", "cc-by-nc-sa", "cc0"}

// Formats, ratings, visibilities and statuses in display order.
var (
	Formats      = []string{"manga_rtl", "comic_ltr", "webtoon"}
	Ratings      = []string{RatingAll, RatingTeen, RatingMature, RatingAdult}
	Visibilities = []string{VisibilityPublic, VisibilityMembers, VisibilityUnlisted, VisibilityPrivate}
	Statuses     = []string{SeriesDraft, SeriesOngoing, SeriesCompleted, SeriesHiatus}
)

// Limits for series fields.
const (
	maxTitle        = 120
	maxSynopsis     = 2000
	maxGenres       = 8
	maxTags         = 16
	maxTagLen       = 40
	maxSupportLinks = 5
	maxLinkLabel    = 40
	maxLinkURL      = 500
	maxReleaseNote  = 60
)

var slugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// seriesInput is the editable series metadata (series settings and E1).
type seriesInput struct {
	Slug, Title, Synopsis                          string
	Genres, Tags                                   []string
	Format, AgeRating, Licence, Visibility, Status string
	OriginalLang                                   string
	SupportLinks                                   []SupportLink
	// Schedule is the optional release note ("Weekly, Saturday").
	Schedule string
	// OwnerCredit is the owner's optional credit name; Credits are the other
	// credited project members, in credit order.
	OwnerCredit string
	Credits     []Creator
}

// fieldErrors maps a form field to the catalogue key of its error message.
type fieldErrors map[string]string

func (in *seriesInput) normalize() {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Title = strings.TrimSpace(in.Title)
	in.Synopsis = strings.TrimSpace(in.Synopsis)
	in.Genres = normalizeTags(in.Genres, true)
	in.Tags = normalizeTags(in.Tags, false)
	in.Format, in.AgeRating = strings.TrimSpace(in.Format), strings.TrimSpace(in.AgeRating)
	in.Licence, in.Visibility, in.Status = strings.TrimSpace(in.Licence), strings.TrimSpace(in.Visibility), strings.TrimSpace(in.Status)
	if c := pagedoc.NormalizeContentLanguage(strings.TrimSpace(in.OriginalLang)); c != "" {
		in.OriginalLang = c
	}
	links := make([]SupportLink, 0, len(in.SupportLinks))
	for _, l := range in.SupportLinks {
		l.Label, l.URL = strings.TrimSpace(l.Label), strings.TrimSpace(l.URL)
		if l.Label == "" && l.URL == "" {
			continue
		}
		links = append(links, l)
	}
	in.SupportLinks = links
	in.Schedule = strings.Join(strings.Fields(in.Schedule), " ")
	in.OwnerCredit = strings.Join(strings.Fields(in.OwnerCredit), " ")
	in.Credits = normalizeCredits(in.Credits)
}

func (in seriesInput) validate() fieldErrors {
	errs := fieldErrors{}
	switch {
	case !slugRE.MatchString(in.Slug) || len(in.Slug) < 3 || len(in.Slug) > 60:
		errs["slug"] = "publish.error.slug"
	}
	if in.Title == "" || utf8.RuneCountInString(in.Title) > maxTitle || !utf8.ValidString(in.Title) {
		errs["title"] = "publish.error.title"
	}
	if utf8.RuneCountInString(in.Synopsis) > maxSynopsis || !utf8.ValidString(in.Synopsis) {
		errs["synopsis"] = "publish.error.synopsis"
	}
	if len(in.Genres) > maxGenres {
		errs["genres"] = "publish.error.genres"
	}
	if len(in.Tags) > maxTags {
		errs["tags"] = "publish.error.tags"
	}
	for _, t := range append(slices.Clone(in.Genres), in.Tags...) {
		if utf8.RuneCountInString(t) > maxTagLen {
			errs["tags"] = "publish.error.tags"
		}
	}
	if !slices.Contains(Formats, in.Format) {
		errs["format"] = "publish.error.choice"
	}
	if !slices.Contains(Ratings, in.AgeRating) {
		errs["age_rating"] = "publish.error.choice"
	}
	if !slices.Contains(Licences, in.Licence) {
		errs["licence"] = "publish.error.choice"
	}
	if !slices.Contains(Visibilities, in.Visibility) {
		errs["visibility"] = "publish.error.choice"
	}
	if !slices.Contains(Statuses, in.Status) {
		errs["status"] = "publish.error.choice"
	}
	if !pagedoc.IsContentLanguage(in.OriginalLang) {
		errs["original_lang"] = "publish.error.choice"
	}
	if len(in.SupportLinks) > maxSupportLinks {
		errs["support_links"] = "publish.error.links"
	}
	if utf8.RuneCountInString(in.Schedule) > maxReleaseNote || !utf8.ValidString(in.Schedule) {
		errs["schedule"] = "publish.error.release_note"
	}
	if utf8.RuneCountInString(in.OwnerCredit) > maxCreditName || len(in.Credits) > maxCreators {
		errs["creators"] = "publish.error.creators"
	}
	for _, c := range in.Credits {
		if !validCredit(c) {
			errs["creators"] = "publish.error.creators"
		}
	}
	for _, l := range in.SupportLinks {
		if !validLink(l) {
			errs["support_links"] = "publish.error.links"
		}
	}
	return errs
}

// validLink accepts an absolute http(s) link with a short label and no
// embedded credentials. Links open outside the app; no payment UI is rendered.
func validLink(l SupportLink) bool {
	if l.Label == "" || utf8.RuneCountInString(l.Label) > maxLinkLabel || len(l.URL) > maxLinkURL {
		return false
	}
	u, err := url.Parse(l.URL)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// normalizeTags trims, lowercases genres, drops blanks and repeats.
func normalizeTags(in []string, lower bool) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range in {
		for _, v := range strings.Split(raw, ",") {
			v = strings.Join(strings.Fields(v), " ")
			if lower {
				v = strings.ToLower(v)
			}
			key := strings.ToLower(v)
			if v == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, v)
		}
	}
	return out
}

// seriesForProject returns the project's series for its creators, without
// reader rules (callers authorize first). ok is false when there is none yet.
func seriesForProject(ctx context.Context, q db.Querier, projectID string) (Series, bool, error) {
	var row seriesRow
	if err := q.Get(ctx, &row, `SELECT `+seriesCols+` FROM series s WHERE s.project_id = ?`+forUpdate(q), projectID); err != nil {
		if db.IsNotFound(err) {
			return Series{}, false, nil
		}
		return Series{}, false, fmt.Errorf("publish: project series: %w", err)
	}
	list := []Series{row.series()}
	if err := attachCreators(ctx, q, list); err != nil {
		return Series{}, false, err
	}
	return list[0], true, nil
}

// seriesDefaults derives a new series from its project: title, synopsis,
// genres, tags, format and language, with a slug that is free right now.
func seriesDefaults(ctx context.Context, q db.Querier, p studio.Project, rating string) (seriesInput, error) {
	synopsis := p.Logline
	if synopsis == "" {
		synopsis = p.Premise
	}
	if utf8.RuneCountInString(synopsis) > maxSynopsis {
		synopsis = string([]rune(synopsis)[:maxSynopsis])
	}
	in := seriesInput{Title: p.Title, Synopsis: synopsis, Genres: p.Genres, Tags: p.Tags, Format: p.Format,
		AgeRating: rating, Licence: "all_rights", Visibility: VisibilityPublic, Status: SeriesDraft, OriginalLang: p.Language}
	if !slices.Contains(Ratings, in.AgeRating) {
		in.AgeRating = RatingAll
	}
	if utf8.RuneCountInString(in.Title) > maxTitle {
		in.Title = string([]rune(in.Title)[:maxTitle])
	}
	in.normalize()
	if len(in.Genres) > maxGenres {
		in.Genres = in.Genres[:maxGenres]
	}
	if len(in.Tags) > maxTags {
		in.Tags = in.Tags[:maxTags]
	}
	slug, err := freeSlug(ctx, q, slugify(p.Title), "")
	if err != nil {
		return in, err
	}
	in.Slug = slug
	return in, nil
}

// freeSlug returns base, or base-2, base-3… when taken by another series.
func freeSlug(ctx context.Context, q db.Querier, base, seriesID string) (string, error) {
	if len(base) < 3 {
		base = strings.Trim(base+"-series", "-")
	}
	if len(base) > 54 {
		base = strings.Trim(base[:54], "-")
	}
	for i := 1; i < 1000; i++ {
		slug := base
		if i > 1 {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		var n int
		if err := q.Get(ctx, &n, `SELECT COUNT(*) FROM series WHERE slug = ? AND id <> ?`, slug, seriesID); err != nil {
			return "", fmt.Errorf("publish: check slug: %w", err)
		}
		if n == 0 {
			return slug, nil
		}
	}
	return "", fmt.Errorf("%w: no free slug", ErrConflict)
}

func slugify(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// errSlugTaken is returned when another series already uses the slug.
var errSlugTaken = fmt.Errorf("%w: slug taken", ErrConflict)

// saveSeriesTx creates or updates the project's series. expected, when not
// zero, is the updated_at the editor loaded (a stale form is ErrConflict).
func (s *service) saveSeriesTx(ctx context.Context, tx *db.Tx, actor studio.Actor, p studio.Project, in seriesInput, expected time.Time) (Series, error) {
	cur, exists, err := seriesForProject(ctx, tx, p.ID)
	if err != nil {
		return Series{}, err
	}
	if exists && !expected.IsZero() && !cur.UpdatedAt.Equal(expected.UTC()) {
		return Series{}, ErrConflict
	}
	var taken int
	if err := tx.Get(ctx, &taken, `SELECT COUNT(*) FROM series WHERE slug = ? AND project_id <> ?`, in.Slug, p.ID); err != nil {
		return Series{}, fmt.Errorf("publish: check slug: %w", err)
	}
	if taken > 0 {
		return Series{}, errSlugTaken
	}
	now := s.now().UTC()
	if !exists {
		id := db.NewID()
		languages := []string{in.OriginalLang}
		if _, err := tx.Exec(ctx, `INSERT INTO series (id, project_id, owner_id, slug, title, synopsis, genres, tags, format,
			age_rating, licence, visibility, cover_asset_id, support_links, ai_label, original_lang, languages, status,
			schedule, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?, ?, ?, ?, ?)`,
			id, p.ID, p.OwnerID, in.Slug, in.Title, in.Synopsis, db.MustJSON(in.Genres), db.MustJSON(in.Tags), in.Format,
			in.AgeRating, in.Licence, in.Visibility, db.NullString(p.CoverAssetID), db.MustJSON(in.SupportLinks),
			in.OriginalLang, db.MustJSON(languages), in.Status, in.Schedule, now, now); err != nil {
			if db.IsUniqueViolation(err) {
				return Series{}, errSlugTaken
			}
			return Series{}, fmt.Errorf("publish: create series: %w", err)
		}
		cur.ID = id
	} else {
		// A series that has gone live never returns to draft by itself.
		status := in.Status
		if status == SeriesDraft && cur.PublishedAt != nil {
			status = SeriesOngoing
		}
		if _, err := tx.Exec(ctx, `UPDATE series SET slug = ?, title = ?, synopsis = ?, genres = ?, tags = ?, format = ?,
			age_rating = ?, licence = ?, visibility = ?, support_links = ?, original_lang = ?, status = ?, schedule = ?, updated_at = ?
			WHERE id = ?`, in.Slug, in.Title, in.Synopsis, db.MustJSON(in.Genres), db.MustJSON(in.Tags), in.Format,
			in.AgeRating, in.Licence, in.Visibility, db.MustJSON(in.SupportLinks), in.OriginalLang, status, in.Schedule, now, cur.ID); err != nil {
			if db.IsUniqueViolation(err) {
				return Series{}, errSlugTaken
			}
			return Series{}, fmt.Errorf("publish: update series: %w", err)
		}
	}
	if err := writeCreditsTx(ctx, tx, cur.ID, p.ID, p.OwnerID, in.OwnerCredit, in.Credits, now); err != nil {
		return Series{}, err
	}
	out, _, err := seriesForProject(ctx, tx, p.ID)
	return out, err
}

// ensureSeriesTx returns the project's series, creating it from the project
// defaults (with the given visibility) when the first chapter is published or
// a cover is chosen.
func (s *service) ensureSeriesTx(ctx context.Context, tx *db.Tx, actor studio.Actor, p studio.Project, visibility string) (Series, error) {
	cur, exists, err := seriesForProject(ctx, tx, p.ID)
	if err != nil || exists {
		return cur, err
	}
	rules := s.contentRules(ctx)
	in, err := seriesDefaults(ctx, tx, p, adminapi.DefaultRating(rules))
	if err != nil {
		return Series{}, err
	}
	if slices.Contains(Visibilities, visibility) {
		in.Visibility = visibility
	}
	errs := in.validate()
	checkSeriesRules(rules, in, errs)
	if len(errs) > 0 {
		return Series{}, fmt.Errorf("%w: project defaults: %v", ErrInvalid, errs)
	}
	return s.saveSeriesTx(ctx, tx, actor, p, in, time.Time{})
}

// setSeriesRulesTx lets E1 change the series-level age rating and licence.
// An unchanged choice leaves the series (and its revision) as it is, so other
// open forms do not turn stale.
func (s *service) setSeriesRulesTx(ctx context.Context, tx *db.Tx, seriesID, rating, licence string) error {
	if !slices.Contains(Ratings, rating) || !slices.Contains(Licences, licence) {
		return ErrInvalid
	}
	_, err := tx.Exec(ctx, `UPDATE series SET age_rating = ?, licence = ?, updated_at = ? WHERE id = ? AND (age_rating <> ? OR licence <> ?)`,
		rating, licence, s.now().UTC(), seriesID, rating, licence)
	return err
}

func isConflict(err error) bool { return errors.Is(err, ErrConflict) }
