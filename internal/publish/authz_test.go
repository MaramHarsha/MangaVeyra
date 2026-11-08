package publish_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestPublishAuthorization(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		f := newChapter(t, ta, false)
		routes := []struct {
			method, path string
		}{
			{http.MethodGet, f.base + "/publish"},
			{http.MethodPost, f.base + "/publish"},
			{http.MethodGet, f.base + "/published"},
			{http.MethodPost, f.base + "/publish/unpublish"},
			{http.MethodPost, f.base + "/publish/cover/page"},
			{http.MethodPost, f.base + "/publish/cover/generate"},
			{http.MethodPost, f.base + "/publish/cover/keep"},
			{http.MethodGet, f.base + "/publish/pages/" + f.pages[0].ID + "/image"},
			{http.MethodGet, "/studio/p/" + f.project.ID + "/series"},
			{http.MethodPost, "/studio/p/" + f.project.ID + "/series"},
		}
		send := func(c *testutil.Client, method, path string) int {
			if method == http.MethodGet {
				return c.Get(path).StatusCode
			}
			return postForm(c, path, f.publishForm("en")).StatusCode
		}
		member := func(role string) *testutil.Client {
			u := ta.CreateUser(t, "", auth.RoleCreator)
			pf.AddMember(f.project.ID, u.ID, role)
			return ta.Login(t, u.Username)
		}
		outsider := ta.Login(t, ta.CreateUser(t, "", auth.RoleCreator).Username)
		raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: f.owner.ID, Kind: auth.TokenAgent, Name: "Publishing agent",
			Scopes: []string{"projects:read", "projects:write", "generate", "admin"}, ProjectIDs: []string{f.project.ID}})
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name   string
			client *testutil.Client
			want   int
		}{
			{"non-member", outsider, http.StatusNotFound},
			{"viewer", member(studio.RoleViewer), http.StatusForbidden},
			{"writer", member(studio.RoleWriter), http.StatusForbidden},
			{"artist", member(studio.RoleArtist), http.StatusForbidden},
			{"site admin outside the project", ta.Login(t, ta.Admin.Username), http.StatusForbidden},
			{"agent token", ta.BearerClient(raw), http.StatusForbidden},
		}
		for _, c := range cases {
			for _, rt := range routes {
				if got := send(c.client, rt.method, rt.path); got != c.want {
					t.Errorf("%s %s %s = %d, want %d", c.name, rt.method, rt.path, got, c.want)
				}
			}
		}
		// Anonymous visitors are sent to sign in.
		if got := ta.NewClient().Get(f.base + "/publish"); got.StatusCode != http.StatusSeeOther || !strings.HasPrefix(got.Header.Get("Location"), "/login") {
			t.Errorf("anonymous = %d %q", got.StatusCode, got.Header.Get("Location"))
		}
		// An unknown project or chapter is 404 even for its owner.
		for _, path := range []string{"/studio/p/01hzzzzzzzzzzzzzzzzzzzzzzz/chapters/1/publish", f.base[:strings.LastIndex(f.base, "/")] + "/99/publish"} {
			if got := f.client.Get(path).StatusCode; got != http.StatusNotFound {
				t.Errorf("GET %s = %d", path, got)
			}
		}
		// An editor may publish; nothing was queued by the refused attempts.
		var queued int
		if err := ta.Deps.DB.Get(ctx, &queued, `SELECT COUNT(*) FROM jobs WHERE type = ?`, publish.JobType); err != nil || queued != 0 {
			t.Fatalf("refused attempts queued %d jobs (%v)", queued, err)
		}
		editor := member(studio.RoleEditor)
		if got := editor.Get(f.base + "/publish").StatusCode; got != http.StatusOK {
			t.Fatalf("editor GET = %d", got)
		}
		resp := postForm(editor, f.base+"/publish", f.publishForm("en"))
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("editor publish = %d %s", resp.StatusCode, resp.Body)
		}
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if j := runJob(t, ta, loc.Query().Get("job")); j.State != "done" {
			t.Fatalf("editor publish job %s %+v", j.State, j.Error)
		}
		// Another user's job is not visible.
		if got := f.client.Get(f.base + "/publish?job=" + loc.Query().Get("job")).StatusCode; got != http.StatusNotFound {
			t.Fatalf("someone else's job = %d", got)
		}
	})
}
