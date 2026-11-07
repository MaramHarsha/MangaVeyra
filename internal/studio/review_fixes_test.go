package studio_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// Slug suffixes come from the ULID's random tail, so several projects with the
// same title created within minutes all succeed with distinct slugs.
func TestSameTitleProjectsGetDistinctSlugs(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		owner := ta.CreateUser(t, "", auth.RoleCreator)
		actor, err := svc.ActorFromUser(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for i := 0; i < 5; i++ {
			p, err := svc.CreateProject(ctx, actor, newProjectInput("Moonlit Harbor", ""))
			if err != nil {
				t.Fatalf("project %d with the same title: %v", i+1, err)
			}
			if seen[p.Slug] {
				t.Fatalf("duplicate slug %q", p.Slug)
			}
			seen[p.Slug] = true
		}
	})
}

// A site admin's narrow API token does not bypass project membership unless
// the token carries the admin scope.
func TestAdminTokenNeedsAdminScopeToBypassMembership(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		owner := ta.CreateUser(t, "", auth.RoleCreator)
		ownerActor, err := svc.ActorFromUser(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		project, err := svc.CreateProject(ctx, ownerActor, newProjectInput("Owner Only", ""))
		if err != nil {
			t.Fatal(err)
		}
		admin := ta.CreateUser(t, "", auth.RoleAdmin)
		_, narrow, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: admin.ID, Kind: auth.TokenAgent,
			Name: "narrow", Scopes: []string{"projects:read", "projects:write"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Authorize(ctx, studio.Actor{UserID: admin.ID, TokenID: narrow.ID}, project.ID, studio.ActEditScript); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("narrow admin token = %v, want ErrForbidden", err)
		}
		_, wide, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: admin.ID, Kind: auth.TokenAgent,
			Name: "admin", Scopes: []string{"projects:read", "projects:write", "admin"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Authorize(ctx, studio.Actor{UserID: admin.ID, TokenID: wide.ID}, project.ID, studio.ActRead); err != nil {
			t.Fatalf("admin-scoped token = %v, want access", err)
		}
		if err := svc.Authorize(ctx, studio.Actor{UserID: admin.ID}, project.ID, studio.ActEditScript); err != nil {
			t.Fatalf("admin session = %v, want access", err)
		}
	})
}
