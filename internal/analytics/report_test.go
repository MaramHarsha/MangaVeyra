package analytics_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// readerSession records one reader of a chapter: an open, page events up to
// furthest (30 s each) and optionally a completion.
func readerSession(s publishtest.Series, p publishtest.Publication, user, session string, furthest int, done bool, at time.Time) []rawEvent {
	out := []rawEvent{{Series: s.ID, Publication: p.ID, User: user, Session: session, Kind: "open", Page: 1, At: at}}
	for page := 1; page <= furthest; page++ {
		out = append(out, rawEvent{Series: s.ID, Publication: p.ID, User: user, Session: session, Kind: "page", Page: page,
			MS: 30000, At: at.Add(time.Duration(page) * time.Second)})
	}
	if done {
		out = append(out, rawEvent{Series: s.ID, Publication: p.ID, User: user, Session: session, Kind: "complete", Page: furthest,
			At: at.Add(time.Minute)})
	}
	return out
}

func TestBuildReportNumbers(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		s := f.Series(publishtest.SeriesSpec{Title: "Tidebreak Courier", PublishedAt: publishtest.Ago(200 * 24 * time.Hour)})
		ch1 := f.Publish(s, publishtest.PublicationSpec{Pages: 10, PublishedAt: publishtest.Ago(150 * 24 * time.Hour)})
		ch2 := f.Publish(s, publishtest.PublicationSpec{Pages: 10, PublishedAt: publishtest.Ago(100 * 24 * time.Hour)})
		member := f.User(auth.RoleReader)
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) // Wednesday; the window starts Monday Aug 24
		thisWeek := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
		var events []rawEvent
		events = append(events, readerSession(s, ch2, "", "a", 10, true, thisWeek)...)
		events = append(events, readerSession(s, ch2, member.ID, "", 5, false, thisWeek)...)
		events = append(events, readerSession(s, ch2, "", "c", 5, false, thisWeek)...)
		events = append(events, readerSession(s, ch2, "", "d", 0, false, thisWeek)...)
		events = append(events,
			rawEvent{Series: s.ID, Publication: ch1.ID, Session: "e", Kind: "open", Page: 1, At: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)},
			rawEvent{Series: s.ID, Publication: ch1.ID, Session: "f", Kind: "open", Page: 1, At: time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)},
			// Two reads in the twelve weeks before the window.
			rawEvent{Series: s.ID, Publication: ch1.ID, Session: "g", Kind: "open", Page: 1, At: time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)},
			rawEvent{Series: s.ID, Publication: ch1.ID, Session: "h", Kind: "open", Page: 1, At: time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)},
			// After now: never counted.
			rawEvent{Series: s.ID, Publication: ch1.ID, Session: "i", Kind: "open", Page: 1, At: now.Add(time.Hour)},
		)
		insertEvents(t, ta, events...)
		for i, at := range []time.Time{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC), thisWeek} {
			insertFollow(t, ta, f.User(auth.RoleReader).ID, s.ID, at)
			_ = i
		}

		owner := publishtest.Viewer(s.Owner)
		series, err := f.Service().SeriesByID(ctx, owner, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		chapters, err := analytics.SeriesChapters(ctx, f.Service(), owner, series)
		if err != nil || len(chapters) != 2 {
			t.Fatalf("chapters = %+v, %v", chapters, err)
		}
		rep, err := analytics.BuildReport(ctx, ta.Deps.DB, now, series, chapters, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.Start.Equal(time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)) || len(rep.Weeks) != 12 {
			t.Fatalf("window = %v, %d weeks", rep.Start, len(rep.Weeks))
		}
		if rep.Reads != 6 || rep.PrevReads != 2 || !rep.PrevKnown {
			t.Errorf("reads = %d, previous = %d (known %v), want 6 and 2", rep.Reads, rep.PrevReads, rep.PrevKnown)
		}
		weekly := make([]int64, len(rep.Weeks))
		for i, w := range rep.Weeks {
			weekly[i] = w.Reads
		}
		if want := []int64{0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4}; !reflect.DeepEqual(weekly, want) {
			t.Errorf("weekly reads = %v, want %v", weekly, want)
		}
		if rep.Followers != 3 || rep.NewFollowers != 2 || rep.Weeks[0].NewFollowers != 1 || rep.Weeks[11].NewFollowers != 1 {
			t.Errorf("followers = %d (+%d), weeks %v", rep.Followers, rep.NewFollowers, rep.Weeks)
		}
		if rep.AvgMS != 20*30000/6 {
			t.Errorf("avg ms = %d, want %d", rep.AvgMS, 20*30000/6)
		}
		if rep.Focus == nil || rep.Focus.Number != 2 || rep.Readers != 4 || rep.Completed != 1 || rep.Completion != .25 {
			t.Fatalf("focus %+v: readers %d, completed %d, completion %v", rep.Focus, rep.Readers, rep.Completed, rep.Completion)
		}
		var pages []int
		var shares []float64
		for _, p := range rep.Pages {
			pages = append(pages, p.Page)
			shares = append(shares, p.Share)
		}
		if !reflect.DeepEqual(pages, []int{2, 4, 6, 8, 10}) || !reflect.DeepEqual(shares, []float64{.75, .75, .25, .25, .25}) {
			t.Errorf("drop-off = %v %v", pages, shares)
		}
		if rep.Biggest != 2 || rep.Pages[2].From != 5 {
			t.Errorf("biggest drop = %d (%+v), want p. 5–6", rep.Biggest, rep.Pages)
		}

		// One chapter: only its reads, its readers and its drop-off.
		one, err := analytics.BuildReport(ctx, ta.Deps.DB, now, series, chapters, &chapters[0])
		if err != nil {
			t.Fatal(err)
		}
		if one.Reads != 2 || one.PrevReads != 2 || one.Focus.Number != 1 || one.Readers != 2 || one.Completed != 0 || one.AvgMS != 0 {
			t.Errorf("chapter 1 report: reads %d prev %d focus %v readers %d completed %d avg %d",
				one.Reads, one.PrevReads, one.Focus.Number, one.Readers, one.Completed, one.AvgMS)
		}
	})
}

func TestAnalyticsPage(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		owner := f.User(auth.RoleCreator)
		s := f.Series(publishtest.SeriesSpec{Owner: owner, Title: "Tidebreak Courier"})
		other := f.Series(publishtest.SeriesSpec{Owner: owner, Title: "Low Tide Radio"})
		ch := f.Publish(s, publishtest.PublicationSpec{Pages: 10, Title: "The sea gate"})
		at := db.Now().Add(-time.Minute)
		var events []rawEvent
		events = append(events, readerSession(s, ch, "", "a", 10, true, at)...)
		events = append(events, readerSession(s, ch, "", "b", 5, false, at)...)
		events = append(events, readerSession(s, ch, "", "c", 5, false, at)...)
		events = append(events, readerSession(s, ch, "", "d", 1, false, at)...)
		insertEvents(t, ta, events...)
		insertFollow(t, ta, f.User(auth.RoleReader).ID, s.ID, at)

		oc := ta.Login(t, owner.Username)
		resp := oc.Get("/studio/analytics?series=" + s.ID)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("owner: %d", resp.StatusCode)
		}
		body := string(resp.Body)
		mustContain(t, body, `data-screen="F2"`, "Analytics · Tidebreak Courier", "Last 12 weeks · counted on this server",
			"All chapters", "Ch. 1 · The sea gate", ">Reads<", ">4<", ">Followers<", ">+1<", ">Completion<", ">25%<", "ch. 1",
			"Avg. time / chapter", "Weekly reads", "Where readers stop · ch. 1", ">p. 10<", "Biggest drop at p.",
			`name="series"`, "Low Tide Radio", `aria-current="page"`, "/studio/analytics")
		mustNotContain(t, body, "Revenue", "Earnings", "revenue", "earnings")
		// Without ?series the most read series is shown (not the newer Low
		// Tide Radio); an unknown chapter falls back to all chapters.
		if resp := oc.Get("/studio/analytics?chapter=99"); resp.StatusCode != http.StatusOK ||
			!strings.Contains(string(resp.Body), "Analytics · Tidebreak Courier") {
			t.Errorf("default series: %d", resp.StatusCode)
		}
		if resp := oc.Get("/studio/analytics?series=" + other.ID); !strings.Contains(string(resp.Body), "No reads yet") {
			t.Error("a series without reads shows the honest empty state")
		}

		// Project members with a creator role see the series' analytics
		// (ADR 0012 §8: creators, their own series).
		for _, role := range []string{studio.RoleWriter, studio.RoleArtist, studio.RoleLetterer, studio.RoleEditor} {
			m := f.User(auth.RoleCreator)
			f.AddMember(s.ProjectID, m.ID, role)
			mc := ta.Login(t, m.Username)
			if resp := mc.Get("/studio/analytics?series=" + s.ID); resp.StatusCode != http.StatusOK {
				t.Errorf("project %s: %d", role, resp.StatusCode)
			}
			if resp := mc.Get("/studio/analytics"); !strings.Contains(string(resp.Body), "Analytics · Tidebreak Courier") {
				t.Errorf("project %s does not get the series by default", role)
			}
		}
		// A project viewer only previews the work: like a stranger they get 404
		// for the series and it is not offered to them.
		viewer := f.User(auth.RoleReader)
		f.AddMember(s.ProjectID, viewer.ID, studio.RoleViewer)
		vc := ta.Login(t, viewer.Username)
		if resp := vc.Get("/studio/analytics?series=" + s.ID); resp.StatusCode != http.StatusNotFound {
			t.Errorf("project viewer: %d, want 404", resp.StatusCode)
		}
		if resp := vc.Get("/studio/analytics"); resp.StatusCode != http.StatusOK || strings.Contains(string(resp.Body), "Tidebreak") {
			t.Errorf("project viewer is offered the series: %d", resp.StatusCode)
		}
		// Everyone else gets 404 for the series and no series of their own.
		stranger := f.User(auth.RoleCreator)
		sc := ta.Login(t, stranger.Username)
		if resp := sc.Get("/studio/analytics?series=" + s.ID); resp.StatusCode != http.StatusNotFound {
			t.Errorf("stranger: %d", resp.StatusCode)
		}
		if resp := sc.Get("/studio/analytics"); resp.StatusCode != http.StatusOK || !strings.Contains(string(resp.Body), "No published series yet") ||
			strings.Contains(string(resp.Body), "Tidebreak") {
			t.Errorf("stranger without series: %d", resp.StatusCode)
		}
		if resp := ta.Login(t, ta.Admin.Username).Get("/studio/analytics?series=" + s.ID); resp.StatusCode != http.StatusNotFound {
			t.Errorf("admin outside the project: %d", resp.StatusCode)
		}
		if resp := ta.NewClient().Get("/studio/analytics?series=" + s.ID); resp.StatusCode != http.StatusSeeOther ||
			!strings.HasPrefix(resp.Header.Get("Location"), "/login") {
			t.Errorf("anonymous: %d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		_ = ctx
		_ = publish.VisibilityPublic
	})
}

func TestReportFormatting(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 940: "940", 1204: "1.2k", 12_440: "12.4k", 99_999: "99.9k", 184_200: "184k", 1_350_000: "1.3M"} {
		if got := analytics.Compact(n); got != want {
			t.Errorf("Compact(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int][]int{0: nil, 1: {1}, 8: {1, 2, 3, 4, 5, 6, 7, 8}, 9: {2, 4, 6, 8, 9}, 34: {5, 10, 15, 20, 25, 30, 34}, 35: {5, 10, 15, 20, 25, 30, 35}, 100: {15, 30, 45, 60, 75, 90, 100}} {
		if got := analytics.DropOffPages(n); !reflect.DeepEqual(got, want) {
			t.Errorf("DropOffPages(%d) = %v, want %v", n, got, want)
		}
	}
	if got := analytics.WeekStart(time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("WeekStart(Sunday) = %v", got)
	}
	if got := analytics.WeekStart(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("WeekStart(Monday) = %v", got)
	}
}
