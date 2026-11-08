package collab_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/collab"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestReviewCommentsRepliesAndStatusRules(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		tm := newTeam(t, ta)
		// Anyone with project access comments; the pin defaults to the panel corner.
		c := tm.comment(t, tm.letterer, tm.page1, 2, "Bubble 2 crowds Kaito’s face; moved it.", "")
		if c.Label != "1.3" || c.Status != collab.StatusOpen || c.Author.ID != tm.letterer.user.ID || c.Author.Role != studio.RoleLetterer || c.X <= 0 || c.Y <= 0 {
			t.Fatalf("comment = %#v", c)
		}
		if !equal(c.Next, collab.StatusDone) {
			t.Fatalf("author may only mark done, got %v", c.Next)
		}
		v := review(t, tm.owner.client.Get(tm.reviewPath(tm.page1)), http.StatusOK)
		if v.PageNumber != 1 || v.Counts.Open != 1 || v.Counts.Resolved != 0 || !v.Viewer.CanReview || !v.Viewer.CanManage || len(v.Members) != 5 || v.Members[0].Role != studio.RoleOwner {
			t.Fatalf("owner review = %#v", v)
		}
		if got := find(v, c.ID).Next; !equal(got, collab.StatusNeedsChanges, collab.StatusDone, collab.StatusApproved) {
			t.Fatalf("reviewer statuses = %v", got)
		}
		if other := review(t, tm.owner.client.Get(tm.reviewPath(tm.page2)), http.StatusOK); len(other.Comments) != 0 {
			t.Fatalf("page 2 shows page 1 comments: %#v", other.Comments)
		}

		// Reviewers (owner, editor) set needs changes and approved; others cannot.
		if r := tm.setStatus(tm.letterer, c.ID, collab.StatusApproved); r.StatusCode != http.StatusForbidden {
			t.Fatalf("author approves own comment = %d %s", r.StatusCode, r.Body)
		}
		if r := tm.setStatus(tm.viewer, c.ID, collab.StatusDone); r.StatusCode != http.StatusForbidden {
			t.Fatalf("viewer marks someone else's comment done = %d", r.StatusCode)
		}
		if got := find(review(t, tm.setStatus(tm.owner, c.ID, collab.StatusNeedsChanges), http.StatusOK), c.ID); got.Status != collab.StatusNeedsChanges {
			t.Fatalf("needs changes = %#v", got)
		}
		if r := tm.setStatus(tm.letterer, c.ID, "closed"); r.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("unknown status = %d", r.StatusCode)
		}
		// The author marks it done and may reopen it from done.
		if got := find(review(t, tm.setStatus(tm.letterer, c.ID, collab.StatusDone), http.StatusOK), c.ID); got.Status != collab.StatusDone || !equal(got.Next, collab.StatusOpen) {
			t.Fatalf("done = %#v", got)
		}
		// Replies: anyone with access, attached to the root.
		r := tm.viewer.client.PostJSON(tm.collabBase+"/comments/"+c.ID+"/replies", map[string]string{"body": "Looks clearer now."})
		got := find(review(t, r, http.StatusCreated), c.ID)
		if len(got.Replies) != 1 || got.Replies[0].Author.ID != tm.viewer.user.ID || got.Replies[0].Body != "Looks clearer now." {
			t.Fatalf("reply = %#v", got.Replies)
		}
		replyID := r.Header.Get("X-Comment-ID")
		if r := tm.viewer.client.PostJSON(tm.collabBase+"/comments/"+replyID+"/replies", map[string]string{"body": "nested"}); r.StatusCode != http.StatusNotFound {
			t.Fatalf("reply to a reply = %d", r.StatusCode)
		}
		if r := tm.setStatus(tm.owner, replyID, collab.StatusApproved); r.StatusCode != http.StatusNotFound {
			t.Fatalf("status on a reply = %d", r.StatusCode)
		}
		// The editor approves it: resolved.
		v = review(t, tm.setStatus(tm.editor, c.ID, collab.StatusApproved), http.StatusOK)
		got = find(v, c.ID)
		if got.Status != collab.StatusApproved || got.ResolvedBy == nil || got.ResolvedBy.ID != tm.editor.user.ID || v.Counts.Open != 0 || v.Counts.Resolved != 1 {
			t.Fatalf("approved = %#v counts %#v", got, v.Counts)
		}
		if r := tm.setStatus(tm.letterer, c.ID, collab.StatusDone); r.StatusCode != http.StatusForbidden {
			t.Fatalf("author changes an approved comment = %d", r.StatusCode)
		}

		// Validation and isolation.
		for _, bad := range []map[string]any{
			{"panel_id": tm.page1.Panels[0].ID, "body": "  "},
			{"panel_id": tm.page1.Panels[0].ID, "body": strings.Repeat("x", 2001)},
			{"panel_id": tm.page2.Panels[0].ID, "body": "wrong page"},
			{"panel_id": tm.page1.Panels[0].ID, "body": "half a pin", "x": 10},
			{"panel_id": tm.page1.Panels[0].ID, "body": "stranger", "assignee_id": ta.Admin.ID},
		} {
			if r := tm.letterer.client.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/comments", bad); r.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("bad comment %v = %d %s", bad, r.StatusCode, r.Body)
			}
		}
		if r := tm.letterer.client.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/comments", map[string]any{"panel_id": tm.page1.Panels[0].ID, "body": "x", "extra": 1}); r.StatusCode != http.StatusBadRequest {
			t.Fatalf("unknown field = %d", r.StatusCode)
		}
		_, outsider := ta.UserClient(t, auth.RoleCreator)
		if r := outsider.Get(tm.reviewPath(tm.page1)); r.StatusCode != http.StatusNotFound {
			t.Fatalf("outsider review = %d", r.StatusCode)
		}
		if r := outsider.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/comments", map[string]any{"panel_id": tm.page1.Panels[0].ID, "body": "hi"}); r.StatusCode != http.StatusNotFound {
			t.Fatalf("outsider comment = %d", r.StatusCode)
		}
		// Pins outside the page are clamped to it.
		r = tm.letterer.client.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/comments", map[string]any{"panel_id": tm.page1.Panels[4].ID, "body": "Lamp should be dark.", "x": 9000, "y": -5})
		pin := find(review(t, r, http.StatusCreated), r.Header.Get("X-Comment-ID"))
		if pin.X != float64(tm.page1.Width) || pin.Y != 0 || pin.Label != "1.5" {
			t.Fatalf("pin = %#v", pin)
		}
	})
}

func TestAssigneeMarksDone(t *testing.T) {
	// Needs migration 1000 (comments.assignee_id).
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		tm := newTeam(t, ta)
		c := tm.comment(t, tm.owner, tm.page1, 0, "Left hand looks off. Fix just that region?", tm.artist.user.ID)
		if c.Assignee == nil || c.Assignee.ID != tm.artist.user.ID || c.Assignee.Role != studio.RoleArtist {
			t.Fatalf("assignee = %#v", c.Assignee)
		}
		if r := tm.setStatus(tm.editor, c.ID, collab.StatusNeedsChanges); r.StatusCode != http.StatusOK {
			t.Fatalf("needs changes = %d", r.StatusCode)
		}
		if got := find(review(t, tm.setStatus(tm.artist, c.ID, collab.StatusDone), http.StatusOK), c.ID); got.Status != collab.StatusDone {
			t.Fatalf("assignee done = %#v", got)
		}
		if r := tm.setStatus(tm.letterer, c.ID, collab.StatusOpen); r.StatusCode != http.StatusForbidden {
			t.Fatalf("unrelated member reopens = %d", r.StatusCode)
		}
		// The assignee was told when it was assigned.
		list, err := ta.Deps.Notify.List(context.Background(), tm.artist.user.ID, notify.ListOptions{Limit: 10})
		if err != nil || len(list) != 1 || list[0].Kind != notify.KindReview || !strings.Contains(list[0].Title, "1.1") {
			t.Fatalf("assignment notification = %#v %v", list, err)
		}
	})
}

func TestPageApprovalRules(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		tm := newTeam(t, ta)
		approve := func(m member, version int) *testutil.Response {
			return m.client.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/approval", map[string]int{"version": version})
		}
		for _, m := range []member{tm.letterer, tm.artist, tm.viewer} {
			if r := approve(m, tm.page1.Version); r.StatusCode != http.StatusForbidden {
				t.Fatalf("non-reviewer approves = %d", r.StatusCode)
			}
		}
		if r := approve(tm.owner, tm.page1.Version-1); r.StatusCode != http.StatusConflict {
			t.Fatalf("stale approval = %d %s", r.StatusCode, r.Body)
		}
		v := review(t, approve(tm.editor, tm.page1.Version), http.StatusOK)
		if v.Approval == nil || !v.Approval.Current || v.Approval.By.ID != tm.editor.user.ID || v.Approval.Version != tm.page1.Version {
			t.Fatalf("approval = %#v", v.Approval)
		}
		// Approving the same version again records nothing new.
		review(t, approve(tm.owner, tm.page1.Version), http.StatusOK)
		var rows int
		if err := ta.Deps.DB.Get(context.Background(), &rows, `SELECT COUNT(*) FROM comments WHERE target_kind = 'page' AND target_id = ?`, tm.page1.ID); err != nil || rows != 1 {
			t.Fatalf("approval rows = %d %v", rows, err)
		}
		// A later change shows the approval as no longer current.
		p := tm.loadPage(t, tm.owner.client, tm.page1.ID).Page
		p.Panels[0].Prompt = "Rin vaults the railing."
		editorPage(t, tm.owner.client.SendJSON(http.MethodPut, tm.editorBase+"/pages/"+p.ID, map[string]any{"page": p, "base_version": p.Version}), http.StatusOK)
		if v := review(t, tm.owner.client.Get(tm.reviewPath(tm.page1)), http.StatusOK); v.Approval == nil || v.Approval.Current {
			t.Fatalf("approval after change = %#v", v.Approval)
		}
		// Approval is a person's decision: never through a token.
		raw, _, err := ta.Deps.Auth.CreateToken(context.Background(), auth.TokenInput{UserID: tm.owner.user.ID, Kind: auth.TokenAgent, Name: "review-bot",
			Scopes: []string{"projects:read", "projects:write"}})
		if err != nil {
			t.Fatal(err)
		}
		bot := ta.BearerClient(raw)
		if r := bot.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/approval", map[string]int{"version": p.Version + 1}); r.StatusCode != http.StatusForbidden {
			t.Fatalf("token approval = %d", r.StatusCode)
		}
		var audit int
		if err := ta.Deps.DB.Get(context.Background(), &audit, `SELECT COUNT(*) FROM audit_log WHERE action = 'page.approve' AND target_id = ?`, tm.page1.ID); err != nil || audit != 1 {
			t.Fatalf("approval audit = %d %v", audit, err)
		}
	})
}

func TestTokensReadAndCommentWithinTheirScopes(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		tm := newTeam(t, ta)
		token := func(scopes []string, projects []string) *testutil.Client {
			// A personal API token: agent tokens never reach collaboration
			// (TestReviewAgentTokensNeverReachCollaboration).
			raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: tm.letterer.user.ID, Kind: auth.TokenAPI, Name: "letter-bot", Scopes: scopes, ProjectIDs: projects})
			if err != nil {
				t.Fatal(err)
			}
			return ta.BearerClient(raw)
		}
		reader := token([]string{"projects:read"}, []string{tm.project.ID})
		if v := review(t, reader.Get(tm.reviewPath(tm.page1)), http.StatusOK); v.Viewer.CanComment || v.Viewer.CanReview || v.Viewer.CanManage {
			t.Fatalf("read token viewer = %#v", v.Viewer)
		}
		post := map[string]any{"panel_id": tm.page1.Panels[1].ID, "body": "Agent: bubble overflows."}
		if r := reader.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/comments", post); r.StatusCode != http.StatusForbidden {
			t.Fatalf("read token comments = %d", r.StatusCode)
		}
		writer := token([]string{"projects:read", "projects:write"}, []string{tm.project.ID})
		r := writer.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/comments", post)
		c := find(review(t, r, http.StatusCreated), r.Header.Get("X-Comment-ID"))
		if !c.Agent || c.Author.ID != tm.letterer.user.ID || len(c.Next) != 0 {
			t.Fatalf("agent comment = %#v", c)
		}
		if r := writer.PostJSON(tm.collabBase+"/comments/"+c.ID+"/status", map[string]string{"status": collab.StatusDone}); r.StatusCode != http.StatusForbidden {
			t.Fatalf("token status = %d", r.StatusCode)
		}
		if r := writer.PostJSON(tm.collabBase+"/members", map[string]any{"username": tm.viewer.user.Username, "role": "viewer"}); r.StatusCode != http.StatusForbidden {
			t.Fatalf("token adds member = %d", r.StatusCode)
		}
		elsewhere := token([]string{"projects:read", "projects:write"}, []string{ta.Admin.ID})
		if r := elsewhere.Get(tm.reviewPath(tm.page1)); r.StatusCode != http.StatusNotFound {
			t.Fatalf("token for another project = %d", r.StatusCode)
		}
	})
}

func TestReviewNotifications(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		tm := newTeam(t, ta)
		c := tm.comment(t, tm.letterer, tm.page1, 2, "Bubble 2 crowds Kaito’s face.", "")
		// The author's own reply notifies nobody.
		review(t, tm.letterer.client.PostJSON(tm.collabBase+"/comments/"+c.ID+"/replies", map[string]string{"body": "Moved it."}), http.StatusCreated)
		if list, _ := ta.Deps.Notify.List(ctx, tm.letterer.user.ID, notify.ListOptions{Limit: 10}); len(list) != 0 {
			t.Fatalf("self notification = %#v", list)
		}
		review(t, tm.owner.client.PostJSON(tm.collabBase+"/comments/"+c.ID+"/replies", map[string]string{"body": "Thanks."}), http.StatusCreated)
		list, err := ta.Deps.Notify.List(ctx, tm.letterer.user.ID, notify.ListOptions{Limit: 10})
		if err != nil || len(list) != 1 || list[0].Kind != notify.KindReview || !strings.Contains(list[0].Title, "1.3") ||
			list[0].Link != "/studio/p/"+tm.project.ID+"/chapters/"+tm.chapter.ID+"/edit?page=1&panel=review" {
			t.Fatalf("reply notification = %#v %v", list, err)
		}
		if _, err := ta.Deps.Notify.MarkKindsRead(ctx, tm.letterer.user.ID, []string{notify.KindReview}, nil); err != nil {
			t.Fatal(err)
		}
		review(t, tm.setStatus(tm.owner, c.ID, collab.StatusNeedsChanges), http.StatusOK)
		list, _ = ta.Deps.Notify.List(ctx, tm.letterer.user.ID, notify.ListOptions{Limit: 10})
		if len(list) != 2 || !strings.Contains(list[0].Title, "Needs changes") || list[0].ReadAt != nil {
			t.Fatalf("status notification = %#v", list)
		}
		if mine, _ := ta.Deps.Notify.List(ctx, tm.owner.user.ID, notify.ListOptions{Limit: 10}); len(mine) != 0 {
			t.Fatalf("actor notified = %#v", mine)
		}
	})
}

func equal(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
