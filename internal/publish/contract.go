package publish

// Frozen Phase 3 contract (docs/plans/0010-phase-3-publish-and-read.md, ADR 0012).
//
// The publish module owns series, chapter publications and their frozen pages.
// Reader, social, discover and analytics consume published content only through
// this interface (core.Use[publish.Service]); they never read drafts or Studio
// page documents directly.

import (
	"context"
	"errors"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// Visibility of a series or a chapter publication. Effective visibility is the
// stricter of the two, combined with the instance mode (ADR 0012).
const (
	VisibilityPublic   = "public"   // listed; readable by anyone the instance mode allows
	VisibilityUnlisted = "unlisted" // readable with the link; never listed in Explore or search
	VisibilityMembers  = "members"  // signed-in members of this server only
	VisibilityPrivate  = "private"  // project members only (preview)
)

// Series status.
const (
	SeriesDraft     = "draft"
	SeriesOngoing   = "ongoing"
	SeriesCompleted = "completed"
	SeriesHiatus    = "hiatus"
)

// Publication status. A scheduled publication becomes published by the
// scheduler at ScheduledAt; unpublishing is always a human action.
const (
	PublicationScheduled   = "scheduled"
	PublicationPublished   = "published"
	PublicationUnpublished = "unpublished"
)

// Age ratings. Mature and adult series are never shown to anonymous readers and
// ask a signed-in reader to confirm once per series (ADR 0012).
const (
	RatingAll    = "all"
	RatingTeen   = "teen"
	RatingMature = "mature"
	RatingAdult  = "adult"
)

// ErrNotFound is returned for anything the viewer may not see, so existence is
// never revealed. ErrInvalid marks rejected input; ErrConflict a stale revision.
var (
	ErrNotFound = errors.New("publish: not found")
	ErrInvalid  = errors.New("publish: invalid")
	ErrConflict = errors.New("publish: conflict")
)

// Viewer is who is reading. An empty UserID is an anonymous visitor.
type Viewer struct {
	UserID string
	Admin  bool
}

// SupportLink is an external support link shown on the series page (no
// payment UI is rendered in this repository; links open outside the app).
type SupportLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Creator is one credited creator of a series (series_creators), in credit order.
type Creator struct {
	UserID     string `json:"user_id"`
	Role       string `json:"role"`                  // owner, writer, artist, letterer, editor or translator
	CreditName string `json:"credit_name,omitempty"` // shown instead of the account's display name when set
}

// Series is a published project as readers see it.
type Series struct {
	ID            string            `json:"id"`
	ProjectID     string            `json:"project_id"`
	OwnerID       string            `json:"owner_id"`
	Slug          string            `json:"slug"`
	Title         string            `json:"title"`
	Synopsis      string            `json:"synopsis"`
	Genres        []string          `json:"genres"`
	Tags          []string          `json:"tags"`
	Format        string            `json:"format"`     // manga_rtl | comic_ltr | webtoon
	AgeRating     string            `json:"age_rating"` // all | teen | mature | adult
	Licence       string            `json:"licence"`    // all_rights | cc-by | cc-by-sa | cc-by-nc | cc-by-nc-sa | cc0
	Visibility    string            `json:"visibility"`
	CoverAssetID  string            `json:"cover_asset_id,omitempty"`
	BannerAssetID string            `json:"banner_asset_id,omitempty"`
	SupportLinks  []SupportLink     `json:"support_links"`
	AILabel       map[string]string `json:"ai_label"` // role → human | ai_assisted | ai (script, art, lettering)
	OriginalLang  string            `json:"original_lang"`
	Languages     []string          `json:"languages"`
	Status        string            `json:"status"`
	Schedule      string            `json:"schedule,omitempty"` // creator-written release note, e.g. "Weekly, Saturday"
	Creators      []Creator         `json:"creators"`           // credit order, the owner first
	FollowerCount int               `json:"follower_count"`
	ViewCount     int64             `json:"view_count"`
	PublishedAt   *time.Time        `json:"published_at,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// Publication is one chapter published in one language.
type Publication struct {
	ID            string            `json:"id"`
	SeriesID      string            `json:"series_id"`
	ChapterID     string            `json:"chapter_id"`
	Lang          string            `json:"lang"`
	Number        float64           `json:"number"`
	Title         string            `json:"title"`
	Visibility    string            `json:"visibility"`
	AILabel       map[string]string `json:"ai_label"`
	Status        string            `json:"status"`
	PageCount     int               `json:"page_count"`
	ScheduledAt   *time.Time        `json:"scheduled_at,omitempty"`
	PublishedAt   *time.Time        `json:"published_at,omitempty"`
	UnpublishedAt *time.Time        `json:"unpublished_at,omitempty"`
}

// GuidedBubble is one lettered line in reading order, for the guided view,
// alt-text and read-aloud.
type GuidedBubble struct {
	Order   int    `json:"order"`
	Kind    string `json:"kind"` // speech | thought | shout | whisper | caption | sfx
	Speaker string `json:"speaker,omitempty"`
	Text    string `json:"text"`
}

// GuidedPanel is a panel's exact geometry (page pixels) and its reading-order
// content, frozen from the editor at publish time.
type GuidedPanel struct {
	Order   int             `json:"order"`
	Points  []pagedoc.Point `json:"points"`
	X       float64         `json:"x"` // bounding box, page pixels
	Y       float64         `json:"y"`
	W       float64         `json:"w"`
	H       float64         `json:"h"`
	AltText string          `json:"alt_text"`
	Bubbles []GuidedBubble  `json:"bubbles"`
}

// PublicationPage is one frozen page image with its guided-view data.
type PublicationPage struct {
	ID            string        `json:"id"`
	PublicationID string        `json:"publication_id"`
	Number        int           `json:"number"`
	AssetID       string        `json:"asset_id"`
	Width         int           `json:"width"`
	Height        int           `json:"height"`
	Panels        []GuidedPanel `json:"panels"`
	AltText       string        `json:"alt_text"`
}

// SeriesFilter lists readable series for Explore, search, profiles and the
// library. Listed=true excludes unlisted series.
type SeriesFilter struct {
	OwnerID string
	IDs     []string
	Genre   string
	Listed  bool
	Cursor  string
	Limit   int
}

// Service is the read side every Phase 3 reader-facing module uses. Every
// method applies visibility, instance mode, publication status and age rules
// for the viewer, and returns ErrNotFound for anything not visible.
type Service interface {
	// SeriesBySlug and SeriesByID return a readable series.
	SeriesBySlug(ctx context.Context, v Viewer, slug string) (Series, error)
	SeriesByID(ctx context.Context, v Viewer, id string) (Series, error)
	// ListSeries lists readable series, newest published first; next is the
	// cursor for the following page ("" when done).
	ListSeries(ctx context.Context, v Viewer, f SeriesFilter) (list []Series, next string, err error)
	// Publications lists the readable published chapters of a series in one
	// language (the series' original language when lang is ""), by number.
	Publications(ctx context.Context, v Viewer, seriesID, lang string) ([]Publication, error)
	// Publication returns one readable published chapter and its frozen pages.
	Publication(ctx context.Context, v Viewer, seriesID string, number float64, lang string) (Publication, []PublicationPage, error)
	// PageImageURL returns a short-lived signed URL for a frozen page image the
	// viewer may read.
	PageImageURL(ctx context.Context, v Viewer, page PublicationPage, ttl time.Duration) (string, error)
	// CanRead reports whether the viewer may read the publication (used by
	// comments, bookmarks, downloads and analytics before writing anything).
	CanRead(ctx context.Context, v Viewer, s Series, p Publication) bool
}
