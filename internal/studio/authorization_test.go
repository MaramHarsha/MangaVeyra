package studio_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestAuthorizationMatrixAndSiteRoleReload(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		_, _, project := createProject(t, ta, "Role Matrix")

		roles := []string{studio.RoleOwner, studio.RoleWriter, studio.RoleArtist, studio.RoleLetterer, studio.RoleEditor, studio.RoleViewer}
		actions := []studio.Action{studio.ActRead, studio.ActEditScript, studio.ActEditArt, studio.ActLetter, studio.ActGenerate, studio.ActManage, studio.ActLock}
		want := map[string]map[studio.Action]bool{
			studio.RoleOwner:    {studio.ActRead: true, studio.ActEditScript: true, studio.ActEditArt: true, studio.ActLetter: true, studio.ActGenerate: true, studio.ActManage: true, studio.ActLock: true},
			studio.RoleWriter:   {studio.ActRead: true, studio.ActEditScript: true, studio.ActGenerate: true},
			studio.RoleArtist:   {studio.ActRead: true, studio.ActEditArt: true, studio.ActGenerate: true},
			studio.RoleLetterer: {studio.ActRead: true, studio.ActLetter: true},
			studio.RoleEditor:   {studio.ActRead: true, studio.ActEditScript: true, studio.ActEditArt: true, studio.ActLetter: true, studio.ActGenerate: true, studio.ActLock: true},
			studio.RoleViewer:   {studio.ActRead: true},
		}
		for _, role := range roles {
			user := ta.CreateUser(t, "", auth.RoleCreator)
			if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
				project.ID, user.ID, role, db.Now()); err != nil {
				t.Fatalf("insert %s membership: %v", role, err)
			}
			actor := studio.Actor{UserID: user.ID, SiteRole: auth.RoleAdmin}
			for _, action := range actions {
				err := svc.Authorize(ctx, actor, project.ID, action)
				if got := err == nil; got != want[role][action] {
					t.Errorf("Authorize(role=%s, action=%s) error=%v, allowed=%v; want %v", role, action, err, got, want[role][action])
				}
			}
		}

		adminActor := studio.Actor{UserID: ta.Admin.ID, SiteRole: auth.RoleReader}
		for _, action := range actions {
			if err := svc.Authorize(ctx, adminActor, project.ID, action); err != nil {
				t.Errorf("admin action %s: %v", action, err)
			}
		}
		if err := svc.Authorize(ctx, adminActor, "missing", studio.ActRead); !errors.Is(err, studio.ErrNotFound) {
			t.Fatalf("missing project error = %v, want ErrNotFound", err)
		}
	})
}

func TestTokenAuthorizationRechecksStateScopesAndProject(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		_, _, project := createProject(t, ta, "Token Project")
		_, _, other := createProject(t, ta, "Other Project")
		writer := ta.CreateUser(t, "", auth.RoleCreator)
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
			project.ID, writer.ID, studio.RoleWriter, db.Now()); err != nil {
			t.Fatal(err)
		}

		_, full, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: writer.ID, Kind: auth.TokenAgent, Name: "studio-full",
			Scopes: []string{"projects:read", "projects:write", "generate"}, ProjectIDs: []string{project.ID}})
		if err != nil {
			t.Fatal(err)
		}
		actor := studio.Actor{UserID: writer.ID, TokenID: full.ID, SiteRole: auth.RoleAdmin}
		for _, action := range []studio.Action{studio.ActRead, studio.ActEditScript, studio.ActGenerate} {
			if err := svc.Authorize(ctx, actor, project.ID, action); err != nil {
				t.Errorf("full token action %s: %v", action, err)
			}
		}
		if err := svc.Authorize(ctx, actor, other.ID, studio.ActRead); !errors.Is(err, studio.ErrForbidden) {
			t.Errorf("project-restricted token error = %v, want ErrForbidden", err)
		}

		_, writeOnly, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: writer.ID, Kind: auth.TokenAPI, Name: "write-only",
			Scopes: []string{"projects:write"}})
		if err != nil {
			t.Fatal(err)
		}
		writeActor := studio.Actor{UserID: writer.ID, TokenID: writeOnly.ID}
		if err := svc.Authorize(ctx, writeActor, project.ID, studio.ActRead); !errors.Is(err, studio.ErrForbidden) {
			t.Errorf("write token read error = %v, want ErrForbidden", err)
		}
		if err := svc.Authorize(ctx, writeActor, project.ID, studio.ActGenerate); !errors.Is(err, studio.ErrForbidden) {
			t.Errorf("write token generate error = %v, want ErrForbidden", err)
		}
		if err := svc.Authorize(ctx, writeActor, project.ID, studio.ActEditScript); err != nil {
			t.Errorf("write token edit: %v", err)
		}

		if err := ta.Deps.Auth.RevokeToken(ctx, writer.ID, full.ID, false); err != nil {
			t.Fatal(err)
		}
		if err := svc.Authorize(ctx, actor, project.ID, studio.ActRead); !errors.Is(err, studio.ErrForbidden) {
			t.Errorf("revoked token error = %v, want ErrForbidden", err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE users SET status = ? WHERE id = ?`, auth.StatusSuspended, writer.ID); err != nil {
			t.Fatal(err)
		}
		if err := svc.Authorize(ctx, studio.Actor{UserID: writer.ID, SiteRole: auth.RoleAdmin}, project.ID, studio.ActManage); !errors.Is(err, studio.ErrForbidden) {
			t.Errorf("suspended/site-role spoof error = %v, want ErrForbidden", err)
		}
	})
}
