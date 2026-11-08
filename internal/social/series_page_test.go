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
)

func mustContain(t *testing.T, what, html string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(html, p) {
			t.Fatalf("%s: missing %q", what, p)
		}
	}
}

func mustNotContain(t *testing.T, what, html string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(html, p) {
			t.Fatalf("%s: unexpected %q", what, p)
		}
	}
}

func TestSeriesPageAnonymousNoJS(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		owner := e.f.User(auth.RoleCreator)
		s := e.f.Series(publishtest.SeriesSpec{Owner: owner, Title: "Tidebreak Courier", Synopsis: "A courier crosses a flooded city.",
			Genres: []string{"adventure", "slice_of_life"}, AgeRating: publish.RatingTeen, Licence: "cc-by-sa", FollowerCount: 12400,
			AILabel:      map[string]string{"art": "ai_assisted", "script": "human", "lettering": "ai"},
			SupportLinks: []publish.SupportLink{{Label: "Rin on Ko-fi", URL: "https://ko-fi.com/rin"}, {Label: "bad", URL: "javascript:alert(1)"}},
			Languages:    []string{"en", "ja"}, Cover: true})
		e.f.Publish(s, publishtest.PublicationSpec{Title: "The Last Crossing", Pages: 2})
		e.f.Publish(s, publishtest.PublicationSpec{Title: "Salt Road", Pages: 3})
		e.f.Publish(s, publishtest.PublicationSpec{Title: "Hidden for now", Status: publish.PublicationScheduled})
		e.f.Publish(s, publishtest.PublicationSpec{Title: "Members only", Visibility: publish.VisibilityMembers})
		e.f.Publish(s, publishtest.PublicationSpec{Lang: "ja", Number: 1, Title: "最後の渡し"})

		r := e.ta.NewClient().Get("/s/" + s.Slug)
		wantStatus(t, "anonymous E5", r, http.StatusOK)
		html := string(r.Body)
		mustContain(t, "E5", html,
			"Tidebreak Courier", "A courier crosses a flooded city.",
			"Ongoing", "Manga (RTL)", "Teen 13+", "CC BY-SA 4.0",
			"adventure", "slice of life", "AI-assisted art", "Human script", "AI lettering",
			"12.4k", "The Last Crossing", "Salt Road", "Newest", "Oldest", "Read ch. 1",
			"Support the creators", `href="https://ko-fi.com/rin"`, "Rin on Ko-fi", `rel="noopener noreferrer nofollow ugc"`,
			"Comments · 0", "Log in to comment", `href="/login?next=%2Fs%2F`+s.Slug,
			`href="/s/`+s.Slug+`?lang=ja"`, "/media/")
		mustNotContain(t, "E5", html, "Hidden for now", "Members only", "javascript:alert", "最後の渡し", `action="/s/`+s.Slug+`/comments"`)
		// Newest first by default; Oldest flips the list.
		if strings.Index(html, "Salt Road") > strings.Index(html, "The Last Crossing") {
			t.Fatal("chapters are not newest first")
		}
		r = e.ta.NewClient().Get("/s/" + s.Slug + "?sort=oldest")
		if html := string(r.Body); strings.Index(html, "Salt Road") < strings.Index(html, "The Last Crossing") {
			t.Fatal("oldest order not applied")
		}
		// The language switch lists the Japanese chapters.
		r = e.ta.NewClient().Get("/s/" + s.Slug + "?lang=ja")
		mustContain(t, "E5 ja", string(r.Body), "最後の渡し")
		mustNotContain(t, "E5 ja", string(r.Body), "Salt Road")
		// Only public series pages may be indexed; unlisted links must not end up in search engines.
		if got := r.Header.Get("X-Robots-Tag"); got != "" {
			t.Fatalf("public E5 X-Robots-Tag = %q", got)
		}
		hidden := e.f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityUnlisted})
		e.f.Publish(hidden, publishtest.PublicationSpec{})
		if got := e.ta.NewClient().Get("/s/" + hidden.Slug).Header.Get("X-Robots-Tag"); got != "noindex" {
			t.Fatalf("unlisted E5 X-Robots-Tag = %q, want noindex", got)
		}
		// The page is complete without JavaScript: no island and no hx- attributes.
		mustNotContain(t, "E5 no-JS", html, "data-island", "hx-post", "hx-get")

		// A member sees the plain POST forms.
		_, c := e.user(auth.RoleReader)
		html = string(c.Get("/s/" + s.Slug).Body)
		mustContain(t, "member E5", html, `<form method="post" action="/s/`+s.Slug+`/follow"`,
			`action="/s/`+s.Slug+`/comments"`, `name="body"`, "Members only")
		// Members start an offline download in the reader; visitors cannot download (ADR 0012 §7).
		mustContain(t, "member E5 download", html, `class="mv-series__dl" href="/s/`+s.Slug+`/`)
		mustContain(t, "member E5 download", html, `?download=1"`)
		mustNotContain(t, "anonymous E5 download", string(e.ta.NewClient().Get("/s/"+s.Slug).Body), "?download=1")
	})
}

func TestSeriesPageAccess(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		owner, ownerClient := e.user(auth.RoleCreator)
		_, member := e.user(auth.RoleReader)
		anon := e.ta.NewClient()
		pub := e.f.Series(publishtest.SeriesSpec{Owner: owner})
		unlisted := e.f.Series(publishtest.SeriesSpec{Owner: owner, Visibility: publish.VisibilityUnlisted})
		members := e.f.Series(publishtest.SeriesSpec{Owner: owner, Visibility: publish.VisibilityMembers})
		private := e.f.Series(publishtest.SeriesSpec{Owner: owner, Visibility: publish.VisibilityPrivate})
		mature := e.f.Series(publishtest.SeriesSpec{Owner: owner, AgeRating: publish.RatingMature})
		adult := e.f.Series(publishtest.SeriesSpec{Owner: owner, AgeRating: publish.RatingAdult})
		draft := e.f.Series(publishtest.SeriesSpec{Owner: owner, NeverPublished: true, Status: publish.SeriesDraft})

		for _, tc := range []struct {
			name   string
			slug   string
			anon   int
			member int
			owner  int
		}{
			{"public", pub.Slug, 200, 200, 200},
			{"unlisted", unlisted.Slug, 200, 200, 200},
			{"members", members.Slug, 404, 200, 200},
			{"private", private.Slug, 404, 404, 200},
			{"mature", mature.Slug, 404, 200, 200},
			{"draft", draft.Slug, 404, 404, 200},
			{"unknown", "no-such-series", 404, 404, 404},
		} {
			wantStatus(t, tc.name+" anonymous", anon.Get("/s/"+tc.slug), tc.anon)
			wantStatus(t, tc.name+" member", member.Get("/s/"+tc.slug), tc.member)
			wantStatus(t, tc.name+" owner", ownerClient.Get("/s/"+tc.slug), tc.owner)
		}

		// Mature and adult: a confirmation step until age_confirmations has a row.
		for _, s := range []publishtest.Series{mature, adult} {
			u, c := e.user(auth.RoleReader)
			html := string(c.Get("/s/" + s.Slug).Body)
			mustContain(t, "age gate", html, `action="/s/`+s.Slug+`/confirm-age"`, `name="next" value="/s/`+s.Slug+`"`)
			mustNotContain(t, "age gate", html, "mv-series__rows", `action="/s/`+s.Slug+`/follow"`)
			e.confirmAge(u.ID, s.ID)
			html = string(c.Get("/s/" + s.Slug).Body)
			mustNotContain(t, "after confirming", html, "/confirm-age")
			mustContain(t, "after confirming", html, `action="/s/`+s.Slug+`/follow"`)
		}
		mustContain(t, "adult gate", string(member.Get("/s/"+adult.Slug).Body), "18")
		// The owner previews without confirming.
		mustNotContain(t, "owner", string(ownerClient.Get("/s/"+mature.Slug).Body), "/confirm-age")

		// Personal and private servers send anonymous visitors to sign in, existing series or not.
		for _, mode := range []string{settings.ModePersonal, settings.ModePrivate} {
			e.f.SetMode(mode)
			for _, slug := range []string{pub.Slug, "no-such-series"} {
				r := anon.Get("/s/" + slug)
				if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), "/login?next=") {
					t.Fatalf("%s anonymous %s = %d %q", mode, slug, r.StatusCode, r.Header.Get("Location"))
				}
			}
			wantStatus(t, mode+" member", member.Get("/s/"+pub.Slug), http.StatusOK)
		}
	})
}

func TestSeriesPageFollowAndCommentForms(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		owner, ownerClient := e.user(auth.RoleCreator)
		s := e.f.Series(publishtest.SeriesSpec{Owner: owner})
		e.f.Publish(s, publishtest.PublicationSpec{})
		u, c := e.user(auth.RoleReader)
		_, other := e.user(auth.RoleReader)
		path := "/s/" + s.Slug

		r := postForm(c, path+"/follow", url.Values{})
		wantStatus(t, "follow form", r, http.StatusSeeOther)
		if loc := r.Header.Get("Location"); loc != path {
			t.Fatalf("follow redirect = %q", loc)
		}
		html := string(c.Get(path).Body)
		mustContain(t, "following", html, `action="`+path+`/unfollow"`, "Following")
		if e.followerCount(s.ID) != 1 {
			t.Fatal("follower_count not incremented")
		}
		wantStatus(t, "unfollow form", postForm(c, path+"/unfollow", url.Values{}), http.StatusSeeOther)
		if e.followerCount(s.ID) != 0 {
			t.Fatal("follower_count not decremented")
		}
		// Explore and search post with a local next to return to; anything else falls back to the series.
		for next, want := range map[string]string{"/explore?genre=mystery": "/explore?genre=mystery", "https://evil.example/": path, "//evil.example": path} {
			r := postForm(c, path+"/follow", url.Values{"next": {next}})
			if loc := r.Header.Get("Location"); loc != want {
				t.Fatalf("follow next %q redirect = %q, want %q", next, loc, want)
			}
			if loc := postForm(c, path+"/unfollow", url.Values{"next": {next}}).Header.Get("Location"); loc != want {
				t.Fatalf("unfollow next %q redirect = %q, want %q", next, loc, want)
			}
		}

		// Post, fail validation (draft kept), edit, report and hide through the no-JS forms.
		r = postForm(c, path+"/comments", url.Values{"body": {"  "}})
		wantStatus(t, "empty comment form", r, http.StatusUnprocessableEntity)
		mustContain(t, "empty comment form", string(r.Body), "Write something first.")
		wantStatus(t, "comment form", postForm(c, path+"/comments", url.Values{"body": {"Pip stole the ferry schedule"}}), http.StatusSeeOther)
		var id string
		if err := e.ta.Deps.DB.Get(context.Background(), &id, `SELECT id FROM comments WHERE author_id = ?`, u.ID); err != nil {
			t.Fatal(err)
		}
		html = string(c.Get(path).Body)
		mustContain(t, "comment shown", html, "Pip stole the ferry schedule", "Comments · 1", `id="comment-`+id+`"`,
			`edit=`+id, `action="`+path+`/comments/`+id+`/delete"`)
		mustNotContain(t, "author menu", html, `/comments/`+id+`/hide"`, `report=`+id)
		html = string(c.Get(path + "?edit=" + id).Body)
		mustContain(t, "edit form", html, `action="`+path+`/comments/`+id+`/edit"`)
		wantStatus(t, "edit form post", postForm(c, path+"/comments/"+id+"/edit", url.Values{"body": {"Pip stole it twice"}}), http.StatusSeeOther)
		mustContain(t, "edited", string(c.Get(path).Body), "Pip stole it twice", "edited")
		wantStatus(t, "other edits", postForm(other, path+"/comments/"+id+"/edit", url.Values{"body": {"x"}}), http.StatusForbidden)

		otherHTML := string(other.Get(path + "?report=" + id).Body)
		mustContain(t, "report form", otherHTML, `action="`+path+`/comments/`+id+`/report"`, `name="reason"`, "Wrong AI label")
		wantStatus(t, "report form post", postForm(other, path+"/comments/"+id+"/report", url.Values{"reason": {"spam"}}), http.StatusSeeOther)
		if n := e.count(`SELECT COUNT(*) FROM reports WHERE target_kind = 'comment' AND target_id = ?`, id); n != 1 {
			t.Fatalf("reports = %d", n)
		}

		mustContain(t, "owner menu", string(ownerClient.Get(path).Body), `action="`+path+`/comments/`+id+`/hide"`)
		wantStatus(t, "other hides", postForm(other, path+"/comments/"+id+"/hide", url.Values{}), http.StatusForbidden)
		wantStatus(t, "owner hides", postForm(ownerClient, path+"/comments/"+id+"/hide", url.Values{}), http.StatusSeeOther)
		mustNotContain(t, "hidden for others", string(other.Get(path).Body), "Pip stole it twice")
		mustContain(t, "hidden for author", string(c.Get(path).Body), "Pip stole it twice", "Hidden")
		wantStatus(t, "owner unhides", postForm(ownerClient, path+"/comments/"+id+"/unhide", url.Values{}), http.StatusSeeOther)
		wantStatus(t, "author deletes", postForm(c, path+"/comments/"+id+"/delete", url.Values{}), http.StatusSeeOther)
		mustContain(t, "deleted", string(other.Get(path).Body), "Comments · 0")
	})
}

func TestSeriesFormsNeedAccessSignInAndBudget(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		private := e.f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityPrivate})
		public := e.f.Series(publishtest.SeriesSpec{})
		_, c := e.user(auth.RoleReader)
		for _, slug := range []string{private.Slug, "no-such-series"} {
			wantStatus(t, "follow form unreadable", postForm(c, "/s/"+slug+"/follow", url.Values{}), http.StatusNotFound)
			wantStatus(t, "comment form unreadable", postForm(c, "/s/"+slug+"/comments", url.Values{"body": {"hi"}}), http.StatusNotFound)
		}
		if n := e.count(`SELECT COUNT(*) FROM follows`) + e.count(`SELECT COUNT(*) FROM comments`); n != 0 {
			t.Fatalf("rows written for unreadable series: %d", n)
		}
		anon := e.ta.NewClient()
		wantStatus(t, "anonymous follow form", postForm(anon, "/s/"+public.Slug+"/follow", url.Values{}), http.StatusUnauthorized)
		wantStatus(t, "anonymous comment form", postForm(anon, "/s/"+public.Slug+"/comments", url.Values{"body": {"hi"}}), http.StatusUnauthorized)

		// The no-JS comment form shares the API's budget and keeps the draft when limited.
		for i := range 5 {
			postComment(t, c, seriesComment(public, "comment "+string(rune('a'+i))), http.StatusCreated)
		}
		r := postForm(c, "/s/"+public.Slug+"/comments", url.Values{"body": {"draft survives"}})
		wantStatus(t, "limited form comment", r, http.StatusTooManyRequests)
		mustContain(t, "limited form comment", string(r.Body), "draft survives")
	})
}

func TestCommentBodiesArePlainText(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		_, c := e.user(auth.RoleReader)
		evil := `<script>alert("x")</script><img src=x onerror=alert(1)> & "quotes"` + "\x00\x07"
		got := postComment(t, c, seriesComment(s, evil), http.StatusCreated)
		if got.Body != `<script>alert("x")</script><img src=x onerror=alert(1)> & "quotes"` {
			t.Fatalf("stored body = %q", got.Body)
		}
		page := c.Get("/s/" + s.Slug)
		wantStatus(t, "series page", page, http.StatusOK)
		html := string(page.Body)
		if strings.Contains(html, `<script>alert("x")</script>`) || strings.Contains(html, `<img src=x onerror`) {
			t.Fatal("comment body rendered as HTML")
		}
		if !strings.Contains(html, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`) {
			t.Fatalf("escaped body missing: %s", truncate(html[strings.Index(html, "mv-series__ctext"):]))
		}
		// JSON keeps the text and the server never sends it as HTML.
		r := c.Get(commentsURL(s.ID, "series", s.ID))
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("content type %q", ct)
		}
	})
}

// E5 credits every creator in credit order and shows the release note in place
// of the status ("by Jerry Okafor & Jun Sato · Weekly, Saturday · …"); F1 cards
// show the release note too.
func TestSeriesPageCreditsCreatorsAndReleaseNote(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		owner, _ := e.user(auth.RoleCreator)
		letterer, _ := e.user(auth.RoleCreator)
		s := e.f.Series(publishtest.SeriesSpec{Owner: owner, Schedule: "Weekly, Saturday", OwnerCredit: "Jerry Okafor",
			Creators: []publish.Creator{{UserID: letterer.ID, Role: "letterer", CreditName: "Jun Sato"}}})
		e.f.Publish(s, publishtest.PublicationSpec{})
		// Browsers collapse the template's whitespace runs, so compare collapsed text.
		html := strings.Join(strings.Fields(string(e.ta.NewClient().Get("/s/"+s.Slug).Body)), " ")
		mustContain(t, "E5 credits", html,
			`by <a class="mv-series__creator" href="/u/`+owner.Username+`">Jerry Okafor</a> &amp; <a class="mv-series__creator" href="/u/`+letterer.Username+`">Jun Sato</a> · Weekly, Saturday · Manga (RTL)`)
		mustNotContain(t, "E5 status replaced by the release note", html, "Ongoing · ")
		profile := string(e.ta.NewClient().Get("/u/" + owner.Username).Body)
		mustContain(t, "F1 card", profile, "Weekly, Saturday · ")
	})
}
