package collab_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/collab"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func each(t *testing.T, fn func(*testing.T, *testutil.TestApp)) {
	t.Helper()
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) {
			t.Parallel()
			fn(t, testutil.NewTestApp(t, testutil.WithDialect(d)))
		})
	}
}

type pageResponse struct {
	Page        pagedoc.Page `json:"page"`
	Permissions *struct {
		Art    bool `json:"art"`
		Letter bool `json:"letter"`
	} `json:"permissions"`
}

// member is a signed-in project member.
type member struct {
	user   *auth.User
	client *testutil.Client
}

// team is the Tidebreak Courier review team: an owner, an editor, a letterer
// limited to lettering, an artist limited to page 1 and a viewer, on a
// chapter of two five-panel pages with one bubble on page 1.
type team struct {
	ta                              *testutil.TestApp
	project                         studio.Project
	chapter                         studio.Chapter
	editorBase, collabBase          string
	owner, editor, letterer, artist member
	viewer                          member
	page1, page2                    pagedoc.Page
	letteringScope, pagesScope      studio.MemberScope
}

func newTeam(t *testing.T, ta *testutil.TestApp) *team {
	t.Helper()
	ctx := context.Background()
	tm := &team{ta: ta, letteringScope: studio.MemberScope{Layer: studio.ScopeLayerLettering}, pagesScope: studio.MemberScope{PageFrom: 1, PageTo: 1}}
	signIn := func(name string) member {
		u := ta.CreateUser(t, name+db.NewID()[20:], auth.RoleCreator)
		return member{user: u, client: ta.Login(t, u.Username)}
	}
	tm.owner = signIn("jerry")
	svc := core.MustUse[studio.Service](ta.Deps)
	actor, err := svc.ActorFromUser(ctx, tm.owner.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	tm.project, err = svc.CreateProject(ctx, actor, studio.NewProject{Title: "Tidebreak Courier", Premise: "A courier crosses the flooded harbour.",
		Style: "seinen_bw", Format: "manga_rtl", Language: "en", PageSize: "b5", PageWidth: 600, PageHeight: 800})
	if err != nil {
		t.Fatal(err)
	}
	tm.chapter, err = svc.CreateChapter(ctx, actor, tm.project.ID, "The Lighthouse Debt")
	if err != nil {
		t.Fatal(err)
	}
	tm.editorBase = "/api/v1/editor/p/" + tm.project.ID + "/chapters/" + tm.chapter.ID
	tm.collabBase = "/api/v1/collab/p/" + tm.project.ID
	tm.page1 = editorPage(t, tm.owner.client.PostJSON(tm.editorBase+"/pages", map[string]string{"layout_id": "reveal-5"}), http.StatusCreated)
	tm.page2 = editorPage(t, tm.owner.client.PostJSON(tm.editorBase+"/pages", map[string]string{"layout_id": "reveal-5"}), http.StatusCreated)
	tm.page1.Bubbles = []pagedoc.Bubble{{ID: db.NewID(), PanelID: tm.page1.Panels[0].ID, Kind: "speech", Box: pagedoc.Rect{X: 60, Y: 40, W: 120, H: 80},
		Shape: "ellipse", Style: pagedoc.BubbleStyle{AutoFit: true}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "The tide is turning.", Source: "human", Status: "draft"}}}}
	tm.page1 = editorPage(t, tm.owner.client.SendJSON(http.MethodPut, tm.editorBase+"/pages/"+tm.page1.ID, map[string]any{"page": tm.page1, "base_version": tm.page1.Version}), http.StatusOK)

	tm.editor = signIn("aiko")
	tm.letterer = signIn("jun")
	tm.artist = signIn("sora")
	tm.viewer = signIn("wren")
	for _, add := range []struct {
		m     member
		role  string
		scope studio.MemberScope
	}{{tm.editor, studio.RoleEditor, studio.MemberScope{}}, {tm.letterer, studio.RoleLetterer, tm.letteringScope},
		{tm.artist, studio.RoleArtist, tm.pagesScope}, {tm.viewer, studio.RoleViewer, studio.MemberScope{}}} {
		r := tm.owner.client.PostJSON(tm.collabBase+"/members", map[string]any{"username": add.m.user.Username, "role": add.role, "scope": add.scope})
		if r.StatusCode != http.StatusCreated {
			t.Fatalf("add %s: %d %s", add.role, r.StatusCode, r.Body)
		}
	}
	return tm
}

func editorPage(t *testing.T, r *testutil.Response, status int) pagedoc.Page {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("HTTP %d want %d: %.1200s", r.StatusCode, status, r.Body)
	}
	var out pageResponse
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Page
}

func (tm *team) loadPage(t *testing.T, c *testutil.Client, id string) pageResponse {
	t.Helper()
	r := c.Get(tm.editorBase + "/pages/" + id)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("load page: %d %s", r.StatusCode, r.Body)
	}
	var out pageResponse
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func review(t *testing.T, r *testutil.Response, status int) collab.Review {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("HTTP %d want %d: %.1200s", r.StatusCode, status, r.Body)
	}
	var out collab.Review
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (tm *team) reviewPath(p pagedoc.Page) string {
	return tm.collabBase + "/pages/" + p.ID + "/review"
}

func (tm *team) comment(t *testing.T, m member, p pagedoc.Page, panel int, body, assignee string) collab.Comment {
	t.Helper()
	in := map[string]any{"panel_id": p.Panels[panel].ID, "body": body}
	if assignee != "" {
		in["assignee_id"] = assignee
	}
	r := m.client.PostJSON(tm.collabBase+"/pages/"+p.ID+"/comments", in)
	v := review(t, r, http.StatusCreated)
	id := r.Header.Get("X-Comment-ID")
	for _, c := range v.Comments {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("comment %s missing from %#v", id, v.Comments)
	return collab.Comment{}
}

func (tm *team) setStatus(m member, id, status string) *testutil.Response {
	return m.client.PostJSON(tm.collabBase+"/comments/"+id+"/status", map[string]string{"status": status})
}

func find(v collab.Review, id string) collab.Comment {
	for _, c := range v.Comments {
		if c.ID == id {
			return c
		}
	}
	return collab.Comment{}
}
