package publish_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// A publish carries out what the creator reviewed on E1. These tests post an
// E1 form rendered before something else changed and check that nothing is
// applied and E1 asks for another review.

const reviewAgain = "changed after you opened this page"

// revisionField is the series editor's hidden revision.
var revisionField = regexp.MustCompile(`name="revision" value="([^"]+)"`)

func seriesRating(t *testing.T, ta *testutil.TestApp, projectID string) (rating, licence string) {
	t.Helper()
	var row struct {
		Rating  string `db:"age_rating"`
		Licence string `db:"licence"`
	}
	if err := ta.Deps.DB.Get(context.Background(), &row, `SELECT age_rating, licence FROM series WHERE project_id = ?`, projectID); err != nil {
		t.Fatal(err)
	}
	return row.Rating, row.Licence
}

func TestStaleE1DoesNotOverwriteTheSeriesRules(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		pf.SetMode(settings.ModePublic)
		f := newChapter(t, ta, false)
		seriesPath := "/studio/p/" + f.project.ID + "/series"
		if resp := postForm(f.client, seriesPath, seriesForm("tidebreak-courier", "age_rating", publish.RatingTeen)); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("series save = %d", resp.StatusCode)
		}
		// E1 is opened (rating Teen, licence CC BY)...
		stale := f.publishForm("en", "age_rating", publish.RatingTeen, "licence", "cc-by")
		// ...then the series editor marks the series Adult.
		edit := f.client.Get(seriesPath)
		rev := revisionField.FindSubmatch(edit.Body)
		if rev == nil {
			t.Fatal("series editor has no revision field")
		}
		if resp := postForm(f.client, seriesPath, seriesForm("tidebreak-courier", "revision", string(rev[1]), "age_rating", publish.RatingAdult)); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("series edit = %d %s", resp.StatusCode, resp.Body)
		}
		resp := postForm(f.client, f.base+"/publish", stale)
		if resp.StatusCode != http.StatusConflict || !contains(resp.Body, reviewAgain) {
			t.Fatalf("stale E1 POST = %d, lacks the review notice", resp.StatusCode)
		}
		if rating, _ := seriesRating(t, ta, f.project.ID); rating != publish.RatingAdult {
			t.Fatalf("stale E1 changed the series rating to %s", rating)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM jobs WHERE type = ?`, publish.JobType); n != 0 {
			t.Fatalf("stale E1 queued %d publish jobs", n)
		}
		// E1 shows the series as it is now, so the creator reviews the new rating.
		if !contains(resp.Body, `<option value="adult" selected`) {
			t.Fatal("review page does not show the current rating")
		}
		// A form rendered before the series existed is stale once it does.
		g := newChapter(t, ta, false)
		early := g.publishForm("en")
		if resp := postForm(g.client, "/studio/p/"+g.project.ID+"/series", seriesForm("lighthouse-debt", "age_rating", publish.RatingMature)); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("series save = %d", resp.StatusCode)
		}
		if resp := postForm(g.client, g.base+"/publish", early); resp.StatusCode != http.StatusConflict {
			t.Fatalf("E1 from before the series existed = %d", resp.StatusCode)
		}
		if rating, _ := seriesRating(t, ta, g.project.ID); rating != publish.RatingMature {
			t.Fatalf("early E1 changed the series rating to %s", rating)
		}
		// Reviewed again, it publishes.
		if j := f.publish(t, f.publishForm("en", "age_rating", publish.RatingAdult, "licence", "cc-by")); j.State != jobs.StateDone {
			t.Fatalf("publish after review: %+v", j.Error)
		}
	})
}

func TestPublishIsBoundToTheRevisionE1Showed(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		pf.SetMode(settings.ModePublic)
		f := newChapter(t, ta, false)
		stale := f.publishForm("en")
		// A page is saved after E1 was rendered and before Publish is pressed.
		exec(t, ta, `UPDATE pages SET version = version + 1 WHERE id = ?`, f.pages[1].ID)
		resp := postForm(f.client, f.base+"/publish", stale)
		if resp.StatusCode != http.StatusConflict || !contains(resp.Body, reviewAgain) {
			t.Fatalf("stale E1 POST = %d, lacks the review notice", resp.StatusCode)
		}
		if ran, err := ta.Deps.Executor.RunOnce(context.Background()); err != nil || ran {
			t.Fatalf("stale E1 queued work: ran=%v %v", ran, err)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM chapter_publications`); n != 0 {
			t.Fatalf("publications after a stale E1 = %d", n)
		}
		// A missing or forged revision is never published either.
		for _, rev := range []string{"", "0123"} {
			form := f.publishForm("en")
			form.Set("revision", rev)
			if resp := postForm(f.client, f.base+"/publish", form); resp.StatusCode != http.StatusConflict {
				t.Fatalf("revision %q = %d", rev, resp.StatusCode)
			}
		}
		// The re-rendered E1 carries the new revision: publishing it now freezes what it shows.
		if j := f.publish(t, f.publishForm("en")); j.State != jobs.StateDone {
			t.Fatalf("publish after review: %+v", j.Error)
		}
	})
}

func TestSecondPublishWhileOneRunsChangesNothing(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		f := newChapter(t, ta, false)
		resp := postForm(f.client, f.base+"/publish", f.publishForm("en", "age_rating", publish.RatingTeen))
		loc, _ := url.Parse(resp.Header.Get("Location"))
		first := loc.Query().Get("job")
		if resp.StatusCode != http.StatusSeeOther || first == "" {
			t.Fatalf("first publish = %d", resp.StatusCode)
		}

		// The creator changes their mind while it runs: private, scheduled, mature.
		second := postForm(f.client, f.base+"/publish", f.publishForm("en", "visibility", publish.VisibilityPrivate,
			"age_rating", publish.RatingMature, "when", "schedule", "date", time.Now().AddDate(0, 1, 0).Format("2006-01-02"), "time", "10:00"))
		if second.StatusCode != http.StatusConflict || !contains(second.Body, "already being published") {
			t.Fatalf("second publish = %d, lacks the running notice", second.StatusCode)
		}
		// Their own job is linked so they can follow it.
		if !contains(second.Body, "/publish/jobs/"+first) {
			t.Fatal("the owner's running job is not linked")
		}
		if rating, _ := seriesRating(t, ta, f.project.ID); rating != publish.RatingTeen {
			t.Fatalf("second decision changed the series rating to %s", rating)
		}

		// Another editor gets the notice without a link to someone else's job.
		editor := ta.CreateUser(t, "", auth.RoleCreator)
		pf.AddMember(f.project.ID, editor.ID, studio.RoleEditor)
		ec := ta.Login(t, editor.Username)
		third := postForm(ec, f.base+"/publish", f.formFor(ec, "en", "visibility", publish.VisibilityMembers))
		if third.StatusCode != http.StatusConflict || !contains(third.Body, "already being published") || contains(third.Body, first) {
			t.Fatalf("editor publish = %d (links the owner's job: %v)", third.StatusCode, contains(third.Body, first))
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM jobs WHERE type = ?`, publish.JobType); n != 1 {
			t.Fatalf("publish jobs = %d, want 1", n)
		}

		// The first decision is what goes live.
		if j := runJob(t, ta, first); j.State != jobs.StateDone {
			t.Fatalf("first job: %+v", j.Error)
		}
		pub, _, err := pf.Service().Publication(ctx, publish.Viewer{}, seriesID(t, ta, f.project.ID), 1, "en")
		if err != nil || pub.Visibility != publish.VisibilityPublic || pub.Status != publish.PublicationPublished {
			t.Fatalf("published = %+v %v", pub, err)
		}
	})
}
