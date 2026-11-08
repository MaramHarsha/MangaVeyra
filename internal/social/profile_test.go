package social_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestProfilePage(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		creator, creatorClient := e.user(auth.RoleCreator)
		if _, err := e.ta.Deps.Auth.UpdateProfile(context.Background(), creator.ID, auth.ProfileUpdate{
			DisplayName: ptr("Jerry Okafor"), Bio: ptr("writes the script, fixes the hands"),
			Links: &[]string{"https://ko-fi.com/jerry", "javascript:alert(1)"}}); err != nil {
			t.Fatal(err)
		}
		listed := e.f.Series(publishtest.SeriesSpec{Owner: creator, Title: "Glass Orchard", Status: publish.SeriesCompleted})
		e.f.Publish(listed, publishtest.PublicationSpec{Title: "Orchard One"})
		e.f.Publish(listed, publishtest.PublicationSpec{Title: "Orchard Two"})
		unlisted := e.f.Series(publishtest.SeriesSpec{Owner: creator, Title: "Secret Unlisted", Visibility: publish.VisibilityUnlisted})
		e.f.Publish(unlisted, publishtest.PublicationSpec{})
		e.f.ReadableSeries(publishtest.SeriesSpec{Owner: creator, Title: "Members Shelf", Visibility: publish.VisibilityMembers})
		e.f.ReadableSeries(publishtest.SeriesSpec{Owner: creator, Title: "Private Draft", Visibility: publish.VisibilityPrivate})
		e.f.ReadableSeries(publishtest.SeriesSpec{Owner: creator, Title: "Mature Tale", AgeRating: publish.RatingMature})

		anon := e.ta.NewClient()
		r := anon.Get("/u/" + creator.Username)
		wantStatus(t, "anonymous F1", r, http.StatusOK)
		html := string(r.Body)
		mustContain(t, "F1", html, "Jerry Okafor", "writes the script, fixes the hands", "Glass Orchard", "Complete · 2 ch.",
			`href="https://ko-fi.com/jerry"`, "ko-fi.com", "-RANK CREATOR · LV. ", "Series", "Chapters", "About",
			`href="/login?next=%2Fu%2F`+creator.Username+`"`)
		mustNotContain(t, "F1", html, "Secret Unlisted", "Members Shelf", "Private Draft", "Mature Tale", "javascript:alert",
			"data-island", "hx-post")

		// Members also see members-only and mature series, never unlisted or private ones.
		_, member := e.user(auth.RoleReader)
		html = string(member.Get("/u/" + creator.Username).Body)
		mustContain(t, "member F1", html, "Glass Orchard", "Members Shelf", "Mature Tale", `action="/u/`+creator.Username+`/follow"`)
		mustNotContain(t, "member F1", html, "Secret Unlisted", "Private Draft")
		html = string(member.Get("/u/" + creator.Username + "?tab=chapters").Body)
		mustContain(t, "chapters tab", html, "Orchard Two", "Orchard One", "Glass Orchard")
		mustContain(t, "about tab", string(member.Get("/u/"+creator.Username+"?tab=about").Body), "@"+creator.Username, "Joined ")

		// The creator's own profile offers Edit profile instead of Follow.
		html = string(creatorClient.Get("/u/" + creator.Username).Body)
		mustContain(t, "own F1", html, "Edit profile")
		mustNotContain(t, "own F1", html, `/follow"`)

		// No readable series: the designed empty state.
		reader, _ := e.user(auth.RoleReader)
		html = string(anon.Get("/u/" + reader.Username).Body)
		mustContain(t, "empty F1", html, "No series yet", "シーン…")

		// Unknown and suspended accounts are not found.
		wantStatus(t, "unknown", anon.Get("/u/no-such-person"), http.StatusNotFound)
		if err := e.ta.Deps.Auth.SetStatus(context.Background(), reader.ID, auth.StatusSuspended, "test"); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, "suspended", member.Get("/u/"+reader.Username), http.StatusNotFound)

		// Personal servers send anonymous visitors to sign in.
		e.f.SetMode(settings.ModePersonal)
		r = anon.Get("/u/" + creator.Username)
		if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), "/login?next=") {
			t.Fatalf("personal anonymous = %d %q", r.StatusCode, r.Header.Get("Location"))
		}
	})
}

func TestPagesInJapanese(t *testing.T) {
	ta := testutil.NewTestApp(t)
	e := &env{t: t, ta: ta, f: publishtest.New(t, ta)}
	e.f.SetMode(settings.ModePublic)
	s := e.f.Series(publishtest.SeriesSpec{})
	e.f.Publish(s, publishtest.PublicationSpec{})
	c := ta.NewClient()
	c.Header.Set("Accept-Language", "ja")
	mustContain(t, "E5 ja", string(c.Get("/s/"+s.Slug).Body), "第1話を読む", "コメント · 0", "新しい順")
	mustContain(t, "F1 ja", string(c.Get("/u/"+s.Owner.Username).Body), "シリーズ", "フォロー")
}

func TestProfileFollowForms(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		creator := e.f.User(auth.RoleCreator)
		me, c := e.user(auth.RoleReader)
		path := "/u/" + creator.Username
		wantStatus(t, "follow form", postForm(c, path+"/follow", url.Values{}), http.StatusSeeOther)
		html := string(c.Get(path).Body)
		mustContain(t, "following", html, `action="`+path+`/unfollow"`, "Following")
		if n := e.count(`SELECT COUNT(*) FROM user_follows WHERE follower_id = ? AND followee_id = ?`, me.ID, creator.ID); n != 1 {
			t.Fatalf("user follows = %d", n)
		}
		wantStatus(t, "unfollow form", postForm(c, path+"/unfollow", url.Values{}), http.StatusSeeOther)
		if n := e.count(`SELECT COUNT(*) FROM user_follows WHERE followee_id = ?`, creator.ID); n != 0 {
			t.Fatalf("user follows after unfollow = %d", n)
		}
		wantStatus(t, "self follow form", postForm(c, "/u/"+me.Username+"/follow", url.Values{}), http.StatusUnprocessableEntity)
		wantStatus(t, "anonymous follow form", postForm(e.ta.NewClient(), path+"/follow", url.Values{}), http.StatusUnauthorized)
	})
}

func ptr[T any](v T) *T { return &v }
