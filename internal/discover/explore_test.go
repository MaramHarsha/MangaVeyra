package discover_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func section(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("section %q not found", start)
	}
	body = body[i:]
	if j := strings.Index(body, end); j >= 0 {
		body = body[:j]
	}
	return body
}

func inOrder(t *testing.T, where, body string, titles ...string) {
	t.Helper()
	last := -1
	for _, title := range titles {
		i := strings.Index(body, title)
		if i < 0 || i < last {
			t.Errorf("%s: %q missing or out of order (want %v)", where, title, titles)
			return
		}
		last = i
	}
}

func TestExploreRanksRecordedActivityOnly(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		shared := f.User(auth.RoleCreator)
		mk := func(title string, genres []string, owner *auth.User, views int64) (publishtest.Series, publishtest.Publication) {
			s := f.Series(publishtest.SeriesSpec{Title: title, Genres: genres, Owner: owner, ViewCount: views})
			return s, f.Publish(s, publishtest.PublicationSpec{})
		}
		a, ap := mk("Alpha Tide", []string{"mystery"}, nil, 0)
		b, bp := mk("Bravo Tide", []string{"mystery"}, nil, 0)
		c, cp := mk("Charlie Tide", []string{"horror"}, shared, 0)
		mk("Delta Tide", []string{"mystery", "horror"}, shared, 10)
		mk("Echo Tide", []string{"comedy"}, nil, 50)
		now := db.Now()
		n := 0
		read := func(s publishtest.Series, p publishtest.Publication, user string, at time.Time) {
			n++
			session := ""
			if user == "" {
				session = "session-" + strconv.Itoa(n)
			}
			if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO read_events (id, series_id, publication_id, user_id, session_hash, kind, page, ms, created_at)
				VALUES (?, ?, ?, ?, ?, 'open', 1, 0, ?)`, db.NewID(), s.ID, p.ID, db.NullString(user), session, at); err != nil {
				t.Fatal(err)
			}
		}
		follow := func(s publishtest.Series, at time.Time) {
			if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO follows (user_id, series_id, notify, created_at) VALUES (?, ?, TRUE, ?)`,
				f.User(auth.RoleReader).ID, s.ID, at); err != nil {
				t.Fatal(err)
			}
		}
		day := 24 * time.Hour
		for i := 0; i < 5; i++ {
			read(b, bp, "", now.Add(-time.Duration(i+1)*time.Hour))
		}
		for i := 0; i < 6; i++ {
			read(b, bp, "", now.Add(-9*day-time.Duration(i)*time.Hour))
		}
		read(a, ap, "", now.Add(-time.Hour))
		member := f.User(auth.RoleReader)
		read(a, ap, member.ID, now.Add(-2*time.Hour))
		follow(a, now.Add(-day))
		follow(a, now.Add(-2*day))
		read(c, cp, "", now.Add(-3*time.Hour))

		anon := ta.NewClient()
		rank := func(tab string) string {
			return section(t, get(t, anon, "/explore?rank="+tab), "mv-explore__rankings", "</aside>")
		}
		// Rising: Alpha grew by 2 reads and 2 followers, Charlie by 1 read;
		// Bravo read less than the week before.
		rising := rank("rising")
		inOrder(t, "rising", rising, "Alpha Tide", "Charlie Tide")
		hasNot(t, "rising", rising, "Bravo Tide", "Delta Tide", "Echo Tide")
		inOrder(t, "week", rank("week"), "Bravo Tide", "Alpha Tide", "Charlie Tide")
		all := rank("all")
		inOrder(t, "all time", all, "Echo Tide", "Delta Tide")
		hasNot(t, "all time", all, "Alpha Tide")

		risingShelf := section(t, get(t, anon, "/explore"), `id="mv-shelf-rising"`, "</section>")
		inOrder(t, "rising creators", risingShelf, "Bravo Tide", "Alpha Tide")
		hasNot(t, "rising creators", risingShelf, "Charlie Tide", "Delta Tide") // their owner has two series
		has(t, "rising creators", risingShelf, `class="mv-scard__rank" aria-hidden="true">1<`, "Ch. 1")

		// A member with history gets "Because you read …"; anonymous visitors
		// get "New & updated".
		has(t, "anonymous shelf", get(t, anon, "/explore"), "New &amp; updated")
		mine := section(t, get(t, ta.Login(t, member.Username), "/explore"), `id="mv-shelf-first"`, "</section>")
		has(t, "because", mine, "Because you read Alpha Tide")
		inOrder(t, "because", mine, "Bravo Tide", "Delta Tide")
		hasNot(t, "because", mine, "Echo Tide", "Charlie Tide")

		// Without recorded activity the rankings say so instead of inventing an order.
		for _, table := range []string{"read_events", "follows"} {
			if _, err := ta.Deps.DB.Exec(ctx, `DELETE FROM `+table); err != nil {
				t.Fatal(err)
			}
		}
		has(t, "empty rankings", rank("week"), "No rankings yet")
		has(t, "empty rising creators", get(t, anon, "/explore"), "No rising creators yet")
	})
}

func TestCoversAreSignedImages(t *testing.T) {
	ta := testutil.NewTestApp(t)
	f := publishtest.New(t, ta)
	f.SetMode(settings.ModePublic)
	s := f.Series(publishtest.SeriesSpec{Title: "Kettle Knight", Cover: true, Synopsis: "A knight in a teapot."})
	f.Publish(s, publishtest.PublicationSpec{})
	anon := ta.NewClient()
	for _, path := range []string{"/explore", "/search?q=kettle"} {
		body := get(t, anon, path)
		i := strings.Index(body, `<img src="/media/`)
		if i < 0 {
			t.Fatalf("%s: no signed cover image", path)
		}
		src := body[i+len(`<img src="`):]
		src = strings.ReplaceAll(src[:strings.IndexByte(src, '"')], "&amp;", "&")
		if resp := anon.Get(src); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
			t.Errorf("%s: cover %s: %d %s", path, src, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}

func TestExploreWithoutSeries(t *testing.T) {
	ta := testutil.NewTestApp(t)
	publishtest.New(t, ta).SetMode(settings.ModePublic)
	has(t, "empty explore", get(t, ta.NewClient(), "/explore"), "Nothing to read yet")
	has(t, "empty search", get(t, ta.NewClient(), "/search"), "No published series yet")
}
