package analytics_test

import (
	"context"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestRollUpAggregatesAndDeletesOldEvents(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		s := f.Series(publishtest.SeriesSpec{})
		ch1 := f.Publish(s, publishtest.PublicationSpec{Pages: 4})
		ch2 := f.Publish(s, publishtest.PublicationSpec{Pages: 4})
		member := f.User(auth.RoleReader)
		now := time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)
		old := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)     // > 90 days before now
		older := time.Date(2026, 6, 30, 22, 0, 0, 0, time.UTC) // the day before
		recent := now.AddDate(0, 0, -89)                       // kept
		insertEvents(t, ta,
			rawEvent{Series: s.ID, Publication: ch1.ID, Session: "anon-a", Kind: "open", Page: 1, At: old},
			rawEvent{Series: s.ID, Publication: ch1.ID, User: member.ID, Kind: "open", Page: 1, At: old.Add(time.Hour)},
			rawEvent{Series: s.ID, Publication: ch1.ID, Session: "anon-a", Kind: "page", Page: 2, MS: 3000, At: old.Add(time.Minute)},
			rawEvent{Series: s.ID, Publication: ch1.ID, Session: "anon-b", Kind: "page", Page: 2, MS: 2000, At: old.Add(2 * time.Minute)},
			rawEvent{Series: s.ID, Publication: ch1.ID, Session: "anon-b", Kind: "page", Page: 2, MS: 1000, At: old.Add(3 * time.Minute)},
			rawEvent{Series: s.ID, Publication: ch2.ID, Session: "anon-c", Kind: "open", Page: 1, At: older},
			rawEvent{Series: s.ID, Publication: ch2.ID, Session: "anon-c", Kind: "open", Page: 1, At: recent},
		)
		res, err := analytics.RollUp(ctx, ta.Deps.DB, now)
		if err != nil {
			t.Fatal(err)
		}
		if res.Days != 2 || res.Events != 6 {
			t.Fatalf("roll up = %+v, want 2 days and 6 events", res)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events`); n != 1 {
			t.Fatalf("raw events left = %d, want only the 89-day-old one", n)
		}
		var rows []struct {
			Day     time.Time `db:"day"`
			Pub     string    `db:"publication_id"`
			Kind    string    `db:"kind"`
			Page    int       `db:"page"`
			Events  int64     `db:"events"`
			Readers int64     `db:"readers"`
			MS      int64     `db:"ms"`
		}
		if err := ta.Deps.DB.Select(ctx, &rows, `SELECT day, publication_id, kind, page, events, readers, ms
			FROM read_event_days ORDER BY day, kind, page`); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Fatalf("aggregates = %+v, want 3 rows", rows)
		}
		if !rows[0].Day.Equal(time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)) || rows[0].Pub != ch2.ID || rows[0].Events != 1 || rows[0].Readers != 1 {
			t.Errorf("June 30 open = %+v", rows[0])
		}
		if rows[1].Kind != "open" || rows[1].Events != 2 || rows[1].Readers != 2 || rows[1].Pub != ch1.ID {
			t.Errorf("July 1 opens = %+v, want 2 events by 2 readers", rows[1])
		}
		if rows[2].Kind != "page" || rows[2].Page != 2 || rows[2].Events != 3 || rows[2].Readers != 2 || rows[2].MS != 6000 {
			t.Errorf("July 1 page 2 = %+v, want 3 events, 2 readers, 6000 ms", rows[2])
		}
		// A second pass finds nothing new and changes nothing.
		again, err := analytics.RollUp(ctx, ta.Deps.DB, now)
		if err != nil || again.Days != 0 {
			t.Fatalf("second roll up = %+v, %v", again, err)
		}
		if n := count(t, ta, `SELECT COALESCE(SUM(events), 0) FROM read_event_days`); n != 6 {
			t.Errorf("aggregated events = %d, want 6", n)
		}
		// Aggregates still count towards the previous period of F2.
		chapters := []analytics.Chapter{{Number: 1, PageCount: 4, PublicationIDs: []string{ch1.ID}}, {Number: 2, PageCount: 4, PublicationIDs: []string{ch2.ID}}}
		series, err := f.Service().SeriesByID(ctx, publishtest.Viewer(s.Owner), s.ID)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := analytics.BuildReport(ctx, ta.Deps.DB, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), series, chapters, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.PrevKnown || rep.PrevReads != 3 {
			t.Errorf("previous-period reads = %d (known %v), want 3 from aggregates", rep.PrevReads, rep.PrevKnown)
		}
	})
}
