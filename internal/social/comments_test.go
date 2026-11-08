package social_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func seriesComment(s publishtest.Series, body string) map[string]any {
	return map[string]any{"series_id": s.ID, "target_kind": "series", "target_id": s.ID, "body": body}
}

func TestCommentTargetsAndAnchors(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{Languages: []string{"en", "ja"}})
		ch := e.f.Publish(s, publishtest.PublicationSpec{Pages: 3})
		ja := e.f.Publish(s, publishtest.PublicationSpec{ChapterID: ch.ChapterID, Lang: "ja", Pages: 2})
		u, c := e.user(auth.RoleReader)

		sc := postComment(t, c, seriesComment(s, "  Pip stealing the ferry schedule  "), http.StatusCreated)
		if sc.Body != "Pip stealing the ferry schedule" || sc.TargetID != s.ID || sc.Author.ID != u.ID || !sc.CanEdit || sc.CanHide || sc.CanReport {
			t.Fatalf("series comment = %+v", sc)
		}
		cc := postComment(t, c, map[string]any{"series_id": s.ID, "target_kind": "chapter", "target_id": ch.ID,
			"anchor": map[string]any{"page": 2}, "body": "Page two!"}, http.StatusCreated)
		if cc.Anchor.Page != 2 || cc.TargetKind != "chapter" {
			t.Fatalf("chapter comment = %+v", cc)
		}
		pc := postComment(t, c, map[string]any{"series_id": s.ID, "target_kind": "panel", "target_id": ch.ID,
			"anchor": map[string]any{"page": 3, "panel": 2}, "body": "That gull."}, http.StatusCreated)
		if pc.Anchor.Page != 3 || pc.Anchor.Panel != 2 {
			t.Fatalf("panel comment = %+v", pc)
		}
		// The Japanese publication is its own target.
		postComment(t, c, map[string]any{"series_id": s.ID, "target_kind": "panel", "target_id": ja.ID,
			"anchor": map[string]any{"page": 1, "panel": 1}, "body": "カモメ"}, http.StatusCreated)

		bad := []map[string]any{
			{"series_id": s.ID, "target_kind": "panel", "target_id": ch.ID, "anchor": map[string]any{"page": 4, "panel": 1}, "body": "x"},
			{"series_id": s.ID, "target_kind": "panel", "target_id": ch.ID, "anchor": map[string]any{"page": 1, "panel": 9}, "body": "x"},
			{"series_id": s.ID, "target_kind": "panel", "target_id": ch.ID, "body": "x"},
			{"series_id": s.ID, "target_kind": "chapter", "target_id": ch.ID, "anchor": map[string]any{"page": 9}, "body": "x"},
			{"series_id": s.ID, "target_kind": "series", "target_id": s.ID, "anchor": map[string]any{"page": 1}, "body": "x"},
			{"series_id": s.ID, "target_kind": "page", "target_id": ch.ID, "body": "x"},
			{"series_id": s.ID, "target_kind": "series", "target_id": s.ID, "body": "   "},
			{"series_id": s.ID, "target_kind": "series", "target_id": s.ID, "body": strings.Repeat("あ", social.MaxCommentRunes+1)},
			{"series_id": s.ID, "target_kind": "series", "target_id": s.ID, "body": "x", "parent_id": "01nosuchcomment000000000000"},
			{"series_id": s.ID, "target_kind": "series", "target_id": s.ID, "body": "x", "parent_id": cc.ID},
		}
		// Each request gets its own account: every attempt spends the writer's comment budget.
		for i, b := range bad {
			_, bc := e.user(auth.RoleReader)
			r := bc.PostJSON("/api/v1/social/comments", b)
			wantStatus(t, "bad comment "+string(rune('a'+i)), r, http.StatusUnprocessableEntity)
		}
		// A target from another series is not found.
		other := e.f.Series(publishtest.SeriesSpec{})
		_, c2 := e.user(auth.RoleReader)
		wantStatus(t, "cross-series target", c2.PostJSON("/api/v1/social/comments", map[string]any{"series_id": other.ID,
			"target_kind": "chapter", "target_id": ch.ID, "body": "x"}), http.StatusNotFound)
		wantStatus(t, "series target mismatch", c2.PostJSON("/api/v1/social/comments", map[string]any{"series_id": s.ID,
			"target_kind": "series", "target_id": other.ID, "body": "x"}), http.StatusNotFound)

		// Replies stay on their target; the list filters by anchor.
		reply := postComment(t, c, map[string]any{"series_id": s.ID, "target_kind": "panel", "target_id": ch.ID,
			"anchor": map[string]any{"page": 3, "panel": 2}, "body": "Agreed", "parent_id": pc.ID}, http.StatusCreated)
		if reply.ParentID != pc.ID {
			t.Fatalf("reply parent = %q", reply.ParentID)
		}
		anon := e.ta.NewClient()
		p := listComments(t, anon, commentsURL(s.ID, "panel", ch.ID, "page", "3", "panel", "2"))
		if len(p.Items) != 2 || p.Items[0].ID != reply.ID || p.Items[1].ID != pc.ID {
			t.Fatalf("panel list = %+v", p.Items)
		}
		if p.Items[0].CanEdit || p.Items[0].CanReport {
			t.Fatalf("anonymous permissions = %+v", p.Items[0])
		}
		if p := listComments(t, anon, commentsURL(s.ID, "panel", ch.ID, "page", "1", "panel", "1")); len(p.Items) != 0 {
			t.Fatalf("other panel = %+v", p.Items)
		}
		if p := listComments(t, anon, commentsURL(s.ID, "chapter", ch.ID)); len(p.Items) != 1 || p.Items[0].ID != cc.ID {
			t.Fatalf("chapter list = %+v", p.Items)
		}
		if p := listComments(t, anon, commentsURL(s.ID, "series", s.ID)); len(p.Items) != 1 || p.Items[0].ID != sc.ID {
			t.Fatalf("series list = %+v", p.Items)
		}
	})
}

func TestCommentsNewestFirstInBoundedPages(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		u := e.f.User(auth.RoleReader)
		base := time.Now().UTC().Add(-time.Hour)
		for i := range 7 {
			at := base.Add(time.Duration(i/2) * time.Second) // pairs share a timestamp
			e.exec(`INSERT INTO comments (id, project_id, series_id, target_kind, target_id, anchor, visibility, author_id, body,
				status, created_at, updated_at) VALUES (?, ?, ?, 'series', ?, '{}', 'public', ?, ?, 'open', ?, ?)`,
				"c"+string(rune('a'+i)), s.ProjectID, s.ID, s.ID, u.ID, "comment "+string(rune('a'+i)), at, at)
		}
		// Internal Studio comments never appear.
		e.exec(`INSERT INTO comments (id, project_id, series_id, target_kind, target_id, anchor, visibility, author_id, body,
			status, created_at, updated_at) VALUES ('internal1', ?, ?, 'series', ?, '{}', 'internal', ?, 'secret', 'open', ?, ?)`,
			s.ProjectID, s.ID, s.ID, u.ID, base, base)
		anon := e.ta.NewClient()
		var got []string
		cursor := ""
		for range 10 {
			p := listComments(t, anon, commentsURL(s.ID, "series", s.ID, "limit", "3", "cursor", cursor))
			if len(p.Items) > 3 {
				t.Fatalf("page of %d", len(p.Items))
			}
			for _, it := range p.Items {
				got = append(got, it.ID)
			}
			if p.NextCursor == "" {
				break
			}
			cursor = p.NextCursor
		}
		want := "cg,cf,ce,cd,cc,cb,ca"
		if strings.Join(got, ",") != want {
			t.Fatalf("order = %v, want %s", got, want)
		}
		wantStatus(t, "bad cursor", anon.Get(commentsURL(s.ID, "series", s.ID, "cursor", "zzz")), http.StatusBadRequest)
		if p := listComments(t, anon, commentsURL(s.ID, "series", s.ID, "limit", "1000")); len(p.Items) != 7 {
			t.Fatalf("capped list = %d", len(p.Items))
		}
	})
}

// TestCommentAccessFollowsPublish: comments are read and written only where
// publish.Service lets the viewer read the series and publication.
func TestCommentAccessFollowsPublish(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		owner := e.f.User(auth.RoleCreator)
		_, memberClient := e.user(auth.RoleReader)
		anon := e.ta.NewClient()

		type tcase struct {
			name          string
			spec          publishtest.SeriesSpec
			pub           publishtest.PublicationSpec
			anonRead      bool
			memberRead    bool
			projectMember bool // studioViewer may read
		}
		cases := []tcase{
			{"public", publishtest.SeriesSpec{}, publishtest.PublicationSpec{}, true, true, true},
			{"unlisted", publishtest.SeriesSpec{Visibility: publish.VisibilityUnlisted}, publishtest.PublicationSpec{}, true, true, true},
			{"members", publishtest.SeriesSpec{Visibility: publish.VisibilityMembers}, publishtest.PublicationSpec{}, false, true, true},
			{"private", publishtest.SeriesSpec{Visibility: publish.VisibilityPrivate}, publishtest.PublicationSpec{}, false, false, true},
			{"mature", publishtest.SeriesSpec{AgeRating: publish.RatingMature}, publishtest.PublicationSpec{}, false, true, true},
			{"members chapter", publishtest.SeriesSpec{}, publishtest.PublicationSpec{Visibility: publish.VisibilityMembers}, false, true, true},
			{"scheduled chapter", publishtest.SeriesSpec{}, publishtest.PublicationSpec{Status: publish.PublicationScheduled}, false, false, false},
			{"unpublished chapter", publishtest.SeriesSpec{}, publishtest.PublicationSpec{Status: publish.PublicationUnpublished}, false, false, false},
		}
		for _, tc := range cases {
			tc.spec.Owner = owner
			s := e.f.Series(tc.spec)
			// Fresh writers per case keep every write inside the comment budget.
			_, writer := e.user(auth.RoleReader)
			studioViewer, studioClient := e.user(auth.RoleReader)
			e.f.AddMember(s.ProjectID, studioViewer.ID, studio.RoleViewer)
			ch := e.f.Publish(s, tc.pub)
			chapterBody := map[string]any{"series_id": s.ID, "target_kind": "chapter", "target_id": ch.ID, "body": "hello"}

			status := func(ok bool, yes int) int {
				if ok {
					return yes
				}
				return http.StatusNotFound
			}
			wantStatus(t, tc.name+": anonymous read", anon.Get(commentsURL(s.ID, "chapter", ch.ID)), status(tc.anonRead, http.StatusOK))
			wantStatus(t, tc.name+": member read", memberClient.Get(commentsURL(s.ID, "chapter", ch.ID)), status(tc.memberRead, http.StatusOK))
			wantStatus(t, tc.name+": member write", writer.PostJSON("/api/v1/social/comments", chapterBody), status(tc.memberRead, http.StatusCreated))
			wantStatus(t, tc.name+": project member write", studioClient.PostJSON("/api/v1/social/comments", chapterBody), status(tc.projectMember, http.StatusCreated))
			wantStatus(t, tc.name+": anonymous write", anon.PostJSON("/api/v1/social/comments", chapterBody), http.StatusUnauthorized)
		}

		// Personal mode: anonymous visitors read nothing, members still read public series.
		s := e.f.Series(publishtest.SeriesSpec{Owner: owner})
		ch := e.f.Publish(s, publishtest.PublicationSpec{})
		e.f.SetMode(settings.ModePersonal)
		wantStatus(t, "personal anonymous", anon.Get(commentsURL(s.ID, "chapter", ch.ID)), http.StatusNotFound)
		wantStatus(t, "personal member", memberClient.Get(commentsURL(s.ID, "chapter", ch.ID)), http.StatusOK)
	})
}

func TestCommentModeration(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		owner, ownerClient := e.user(auth.RoleCreator)
		s := e.f.Series(publishtest.SeriesSpec{Owner: owner})
		_, authorClient := e.user(auth.RoleReader)
		_, otherClient := e.user(auth.RoleReader)
		anon := e.ta.NewClient()

		c := postComment(t, authorClient, seriesComment(s, "first take"), http.StatusCreated)
		path := "/api/v1/social/comments/" + c.ID

		// Only the author edits or deletes.
		wantStatus(t, "other edits", otherClient.SendJSON(http.MethodPatch, path, map[string]any{"body": "hijack"}), http.StatusForbidden)
		wantStatus(t, "owner edits", ownerClient.SendJSON(http.MethodPatch, path, map[string]any{"body": "hijack"}), http.StatusForbidden)
		wantStatus(t, "other deletes", otherClient.Do(http.MethodDelete, path, nil, ""), http.StatusForbidden)
		wantStatus(t, "anonymous edits", anon.SendJSON(http.MethodPatch, path, map[string]any{"body": "x"}), http.StatusUnauthorized)
		r := authorClient.SendJSON(http.MethodPatch, path, map[string]any{"body": "second take"})
		wantStatus(t, "author edits", r, http.StatusOK)
		var edited commentJSON
		r.JSON(t, &edited)
		if edited.Body != "second take" || edited.EditedAt == nil {
			t.Fatalf("edited = %+v", edited)
		}
		wantStatus(t, "empty edit", authorClient.SendJSON(http.MethodPatch, path, map[string]any{"body": ""}), http.StatusUnprocessableEntity)

		// Only the series owner hides; hidden comments disappear for everyone else.
		wantStatus(t, "other hides", otherClient.PostJSON(path+"/hide", nil), http.StatusForbidden)
		wantStatus(t, "author hides", authorClient.PostJSON(path+"/hide", nil), http.StatusForbidden)
		r = ownerClient.PostJSON(path+"/hide", nil)
		wantStatus(t, "owner hides", r, http.StatusOK)
		if m := r.Map(t); m["hidden"] != true {
			t.Fatalf("hide = %v", m)
		}
		list := commentsURL(s.ID, "series", s.ID)
		if p := listComments(t, anon, list); len(p.Items) != 0 {
			t.Fatalf("anonymous sees hidden: %+v", p.Items)
		}
		if p := listComments(t, otherClient, list); len(p.Items) != 0 {
			t.Fatalf("other sees hidden: %+v", p.Items)
		}
		if p := listComments(t, authorClient, list); len(p.Items) != 1 || !p.Items[0].Hidden {
			t.Fatalf("author list = %+v", p.Items)
		}
		if p := listComments(t, ownerClient, list); len(p.Items) != 1 || !p.Items[0].Hidden || !p.Items[0].CanHide {
			t.Fatalf("owner list = %+v", p.Items)
		}
		wantStatus(t, "report hidden", otherClient.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "comment",
			"target_id": c.ID, "reason": "spam"}), http.StatusNotFound)
		wantStatus(t, "edit hidden as other", otherClient.SendJSON(http.MethodPatch, path, map[string]any{"body": "x"}), http.StatusNotFound)
		wantStatus(t, "owner unhides", ownerClient.Do(http.MethodDelete, path+"/hide", nil, ""), http.StatusOK)
		if p := listComments(t, anon, list); len(p.Items) != 1 || p.Items[0].Hidden {
			t.Fatalf("after unhide = %+v", p.Items)
		}

		wantStatus(t, "author deletes", authorClient.Do(http.MethodDelete, path, nil, ""), http.StatusNoContent)
		if p := listComments(t, ownerClient, list); len(p.Items) != 0 {
			t.Fatalf("deleted still listed: %+v", p.Items)
		}
		if n := e.count(`SELECT COUNT(*) FROM comments WHERE id = ? AND body = '' AND deleted_at IS NOT NULL`, c.ID); n != 1 {
			t.Fatal("delete did not clear the body")
		}
		wantStatus(t, "delete twice", authorClient.Do(http.MethodDelete, path, nil, ""), http.StatusNotFound)
	})
}

func TestReports(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		ch := e.f.Publish(s, publishtest.PublicationSpec{})
		private := e.f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityPrivate})
		author, authorClient := e.user(auth.RoleReader)
		reporter, reporterClient := e.user(auth.RoleReader)
		c := postComment(t, authorClient, seriesComment(s, "buy followers at example.com"), http.StatusCreated)

		r := reporterClient.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "comment", "target_id": c.ID,
			"reason": "spam", "details": "link spam"})
		wantStatus(t, "report comment", r, http.StatusCreated)
		first := r.Map(t)
		r = reporterClient.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "comment", "target_id": c.ID, "reason": "harassment"})
		wantStatus(t, "report again", r, http.StatusOK)
		if again := r.Map(t); again["id"] != first["id"] || again["existing"] != true {
			t.Fatalf("duplicate report = %v (first %v)", again, first)
		}
		if n := e.count(`SELECT COUNT(*) FROM reports WHERE reporter_id = ? AND target_kind = 'comment' AND target_id = ?
			AND reason = 'spam' AND details = 'link spam' AND status = 'open'`, reporter.ID, c.ID); n != 1 {
			t.Fatalf("report rows = %d", n)
		}
		wantStatus(t, "report series", reporterClient.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "series",
			"target_id": s.ID, "reason": "ai_label"}), http.StatusCreated)
		wantStatus(t, "report chapter", reporterClient.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "chapter",
			"target_id": ch.ID, "series_id": s.ID, "reason": "unmarked_mature"}), http.StatusCreated)
		wantStatus(t, "report user", reporterClient.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "user",
			"target_id": author.ID, "reason": "harassment"}), http.StatusCreated)
		// The error cases use other accounts: the reporter spent its burst of five.
		reporter2, reporterClient2 := e.user(auth.RoleReader)
		wantStatus(t, "report self", reporterClient2.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "user",
			"target_id": reporter2.ID, "reason": "other"}), http.StatusUnprocessableEntity)
		wantStatus(t, "reporter limited", reporterClient.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "user",
			"target_id": reporter2.ID, "reason": "other"}), http.StatusTooManyRequests)
		wantStatus(t, "report own comment", authorClient.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "comment",
			"target_id": c.ID, "reason": "other"}), http.StatusUnprocessableEntity)
		wantStatus(t, "report unreadable series", reporterClient2.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "series",
			"target_id": private.ID, "reason": "copyright"}), http.StatusNotFound)
		wantStatus(t, "bad reason", reporterClient2.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "series",
			"target_id": s.ID, "reason": "boring"}), http.StatusUnprocessableEntity)
		wantStatus(t, "bad kind", reporterClient2.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "page",
			"target_id": s.ID, "reason": "spam"}), http.StatusUnprocessableEntity)
		wantStatus(t, "anonymous report", e.ta.NewClient().PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "series",
			"target_id": s.ID, "reason": "spam"}), http.StatusUnauthorized)
	})
}

func TestCommentAndReportWritesAreRateLimited(t *testing.T) {
	ta := testutil.NewTestApp(t)
	e := &env{t: t, ta: ta, f: publishtest.New(t, ta)}
	s := e.f.Series(publishtest.SeriesSpec{})
	_, c := e.user(auth.RoleReader)
	for i := range 5 {
		postComment(t, c, seriesComment(s, "comment "+string(rune('a'+i))), http.StatusCreated)
	}
	r := c.PostJSON("/api/v1/social/comments", seriesComment(s, "one too many"))
	wantStatus(t, "sixth comment", r, http.StatusTooManyRequests)
	if r.Header.Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// Reports: five, then limited.
	limited := false
	for i := range 8 {
		target := e.f.Series(publishtest.SeriesSpec{})
		r := c.PostJSON("/api/v1/social/reports", map[string]any{"target_kind": "series", "target_id": target.ID, "reason": "spam"})
		if r.StatusCode == http.StatusTooManyRequests {
			limited = i >= 5
			break
		}
	}
	if !limited {
		t.Fatal("reports were not limited after five")
	}
}
