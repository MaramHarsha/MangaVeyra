package publish

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// publishView is everything E1 (publish flow) and E1b (published) render.
type publishView struct {
	Project   studio.Project
	Chapter   studio.Chapter
	Series    Series
	HasSeries bool
	Lang      string
	Languages []langOption
	// OtherReady are lettered languages without a live publication yet.
	OtherReady []string
	Pub        pubState
	Form       publishForm
	Errors     fieldErrors
	// Switched is set when the creator picked another language: E1 shows
	// that language's alt-text drafts instead of publishing.
	Switched bool
	Pages    []altPage
	CoverURL string
	CoverJob *coverJobState
	CanCover bool
	Job      *jobs.Job
	// Running is set when a publish of this chapter and language was already
	// running, so the creator's new decision was not applied.
	Running   bool
	Base      string
	BackURL   string
	TZ        string
	Recorded  map[string]string
	rev       revision
	SeriesURL string
	ReaderURL string
	Now       time.Time
	// Revision and SeriesRevision are the chapter and series states E1 shows,
	// posted back so a publish applies only what the creator reviewed.
	Revision       string
	SeriesRevision string
}

// seriesRevision is the series state E1 shows: its last update, or "" before
// the series exists.
func seriesRevision(s Series, ok bool) string {
	if !ok {
		return ""
	}
	return s.UpdatedAt.UTC().Format(time.RFC3339Nano)
}

// stale reports which of the states the form was rendered from have changed
// since: the chapter (pages or chapter row) and the series.
func (v publishView) stale(r *http.Request) (chapter, series bool) {
	return web.FormValue(r, "revision") != v.Revision, web.FormValue(r, "series_revision") != v.SeriesRevision
}

type langOption struct {
	Value   string
	Missing int
}

// pubState is the current publication of the chapter in the view language.
type pubState struct {
	ID          string
	Status      string
	Title       string
	Visibility  string
	AILabel     map[string]string
	Flags       pubFlags
	PageCount   int
	PublishedAt *time.Time
	ScheduledAt *time.Time
	// Alt holds the frozen alt-text by panel ID (prefills E1 on update).
	Alt map[string]string
}

// publishForm is the E1 form.
type publishForm struct {
	Title, Synopsis, Tags, Visibility, AgeRating, Licence string
	When, Date, Time                                      string
	Notify                                                bool
	AILabel                                               map[string]string
	AltText                                               map[string]string
	ScheduledAt                                           time.Time
}

type altPage struct {
	ID     string
	Number int
	Panels []altPanel
}

type altPanel struct {
	ID    string
	Order int
	Value string
}

type coverJobState struct {
	Job    *jobs.Job
	Result gen.CoverResult
}

func (v publishView) live() bool { return v.Pub.Status == PublicationPublished }

func (v publishView) err(field string) string { return v.Errors[field] }

// buildView loads E1 for lang (the project language when lang is not lettered).
func (m *Module) buildView(r *http.Request, t target, lang string) (publishView, error) {
	ctx := r.Context()
	s := m.svc
	v := publishView{Project: t.project, Chapter: t.chapter, Errors: fieldErrors{}, Base: t.base, Now: s.now().UTC(),
		BackURL: t.base + "/edit", CanCover: s.cover != nil, Recorded: map[string]string{}}
	var err error
	if v.Series, v.HasSeries, err = seriesForProject(ctx, s.db, t.project.ID); err != nil {
		return v, err
	}
	src, err := s.loadChapter(ctx, t.project.ID, t.chapter.ID)
	if errors.Is(err, ErrConflict) {
		src, err = s.loadChapter(ctx, t.project.ID, t.chapter.ID)
	}
	if err != nil {
		return v, httpError(err)
	}
	v.rev, v.Revision, v.SeriesRevision = src.rev, src.rev.token(), seriesRevision(v.Series, v.HasSeries)
	langs := chapterLanguages(t.project.Language, src.pages)
	v.Lang = strings.TrimSpace(lang)
	if !slices.Contains(langs, v.Lang) {
		v.Lang = t.project.Language
		if v.HasSeries && slices.Contains(langs, v.Series.OriginalLang) {
			v.Lang = v.Series.OriginalLang
		}
	}
	live, err := m.liveLanguages(ctx, t.chapter.ID)
	if err != nil {
		return v, err
	}
	for _, l := range langs {
		o := langOption{Value: l, Missing: missingBubbles(src.pages, l)}
		v.Languages = append(v.Languages, o)
		if l != v.Lang && o.Missing == 0 && !live[l] {
			v.OtherReady = append(v.OtherReady, l)
		}
	}
	if v.Pub, err = loadPubState(ctx, s.db, t.chapter.ID, v.Lang); err != nil {
		return v, err
	}
	for _, role := range aiRoles {
		v.Recorded[role] = "human"
		if val := t.chapter.AILabel[role]; aiRank(val) >= 0 {
			v.Recorded[role] = val
		}
	}
	v.Form = publishForm{Title: t.chapter.Title, Synopsis: t.chapter.Summary, Visibility: VisibilityPublic, When: "now",
		Notify: true, AgeRating: RatingAll, Licence: "all_rights", AILabel: map[string]string{}, AltText: map[string]string{}}
	if v.HasSeries {
		v.Form.AgeRating, v.Form.Licence, v.Form.Visibility = v.Series.AgeRating, v.Series.Licence, v.Series.Visibility
		v.SeriesURL = "/s/" + url.PathEscape(v.Series.Slug)
	}
	if v.Pub.ID != "" {
		v.Form.Title, v.Form.Visibility = v.Pub.Title, v.Pub.Visibility
		v.Form.Synopsis, v.Form.Tags, v.Form.Notify = v.Pub.Flags.Synopsis, strings.Join(v.Pub.Flags.Tags, ", "), v.Pub.Flags.Notify
		if v.Pub.Status == PublicationScheduled && v.Pub.ScheduledAt != nil {
			loc := m.userLocation(ctx)
			v.Form.When = "schedule"
			v.Form.Date, v.Form.Time = v.Pub.ScheduledAt.In(loc).Format("2006-01-02"), v.Pub.ScheduledAt.In(loc).Format("15:04")
		}
		if v.live() && v.HasSeries {
			v.ReaderURL = v.SeriesURL + "/" + strconv.FormatFloat(float64(t.chapter.Number), 'f', -1, 64)
		}
	}
	for _, role := range aiRoles {
		val := v.Recorded[role]
		if p := v.Pub.AILabel[role]; aiRank(p) > aiRank(val) {
			val = p
		}
		v.Form.AILabel[role] = val
	}
	for _, p := range src.pages {
		panels, _ := s.guidedPage(ctx, src, p, v.Lang, v.Pub.Alt)
		ap := altPage{ID: p.ID, Number: p.Number}
		for _, fp := range panels {
			ap.Panels = append(ap.Panels, altPanel{ID: fp.PanelID, Order: fp.Order, Value: fp.AltText})
		}
		v.Pages = append(v.Pages, ap)
	}
	coverID := t.project.CoverAssetID
	if v.HasSeries && v.Series.CoverAssetID != "" {
		coverID = v.Series.CoverAssetID
	}
	v.CoverURL = s.assets.URLFor(ctx, coverID, 15*time.Minute)
	v.TZ = m.userLocation(ctx).String()
	return v, nil
}

func (v publishView) pageCount() int { return len(v.Pages) }

// liveLanguages lists the chapter's languages that are currently published.
func (m *Module) liveLanguages(ctx context.Context, chapterID string) (map[string]bool, error) {
	var langs []string
	if err := m.svc.db.Select(ctx, &langs, `SELECT lang FROM chapter_publications WHERE chapter_id = ? AND status = ?`,
		chapterID, PublicationPublished); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, l := range langs {
		out[l] = true
	}
	return out, nil
}

func loadPubState(ctx context.Context, q db.Querier, chapterID, lang string) (pubState, error) {
	var row struct {
		ID          string     `db:"id"`
		Status      string     `db:"status"`
		Title       string     `db:"title"`
		Visibility  string     `db:"visibility"`
		AILabel     string     `db:"ai_label"`
		Flags       string     `db:"flags"`
		PageCount   int        `db:"page_count"`
		PublishedAt *time.Time `db:"published_at"`
		ScheduledAt *time.Time `db:"scheduled_at"`
	}
	st := pubState{Alt: map[string]string{}, AILabel: map[string]string{}}
	if err := q.Get(ctx, &row, `SELECT id, status, title, visibility, ai_label, flags, page_count, published_at, scheduled_at
		FROM chapter_publications WHERE chapter_id = ? AND lang = ?`, chapterID, lang); err != nil {
		if db.IsNotFound(err) {
			return st, nil
		}
		return st, err
	}
	st.ID, st.Status, st.Title, st.Visibility, st.PageCount = row.ID, row.Status, row.Title, row.Visibility, row.PageCount
	st.PublishedAt, st.ScheduledAt, st.Flags = row.PublishedAt, row.ScheduledAt, decodeFlags(row.Flags)
	_ = json.Unmarshal([]byte(row.AILabel), &st.AILabel)
	var panels []string
	if err := q.Select(ctx, &panels, `SELECT panels FROM publication_pages WHERE publication_id = ? ORDER BY number`, row.ID); err != nil {
		return st, err
	}
	for _, raw := range panels {
		var fps []frozenPanel
		_ = json.Unmarshal([]byte(raw), &fps)
		for _, fp := range fps {
			if fp.PanelID != "" && fp.AltText != "" {
				st.Alt[fp.PanelID] = fp.AltText
			}
		}
	}
	return st, nil
}

// userLocation is the signed-in user's time zone (UTC when unset or unknown).
func (m *Module) userLocation(ctx context.Context) *time.Location {
	if u := core.UserFrom(ctx); u != nil && u.Timezone != "" {
		if loc, err := time.LoadLocation(u.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// readPublishForm validates the E1 form against the loaded view.
func (m *Module) readPublishForm(r *http.Request, v *publishView) (publishForm, fieldErrors) {
	f := publishForm{Title: web.FormValue(r, "title"), Synopsis: web.FormValue(r, "synopsis"), Tags: web.FormValue(r, "tags"),
		Visibility: web.FormValue(r, "visibility"), AgeRating: web.FormValue(r, "age_rating"), Licence: web.FormValue(r, "licence"),
		When: web.FormValue(r, "when"), Date: web.FormValue(r, "date"), Time: web.FormValue(r, "time"),
		Notify: web.FormBool(r, "notify"), AILabel: map[string]string{}, AltText: map[string]string{}}
	errs := fieldErrors{}
	if f.Title == "" || utf8.RuneCountInString(f.Title) > 160 {
		errs["title"] = "publish.error.chapter_title"
	}
	if utf8.RuneCountInString(f.Synopsis) > maxSynopsis {
		errs["synopsis"] = "publish.error.synopsis"
	}
	if tags := normalizeTags([]string{f.Tags}, false); len(tags) > maxTags {
		errs["tags"] = "publish.error.tags"
	} else {
		for _, t := range tags {
			if utf8.RuneCountInString(t) > maxTagLen {
				errs["tags"] = "publish.error.tags"
			}
		}
	}
	if web.FormValue(r, "lang") != v.Lang {
		errs["lang"] = "publish.error.choice"
	}
	for _, o := range v.Languages {
		if o.Value == v.Lang && o.Missing > 0 {
			errs["lang"] = "publish.error.lang_missing"
		}
	}
	if len(v.Pages) == 0 {
		errs[""] = "publish.error.no_pages"
	}
	if !slices.Contains(Visibilities, f.Visibility) {
		errs["visibility"] = "publish.error.choice"
	}
	if !slices.Contains(Ratings, f.AgeRating) {
		errs["age_rating"] = "publish.error.choice"
	}
	if !slices.Contains(Licences, f.Licence) {
		errs["licence"] = "publish.error.choice"
	}
	switch {
	case v.live():
		f.When = "now"
	case f.When == "schedule":
		at, err := time.ParseInLocation("2006-01-02 15:04", f.Date+" "+f.Time, m.userLocation(r.Context()))
		now := m.svc.now()
		if err != nil || !at.After(now.Add(time.Minute)) || at.After(now.AddDate(1, 0, 1)) {
			errs["when"] = "publish.error.schedule"
		}
		f.ScheduledAt = at.UTC()
	default:
		f.When = "now"
	}
	for _, role := range aiRoles {
		val := web.FormValue(r, "ai_"+role)
		if aiRank(val) < 0 || aiRank(val) < aiRank(v.Recorded[role]) {
			// A label may be corrected towards more AI involvement, never below the recorded history.
			errs["ai_"+role] = "publish.error.ai_label"
		}
		f.AILabel[role] = val
	}
	for _, p := range v.Pages {
		for _, pn := range p.Panels {
			if val, ok := r.PostForm["alt_"+pn.ID]; ok && len(val) > 0 {
				f.AltText[pn.ID] = clip(strings.Join(strings.Fields(val[0]), " "), maxAltText)
			}
		}
	}
	return f, errs
}

// applyFormAlt keeps the creator's alt-text edits when E1 is shown again.
func (v *publishView) applyFormAlt(r *http.Request) {
	for i := range v.Pages {
		for j, pn := range v.Pages[i].Panels {
			if val, ok := r.PostForm["alt_"+pn.ID]; ok && len(val) > 0 {
				v.Pages[i].Panels[j].Value = val[0]
			}
		}
	}
}

// seriesView is the series settings screen.
type seriesView struct {
	Project   studio.Project
	Exists    bool
	Series    Series
	Form      seriesInput
	Errors    fieldErrors
	Revision  string
	CoverURL  string
	SeriesURL string
	Action    string
	// Members are the project members who can be credited, the owner first.
	Members []member
}

// credit returns the credited role and name of a member in the form.
func (v seriesView) credit(userID string) Creator {
	for _, c := range v.Form.Credits {
		if c.UserID == userID {
			return c
		}
	}
	return Creator{UserID: userID}
}

func (m *Module) seriesView(r *http.Request, t target) (seriesView, error) {
	ctx := r.Context()
	v := seriesView{Project: t.project, Errors: fieldErrors{}, Action: "/studio/p/" + url.PathEscape(t.project.ID) + "/series"}
	var err error
	if v.Series, v.Exists, err = seriesForProject(ctx, m.svc.db, t.project.ID); err != nil {
		return v, err
	}
	if v.Exists {
		s := v.Series
		v.Form = seriesInput{Slug: s.Slug, Title: s.Title, Synopsis: s.Synopsis, Genres: s.Genres, Tags: s.Tags, Format: s.Format,
			AgeRating: s.AgeRating, Licence: s.Licence, Visibility: s.Visibility, Status: s.Status, OriginalLang: s.OriginalLang,
			SupportLinks: s.SupportLinks, Schedule: s.Schedule}
		if len(s.Creators) > 0 {
			v.Form.OwnerCredit, v.Form.Credits = s.Creators[0].CreditName, s.Creators[1:]
		}
		v.Revision = s.UpdatedAt.UTC().Format(time.RFC3339Nano)
		v.SeriesURL = "/s/" + url.PathEscape(s.Slug)
		v.CoverURL = m.svc.assets.URLFor(ctx, s.CoverAssetID, 15*time.Minute)
	} else if v.Form, err = seriesDefaults(ctx, m.svc.db, t.project); err != nil {
		return v, err
	}
	if v.Members, err = projectMembers(ctx, m.svc.db, t.project.ID, t.project.OwnerID); err != nil {
		return v, err
	}
	return v, nil
}

func (v seriesView) link(i int) SupportLink {
	if i < len(v.Form.SupportLinks) {
		return v.Form.SupportLinks[i]
	}
	return SupportLink{}
}
