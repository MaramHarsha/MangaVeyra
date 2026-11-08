package admin_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// commentOn posts a public chapter comment through the social API.
func (e *env) commentOn(author *auth.User, s publishtest.Series, p publishtest.Publication, body string) string {
	e.t.Helper()
	c := e.ta.Login(e.t, author.Username)
	r := c.PostJSON("/api/v1/social/comments", map[string]any{"series_id": s.ID, "target_kind": "chapter", "target_id": p.ID, "body": body})
	wantStatus(e.t, "post comment", r, http.StatusCreated)
	var out struct {
		ID string `json:"id"`
	}
	r.JSON(e.t, &out)
	return out.ID
}

// Dismissing closes only that report and records who did it and why.
func TestDismissReport(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		reader, _ := e.user(auth.RoleReader)
		other, _ := e.user(auth.RoleReader)
		r1 := e.report(reader, "user", other.ID, "spam", "posts links everywhere")
		r2 := e.report(other, "user", reader.ID, "spam", "")

		page := e.admin.Get("/admin/reports/" + r1)
		wantStatus(t, "review", page, http.StatusOK)
		wantBody(t, "review", page, `id="review"`, "posts links everywhere", `value="dismiss"`, `value="suspend"`)

		wantSaved(t, "dismiss", resolve(e.admin, r1, "dismiss", "note", "Not spam, a shared fan page"), "/admin")
		st := e.reportState(r1)
		if st.Status != "dismissed" || st.Resolution != "Not spam, a shared fan page" || st.ResolvedBy == nil || *st.ResolvedBy != e.ta.Admin.ID {
			t.Fatalf("dismissed report: %+v", st)
		}
		if e.reportState(r2).Status != "open" {
			t.Fatal("dismissing one report closed another")
		}
		if e.audits("admin.report.dismiss", r1) != 1 {
			t.Fatal("dismissal not audited")
		}
		// A resolved report cannot be resolved again.
		wantStatus(t, "dismiss twice", resolve(e.admin, r1, "dismiss"), http.StatusConflict)
		// Actions that do not apply to the target are refused.
		wantStatus(t, "takedown a user report", resolve(e.admin, r2, "takedown_series"), http.StatusConflict)
		wantStatus(t, "unknown report", resolve(e.admin, "01m6yf00000000000000000r99", "dismiss"), http.StatusNotFound)
	})
}

// Hiding a reported comment hides it from readers, closes every open report on
// it, and the series owner cannot show it again.
func TestHideComment(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		s := e.f.ReadableSeries(publishtest.SeriesSpec{})
		p := e.f.Publish(s, publishtest.PublicationSpec{Pages: 1})
		author, _ := e.user(auth.RoleReader)
		r1u, _ := e.user(auth.RoleReader)
		r2u, _ := e.user(auth.RoleReader)
		commentID := e.commentOn(author, s, p, "Follow my page for free art packs")
		r1 := e.report(r1u, "comment", commentID, "spam", "Spam comments")
		r2 := e.report(r2u, "comment", commentID, "harassment", "")

		page := e.admin.Get("/admin/reports/" + r1)
		wantBody(t, "review", page, "Follow my page for free art packs", `value="hide_comment"`)

		wantSaved(t, "hide", resolve(e.admin, r1, "hide_comment"), "/admin")
		for _, id := range []string{r1, r2} {
			if st := e.reportState(id); st.Status != "actioned" || st.Resolution != "Comment hidden" {
				t.Fatalf("report %s after hide: %+v", id, st)
			}
		}
		if e.audits("admin.report.hide_comment", commentID) != 1 {
			t.Fatal("hide not audited")
		}
		list := e.ta.NewClient().Get(commentsURLFor(s.ID, p.ID))
		wantStatus(t, "anonymous comments", list, http.StatusOK)
		wantNoBody(t, "anonymous comments", list, commentID)

		owner := e.ta.Login(t, s.Owner.Username)
		ownerList := owner.Get(commentsURLFor(s.ID, p.ID))
		wantBody(t, "owner sees the hidden comment", ownerList, commentID, `"can_hide":false`)
		r := owner.SendJSON(http.MethodDelete, "/api/v1/social/comments/"+commentID+"/hide", nil)
		wantStatus(t, "owner unhide", r, http.StatusForbidden)
		if n := e.count(`SELECT COUNT(*) FROM comments WHERE id = ? AND resolved_at IS NOT NULL AND resolved_by = ?`, commentID, e.ta.Admin.ID); n != 1 {
			t.Fatal("comment is no longer hidden by the admin")
		}
	})
}

func commentsURLFor(seriesID, pubID string) string {
	return "/api/v1/social/comments?" + url.Values{"series": {seriesID}, "target_kind": {"chapter"}, "target_id": {pubID}}.Encode()
}

// A chapter takedown hides every language of the chapter from everyone outside
// the project through publish.Service; the project still reads it (and sees a
// notice), nothing is deleted, and restoring brings it back.
func TestTakedownChapter(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		s := e.f.ReadableSeries(publishtest.SeriesSpec{})
		p := e.f.Publish(s, publishtest.PublicationSpec{Pages: 1})
		ja := e.f.Publish(s, publishtest.PublicationSpec{ChapterID: p.ChapterID, Lang: "ja", Number: p.Number, Pages: 1})
		member, _ := e.user(auth.RoleReader)
		projectMember, _ := e.user(auth.RoleReader)
		e.f.AddMember(s.ProjectID, projectMember.ID, studio.RoleViewer)
		reportID := e.report(member, "chapter", p.ID, "copyright", "Traced from another comic")

		wantSaved(t, "takedown", resolve(e.admin, reportID, "takedown_chapter", "note", "Copyright claim"), "/admin")
		if st := e.reportState(reportID); st.Status != "actioned" || st.Resolution != "Copyright claim" {
			t.Fatalf("report after takedown: %+v", st)
		}
		if e.audits("admin.takedown", p.ChapterID) != 1 {
			t.Fatal("takedown not audited")
		}
		svc := e.pub()
		series, err := svc.SeriesByID(ctx, publish.Viewer{}, s.ID)
		if err != nil {
			t.Fatalf("the series stays readable through its first chapter: %v", err)
		}
		outside := map[string]publish.Viewer{"anonymous": {}, "member": publishtest.Viewer(member), "admin": {UserID: e.ta.Admin.ID, Admin: true}}
		for who, v := range outside {
			for _, pub := range []publishtest.Publication{p, ja} {
				if _, _, err := svc.Publication(ctx, v, s.ID, pub.Number, pub.Lang); !errors.Is(err, publish.ErrNotFound) {
					t.Fatalf("%s reads the taken-down chapter (%s): %v", who, pub.Lang, err)
				}
				if svc.CanRead(ctx, v, series, publish.Publication{ID: pub.ID, SeriesID: s.ID}) {
					t.Fatalf("%s CanRead the taken-down chapter", who)
				}
			}
			list, err := svc.Publications(ctx, v, s.ID, "")
			if err != nil || len(list) != 1 {
				t.Fatalf("%s chapter list: %d, %v", who, len(list), err)
			}
		}
		got, _, err := svc.Publication(ctx, publishtest.Viewer(projectMember), s.ID, p.Number, p.Lang)
		if err != nil || !got.TakenDown {
			t.Fatalf("project member: %+v, %v", got, err)
		}
		if n := e.count(`SELECT COUNT(*) FROM chapter_publications WHERE chapter_id = ?`, p.ChapterID); n != 2 {
			t.Fatalf("a takedown deleted publications: %d left", n)
		}
		// E1 tells the creator why readers cannot open the chapter.
		e1 := e.ta.Login(t, s.Owner.Username).Get("/studio/p/" + s.ProjectID + "/chapters/" + strconv.FormatFloat(p.Number, 'f', -1, 64) + "/publish")
		wantStatus(t, "E1", e1, http.StatusOK)
		wantBody(t, "E1 notice", e1, "An admin took this down")
		// E5 tells the project; readers outside it see the series without the chapter.
		seriesPage := e.ta.Login(t, projectMember.Username).Get("/s/" + s.Slug)
		wantBody(t, "E5 for the project", seriesPage, "An admin took a chapter of this series down")
		wantNoBody(t, "E5 for readers", e.ta.Login(t, member.Username).Get("/s/"+s.Slug), "An admin took")

		// The Reports tab lists it with Restore; restoring makes it readable again.
		reports := e.admin.Get("/admin")
		wantBody(t, "takedown list", reports, "Taken down", adminapi.TakedownKey(adminapi.TakedownChapter, p.ChapterID))
		wantSaved(t, "restore", postForm(e.admin, "/admin/takedowns/restore", url.Values{"key": {adminapi.TakedownKey(adminapi.TakedownChapter, p.ChapterID)}}), "/admin")
		if _, _, err := svc.Publication(ctx, publishtest.Viewer(member), s.ID, p.Number, p.Lang); err != nil {
			t.Fatalf("restored chapter: %v", err)
		}
		if e.audits("admin.takedown.restore", p.ChapterID) != 1 {
			t.Fatal("restore not audited")
		}
		wantStatus(t, "restore twice", postForm(e.admin, "/admin/takedowns/restore", url.Values{"key": {adminapi.TakedownKey(adminapi.TakedownChapter, p.ChapterID)}}), http.StatusNotFound)
		wantStatus(t, "restore a foreign key", postForm(e.admin, "/admin/takedowns/restore", url.Values{"key": {"limits"}}), http.StatusNotFound)
	})
}

// A series takedown hides the series from lists, reads and its readers, admins
// included, while its project keeps previewing it with a notice.
func TestTakedownSeries(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		s := e.f.ReadableSeries(publishtest.SeriesSpec{})
		p := e.f.Publish(s, publishtest.PublicationSpec{Pages: 1})
		member, memberC := e.user(auth.RoleReader)
		reportID := e.report(member, "series", s.ID, "illegal", "")
		wantSaved(t, "takedown", resolve(e.admin, reportID, "takedown_series"), "/admin")
		if e.audits("admin.takedown", s.ID) != 1 {
			t.Fatal("series takedown not audited")
		}
		svc := e.pub()
		for who, v := range map[string]publish.Viewer{"anonymous": {}, "member": publishtest.Viewer(member), "admin": {UserID: e.ta.Admin.ID, Admin: true}} {
			if _, err := svc.SeriesBySlug(ctx, v, s.Slug); !errors.Is(err, publish.ErrNotFound) {
				t.Fatalf("%s reads the taken-down series: %v", who, err)
			}
			list, _, err := svc.ListSeries(ctx, v, publish.SeriesFilter{})
			if err != nil {
				t.Fatal(err)
			}
			for _, x := range list {
				if x.ID == s.ID {
					t.Fatalf("%s lists the taken-down series", who)
				}
			}
			if _, _, err := svc.Publication(ctx, v, s.ID, p.Number, ""); !errors.Is(err, publish.ErrNotFound) {
				t.Fatalf("%s reads a chapter of the taken-down series: %v", who, err)
			}
		}
		wantStatus(t, "E5 for readers", memberC.Get("/s/"+s.Slug), http.StatusNotFound)
		owner := e.ta.Login(t, s.Owner.Username)
		got, err := svc.SeriesBySlug(ctx, publishtest.Viewer(s.Owner), s.Slug)
		if err != nil || !got.TakenDown {
			t.Fatalf("owner preview: %+v, %v", got, err)
		}
		wantBody(t, "E5 for the owner", owner.Get("/s/"+s.Slug), "An admin took this series down")
		// The review panel no longer offers a second takedown.
		review := e.admin.Get("/admin/reports/" + reportID)
		wantNoBody(t, "resolved review", review, `value="takedown_series"`)
		wantBody(t, "resolved review", review, "Series taken down")
	})
}

// Suspending the reported account signs it out and closes the report; the
// last admin cannot be suspended this way.
func TestSuspendFromReport(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		reader, _ := e.user(auth.RoleReader)
		offender, offenderC := e.user(auth.RoleCreator)
		reportID := e.report(reader, "user", offender.ID, "harassment", "")
		wantSaved(t, "suspend", resolve(e.admin, reportID, "suspend"), "/admin")
		u, _ := e.ta.Deps.Auth.UserByID(ctx, offender.ID)
		if u.Status != auth.StatusSuspended {
			t.Fatalf("status %q", u.Status)
		}
		if e.audits("admin.report.suspend", offender.ID) != 1 || e.audits("user.set_status", offender.ID) != 1 {
			t.Fatal("suspension not audited")
		}
		if r := offenderC.Get("/studio"); r.StatusCode == http.StatusOK {
			t.Fatal("the suspended account is still signed in")
		}
		// The admin's own account is never offered for suspension.
		selfReport := e.report(reader, "user", e.ta.Admin.ID, "spam", "")
		wantStatus(t, "suspend self", resolve(e.admin, selfReport, "suspend"), http.StatusConflict)
	})
}

// An AI-label dispute shows the per-role labels and can ask the creator to
// review them: the series owner is notified with a link to E1.
func TestAILabelDispute(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		s := e.f.ReadableSeries(publishtest.SeriesSpec{})
		p := e.f.Publish(s, publishtest.PublicationSpec{Pages: 1, AILabel: map[string]string{"script": "human", "art": "human", "lettering": "ai_assisted"}})
		reader, _ := e.user(auth.RoleReader)
		dispute := e.report(reader, "chapter", p.ID, "ai_label", "AI label says hand-drawn but art is generated")
		spam := e.report(reader, "series", s.ID, "spam", "")

		tab := e.admin.Get("/admin/disputes")
		wantStatus(t, "disputes", tab, http.StatusOK)
		wantBody(t, "disputes", tab, "AI label says hand-drawn but art is generated", "Script: Human", "Lettering: AI-assisted", ">AI-label disputes 1<")
		wantNoBody(t, "disputes", tab, "/admin/disputes/"+spam)
		wantStatus(t, "spam is not a dispute", e.admin.Get("/admin/disputes/"+spam), http.StatusNotFound)

		review := e.admin.Get("/admin/disputes/" + dispute)
		wantBody(t, "dispute review", review, `value="review_label"`, `name="tab" value="disputes"`)
		r := resolve(e.admin, dispute, "review_label", "tab", "disputes")
		wantSaved(t, "ask for review", r, "/admin/disputes")
		if st := e.reportState(dispute); st.Status != "actioned" || st.Resolution != "Creator asked to review the AI label" {
			t.Fatalf("dispute after review request: %+v", st)
		}
		if e.audits("admin.report.review_label", p.ChapterID) != 1 {
			t.Fatal("review request not audited")
		}
		list, err := e.ta.Deps.Notify.List(ctx, s.Owner.ID, notify.ListOptions{Kind: notify.KindSystem})
		if err != nil || len(list) != 1 {
			t.Fatalf("owner notifications: %d, %v", len(list), err)
		}
		n := list[0]
		wantLink := "/studio/p/" + s.ProjectID + "/chapters/" + strconv.FormatFloat(p.Number, 'f', -1, 64) + "/publish?lang=en"
		if n.Link != wantLink || n.ActorID != e.ta.Admin.ID {
			t.Fatalf("notification %+v, want link %s", n, wantLink)
		}
	})
}
