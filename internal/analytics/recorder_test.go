package analytics_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestRecorderIsProvided(t *testing.T) {
	ta := testutil.NewTestApp(t)
	if _, ok := core.Use[analytics.Recorder](ta.Deps); !ok {
		t.Fatal("analytics.Recorder is not provided")
	}
}

func open(s publishtest.Series, p publishtest.Publication) analytics.ReadEvent {
	return analytics.ReadEvent{SeriesID: s.ID, PublicationID: p.ID, Kind: analytics.EventOpen, Page: 1}
}

func TestRecordChecksAccess(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		rec := recorderOf(ta)
		member := f.User(auth.RoleReader)
		public := f.Series(publishtest.SeriesSpec{})
		publicCh := f.Publish(public, publishtest.PublicationSpec{})
		membersCh := f.Publish(public, publishtest.PublicationSpec{Visibility: publish.VisibilityMembers})
		scheduled := f.Publish(public, publishtest.PublicationSpec{Status: publish.PublicationScheduled})
		unpublished := f.Publish(public, publishtest.PublicationSpec{Status: publish.PublicationUnpublished})
		members := f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityMembers})
		membersSeriesCh := f.Publish(members, publishtest.PublicationSpec{})
		mature := f.Series(publishtest.SeriesSpec{AgeRating: publish.RatingMature})
		matureCh := f.Publish(mature, publishtest.PublicationSpec{})
		private := f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityPrivate})
		privateCh := f.Publish(private, publishtest.PublicationSpec{})
		other := f.Series(publishtest.SeriesSpec{})
		otherCh := f.Publish(other, publishtest.PublicationSpec{})

		anon, signedIn := publish.Viewer{}, publishtest.Viewer(member)
		cases := []struct {
			name    string
			v       publish.Viewer
			e       analytics.ReadEvent
			wantErr error
		}{
			{"anonymous public", anon, open(public, publicCh), nil},
			{"anonymous members chapter", anon, open(public, membersCh), analytics.ErrNotReadable},
			{"anonymous members series", anon, open(members, membersSeriesCh), analytics.ErrNotReadable},
			{"anonymous mature", anon, open(mature, matureCh), analytics.ErrNotReadable},
			{"anonymous private", anon, open(private, privateCh), analytics.ErrNotReadable},
			{"scheduled", signedIn, open(public, scheduled), analytics.ErrNotReadable},
			{"unpublished", signedIn, open(public, unpublished), analytics.ErrNotReadable},
			{"member members chapter", signedIn, open(public, membersCh), nil},
			{"member members series", signedIn, open(members, membersSeriesCh), nil},
			{"member mature", signedIn, open(mature, matureCh), nil},
			{"member private not in project", signedIn, open(private, privateCh), analytics.ErrNotReadable},
			{"project member private", publishtest.Viewer(private.Owner), open(private, privateCh), nil},
			{"publication of another series", signedIn, analytics.ReadEvent{SeriesID: public.ID, PublicationID: otherCh.ID, Kind: analytics.EventOpen}, analytics.ErrNotReadable},
			{"unknown publication", signedIn, analytics.ReadEvent{SeriesID: public.ID, PublicationID: "01jb0000000000000000000000", Kind: analytics.EventOpen}, analytics.ErrNotReadable},
			{"bad kind", signedIn, analytics.ReadEvent{SeriesID: public.ID, PublicationID: publicCh.ID, Kind: "view"}, analytics.ErrInvalidEvent},
			{"bad id", signedIn, analytics.ReadEvent{SeriesID: "x' OR 1=1 --", PublicationID: publicCh.ID, Kind: analytics.EventOpen}, analytics.ErrInvalidEvent},
			{"page zero", signedIn, analytics.ReadEvent{SeriesID: public.ID, PublicationID: publicCh.ID, Kind: analytics.EventPage}, analytics.ErrInvalidEvent},
			{"page too high", signedIn, analytics.ReadEvent{SeriesID: public.ID, PublicationID: publicCh.ID, Kind: analytics.EventPage, Page: analytics.MaxPage + 1}, analytics.ErrInvalidEvent},
		}
		for _, c := range cases {
			err := rec.Record(ctx, c.v, "session-"+c.name, c.e)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
			}
		}
		if err := rec.Record(ctx, anon, "", open(public, publicCh)); !errors.Is(err, analytics.ErrInvalidEvent) {
			t.Errorf("anonymous without session: err = %v", err)
		}
		if err := rec.Record(ctx, anon, strings.Repeat("s", analytics.MaxSessionID+1), open(public, publicCh)); !errors.Is(err, analytics.ErrInvalidEvent) {
			t.Errorf("oversized session: err = %v", err)
		}
		// Nothing unreadable was stored.
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events WHERE publication_id IN (?, ?, ?, ?, ?)`,
			scheduled.ID, unpublished.ID, privateCh.ID, otherCh.ID, membersCh.ID); n != 2 {
			t.Errorf("stored events for limited chapters = %d, want 2 (the member's members-only read, the owner's preview)", n)
		}

		// Personal and private modes: anonymous visitors read nothing.
		for _, mode := range []string{settings.ModePersonal, settings.ModePrivate} {
			f.SetMode(mode)
			if err := rec.Record(ctx, anon, "session-"+mode, open(other, otherCh)); !errors.Is(err, analytics.ErrNotReadable) {
				t.Errorf("%s mode anonymous: err = %v", mode, err)
			}
			if err := rec.Record(ctx, signedIn, "", open(other, otherCh)); err != nil {
				t.Errorf("%s mode member: err = %v", mode, err)
			}
		}
	})
}

func TestRecordStoresOnlyDailyHashes(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		rec := recorderOf(ta)
		clock := testutil.NewClock(time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC))
		analytics.SetClock(rec, clock.Now)
		s := f.Series(publishtest.SeriesSpec{})
		ch := f.Publish(s, publishtest.PublicationSpec{Pages: 3})
		const session = "raw-session-RIN-ASAGIRI-7"
		member := f.User(auth.RoleReader)

		if err := rec.Record(ctx, publish.Viewer{}, session, analytics.ReadEvent{SeriesID: s.ID, PublicationID: ch.ID,
			Kind: analytics.EventPage, Page: 2, MS: 1 << 30}); err != nil {
			t.Fatal(err)
		}
		if err := rec.Record(ctx, publishtest.Viewer(member), session, analytics.ReadEvent{SeriesID: s.ID, PublicationID: ch.ID,
			Kind: analytics.EventPage, Page: 1, MS: -40}); err != nil {
			t.Fatal(err)
		}
		day1 := analytics.HashSession(rec, session, clock.Now())
		clock.Advance(2 * time.Hour) // the next UTC day
		if err := rec.Record(ctx, publish.Viewer{}, session, analytics.ReadEvent{SeriesID: s.ID, PublicationID: ch.ID,
			Kind: analytics.EventPage, Page: 3, MS: 5000}); err != nil {
			t.Fatal(err)
		}
		day2 := analytics.HashSession(rec, session, clock.Now())
		if day1 == day2 || len(day1) != 64 || strings.Contains(day1, session) {
			t.Fatalf("session hash must rotate daily and hide the ID: %q / %q", day1, day2)
		}
		if again := analytics.HashSession(rec, session, clock.Now().Add(-time.Minute)); again != day2 {
			t.Fatal("the hash must be stable within a UTC day")
		}

		var rows []struct {
			UserID  *string `db:"user_id"`
			Session string  `db:"session_hash"`
			Page    int     `db:"page"`
			MS      int     `db:"ms"`
		}
		if err := ta.Deps.DB.Select(ctx, &rows, `SELECT user_id, session_hash, page, ms FROM read_events ORDER BY page`); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Fatalf("rows = %d, want 3", len(rows))
		}
		if rows[0].UserID == nil || *rows[0].UserID != member.ID || rows[0].Session != "" || rows[0].MS != 0 {
			t.Errorf("member row = %+v, want user id, no session hash, ms clamped to 0", rows[0])
		}
		if rows[1].UserID != nil || rows[1].Session != day1 || rows[1].MS != analytics.MaxDwellMS {
			t.Errorf("anonymous row = %+v, want day-1 hash and ms clamped to %d", rows[1], analytics.MaxDwellMS)
		}
		if rows[2].UserID != nil || rows[2].Session != day2 {
			t.Errorf("next-day row = %+v, want day-2 hash", rows[2])
		}
		// The raw session ID appears in no column of any row.
		var dump []string
		if err := ta.Deps.DB.Select(ctx, &dump, `SELECT id || '|' || series_id || '|' || COALESCE(publication_id, '') || '|' ||
			COALESCE(user_id, '') || '|' || session_hash || '|' || kind FROM read_events`); err != nil {
			t.Fatal(err)
		}
		for _, d := range dump {
			if strings.Contains(d, session) {
				t.Fatalf("raw session ID stored: %q", d)
			}
		}
	})
}

func TestOpenCountsOncePerReaderAndDay(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		rec := recorderOf(ta)
		clock := testutil.NewClock(time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))
		analytics.SetClock(rec, clock.Now)
		s := f.Series(publishtest.SeriesSpec{ViewCount: 7})
		ch := f.Publish(s, publishtest.PublicationSpec{Pages: 2})
		member := f.User(auth.RoleReader)
		for range 3 {
			for _, v := range []publish.Viewer{{}, publishtest.Viewer(member)} {
				if err := rec.Record(ctx, v, "tab-1", open(s, ch)); err != nil {
					t.Fatal(err)
				}
				if err := rec.Record(ctx, v, "tab-1", analytics.ReadEvent{SeriesID: s.ID, PublicationID: ch.ID, Kind: analytics.EventComplete, Page: 2}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := rec.Record(ctx, publish.Viewer{}, "tab-2", open(s, ch)); err != nil {
			t.Fatal(err)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events WHERE kind = 'open'`); n != 3 {
			t.Errorf("opens = %d, want 3 (anonymous tab-1, member, tab-2)", n)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events WHERE kind = 'complete'`); n != 2 {
			t.Errorf("completions = %d, want 2", n)
		}
		clock.Advance(24 * time.Hour)
		if err := rec.Record(ctx, publishtest.Viewer(member), "", open(s, ch)); err != nil {
			t.Fatal(err)
		}
		got, err := f.Service().SeriesByID(ctx, publish.Viewer{}, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ViewCount != 7+4 {
			t.Errorf("view_count = %d, want 11", got.ViewCount)
		}
	})
}

func TestRecordIsRateLimitedPerViewer(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		rec := recorderOf(ta)
		clock := testutil.NewClock(time.Now())
		analytics.SetClock(rec, clock.Now)
		analytics.SetLimit(rec, 0.5, 3, clock.Now)
		s := f.Series(publishtest.SeriesSpec{})
		ch := f.Publish(s, publishtest.PublicationSpec{Pages: 9})
		member := f.User(auth.RoleReader)
		page := func(n int) analytics.ReadEvent {
			return analytics.ReadEvent{SeriesID: s.ID, PublicationID: ch.ID, Kind: analytics.EventPage, Page: n, MS: 1000}
		}
		for i := 1; i <= 3; i++ {
			if err := rec.Record(ctx, publish.Viewer{}, "busy", page(i)); err != nil {
				t.Fatalf("event %d: %v", i, err)
			}
		}
		if err := rec.Record(ctx, publish.Viewer{}, "busy", page(4)); !errors.Is(err, analytics.ErrRateLimited) {
			t.Fatalf("4th event: err = %v, want ErrRateLimited", err)
		}
		// Other viewers have their own budget; members are limited by account,
		// not by the session they send.
		if err := rec.Record(ctx, publish.Viewer{}, "calm", page(1)); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 3; i++ {
			if err := rec.Record(ctx, publishtest.Viewer(member), "s"+string(rune('a'+i)), page(i)); err != nil {
				t.Fatal(err)
			}
		}
		if err := rec.Record(ctx, publishtest.Viewer(member), "fresh", page(4)); !errors.Is(err, analytics.ErrRateLimited) {
			t.Fatalf("member 4th event: err = %v, want ErrRateLimited", err)
		}
		clock.Advance(2 * time.Second)
		if err := rec.Record(ctx, publish.Viewer{}, "busy", page(5)); err != nil {
			t.Fatalf("after refill: %v", err)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events`); n != 8 {
			t.Errorf("stored = %d, want 8", n)
		}
	})
}

// The once-per-day rule holds when the same reader's opens and completions
// arrive at the same time (two tabs, a retried batch): PostgreSQL's READ
// COMMITTED must not let two transactions both see "no open yet".
func TestOpenCountsOnceUnderConcurrentRepeats(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		rec := recorderOf(ta)
		s := f.Series(publishtest.SeriesSpec{})
		ch := f.Publish(s, publishtest.PublicationSpec{Pages: 2})
		member := f.User(auth.RoleReader)
		done := analytics.ReadEvent{SeriesID: s.ID, PublicationID: ch.ID, Kind: analytics.EventComplete, Page: 2}
		var wg sync.WaitGroup
		errs := make(chan error, 160)
		for range 40 {
			for _, v := range []publish.Viewer{{}, publishtest.Viewer(member)} {
				wg.Add(2)
				go func() { defer wg.Done(); errs <- rec.Record(ctx, v, "same-tab", open(s, ch)) }()
				go func() { defer wg.Done(); errs <- rec.Record(ctx, v, "same-tab", done) }()
			}
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events WHERE kind = 'open'`); n != 2 {
			t.Errorf("opens = %d, want 2 (one anonymous reader, one member)", n)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events WHERE kind = 'complete'`); n != 2 {
			t.Errorf("completions = %d, want 2", n)
		}
		got, err := f.Service().SeriesByID(ctx, publish.Viewer{}, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ViewCount != 2 {
			t.Errorf("view_count = %d, want 2", got.ViewCount)
		}
	})
}

func TestRecordConcurrentOpensStaySane(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		rec := recorderOf(ta)
		s := f.Series(publishtest.SeriesSpec{})
		ch := f.Publish(s, publishtest.PublicationSpec{})
		var wg sync.WaitGroup
		errs := make(chan error, 20)
		for i := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- rec.Record(ctx, publish.Viewer{}, "reader-"+string(rune('a'+i)), open(s, ch))
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := f.Service().SeriesByID(ctx, publish.Viewer{}, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ViewCount != 20 || count(t, ta, `SELECT COUNT(*) FROM read_events`) != 20 {
			t.Errorf("view_count = %d, want 20", got.ViewCount)
		}
	})
}

// ADR 0012 §8: anonymous session hashes use a random salt per UTC day that is
// deleted once the day is over, so not even the master key together with a
// visitor's cookie can link that visitor's reads across days. Within a day
// every server process, and a restarted one, uses the same salt.
func TestSessionSaltIsRandomPerDayAndForgotten(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		createDaySalts(t, ta)
		ctx := context.Background()
		rec := recorderOf(ta)
		day1 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
		clock := testutil.NewClock(day1)
		analytics.SetClock(rec, clock.Now)
		s := f.Series(publishtest.SeriesSpec{})
		ch := f.Publish(s, publishtest.PublicationSpec{Pages: 3})
		const session = "visitor-cookie-id-Rin-Asagiri"
		page := func(n int) analytics.ReadEvent {
			return analytics.ReadEvent{SeriesID: s.ID, PublicationID: ch.ID, Kind: analytics.EventPage, Page: n, MS: 1000}
		}
		stored := func(n int) string {
			var h string
			if err := ta.Deps.DB.Get(ctx, &h, `SELECT session_hash FROM read_events WHERE page = ?`, n); err != nil {
				t.Fatal(err)
			}
			return h
		}
		if err := rec.Record(ctx, publish.Viewer{}, session, page(1)); err != nil {
			t.Fatal(err)
		}
		h1 := stored(1)

		// The day's salt is not derived from the master key and the date.
		key := ta.Deps.Secrets.Derive("analytics-session-hash-v1")
		daySalt := hmac.New(sha256.New, key)
		daySalt.Write([]byte(day1.Format(time.DateOnly)))
		derived := hmac.New(sha256.New, daySalt.Sum(nil))
		derived.Write([]byte(session))
		if h1 == hex.EncodeToString(derived.Sum(nil)) {
			t.Fatal("the session hash can be recomputed from the master key, the date and the cookie")
		}
		var days []string
		if err := ta.Deps.DB.Select(ctx, &days, `SELECT day FROM analytics_day_salts ORDER BY day`); err != nil {
			t.Fatal(err)
		}
		if len(days) != 1 || days[0] != "2026-11-08" {
			t.Fatalf("salt days = %v, want [2026-11-08]", days)
		}
		var salt string
		if err := ta.Deps.DB.Get(ctx, &salt, `SELECT salt FROM analytics_day_salts`); err != nil || len(salt) != 64 {
			t.Fatalf("salt = %q, %v; want 32 random bytes in hex", salt, err)
		}

		// Another server process on the same database, or this one after a
		// restart, hashes the same way within the day.
		other := analytics.NewTestRecorder(ta.Deps.DB, key, f.Service())
		analytics.SetClock(other, clock.Now)
		if got := analytics.HashSession(other, session, clock.Now().Add(3*time.Hour)); got != h1 {
			t.Fatalf("another process hashes %q, want %q", got, h1)
		}

		// The next day gets a new salt and the old one is deleted, so the
		// day-1 hash cannot be recomputed any more.
		clock.Advance(24 * time.Hour)
		if err := rec.Record(ctx, publish.Viewer{}, session, page(2)); err != nil {
			t.Fatal(err)
		}
		if h2 := stored(2); h2 == h1 || h2 == "" {
			t.Fatalf("day-2 hash %q must differ from day 1", h2)
		}
		days = nil
		if err := ta.Deps.DB.Select(ctx, &days, `SELECT day FROM analytics_day_salts ORDER BY day`); err != nil {
			t.Fatal(err)
		}
		if len(days) != 1 || days[0] != "2026-11-09" {
			t.Fatalf("salt days after the day changed = %v, want [2026-11-09]", days)
		}
		// The other process still held day 1's salt in memory; the
		// background loop's purge makes it forget it too.
		if err := analytics.PurgeDaySalts(other); err != nil {
			t.Fatal(err)
		}
		restarted := analytics.NewTestRecorder(ta.Deps.DB, key, f.Service())
		for name, r := range map[string]analytics.Recorder{"this process": rec, "the other process": other, "a restarted process": restarted} {
			if again := analytics.HashSession(r, session, day1); again == h1 {
				t.Fatalf("%s can still recompute the day-1 hash", name)
			}
		}

		// Without any event the background loop still forgets a finished day.
		clock.Advance(24 * time.Hour)
		if err := analytics.PurgeDaySalts(rec); err != nil {
			t.Fatal(err)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM analytics_day_salts WHERE day < '2026-11-10'`); n != 0 {
			t.Fatalf("%d salts of finished days kept", n)
		}
	})
}
