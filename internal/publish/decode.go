package publish

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// JSON columns are decoded leniently: a malformed or missing value becomes an
// empty value instead of failing the read, and lists are never nil so API
// responses always carry arrays.

const seriesCols = `s.id, s.project_id, s.owner_id, s.slug, s.title, s.synopsis, s.genres, s.tags, s.format,
	s.age_rating, s.licence, s.visibility, s.cover_asset_id, s.banner_asset_id, s.support_links, s.ai_label,
	s.original_lang, s.languages, s.status, s.schedule, s.follower_count, s.view_count, s.published_at, s.updated_at`

type seriesRow struct {
	ID            string         `db:"id"`
	ProjectID     string         `db:"project_id"`
	OwnerID       string         `db:"owner_id"`
	Slug          string         `db:"slug"`
	Title         string         `db:"title"`
	Synopsis      string         `db:"synopsis"`
	Genres        sql.NullString `db:"genres"`
	Tags          sql.NullString `db:"tags"`
	Format        string         `db:"format"`
	AgeRating     string         `db:"age_rating"`
	Licence       string         `db:"licence"`
	Visibility    string         `db:"visibility"`
	CoverAssetID  sql.NullString `db:"cover_asset_id"`
	BannerAssetID sql.NullString `db:"banner_asset_id"`
	SupportLinks  sql.NullString `db:"support_links"`
	AILabel       sql.NullString `db:"ai_label"`
	OriginalLang  string         `db:"original_lang"`
	Languages     sql.NullString `db:"languages"`
	Status        string         `db:"status"`
	Schedule      string         `db:"schedule"`
	FollowerCount int64          `db:"follower_count"`
	ViewCount     int64          `db:"view_count"`
	PublishedAt   sql.NullTime   `db:"published_at"`
	UpdatedAt     time.Time      `db:"updated_at"`
}

func (r seriesRow) series() Series {
	return Series{
		ID:            r.ID,
		ProjectID:     r.ProjectID,
		OwnerID:       r.OwnerID,
		Slug:          r.Slug,
		Title:         r.Title,
		Synopsis:      r.Synopsis,
		Genres:        decodeStrings(r.Genres),
		Tags:          decodeStrings(r.Tags),
		Format:        r.Format,
		AgeRating:     r.AgeRating,
		Licence:       r.Licence,
		Visibility:    r.Visibility,
		CoverAssetID:  r.CoverAssetID.String,
		BannerAssetID: r.BannerAssetID.String,
		SupportLinks:  decodeSupportLinks(r.SupportLinks),
		AILabel:       decodeLabel(r.AILabel),
		OriginalLang:  r.OriginalLang,
		Languages:     decodeStrings(r.Languages),
		Status:        r.Status,
		Schedule:      strings.TrimSpace(r.Schedule),
		Creators:      []Creator{{UserID: r.OwnerID, Role: RoleOwner}},
		FollowerCount: int(r.FollowerCount),
		ViewCount:     r.ViewCount,
		PublishedAt:   timePtr(r.PublishedAt),
		UpdatedAt:     r.UpdatedAt.UTC(),
	}
}

const publicationCols = `p.id, p.series_id, p.chapter_id, p.lang, p.number, p.title, p.visibility, p.ai_label,
	p.status, p.page_count, p.scheduled_at, p.published_at, p.unpublished_at`

type publicationRow struct {
	ID            string         `db:"id"`
	SeriesID      string         `db:"series_id"`
	ChapterID     string         `db:"chapter_id"`
	Lang          string         `db:"lang"`
	Number        float64        `db:"number"`
	Title         string         `db:"title"`
	Visibility    string         `db:"visibility"`
	AILabel       sql.NullString `db:"ai_label"`
	Status        string         `db:"status"`
	PageCount     int64          `db:"page_count"`
	ScheduledAt   sql.NullTime   `db:"scheduled_at"`
	PublishedAt   sql.NullTime   `db:"published_at"`
	UnpublishedAt sql.NullTime   `db:"unpublished_at"`
}

func (r publicationRow) publication() Publication {
	return Publication{
		ID:            r.ID,
		SeriesID:      r.SeriesID,
		ChapterID:     r.ChapterID,
		Lang:          r.Lang,
		Number:        r.Number,
		Title:         r.Title,
		Visibility:    r.Visibility,
		AILabel:       decodeLabel(r.AILabel),
		Status:        r.Status,
		PageCount:     int(r.PageCount),
		ScheduledAt:   timePtr(r.ScheduledAt),
		PublishedAt:   timePtr(r.PublishedAt),
		UnpublishedAt: timePtr(r.UnpublishedAt),
	}
}

const pageCols = `pg.id, pg.publication_id, pg.number, pg.asset_id, pg.width, pg.height, pg.panels, pg.alt_text`

type pageRow struct {
	ID            string         `db:"id"`
	PublicationID string         `db:"publication_id"`
	Number        int64          `db:"number"`
	AssetID       sql.NullString `db:"asset_id"`
	Width         int64          `db:"width"`
	Height        int64          `db:"height"`
	Panels        sql.NullString `db:"panels"`
	AltText       string         `db:"alt_text"`
}

func (r pageRow) page() PublicationPage {
	return PublicationPage{
		ID:            r.ID,
		PublicationID: r.PublicationID,
		Number:        int(r.Number),
		AssetID:       r.AssetID.String,
		Width:         int(r.Width),
		Height:        int(r.Height),
		Panels:        decodePanels(r.Panels),
		AltText:       r.AltText,
	}
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

// decodeStrings reads a JSON array of strings, skipping non-string, blank and
// repeated entries.
func decodeStrings(raw sql.NullString) []string {
	out := []string{}
	var items []json.RawMessage
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &items) != nil {
		return out
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		var v string
		if json.Unmarshal(item, &v) != nil {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// decodeLabel reads a per-role AI label object (role → human | ai_assisted |
// ai). Values are kept as stored so provenance is never silently rewritten;
// only non-string and blank entries are dropped.
func decodeLabel(raw sql.NullString) map[string]string {
	out := map[string]string{}
	var items map[string]json.RawMessage
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &items) != nil {
		return out
	}
	for role, item := range items {
		var v string
		if json.Unmarshal(item, &v) != nil {
			continue
		}
		role, v = strings.TrimSpace(role), strings.TrimSpace(v)
		if role == "" || v == "" {
			continue
		}
		out[role] = v
	}
	return out
}

// decodeSupportLinks keeps only absolute http(s) links so a stored value can
// never become a script or relative link on the series page.
func decodeSupportLinks(raw sql.NullString) []SupportLink {
	out := []SupportLink{}
	var items []json.RawMessage
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &items) != nil {
		return out
	}
	for _, item := range items {
		var l SupportLink
		if json.Unmarshal(item, &l) != nil {
			continue
		}
		l.Label, l.URL = strings.TrimSpace(l.Label), strings.TrimSpace(l.URL)
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			continue
		}
		out = append(out, l)
	}
	return out
}

// decodePanels reads frozen guided-view panels, skipping malformed entries,
// and returns panels and their bubbles in reading order.
func decodePanels(raw sql.NullString) []GuidedPanel {
	out := []GuidedPanel{}
	var items []json.RawMessage
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &items) != nil {
		return out
	}
	for _, item := range items {
		var p GuidedPanel
		if json.Unmarshal(item, &p) != nil {
			continue
		}
		if p.Points == nil {
			p.Points = []pagedoc.Point{}
		}
		if p.Bubbles == nil {
			p.Bubbles = []GuidedBubble{}
		}
		sort.SliceStable(p.Bubbles, func(i, j int) bool { return p.Bubbles[i].Order < p.Bubbles[j].Order })
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}
