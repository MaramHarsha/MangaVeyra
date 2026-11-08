package admin_test

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// With allow_mature / allow_adult off, such series are readable only inside
// their project (publish.Service answers ErrNotFound to everyone else, admins
// included); turning the switch on in G5 shows them again.
func TestContentRulesReading(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		e.f.SetContent(func(c *settings.Content) { c.AllowMature, c.AllowAdult = false, false })
		mature := e.f.ReadableSeries(publishtest.SeriesSpec{AgeRating: publish.RatingMature})
		adult := e.f.ReadableSeries(publishtest.SeriesSpec{AgeRating: publish.RatingAdult})
		teen := e.f.ReadableSeries(publishtest.SeriesSpec{AgeRating: publish.RatingTeen})
		member, _ := e.user(auth.RoleReader)
		svc := e.pub()
		readable := func(v publish.Viewer, s publishtest.Series) bool {
			_, err := svc.SeriesByID(ctx, v, s.ID)
			if err != nil && !errors.Is(err, publish.ErrNotFound) {
				t.Fatal(err)
			}
			return err == nil
		}
		listed := func(v publish.Viewer, s publishtest.Series) bool {
			list, _, err := svc.ListSeries(ctx, v, publish.SeriesFilter{})
			if err != nil {
				t.Fatal(err)
			}
			return slices.ContainsFunc(list, func(x publish.Series) bool { return x.ID == s.ID })
		}
		admin := publish.Viewer{UserID: e.ta.Admin.ID, Admin: true}
		for _, s := range []publishtest.Series{mature, adult} {
			for who, v := range map[string]publish.Viewer{"member": publishtest.Viewer(member), "admin": admin, "anonymous": {}} {
				if readable(v, s) || listed(v, s) {
					t.Fatalf("%s sees a %s series the rules do not allow", who, s.Title)
				}
				if _, _, err := svc.Publication(ctx, v, s.ID, 1, ""); !errors.Is(err, publish.ErrNotFound) {
					t.Fatalf("%s reads a chapter of a disallowed series: %v", who, err)
				}
			}
			if !readable(publishtest.Viewer(s.Owner), s) {
				t.Fatal("the project cannot preview its own series")
			}
		}
		if !readable(publishtest.Viewer(member), teen) || !listed(publishtest.Viewer(member), teen) {
			t.Fatal("teen series are always allowed")
		}
		// The owner sees why on E5 (after the age step every reader passes).
		e.exec(`INSERT INTO age_confirmations (user_id, series_id, confirmed_at) VALUES (?, ?, ?)`, mature.Owner.ID, mature.ID, db.Now())
		owner := e.ta.Login(t, mature.Owner.Username)
		wantBody(t, "E5 notice", owner.Get("/s/"+mature.Slug), "This server no longer allows")

		wantSaved(t, "allow mature", postForm(e.admin, "/admin/rules", url.Values{"part": {"content"}, "allow_mature": {"on"}, "licence_guard": {"on"}}), "/admin/rules")
		if !readable(publishtest.Viewer(member), mature) || !listed(publishtest.Viewer(member), mature) {
			t.Fatal("allowing mature series did not show the series")
		}
		if readable(publishtest.Viewer(member), adult) {
			t.Fatal("allowing mature series showed an adult one")
		}
		// Anonymous visitors never see mature series (ADR 0012 §1).
		if readable(publish.Viewer{}, mature) {
			t.Fatal("anonymous visitor reads a mature series")
		}
		wantNoBody(t, "E5 without notice", owner.Get("/s/"+mature.Slug), "no longer allows")
	})
}

// E1 and series settings offer only the ratings the server allows, refuse the
// others and blocked words, and start new series at the default rating.
func TestContentRulesWriting(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		e.f.SetContent(func(c *settings.Content) {
			c.AllowMature, c.AllowAdult, c.DefaultAgeRating, c.BlockedWords = false, false, "teen", []string{"Gloomweed"}
		})
		owner, ownerC := e.user(auth.RoleCreator)
		ss := core.MustUse[studio.Service](e.ta.Deps)
		actor, err := ss.ActorFromUser(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		project, err := ss.CreateProject(ctx, actor, studio.NewProject{Title: "Salt & Static", Logline: "Radio ghosts at low tide.", Format: "manga_rtl", Language: "en"})
		if err != nil {
			t.Fatal(err)
		}
		chapter, err := ss.CreateChapter(ctx, actor, project.ID, "Dead Air")
		if err != nil {
			t.Fatal(err)
		}
		seriesPath := "/studio/p/" + project.ID + "/series"
		page := ownerC.Get(seriesPath)
		wantStatus(t, "series settings", page, http.StatusOK)
		wantBody(t, "series settings", page, `<option value="teen" selected>`)
		wantNoBody(t, "series settings", page, `<option value="mature"`, `<option value="adult"`)

		form := url.Values{"slug": {"salt-static"}, "title": {"Salt & Static"}, "synopsis": {"Radio ghosts at low tide."}, "format": {"manga_rtl"},
			"age_rating": {"mature"}, "licence": {"all_rights"}, "visibility": {"public"}, "status": {"ongoing"}, "original_lang": {"en"}}
		r := postForm(ownerC, seriesPath, form)
		wantStatus(t, "mature series", r, http.StatusUnprocessableEntity)
		wantBody(t, "mature series", r, "This server does not allow this age rating.")
		form.Set("age_rating", "teen")
		form.Set("title", "The gloomweed run")
		r = postForm(ownerC, seriesPath, form)
		wantStatus(t, "blocked title", r, http.StatusUnprocessableEntity)
		wantBody(t, "blocked title", r, "This contains a word this server does not allow.")
		form.Set("title", "Salt & Static")
		form.Set("synopsis", "A GLOOMWEED story.")
		wantStatus(t, "blocked synopsis", postForm(ownerC, seriesPath, form), http.StatusUnprocessableEntity)
		form.Set("synopsis", "Gloomweeds grow on the pier.") // not a whole word
		wantStatus(t, "allowed series", postForm(ownerC, seriesPath, form), http.StatusSeeOther)

		// E1 offers the same ratings and refuses a disallowed one.
		e1 := "/studio/p/" + project.ID + "/chapters/" + itoa(chapter.Number) + "/publish"
		page = ownerC.Get(e1)
		wantStatus(t, "E1", page, http.StatusOK)
		wantNoBody(t, "E1", page, `<option value="mature"`, `<option value="adult"`)
		hidden := func(name string) string {
			m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(string(page.Body))
			if m == nil {
				t.Fatalf("E1 has no %s", name)
			}
			return html.UnescapeString(m[1])
		}
		pub := url.Values{"page_lang": {"en"}, "revision": {hidden("revision")}, "series_revision": {hidden("series_revision")},
			"lang": {"en"}, "title": {"Dead Air"}, "visibility": {"public"}, "age_rating": {"adult"}, "licence": {"all_rights"},
			"when": {"now"}, "ai_script": {"human"}, "ai_art": {"human"}, "ai_lettering": {"human"}, "synopsis": {"gloomweed at dawn"}}
		r = postForm(ownerC, e1, pub)
		wantStatus(t, "E1 adult", r, http.StatusUnprocessableEntity)
		wantBody(t, "E1 adult", r, "This server does not allow this age rating.", "This contains a word this server does not allow.")

		// Allowing the rating in G5 makes it a choice again.
		wantSaved(t, "allow adult", postForm(e.admin, "/admin/rules", url.Values{"part": {"content"}, "allow_adult": {"on"}, "default_age_rating": {"teen"}}), "/admin/rules")
		wantBody(t, "series settings", ownerC.Get(seriesPath), `<option value="adult"`)
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

// The Content card saves the switches; a default the rules no longer allow
// falls back to all ages, AI labels stay required, NSFW is Soon.
func TestRulesForm(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		wantSaved(t, "rules", postForm(e.admin, "/admin/rules", url.Values{"part": {"content"}, "allow_mature": {"on"}, "default_age_rating": {"mature"}}), "/admin/rules")
		c := e.instance().Content
		if !c.AllowMature || c.AllowAdult || c.DefaultAgeRating != "mature" || c.LicenceGuard || !c.RequireAILabels {
			t.Fatalf("content %+v", c)
		}
		if e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'admin.settings.content' AND actor_id = ?`, e.ta.Admin.ID) != 1 {
			t.Fatal("content rules not audited")
		}
		wantSaved(t, "rules", postForm(e.admin, "/admin/rules", url.Values{"part": {"content"}, "default_age_rating": {"mature"}}), "/admin/rules")
		if c := e.instance().Content; c.AllowMature || c.DefaultAgeRating != "all" {
			t.Fatalf("a disallowed default was kept: %+v", c)
		}
		wantSaved(t, "sign-up rules", postForm(e.admin, "/admin/rules", url.Values{"part": {"server_rules"}, "rules": {"Be kind. Label AI use honestly."}}), "/admin/rules")
		if e.instance().Rules != "Be kind. Label AI use honestly." {
			t.Fatal("sign-up rules not saved")
		}
		wantStatus(t, "long rules", postForm(e.admin, "/admin/rules", url.Values{"part": {"server_rules"}, "rules": {strings.Repeat("x", 2001)}}), http.StatusUnprocessableEntity)

		page := e.admin.Get("/admin/rules")
		wantBody(t, "rules page", page, `id="rule-nsfw" name="" value="on" disabled`, `id="rule-ai-labels" name="" value="on" checked disabled`, "Be kind. Label AI use honestly.", "0 words · edit")
		if !strings.Contains(string(page.Body), "NSFW filter") || !strings.Contains(string(page.Body), `class="mv-soon"`) {
			t.Fatal("NSFW filter is not in the Soon state")
		}
	})
}

// Blocked words refuse public comments (new and edited) that contain them as
// whole words, in any case.
func TestBlockedWords(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		wantSaved(t, "words", postForm(e.admin, "/admin/rules/words", url.Values{"words": {"Gloomweed\r\nsalt  fog\ngloomweed"}}), "/admin/rules")
		if w := e.instance().Content.BlockedWords; !slices.Equal(w, []string{"Gloomweed", "salt fog"}) {
			t.Fatalf("words %q", w)
		}
		if e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'admin.settings.blocked_words'`) != 1 {
			t.Fatal("blocked words not audited")
		}
		bad := postForm(e.admin, "/admin/rules/words", url.Values{"words": {strings.Repeat("y", 65)}})
		wantStatus(t, "long word", bad, http.StatusUnprocessableEntity)
		wantBody(t, "long word", bad, "Each word or phrase can be up to 64 characters.")
		wantBody(t, "rules page", e.admin.Get("/admin/rules"), "2 words · edit")

		s := e.f.ReadableSeries(publishtest.SeriesSpec{})
		p := e.f.Publish(s, publishtest.PublicationSpec{Pages: 1})
		_, c := e.user(auth.RoleReader)
		post := func(body string) int {
			return c.PostJSON("/api/v1/social/comments", map[string]any{"series_id": s.ID, "target_kind": "chapter", "target_id": p.ID, "body": body}).StatusCode
		}
		if got := post("What a GLOOMWEED!"); got != http.StatusUnprocessableEntity {
			t.Fatalf("blocked comment: %d", got)
		}
		if got := post("The salt\nfog rolls in"); got != http.StatusUnprocessableEntity {
			t.Fatalf("blocked phrase across a line break: %d", got)
		}
		r := c.PostJSON("/api/v1/social/comments", map[string]any{"series_id": s.ID, "target_kind": "chapter", "target_id": p.ID, "body": "Gloomweeds everywhere"})
		wantStatus(t, "allowed comment", r, http.StatusCreated)
		var made struct {
			ID string `json:"id"`
		}
		r.JSON(t, &made)
		edit := c.SendJSON(http.MethodPatch, "/api/v1/social/comments/"+made.ID, map[string]any{"body": "now with gloomweed"})
		wantStatus(t, "blocked edit", edit, http.StatusUnprocessableEntity)
	})
}
