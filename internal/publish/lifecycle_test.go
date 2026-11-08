package publish_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func count(t *testing.T, ta *testutil.TestApp, q string, args ...any) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestPublishIsAtomicWhenTheChapterChanges(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		f := newChapter(t, ta, false)
		frozenAssets := `SELECT COUNT(*) FROM assets WHERE project_id = ? AND kind = 'publication_page'`

		// A page saved while the job renders: nothing is published and no page image is kept.
		resp := postForm(f.client, f.base+"/publish", f.publishForm("en"))
		loc, _ := url.Parse(resp.Header.Get("Location"))
		exec(t, ta, `UPDATE pages SET version = version + 1 WHERE id = ?`, f.pages[1].ID)
		j := runJob(t, ta, loc.Query().Get("job"))
		if j.State != jobs.StateFailed || j.Error == nil || j.Error.Code != "conflict" {
			t.Fatalf("changed chapter job = %s %+v", j.State, j.Error)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM chapter_publications`); n != 0 {
			t.Fatalf("publications after failure = %d", n)
		}
		if n := count(t, ta, frozenAssets, f.project.ID); n != 0 {
			t.Fatalf("page assets after failure = %d", n)
		}
		// E1 shows the failure with its public explanation.
		page := f.client.Get(f.base + "/publish?lang=en&job=" + j.ID)
		if page.StatusCode != http.StatusOK || !contains(page.Body, "Publishing stopped") {
			t.Fatalf("failed job E1 = %d", page.StatusCode)
		}

		// Publish, then fail an update: readers keep the old frozen copy.
		if j := f.publish(t, f.publishForm("en")); j.State != jobs.StateDone {
			t.Fatalf("publish: %+v", j.Error)
		}
		sid := seriesID(t, ta, f.project.ID)
		_, before, err := pf.Service().Publication(ctx, publish.Viewer{}, sid, 1, "en")
		if err != nil {
			t.Fatal(err)
		}
		resp = postForm(f.client, f.base+"/publish", f.publishForm("en", "title", "Renamed"))
		loc, _ = url.Parse(resp.Header.Get("Location"))
		exec(t, ta, `UPDATE chapters SET updated_at = ? WHERE id = ?`, db.Now().Add(time.Second), f.chapter.ID)
		if j := runJob(t, ta, loc.Query().Get("job")); j.State != jobs.StateFailed {
			t.Fatalf("stale update = %s", j.State)
		}
		pub, after, err := pf.Service().Publication(ctx, publish.Viewer{}, sid, 1, "en")
		if err != nil || pub.Title != "The Lighthouse Debt" || len(after) != len(before) || after[0].ID != before[0].ID || after[0].AssetID != before[0].AssetID {
			t.Fatalf("failed update changed the publication: %+v %v", pub, err)
		}
		if n := count(t, ta, frozenAssets, f.project.ID); n != 2 {
			t.Fatalf("page assets after failed update = %d", n)
		}
	})
}

func TestUpdateAndUnpublishThroughTheReadSide(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		svc := pf.Service()
		f := newChapter(t, ta, false)
		follower := ta.CreateUser(t, "", auth.RoleReader)
		fake := &fakeFollowers{users: map[string][]string{}}
		publish.SetFollowersForTest(svc, fake)

		if j := f.publish(t, f.publishForm("en")); j.State != jobs.StateDone {
			t.Fatalf("publish: %+v", j.Error)
		}
		sid := seriesID(t, ta, f.project.ID)
		fake.users[sid] = []string{follower.ID}
		pub, pages, err := svc.Publication(ctx, publish.Viewer{}, sid, 1, "en")
		if err != nil {
			t.Fatal(err)
		}

		// Update re-freezes with the creator's alt-text edit as a new copy; the publication keeps its identity and time.
		altField := "alt_" + f.pages[0].Panels[0].ID
		if j := f.publish(t, f.publishForm("en", "title", "The Lighthouse Debt (revised)", altField, "Rin races along the wall.")); j.State != jobs.StateDone {
			t.Fatalf("update: %+v", j.Error)
		}
		upd, newPages, err := svc.Publication(ctx, publish.Viewer{}, sid, 1, "en")
		if err != nil || upd.ID != pub.ID || upd.Title != "The Lighthouse Debt (revised)" || !upd.PublishedAt.Equal(*pub.PublishedAt) {
			t.Fatalf("update = %+v %v", upd, err)
		}
		if len(newPages) != 2 || newPages[0].ID == pages[0].ID || newPages[0].AssetID == pages[0].AssetID || newPages[0].Panels[0].AltText != "Rin races along the wall." {
			t.Fatalf("update pages = %+v", newPages)
		}
		if _, err := assets.From(ta.Deps).Get(ctx, pages[0].AssetID); err == nil {
			t.Fatal("old frozen image kept after update")
		}
		// E1 prefills the edited alt-text for the next update.
		if page := f.client.Get(f.base + "/publish"); !contains(page.Body, "Rin races along the wall.") || !contains(page.Body, "Update published chapter") {
			t.Fatal("E1 does not prefill the frozen alt-text")
		}
		if n := notifications(t, ta, notify.KindChapter)[follower.ID]; n != 0 {
			t.Fatalf("follower notified %d times for an update of a chapter that went live before they followed", n)
		}

		// Unpublish hides it at once; publishing again never notifies.
		resp := postForm(f.client, f.base+"/publish/unpublish", f.publishForm("en"))
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("unpublish = %d", resp.StatusCode)
		}
		if _, _, err := svc.Publication(ctx, publish.Viewer{}, sid, 1, "en"); !errors.Is(err, publish.ErrNotFound) {
			t.Fatalf("unpublished chapter readable: %v", err)
		}
		if svc.CanRead(ctx, publish.Viewer{UserID: f.owner.ID}, publish.Series{ID: sid}, publish.Publication{ID: pub.ID}) {
			t.Fatal("owner can read an unpublished chapter through the read side")
		}
		// With no readable chapter left, readers no longer see the series; the owner keeps the preview.
		if _, err := svc.SeriesByID(ctx, publish.Viewer{}, sid); !errors.Is(err, publish.ErrNotFound) {
			t.Fatalf("series readable after its only chapter was unpublished: %v", err)
		}
		if s, err := svc.SeriesByID(ctx, publish.Viewer{UserID: f.owner.ID}, sid); err != nil || len(s.AILabel) != 0 {
			t.Fatalf("series after unpublish = %+v %v", s, err)
		}
		if got := postForm(f.client, f.base+"/publish/unpublish", f.publishForm("en")).StatusCode; got != http.StatusNotFound {
			t.Fatalf("second unpublish = %d", got)
		}
		if j := f.publish(t, f.publishForm("en")); j.State != jobs.StateDone {
			t.Fatalf("republish: %+v", j.Error)
		}
		if _, _, err := svc.Publication(ctx, publish.Viewer{}, sid, 1, "en"); err != nil {
			t.Fatalf("republished chapter: %v", err)
		}
		if n := notifications(t, ta, notify.KindChapter)[follower.ID]; n != 0 {
			t.Fatalf("republish notified %d times", n)
		}
	})
}

func TestScheduleLoopPublishesOnceAcrossConnections(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		svc := pf.Service()
		f := newChapter(t, ta, false)
		readers := []*auth.User{ta.CreateUser(t, "", auth.RoleReader), ta.CreateUser(t, "", auth.RoleReader)}
		suspended := ta.CreateUser(t, "", auth.RoleReader)
		if err := ta.Deps.Auth.SetStatus(ctx, suspended.ID, auth.StatusSuspended, "test"); err != nil {
			t.Fatal(err)
		}
		fake := &fakeFollowers{users: map[string][]string{}}
		publish.SetFollowersForTest(svc, fake)

		at := time.Now().Add(2 * time.Hour).UTC()
		form := f.publishForm("en", "when", "schedule", "date", at.Format("2006-01-02"), "time", at.Format("15:04"),
			"visibility", publish.VisibilityMembers)
		if j := f.publish(t, form); j.State != jobs.StateDone {
			t.Fatalf("schedule: %+v", j.Error)
		}
		sid := seriesID(t, ta, f.project.ID)
		fake.users[sid] = []string{readers[0].ID, suspended.ID, f.owner.ID, readers[1].ID}
		reader := publish.Viewer{UserID: readers[0].ID}
		if _, _, err := svc.Publication(ctx, reader, sid, 1, "en"); !errors.Is(err, publish.ErrNotFound) {
			t.Fatalf("scheduled chapter readable early: %v", err)
		}
		if live := f.client.Get(f.base + "/published?lang=en"); !contains(live.Body, "Chapter 1 is scheduled") {
			t.Fatal("published page does not show the schedule")
		}
		if n, err := publish.PublishDueForTest(ctx, svc, ta.Deps.DB); err != nil || n != 0 {
			t.Fatalf("early pass = %d %v", n, err)
		}

		// Two server processes (two connections) pass at the due time: exactly one publishes.
		// Time passes: the schedule is now due.
		exec(t, ta, `UPDATE chapter_publications SET scheduled_at = ?`, db.Now().Add(-time.Minute))
		dialect, dsn, err := ta.Deps.Cfg.Database()
		if err != nil {
			t.Fatal(err)
		}
		other, err := db.Open(ctx, dialect, dsn, db.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		var wg sync.WaitGroup
		results := make([]int, 2)
		errs := make([]error, 2)
		for i, conn := range []*db.DB{ta.Deps.DB, other} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = publish.PublishDueForTest(ctx, svc, conn)
			}()
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil || results[0]+results[1] != 1 {
			t.Fatalf("passes = %v %v", results, errs)
		}
		if n, err := publish.PublishDueForTest(ctx, svc, other); err != nil || n != 0 {
			t.Fatalf("repeat pass = %d %v", n, err)
		}
		pub, _, err := svc.Publication(ctx, reader, sid, 1, "en")
		if err != nil || pub.Status != publish.PublicationPublished {
			t.Fatalf("after the loop: %+v %v", pub, err)
		}
		// Followers who may read it are notified once; suspended accounts and the publisher are not.
		got := notifications(t, ta, notify.KindChapter)
		if got[readers[0].ID] != 1 || got[readers[1].ID] != 1 || got[suspended.ID] != 0 || got[f.owner.ID] != 0 {
			t.Fatalf("notifications = %v", got)
		}
		publish.NotifyFollowersForTest(ctx, svc, pub.ID)
		if again := notifications(t, ta, notify.KindChapter); again[readers[0].ID] != 1 {
			t.Fatalf("second fan-out sent more: %v", again)
		}
		var link string
		if err := ta.Deps.DB.Get(ctx, &link, `SELECT link FROM notifications WHERE user_id = ?`, readers[0].ID); err != nil || link != "/s/tidebreak-courier/1" {
			t.Fatalf("notification link %q %v", link, err)
		}
		if live := f.client.Get(f.base + "/published?lang=en"); !contains(live.Body, "2 followers notified") {
			t.Fatal("published page lacks the notified count")
		}
	})
}

// The schedule loop publishes as the person who scheduled the chapter, so it
// checks again when the chapter comes due that they still may: an editor
// demoted meanwhile, or a suspended owner, publishes nothing. The chapter is
// left unpublished (E1 says so) and nobody is notified.
func TestScheduleLoopRechecksWhoScheduled(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		svc := pf.Service()
		fake := &fakeFollowers{users: map[string][]string{}}
		publish.SetFollowersForTest(svc, fake)
		follower := ta.CreateUser(t, "", auth.RoleReader)
		at := time.Now().Add(2 * time.Hour).UTC()
		schedule := func(f *chapterFix, c *testutil.Client) string {
			t.Helper()
			form := f.formFor(c, "en", "when", "schedule", "date", at.Format("2006-01-02"), "time", at.Format("15:04"))
			resp := postForm(c, f.base+"/publish", form)
			loc, _ := url.Parse(resp.Header.Get("Location"))
			if resp.StatusCode != http.StatusSeeOther || loc == nil || loc.Query().Get("job") == "" {
				t.Fatalf("schedule POST = %d %s", resp.StatusCode, resp.Body)
			}
			if j := runJob(t, ta, loc.Query().Get("job")); j.State != jobs.StateDone {
				t.Fatalf("schedule: %+v", j.Error)
			}
			sid := seriesID(t, ta, f.project.ID)
			fake.users[sid] = []string{follower.ID}
			return sid
		}

		// An editor schedules chapter 1 of one project, then is demoted to viewer.
		demoted := newChapter(t, ta, false)
		editor := ta.CreateUser(t, "", auth.RoleCreator)
		pf.AddMember(demoted.project.ID, editor.ID, studio.RoleEditor)
		first := schedule(demoted, ta.Login(t, editor.Username))
		exec(t, ta, `UPDATE project_members SET role = ? WHERE project_id = ? AND user_id = ?`, studio.RoleViewer, demoted.project.ID, editor.ID)
		// The owner of another project schedules, then is suspended.
		suspended := newChapter(t, ta, false)
		second := schedule(suspended, suspended.client)
		if err := ta.Deps.Auth.SetStatus(ctx, suspended.owner.ID, auth.StatusSuspended, "test"); err != nil {
			t.Fatal(err)
		}

		exec(t, ta, `UPDATE chapter_publications SET scheduled_at = ?`, db.Now().Add(-time.Minute))
		dialect, dsn, err := ta.Deps.Cfg.Database()
		if err != nil {
			t.Fatal(err)
		}
		other, err := db.Open(ctx, dialect, dsn, db.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		var wg sync.WaitGroup
		results := make([]int, 2)
		errs := make([]error, 2)
		for i, conn := range []*db.DB{ta.Deps.DB, other} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = publish.PublishDueForTest(ctx, svc, conn)
			}()
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil || results[0]+results[1] != 0 {
			t.Fatalf("passes = %v %v, want nothing published", results, errs)
		}
		for _, sid := range []string{first, second} {
			if _, _, err := svc.Publication(ctx, publish.Viewer{UserID: follower.ID}, sid, 1, "en"); !errors.Is(err, publish.ErrNotFound) {
				t.Fatalf("chapter published for a user who may no longer publish: %v", err)
			}
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM chapter_publications WHERE status = ? AND scheduled_at IS NULL`, publish.PublicationUnpublished); n != 2 {
			t.Fatalf("held chapters = %d, want 2 unpublished", n)
		}
		if n := notifications(t, ta, notify.KindChapter)[follower.ID]; n != 0 {
			t.Fatalf("follower notified %d times", n)
		}
		// Later passes leave them alone; the owner sees the state on E1 and can publish again.
		if n, err := publish.PublishDueForTest(ctx, svc, other); err != nil || n != 0 {
			t.Fatalf("repeat pass = %d %v", n, err)
		}
		if page := demoted.client.Get(demoted.base + "/publish?lang=en"); !contains(page.Body, "This chapter is unpublished") {
			t.Fatal("E1 does not show that the scheduled chapter was not published")
		}
		if j := demoted.publish(t, demoted.publishForm("en")); j.State != jobs.StateDone {
			t.Fatalf("owner publishes again: %+v", j.Error)
		}
	})
}

func TestNewChapterNotifiesOnceWhenPublishedNow(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		pf.SetMode(settings.ModePublic)
		svc := pf.Service()
		f := newChapter(t, ta, false)
		reader := ta.CreateUser(t, "", auth.RoleReader)
		fake := &fakeFollowers{users: map[string][]string{}}
		publish.SetFollowersForTest(svc, fake)
		// The series exists once the creator saves it, so followers can follow before chapter 1.
		if resp := postForm(f.client, "/studio/p/"+f.project.ID+"/series", seriesForm("tidebreak-courier")); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("series save = %d %s", resp.StatusCode, resp.Body)
		}
		sid := seriesID(t, ta, f.project.ID)
		fake.users[sid] = []string{reader.ID}
		if j := f.publish(t, f.publishForm("en", "notify", "")); j.State != jobs.StateDone {
			t.Fatalf("publish without notify: %+v", j.Error)
		}
		if n := notifications(t, ta, notify.KindChapter)[reader.ID]; n != 0 {
			t.Fatalf("notified although the creator unticked it: %d", n)
		}
		if j := f.publish(t, f.publishForm("ja")); j.State != jobs.StateDone {
			t.Fatalf("publish ja: %+v", j.Error)
		}
		if n := notifications(t, ta, notify.KindChapter)[reader.ID]; n != 1 {
			t.Fatalf("new language notifications = %d", n)
		}
		if j := f.publish(t, f.publishForm("ja", "title", "Update")); j.State != jobs.StateDone {
			t.Fatalf("update ja: %+v", j.Error)
		}
		if n := notifications(t, ta, notify.KindChapter)[reader.ID]; n != 1 {
			t.Fatalf("update notified again: %d", n)
		}
		if fake.calls != 1 {
			t.Fatalf("follower source called %d times", fake.calls)
		}
	})
}

func seriesForm(slug string, extra ...string) url.Values {
	v := url.Values{"slug": {slug}, "title": {"Tidebreak Courier"}, "synopsis": {"A courier crosses a flooded city."},
		"genres": {"Adventure, mystery"}, "tags": {"salt city"}, "format": {"manga_rtl"}, "age_rating": {"teen"},
		"licence": {"cc-by"}, "visibility": {"public"}, "status": {"ongoing"}, "original_lang": {"en"},
		"link_label_0": {"Studio site"}, "link_url_0": {"https://example.org/tidebreak"}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return v
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }

// A series appears to readers only while they can read one of its chapters:
// a private preview chapter does not publish the series, unpublishing every
// chapter hides it again, and the series' languages and AI label come from
// the chapters that are not private.
func TestSeriesVisibleOnlyWithAReadableChapter(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		svc := pf.Service()
		f := newChapter(t, ta, false)
		reader := publish.Viewer{UserID: ta.CreateUser(t, "", auth.RoleReader).ID}
		owner := publish.Viewer{UserID: f.owner.ID}
		visible := func(what string, v publish.Viewer, sid string, want bool) {
			t.Helper()
			_, err := svc.SeriesByID(ctx, v, sid)
			if (err == nil) != want || (err != nil && !errors.Is(err, publish.ErrNotFound)) {
				t.Fatalf("%s: SeriesByID err = %v, want visible=%v", what, err, want)
			}
			slug := "tidebreak-courier"
			if _, err := svc.SeriesBySlug(ctx, v, slug); (err == nil) != want {
				t.Fatalf("%s: SeriesBySlug err = %v, want visible=%v", what, err, want)
			}
			for _, listed := range []bool{false, true} {
				if in := slices.Contains(listAll(t, svc, v, publish.SeriesFilter{Listed: listed}), sid); in != want {
					t.Fatalf("%s: in ListSeries(Listed=%v) = %v, want %v", what, listed, in, want)
				}
			}
		}

		// The public series is saved first, then a private preview in Japanese
		// labelled with AI art is published: readers still see no series.
		if resp := postForm(f.client, "/studio/p/"+f.project.ID+"/series", seriesForm("tidebreak-courier", "status", publish.SeriesDraft)); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("series save = %d %s", resp.StatusCode, resp.Body)
		}
		if j := f.publish(t, f.publishForm("ja", "visibility", publish.VisibilityPrivate, "ai_art", "ai")); j.State != jobs.StateDone {
			t.Fatalf("private publish: %+v", j.Error)
		}
		sid := seriesID(t, ta, f.project.ID)
		visible("anonymous, private chapter only", publish.Viewer{}, sid, false)
		visible("member, private chapter only", reader, sid, false)
		s, err := svc.SeriesByID(ctx, owner, sid)
		if err != nil {
			t.Fatalf("owner preview: %v", err)
		}
		if s.PublishedAt != nil || s.Status != publish.SeriesDraft || len(s.AILabel) != 0 || !slices.Equal(s.Languages, []string{"en"}) {
			t.Fatalf("series after a private chapter = published %v, %s, %v, %v", s.PublishedAt, s.Status, s.AILabel, s.Languages)
		}

		// A public chapter publishes the series; the private one still adds nothing to it.
		if j := f.publish(t, f.publishForm("en")); j.State != jobs.StateDone {
			t.Fatalf("public publish: %+v", j.Error)
		}
		visible("anonymous, public chapter", publish.Viewer{}, sid, true)
		visible("member, public chapter", reader, sid, true)
		if s, err = svc.SeriesByID(ctx, publish.Viewer{}, sid); err != nil || s.PublishedAt == nil || s.Status != publish.SeriesOngoing ||
			s.AILabel["art"] != "human" || s.AILabel["script"] != "ai" || !slices.Equal(s.Languages, []string{"en"}) {
			t.Fatalf("series after a public chapter = %+v %v", s, err)
		}

		// Unpublishing the only readable chapter hides the series again; the owner keeps the preview.
		if resp := postForm(f.client, f.base+"/publish/unpublish", f.publishForm("en")); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("unpublish = %d", resp.StatusCode)
		}
		visible("anonymous, all unpublished", publish.Viewer{}, sid, false)
		visible("member, all unpublished", reader, sid, false)
		if _, err := svc.SeriesByID(ctx, owner, sid); err != nil {
			t.Fatalf("owner preview after unpublish: %v", err)
		}
	})
}

// Publishing is the creator's own decision: a canceled or failed publish job
// cannot be retried through the jobs API, by a site admin or by its owner, so
// nobody can publish a chapter on the creator's behalf (ADR 0012 §3).
func TestPublishJobsCannotBeRetried(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		f := newChapter(t, ta, false)
		admin := ta.Login(t, ta.Admin.Username)
		queue := func() string {
			t.Helper()
			resp := postForm(f.client, f.base+"/publish", f.publishForm("en"))
			loc, err := url.Parse(resp.Header.Get("Location"))
			if resp.StatusCode != http.StatusSeeOther || err != nil || loc.Query().Get("job") == "" {
				t.Fatalf("publish POST = %d %q", resp.StatusCode, resp.Header.Get("Location"))
			}
			return loc.Query().Get("job")
		}

		// The creator cancels before it runs; an admin retry must not publish it.
		canceled := queue()
		if _, err := ta.Deps.Jobs.Cancel(ctx, canceled); err != nil {
			t.Fatal(err)
		}
		// A failed one (the chapter changed) is refused the same way.
		failed := queue()
		exec(t, ta, `UPDATE pages SET version = version + 1 WHERE id = ?`, f.pages[0].ID)
		if j := runJob(t, ta, failed); j.State != jobs.StateFailed {
			t.Fatalf("changed chapter job = %s", j.State)
		}
		for _, id := range []string{canceled, failed} {
			for name, c := range map[string]*testutil.Client{"admin": admin, "owner": f.client} {
				if got := c.PostJSON("/api/v1/jobs/"+id+"/retry", nil); got.StatusCode != http.StatusConflict {
					t.Fatalf("%s retry of a publish job = %d %s", name, got.StatusCode, got.Body)
				}
			}
		}
		if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || ran {
			t.Fatalf("a refused retry queued work: ran=%v %v", ran, err)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM chapter_publications`); n != 0 {
			t.Fatalf("publications after refused retries = %d", n)
		}
	})
}

// An unlisted chapter is shared by link only: going live never notifies
// followers, though they may open it (ADR 0012 §1, §10).
func TestUnlistedChapterNotifiesNobody(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		pf.SetMode(settings.ModePublic)
		svc := pf.Service()
		f := newChapter(t, ta, false)
		reader := ta.CreateUser(t, "", auth.RoleReader)
		fake := &fakeFollowers{users: map[string][]string{}}
		publish.SetFollowersForTest(svc, fake)
		if resp := postForm(f.client, "/studio/p/"+f.project.ID+"/series", seriesForm("tidebreak-courier")); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("series save = %d %s", resp.StatusCode, resp.Body)
		}
		sid := seriesID(t, ta, f.project.ID)
		fake.users[sid] = []string{reader.ID}
		if j := f.publish(t, f.publishForm("en", "visibility", publish.VisibilityUnlisted)); j.State != jobs.StateDone {
			t.Fatalf("publish unlisted: %+v", j.Error)
		}
		rv := publish.Viewer{UserID: reader.ID}
		pub, _, err := svc.Publication(ctx, rv, sid, 1, "en")
		if err != nil || !svc.CanRead(ctx, rv, publish.Series{ID: sid}, pub) {
			t.Fatalf("follower cannot open the unlisted chapter by link: %v", err)
		}
		if n := notifications(t, ta, notify.KindChapter)[reader.ID]; n != 0 {
			t.Fatalf("follower notified %d times for an unlisted chapter", n)
		}
		// A second fan-out (the schedule loop's path) never notifies for it either.
		publish.NotifyFollowersForTest(ctx, svc, pub.ID)
		if n := notifications(t, ta, notify.KindChapter)[reader.ID]; n != 0 || fake.calls != 0 {
			t.Fatalf("fan-out for an unlisted chapter: %d notifications, %d follower calls", n, fake.calls)
		}
		if live := f.client.Get(f.base + "/published?lang=en"); !contains(live.Body, "0 followers notified") {
			t.Fatal("published page does not say nobody was notified")
		}
	})
}
