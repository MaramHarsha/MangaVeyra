// Package publishtest creates published series, chapter publications and
// frozen pages for the tests of modules that read through publish.Service
// (reader, social, discover, analytics) and of publish itself.
//
// It writes the publish tables directly instead of running the E1 publish
// flow, so a test can build any combination of instance mode, series
// visibility, age rating, format, status, AI label and publication
// language/number/status/visibility, with frozen PNG pages and guided-view
// panels. Projects and chapters are created through studio.Service. Every
// helper works on SQLite and PostgreSQL.
//
// A series is visible to readers outside its project only while they can
// read at least one of its chapters (ADR 0012 §1): a series made with Series
// alone is seen by its project members only. Publish a chapter, or use
// ReadableSeries, before reading it as anyone else.
//
//	ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
//	f := publishtest.New(t, ta)
//	f.SetMode(settings.ModePublic)
//	s := f.Series(publishtest.SeriesSpec{AgeRating: publish.RatingTeen})
//	ch := f.Publish(s, publishtest.PublicationSpec{Pages: 3})
//	got, pages, err := f.Service().Publication(ctx, publish.Viewer{}, s.ID, ch.Number, "")
package publishtest

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// Default frozen page size in pixels (small, so fixtures stay fast).
const (
	DefaultPageWidth  = 160
	DefaultPageHeight = 240
)

// Fixture builds publish data inside one test app.
type Fixture struct {
	t   testing.TB
	ta  *testutil.TestApp
	seq atomic.Int64
}

// New returns a fixture builder for ta. The fixture's server allows mature and
// adult series (content rules, ADR 0013 §2), so tests of the reading rules
// exercise every rating; SetContent changes the rules.
func New(t testing.TB, ta *testutil.TestApp) *Fixture {
	t.Helper()
	f := &Fixture{t: t, ta: ta}
	f.SetContent(func(c *settings.Content) { c.AllowMature, c.AllowAdult = true, true })
	return f
}

// SetContent changes the instance content rules.
func (f *Fixture) SetContent(fn func(*settings.Content)) {
	f.t.Helper()
	if _, err := f.ta.Deps.Settings.UpdateInstance(context.Background(), "", func(i *settings.Instance) error {
		fn(&i.Content)
		return nil
	}); err != nil {
		f.t.Fatalf("publishtest: set content rules: %v", err)
	}
}

// Service returns the app's publish.Service.
func (f *Fixture) Service() publish.Service { return core.MustUse[publish.Service](f.ta.Deps) }

// SetMode switches the instance mode (settings.ModePersonal, ModePrivate or ModePublic).
func (f *Fixture) SetMode(mode string) {
	f.t.Helper()
	if _, err := f.ta.Deps.Settings.UpdateInstance(context.Background(), "", func(i *settings.Instance) error {
		i.Mode = mode
		return nil
	}); err != nil {
		f.t.Fatalf("publishtest: set mode %q: %v", mode, err)
	}
}

// User creates an active user with role (auth.RoleReader, RoleCreator or
// RoleAdmin) and testutil.TestPassword, so ta.Login works for it.
func (f *Fixture) User(role string) *auth.User {
	f.t.Helper()
	return f.ta.CreateUser(f.t, "", role)
}

// AddMember adds userID to the project with a studio role (studio.RoleViewer…).
func (f *Fixture) AddMember(projectID, userID, role string) {
	f.t.Helper()
	if _, err := f.ta.Deps.DB.Exec(context.Background(), `INSERT INTO project_members (project_id, user_id, role, created_at)
		VALUES (?, ?, ?, ?)`, projectID, userID, role, db.Now()); err != nil {
		f.t.Fatalf("publishtest: add member: %v", err)
	}
}

// Viewer returns the publish viewer for u (anonymous when u is nil).
func Viewer(u *auth.User) publish.Viewer {
	if u == nil {
		return publish.Viewer{}
	}
	return publish.Viewer{UserID: u.ID, Admin: u.Role == auth.RoleAdmin}
}

// Ago returns a pointer to the time d before now (UTC, microsecond precision).
func Ago(d time.Duration) *time.Time { t := db.Now().Add(-d); return &t }

// In returns a pointer to the time d after now (UTC, microsecond precision).
func In(d time.Duration) *time.Time { t := db.Now().Add(d); return &t }

// SeriesSpec describes a series. Zero fields take the documented defaults.
type SeriesSpec struct {
	// ID overrides the generated series ID (a db.NewID value), e.g. to test ordering.
	ID string
	// Owner must be able to create projects (creator or admin); nil creates a creator.
	Owner *auth.User
	// Title defaults to "Glass Orchard <n>"; Slug to a unique slug of the title.
	Title, Slug, Synopsis string
	// Genres and Tags default to ["adventure"] and ["original"] when nil.
	Genres, Tags []string
	// Format (manga_rtl), AgeRating (all), Licence (all_rights), Visibility
	// (public) and Status (ongoing) use the publish constants.
	Format, AgeRating, Licence, Visibility, Status string
	// AILabel defaults to human script, art and lettering when nil.
	AILabel      map[string]string
	SupportLinks []publish.SupportLink
	// OriginalLang defaults to "en"; Languages to [OriginalLang] when nil.
	OriginalLang string
	Languages    []string
	// PublishedAt defaults to an hour ago; NeverPublished leaves it NULL.
	PublishedAt    *time.Time
	NeverPublished bool
	// Cover stores a PNG cover asset and sets cover_asset_id.
	Cover         bool
	FollowerCount int
	ViewCount     int64
	// Schedule is the release note ("Weekly, Saturday").
	Schedule string
	// OwnerCredit is the owner's credit name; Creators are credited after the
	// owner in this order (any existing users; they need not be project members).
	OwnerCredit string
	Creators    []publish.Creator
}

// Series is a created series.
type Series struct {
	ID, Slug, ProjectID, Title, OriginalLang, CoverAssetID string
	Owner                                                  *auth.User
	PublishedAt                                            *time.Time
}

// Series creates a project (through studio.Service), its owner membership and a series row.
func (f *Fixture) Series(spec SeriesSpec) Series {
	f.t.Helper()
	ctx := context.Background()
	n := f.seq.Add(1)
	if spec.Owner == nil {
		spec.Owner = f.User(auth.RoleCreator)
	}
	if spec.Title == "" {
		spec.Title = fmt.Sprintf("Glass Orchard %d", n)
	}
	if spec.Slug == "" {
		spec.Slug = fmt.Sprintf("%s-%d", slug(spec.Title), n)
	}
	spec.Genres = orDefault(spec.Genres, []string{"adventure"})
	spec.Tags = orDefault(spec.Tags, []string{"original"})
	spec.Format = orString(spec.Format, "manga_rtl")
	spec.AgeRating = orString(spec.AgeRating, publish.RatingAll)
	spec.Licence = orString(spec.Licence, "all_rights")
	spec.Visibility = orString(spec.Visibility, publish.VisibilityPublic)
	spec.Status = orString(spec.Status, publish.SeriesOngoing)
	spec.OriginalLang = orString(spec.OriginalLang, "en")
	spec.Languages = orDefault(spec.Languages, []string{spec.OriginalLang})
	if spec.AILabel == nil {
		spec.AILabel = map[string]string{"script": "human", "art": "human", "lettering": "human"}
	}
	if spec.SupportLinks == nil {
		spec.SupportLinks = []publish.SupportLink{}
	}
	var publishedAt *time.Time
	if !spec.NeverPublished {
		publishedAt = spec.PublishedAt
		if publishedAt == nil {
			publishedAt = Ago(time.Hour)
		}
		utc := publishedAt.UTC()
		publishedAt = &utc
	}

	st := core.MustUse[studio.Service](f.ta.Deps)
	actor, err := st.ActorFromUser(ctx, spec.Owner.ID)
	if err != nil {
		f.t.Fatalf("publishtest: owner actor: %v", err)
	}
	lang := spec.OriginalLang
	if !pagedoc.IsContentLanguage(lang) {
		lang = "en"
	}
	project, err := st.CreateProject(ctx, actor, studio.NewProject{Title: spec.Title, Premise: "A courier crosses a flooded city.",
		Logline: "One last tide run.", Format: spec.Format, Language: lang})
	if err != nil {
		f.t.Fatalf("publishtest: create project: %v", err)
	}
	if spec.ID == "" {
		spec.ID = db.NewID()
	}
	out := Series{ID: spec.ID, Slug: spec.Slug, ProjectID: project.ID, Title: spec.Title,
		OriginalLang: spec.OriginalLang, Owner: spec.Owner, PublishedAt: publishedAt}
	if spec.Cover {
		out.CoverAssetID = f.png(project.ID, spec.Owner.ID, "series_cover", 90, 120, 0).ID
	}
	now := db.Now()
	if _, err := f.ta.Deps.DB.Exec(ctx, `INSERT INTO series (id, project_id, owner_id, slug, title, synopsis, genres, tags,
		format, age_rating, licence, visibility, cover_asset_id, support_links, ai_label, original_lang, languages, status,
		follower_count, view_count, published_at, schedule, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		out.ID, project.ID, spec.Owner.ID, spec.Slug, spec.Title, spec.Synopsis, db.MustJSON(spec.Genres), db.MustJSON(spec.Tags),
		spec.Format, spec.AgeRating, spec.Licence, spec.Visibility, db.NullString(out.CoverAssetID),
		db.MustJSON(spec.SupportLinks), db.MustJSON(spec.AILabel), spec.OriginalLang, db.MustJSON(spec.Languages), spec.Status,
		spec.FollowerCount, spec.ViewCount, timeArg(publishedAt), spec.Schedule, now, now); err != nil {
		f.t.Fatalf("publishtest: insert series: %v", err)
	}
	if _, err := f.ta.Deps.DB.Exec(ctx, `INSERT INTO series_creators (series_id, user_id, role, credit_name, order_idx, created_at)
		VALUES (?, ?, 'owner', ?, 0, ?)`, out.ID, spec.Owner.ID, spec.OwnerCredit, now); err != nil {
		f.t.Fatalf("publishtest: insert series creator: %v", err)
	}
	for i, c := range spec.Creators {
		if _, err := f.ta.Deps.DB.Exec(ctx, `INSERT INTO series_creators (series_id, user_id, role, credit_name, order_idx, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, out.ID, c.UserID, c.Role, c.CreditName, i+1, now); err != nil {
			f.t.Fatalf("publishtest: insert series creator: %v", err)
		}
	}
	return out
}

// ReadableSeries creates a series (as Series) with one public, published
// one-page chapter in its original language, so readers the series' own rules
// allow can see it. Further chapters published with Publish are numbered
// from 2.
func (f *Fixture) ReadableSeries(spec SeriesSpec) Series {
	f.t.Helper()
	s := f.Series(spec)
	f.Publish(s, PublicationSpec{Pages: 1})
	return s
}

// PublicationSpec describes one chapter publication. Zero fields take the documented defaults.
type PublicationSpec struct {
	// ChapterID publishes another language of an existing chapter; "" creates a chapter.
	ChapterID string
	// Lang defaults to the series' original language.
	Lang string
	// Number defaults to one more than the highest number in this series and language.
	Number float64
	// Title defaults to "Chapter <number>".
	Title string
	// Visibility defaults to public; Status to published.
	Visibility, Status string
	// PublishedAt defaults to an hour ago (published) or two hours ago
	// (unpublished); scheduled publications leave it NULL unless set.
	PublishedAt *time.Time
	// ScheduledAt defaults to an hour from now for scheduled publications.
	ScheduledAt *time.Time
	// UnpublishedAt defaults to an hour ago for unpublished publications.
	UnpublishedAt *time.Time
	// AILabel defaults to human script, art and lettering when nil.
	AILabel map[string]string
	// Pages is the number of frozen pages (default 2); NoPages stores none.
	Pages   int
	NoPages bool
	// Width and Height of each frozen page (default 160×240).
	Width, Height int
}

// Publication is a created publication.
type Publication struct {
	ID, SeriesID, ChapterID, Lang string
	Number                        float64
	Pages                         []Page
}

// Page is a frozen page.
type Page struct {
	ID, AssetID   string
	Number        int
	Width, Height int
}

// Publish creates a chapter publication of s with frozen PNG pages and guided panels.
func (f *Fixture) Publish(s Series, spec PublicationSpec) Publication {
	f.t.Helper()
	ctx := context.Background()
	d := f.ta.Deps.DB
	spec.Lang = orString(spec.Lang, s.OriginalLang)
	spec.Visibility = orString(spec.Visibility, publish.VisibilityPublic)
	spec.Status = orString(spec.Status, publish.PublicationPublished)
	if spec.Number == 0 {
		var highest float64
		if err := d.Get(ctx, &highest, `SELECT COALESCE(MAX(number), 0) FROM chapter_publications WHERE series_id = ? AND lang = ?`,
			s.ID, spec.Lang); err != nil {
			f.t.Fatalf("publishtest: next number: %v", err)
		}
		spec.Number = float64(int(highest) + 1)
	}
	if spec.Title == "" {
		spec.Title = "Chapter " + strconv.FormatFloat(spec.Number, 'f', -1, 64)
	}
	if spec.AILabel == nil {
		spec.AILabel = map[string]string{"script": "human", "art": "human", "lettering": "human"}
	}
	switch spec.Status {
	case publish.PublicationPublished:
		if spec.PublishedAt == nil {
			spec.PublishedAt = Ago(time.Hour)
		}
	case publish.PublicationScheduled:
		if spec.ScheduledAt == nil {
			spec.ScheduledAt = In(time.Hour)
		}
	case publish.PublicationUnpublished:
		if spec.PublishedAt == nil {
			spec.PublishedAt = Ago(2 * time.Hour)
		}
		if spec.UnpublishedAt == nil {
			spec.UnpublishedAt = Ago(time.Hour)
		}
	}
	pages := spec.Pages
	if pages <= 0 {
		pages = 2
	}
	if spec.NoPages {
		pages = 0
	}
	w, h := spec.Width, spec.Height
	if w <= 0 {
		w = DefaultPageWidth
	}
	if h <= 0 {
		h = DefaultPageHeight
	}

	if spec.ChapterID == "" {
		st := core.MustUse[studio.Service](f.ta.Deps)
		actor, err := st.ActorFromUser(ctx, s.Owner.ID)
		if err != nil {
			f.t.Fatalf("publishtest: owner actor: %v", err)
		}
		ch, err := st.CreateChapter(ctx, actor, s.ProjectID, spec.Title)
		if err != nil {
			f.t.Fatalf("publishtest: create chapter: %v", err)
		}
		spec.ChapterID = ch.ID
	}

	out := Publication{ID: db.NewID(), SeriesID: s.ID, ChapterID: spec.ChapterID, Lang: spec.Lang, Number: spec.Number}
	now := db.Now()
	if _, err := d.Exec(ctx, `INSERT INTO chapter_publications (id, series_id, chapter_id, lang, number, title, visibility,
		flags, ai_label, status, page_count, scheduled_at, published_at, unpublished_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '{}', ?, ?, ?, ?, ?, ?, ?, ?)`,
		out.ID, s.ID, spec.ChapterID, spec.Lang, spec.Number, spec.Title, spec.Visibility, db.MustJSON(spec.AILabel),
		spec.Status, pages, timeArg(spec.ScheduledAt), timeArg(spec.PublishedAt), timeArg(spec.UnpublishedAt), now, now); err != nil {
		f.t.Fatalf("publishtest: insert publication: %v", err)
	}
	for n := 1; n <= pages; n++ {
		a := f.png(s.ProjectID, s.Owner.ID, "publication_page", w, h, n)
		panels := GuidedPanels(w, h)
		page := Page{ID: db.NewID(), AssetID: a.ID, Number: n, Width: w, Height: h}
		if _, err := d.Exec(ctx, `INSERT INTO publication_pages (id, publication_id, number, asset_id, width, height, panels,
			alt_text, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			page.ID, out.ID, n, a.ID, w, h, db.MustJSON(panels), PageAltText(n), now); err != nil {
			f.t.Fatalf("publishtest: insert page: %v", err)
		}
		out.Pages = append(out.Pages, page)
	}
	f.addLanguage(s.ID, spec.Lang)
	return out
}

// GuidedPanels is the frozen guided-view data every fixture page carries: two
// polygon panels in reading order with alt-text and lettered bubbles.
func GuidedPanels(w, h int) []publish.GuidedPanel {
	fw, fh := float64(w), float64(h)
	return []publish.GuidedPanel{
		{Order: 1, Points: []pagedoc.Point{{X: 0, Y: 0}, {X: fw, Y: 0}, {X: fw, Y: fh * 0.45}, {X: 0, Y: fh * 0.55}},
			X: 0, Y: 0, W: fw, H: fh * 0.55,
			AltText: "Rin Asagiri sprints along the flooded seawall with a parcel under one arm.",
			Bubbles: []publish.GuidedBubble{
				{Order: 1, Kind: "speech", Speaker: "Rin Asagiri", Text: "Keep moving!"},
				{Order: 2, Kind: "sfx", Text: "SPLASH"},
			}},
		{Order: 2, Points: []pagedoc.Point{{X: 0, Y: fh * 0.55}, {X: fw, Y: fh * 0.45}, {X: fw, Y: fh}, {X: 0, Y: fh}},
			X: 0, Y: fh * 0.45, W: fw, H: fh * 0.55,
			AltText: "Kaito Mbeki waits at the sea gate, watching the tide rise.",
			Bubbles: []publish.GuidedBubble{
				{Order: 1, Kind: "speech", Speaker: "Kaito Mbeki", Text: "The tide won't wait."},
			}},
	}
}

// PageAltText is the page-level alt-text of fixture page n.
func PageAltText(n int) string {
	return fmt.Sprintf("Page %d: Rin races the tide to the sea gate where Kaito waits.", n)
}

func (f *Fixture) addLanguage(seriesID, lang string) {
	f.t.Helper()
	ctx := context.Background()
	var raw string
	if err := f.ta.Deps.DB.Get(ctx, &raw, `SELECT languages FROM series WHERE id = ?`, seriesID); err != nil {
		f.t.Fatalf("publishtest: read languages: %v", err)
	}
	var langs db.JSON[[]string]
	_ = langs.Scan(raw)
	for _, l := range langs.V {
		if l == lang {
			return
		}
	}
	if _, err := f.ta.Deps.DB.Exec(ctx, `UPDATE series SET languages = ? WHERE id = ?`,
		db.MustJSON(append(langs.V, lang)), seriesID); err != nil {
		f.t.Fatalf("publishtest: update languages: %v", err)
	}
}

// png stores a solid-colour PNG of w×h as an asset of the project.
func (f *Fixture) png(projectID, ownerID, kind string, w, h, shade int) assets.Asset {
	f.t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	c := color.Gray{Y: uint8(40 + (shade*37)%200)}
	for i := range img.Pix {
		img.Pix[i] = c.Y
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		f.t.Fatalf("publishtest: encode png: %v", err)
	}
	a, err := assets.From(f.ta.Deps).Save(context.Background(), assets.SaveParams{OwnerID: ownerID, ProjectID: projectID,
		Kind: kind, Source: assets.SourceSystem, ContentType: "image/png", Data: buf.Bytes()})
	if err != nil {
		f.t.Fatalf("publishtest: save png: %v", err)
	}
	return a
}

func timeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func orString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefault(v, def []string) []string {
	if v == nil {
		return def
	}
	return v
}

func slug(title string) string {
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
	s := strings.TrimSuffix(b.String(), "-")
	if s == "" {
		s = "series"
	}
	return s
}
