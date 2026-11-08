package studio_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestMemberScopeValidateAndParse(t *testing.T) {
	lettering := studio.MemberScope{Layer: studio.ScopeLayerLettering}
	pages := studio.MemberScope{PageFrom: 1, PageTo: 20}
	for _, tc := range []struct {
		role  string
		scope studio.MemberScope
		ok    bool
	}{
		{studio.RoleLetterer, lettering, true},
		{studio.RoleEditor, lettering, true},
		{studio.RoleArtist, lettering, false},
		{studio.RoleArtist, pages, true},
		{studio.RoleLetterer, studio.MemberScope{Layer: studio.ScopeLayerLettering, PageFrom: 3, PageTo: 3}, true},
		{studio.RoleWriter, pages, false},
		{studio.RoleViewer, pages, false},
		{studio.RoleOwner, pages, false},
		{studio.RoleArtist, studio.MemberScope{PageFrom: 4, PageTo: 2}, false},
		{studio.RoleArtist, studio.MemberScope{PageFrom: 0, PageTo: 2}, false},
		{studio.RoleArtist, studio.MemberScope{PageFrom: 1, PageTo: studio.MaxScopePage + 1}, false},
		{studio.RoleArtist, studio.MemberScope{Layer: "fx"}, false},
		{studio.RoleOwner, studio.MemberScope{}, true},
	} {
		if err := tc.scope.Validate(tc.role); (err == nil) != tc.ok {
			t.Errorf("Validate(%s, %+v) = %v, want ok=%v", tc.role, tc.scope, err, tc.ok)
		}
	}
	for raw, want := range map[string]studio.MemberScope{"": {}, "{}": {}, `{"layer":"lettering"}`: lettering, `{"page_from":1,"page_to":20}`: pages} {
		got, err := studio.ParseMemberScope(raw)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v", raw, got, err)
		}
		if raw != "" && got.Encode() != map[bool]string{true: "{}", false: raw}[got.IsZero()] {
			t.Errorf("Encode(%+v) = %s", got, got.Encode())
		}
	}
	for _, raw := range []string{`{"layer":"background"}`, `{"pages":[1,2]}`, `{"page_from":3,"page_to":1}`, `not json`} {
		if _, err := studio.ParseMemberScope(raw); err == nil {
			t.Errorf("Parse(%q) accepted", raw)
		}
	}
}

func TestMemberScopesNarrowAuthorizeAndTargets(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		targets := core.MustUse[studio.TargetAuthorizer](ta.Deps)
		owner, ownerActor, project := createProject(t, ta, "Scoped Roles")
		chapter, err := svc.CreateChapter(ctx, ownerActor, project.ID, "The Lighthouse Debt")
		if err != nil {
			t.Fatal(err)
		}
		var pages []pagedoc.Page
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			var docs []pagedoc.Page
			for range 3 {
				p := pagedoc.New("", project.ID, chapter.ID, 0, 600, 800, "rtl")
				p.Panels = []pagedoc.Panel{{Points: []pagedoc.Point{{X: 0, Y: 0}, {X: 600, Y: 0}, {X: 600, Y: 800}, {X: 0, Y: 800}}, PromptSource: "user",
					Layers: []pagedoc.Layer{{Kind: pagedoc.LayerBackground, Visible: true, Opacity: 1}, {Kind: pagedoc.LayerLettering, Visible: true, Opacity: 1}}}}
				docs = append(docs, p)
			}
			var err error
			pages, err = core.MustUse[pagestore.Service](ta.Deps).CreatePagesTx(ctx, tx, chapter.ID, docs, pagestore.Change{ActorKind: "user", ActorID: owner.ID, Action: "create_page", Summary: "create"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		member := func(role string, scope string) studio.Actor {
			u := ta.CreateUser(t, "", auth.RoleCreator)
			if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, scope, created_at) VALUES (?, ?, ?, ?, ?)`, project.ID, u.ID, role, scope, db.Now()); err != nil {
				t.Fatal(err)
			}
			return studio.Actor{UserID: u.ID}
		}
		pageScope := func(i int) jobs.Scope {
			return jobs.Scope{Kind: jobs.ScopePage, ProjectID: project.ID, PageID: pages[i].ID}
		}
		panelScope := func(i int) jobs.Scope {
			return jobs.Scope{Kind: jobs.ScopePanel, ProjectID: project.ID, PageID: pages[i].ID, PanelID: pages[i].Panels[0].ID}
		}
		layerScope := func(i, l int) jobs.Scope {
			return jobs.Scope{Kind: jobs.ScopeLayer, ProjectID: project.ID, PanelID: pages[i].Panels[0].ID, LayerID: pages[i].Panels[0].Layers[l].ID}
		}
		region := json.RawMessage(`{"mask_asset_id":"x"}`)
		regionScope := func(i int) jobs.Scope {
			return jobs.Scope{Kind: jobs.ScopeRegion, ProjectID: project.ID, LayerID: pages[i].Panels[0].Layers[0].ID, Region: region}
		}
		chapterScope := jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: project.ID, ChapterID: chapter.ID}
		allowed := func(a studio.Actor, s jobs.Scope, act studio.Action) bool {
			err := targets.AuthorizeTarget(ctx, a, s, act)
			if err != nil && !errors.Is(err, studio.ErrForbidden) {
				t.Fatalf("AuthorizeTarget(%+v, %s): %v", s, act, err)
			}
			return err == nil
		}

		artist := member(studio.RoleArtist, `{"page_from":2,"page_to":3}`)
		if svc.Authorize(ctx, artist, project.ID, studio.ActRead) != nil || svc.Authorize(ctx, artist, project.ID, studio.ActEditArt) == nil || svc.Authorize(ctx, artist, project.ID, studio.ActGenerate) == nil {
			t.Fatal("page-range artist: project-level checks must allow reading only")
		}
		for i, want := range []bool{false, true, true} {
			for _, s := range []jobs.Scope{pageScope(i), panelScope(i), layerScope(i, 0), regionScope(i)} {
				if got := allowed(artist, s, studio.ActEditArt); got != want {
					t.Errorf("artist page %d %s: allowed=%v want %v", i+1, s.Kind, got, want)
				}
			}
		}
		if allowed(artist, chapterScope, studio.ActEditArt) || allowed(artist, pageScope(1), studio.ActLetter) {
			t.Error("page-range artist: chapter-wide work or a role the artist lacks was allowed")
		}

		letterer := member(studio.RoleEditor, `{"layer":"lettering"}`)
		for act, want := range map[studio.Action]bool{studio.ActRead: true, studio.ActLetter: true, studio.ActEditArt: false, studio.ActEditScript: false, studio.ActGenerate: false, studio.ActLock: false, studio.ActManage: false} {
			if got := svc.Authorize(ctx, letterer, project.ID, act) == nil; got != want {
				t.Errorf("lettering-only editor %s: allowed=%v want %v", act, got, want)
			}
		}
		if !allowed(letterer, layerScope(0, 1), studio.ActLetter) || allowed(letterer, layerScope(0, 0), studio.ActLetter) || allowed(letterer, pageScope(0), studio.ActEditArt) {
			t.Error("lettering-only editor reached an art layer")
		}

		both := member(studio.RoleLetterer, `{"layer":"lettering","page_from":1,"page_to":1}`)
		if !allowed(both, pageScope(0), studio.ActLetter) || allowed(both, pageScope(1), studio.ActLetter) {
			t.Error("lettering and page limits do not combine")
		}

		broken := member(studio.RoleEditor, `{"layer":"everything"}`)
		if svc.Authorize(ctx, broken, project.ID, studio.ActRead) != nil || svc.Authorize(ctx, broken, project.ID, studio.ActEditArt) == nil {
			t.Error("an unreadable stored scope must leave read access only")
		}

		// The same limits hold for the member's agent tokens.
		_, tok, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: artist.UserID, Kind: auth.TokenAgent, Name: "art-bot",
			Scopes: []string{"projects:read", "projects:write", "generate"}})
		if err != nil {
			t.Fatal(err)
		}
		agent := studio.Actor{UserID: artist.UserID, TokenID: tok.ID}
		if allowed(agent, pageScope(0), studio.ActGenerate) || !allowed(agent, pageScope(2), studio.ActGenerate) || svc.Authorize(ctx, agent, project.ID, studio.ActGenerate) == nil {
			t.Error("agent token escaped the artist's page range")
		}

		// Unscoped members and admins are unaffected, but targets must belong to the project.
		if !allowed(ownerActor, pageScope(0), studio.ActEditArt) || !allowed(studio.Actor{UserID: ta.Admin.ID}, chapterScope, studio.ActManage) {
			t.Error("unscoped access changed")
		}
		_, _, other := createProject(t, ta, "Other Harbour")
		foreign := pageScope(0)
		foreign.ProjectID = other.ID
		if err := targets.AuthorizeTarget(ctx, studio.Actor{UserID: ta.Admin.ID}, foreign, studio.ActRead); !errors.Is(err, studio.ErrForbidden) {
			t.Errorf("foreign target = %v", err)
		}
		wrongPanel := panelScope(0)
		wrongPanel.PageID = pages[1].ID
		if err := targets.AuthorizeTarget(ctx, ownerActor, wrongPanel, studio.ActEditArt); !errors.Is(err, studio.ErrForbidden) {
			t.Errorf("panel under another page = %v", err)
		}
		if err := targets.AuthorizeTarget(ctx, ownerActor, jobs.Scope{Kind: jobs.ScopePage, ProjectID: project.ID}, studio.ActEditArt); !errors.Is(err, studio.ErrInvalid) {
			t.Errorf("incomplete target = %v", err)
		}
	})
}
