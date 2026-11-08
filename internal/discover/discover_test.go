package discover_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func forEachDialect(t *testing.T, fn func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			f := publishtest.New(t, ta)
			f.SetMode(settings.ModePublic)
			fn(t, ta, f)
		})
	}
}

func get(t *testing.T, c *testutil.Client, path string) string {
	t.Helper()
	resp := c.Get(path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, resp.Body)
	}
	return string(resp.Body)
}

func has(t *testing.T, where, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s: missing %q", where, w)
		}
	}
}

func hasNot(t *testing.T, where, body string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(body, w) {
			t.Errorf("%s: leaks %q", where, w)
		}
	}
}

// visibilityFixture publishes one series per access case; every title shares
// the word "Lantern" so one search reaches them all.
type visibilityFixture struct {
	member, projectMember *auth.User
}

func publishVisibilityCases(t *testing.T, f *publishtest.Fixture) visibilityFixture {
	t.Helper()
	pub := func(spec publishtest.SeriesSpec) publishtest.Series {
		spec.Synopsis = "A lantern story about the harbour."
		s := f.Series(spec)
		f.Publish(s, publishtest.PublicationSpec{})
		return s
	}
	pub(publishtest.SeriesSpec{Title: "Lantern Public"})
	pub(publishtest.SeriesSpec{Title: "Lantern Teen", AgeRating: publish.RatingTeen})
	pub(publishtest.SeriesSpec{Title: "Lantern Unlisted", Visibility: publish.VisibilityUnlisted})
	pub(publishtest.SeriesSpec{Title: "Lantern Members", Visibility: publish.VisibilityMembers})
	private := pub(publishtest.SeriesSpec{Title: "Lantern Private", Visibility: publish.VisibilityPrivate})
	pub(publishtest.SeriesSpec{Title: "Lantern Mature", AgeRating: publish.RatingMature})
	pub(publishtest.SeriesSpec{Title: "Lantern Adult", AgeRating: publish.RatingAdult})
	pub(publishtest.SeriesSpec{Title: "Lantern Draft", Status: publish.SeriesDraft})
	pub(publishtest.SeriesSpec{Title: "Lantern Future", PublishedAt: publishtest.In(48 * time.Hour)})
	f.Series(publishtest.SeriesSpec{Title: "Lantern Never", NeverPublished: true, Synopsis: "A lantern story."})
	return visibilityFixture{member: f.User(auth.RoleReader), projectMember: private.Owner}
}

var always = []string{"Lantern Unlisted", "Lantern Private", "Lantern Draft", "Lantern Future", "Lantern Never"}

func TestExploreAndSearchVisibility(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		v := publishVisibilityCases(t, f)
		anon := ta.NewClient()
		for _, path := range []string{"/explore", "/search?q=lantern", "/search", "/search?tab=creators&q=lantern", "/search?tab=tags"} {
			body := get(t, anon, path)
			hasNot(t, "anonymous "+path, body, append(always, "Lantern Members", "Lantern Mature", "Lantern Adult")...)
		}
		has(t, "anonymous search", get(t, anon, "/search?q=lantern"), "Lantern Public", "Lantern Teen", `data-screen="E8"`, ">Series 2<")
		has(t, "anonymous explore", get(t, anon, "/explore"), "Lantern Public", "Lantern Teen", `data-screen="E6"`)

		for _, u := range []*auth.User{v.member, v.projectMember} {
			c := ta.Login(t, u.Username)
			for _, path := range []string{"/explore", "/search?q=lantern"} {
				body := get(t, c, path)
				has(t, "member "+path, body, "Lantern Public", "Lantern Members", "Lantern Mature")
				// Unlisted and private series are never listed, even for the project member.
				hasNot(t, "member "+path, body, always...)
			}
		}

		// Personal and private servers send anonymous visitors to sign in.
		for _, mode := range []string{settings.ModePersonal, settings.ModePrivate} {
			f.SetMode(mode)
			for _, path := range []string{"/explore", "/search?q=lantern"} {
				resp := anon.Get(path)
				if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
					t.Errorf("%s anonymous %s: %d %s", mode, path, resp.StatusCode, resp.Header.Get("Location"))
				}
			}
			has(t, mode+" member", get(t, ta.Login(t, v.member.Username), "/search?q=lantern"), "Lantern Public", "Lantern Members")
		}
	})
}

func TestHandDrawnFilter(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		human := map[string]string{"script": "human", "art": "human", "lettering": "human"}
		for title, label := range map[string]map[string]string{
			"Paper Crows Ink":       human,
			"Paper Crows Assisted":  {"script": "human", "art": "ai_assisted", "lettering": "human"},
			"Paper Crows Generated": {"script": "ai", "art": "ai", "lettering": "ai"},
			"Paper Crows Unlabeled": {"script": "human", "art": "human"},
		} {
			s := f.Series(publishtest.SeriesSpec{Title: title, AILabel: label, Genres: []string{"action"}})
			f.Publish(s, publishtest.PublicationSpec{})
		}
		anon := ta.NewClient()
		for _, path := range []string{"/explore?hand=1", "/explore?hand=1&genre=action", "/search?q=paper&hand=1", "/search?hand=1"} {
			body := get(t, anon, path)
			// On Explore the filters apply to the shelves below them; the
			// featured series and rankings above stay server-wide.
			if i := strings.Index(body, "mv-explore__filters"); i >= 0 {
				body = body[i:]
			}
			has(t, path, body, "Paper Crows Ink")
			hasNot(t, path, body, "Paper Crows Assisted", "Paper Crows Generated", "Paper Crows Unlabeled")
		}
		body := get(t, anon, "/explore?hand=1")
		has(t, "explore toggle", body, `name="hand" value="1" checked`, "Art: hand-drawn")
		all := get(t, anon, "/search?q=paper")
		has(t, "unfiltered", all, "Paper Crows Ink", "Paper Crows Assisted", "Paper Crows Generated", "Paper Crows Unlabeled")
		art := get(t, anon, "/search?q=paper&art=ai_assisted")
		has(t, "art filter", art, "Paper Crows Assisted")
		hasNot(t, "art filter", art, "Paper Crows Ink", "Paper Crows Generated")
	})
}

func TestSearchFiltersAndSources(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		owner := f.User(auth.RoleCreator)
		name := "Mika Sato"
		if _, err := ta.Deps.Auth.UpdateProfile(ctx, owner.ID, auth.ProfileUpdate{DisplayName: &name}); err != nil {
			t.Fatal(err)
		}
		mk := func(spec publishtest.SeriesSpec) publishtest.Series {
			s := f.Series(spec)
			f.Publish(s, publishtest.PublicationSpec{})
			return s
		}
		mk(publishtest.SeriesSpec{Title: "Iron Koi", Synopsis: "A clockwork carp opens a noodle stand.", Genres: []string{"comedy"},
			Tags: []string{"food", "clockwork"}, Format: "webtoon", Status: publish.SeriesCompleted, Owner: owner})
		mk(publishtest.SeriesSpec{Title: "Salt & Static", Synopsis: "A radio that only plays the sea.", Genres: []string{"scifi"},
			Tags: []string{"radio"}, Languages: []string{"en", "ja"}, AgeRating: publish.RatingTeen})
		mk(publishtest.SeriesSpec{Title: "灯台守の夜", Synopsis: "東京の灯台守が海を見張る。", OriginalLang: "ja", Genres: []string{"drama"}})
		anon := ta.NewClient()
		cases := []struct {
			path    string
			want    []string
			notWant []string
		}{
			{"/search?q=noodle", []string{"Iron Koi"}, []string{"Salt &amp; Static"}},
			{"/search?q=clock", []string{"Iron Koi"}, nil},                  // prefix match on a tag
			{"/search?q=mika+sato", []string{"Iron Koi"}, []string{"Salt"}}, // creator name
			{"/search?q=灯台守", []string{"灯台守の夜"}, []string{"Iron Koi"}},      // CJK substring
			{"/search?q=" + url.QueryEscape("東京"), []string{"灯台守の夜"}, nil},
			{"/search?q=" + url.QueryEscape("見張る"), []string{"灯台守の夜"}, nil}, // CJK inside a run
			{"/search?format=webtoon", []string{"Iron Koi"}, []string{"Salt"}},
			{"/search?status=completed", []string{"Iron Koi"}, []string{"Salt"}},
			{"/search?ja=1", []string{"Salt &amp; Static", "灯台守の夜"}, []string{"Iron Koi"}},
			{"/search?lang=ja", []string{"Salt &amp; Static"}, []string{"Iron Koi"}},
			{"/search?rating=teen", []string{"Salt &amp; Static"}, []string{"Iron Koi"}},
			{"/search?genre=scifi", []string{"Salt &amp; Static"}, []string{"Iron Koi"}},
			{"/search?tag=food", []string{"Iron Koi"}, []string{"Salt"}},
			{"/search?q=" + url.QueryEscape(`" OR 1=1 --`), nil, []string{"Iron Koi", "Salt"}},
			{"/search?q=" + url.QueryEscape(`'); DROP TABLE series; --`), nil, []string{"Iron Koi"}},
			{"/search?q=" + url.QueryEscape(`title:* NEAR( "x"* ^ & | ! <->`), nil, nil},
			{"/search?q=" + url.QueryEscape(strings.Repeat("koi ", 400)), []string{"Iron Koi"}, nil},
			{"/search?q=" + url.QueryEscape("%_\\"), []string{"Iron Koi"}, nil}, // no words: browse
			{"/search?format=../../etc&status=%27&rating=x&lang=zz&art=y&genre=%3Cscript%3E&page=-4", []string{"Iron Koi", "Salt &amp; Static"}, []string{"<script>"}},
		}
		for _, c := range cases {
			body := get(t, anon, c.path)
			has(t, c.path, body, c.want...)
			hasNot(t, c.path, body, c.notWant...)
		}
		// Creators and tags tabs.
		has(t, "creators", get(t, anon, "/search?tab=creators&q=mika"), "Mika Sato", "1 series")
		has(t, "tags", get(t, anon, "/search?tab=tags&q=koi"), "food", "clockwork")
		has(t, "empty", get(t, anon, "/search?q=zzzzqqq"), "Nothing matches “zzzzqqq”", "SHIIN")
		// The full-text index follows title changes made by the publish module.
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE series SET title = ?, updated_at = ? WHERE title = ?`, "Copper Koi", db.Now(), "Iron Koi"); err != nil {
			t.Fatal(err)
		}
		has(t, "renamed", get(t, anon, "/search?q=copper"), "Copper Koi")
	})
}

func TestSearchPaging(t *testing.T) {
	ta := testutil.NewTestApp(t)
	f := publishtest.New(t, ta)
	f.SetMode(settings.ModePublic)
	for i := 0; i < 23; i++ {
		s := f.Series(publishtest.SeriesSpec{Synopsis: "Orchard tale."})
		f.Publish(s, publishtest.PublicationSpec{})
	}
	anon := ta.NewClient()
	first := get(t, anon, "/search?q=orchard")
	has(t, "page 1", first, ">Series 23<", "page=2", "Next")
	if n := strings.Count(first, `class="mv-result"`); n != 20 {
		t.Errorf("page 1 rows = %d", n)
	}
	second := get(t, anon, "/search?q=orchard&page=2")
	if n := strings.Count(second, `class="mv-result"`); n != 3 {
		t.Errorf("page 2 rows = %d", n)
	}
	has(t, "page 2", second, "Previous")
}
