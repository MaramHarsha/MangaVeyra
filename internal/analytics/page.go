package analytics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// maxCreatorSeries bounds the F2 series selector.
const maxCreatorSeries = 100

// CreatorSeries lists the series whose analytics userID may see: series they
// own or whose project they belong to (ADR 0012 §8, "their own series only").
// Site admins get nothing extra. Series come from publish.Service, which lets
// project members preview their series in any state.
func CreatorSeries(ctx context.Context, d *db.DB, svc publish.Service, v publish.Viewer) ([]publish.Series, error) {
	if strings.TrimSpace(v.UserID) == "" {
		return nil, nil
	}
	var ids []string
	if err := d.Select(ctx, &ids, `SELECT s.id FROM series s WHERE s.owner_id = ?
		OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = s.project_id AND pm.user_id = ?)
		ORDER BY s.created_at DESC, s.id DESC LIMIT ?`, v.UserID, v.UserID, maxCreatorSeries); err != nil {
		return nil, fmt.Errorf("analytics: creator series: %w", err)
	}
	out := make([]publish.Series, 0, len(ids))
	for _, id := range ids {
		s, err := svc.SeriesByID(ctx, v, id)
		if errors.Is(err, publish.ErrNotFound) {
			continue // an owner who left the project no longer previews a private series
		}
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// SeriesChapters lists a series' readable published chapters in every language.
func SeriesChapters(ctx context.Context, svc publish.Service, v publish.Viewer, s publish.Series) ([]Chapter, error) {
	langs := append([]string{s.OriginalLang}, s.Languages...)
	seen := map[string]bool{}
	var pubs []publish.Publication
	for _, l := range langs {
		if l == "" || seen[l] || len(seen) >= 12 {
			continue
		}
		seen[l] = true
		list, err := svc.Publications(ctx, v, s.ID, l)
		if err != nil && !errors.Is(err, publish.ErrNotFound) {
			return nil, err
		}
		pubs = append(pubs, list...)
	}
	return ChaptersFrom(s.OriginalLang, pubs), nil
}

func (m *Module) page(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	v := publish.Viewer{UserID: u.ID, Admin: u.IsAdmin()}
	list, err := CreatorSeries(ctx, m.d.DB, m.publish, v)
	if err != nil {
		return err
	}
	opts := web.PageOpts{Title: i18n.T(ctx, "analytics.title"), Nav: layout.NavAnalytics, Tab: layout.TabHome}
	want := strings.TrimSpace(r.URL.Query().Get("series"))
	var cur *publish.Series
	for i := range list {
		if list[i].ID == want || (want == "" && i == 0) {
			cur = &list[i]
			break
		}
	}
	if cur == nil {
		if want != "" {
			return core.ErrNotFound()
		}
		return m.ui.Page(w, r, http.StatusOK, opts, emptyBody())
	}
	chapters, err := SeriesChapters(ctx, m.publish, v, *cur)
	if err != nil {
		return err
	}
	var chapter *Chapter
	if raw := strings.TrimSpace(r.URL.Query().Get("chapter")); raw != "" {
		n, err := strconv.ParseFloat(raw, 64)
		for i := range chapters {
			if err == nil && chapters[i].Number == n {
				chapter = &chapters[i]
			}
		}
	}
	rep, err := BuildReport(ctx, m.d.DB, m.rec.now(), *cur, chapters, chapter)
	if err != nil {
		return err
	}
	view := buildView(ctx, list, *cur, chapters, chapter, rep)
	opts.Title = view.Title
	return m.ui.Page(w, r, http.StatusOK, opts, reportBody(view))
}

// ---- view model ------------------------------------------------------------

type statView struct {
	Label, Value, Delta string
	// Tone colours Delta ("success" up, "warning" down, "" neutral); the sign
	// or text says the same, never colour alone.
	Tone string
}

type barView struct {
	X, Y, H, Title string
	Current        bool
}

type dropView struct {
	Page, Pct, Label string
	Share            float64
	Warn             bool
}

type reportView struct {
	Title, Subtitle  string
	SeriesID         string
	SeriesOptions    []ui.Option
	Chapter          string
	ChapterOptions   []ui.Option
	Stats            []statView
	Bars             []barView
	ChartTitle       string
	ChartDesc        string
	Axis             string
	HasReads         bool
	DropTitle        string
	Drops            []dropView
	DropNote         string
	DropNoteWarn     bool
	DropEmptyTitle   string
	DropEmptyBody    string
	FollowersHistory string
}

const (
	chartBarW  = 45
	chartStep  = 55
	chartH     = 264
	chartViewW = chartStep*ReportWeeks - (chartStep - chartBarW)
)

func buildView(ctx context.Context, list []publish.Series, cur publish.Series, chapters []Chapter, chapter *Chapter, rep Report) reportView {
	v := reportView{
		Title:    i18n.T(ctx, "analytics.title_series", "series", cur.Title),
		Subtitle: i18n.T(ctx, "analytics.subtitle"),
		SeriesID: cur.ID,
		HasReads: rep.Reads > 0,
	}
	if len(list) > 1 {
		for _, s := range list {
			v.SeriesOptions = append(v.SeriesOptions, ui.Option{Value: s.ID, Label: s.Title})
		}
	}
	v.ChapterOptions = []ui.Option{{Value: "", Label: i18n.T(ctx, "analytics.all_chapters")}}
	for _, c := range chapters {
		v.ChapterOptions = append(v.ChapterOptions, ui.Option{Value: chapterNumber(c.Number), Label: chapterLabel(ctx, c)})
	}
	if chapter != nil {
		v.Chapter = chapterNumber(chapter.Number)
	}

	reads := statView{Label: i18n.T(ctx, "analytics.reads"), Value: Compact(rep.Reads)}
	if rep.PrevKnown {
		reads.Delta, reads.Tone = percentChange(rep.Reads, rep.PrevReads)
	}
	followers := statView{Label: i18n.T(ctx, "analytics.followers"), Value: Compact(rep.Followers)}
	if rep.NewFollowers > 0 {
		followers.Delta, followers.Tone = "+"+Compact(rep.NewFollowers), "success"
	}
	completion := statView{Label: i18n.T(ctx, "analytics.completion"), Value: i18n.T(ctx, "analytics.none")}
	if rep.Focus != nil {
		completion.Delta = i18n.T(ctx, "analytics.chapter_short", "number", chapterNumber(rep.Focus.Number))
		if rep.Readers > 0 {
			completion.Value = strconv.Itoa(wholePercent(rep.Completion)) + "%"
			completion.Tone = "success"
		}
	}
	avg := statView{Label: i18n.T(ctx, "analytics.avg_time"), Value: i18n.T(ctx, "analytics.none")}
	if rep.Reads > 0 {
		avg.Value = Duration(ctx, rep.AvgMS)
	}
	v.Stats = []statView{reads, followers, completion, avg}

	var peak int64
	for _, w := range rep.Weeks {
		peak = max(peak, w.Reads)
	}
	var values, follows []string
	for i, w := range rep.Weeks {
		h := 0.0
		if peak > 0 {
			h = float64(w.Reads) / float64(peak) * chartH
		}
		h = math.Max(h, 2) // a week without reads keeps a visible baseline bar
		v.Bars = append(v.Bars, barView{
			X: strconv.Itoa(i * chartStep), Y: fmtFloat(chartH - h), H: fmtFloat(h), Current: i == len(rep.Weeks)-1,
			Title: i18n.T(ctx, "analytics.week_reads", "date", shortDate(ctx, w.Start), "count", w.Reads),
		})
		values = append(values, strconv.FormatInt(w.Reads, 10))
		follows = append(follows, strconv.FormatInt(w.NewFollowers, 10))
	}
	v.ChartTitle = i18n.T(ctx, "analytics.weekly_reads")
	v.ChartDesc = i18n.T(ctx, "analytics.weekly_desc", "values", strings.Join(values, ", "))
	v.FollowersHistory = i18n.T(ctx, "analytics.followers_desc", "values", strings.Join(follows, ", "))
	if len(rep.Weeks) > 0 {
		v.Axis = i18n.T(ctx, "analytics.axis", "from", shortDate(ctx, rep.Weeks[0].Start),
			"to", shortDate(ctx, rep.Weeks[len(rep.Weeks)-1].Start))
	}

	v.DropTitle = i18n.T(ctx, "analytics.where_stop_none")
	switch {
	case rep.Focus == nil:
		v.DropEmptyTitle, v.DropEmptyBody = i18n.T(ctx, "analytics.no_chapters.title"), i18n.T(ctx, "analytics.no_chapters.body")
	case rep.Readers == 0 || len(rep.Pages) == 0:
		v.DropTitle = i18n.T(ctx, "analytics.where_stop", "number", chapterNumber(rep.Focus.Number))
		v.DropEmptyTitle, v.DropEmptyBody = i18n.T(ctx, "analytics.no_reads.title"), i18n.T(ctx, "analytics.no_dropoff.body")
	default:
		v.DropTitle = i18n.T(ctx, "analytics.where_stop", "number", chapterNumber(rep.Focus.Number))
		for i, p := range rep.Pages {
			pct := strconv.Itoa(wholePercent(p.Share)) + "%"
			page := i18n.T(ctx, "analytics.page_short", "page", p.Page)
			v.Drops = append(v.Drops, dropView{Page: page, Pct: pct, Share: p.Share, Warn: i == rep.Biggest,
				Label: i18n.T(ctx, "analytics.reached", "page", p.Page, "pct", pct)})
		}
		if rep.Biggest >= 0 {
			b := rep.Pages[rep.Biggest]
			v.DropNote, v.DropNoteWarn = i18n.T(ctx, "analytics.biggest_drop", "from", b.From, "to", b.Page), true
		} else {
			v.DropNote = i18n.T(ctx, "analytics.no_drop")
		}
	}
	return v
}

func chapterLabel(ctx context.Context, c Chapter) string {
	if strings.TrimSpace(c.Title) == "" {
		return i18n.T(ctx, "analytics.chapter_option_untitled", "number", chapterNumber(c.Number))
	}
	return i18n.T(ctx, "analytics.chapter_option", "number", chapterNumber(c.Number), "title", c.Title)
}

func chapterNumber(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// wholePercent truncates a share to a whole percent so 99.6% never reads 100%.
func wholePercent(share float64) int {
	return int(math.Floor(math.Max(0, math.Min(1, share))*100 + 1e-9))
}

// percentChange formats cur against prev ("+22%", "−5%", "0%").
func percentChange(cur, prev int64) (string, string) {
	if prev <= 0 {
		return "", ""
	}
	pct := math.Round(float64(cur-prev) / float64(prev) * 100)
	switch {
	case pct > 0:
		return "+" + strconv.Itoa(int(pct)) + "%", "success"
	case pct < 0:
		return "−" + strconv.Itoa(int(-pct)) + "%", "warning"
	}
	return "0%", ""
}

// Compact formats a count like the artboard: 940, 1.2k, 12.4k, 184k, 1.3M.
func Compact(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	var s string
	switch {
	case n < 1000:
		s = strconv.FormatInt(n, 10)
	case n < 100_000:
		s = trimZero(math.Floor(float64(n)/100)/10) + "k"
	case n < 1_000_000:
		s = strconv.FormatInt(n/1000, 10) + "k"
	default:
		s = trimZero(math.Floor(float64(n)/100_000)/10) + "M"
	}
	if neg {
		return "−" + s
	}
	return s
}

func trimZero(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// Duration formats a mean reading time: "40 s", "6 m 40 s", "1 h 5 m".
func Duration(ctx context.Context, ms int64) string {
	s := int(math.Round(float64(ms) / 1000))
	switch {
	case s < 60:
		return i18n.T(ctx, "analytics.duration.s", "s", s)
	case s < 3600:
		return i18n.T(ctx, "analytics.duration.ms", "m", s/60, "s", s%60)
	}
	return i18n.T(ctx, "analytics.duration.hm", "h", s/3600, "m", s/60%60)
}

func shortDate(ctx context.Context, t time.Time) string {
	if i18n.LocaleFrom(ctx) == "ja" {
		return fmt.Sprintf("%d月%d日", t.Month(), t.Day())
	}
	return t.Format("Jan 2")
}
