package analytics

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// ReportWeeks is the F2 window: the current UTC week (from Monday 00:00) and
// the eleven before it. It stays inside the 90 days of raw events.
const ReportWeeks = 12

// Chapter is one chapter number of a series with every published language of
// it (the drop-off of a chapter combines its languages).
type Chapter struct {
	Number         float64
	Title          string
	PageCount      int
	PublicationIDs []string
}

// Week is one bar of the weekly chart.
type Week struct {
	Start        time.Time
	Reads        int64
	NewFollowers int64
}

// PageShare is one drop-off row: the share of a chapter's readers who reached
// Page.
type PageShare struct {
	Page  int
	Share float64
	// Drop is the share lost since the previous row (or page 1).
	Drop float64
	From int
}

// Report is everything F2 shows for one series (and optionally one chapter).
type Report struct {
	Start, End time.Time
	Weeks      []Week

	Reads int64
	// PrevReads covers the twelve weeks before Start; PrevKnown is false when
	// that period had no reads, so no change is claimed.
	PrevReads int64
	PrevKnown bool

	Followers    int64
	NewFollowers int64

	// AvgMS is the mean recorded page time per read (0 without reads).
	AvgMS int64

	// Focus is the chapter completion and drop-off describe (nil without
	// published chapters).
	Focus      *Chapter
	Readers    int64
	Completed  int64
	Completion float64
	Pages      []PageShare
	// Biggest is the index in Pages of the largest drop (-1 when nothing dropped).
	Biggest int
}

// reportScope narrows queries to a series and, optionally, publications.
type reportScope struct {
	seriesID string
	pubs     []string
}

func (s reportScope) where() (string, []any) {
	sql, args := "series_id = ?", []any{s.seriesID}
	if len(s.pubs) > 0 {
		in, a := inList(s.pubs)
		sql += " AND publication_id IN (" + in + ")"
		args = append(args, a...)
	}
	return sql, args
}

// WeekStart is Monday 00:00 UTC of t's week.
func WeekStart(t time.Time) time.Time {
	d := dayStart(t)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

// BuildReport computes F2 for series s. chapters are its published chapters
// (from publish.Service, by number); chapter, when non-nil, selects one of them.
func BuildReport(ctx context.Context, d *db.DB, now time.Time, s publish.Series, chapters []Chapter, chapter *Chapter) (Report, error) {
	r := Report{Biggest: -1}
	r.End = now.UTC()
	r.Start = WeekStart(now).AddDate(0, 0, -7*(ReportWeeks-1))
	scope := reportScope{seriesID: s.ID}
	if chapter != nil {
		scope.pubs = chapter.PublicationIDs
	}
	var err error
	for i := range ReportWeeks {
		from := r.Start.AddDate(0, 0, 7*i)
		to := from.AddDate(0, 0, 7)
		if to.After(r.End) {
			to = r.End
		}
		w := Week{Start: from}
		if w.Reads, err = countReads(ctx, d, scope, from, to, false); err != nil {
			return r, err
		}
		if w.NewFollowers, err = countFollows(ctx, d, s.ID, from, to); err != nil {
			return r, err
		}
		r.Reads += w.Reads
		r.NewFollowers += w.NewFollowers
		r.Weeks = append(r.Weeks, w)
	}
	if r.PrevReads, err = countReads(ctx, d, scope, r.Start.AddDate(0, 0, -7*ReportWeeks), r.Start, true); err != nil {
		return r, err
	}
	r.PrevKnown = r.PrevReads > 0
	if err := d.Get(ctx, &r.Followers, `SELECT COUNT(*) FROM follows WHERE series_id = ?`, s.ID); err != nil {
		return r, fmt.Errorf("analytics: followers: %w", err)
	}
	if r.Reads > 0 {
		where, args := scope.where()
		var ms int64
		if err := d.Get(ctx, &ms, `SELECT COALESCE(SUM(ms), 0) FROM read_events WHERE `+where+
			` AND kind = 'page' AND created_at >= ? AND created_at < ?`, append(args, r.Start, r.End)...); err != nil {
			return r, fmt.Errorf("analytics: reading time: %w", err)
		}
		r.AvgMS = ms / r.Reads
	}

	r.Focus = chapter
	if r.Focus == nil {
		if r.Focus, err = latestReadChapter(ctx, d, s.ID, r.Start, r.End, chapters); err != nil {
			return r, err
		}
	}
	if r.Focus != nil {
		if err := r.dropOff(ctx, d, *r.Focus); err != nil {
			return r, err
		}
	}
	return r, nil
}

// countReads counts open events in [from, to), adding daily aggregates when
// the period may reach past the raw retention.
func countReads(ctx context.Context, d *db.DB, scope reportScope, from, to time.Time, aggregates bool) (int64, error) {
	where, args := scope.where()
	var n int64
	if err := d.Get(ctx, &n, `SELECT COUNT(*) FROM read_events WHERE `+where+
		` AND kind = 'open' AND created_at >= ? AND created_at < ?`, append(args, from, to)...); err != nil {
		return 0, fmt.Errorf("analytics: reads: %w", err)
	}
	if aggregates {
		var m int64
		if err := d.Get(ctx, &m, `SELECT COALESCE(SUM(events), 0) FROM `+aggregateTable+` WHERE `+where+
			` AND kind = 'open' AND day >= ? AND day < ?`, append(args, from, to)...); err != nil {
			return 0, fmt.Errorf("analytics: aggregated reads: %w", err)
		}
		n += m
	}
	return n, nil
}

// countFollows is a read-only aggregate over the social module's follows.
func countFollows(ctx context.Context, d *db.DB, seriesID string, from, to time.Time) (int64, error) {
	var n int64
	if err := d.Get(ctx, &n, `SELECT COUNT(*) FROM follows WHERE series_id = ? AND created_at >= ? AND created_at < ?`,
		seriesID, from, to); err != nil {
		return 0, fmt.Errorf("analytics: follows: %w", err)
	}
	return n, nil
}

// latestReadChapter is the highest-numbered chapter read since from, or the
// latest published chapter when none was read.
func latestReadChapter(ctx context.Context, d *db.DB, seriesID string, from, to time.Time, chapters []Chapter) (*Chapter, error) {
	if len(chapters) == 0 {
		return nil, nil
	}
	var read []string
	if err := d.Select(ctx, &read, `SELECT DISTINCT publication_id FROM read_events
		WHERE series_id = ? AND kind = 'open' AND created_at >= ? AND created_at < ? AND publication_id IS NOT NULL`, seriesID, from, to); err != nil {
		return nil, fmt.Errorf("analytics: read chapters: %w", err)
	}
	seen := map[string]bool{}
	for _, id := range read {
		seen[id] = true
	}
	for i := len(chapters) - 1; i >= 0; i-- {
		for _, id := range chapters[i].PublicationIDs {
			if seen[id] {
				return &chapters[i], nil
			}
		}
	}
	return &chapters[len(chapters)-1], nil
}

// dropOff computes the share of readers reaching each page of ch. A reader is
// a member, or an anonymous session for one day, per publication; the furthest
// page they reached and whether they completed come from their events.
func (r *Report) dropOff(ctx context.Context, d *db.DB, ch Chapter) error {
	if len(ch.PublicationIDs) == 0 {
		return nil
	}
	in, args := inList(ch.PublicationIDs)
	var rows []struct {
		Max  int   `db:"m"`
		Done int   `db:"done"`
		N    int64 `db:"n"`
	}
	if err := d.Select(ctx, &rows, `SELECT m, done, COUNT(*) AS n FROM (
			SELECT MAX(page) AS m, MAX(CASE WHEN kind = 'complete' THEN 1 ELSE 0 END) AS done FROM read_events
			WHERE publication_id IN (`+in+`) AND created_at >= ? AND created_at < ? AND (user_id IS NOT NULL OR session_hash <> '')
			GROUP BY publication_id, user_id, session_hash
		) t GROUP BY m, done`, append(args, r.Start, r.End)...); err != nil {
		return fmt.Errorf("analytics: drop-off: %w", err)
	}
	reached := map[int]int64{} // furthest page → readers
	for _, row := range rows {
		r.Readers += row.N
		furthest := min(row.Max, ch.PageCount)
		if row.Done == 1 || (ch.PageCount > 0 && row.Max >= ch.PageCount) {
			r.Completed += row.N
			furthest = ch.PageCount
		}
		reached[furthest] += row.N
	}
	if r.Readers == 0 {
		return nil
	}
	r.Completion = float64(r.Completed) / float64(r.Readers)
	if ch.PageCount <= 0 {
		return nil
	}
	// atLeast[p] = readers whose furthest page is ≥ p; every reader opened it.
	atLeast := func(p int) int64 {
		if p <= 1 {
			return r.Readers
		}
		var n int64
		for page, c := range reached {
			if page >= p {
				n += c
			}
		}
		return n
	}
	prevPage, prevShare := 1, 1.0
	biggest := 0.0
	for _, p := range DropOffPages(ch.PageCount) {
		share := float64(atLeast(p)) / float64(r.Readers)
		row := PageShare{Page: p, Share: share, Drop: math.Max(0, prevShare-share), From: prevPage + 1}
		if p == 1 {
			row.From = 1
		}
		if row.Drop > biggest+1e-9 {
			biggest = row.Drop
			r.Biggest = len(r.Pages)
		}
		r.Pages = append(r.Pages, row)
		prevPage, prevShare = p, share
	}
	return nil
}

// DropOffPages picks the drop-off rows for a chapter of n pages: every page
// up to eight pages, otherwise every ⌈n/7⌉-th page and the last, at most eight
// rows (34 pages give the artboard's p. 5, 10 … 30, 34).
func DropOffPages(n int) []int {
	if n <= 0 {
		return nil
	}
	var out []int
	if n <= 8 {
		for p := 1; p <= n; p++ {
			out = append(out, p)
		}
		return out
	}
	step := (n + 6) / 7
	for p := step; p < n; p += step {
		out = append(out, p)
	}
	return append(out, n)
}

// ChaptersFrom groups publications (any language) by chapter number. Title and
// page count come from the original language when it has the chapter.
func ChaptersFrom(original string, pubs []publish.Publication) []Chapter {
	byNum := map[float64]*Chapter{}
	var nums []float64
	for _, p := range pubs {
		c := byNum[p.Number]
		if c == nil {
			c = &Chapter{Number: p.Number, Title: p.Title}
			byNum[p.Number] = c
			nums = append(nums, p.Number)
		}
		if p.Lang == original && strings.TrimSpace(p.Title) != "" {
			c.Title = p.Title
		}
		c.PageCount = max(c.PageCount, p.PageCount)
		c.PublicationIDs = append(c.PublicationIDs, p.ID)
	}
	sort.Float64s(nums)
	out := make([]Chapter, 0, len(nums))
	for _, n := range nums {
		out = append(out, *byNum[n])
	}
	return out
}
