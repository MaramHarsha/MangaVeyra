package collab_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func (tm *team) save(m member, p pagedoc.Page) *testutil.Response {
	return m.client.SendJSON(http.MethodPut, tm.editorBase+"/pages/"+p.ID, map[string]any{"page": p, "base_version": p.Version})
}

func TestMembersAPIManagesRolesAndScopes(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		tm := newTeam(t, ta)
		var list struct {
			Members []struct {
				UserID string             `json:"user_id"`
				Role   string             `json:"role"`
				Scope  studio.MemberScope `json:"scope"`
			} `json:"members"`
		}
		tm.viewer.client.Get(tm.collabBase+"/members").JSON(t, &list)
		scopes := map[string]studio.MemberScope{}
		for _, m := range list.Members {
			scopes[m.UserID] = m.Scope
		}
		if len(list.Members) != 5 || list.Members[0].Role != studio.RoleOwner || scopes[tm.letterer.user.ID] != tm.letteringScope || scopes[tm.artist.user.ID] != tm.pagesScope {
			t.Fatalf("members = %#v", list.Members)
		}
		newcomer := ta.CreateUser(t, "", auth.RoleReader)
		for _, bad := range []map[string]any{
			{"username": newcomer.Username, "role": studio.RoleOwner},
			{"username": newcomer.Username, "role": studio.RoleWriter, "scope": map[string]int{"page_from": 1, "page_to": 4}},
			{"username": newcomer.Username, "role": studio.RoleArtist, "scope": map[string]string{"layer": "lettering"}},
			{"username": newcomer.Username, "role": studio.RoleArtist, "scope": map[string]int{"page_from": 5, "page_to": 2}},
			{"username": "nobody-here", "role": studio.RoleViewer},
			{"username": tm.viewer.user.Username, "role": studio.RoleViewer},
		} {
			if r := tm.owner.client.PostJSON(tm.collabBase+"/members", bad); r.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("add %v = %d %s", bad, r.StatusCode, r.Body)
			}
		}
		if r := tm.editor.client.PostJSON(tm.collabBase+"/members", map[string]any{"username": newcomer.Username, "role": studio.RoleViewer}); r.StatusCode != http.StatusForbidden {
			t.Fatalf("editor adds member = %d", r.StatusCode)
		}
		path := tm.collabBase + "/members/" + tm.artist.user.ID
		widen := map[string]any{"role": studio.RoleArtist, "scope": map[string]int{"page_from": 1, "page_to": 20}}
		if r := tm.artist.client.SendJSON(http.MethodPatch, path, widen); r.StatusCode != http.StatusForbidden {
			t.Fatalf("member widens own scope = %d", r.StatusCode)
		}
		if r := tm.owner.client.SendJSON(http.MethodPatch, path, widen); r.StatusCode != http.StatusOK {
			t.Fatalf("owner widens scope = %d %s", r.StatusCode, r.Body)
		}
		if r := tm.owner.client.SendJSON(http.MethodPatch, tm.collabBase+"/members/"+tm.owner.user.ID, map[string]any{"role": studio.RoleViewer}); r.StatusCode != http.StatusForbidden {
			t.Fatalf("owner demoted = %d", r.StatusCode)
		}
		if r := tm.owner.client.Do(http.MethodDelete, tm.collabBase+"/members/"+tm.viewer.user.ID, nil, ""); r.StatusCode != http.StatusOK {
			t.Fatalf("remove = %d", r.StatusCode)
		}
		if r := tm.viewer.client.Get(tm.reviewPath(tm.page1)); r.StatusCode != http.StatusNotFound {
			t.Fatalf("removed member still reads = %d", r.StatusCode)
		}
		var audit int
		if err := ta.Deps.DB.Get(context.Background(), &audit, `SELECT COUNT(*) FROM audit_log WHERE action LIKE 'project.member_%' AND project_id = ?`, tm.project.ID); err != nil || audit != 6 {
			t.Fatalf("member audit rows = %d %v", audit, err)
		}
	})
}

func TestScopedRolesAreEnforcedOnEditPaths(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		tm := newTeam(t, ta)

		// Lettering only: an editor narrowed to the lettering layer changes
		// bubbles but not art, script-wide settings or locks.
		if r := tm.owner.client.SendJSON(http.MethodPatch, tm.collabBase+"/members/"+tm.editor.user.ID, map[string]any{"role": studio.RoleEditor, "scope": tm.letteringScope}); r.StatusCode != http.StatusOK {
			t.Fatalf("narrow editor = %d %s", r.StatusCode, r.Body)
		}
		load := tm.loadPage(t, tm.editor.client, tm.page1.ID)
		if load.Permissions == nil || load.Permissions.Art || !load.Permissions.Letter {
			t.Fatalf("lettering-only permissions = %#v", load.Permissions)
		}
		art := load.Page
		art.Panels[0].Prompt = "Paint over the harbour."
		if r := tm.save(tm.editor, art); r.StatusCode == http.StatusOK {
			t.Fatal("lettering-only editor changed art")
		}
		letter := tm.loadPage(t, tm.editor.client, tm.page1.ID).Page
		letter.Bubbles[0].Texts["en"] = pagedoc.BubbleText{Text: "The tide turned.", Source: "human", Status: "draft"}
		saved := editorPage(t, tm.save(tm.editor, letter), http.StatusOK)
		if saved.Bubbles[0].Texts["en"].Text != "The tide turned." {
			t.Fatalf("lettering save = %#v", saved.Bubbles)
		}
		if r := tm.editor.client.PostJSON(tm.editorBase+"/pages/"+saved.ID+"/lock", map[string]any{"locked": true, "base_version": saved.Version}); r.StatusCode == http.StatusOK {
			t.Fatal("lettering-only editor locked a page")
		}
		studioSvc := core.MustUse[studio.Service](ta.Deps)
		editorActor := studio.Actor{UserID: tm.editor.user.ID}
		if err := studioSvc.Authorize(ctx, editorActor, tm.project.ID, studio.ActEditScript); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("lettering-only editor edits script: %v", err)
		}

		// Page range: the artist changes page 1 only, and no chapter-wide work.
		if p := tm.loadPage(t, tm.artist.client, tm.page2.ID); p.Permissions == nil || p.Permissions.Art {
			t.Fatalf("out-of-range permissions = %#v", p.Permissions)
		}
		inRange := tm.loadPage(t, tm.artist.client, tm.page1.ID)
		if inRange.Permissions == nil || !inRange.Permissions.Art {
			t.Fatalf("in-range permissions = %#v", inRange.Permissions)
		}
		p1 := inRange.Page
		p1.Panels[2].Prompt = "Rin vaults the railing, low angle."
		p1 = editorPage(t, tm.save(tm.artist, p1), http.StatusOK)
		p2 := tm.loadPage(t, tm.artist.client, tm.page2.ID).Page
		p2.Panels[0].Prompt = "Not my page."
		if r := tm.save(tm.artist, p2); r.StatusCode == http.StatusOK {
			t.Fatal("page-range artist changed a page outside the range")
		}
		if r := tm.artist.client.PostJSON(tm.editorBase+"/pages", map[string]string{"layout_id": "splash"}); r.StatusCode == http.StatusCreated {
			t.Fatal("page-range artist added a page (renumbers the chapter)")
		}
		if r := tm.artist.client.Do(http.MethodDelete, tm.editorBase+"/pages/"+p1.ID, nil, ""); r.StatusCode == http.StatusOK {
			t.Fatal("page-range artist deleted a page")
		}
		artistActor := studio.Actor{UserID: tm.artist.user.ID}
		if err := studioSvc.Authorize(ctx, artistActor, tm.project.ID, studio.ActEditArt); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("page-range artist passes a project-wide art check: %v", err)
		}

		// Locks still win inside the range.
		locked := editorPage(t, tm.owner.client.PostJSON(tm.editorBase+"/pages/"+p1.ID+"/lock", map[string]any{"locked": true, "base_version": p1.Version}), http.StatusOK)
		locked.Panels[2].Prompt = "Forbidden while locked."
		if r := tm.save(tm.artist, locked); r.StatusCode != http.StatusLocked {
			t.Fatalf("locked in-range save = %d %s", r.StatusCode, r.Body)
		}
		editorPage(t, tm.owner.client.PostJSON(tm.editorBase+"/pages/"+p1.ID+"/lock", map[string]any{"locked": false, "confirm": true, "base_version": locked.Version}), http.StatusOK)

		// Agents act through the same checks: the artist's agent token is
		// held to the artist's page range for page writes and generation.
		raw, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: tm.artist.user.ID, Kind: auth.TokenAgent, Name: "art-bot",
			Scopes: []string{"projects:read", "projects:write", "generate"}, ProjectIDs: []string{tm.project.ID}})
		if err != nil || raw == "" {
			t.Fatal(err)
		}
		pages := core.MustUse[pagestore.Service](ta.Deps)
		agent := pagestore.Change{ActorKind: "agent", ActorID: tm.artist.user.ID, TokenID: info.ID, Action: "agent.edit", Summary: "agent edit"}
		outside, _ := pages.GetPage(ctx, tm.page2.ID)
		outside.Panels[1].Prompt = "Agent outside the range."
		if _, err := pages.SavePage(ctx, outside, outside.Version, agent); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("agent page write outside the range: %v", err)
		}
		inside, _ := pages.GetPage(ctx, tm.page1.ID)
		inside.Panels[1].Prompt = "Agent inside the range."
		if _, err := pages.SavePage(ctx, inside, inside.Version, agent); err != nil {
			t.Fatalf("agent page write inside the range: %v", err)
		}
		agentActor := studio.Actor{UserID: tm.artist.user.ID, TokenID: info.ID}
		images := core.MustUse[gen.Service](ta.Deps)
		if _, err := images.EnqueuePanel(ctx, agentActor, gen.PanelRequest{ProjectID: tm.project.ID, PageID: tm.page2.ID, PanelID: tm.page2.Panels[0].ID, Prompt: "Harbour at dusk.", Variations: 1}); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("agent generation outside the range: %v", err)
		}
		targets := core.MustUse[studio.TargetAuthorizer](ta.Deps)
		layer := tm.page1.Panels[0].Layers[0]
		if err := targets.AuthorizeTarget(ctx, agentActor, layerScope(tm.project.ID, tm.page1.Panels[0].ID, layer.ID), studio.ActGenerate); err != nil {
			t.Fatalf("agent generation target inside the range: %v", err)
		}
		if err := targets.AuthorizeTarget(ctx, agentActor, layerScope(tm.project.ID, tm.page2.Panels[0].ID, tm.page2.Panels[0].Layers[0].ID), studio.ActGenerate); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("agent generation target outside the range: %v", err)
		}
		// A target from another project is refused even for an unscoped owner.
		if err := targets.AuthorizeTarget(ctx, studio.Actor{UserID: tm.owner.user.ID}, layerScope(db.NewID(), tm.page1.Panels[0].ID, layer.ID), studio.ActEditArt); err == nil {
			t.Fatal("cross-project target accepted")
		}
	})
}

func layerScope(projectID, panelID, layerID string) jobs.Scope {
	return jobs.Scope{Kind: jobs.ScopeLayer, ProjectID: projectID, PanelID: panelID, LayerID: layerID}
}
